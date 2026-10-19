package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed web/index.html
var defaultHtml []byte

// AirPlay Contact point
type AirPlayContact struct {
	ID   int     `json:"id"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Down bool    `json:"down"`
}

type TouchPayload struct {
	Contacts []AirPlayContact `json:"contacts"`
}

type ActionPayload struct {
	Action string `json:"action"`
}

type ResolutionPayload struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type HardwareStats struct {
	Cpu        float64 `json:"cpu"`
	Temp       float64 `json:"temp"`
	MemUsedMB  int     `json:"memUsed"`
	MemTotalMB int     `json:"memTotal"`
	Throttled  string  `json:"throttled,omitempty"`
}

type ServerStatus struct {
	Active        bool    `json:"active"`
	HasSession    bool    `json:"hasSession"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	Fps           int     `json:"fps"`
	StreamClients int     `json:"streamClients"`
	AudioClients  int     `json:"audioClients"`
	Cpu           float64 `json:"cpu"`
	Temp          float64 `json:"temp"`
	MemUsedMB     int     `json:"memUsed"`
	MemTotalMB    int     `json:"memTotal"`
	Throttled     string  `json:"throttled,omitempty"`
	// Network & Stream Diagnostics
	BitrateMbps   float64 `json:"bitrate"`
	QueueLen      int     `json:"queueLen"`
	DroppedFrames int64   `json:"droppedFrames"`
	WlanBand      string  `json:"wlanBand"`
	WlanChan      int     `json:"wlanChan"`
	WlanTxMbps    float64 `json:"wlanTx"`
	WlanRxMbps    float64 `json:"wlanRx"`
	WlanDropPkts  uint64  `json:"wlanDrop"`
}

type WifiNetwork struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Security string `json:"security"`
	Channel  int    `json:"channel"`
	Active   bool   `json:"active"`
	Saved    bool   `json:"saved"`
}

type WifiStatusResponse struct {
	OK         bool     `json:"ok"`
	Mode       string   `json:"mode"`        // "ap", "sta", "dual"
	SSID       string   `json:"ssid"`        // Current connected or AP SSID
	IP         string   `json:"ip"`          // Current primary local IP
	APIP       string   `json:"ap_ip"`       // Hotspot IP (100.96.0.1)
	APActive   bool     `json:"ap_active"`   // Is Tesla-CarPlay active?
	STAActive  bool     `json:"sta_active"`  // Is connected to an external WiFi?
	Interfaces []string `json:"interfaces"`  // Wireless interfaces, e.g. ["wlan0"]
	MultiRadio bool     `json:"multi_radio"` // Multi-radio hardware detected
	CanCoexist bool     `json:"can_coexist"` // Coexistence capability flag
	Channel    int      `json:"channel"`
	Band       string   `json:"band"` // "5GHz" or "2.4GHz"
	SavedCount int      `json:"saved_count"`
	SavedList  []string `json:"saved_list"`
}

type WifiConnectPayload struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

type WifiModePayload struct {
	Mode string `json:"mode"` // "ap", "sta"
	SSID string `json:"ssid,omitempty"`
}

// Thread-safe WebSocket Client wrapper with per-connection write mutex
type WsClient struct {
	conn       net.Conn
	sendQueue  chan []byte
	closed     int32
	remoteAddr string
}

func newWsClient(conn net.Conn) *WsClient {
	c := &WsClient{
		conn:       conn,
		sendQueue:  make(chan []byte, 32),
		remoteAddr: conn.RemoteAddr().String(),
	}
	go c.writePump()
	return c
}

func (c *WsClient) Close() error {
	if atomic.CompareAndSwapInt32(&c.closed, 0, 1) {
		close(c.sendQueue)
		return c.conn.Close()
	}
	return nil
}

func (c *WsClient) writePump() {
	defer c.conn.Close()
	for msg := range c.sendQueue {
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.conn.Write(msg); err != nil {
			atomic.StoreInt32(&c.closed, 1)
			return
		}
	}
}

func (c *WsClient) SendFrame(opcode byte, payload []byte) error {
	if atomic.LoadInt32(&c.closed) != 0 {
		return net.ErrClosed
	}
	frame := buildWsFrame(opcode, payload)
	select {
	case c.sendQueue <- frame:
		return nil
	default:
		return nil
	}
}

func (c *WsClient) SendMultiplex(channel byte, flag byte, data []byte) error {
	if atomic.LoadInt32(&c.closed) != 0 {
		return net.ErrClosed
	}
	frame := buildMultiplexFrame(channel, flag, data)
	select {
	case c.sendQueue <- frame:
		return nil
	default:
		return nil
	}
}

// Global streaming hub
type Hub struct {
	mu           sync.RWMutex
	wsClients    map[*WsClient]bool
	audioClients map[*WsClient]bool

	// Video Caching
	lastCodecData []byte
	annexBConfig  []byte
	lastKeyFrame  []byte
	lastJpegFrame []byte

	// Stats
	width      int
	height     int
	fpsCounter int64
	currentFps int32
	hasSession bool

	// Network & Video Stream Diagnostics (CarPlay map panning / high dynamic bitrate & queue telemetry)
	bytesCounter       int64
	lastBytesCount     int64
	currentBitrateMbps float64
	droppedFrames      int64
	wlanBand           string
	wlanChan           int
	wlanTxMbps         float64
	wlanRxMbps         float64
	lastWlanTxBytes    uint64
	lastWlanRxBytes    uint64
	lastWlanTime       time.Time
	wlanDropPkts       uint64

	// Channels
	frameChan chan []byte
	audioChan chan []byte
}

type ControlClient struct {
	addr        string
	mu          sync.Mutex
	conn        net.Conn
	lastLogTime time.Time
}

func NewControlClient(addr string) *ControlClient {
	c := &ControlClient{addr: addr}
	go c.maintainConnection()
	return c
}

func (c *ControlClient) maintainConnection() {
	for {
		c.mu.Lock()
		if c.conn == nil {
			conn, err := net.DialTimeout("tcp", c.addr, 2*time.Second)
			if err == nil {
				if tc, ok := conn.(*net.TCPConn); ok {
					tc.SetNoDelay(true)
				}
				c.conn = conn
				log.Printf("[ControlClient] >>> Successfully connected to CarPlay control bridge at %s <<<", c.addr)
			}
		}
		c.mu.Unlock()
		time.Sleep(2 * time.Second)
	}
}

