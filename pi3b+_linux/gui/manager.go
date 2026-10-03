package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SystemStatus struct {
	mu sync.RWMutex

	// Hardware
	CpuTemp   float64 // °C
	UptimeStr string
	MemUsedMB int
	MemTotalMB int

	// Network
	NetworkMode string // "WiFi: Xiaomi_1803" or "AP: Tesla-CarPlay"
	LocalIP     string
	IsAPMode    bool

	// Bluetooth & CarPlay
	BtPowered      bool
	BtDiscoverable bool
	BtPairable     bool
	BtAlias        string
	BtMac          string
	ConnectedPhone string // e.g. "iPhone 15 Pro (A4:83:E7:XX:XX)" or "等待 iPhone 连接..."
	IsPairing      bool
	ScannedDevices []string

	// DiPlay Web Remote Service
	DiPlayActive   bool
	WebClients     int
	VideoFeedReady bool
	AudioFeedReady bool
	FPS            int

	// User Actions & Notifications
	BannerMsg     string
	BannerExpires time.Time
}

type ServiceStatusResponse struct {
	Service        string `json:"service"`
	Port           int    `json:"port"`
	ActiveClients  int    `json:"activeClients"`
	VideoFeedReady bool   `json:"videoFeedReady"`
	AudioFeedReady bool   `json:"audioFeedReady"`
}

type Manager struct {
	Status *SystemStatus
	client *http.Client
}

func NewManager() *Manager {
	m := &Manager{
		Status: &SystemStatus{
			BtAlias:        "Tesla-CarPlay",
			ConnectedPhone: "Waiting for iPhone...",
		},
		client: &http.Client{Timeout: 1 * time.Second},
	}

	// Initial poll & start background refresh loops
	go m.loopSysInfo()
	go m.loopBluetooth()
	go m.loopDiPlayStatus()

	return m
}

func (m *Manager) loopSysInfo() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		m.updateHardware()
		m.updateNetwork()
		<-ticker.C
	}
}

func (m *Manager) updateHardware() {
	m.Status.mu.Lock()
	defer m.Status.mu.Unlock()

	// CPU Temp
	if data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
			m.Status.CpuTemp = val / 1000.0
		}
	}

	// Uptime
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) > 0 {
			if secs, err := strconv.ParseFloat(parts[0], 64); err == nil {
				h := int(secs) / 3600
				mins := (int(secs) % 3600) / 60
				m.Status.UptimeStr = fmt.Sprintf("%dh %02dm", h, mins)
			}
		}
	}

	// Meminfo
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(data), "\n")
		var total, avail int
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					total, _ = strconv.Atoi(fields[1])
				}
			} else if strings.HasPrefix(line, "MemAvailable:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					avail, _ = strconv.Atoi(fields[1])
				}
			}
		}
		if total > 0 {
			m.Status.MemTotalMB = total / 1024
			m.Status.MemUsedMB = (total - avail) / 1024
		}
	}
}

func (m *Manager) updateNetwork() {
	m.Status.mu.Lock()
	defer m.Status.mu.Unlock()

	// Find non-loopback IP
	addrs, err := net.InterfaceAddrs()
	ipFound := ""
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() != nil {
					ip := ipNet.IP.String()
					if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "172.") {
						ipFound = ip
						break
					}
				}
			}
		}
	}
	if ipFound != "" {
		m.Status.LocalIP = ipFound
	} else {
		m.Status.LocalIP = "未联网"
	}

	// Check if in Hotspot mode (192.168.43.1)
	if strings.HasPrefix(m.Status.LocalIP, "192.168.43.") {
		m.Status.IsAPMode = true
		m.Status.NetworkMode = "车载热点 (Tesla-CarPlay)"
	} else {
		m.Status.IsAPMode = false
		m.Status.NetworkMode = "Wi-Fi (Xiaomi_1803)"
	}
}

func (m *Manager) loopBluetooth() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		m.refreshBluetooth()
		<-ticker.C
	}
}

