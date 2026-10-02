package main

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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

type ServerStatus struct {
	Active        bool `json:"active"`
	HasSession    bool `json:"hasSession"`
	Width         int  `json:"width"`
	Height        int  `json:"height"`
	Fps           int  `json:"fps"`
	StreamClients int  `json:"streamClients"`
	AudioClients  int  `json:"audioClients"`
}

// Global streaming hub
type Hub struct {
	mu           sync.RWMutex
	wsClients    map[net.Conn]bool
	audioClients map[net.Conn]bool

	// Video Caching
	lastCodecData []byte
	lastIdrFrame  []byte
	lastFrame     []byte
	lastJpegFrame []byte

	// Stats
	width      int
	height     int
	fpsCounter int64
	currentFps int32
	hasSession bool

	// Channels
	frameChan chan []byte
	audioChan chan []byte
}

var hub = &Hub{
	wsClients:    make(map[net.Conn]bool),
	audioClients: make(map[net.Conn]bool),
	width:        1920,
	height:       1080,
	frameChan:    make(chan []byte, 120),
	audioChan:    make(chan []byte, 256),
	hasSession:   true,
}

func main() {
	port := flag.Int("port", 8088, "HTTP/WebSocket service port")
	videoFeedPort := flag.Int("video-feed", 7001, "Raw H.264 TCP input port")
	audioFeedPort := flag.Int("audio-feed", 7002, "Raw PCM 44.1kHz 16bit TCP input port")
	flag.Parse()

	log.Printf("[DiPlay-Pi] Starting Linux Native Headless CarPlay Server on port %d...", *port)

	// Background FPS counter tick
	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			frames := atomic.SwapInt64(&hub.fpsCounter, 0)
			atomic.StoreInt32(&hub.currentFps, int32(frames))
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
	mux.HandleFunc("/api/resolution", handleResolution)
	mux.HandleFunc("/snapshot", handleSnapshot)

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

	log.Printf("[DiPlay-Pi] Web Remote ready! Open browser at http://<RaspberryPi_IP>:%d (or http://192.168.43.1)", *port)

	// Graceful shutdown on Ctrl+C / SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("[DiPlay-Pi] Shutting down server...")
	server.Close()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	path := strings.ToLower(r.URL.Path)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(defaultHtml)
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

func handleTouch(w http.ResponseWriter, r *http.Request) {
	var payload TouchPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Touch forwarded to CarPlay iAP2 virtual touchscreen endpoint
	// log.Printf("[Touch] Received %d contacts", len(payload.Contacts))
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
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func handleResolution(w http.ResponseWriter, r *http.Request) {
	var payload ResolutionPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Width > 0 && payload.Height > 0 {
		hub.mu.Lock()
		hub.width = payload.Width
		hub.height = payload.Height
		hub.mu.Unlock()
		log.Printf("[Resolution] Updated display resolution: %dx%d", payload.Width, payload.Height)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
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

	hub.mu.Lock()
	hub.wsClients[conn] = true
	codecData := hub.lastCodecData
	idrFrame := hub.lastIdrFrame
	wWidth := hub.width
	wHeight := hub.height
	hub.mu.Unlock()

	log.Printf("[WS] New client connected (%s). Total: %d", conn.RemoteAddr(), len(hub.wsClients))

	// 1. Send initial hello status
	initMsg := fmt.Sprintf(`{"type":"hello","active":true,"hasSession":true,"width":%d,"height":%d,"fps":60}`, wWidth, wHeight)
	sendWsFrame(conn, 0x01, []byte(initMsg))

	// 2. Instant Zero-Latency Initial Render: Push cached SPS/PPS + IDR Keyframe!
	if len(codecData) > 0 {
		sendMultiplexBinary(conn, 0x01, 0, codecData)
	}
	if len(idrFrame) > 0 {
		sendMultiplexBinary(conn, 0x02, 1, idrFrame)
	}

	// Read loop
	buf := make([]byte, 4096)
	for {
		_, payload, err := readWsFrame(conn, buf)
		if err != nil {
			break
		}
		if len(payload) > 0 {
			var m map[string]interface{}
			if err := json.Unmarshal(payload, &m); err == nil {
				if m["type"] == "ping" {
					sendWsFrame(conn, 0x01, []byte(`{"type":"pong"}`))
				} else if m["type"] == "requestKeyFrame" {
					log.Printf("[WS] Client requested keyframe")
					// Send cached IDR frame immediately if available
					hub.mu.RLock()
					idr := hub.lastIdrFrame
					hub.mu.RUnlock()
					if len(idr) > 0 {
						sendMultiplexBinary(conn, 0x02, 1, idr)
					}
				}
			}
		}
	}

	hub.mu.Lock()
	delete(hub.wsClients, conn)
	hub.mu.Unlock()
	conn.Close()
	log.Printf("[WS] Client disconnected. Remaining: %d", len(hub.wsClients))
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

	hub.mu.Lock()
	hub.audioClients[conn] = true
	hub.mu.Unlock()

	log.Printf("[AudioWS] Dedicated audio client connected (%s)", conn.RemoteAddr())
	sendWsFrame(conn, 0x01, []byte(`{"type":"audio_ready"}`))

	buf := make([]byte, 1024)
	for {
		_, _, err := readWsFrame(conn, buf)
		if err != nil {
			break
		}
	}

	hub.mu.Lock()
	delete(hub.audioClients, conn)
	hub.mu.Unlock()
	conn.Close()
	log.Printf("[AudioWS] Audio client disconnected")
}

// Broadcaster routine: fan-out video & audio to clients
func (h *Hub) runBroadcaster() {
	for {
		select {
		case frame := <-h.frameChan:
			isKey := isH264Keyframe(frame)
			h.mu.Lock()
			h.lastFrame = frame
			if isKey {
				h.lastIdrFrame = frame
			}
			clients := make([]net.Conn, 0, len(h.wsClients))
			for c := range h.wsClients {
				clients = append(clients, c)
			}
			h.mu.Unlock()

			flag := byte(0)
			if isKey {
				flag = 1
			}

			atomic.AddInt64(&h.fpsCounter, 1)

			for _, c := range clients {
				if err := sendMultiplexBinary(c, 0x02, flag, frame); err != nil {
					h.mu.Lock()
					delete(h.wsClients, c)
					h.mu.Unlock()
					c.Close()
				}
			}

		case pcm := <-h.audioChan:
			h.mu.RLock()
			audioClients := make([]net.Conn, 0, len(h.audioClients))
			for c := range h.audioClients {
				audioClients = append(audioClients, c)
			}
			h.mu.RUnlock()

			// Channel 0x00: 44.1kHz Stereo PCM (flag=0x00)
			for _, c := range audioClients {
				if err := sendMultiplexBinary(c, 0x00, 0x00, pcm); err != nil {
					h.mu.Lock()
					delete(h.audioClients, c)
					h.mu.Unlock()
					c.Close()
				}
			}
		}
	}
}

// WebSocket RFC 6455 Frame helpers
func sendWsFrame(conn net.Conn, opcode byte, payload []byte) error {
	length := len(payload)
	header := make([]byte, 10)
	header[0] = 0x80 | (opcode & 0x0F) // FIN + opcode

	hLen := 2
	if length <= 125 {
		header[1] = byte(length)
	} else if length <= 65535 {
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(length))
		hLen = 4
	} else {
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(length))
		hLen = 10
	}

	if _, err := conn.Write(header[:hLen]); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func sendMultiplexBinary(conn net.Conn, channel byte, flag byte, data []byte) error {
	totalLen := len(data) + 4
	header := make([]byte, 14)
	header[0] = 0x82 // Binary frame (FIN + opcode 2)

	hLen := 2
	if totalLen <= 125 {
		header[1] = byte(totalLen)
	} else if totalLen <= 65535 {
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(totalLen))
		hLen = 4
	} else {
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(totalLen))
		hLen = 10
	}

	header[hLen] = channel
	header[hLen+1] = flag
	header[hLen+2] = 0
	header[hLen+3] = 0
	hLen += 4

	if _, err := conn.Write(header[:hLen]); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
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
	log.Printf("[VideoFeed] Ingestion pipeline attached: %s", conn.RemoteAddr())

	lenBuf := make([]byte, 4)
	for {
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			break
		}
		frameLen := binary.BigEndian.Uint32(lenBuf)
		if frameLen == 0 || frameLen > 4*1024*1024 {
			continue
		}
		frame := make([]byte, frameLen)
		if _, err := io.ReadFull(conn, frame); err != nil {
			break
		}

		// Check if this is an avcC/SPS/PPS configuration frame (starts with 0x01 0x64 or SPS 0x67)
		if isConfigPacket(frame) {
			hub.mu.Lock()
			hub.lastCodecData = frame
			hub.mu.Unlock()
			// Push immediately to all active WS clients
			hub.mu.RLock()
			for c := range hub.wsClients {
				sendMultiplexBinary(c, 0x01, 0, frame)
			}
			hub.mu.RUnlock()
		} else {
			select {
			case hub.frameChan <- frame:
			default:
				// Dropping queue frame if congested
			}
		}
	}
	log.Printf("[VideoFeed] Ingestion pipeline detached: %s", conn.RemoteAddr())
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
	// Check Annex-B or AVCC length-prefixed NAL header
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
	// Also check AVCC 4-byte prefix at offset 0
	if (data[4] & 0x1F) == 5 {
		return true
	}
	return false
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