func (c *ControlClient) Send(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		conn, err := net.DialTimeout("tcp", c.addr, 1*time.Second)
		if err != nil {
			return err
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
		}
		c.conn = conn
		log.Printf("[ControlClient] Reconnected to CarPlay control bridge at %s", c.addr)
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, err := c.conn.Write([]byte(line))
	if err != nil {
		c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *ControlClient) SendBytes(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		conn, err := net.DialTimeout("tcp", c.addr, 1*time.Second)
		if err != nil {
			return err
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
		}
		c.conn = conn
		log.Printf("[ControlClient] Reconnected to CarPlay control bridge at %s", c.addr)
	}
	_, err := c.conn.Write(data)
	if err != nil {
		c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

var hub = &Hub{
	wsClients:    make(map[*WsClient]bool),
	audioClients: make(map[*WsClient]bool),
	width:        1920,
	height:       1080,
	frameChan:    make(chan []byte, 120),
	audioChan:    make(chan []byte, 256),
	hasSession:   true,
	wlanBand:     "5GHz",
	wlanChan:     36,
}

var ctrlBridge *ControlClient

func (h *Hub) ResetStreamingState() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastCodecData = nil
	h.annexBConfig = nil
	for len(h.frameChan) > 0 {
		select {
		case <-h.frameChan:
		default:
		}
	}
	for len(h.audioChan) > 0 {
		select {
		case <-h.audioChan:
		default:
		}
	}
	log.Printf("[Hub] Streaming state reset: codec data & frame queue cleared.")
}

func main() {
	port := flag.Int("port", 8088, "HTTP/WebSocket service port")
	videoFeedPort := flag.Int("video-feed", 7001, "Raw H.264 TCP input port")
	audioFeedPort := flag.Int("audio-feed", 7002, "Raw PCM 44.1kHz 16bit TCP input port")
	controlPort := flag.Int("control-port", 7003, "CarPlay AirPlay Bridge control TCP port")
	flag.Parse()

	ctrlBridge = NewControlClient(fmt.Sprintf("127.0.0.1:%d", *controlPort))

	if resBytes, err := os.ReadFile("/opt/diplay/auth/resolution.conf"); err == nil {
		parts := strings.FieldsFunc(strings.TrimSpace(string(resBytes)), func(r rune) bool {
			return r == 'x' || r == ',' || r == ' '
		})
		if len(parts) >= 2 {
			var w, h int
			fmt.Sscanf(parts[0], "%d", &w)
			fmt.Sscanf(parts[1], "%d", &h)
			if w > 0 && h > 0 {
				hub.width = w
				hub.height = h
				log.Printf("[DiPlay-Pi] Loaded saved default resolution: %dx%d", w, h)
			}
		}
	}

	log.Printf("[DiPlay-Pi] Starting Linux Native Headless CarPlay Server on port %d...", *port)

	// Background FPS counter tick & idle auto-refresh
	go func() {
		ticker := time.NewTicker(time.Second)
		idleSec := 0
		for range ticker.C {
			frames := atomic.SwapInt64(&hub.fpsCounter, 0)
			atomic.StoreInt32(&hub.currentFps, int32(frames))
			if frames == 0 {
				idleSec++
				hub.mu.RLock()
				clientCount := len(hub.wsClients)
				hub.mu.RUnlock()
				if clientCount > 0 && idleSec >= 15 {
					idleSec = 0
				}
			} else {
				idleSec = 0
			}
		}
	}()

	// Background Hardware Telemetry & Throttling Diagnostics (Pi 3B+)
	go func() {
		// Prime initial CPU counters
		updateHardwareMetrics()
		time.Sleep(500 * time.Millisecond)
		updateHardwareMetrics()

		hwTicker := time.NewTicker(2 * time.Second)
		logCount := 0
		for range hwTicker.C {
			stats := updateHardwareMetrics()
			logCount++

			// 1. Calculate incoming video bitrate (Mbps)
			curBytes := atomic.LoadInt64(&hub.bytesCounter)
			deltaBytes := curBytes - hub.lastBytesCount
			hub.lastBytesCount = curBytes
			bitrate := math.Round((float64(deltaBytes)*8.0/(1024*1024*2.0))*10) / 10
			hub.currentBitrateMbps = bitrate

			// 2. Query wlan0 throughput and packets from /proc/net/dev
			rxB, txB, drops, err := readNetDevStats("wlan0")
			if err == nil {
				now := time.Now()
				if !hub.lastWlanTime.IsZero() {
					dt := now.Sub(hub.lastWlanTime).Seconds()
					if dt > 0 && txB >= hub.lastWlanTxBytes && rxB >= hub.lastWlanRxBytes {
						hub.wlanTxMbps = math.Round((float64(txB-hub.lastWlanTxBytes)*8.0/(1024*1024*dt))*10) / 10
						hub.wlanRxMbps = math.Round((float64(rxB-hub.lastWlanRxBytes)*8.0/(1024*1024*dt))*10) / 10
					}
				}
				hub.lastWlanTxBytes = txB
				hub.lastWlanRxBytes = rxB
				hub.lastWlanTime = now
				hub.wlanDropPkts = drops
			}

			// 3. Query true Wi-Fi channel and band periodically (every 10s)
			if logCount%5 == 1 || hub.wlanBand == "" {
				ch, band := getRealWlanChannel()
				hub.wlanChan = ch
				hub.wlanBand = band
			}

			// Broadcast hardware stats and network diagnostics to connected WebSocket clients
			hub.mu.RLock()
			clientCount := len(hub.wsClients)
			fps := atomic.LoadInt32(&hub.currentFps)
			hasSession := hub.hasSession
			clients := make([]*WsClient, 0, clientCount)
			for c := range hub.wsClients {
				clients = append(clients, c)
			}
			hub.mu.RUnlock()

			queueLen := len(hub.frameChan)
			droppedFrames := atomic.LoadInt64(&hub.droppedFrames)

			if clientCount > 0 {
				hwJson := fmt.Sprintf(`{"type":"hardware","cpu":%.1f,"temp":%.1f,"memUsed":%d,"memTotal":%d,"bitrate":%.1f,"fps":%d,"queueLen":%d,"droppedFrames":%d,"wlanBand":"%s","wlanChan":%d,"wlanTx":%.1f,"wlanRx":%.1f,"wlanDrop":%d}`,
					stats.Cpu, stats.Temp, stats.MemUsedMB, stats.MemTotalMB,
					bitrate, fps, queueLen, droppedFrames,
					hub.wlanBand, hub.wlanChan, hub.wlanTxMbps, hub.wlanRxMbps, hub.wlanDropPkts)
				for _, c := range clients {
					_ = c.SendFrame(0x01, []byte(hwJson))
				}
			}

			// Periodic telemetry logging to syslog / journalctl every 10 seconds
			if logCount%5 == 0 {
				memPercent := 0
				if stats.MemTotalMB > 0 {
					memPercent = (stats.MemUsedMB * 100) / stats.MemTotalMB
				}
				warnTag := ""
				if stats.Temp >= 75.0 {
					warnTag = " ⚠️ [高温超标 75°C+，树莓派硬件已触发降频限速]"
				} else if stats.Temp >= 68.0 {
					warnTag = " ⚠️ [温度偏高，接近降频门限]"
				}
				if stats.Cpu >= 90.0 {
					warnTag += " ⚠️ [CPU占用过高 90%+]"
				}

				warnDiag := ""
				if hub.wlanBand == "2.4GHz" {
					warnDiag += " ⚠️ [Wi-Fi 2.4GHz 频宽受限，划动地图易卡死，建议切换 5GHz]"
				}
				if droppedFrames > 0 {
					warnDiag += fmt.Sprintf(" 🚨 [累计丢帧: %d 帧! 网络下行发送阻塞]", droppedFrames)
				} else if queueLen > 20 {
					warnDiag += fmt.Sprintf(" ⚠️ [发送队列积压 %d/120 帧]", queueLen)
				} else if bitrate > 15.0 {
					warnDiag += " ⚡ [高动态大码率: 地图缩放/拖动中]"
				}

				if hasSession || clientCount > 0 || warnTag != "" || warnDiag != "" {
					log.Printf("[DiPlay-Diag] 📡 视频码率: %5.1f Mbps | 帧率: %2d FPS | 队列缓冲: %2d/120 (丢帧: %d) | Wi-Fi: %s CH%d (↑%.1f ↓%.1f Mbps) | CPU: %4.1f%% · %4.1f°C | 内存: %d%% (%d/%dMB)%s%s",
						bitrate, fps, queueLen, droppedFrames, hub.wlanBand, hub.wlanChan, hub.wlanTxMbps, hub.wlanRxMbps, stats.Cpu, stats.Temp, memPercent, stats.MemUsedMB, stats.MemTotalMB, warnTag, warnDiag)
				}
			}
		}
	}()

	// Start Local TCP Feed Listeners (receives raw H.264 and PCM from AirPlay pipeline/NCM)
	go startVideoFeedServer(*videoFeedPort)
	go startAudioFeedServer(*audioFeedPort)

	// Dispatcher goroutines
	go hub.runBroadcaster()

	// HTTP API routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/ws", handleWs)
	mux.HandleFunc("/audio_ws", handleAudioWs)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/touch", handleTouch)
	mux.HandleFunc("/api/action", handleAction)
	mux.HandleFunc("/api/reconnect", handleReconnect)
	mux.HandleFunc("/api/soft_reset", handleSoftReset)
	mux.HandleFunc("/api/calib", handleCalib)
	mux.HandleFunc("/api/resolution", handleResolution)
	mux.HandleFunc("/api/wifi/status", handleWifiStatus)
	mux.HandleFunc("/api/wifi/scan", handleWifiScan)
	mux.HandleFunc("/api/wifi/connect", handleWifiConnect)
	mux.HandleFunc("/api/wifi/mode", handleWifiMode)
	mux.HandleFunc("/api/terminal/exec", handleTerminalExec)
	mux.HandleFunc("/api/terminal/logs", handleTerminalLogs)
	mux.HandleFunc("/api/terminal/export", handleTerminalExport)
	mux.HandleFunc("/api/terminal/download", handleTerminalDownload)
	mux.HandleFunc("/api/video_log/save", handleVideoLogSave)
	mux.HandleFunc("/api/video_log/latest.txt", handleVideoLogLatest)
	mux.HandleFunc("/api/system/upgrade", handleSystemUpgrade)
	mux.HandleFunc("/api/teslacam/status", handleTeslaCamStatus)
	mux.HandleFunc("/api/teslacam/events", handleTeslaCamEvents)
	mux.HandleFunc("/api/teslacam/stream", handleTeslaCamStream)
	mux.HandleFunc("/api/teslacam/download_zip", handleTeslaCamDownloadZip)
	mux.HandleFunc("/api/teslacam/refresh", handleTeslaCamRefresh)
	mux.HandleFunc("/snapshot", handleSnapshot)
	mux.HandleFunc("/stream", handleSnapshot)

	// Captive Portal & Tesla Connectivity Check Bypass Routes (Fake 204 & Success)
	mux.HandleFunc("/generate_204", handleCaptivePortal204)
	mux.HandleFunc("/gen_204", handleCaptivePortal204)
	mux.HandleFunc("/hotspot-detect.html", handleCaptivePortalSuccess)
	mux.HandleFunc("/canonical.html", handleCaptivePortalSuccess)
	mux.HandleFunc("/ncsi.txt", handleNcsi)
	mux.HandleFunc("/connecttest.txt", handleConnectTest)
	mux.HandleFunc("/success.txt", handleSuccessText)

	server := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", *port),
		Handler: corsMiddleware(mux),
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[DiPlay-Pi] HTTP Server fatal error: %v", err)
		}
	}()

	// Start HTTPS server on port 8443 (enables secure context for WebCodecs)
	go func() {
		tlsCert, err := generateSelfSignedCert()
		if err != nil {
			log.Printf("[DiPlay-Pi] Failed to generate TLS cert: %v", err)
			return
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		}
		httpsServer := &http.Server{
			Addr:      "0.0.0.0:8443",
			Handler:   corsMiddleware(mux),
			TLSConfig: tlsConfig,
		}
		log.Printf("[DiPlay-Pi] HTTPS Web Remote ready on https://<IP>:8443")
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("[DiPlay-Pi] HTTPS Server error: %v", err)
		}
	}()

	// Optionally start direct port 80 listener for Tesla convenience if run as root
	if *port != 80 {
		go func() {
			p80Server := &http.Server{
				Addr:    "0.0.0.0:80",
				Handler: corsMiddleware(mux),
			}
			if err := p80Server.ListenAndServe(); err != nil {
				log.Printf("[DiPlay-Pi] Port 80 direct binding not available (requires root/sudo or iptables redirect): %v", err)
			}
		}()
	}

	// Embedded Fake-DNS Server on UDP 53 (Tesla China offline resolution)
	go startEmbeddedDNSServer()


	log.Printf("[DiPlay-Pi] Web Remote ready! Open browser at http://<RaspberryPi_IP>:%d or https://<RaspberryPi_IP>:8443", *port)


	// Graceful shutdown on Ctrl+C / SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("[DiPlay-Pi] Shutting down server...")
	server.Close()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if strings.Contains(host, ":") {
			host, _, _ = net.SplitHostPort(host)
		}
		host = strings.TrimSuffix(host, ".")

		log.Printf("[HTTP] %s %s (Host: %s, From: %s)", r.Method, r.URL.RequestURI(), r.Host, r.RemoteAddr)

		// 1. Tesla China / ConnMan Offline Connectivity Checks (100% 1:1 Replica of Tesla Cloud)
		if host == "connman.vn.cloud.tesla.cn" || strings.Contains(r.URL.Path, "status.html") {
			// CRITICAL: Go's net/http automatically converts "X-ConnMan-Status" to "X-Connman-Status"
			// (lowercase 'm') via CanonicalMIMEHeaderKey. But ConnMan's GWeb parser uses GLib g_str_equal,
			// which is STRICTLY CASE-SENSITIVE! If 'M' is lowercase, ConnMan fails to find it and triggers
			// the "Captive Portal Login" popup on Tesla!
			// We use http.Hijacker to write raw bytes directly to TCP socket, preserving exact case!
			if hj, ok := w.(http.Hijacker); ok {
				conn, bufrw, err := hj.Hijack()
				if err == nil {
					defer conn.Close()
					rawHTTP := "HTTP/1.1 200 OK\r\n" +
						"Content-Type: text/html\r\n" +
						"Content-Length: 46\r\n" +
						"Connection: keep-alive\r\n" +
						"Server: AmazonS3\r\n" +
						"X-ConnMan-Status: online\r\n\r\n" +
						"<html>\n<head>\n</head>\n<body>\n</body>\n</html>\n\n"
					_, _ = bufrw.WriteString(rawHTTP)
					_ = bufrw.Flush()
					log.Printf("[ConnMan-Hijack] Sent 100%% exact raw X-ConnMan-Status: online to %s", r.RemoteAddr)
					return
				}
			}

			body := []byte("<html>\n<head>\n</head>\n<body>\n</body>\n</html>\n\n")
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Length", "46")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("Server", "AmazonS3")
			w.Header()["X-ConnMan-Status"] = []string{"online"}
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return
		}

		if host == "www.baidu.cn" || host == "www.qq.com" || host == "www.tesla.cn" {
			body := []byte("<html><body>online</body></html>\n")
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return
		}


		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleCaptivePortal204(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func handleCaptivePortalSuccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>"))
}

func handleNcsi(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Microsoft NCSI"))
}

func handleConnectTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Microsoft Connect Test"))
}