func (m *Manager) refreshBluetooth() {
	// First check /run/diplay_bt_state.json produced by bt_agent.py
	if data, err := os.ReadFile("/run/diplay_bt_state.json"); err == nil {
		var btState struct {
			Alias           string `json:"alias"`
			Powered         bool   `json:"powered"`
			Discoverable    bool   `json:"discoverable"`
			Pairable        bool   `json:"pairable"`
			ConnectedDevice string `json:"connected_device"`
			ConnectedMac    string `json:"connected_mac"`
			LastEvent       string `json:"last_event"`
		}
		if err := json.Unmarshal(data, &btState); err == nil {
			m.Status.mu.Lock()
			m.Status.BtAlias = btState.Alias
			m.Status.BtPowered = btState.Powered
			m.Status.BtDiscoverable = btState.Discoverable
			m.Status.BtPairable = btState.Pairable
			if btState.ConnectedDevice != "" {
				m.Status.ConnectedPhone = btState.ConnectedDevice
			} else if btState.ConnectedMac != "" {
				m.Status.ConnectedPhone = btState.ConnectedMac
			}
			m.Status.mu.Unlock()
		}
	}

	out, err := exec.Command("bluetoothctl", "show").Output()
	if err != nil {
		return
	}

	m.Status.mu.Lock()
	defer m.Status.mu.Unlock()

	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "Controller") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				m.Status.BtMac = parts[1]
			}
		} else if strings.HasPrefix(trimmed, "Alias:") {
			m.Status.BtAlias = strings.TrimPrefix(trimmed, "Alias: ")
		} else if strings.HasPrefix(trimmed, "Powered:") {
			m.Status.BtPowered = strings.Contains(trimmed, "yes")
		} else if strings.HasPrefix(trimmed, "Discoverable:") {
			m.Status.BtDiscoverable = strings.Contains(trimmed, "yes")
		} else if strings.HasPrefix(trimmed, "Pairable:") {
			m.Status.BtPairable = strings.Contains(trimmed, "yes")
		}
	}

	// Check paired / connected devices
	devOut, err := exec.Command("bluetoothctl", "devices").Output()
	if err == nil && len(devOut) > 0 {
		devLines := strings.Split(strings.TrimSpace(string(devOut)), "\n")
		var phoneList []string
		for _, dl := range devLines {
			fields := strings.Fields(dl)
			// Device XX:XX:XX:XX:XX:XX Name
			if len(fields) >= 3 {
				name := strings.Join(fields[2:], " ")
				phoneList = append(phoneList, fmt.Sprintf("%s (%s)", name, fields[1]))
			}
		}
		if len(phoneList) > 0 {
			m.Status.ConnectedPhone = phoneList[0]
		}
	}
}

func (m *Manager) loopDiPlayStatus() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		resp, err := m.client.Get("http://127.0.0.1:8088/api/status")
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var stat ServiceStatusResponse
			if err := json.Unmarshal(body, &stat); err == nil {
				m.Status.mu.Lock()
				m.Status.DiPlayActive = true
				m.Status.WebClients = stat.ActiveClients
				m.Status.VideoFeedReady = stat.VideoFeedReady
				m.Status.AudioFeedReady = stat.AudioFeedReady
				m.Status.mu.Unlock()
			}
		} else {
			m.Status.mu.Lock()
			m.Status.DiPlayActive = false
			m.Status.mu.Unlock()
		}
		<-ticker.C
	}
}

// User Action Handlers
func (m *Manager) SetBanner(msg string) {
	m.Status.mu.Lock()
	m.Status.BannerMsg = msg
	m.Status.BannerExpires = time.Now().Add(4 * time.Second)
	m.Status.mu.Unlock()
}

func (m *Manager) MakeDiscoverable() {
	m.SetBanner("Activating Beacon: Search 'Tesla-CarPlay' on iPhone")
	go func() {
		_ = exec.Command("bluetoothctl", "system-alias", "Tesla-CarPlay").Run()
		_ = exec.Command("bluetoothctl", "discoverable", "on").Run()
		_ = exec.Command("bluetoothctl", "pairable", "on").Run()
		m.refreshBluetooth()
	}()
}

func (m *Manager) ScanNearbyDevices() {
	m.SetBanner("Scanning nearby Bluetooth devices (8s)...")
	go func() {
		cmd := exec.Command("bluetoothctl", "scan", "on")
		_ = cmd.Start()
		time.Sleep(8 * time.Second)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		m.refreshBluetooth()
		m.SetBanner("Scan complete! Check list")
	}()
}

func (m *Manager) ResetBluetooth() {
	m.SetBanner("Resetting Bluetooth controller...")
	go func() {
		_ = exec.Command("sudo", "systemctl", "restart", "bluetooth").Run()
		time.Sleep(2 * time.Second)
		_ = exec.Command("bluetoothctl", "power", "on").Run()
		_ = exec.Command("bluetoothctl", "discoverable", "on").Run()
		_ = exec.Command("bluetoothctl", "pairable", "on").Run()
		_ = exec.Command("bluetoothctl", "system-alias", "Tesla-CarPlay").Run()
		m.refreshBluetooth()
		m.SetBanner("Bluetooth Ready: Discoverable ON")
	}()
}

func (m *Manager) SwitchToCarHotspot() {
	m.SetBanner("Switching to Tesla Hotspot (192.168.43.1)...")
	go func() {
		_ = exec.Command("sudo", "nmcli", "connection", "up", "Tesla-CarPlay").Run()
		time.Sleep(3 * time.Second)
		m.updateNetwork()
		m.SetBanner("Car Hotspot Active: 192.168.43.1")
	}()
}

func (m *Manager) SwitchToHomeWifi() {
	m.SetBanner("Connecting to Home Wi-Fi (Xiaomi_1803)...")
	go func() {
		_ = exec.Command("sudo", "nmcli", "connection", "up", "Xiaomi_1803").Run()
		time.Sleep(3 * time.Second)
		m.updateNetwork()
		m.SetBanner("Connected to Home Wi-Fi")
	}()
}

func (m *Manager) RestartDiPlayService() {
	m.SetBanner("Restarting DiPlay Web Streamer...")
	go func() {
		_ = exec.Command("sudo", "systemctl", "restart", "diplay-pi").Run()
		time.Sleep(1 * time.Second)
		m.SetBanner("DiPlay Streamer Restarted")
	}()
}

func (m *Manager) RebootPi() {
	m.SetBanner("Rebooting system in 2s...")
	go func() {
		time.Sleep(2 * time.Second)
		_ = exec.Command("sudo", "reboot").Run()
	}()
}