func handleSuccessText(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("success\n"))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if strings.Contains(host, ":") {
		host, _, _ = net.SplitHostPort(host)
	}
	host = strings.TrimSuffix(host, ".")
	path := strings.ToLower(r.URL.Path)

	// 1. Any path containing 204 immediately gets 204 No Content
	if strings.Contains(path, "204") {
		handleCaptivePortal204(w, r)
		return
	}
	if strings.Contains(path, "hotspot-detect") || strings.Contains(path, "canonical") {
		handleCaptivePortalSuccess(w, r)
		return
	}
	if strings.Contains(path, "ncsi") {
		handleNcsi(w, r)
		return
	}

	// 2. Distinguish between actual browser opening CarPlay web page vs OS connectivity probe
	isLocalCarPlayHost := host == "100.96.0.1" || host == "carplay" || host == "tesla.carplay" || host == "diplay.com" || host == "dashplay.lan" || host == "localhost" || host == "127.0.0.1"

	// Critical: Never return full HTML web page to non-local probe hosts, otherwise Tesla OS
	// misclassifies it as a Captive Portal (login page) and drops the Wi-Fi connection!
	if !isLocalCarPlayHost {
		w.WriteHeader(http.StatusNoContent) // 204 No Content: proves internet is directly online
		return
	}

	// 3. Static Web Assets (libffmpeg_264.js, libffmpeg_264.wasm, ffmpeg_webgl.js, broadway.js)
	cleanPath := strings.TrimPrefix(path, "/")
	cleanPath = strings.TrimPrefix(cleanPath, "web/")

	if cleanPath == "libffmpeg_264.js" || cleanPath == "ffmpeg_webgl.js" || cleanPath == "broadway.js" {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if data, err := os.ReadFile("/opt/diplay/web/" + cleanPath); err == nil && len(data) > 0 {
			w.Write(data)
			return
		}
		if data, err := os.ReadFile("web/" + cleanPath); err == nil && len(data) > 0 {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
		return
	}

	if cleanPath == "libffmpeg_264.wasm" {
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if data, err := os.ReadFile("/opt/diplay/web/" + cleanPath); err == nil && len(data) > 0 {
			w.Write(data)
			return
		}
		if data, err := os.ReadFile("web/" + cleanPath); err == nil && len(data) > 0 {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
		return
	}

	if path != "/" && path != "/index.html" && path != "/favicon.ico" {
		http.NotFound(w, r)
		return
	}

	if strings.Contains(r.RemoteAddr, "100.96.0.") || strings.Contains(r.RemoteAddr, "192.168.43.") {
		if f, err := os.Create("/tmp/ap_confirmed"); err == nil {
			f.Close()
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if data, err := os.ReadFile("/opt/diplay/web/index.html"); err == nil && len(data) > 0 {
		w.Write(data)
		return
	}
	w.Write(defaultHtml)
}


var (
	hwMu          sync.RWMutex
	lastCpuTotal  uint64
	lastCpuIdle   uint64
	cachedHwStats HardwareStats
)

func readCpuTimes() (uint64, uint64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 4 && fields[0] == "cpu" {
			var sum uint64
			for i := 1; i < len(fields); i++ {
				v, _ := strconv.ParseUint(fields[i], 10, 64)
				sum += v
			}
			idleVal, _ := strconv.ParseUint(fields[4], 10, 64)
			var iowaitVal uint64
			if len(fields) > 5 {
				iowaitVal, _ = strconv.ParseUint(fields[5], 10, 64)
			}
			return sum, idleVal + iowaitVal, nil
		}
	}
	return 0, 0, fmt.Errorf("cpu line not found")
}

func updateHardwareMetrics() HardwareStats {
	var stats HardwareStats

	// 1. CPU Usage delta (/proc/stat)
	total, idle, err := readCpuTimes()
	if err == nil {
		hwMu.Lock()
		if lastCpuTotal > 0 && total > lastCpuTotal {
			deltaTotal := float64(total - lastCpuTotal)
			deltaIdle := float64(idle - lastCpuIdle)
			if deltaTotal > 0 {
				usage := (1.0 - (deltaIdle / deltaTotal)) * 100.0
				if usage < 0 {
					usage = 0
				} else if usage > 100 {
					usage = 100
				}
				stats.Cpu = math.Round(usage*10) / 10
			}
		}
		lastCpuTotal = total
		lastCpuIdle = idle
		hwMu.Unlock()
	}

	// 2. CPU Temperature (Raspberry Pi thermal zone 0)
	if data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		tStr := strings.TrimSpace(string(data))
		if millideg, err := strconv.ParseFloat(tStr, 64); err == nil {
			stats.Temp = math.Round((millideg/1000.0)*10) / 10
		}
	}

	// 3. RAM Usage (/proc/meminfo)
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var memTotalKB, memAvailKB uint64
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if fields[0] == "MemTotal:" {
					memTotalKB, _ = strconv.ParseUint(fields[1], 10, 64)
				} else if fields[0] == "MemAvailable:" {
					memAvailKB, _ = strconv.ParseUint(fields[1], 10, 64)
				}
			}
		}
		if memTotalKB > 0 {
			stats.MemTotalMB = int(memTotalKB / 1024)
			if memTotalKB >= memAvailKB {
				stats.MemUsedMB = int((memTotalKB - memAvailKB) / 1024)
			}
		}
	}

	// 4. Throttling status (/sys/devices/platform/soc/soc:firmware/get_throttled)
	if data, err := os.ReadFile("/sys/devices/platform/soc/soc:firmware/get_throttled"); err == nil {
		stats.Throttled = strings.TrimSpace(string(data))
	}

	hwMu.Lock()
	if stats.Cpu == 0 && cachedHwStats.Cpu > 0 {
		stats.Cpu = cachedHwStats.Cpu
	}
	cachedHwStats = stats
	hwMu.Unlock()

	return stats
}

func getHardwareStats() HardwareStats {
	hwMu.RLock()
	defer hwMu.RUnlock()
	return cachedHwStats
}

// readNetDevStats parses /proc/net/dev to get interface RX/TX bytes and drop counters
func readNetDevStats(targetIface string) (rxBytes, txBytes, drops uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, 0, err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, targetIface+":") || strings.Contains(line, targetIface+":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				fields := strings.Fields(parts[1])
				if len(fields) >= 12 {
					rxB, _ := strconv.ParseUint(fields[0], 10, 64)
					rxD, _ := strconv.ParseUint(fields[3], 10, 64)
					txB, _ := strconv.ParseUint(fields[8], 10, 64)
					txD, _ := strconv.ParseUint(fields[11], 10, 64)
					return rxB, txB, rxD + txD, nil
				}
			}
		}
	}
	return 0, 0, 0, fmt.Errorf("interface %s not found in /proc/net/dev", targetIface)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	hub.mu.RLock()
	st := ServerStatus{
		Active:        len(hub.wsClients) > 0,
		HasSession:    hub.hasSession,
		Width:         hub.width,
		Height:        hub.height,
		Fps:           int(atomic.LoadInt32(&hub.currentFps)),
		StreamClients: len(hub.wsClients),
		AudioClients:  len(hub.audioClients),
	}
	hub.mu.RUnlock()

	hw := getHardwareStats()
	st.Cpu = hw.Cpu
	st.Temp = hw.Temp
	st.MemUsedMB = hw.MemUsedMB
	st.MemTotalMB = hw.MemTotalMB
	st.Throttled = hw.Throttled

	// Network & Video Stream Diagnostics
	st.BitrateMbps = hub.currentBitrateMbps
	st.QueueLen = len(hub.frameChan)
	st.DroppedFrames = atomic.LoadInt64(&hub.droppedFrames)
	st.WlanBand = hub.wlanBand
	st.WlanChan = hub.wlanChan
	st.WlanTxMbps = hub.wlanTxMbps
	st.WlanRxMbps = hub.wlanRxMbps
	st.WlanDropPkts = hub.wlanDropPkts

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

func handleTouch(w http.ResponseWriter, r *http.Request) {
	var payload TouchPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type":     "touch",
		"contacts": payload.Contacts,
	})
	if ctrlBridge != nil {
		go ctrlBridge.Send(string(data))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func handleAction(w http.ResponseWriter, r *http.Request) {
	var payload ActionPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("[Action] Executing CarPlay action: %s", payload.Action)
	data, _ := json.Marshal(map[string]interface{}{
		"type":   "action",
		"action": payload.Action,
	})
	if ctrlBridge != nil {
		go ctrlBridge.Send(string(data))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func executeSoftReset() {
	log.Printf("[SoftReset] Starting complete 0-state streaming & protocol stack soft reset...")
	hub.ResetStreamingState()

	go func() {
		scriptPath := "/opt/diplay/scripts/soft_reset.sh"
		if _, err := os.Stat(scriptPath); err == nil {
			log.Printf("[SoftReset] Executing %s...", scriptPath)
			cmd := exec.Command("sudo", "bash", scriptPath)
			out, err := cmd.CombinedOutput()
			log.Printf("[SoftReset] Script finished: %s (err: %v)", strings.TrimSpace(string(out)), err)
			return
		}

		// Fallback pure-Go execution in case script is not yet deployed
		log.Printf("[SoftReset] Running fallback systemd & hardware reset sequence...")
		_ = exec.Command("sudo", "systemctl", "stop", "diplay-airplay", "diplay-iap2", "diplay-bt").Run()
		_ = exec.Command("sh", "-c", "fuser -k 7000/tcp 7001/tcp 7002/tcp 7003/tcp 2>/dev/null || true").Run()
		_ = os.WriteFile("/run/diplay_airplay_state", []byte("IDLE\n"), 0644)
		_ = os.Remove("/run/diplay_bt_state.json")
		_ = exec.Command("sudo", "hciconfig", "hci0", "up").Run()
		_ = exec.Command("sudo", "hciconfig", "hci0", "class", "0x240408").Run()
		_ = exec.Command("sudo", "hciconfig", "hci0", "name", "Tesla-CarPlay").Run()
		_ = exec.Command("sudo", "hciconfig", "hci0", "sspmode", "1").Run()
		_ = exec.Command("sudo", "hciconfig", "hci0", "piscan").Run()
		_ = exec.Command("sudo", "systemctl", "start", "diplay-bt").Run()
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("sudo", "systemctl", "start", "diplay-airplay").Run()
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("sudo", "systemctl", "start", "diplay-iap2").Run()
		_ = os.WriteFile("/tmp/trigger_iap2_reconnect", []byte("1"), 0644)
		log.Printf("[SoftReset] Fallback soft reset complete!")
	}()
}

func handleSoftReset(w http.ResponseWriter, r *http.Request) {
	executeSoftReset()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"action":"soft_reset_dispatched"}`))
}

func handleReconnect(w http.ResponseWriter, r *http.Request) {
	restart := r.URL.Query().Get("restart") == "true"
	if restart {
		log.Printf("[Action] Requesting CarPlay session restart for resolution renegotiation (keeping Bluetooth intact)")
		if ctrlBridge != nil {
			ctrlBridge.Send(`{"type":"restart_session"}`)
		}
		_ = os.WriteFile("/tmp/trigger_iap2_reconnect", []byte("1"), 0644)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"action":"session_restart_dispatched"}`))
		return
	}

	log.Printf("[Action] Executing CarPlay Reconnect / Auto-Wake via Bluetooth")
	if ctrlBridge != nil {
		ctrlBridge.Send(`{"type":"reconnect"}`)
	}
	_ = os.WriteFile("/tmp/trigger_iap2_reconnect", []byte("1"), 0644)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"action":"reconnect_dispatched"}`))
}

func handleCalib(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	if action == "" {
		action = "start"
	}
	w.Header().Set("Content-Type", "application/json")

	switch action {
	case "toggle":
		log.Printf("[Calib] Triggering Orientation Toggle")
		_ = os.WriteFile("/tmp/trigger_toggle_orientation", []byte("1"), 0644)
		w.Write([]byte(`{"ok":true,"action":"toggle_orientation"}`))
	case "reset":
		log.Printf("[Calib] Triggering Reset Golden Calibration")
		_ = os.WriteFile("/tmp/trigger_reset_golden", []byte("1"), 0644)
		w.Write([]byte(`{"ok":true,"action":"reset_golden"}`))
	case "exit":
		log.Printf("[Calib] Triggering Exit Calibration")
		_ = os.WriteFile("/tmp/trigger_exit_calib", []byte("1"), 0644)
		w.Write([]byte(`{"ok":true,"action":"exit_calib"}`))
	case "status":
		data, err := os.ReadFile("/tmp/diplay_touch_status.json")
		if err != nil {
			w.Write([]byte(`{"ok":false,"error":"status not ready"}`))
			return
		}
		w.Write(data)
	default: // "start"
		log.Printf("[Calib] Triggering Touchscreen Calibration Wizard")
		_ = os.WriteFile("/tmp/trigger_calib", []byte("1"), 0644)
		w.Write([]byte(`{"ok":true,"action":"calibration_triggered"}`))
	}
}

func applyResolution(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	hub.mu.Lock()
	hub.width = width
	hub.height = height
	hub.mu.Unlock()
	log.Printf("[Resolution] Applied display resolution: %dx%d", width, height)

	// 1. Notify Java AirPlay bridge over control port 7003
	if ctrlBridge != nil {
		msg, _ := json.Marshal(map[string]interface{}{
			"type":   "resolution",
			"width":  width,
			"height": height,
		})
		ctrlBridge.Send(string(msg))
	}

	// 2. Persist to /opt/diplay/auth/resolution.conf
	_ = os.WriteFile("/opt/diplay/auth/resolution.conf", []byte(fmt.Sprintf("%dx%d", width, height)), 0644)

	// 3. Broadcast resolution update to all connected Web clients
	notice := fmt.Sprintf(`{"type":"resolution_updated","width":%d,"height":%d}`, width, height)
	broadcastWsText(notice)
}

func broadcastWsText(msg string) {
	hub.mu.RLock()
	clients := make([]*WsClient, 0, len(hub.wsClients))
	for c := range hub.wsClients {
		clients = append(clients, c)
	}
	hub.mu.RUnlock()

	data := []byte(msg)
	for _, c := range clients {
		_ = c.SendFrame(0x01, data)
	}
}

func handleResolution(w http.ResponseWriter, r *http.Request) {
	var payload ResolutionPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Width > 0 && payload.Height > 0 {
		applyResolution(payload.Width, payload.Height)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func getPrimaryIP() (string, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ip := ipNet.IP.String()
				if !strings.HasPrefix(ip, "100.96.0.") && !strings.HasPrefix(ip, "192.168.43.") && (strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "172.")) {
					return ip, true
				}
			}
		}
	}
	return "", false
}

func getWifiInterfaces() []string {
	out, err := exec.Command("nmcli", "-t", "-f", "DEVICE,TYPE", "device").CombinedOutput()
	if err != nil {
		return []string{"wlan0"}
	}
	var ifaces []string
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		parts := strings.Split(strings.TrimSpace(l), ":")
		if len(parts) >= 2 && (parts[1] == "wifi" || parts[1] == "802-11-wireless") {
			ifaces = append(ifaces, parts[0])
		}
	}
	if len(ifaces) == 0 {
		ifaces = append(ifaces, "wlan0")
	}
	return ifaces
}

func getSavedWifiList() []string {
	out, err := exec.Command("nmcli", "-t", "-f", "NAME,TYPE", "connection", "show").CombinedOutput()
	if err != nil {
		return nil
	}
	var list []string
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		parts := strings.Split(strings.TrimSpace(l), ":")
		if len(parts) >= 2 && (parts[1] == "802-11-wireless" || parts[1] == "wifi") {
			name := parts[0]
			if name != "Tesla-CarPlay" && name != "" {
				list = append(list, name)
			}
		}
	}
	return list
}

func getWifiStatus() WifiStatusResponse {
	resp := WifiStatusResponse{
		OK:         true,
		APIP:       "100.96.0.1",
		Interfaces: getWifiInterfaces(),
		SavedList:  getSavedWifiList(),
	}
	resp.SavedCount = len(resp.SavedList)
	resp.MultiRadio = len(resp.Interfaces) >= 2
	resp.CanCoexist = resp.MultiRadio

	// Check active connections
	out, err := exec.Command("nmcli", "-t", "-f", "NAME,TYPE,DEVICE,STATE", "connection", "show", "--active").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, l := range lines {
			parts := strings.Split(strings.TrimSpace(l), ":")
			if len(parts) >= 2 {
				name := parts[0]
				connType := parts[1]
				if connType == "802-11-wireless" || connType == "wifi" {
					if name == "Tesla-CarPlay" {
						resp.APActive = true
					} else {
						resp.STAActive = true
						resp.SSID = name
					}
				}
			}
		}
	}

	// Determine local primary IP
	clientIP, hasClientIP := getPrimaryIP()
	if hasClientIP {
		resp.IP = clientIP
	} else if resp.APActive {
		resp.IP = "100.96.0.1"
	} else {
		resp.IP = "未获取到IP"
	}

	// Query true hardware channel and band
	realChan, realBand := getRealWlanChannel()
	resp.Channel = realChan
	resp.Band = realBand

	if resp.APActive && resp.STAActive {
		resp.Mode = "dual"
	} else if resp.APActive {
		resp.Mode = "ap"
		if resp.SSID == "" {
			resp.SSID = "Tesla-CarPlay"
		}
	} else {
		resp.Mode = "sta"
	}

	return resp
}

func getRealWlanChannel() (int, string) {
	out, err := exec.Command("iw", "dev", "wlan0", "info").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "channel ") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if ch, e := strconv.Atoi(fields[1]); e == nil {
						band := "2.4GHz"
						if ch >= 36 {
							band = "5GHz"
						}
						return ch, band
					}
				}
			}
		}
	}
	return 11, "2.4GHz"
}

func handleWifiStatus(w http.ResponseWriter, r *http.Request) {
	st := getWifiStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

func handleWifiScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Trigger rescan
	out, err := exec.Command("nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY,CHAN", "dev", "wifi", "list", "--rescan", "yes").CombinedOutput()
	if err != nil {
		out, _ = exec.Command("nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY,CHAN", "dev", "wifi", "list").CombinedOutput()
	}

	savedSet := make(map[string]bool)
	for _, s := range getSavedWifiList() {
		savedSet[s] = true
	}

	networkMap := make(map[string]WifiNetwork)
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		parts := strings.Split(l, ":")
		if len(parts) >= 5 {
			inUse := parts[0] == "*"
			ssid := strings.TrimSpace(parts[1])
			if ssid == "" || ssid == "--" {
				continue
			}
			sig, _ := strconv.Atoi(parts[2])
			sec := parts[3]
			if sec == "" {
				sec = "Open"
			}
			chanNum, _ := strconv.Atoi(parts[4])

			existing, exists := networkMap[ssid]
			if !exists || sig > existing.Signal {
				networkMap[ssid] = WifiNetwork{
					SSID:     ssid,
					Signal:   sig,
					Security: sec,
					Channel:  chanNum,
					Active:   inUse,
					Saved:    savedSet[ssid],
				}
			}
		}
	}

	var results []WifiNetwork
	for _, n := range networkMap {
		results = append(results, n)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Active != results[j].Active {
			return results[i].Active
		}
		return results[i].Signal > results[j].Signal
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       true,
		"networks": results,
		"count":    len(results),
	})
}

func handleWifiConnect(w http.ResponseWriter, r *http.Request) {
	var payload WifiConnectPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.SSID) == "" {
		http.Error(w, "invalid ssid", http.StatusBadRequest)
		return
	}
	targetSSID := strings.TrimSpace(payload.SSID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"message": fmt.Sprintf("正在尝试连接 Wi-Fi '%s'，若失败系统将在 20 秒后自动恢复 Tesla-CarPlay 车载热点", targetSSID),
		"ssid":    targetSSID,
	})

	// Run connect worker with Safety Watchdog
	go func(target, password string) {
		log.Printf("[WiFi-Watchdog] Starting connection attempt to '%s'...", target)

		// 1. Lower Tesla-CarPlay priority temporarily to allow client WiFi to take over
		_ = exec.Command("sudo", "nmcli", "connection", "modify", "Tesla-CarPlay", "connection.autoconnect-priority", "1").Run()

		var cmd *exec.Cmd
		if password != "" {
			cmd = exec.Command("sudo", "nmcli", "dev", "wifi", "connect", target, "password", password)
		} else {
			cmd = exec.Command("sudo", "nmcli", "connection", "up", target)
		}

		out, err := cmd.CombinedOutput()
		log.Printf("[WiFi-Watchdog] nmcli connect output: %s, err: %v", string(out), err)

		connected := false
		if err == nil {
			// Check if we acquire a non-hotspot IP within 15 seconds
			for i := 0; i < 15; i++ {
				time.Sleep(1 * time.Second)
				if ip, ok := getPrimaryIP(); ok && ip != "" {
					connected = true
					log.Printf("[WiFi-Watchdog] Connection successful! Acquired IP: %s", ip)
					// Set high priority for this WiFi
					_ = exec.Command("sudo", "nmcli", "connection", "modify", target, "connection.autoconnect-priority", "500").Run()
					// Restart services so they bind properly to the new IP
					_ = exec.Command("sudo", "systemctl", "restart", "diplay-airplay", "diplay-pi").Run()
					break
				}
			}
		}

		// 2. Safety Rollback: If failed or no IP, restore Tesla-CarPlay immediately!
		if !connected {
			log.Printf("[WiFi-Watchdog] Connection to '%s' failed. Safety Rollback: Restoring Tesla-CarPlay hotspot...", target)
			_ = exec.Command("sudo", "nmcli", "connection", "modify", "Tesla-CarPlay", "connection.autoconnect-priority", "999").Run()
			_ = exec.Command("sudo", "nmcli", "connection", "up", "Tesla-CarPlay").Run()
			time.Sleep(2 * time.Second)
			_ = exec.Command("sudo", "systemctl", "restart", "diplay-airplay", "diplay-pi").Run()
		}
	}(targetSSID, payload.Password)
}

func handleWifiMode(w http.ResponseWriter, r *http.Request) {
	var payload WifiModePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if payload.Mode == "ap" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":      true,
			"message": "正在切换至 5GHz 车载热点 (Tesla-CarPlay)... 设备 IP 将变更为 100.96.0.1",
		})
		go func() {
			log.Printf("[WiFi] Switching to Car Hotspot (Tesla-CarPlay)...")
			_ = exec.Command("sudo", "nmcli", "connection", "modify", "Tesla-CarPlay", "connection.autoconnect-priority", "999").Run()
			_ = exec.Command("sudo", "nmcli", "connection", "up", "Tesla-CarPlay").Run()
			time.Sleep(2 * time.Second)
			_ = exec.Command("sudo", "systemctl", "restart", "diplay-airplay", "diplay-pi").Run()
		}()
		return
	}

	if payload.Mode == "sta" {
		targetSSID := payload.SSID
		if targetSSID == "" {
			saved := getSavedWifiList()
			if len(saved) > 0 {
				targetSSID = saved[0]
			}
		}
		if targetSSID == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":    false,
				"error": "未发现已保存的外部 Wi-Fi，请先在列表中扫描并连接",
			})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":      true,
			"message": fmt.Sprintf("正在切换至已保存 Wi-Fi '%s'... 若 20 秒未连通将自动回滚至车载热点", targetSSID),
		})

		go func(ssid string) {
			log.Printf("[WiFi-Watchdog] Switching to saved WiFi: %s", ssid)
			_ = exec.Command("sudo", "nmcli", "connection", "modify", "Tesla-CarPlay", "connection.autoconnect-priority", "1").Run()
			out, err := exec.Command("sudo", "nmcli", "connection", "up", ssid).CombinedOutput()
			log.Printf("[WiFi-Watchdog] Switch output: %s, err: %v", string(out), err)

			connected := false
			if err == nil {
				for i := 0; i < 15; i++ {
					time.Sleep(1 * time.Second)
					if ip, ok := getPrimaryIP(); ok && ip != "" {
						connected = true
						log.Printf("[WiFi-Watchdog] Switched successfully to %s (IP: %s)", ssid, ip)
						_ = exec.Command("sudo", "systemctl", "restart", "diplay-airplay", "diplay-pi").Run()
						break
					}
				}
			}

			if !connected {
				log.Printf("[WiFi-Watchdog] Failed to connect to %s. Rolling back to Tesla-CarPlay hotspot...", ssid)
				_ = exec.Command("sudo", "nmcli", "connection", "modify", "Tesla-CarPlay", "connection.autoconnect-priority", "999").Run()
				_ = exec.Command("sudo", "nmcli", "connection", "up", "Tesla-CarPlay").Run()
				time.Sleep(2 * time.Second)
				_ = exec.Command("sudo", "systemctl", "restart", "diplay-airplay", "diplay-pi").Run()
			}
		}(targetSSID)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":    false,
		"error": "未知模式: " + payload.Mode,
	})
}

func handleTerminalExec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Cmd string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Cmd) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "指令不能为空"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", req.Cmd)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       exitCode == 0,
		"output":   string(out),
		"exitCode": exitCode,
		"cmd":      req.Cmd,
	})
}

func handleTerminalLogs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service == "" {
		service = "diplay-airplay"
	}
	lines := r.URL.Query().Get("lines")
	if lines == "" {
		lines = "40"
	}

	out, err := exec.Command("journalctl", "-u", service, "-n", lines, "--no-pager").CombinedOutput()
	if err != nil {
		out = append(out, []byte(fmt.Sprintf("\n[Error executing journalctl: %v]", err))...)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(out)
}

var (
	exportedLogsMu sync.Mutex
	exportedLogs   = make(map[string][]byte)
)

func handleTerminalExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Logs string `json:"logs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	nowStr := time.Now().Format("2006-01-02 15:04:05")
	fileId := fmt.Sprintf("%d", time.Now().UnixNano())

	var buf bytes.Buffer
	buf.WriteString("==================================================================\n")
	buf.WriteString("       DiPlay-Pi 车载无线投屏系统 · 实时链路与控制台完整日志导出       \n")
	buf.WriteString(fmt.Sprintf("导出时间: %s\n", nowStr))
	buf.WriteString("==================================================================\n\n")

	if strings.TrimSpace(req.Logs) != "" {
		buf.WriteString("--- [ 网页控制台屏幕输出记录 (Terminal Output) ] ---\n")
		buf.WriteString(req.Logs)
		buf.WriteString("\n\n")
	}

	buf.WriteString("--- [ 自动诊断快照: 系统服务运行状态 (System Services) ] ---\n")
	if out, err := exec.Command("systemctl", "status", "diplay-airplay", "diplay-iap2", "diplay-bt", "diplay-pi", "--no-pager").CombinedOutput(); err == nil {
		buf.Write(out)
		buf.WriteString("\n")
	}

	buf.WriteString("--- [ 自动诊断快照: 蓝牙与配对列表 (Bluetooth Status) ] ---\n")
	if out, err := exec.Command("bluetoothctl", "paired-devices").CombinedOutput(); err == nil {
		buf.WriteString("Paired Devices:\n")
		buf.Write(out)
	}
	if out, err := exec.Command("hcitool", "con").CombinedOutput(); err == nil {
		buf.WriteString("Active ACL Connections:\n")
		buf.Write(out)
	}

	buf.WriteString("\n--- [ 自动诊断快照: 无线网卡吞吐与信道 (Wi-Fi Driver Status) ] ---\n")
	if out, err := exec.Command("iw", "dev", "wlan0", "info").CombinedOutput(); err == nil {
		buf.Write(out)
	}

	buf.WriteString("\n--- [ 自动诊断快照: 内核最近报错信息 (dmesg tail) ] ---\n")
	if out, err := exec.Command("sh", "-c", "dmesg -T | tail -n 40").CombinedOutput(); err == nil {
		buf.Write(out)
	}

	exportedLogsMu.Lock()
	exportedLogs[fileId] = buf.Bytes()
	if len(exportedLogs) > 20 {
		for k := range exportedLogs {
			if len(exportedLogs) <= 20 {
				break
			}
			delete(exportedLogs, k)
		}
	}
	exportedLogsMu.Unlock()

	host := r.Host
	if host == "" {
		host = "100.96.0.1:8088"
	}
	downloadUrl := fmt.Sprintf("http://%s/api/terminal/download?id=%s", host, fileId)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"id":          fileId,
		"downloadUrl": downloadUrl,
		"sizeBytes":   buf.Len(),
	})
}

func handleTerminalDownload(w http.ResponseWriter, r *http.Request) {
	fileId := r.URL.Query().Get("id")
	exportedLogsMu.Lock()
	data, ok := exportedLogs[fileId]
	exportedLogsMu.Unlock()

	if !ok || len(data) == 0 {
		http.Error(w, "日志文件不存在或已过期，请在控制台重新生成二维码", http.StatusNotFound)
		return
	}

	fileName := fmt.Sprintf("diplay_log_%s.txt", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Write(data)
}

var (
	latestVideoLogMu sync.Mutex
	latestVideoLog   []byte
)

func handleVideoLogSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, `{"error":"Method Not Allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"Failed to read body"}`, http.StatusBadRequest)
		return
	}
	latestVideoLogMu.Lock()
	latestVideoLog = body
	latestVideoLogMu.Unlock()

	w.Write([]byte(`{"ok":true}`))
}

func handleVideoLogLatest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	latestVideoLogMu.Lock()
	data := append([]byte(nil), latestVideoLog...)
	latestVideoLogMu.Unlock()

	if len(data) == 0 {
		w.Write([]byte("暂无已同步的车载视频播放诊断日志。\n请在特斯拉车机网页点击【📺 播放日志】->【☁️ 在手机/电脑查看】即可在此页面直接刷新查看并复制完整日志！"))
		return
	}
	w.Write(data)
}

func handleSystemUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	flusher, _ := w.(http.Flusher)

	emit := func(msg string) {
		fmt.Fprintf(w, "%s\n", msg)
		if flusher != nil {
			flusher.Flush()
		}
	}

	emit("==================================================================")
	emit("       🚀 DiPlay-Pi 车载系统 OTA 在线升级程序启动        ")
	emit("==================================================================")

	// 1. Receive and save archive to /tmp/diplay-pi-tesla-bundle.tar.gz
	tmpBundle := "/tmp/diplay-pi-tesla-bundle.tar.gz"
	var src io.Reader
	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "multipart/form-data") {
		emit("[*] 正在解析 Multipart 文件上传数据流...")
		if err := r.ParseMultipartForm(120 * 1024 * 1024); err != nil {
			emit(fmt.Sprintf("[-] 上传数据解析失败: %v", err))
			return
		}
		file, header, err := r.FormFile("package")
		if err != nil {
			file, header, err = r.FormFile("bundle")
		}
		if err != nil {
			file, header, err = r.FormFile("file")
		}
		if err != nil {
			emit(fmt.Sprintf("[-] 未在表单中找到升级包文件 (package/bundle/file): %v", err))
			return
		}
		defer file.Close()
		emit(fmt.Sprintf("[*] 文件名称: %s", header.Filename))
		src = file
	} else {
		emit("[*] 正在接收原始二进制数据流...")
		src = r.Body
	}

	dstFile, err := os.Create(tmpBundle)
	if err != nil {
		emit(fmt.Sprintf("[-] 创建临时升级文件失败: %v", err))
		return
	}

	written, err := io.Copy(dstFile, src)
	dstFile.Close()
	if err != nil || written < 1024*1024 {
		emit(fmt.Sprintf("[-] 文件写入不完整 (已写入: %d 字节): %v", written, err))
		return
	}

	emit(fmt.Sprintf("[✓] 第一步: 安装包上传成功！(大小: %.2f MB)", float64(written)/(1024*1024)))

	// 2. Extract archive
	emit("[*] 第二步: 正在解压安装包到升级工作区 /tmp/diplay_upgrade/ ...")
	upgradeDir := "/tmp/diplay_upgrade"
	_ = os.RemoveAll(upgradeDir)
	_ = os.MkdirAll(upgradeDir, 0755)

	tarCmd := exec.Command("tar", "-zxvf", tmpBundle, "-C", upgradeDir)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		emit(fmt.Sprintf("[-] 解压安装包失败: %v\n输出: %s", err, string(out)))
		return
	}
	emit("[✓] 安装包解压完成！")

	// Locate install.sh
	installScript := filepath.Join(upgradeDir, "install.sh")
	if _, err := os.Stat(installScript); os.IsNotExist(err) {
		installScript = filepath.Join(upgradeDir, "diplay-pi-tesla", "install.sh")
	}
	if _, err := os.Stat(installScript); os.IsNotExist(err) {
		emit("[-] 错误: 未在解压目录中找到 install.sh 安装引导脚本")
		return
	}
	_ = os.Chmod(installScript, 0755)

	// 3. Execute install.sh with DIPLAY_WEB_UPGRADE=1
	scriptDir := filepath.Dir(installScript)
	emit(fmt.Sprintf("[*] 第三步: 正在执行自动化安装程序 (工作目录: %s)...", scriptDir))
	emit("------------------------------------------------------------------")

	cmd := exec.Command("bash", installScript)
	cmd.Dir = scriptDir
	cmd.Env = append(os.Environ(), "DIPLAY_WEB_UPGRADE=1")

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		emit(fmt.Sprintf("[-] 管道创建失败: %v", err))
		return
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		emit(fmt.Sprintf("[-] 启动安装脚本失败: %v", err))
		return
	}

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		emit(scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		emit("------------------------------------------------------------------")
		emit(fmt.Sprintf("[-] 安装脚本执行异常 (退出码非0): %v", err))
		return
	}

	emit("------------------------------------------------------------------")
	emit("🎉 升级安装全部完成！")
	emit("[*] 第四步: 正在调度系统守护进程平滑重启...")
	emit("[*] 页面将在 3 秒后自动重新连接...")

	// 4. Background delayed restart
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_ = exec.Command("systemctl", "daemon-reload").Run()
		_ = exec.Command("systemctl", "restart", "diplay-airplay", "diplay-pi").Run()
	}()
}

func handleSnapshot(w http.ResponseWriter, r *http.Request) {
	hub.mu.RLock()
	snap := hub.lastJpegFrame
	hub.mu.RUnlock()

	w.Header().Set("Content-Type", "image/jpeg")
	if len(snap) > 0 {
		w.Write(snap)
	} else {
		// Empty 1x1 JPEG fallback
		w.Write([]byte{
			0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x43, 0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07,
			0x07, 0x09, 0x09, 0x08, 0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12, 0x13, 0x0F,
			0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20, 0x24, 0x2E, 0x27, 0x20, 0x22, 0x2C,
			0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29, 0x2C, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39, 0x3D,
			0x38, 0x32, 0x3C, 0x2E, 0x33, 0x34, 0x32, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01, 0x00, 0x01,
			0x01, 0x01, 0x11, 0x00, 0xFF, 0xC4, 0x00, 0x1F, 0x00, 0x00, 0x01, 0x05, 0x01, 0x01, 0x01, 0x01,
			0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
			0x07, 0x08, 0x09, 0x0A, 0x0B, 0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00, 0xBF,
			0x80, 0xFF, 0xD9,
		})
	}
}

// Low-overhead standard WebSocket Handshake (RFC 6455)
func handleWs(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "Not a websocket handshake", http.StatusBadRequest)
		return
	}

	conn, err := upgradeWebSocket(w, r)
	if err != nil {
		log.Printf("[WS] Upgrade failed: %v", err)
		return
	}

	client := newWsClient(conn)

	hub.mu.Lock()
	hub.wsClients[client] = true
	codecData := hub.lastCodecData
	wWidth := hub.width
	wHeight := hub.height
	hub.mu.Unlock()

	log.Printf("[WS] New client connected (%s). Total: %d", client.remoteAddr, len(hub.wsClients))

	// 1. Send initial hello status
	hw := getHardwareStats()
	initMsg := fmt.Sprintf(`{"type":"hello","active":true,"hasSession":true,"width":%d,"height":%d,"fps":60,"cpu":%.1f,"temp":%.1f,"memUsed":%d,"memTotal":%d}`,
		wWidth, wHeight, hw.Cpu, hw.Temp, hw.MemUsedMB, hw.MemTotalMB)
	_ = client.SendFrame(0x01, []byte(initMsg))

	// 2. Instant Render: Push cached SPS/PPS codec configuration (avcC) and last keyframe
	if len(codecData) > 0 {
		_ = client.SendMultiplex(0x01, 0, codecData)
	}
	hub.mu.RLock()
	lastInitKey := hub.lastKeyFrame
	hub.mu.RUnlock()
	if len(lastInitKey) > 0 {
		_ = client.SendMultiplex(0x02, 1, lastInitKey)
	}

	// 3. Client connected. Cached SPS/PPS pushed.

	// Read loop
	buf := make([]byte, 4096)
	for {
		op, payload, err := readWsFrame(conn, buf)
		if err != nil {
			log.Printf("[WS] Client %s disconnected: %v. Remaining: %d", client.remoteAddr, err, len(hub.wsClients)-1)
			break
		}
		if op == 0x09 { // Browser ping
			_ = client.SendFrame(0x0A, payload) // Pong
			continue
		}
		if op == 0x08 { // Browser close
			_ = client.SendFrame(0x08, payload)
			break
		}
		if len(payload) > 0 {
			// Ultra low-latency binary touch protocol (opcode 0x02 or 'T' magic header)
			if op == 0x02 || (len(payload) >= 2 && payload[0] == 0x54) {
				if ctrlBridge != nil {
					go func(p []byte) {
						_ = ctrlBridge.SendBytes(p)
					}(payload)
				}
				continue
			}

			var m map[string]interface{}
			if err := json.Unmarshal(payload, &m); err == nil {
				msgType, _ := m["type"].(string)
				switch msgType {
				case "ping":
					_ = client.SendFrame(0x01, []byte(`{"type":"pong"}`))
				case "log":
					log.Printf("[WebClient %s] %v", client.remoteAddr, m["msg"])
				case "reconnect":
					log.Printf("[WS] Client %s requested CarPlay reconnect/wake", client.remoteAddr)
					if ctrlBridge != nil {
						go ctrlBridge.Send(`{"type":"reconnect"}`)
					}
				case "resolution":
					var w, h int
					if wVal, ok := m["width"]; ok {
						switch v := wVal.(type) {
						case float64:
							w = int(v)
						case int:
							w = v
						}
					}
					if hVal, ok := m["height"]; ok {
						switch v := hVal.(type) {
						case float64:
							h = int(v)
						case int:
							h = v
						}
					}
					if w > 0 && h > 0 {
						applyResolution(w, h)
					}
				case "touch":
					if ctrlBridge != nil {
						go ctrlBridge.Send(string(payload))
					}
				case "action":
					if ctrlBridge != nil {
						go ctrlBridge.Send(string(payload))
					}
				case "requestKeyFrame":
					if ctrlBridge != nil {
						go ctrlBridge.Send(`{"type":"keyframe"}`)
					}
					hub.mu.RLock()
					codec := hub.lastCodecData
					lastK := hub.lastKeyFrame
					hub.mu.RUnlock()
					if len(codec) > 0 {
						_ = client.SendMultiplex(0x01, 0, codec)
					}
					if len(lastK) > 0 {
						_ = client.SendMultiplex(0x02, 1, lastK)
					}
				}
			}
		}
	}

	hub.mu.Lock()
	delete(hub.wsClients, client)
	hub.mu.Unlock()
	client.Close()
	log.Printf("[WS] Client %s disconnected. Remaining: %d", client.remoteAddr, len(hub.wsClients))
}

func handleAudioWs(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "Not a websocket handshake", http.StatusBadRequest)
		return
	}

	conn, err := upgradeWebSocket(w, r)
	if err != nil {
		log.Printf("[AudioWS] Upgrade failed: %v", err)
		return
	}

	client := newWsClient(conn)

	hub.mu.Lock()
	hub.audioClients[client] = true
	hub.mu.Unlock()

	log.Printf("[AudioWS] Dedicated audio client connected (%s)", client.remoteAddr)
	_ = client.SendFrame(0x01, []byte(`{"type":"audio_ready"}`))

	buf := make([]byte, 1024)
	for {
		op, payload, err := readWsFrame(conn, buf)
		if err != nil {
			break
		}
		if op == 0x09 {
			_ = client.SendFrame(0x0A, payload)
			continue
		}
		if op == 0x08 {
			_ = client.SendFrame(0x08, payload)
			break
		}
	}

	hub.mu.Lock()
	delete(hub.audioClients, client)
	hub.mu.Unlock()
	client.Close()
	log.Printf("[AudioWS] Audio client disconnected (%s)", client.remoteAddr)
}

// Broadcaster routine: fan-out video & audio to clients
func (h *Hub) runBroadcaster() {
	for {
		select {
		case frame := <-h.frameChan:
			isKey := isH264Keyframe(frame)
			h.mu.RLock()
			annexB := h.annexBConfig
			clients := make([]*WsClient, 0, len(h.wsClients))
			for c := range h.wsClients {
				clients = append(clients, c)
			}
			h.mu.RUnlock()

			if isKey && len(annexB) > 0 {
				hasSps := false
				if len(frame) >= 5 && frame[0] == 0 && frame[1] == 0 && frame[2] == 0 && frame[3] == 1 && ((frame[4]&0x1F) == 7 || (frame[4]&0x1F) == 8) {
					hasSps = true
				}
				if !hasSps {
					completeKeyframe := make([]byte, len(annexB)+len(frame))
					copy(completeKeyframe, annexB)
					copy(completeKeyframe[len(annexB):], frame)
					frame = completeKeyframe
				}
			}

			if isKey {
				h.mu.Lock()
				h.lastKeyFrame = frame
				h.mu.Unlock()
			}

			flag := byte(0)
			if isKey {
				flag = 1
			}

			atomic.AddInt64(&h.fpsCounter, 1)

			packet := buildMultiplexFrame(0x02, flag, frame)

			for _, c := range clients {
				if atomic.LoadInt32(&c.closed) != 0 {
					h.mu.Lock()
					delete(h.wsClients, c)
					h.mu.Unlock()
					continue
				}
				select {
				case c.sendQueue <- packet:
				default:
					atomic.AddInt64(&h.droppedFrames, 1)
					if isKey {
						// Priority queueing for keyframe: drop older delta frames so client receives fresh keyframe immediately!
						for len(c.sendQueue) > 0 {
							select {
							case <-c.sendQueue:
							default:
							}
						}
						select {
						case c.sendQueue <- packet:
						default:
						}
					}
				}
			}

		case pcm := <-h.audioChan:
			h.mu.RLock()
			audioClients := make([]*WsClient, 0, len(h.audioClients))
			for c := range h.audioClients {
				audioClients = append(audioClients, c)
			}
			h.mu.RUnlock()

			// Channel 0x00: 44.1kHz Stereo PCM (flag=0x00)
			audioPacket := buildMultiplexFrame(0x00, 0x00, pcm)
			for _, c := range audioClients {
				if atomic.LoadInt32(&c.closed) != 0 {
					h.mu.Lock()
					delete(h.audioClients, c)
					h.mu.Unlock()
					continue
				}
				select {
				case c.sendQueue <- audioPacket:
				default:
				}
			}
		}
	}
}

// WebSocket RFC 6455 Frame builders with atomic pre-allocation (avoids partial writes & corrupt frames)
func buildWsFrame(opcode byte, payload []byte) []byte {
	length := len(payload)
	hLen := 2
	if length <= 125 {
		hLen = 2
	} else if length <= 65535 {
		hLen = 4
	} else {
		hLen = 10
	}
	frame := make([]byte, hLen+length)
	frame[0] = 0x80 | (opcode & 0x0F) // FIN + opcode
	if length <= 125 {
		frame[1] = byte(length)
	} else if length <= 65535 {
		frame[1] = 126
		binary.BigEndian.PutUint16(frame[2:4], uint16(length))
	} else {
		frame[1] = 127
		binary.BigEndian.PutUint64(frame[2:10], uint64(length))
	}
	copy(frame[hLen:], payload)
	return frame
}

func buildMultiplexFrame(channel byte, flag byte, data []byte) []byte {
	totalLen := len(data) + 4
	hLen := 2
	if totalLen <= 125 {
		hLen = 2
	} else if totalLen <= 65535 {
		hLen = 4
	} else {
		hLen = 10
	}
	frame := make([]byte, hLen+4+len(data))
	frame[0] = 0x82 // Binary frame (FIN + opcode 2)
	if totalLen <= 125 {
		frame[1] = byte(totalLen)
	} else if totalLen <= 65535 {
		frame[1] = 126
		binary.BigEndian.PutUint16(frame[2:4], uint16(totalLen))
	} else {
		frame[1] = 127
		binary.BigEndian.PutUint64(frame[2:10], uint64(totalLen))
	}
	frame[hLen] = channel
	frame[hLen+1] = flag
	frame[hLen+2] = 0
	frame[hLen+3] = 0
	copy(frame[hLen+4:], data)
	return frame
}

func readWsFrame(r io.Reader, buf []byte) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0F
	masked := (hdr[1] & 0x80) != 0
	payloadLen := uint64(hdr[1] & 0x7F)

	if payloadLen == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext[:]))
	} else if payloadLen == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(ext[:])
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}

	return opcode, payload, nil
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("webserver doesn't support hijacking")
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	h := sha1Hash(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	accept := base64Encode(h)

	resp := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", accept)

	if _, err := bufrw.WriteString(resp); err != nil {
		conn.Close()
		return nil, err
	}
	bufrw.Flush()

	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetWriteBuffer(1024 * 1024)
	}

	return conn, nil
}

// Raw Video & Audio Feed Ingestion from AirPlay Pipeline / USB
func startVideoFeedServer(port int) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Printf("[VideoFeed] Listen failed on :%d: %v", port, err)
		return
	}
	defer listener.Close()
	log.Printf("[VideoFeed] Listening for raw H.264 stream on 127.0.0.1:%d", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleVideoIngest(conn)
	}
}

func handleVideoIngest(conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetReadBuffer(2 * 1024 * 1024)
	}
	log.Printf("[VideoFeed] >>> Ingestion pipeline attached: %s <<<", conn.RemoteAddr())

	lenBuf := make([]byte, 4)
	var totalFrames int64
	var totalBytes int64
	lastLogTime := time.Now()

	for {
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			log.Printf("[VideoFeed] Read length prefix error: %v", err)
			break
		}
		frameLen := binary.BigEndian.Uint32(lenBuf)
		if frameLen == 0 || frameLen > 4*1024*1024 {
			log.Printf("[VideoFeed] Abnormal frame length: %d bytes (skipping)", frameLen)
			continue
		}
		frame := make([]byte, frameLen)
		if _, err := io.ReadFull(conn, frame); err != nil {
			log.Printf("[VideoFeed] Read frame payload error: %v", err)
			break
		}

		totalFrames++
		totalBytes += int64(frameLen)

		// Periodic or first frames logging
		isConfig := isConfigPacket(frame)
		isKey := isH264Keyframe(frame)
		if totalFrames <= 5 || totalFrames%120 == 0 || isConfig || isKey {
			prefixLen := 16
			if len(frame) < prefixLen {
				prefixLen = len(frame)
			}
			hexPrefix := fmt.Sprintf("%X", frame[:prefixLen])
			log.Printf("[VideoFeed] Ingest Frame #%d: len=%d bytes, isConfig=%v, isKey=%v, hexPrefix=%s",
				totalFrames, frameLen, isConfig, isKey, hexPrefix)
		}

		if time.Since(lastLogTime) >= 5*time.Second {
			log.Printf("[VideoFeed] Telemetry: totalFrames=%d, totalBytes=%.2f MB, activeClients=%d",
				totalFrames, float64(totalBytes)/(1024*1024), len(hub.wsClients))
			lastLogTime = time.Now()
		}

		// Check if this is an avcC/SPS/PPS configuration frame (starts with 0x01 0x64 or SPS 0x67)
		if isConfig {
			annexB := avccToAnnexB(frame)
			hub.mu.Lock()
			hub.lastCodecData = frame
			if len(annexB) > 0 {
				hub.annexBConfig = annexB
			}
			hub.mu.Unlock()
			// Push immediately to all active WS clients
			hub.mu.RLock()
			spsClients := make([]*WsClient, 0, len(hub.wsClients))
			for c := range hub.wsClients {
				spsClients = append(spsClients, c)
			}
			hub.mu.RUnlock()
			for _, c := range spsClients {
				_ = c.SendMultiplex(0x01, 0, frame)
			}
		} else {
			atomic.AddInt64(&hub.bytesCounter, int64(len(frame)))
			select {
			case hub.frameChan <- frame:
			default:
				// Dropping queue frame if congested
				dropped := atomic.AddInt64(&hub.droppedFrames, 1)
				if dropped <= 5 || dropped%60 == 0 {
					log.Printf("[VideoFeed] ⚠️ 警告: 视频帧队列拥塞已丢帧! (累计丢帧: %d, 当前码率: %.1f Mbps). 常见原因: Wi-Fi 空口带宽打满(特别是地图划动高动态场景)或车机下行阻塞",
						dropped, hub.currentBitrateMbps)
				}
			}
		}
	}
	log.Printf("[VideoFeed] >>> Ingestion pipeline detached: %s (Total received: %d frames, %.2f MB) <<<",
		conn.RemoteAddr(), totalFrames, float64(totalBytes)/(1024*1024))
}

func startAudioFeedServer(port int) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Printf("[AudioFeed] Listen failed on :%d: %v", port, err)
		return
	}
	defer listener.Close()
	log.Printf("[AudioFeed] Listening for raw PCM stream on 127.0.0.1:%d", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleAudioIngest(conn)
	}
}

func handleAudioIngest(conn net.Conn) {
	defer conn.Close()
	log.Printf("[AudioFeed] Audio pipeline attached: %s", conn.RemoteAddr())

	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case hub.audioChan <- chunk:
			default:
			}
		}
	}
}

func isH264Keyframe(data []byte) bool {
	if len(data) < 5 {
		return false
	}
	// 1. Walk AVCC 4-byte length-prefixed NALUs (AirPlay video default)
	offset := 0
	isAvcc := true
	for offset+4 < len(data) {
		naluLen := int(data[offset])<<24 | int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
		if naluLen <= 0 || offset+4+naluLen > len(data) {
			isAvcc = false
			break
		}
		nalType := data[offset+4] & 0x1F
		if nalType == 5 { // IDR frame
			return true
		}
		offset += 4 + naluLen
	}
	if isAvcc && offset == len(data) {
		return false
	}

	// 2. Check Annex-B start codes (0x000001 or 0x00000001)
	for i := 0; i < len(data)-4; i++ {
		if (data[i] == 0 && data[i+1] == 0 && data[i+2] == 1) ||
			(data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1) {
			headerOffset := i + 3
			if data[i+2] == 0 {
				headerOffset = i + 4
			}
			if headerOffset < len(data) {
				nalType := data[headerOffset] & 0x1F
				if nalType == 5 { // IDR frame
					return true
				}
			}
		}
	}
	// Also fallback check AVCC at offset 0
	if (data[4] & 0x1F) == 5 {
		return true
	}
	return false
}

func avccToAnnexB(avcc []byte) []byte {
	if len(avcc) < 11 || avcc[0] != 1 {
		// If already Annex-B with SPS
		if len(avcc) >= 4 && avcc[0] == 0 && avcc[1] == 0 && (avcc[2] == 1 || (avcc[2] == 0 && avcc[3] == 1)) {
			return avcc
		}
		return nil
	}
	numSps := int(avcc[5] & 0x1F)
	offset := 6
	var out []byte
	for i := 0; i < numSps && offset+2 <= len(avcc); i++ {
		spsLen := int(avcc[offset])<<8 | int(avcc[offset+1])
		offset += 2
		if offset+spsLen <= len(avcc) {
			out = append(out, 0, 0, 0, 1)
			out = append(out, avcc[offset:offset+spsLen]...)
			offset += spsLen
		}
	}
	if offset < len(avcc) {
		numPps := int(avcc[offset])
		offset++
		for i := 0; i < numPps && offset+2 <= len(avcc); i++ {
			ppsLen := int(avcc[offset])<<8 | int(avcc[offset+1])
			offset += 2
			if offset+ppsLen <= len(avcc) {
				out = append(out, 0, 0, 0, 1)
				out = append(out, avcc[offset:offset+ppsLen]...)
				offset += ppsLen
			}
		}
	}
	return out
}

func isConfigPacket(data []byte) bool {
	if len(data) >= 7 && data[0] == 1 && data[1] == 0x64 { // avcC header
		return true
	}
	if len(data) >= 4 && (data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 && (data[4]&0x1F) == 7) {
		return true
	}
	return false
}

// Crypto & Base64 utilities
func sha1Hash(s string) []byte {
	importSha1 := func(str string) []byte {
		// Pure standard SHA-1
		var w [80]uint32
		h0 := uint32(0x67452301)
		h1 := uint32(0xEFCDAB89)
		h2 := uint32(0x98BADCFE)
		h3 := uint32(0x10325476)
		h4 := uint32(0xC3D2E1F0)

		data := []byte(str)
		origLen := uint64(len(data) * 8)
		data = append(data, 0x80)
		for (len(data)+8)%64 != 0 {
			data = append(data, 0)
		}
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, origLen)
		data = append(data, lenBytes...)

		for chunk := 0; chunk < len(data); chunk += 64 {
			for i := 0; i < 16; i++ {
				w[i] = binary.BigEndian.Uint32(data[chunk+i*4 : chunk+i*4+4])
			}
			for i := 16; i < 80; i++ {
				w[i] = (w[i-3] ^ w[i-8] ^ w[i-14] ^ w[i-16])
				w[i] = (w[i] << 1) | (w[i] >> 31)
			}
			a, b, c, d, e := h0, h1, h2, h3, h4
			for i := 0; i < 80; i++ {
				var f, k uint32
				if i < 20 {
					f = (b & c) | ((^b) & d)
					k = 0x5A827999
				} else if i < 40 {
					f = b ^ c ^ d
					k = 0x6ED9EBA1
				} else if i < 60 {
					f = (b & c) | (b & d) | (c & d)
					k = 0x8F1BBCDC
				} else {
					f = b ^ c ^ d
					k = 0xCA62C1D6
				}
				temp := ((a << 5) | (a >> 27)) + f + e + k + w[i]
				e = d
				d = c
				c = (b << 30) | (b >> 2)
				b = a
				a = temp
			}
			h0 += a
			h1 += b
			h2 += c
			h3 += d
			h4 += e
		}
		res := make([]byte, 20)
		binary.BigEndian.PutUint32(res[0:4], h0)
		binary.BigEndian.PutUint32(res[4:8], h1)
		binary.BigEndian.PutUint32(res[8:12], h2)
		binary.BigEndian.PutUint32(res[12:16], h3)
		binary.BigEndian.PutUint32(res[16:20], h4)
		return res
	}
	return importSha1(s)
}

func base64Encode(data []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var result []byte
	n := len(data)
	for i := 0; i < n; i += 3 {
		b0 := data[i]
		b1 := byte(0)
		b2 := byte(0)
		if i+1 < n {
			b1 = data[i+1]
		}
		if i+2 < n {
			b2 = data[i+2]
		}
		result = append(result, table[b0>>2])
		result = append(result, table[((b0&0x03)<<4)|(b1>>4)])
		if i+1 < n {
			result = append(result, table[((b1&0x0F)<<2)|(b2>>6)])
		} else {
			result = append(result, '=')
		}
		if i+2 < n {
			result = append(result, table[b2&0x3F])
		} else {
			result = append(result, '=')
		}
	}
	return string(result)
}

func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"DiPlay-Pi"},
			CommonName:   "Tesla-CarPlay",
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("100.96.0.1"),
			net.ParseIP("192.168.43.1"),
		},
		DNSNames: []string{"localhost", "raspberrypi", "tesla-carplay", "carplay", "tesla.carplay", "diplay.com", "tespush.com", "dashplay.lan"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	cert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}
	return cert, nil
}

var allowedDNSDomains = map[string]bool{
	"connman.vn.cloud.tesla.cn": true,
	"www.baidu.cn":              true,
	"www.qq.com":                true,
	"www.tesla.cn":              true,
	"carplay":                   true,
	"tesla.carplay":             true,
	"diplay.com":                true,
	"dashplay.lan":              true,
	"tespush.com":               true,
	"clients3.google.com":       true,
	"connectivitycheck.gstatic.com": true,
}

func parseDNSQuestion(data []byte) string {
	if len(data) <= 12 {
		return ""
	}
	idx := 12
	var parts []string
	for idx < len(data) {
		length := int(data[idx])
		if length == 0 {
			break
		}
		idx++
		if idx+length > len(data) {
			return ""
		}
		parts = append(parts, string(data[idx:idx+length]))
		idx += length
	}
	return strings.ToLower(strings.Join(parts, "."))
}

// startEmbeddedDNSServer runs a high-performance lightweight UDP DNS server on 0.0.0.0:53.
// Only answers Tesla offline check domains with 100.96.0.1; returns REFUSED (RCODE=5) for all
// genuine Tesla services (maps, music, telemetry) to allow car to resolve them via its own LTE.
func startEmbeddedDNSServer() {
	addr, err := net.ResolveUDPAddr("udp", "0.0.0.0:53")
	if err != nil {
		log.Printf("[DiPlay-DNS] ResolveUDPAddr error: %v", err)
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("[DiPlay-DNS] Port 53 binding not available (requires root): %v", err)
		return
	}
	defer conn.Close()
	log.Printf("[DiPlay-DNS] Embedded Tesla DNS Resolver listening on 0.0.0.0:53")

	buf := make([]byte, 512)
	ipBytes := []byte{100, 96, 0, 1}
	answerPrefix := []byte{0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04}

	for {
		n, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil || n < 12 {
			continue
		}

		qname := parseDNSQuestion(buf[:n])
		log.Printf("[DiPlay-DNS] Query from %s for: %s", remoteAddr, qname)

		if allowedDNSDomains[qname] {
			// Answer 100.96.0.1
			resp := make([]byte, 0, n+16)
			resp = append(resp, buf[:2]...)              // Transaction ID
			resp = append(resp, 0x81, 0x80)              // Flags: Response, NoError
			resp = append(resp, buf[4], buf[5])          // QDCOUNT
			resp = append(resp, 0x00, 0x01)              // ANCOUNT = 1
			resp = append(resp, 0x00, 0x00, 0x00, 0x00)  // NSCOUNT = 0, ARCOUNT = 0
			resp = append(resp, buf[12:n]...)            // Copy Question
			resp = append(resp, answerPrefix...)         // Pointer, Type A, Class IN, TTL 60, Len 4
			resp = append(resp, ipBytes...)              // 100.96.0.1
			_, _ = conn.WriteTo(resp, remoteAddr)
		} else {
			// Refuse (RCODE 5): tell the car's resolver to fallback to its LTE upstream
			resp := make([]byte, 0, n)
			resp = append(resp, buf[:2]...)              // Transaction ID
			resp = append(resp, 0x81, 0x85)              // Flags: Response, Refused (RCODE=5)
			resp = append(resp, buf[4], buf[5])          // QDCOUNT
			resp = append(resp, 0x00, 0x00)              // ANCOUNT = 0
			resp = append(resp, 0x00, 0x00, 0x00, 0x00)  // NSCOUNT = 0, ARCOUNT = 0
			resp = append(resp, buf[12:n]...)            // Copy Question
			_, _ = conn.WriteTo(resp, remoteAddr)
		}
	}
}

// ============================================================================
// TeslaCam USB Storage & Dashcam Video Management APIs
// ============================================================================

type TeslaCamClip struct {
	Camera   string `json:"camera"`    // front, back, left_repeater, right_repeater
	FileName string `json:"file_name"` // 2026-10-18_14-25-30-front.mp4
	FilePath string `json:"file_path"` // relative path: SentryClips/2026-10-18_14-25-30/2026-10-18_14-25-30-front.mp4
	FileSize int64  `json:"file_size"`
	ModTime  int64  `json:"mod_time"`
}

type TeslaCamEvent struct {
	ID        string         `json:"id"`       // 2026-10-18_14-25-30
	Category  string         `json:"category"` // sentry, saved, recent
	Timestamp string         `json:"timestamp"`
	Reason    string         `json:"reason"` // Sentry Aware / Honk / User Save
	City      string         `json:"city"`
	Clips     []TeslaCamClip `json:"clips"`
	TotalSize int64          `json:"total_size"`
	HasFront  bool           `json:"has_front"`
	HasBack   bool           `json:"has_back"`
	HasLeft   bool           `json:"has_left"`
	HasRight  bool           `json:"has_right"`
	ThumbPath string         `json:"thumb_path,omitempty"`
}

type TeslaCamStatus struct {
	Supported   bool    `json:"supported"`
	Active      bool    `json:"active"`
	MountPath   string  `json:"mount_path"`
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
	SentryCount int     `json:"sentry_count"`
	SavedCount  int     `json:"saved_count"`
	RecentCount int     `json:"recent_count"`
	TotalEvents int     `json:"total_events"`
	DriveModel  string  `json:"drive_model"`
	PiModel     string  `json:"pi_model"`
	IsPi3       bool    `json:"is_pi3"`
	HasUdc      bool    `json:"has_udc"`
}

func getTeslaCamRootDir() string {
	if custom := os.Getenv("TESLACAM_PATH"); custom != "" {
		if fi, err := os.Stat(custom); err == nil && fi.IsDir() {
			if sub, err := os.Stat(filepath.Join(custom, "TeslaCam")); err == nil && sub.IsDir() {
				return filepath.Join(custom, "TeslaCam")
			}
			return custom
		}
	}

	candidates := []string{
		"/mnt/teslacam/TeslaCam",
		"/mnt/teslacam",
		"/media/teslacam/TeslaCam",
		"/media/teslacam",
		"/var/teslacam/TeslaCam",
		"/var/teslacam",
		"/opt/diplay/teslacam/TeslaCam",
		"/opt/diplay/teslacam",
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}

	// Default fallback path
	fallback := "/mnt/teslacam"
	_ = os.MkdirAll(filepath.Join(fallback, "TeslaCam", "RecentClips"), 0755)
	_ = os.MkdirAll(filepath.Join(fallback, "TeslaCam", "SavedClips"), 0755)
	_ = os.MkdirAll(filepath.Join(fallback, "TeslaCam", "SentryClips"), 0755)
	return filepath.Join(fallback, "TeslaCam")
}

func parseEventReason(eventDir string) (string, string) {
	jsonPath := filepath.Join(eventDir, "event.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return "", ""
	}
	var meta struct {
		Reason string `json:"reason"`
		City   string `json:"city"`
	}
	if err := json.Unmarshal(data, &meta); err == nil {
		return meta.Reason, meta.City
	}
	return "", ""
}

func sanitizeTeslaFileName(name string) string {
	clean := strings.ReplaceAll(name, "/", "_")
	clean = strings.ReplaceAll(clean, "\\", "_")
	clean = strings.ReplaceAll(clean, "..", "")
	return clean
}

func handleTeslaCamStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rootDir := getTeslaCamRootDir()

	status := TeslaCamStatus{
		Supported: true,
		Active:    false,
		MountPath: rootDir,
	}

	// 1. Check disk capacity via Statfs
	var stat syscall.Statfs_t
	if err := syscall.Statfs(rootDir, &stat); err == nil {
		status.Active = true
		status.TotalBytes = stat.Blocks * uint64(stat.Bsize)
		status.FreeBytes = stat.Bavail * uint64(stat.Bsize)
		status.UsedBytes = status.TotalBytes - status.FreeBytes
		if status.TotalBytes > 0 {
			status.UsedPercent = math.Round((float64(status.UsedBytes)/float64(status.TotalBytes)*100)*10) / 10
		}
	}

	// Check if USB Gadget ConfigFS is active
	if _, err := os.Stat("/sys/kernel/config/usb_gadget/teslacam"); err == nil {
		status.Supported = true
		status.Active = true
	}

	// Read external disk model if available
	if modelBytes, err := os.ReadFile("/sys/block/sda/device/model"); err == nil {
		status.DriveModel = strings.TrimSpace(string(modelBytes))
	} else {
		status.DriveModel = "TeslaCam USB Storage"
	}

	// Detect Pi model
	if mBytes, err := os.ReadFile("/sys/firmware/devicetree/base/model"); err == nil {
		status.PiModel = strings.TrimRight(string(mBytes), "\x00\r\n ")
	} else {
		status.PiModel = "Raspberry Pi"
	}
	if strings.Contains(status.PiModel, "Raspberry Pi 3") {
		status.IsPi3 = true
	}

	// Check UDC hardware
	if udcEntries, err := os.ReadDir("/sys/class/udc"); err == nil && len(udcEntries) > 0 {
		status.HasUdc = true
	}

	// Count events
	countDir := func(sub string) int {
		p := filepath.Join(rootDir, sub)
		entries, err := os.ReadDir(p)
		if err != nil {
			return 0
		}
		c := 0
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
				c++
			}
		}
		return c
	}

	status.SentryCount = countDir("SentryClips")
	status.SavedCount = countDir("SavedClips")
	status.RecentCount = countDir("RecentClips")
	status.TotalEvents = status.SentryCount + status.SavedCount + status.RecentCount

	_ = json.NewEncoder(w).Encode(status)
}

func handleTeslaCamEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rootDir := getTeslaCamRootDir()

	category := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category")))
	if category == "" {
		category = "all"
	}

	categoriesToScan := []string{}
	switch category {
	case "sentry":
		categoriesToScan = append(categoriesToScan, "SentryClips")
	case "saved":
		categoriesToScan = append(categoriesToScan, "SavedClips")
	case "recent":
		categoriesToScan = append(categoriesToScan, "RecentClips")
	default:
		categoriesToScan = append(categoriesToScan, "SentryClips", "SavedClips", "RecentClips")
	}

	var events []TeslaCamEvent

	for _, catName := range categoriesToScan {
		catDir := filepath.Join(rootDir, catName)
		entries, err := os.ReadDir(catDir)
		if err != nil {
			continue
		}

		// Handle directory-based events (SentryClips / SavedClips)
		for _, entry := range entries {
			ePath := filepath.Join(catDir, entry.Name())
			if entry.IsDir() {
				// Each folder is an event
				files, err := os.ReadDir(ePath)
				if err != nil {
					continue
				}

				ev := TeslaCamEvent{
					ID:        entry.Name(),
					Category:  strings.ToLower(strings.TrimSuffix(catName, "Clips")),
					Timestamp: entry.Name(),
					Clips:     []TeslaCamClip{},
				}
				ev.Reason, ev.City = parseEventReason(ePath)

				for _, f := range files {
					if f.IsDir() {
						continue
					}
					fName := f.Name()
					lower := strings.ToLower(fName)
					if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".png") {
						ev.ThumbPath = filepath.Join(catName, entry.Name(), fName)
					}
					if !strings.HasSuffix(lower, ".mp4") {
						continue
					}

					fi, _ := f.Info()
					fSize := int64(0)
					modTime := int64(0)
					if fi != nil {
						fSize = fi.Size()
						modTime = fi.ModTime().Unix()
					}
					ev.TotalSize += fSize

					cam := "front"
					if strings.Contains(lower, "back") || strings.Contains(lower, "rear") {
						cam = "back"
						ev.HasBack = true
					} else if strings.Contains(lower, "left_repeater") || strings.Contains(lower, "left") {
						cam = "left"
						ev.HasLeft = true
					} else if strings.Contains(lower, "right_repeater") || strings.Contains(lower, "right") {
						cam = "right"
						ev.HasRight = true
					} else {
						ev.HasFront = true
					}

					ev.Clips = append(ev.Clips, TeslaCamClip{
						Camera:   cam,
						FileName: fName,
						FilePath: filepath.Join(catName, entry.Name(), fName),
						FileSize: fSize,
						ModTime:  modTime,
					})
				}

				if len(ev.Clips) > 0 {
					events = append(events, ev)
				}
			} else if strings.HasSuffix(strings.ToLower(entry.Name()), ".mp4") {
				// Flat files inside RecentClips: group by timestamp prefix
				fName := entry.Name()
				lower := strings.ToLower(fName)
				// Format: 2026-10-18_14-25-00-front.mp4
				prefix := fName
				if idx := strings.LastIndex(fName, "-"); idx > 0 {
					prefix = fName[:idx]
				}

				// Find existing or create new
				var targetEv *TeslaCamEvent
				for i := range events {
					if events[i].ID == prefix && events[i].Category == "recent" {
						targetEv = &events[i]
						break
					}
				}
				if targetEv == nil {
					events = append(events, TeslaCamEvent{
						ID:        prefix,
						Category:  "recent",
						Timestamp: prefix,
						Clips:     []TeslaCamClip{},
					})
					targetEv = &events[len(events)-1]
				}

				fi, _ := entry.Info()
				fSize := int64(0)
				modTime := int64(0)
				if fi != nil {
					fSize = fi.Size()
					modTime = fi.ModTime().Unix()
				}
				targetEv.TotalSize += fSize

				cam := "front"
				if strings.Contains(lower, "back") || strings.Contains(lower, "rear") {
					cam = "back"
					targetEv.HasBack = true
				} else if strings.Contains(lower, "left_repeater") || strings.Contains(lower, "left") {
					cam = "left"
					targetEv.HasLeft = true
				} else if strings.Contains(lower, "right_repeater") || strings.Contains(lower, "right") {
					cam = "right"
					targetEv.HasRight = true
				} else {
					targetEv.HasFront = true
				}

				targetEv.Clips = append(targetEv.Clips, TeslaCamClip{
					Camera:   cam,
					FileName: fName,
					FilePath: filepath.Join(catName, fName),
					FileSize: fSize,
					ModTime:  modTime,
				})
			}
		}
	}

	// Sort events descending by ID / timestamp (newest first)
	sort.Slice(events, func(i, j int) bool {
		return events[i].ID > events[j].ID
	})

	// Limit to max 100 for fast network transfer
	if len(events) > 100 {
		events = events[:100]
	}

	_ = json.NewEncoder(w).Encode(events)
}

func handleTeslaCamStream(w http.ResponseWriter, r *http.Request) {
	rootDir := getTeslaCamRootDir()
	relPath := r.URL.Query().Get("file")
	if relPath == "" {
		http.Error(w, "Missing file parameter", http.StatusBadRequest)
		return
	}

	// Security: Clean and ensure file is strictly within rootDir
	cleanRel := filepath.Clean(relPath)
	if strings.Contains(cleanRel, "..") {
		http.Error(w, "Access Denied: Path Traversal", http.StatusForbidden)
		return
	}

	fullPath := filepath.Join(rootDir, cleanRel)
	fi, err := os.Stat(fullPath)
	if err != nil || fi.IsDir() {
		http.Error(w, "File Not Found", http.StatusNotFound)
		return
	}

	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", sanitizeTeslaFileName(filepath.Base(fullPath))))
	}

	// Stream with native Range support
	http.ServeFile(w, r, fullPath)
}

func handleTeslaCamDownloadZip(w http.ResponseWriter, r *http.Request) {
	rootDir := getTeslaCamRootDir()
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	eventID := strings.TrimSpace(r.URL.Query().Get("event"))

	if eventID == "" {
		http.Error(w, "Missing event parameter", http.StatusBadRequest)
		return
	}

	catFolder := "SentryClips"
	if strings.EqualFold(category, "saved") {
		catFolder = "SavedClips"
	} else if strings.EqualFold(category, "recent") {
		catFolder = "RecentClips"
	}

	eventDir := filepath.Join(rootDir, catFolder, eventID)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"TeslaCam_%s_%s.zip\"", catFolder, sanitizeTeslaFileName(eventID)))
	w.Header().Set("Cache-Control", "no-cache")

	zw := zip.NewWriter(w)
	defer zw.Close()

	// Check if it's a directory
	if fi, err := os.Stat(eventDir); err == nil && fi.IsDir() {
		entries, err := os.ReadDir(eventDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				filePath := filepath.Join(eventDir, entry.Name())
				f, err := os.Open(filePath)
				if err != nil {
					continue
				}
				info, err := entry.Info()
				if err == nil {
					hdr, _ := zip.FileInfoHeader(info)
					hdr.Name = filepath.Join(eventID, entry.Name())
					hdr.Method = zip.Store // Fast streaming without CPU compression
					if wFile, err := zw.CreateHeader(hdr); err == nil {
						_, _ = io.Copy(wFile, f)
					}
				}
				f.Close()
			}
		}
	} else {
		// Recent flat files with prefix match
		recentDir := filepath.Join(rootDir, "RecentClips")
		entries, err := os.ReadDir(recentDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if strings.HasPrefix(entry.Name(), eventID) {
					filePath := filepath.Join(recentDir, entry.Name())
					f, err := os.Open(filePath)
					if err != nil {
						continue
					}
					info, err := entry.Info()
					if err == nil {
						hdr, _ := zip.FileInfoHeader(info)
						hdr.Name = filepath.Join(eventID, entry.Name())
						hdr.Method = zip.Store
						if wFile, err := zw.CreateHeader(hdr); err == nil {
							_, _ = io.Copy(wFile, f)
						}
					}
					f.Close()
				}
			}
		}
	}
}

func handleTeslaCamRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rootDir := getTeslaCamRootDir()

	// Trigger kernel pagecache sync and remount ro to fetch newest Tesla writes
	_ = exec.Command("sync").Run()
	_ = exec.Command("mount", "-o", "remount,ro", rootDir).Run()

	resp := map[string]interface{}{
		"success": true,
		"message": "TeslaCam 存储缓存已同步刷新",
		"time":    time.Now().Format("2006-01-02 15:04:05"),
	}
	_ = json.NewEncoder(w).Encode(resp)
}
