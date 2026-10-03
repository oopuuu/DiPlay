package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Button struct {
	ID      string
	X, Y    int
	W, H    int
	Text    string
	Active  bool
	Pressed bool
	OnClick func()
}

func (b *Button) Hit(x, y int) bool {
	return x >= b.X && x < b.X+b.W && y >= b.Y && y < b.Y+b.H
}

func main() {
	fbPath := flag.String("fb", "/dev/fb1", "Path to framebuffer device")
	touchPath := flag.String("touch", "/dev/input/event4", "Path to touchscreen event device")
	mousePath := flag.String("mouse", "/dev/input/mice", "Path to mouse device")
	flag.Parse()

	log.Printf("[DiPlay-GUI] Initializing 3.5-inch Car GUI (480x320)...")

	fb, err := NewFramebuffer(*fbPath)
	if err != nil {
		log.Fatalf("[DiPlay-GUI] Framebuffer init failed: %v", err)
	}
	defer fb.Close()

	input := NewInputManager(*touchPath, *mousePath)
	defer input.Close()

	mgr := NewManager()

	// Initial trigger to ensure Bluetooth is active & discoverable
	go func() {
		time.Sleep(1 * time.Second)
		mgr.MakeDiscoverable()
	}()

	currentTab := 0 // 0: Pairing, 1: Stream, 2: Network, 3: System
	tabs := []string{"Pairing", "Stream", "Network", "System"}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
	defer ticker.Stop()

	// Buttons for active Tab
	var tabButtons []*Button

	rebuildButtons := func() {
		tabButtons = nil

		switch currentTab {
		case 0: // Pairing
			tabButtons = append(tabButtons, &Button{
				ID: "btn_discoverable", X: 265, Y: 46, W: 205, H: 44,
				Text: "📡 Broadcast Beacon",
				OnClick: func() {
					mgr.MakeDiscoverable()
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_scan", X: 265, Y: 100, W: 205, H: 44,
				Text: "🔍 Scan Devices",
				OnClick: func() {
					mgr.ScanNearbyDevices()
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_reset_bt", X: 265, Y: 154, W: 205, H: 44,
				Text: "🔄 Reset Bluetooth",
				OnClick: func() {
					mgr.ResetBluetooth()
				},
			})
		case 1: // Stream
			tabButtons = append(tabButtons, &Button{
				ID: "btn_restart_diplay", X: 265, Y: 154, W: 205, H: 44,
				Text: "🔄 Restart Streamer",
				OnClick: func() {
					mgr.RestartDiPlayService()
				},
			})
		case 2: // Network
			tabButtons = append(tabButtons, &Button{
				ID: "btn_hotspot", X: 265, Y: 56, W: 205, H: 48,
				Text: "🚗 Start Tesla Hotspot",
				OnClick: func() {
					mgr.SwitchToCarHotspot()
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_home_wifi", X: 265, Y: 120, W: 205, H: 48,
				Text: "🏠 Connect Home Wi-Fi",
				OnClick: func() {
					mgr.SwitchToHomeWifi()
				},
			})
		case 3: // System
			tabButtons = append(tabButtons, &Button{
				ID: "btn_reboot", X: 265, Y: 70, W: 205, H: 48,
				Text: "🔄 Reboot Raspberry Pi",
				OnClick: func() {
					mgr.RebootPi()
				},
			})
		}
	}

	rebuildButtons()

	// Touch ripple effect
	var touchRippleX, touchRippleY int
	var touchRippleR int

	for {
		select {
		case <-sigChan:
			log.Println("[DiPlay-GUI] Exiting...")
			fb.Clear(ColBg)
			_ = fb.SwapBuffers()
			return

		case ev := <-input.Events:
			if ev.Pressed {
				touchRippleX = ev.X
				touchRippleY = ev.Y
				touchRippleR = 3
			}
			if ev.Released {
				// Check Bottom Dock Tabs (Y: 266 ~ 316)
				if ev.Y >= 264 {
					tabW := (ScreenWidth - 16) / len(tabs)
					for i := range tabs {
						tx := 8 + i*tabW
						if ev.X >= tx && ev.X < tx+tabW {
							if currentTab != i {
								currentTab = i
								rebuildButtons()
							}
							break
						}
					}
				} else {
					// Check active action buttons
					for _, b := range tabButtons {
						if b.Hit(ev.X, ev.Y) {
							if b.OnClick != nil {
								b.OnClick()
							}
							break
						}
					}
				}
			}

		case <-ticker.C:
			// Read status snapshots
			mgr.Status.mu.RLock()
			status := *mgr.Status
			mgr.Status.mu.RUnlock()

			// 1. Clear background
			fb.Clear(ColBg)

			// 2. Render Header (Y: 0 ~ 34)
			fb.FillRect(0, 0, ScreenWidth, 34, ColCardBg)
			fb.DrawIcon(12, 9, "car", ColPrimary)
			fb.DrawText(36, 9, "DiPlay-Pi", ColPrimary, 1)

			// Center Time & Temp
			nowStr := time.Now().Format("15:04:05")
			timeText := fmt.Sprintf("%s · %.1f'C", nowStr, status.CpuTemp)
			fb.DrawText(175, 9, timeText, ColWhite, 1)

			// Right Network Badge
			ipDisplay := status.LocalIP
			if ipDisplay == "" {
				ipDisplay = "192.168.31.52"
			}
			fb.DrawText(335, 9, ipDisplay, ColSuccess, 1)
			fb.DrawHLine(0, 34, ScreenWidth, ColBorder)

			// 3. Render Main Content based on Tab (Y: 42 ~ 260)
			switch currentTab {
			case 0: // 📱 Pairing
				// Left Card: Bluetooth & CarPlay Info
				fb.DrawCard(10, 42, 245, 218, "CarPlay Pairing Status")
				fb.DrawIcon(20, 78, "bluetooth", ColPrimary)
				fb.DrawText(38, 77, "BT: "+status.BtAlias, ColWhite, 1)

				// Status badge
				if status.BtDiscoverable {
					fb.DrawBadge(20, 102, "DISCOVERABLE (READY)", ColSuccess, ColWhite)
				} else {
					fb.DrawBadge(20, 102, "NOT DISCOVERABLE", ColWarning, ColBlack)
				}

				fb.DrawText(20, 134, "MAC: "+status.BtMac, ColTextMuted, 1)
				fb.DrawText(20, 154, "PIN: 0000 (Auto Accept)", ColTextMuted, 1)

				fb.DrawText(20, 180, "Active Phone:", ColTextMuted, 1)
				phoneDisplay := status.ConnectedPhone
				if phoneDisplay == "" {
					phoneDisplay = "Waiting for iPhone..."
				}
				if len(phoneDisplay) > 24 {
					phoneDisplay = phoneDisplay[:24] + "..."
				}
				fb.DrawText(20, 198, phoneDisplay, ColPrimary, 1)

				fb.DrawText(20, 226, "Guide: Connect in iPhone BT", ColTextMuted, 1)

				// Right Action Buttons
				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 1: // 🚗 Stream
				// Left Card: Streaming Pipeline
				fb.DrawCard(10, 42, 245, 218, "Tesla Streaming Feed")

				if status.DiPlayActive {
					fb.DrawBadge(20, 76, "ACTIVE (Port 8088)", ColSuccess, ColWhite)
				} else {
					fb.DrawBadge(20, 76, "OFFLINE", ColDanger, ColWhite)
				}

				clientStr := fmt.Sprintf("Web Clients: %d Connected", status.WebClients)
				fb.DrawText(20, 110, clientStr, ColWhite, 1)

				vStr := "Video Ingest: 7001 (Ready 60FPS)"
				if !status.VideoFeedReady {
					vStr = "Video Ingest: 7001 (Idle)"
				}
				fb.DrawText(20, 135, vStr, ColTextMuted, 1)

				aStr := "Audio Ingest: 7002 (Ready PCM)"
				if !status.AudioFeedReady {
					aStr = "Audio Ingest: 7002 (Idle)"
				}
				fb.DrawText(20, 160, aStr, ColTextMuted, 1)
				fb.DrawText(20, 188, "WebCodecs Ultra-Low Latency", ColPrimary, 1)
				fb.DrawText(20, 212, "Auto-Adaptive Tesla Fullscreen", ColTextMuted, 1)

				// Right Card: Access Guide & Button
				fb.DrawCard(265, 42, 205, 96, "Browser Address")
				url := fmt.Sprintf("http://%s:8088", status.LocalIP)
				if status.IsAPMode {
					url = "http://192.168.43.1"
				}
				fb.DrawText(275, 78, url, ColPrimary, 1)
				fb.DrawText(275, 102, "Open URL in Tesla Browser", ColTextMuted, 1)

				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 2: // 🌐 Network
				// Left Card: Current Network
				fb.DrawCard(10, 42, 245, 218, "Network Diagnostics")
				fb.DrawText(20, 78, "Mode: "+status.NetworkMode, ColWhite, 1)
				fb.DrawText(20, 106, "IP: "+status.LocalIP, ColPrimary, 1)

				fb.DrawText(20, 138, "Tesla Fake-204 Captive:", ColWhite, 1)
				fb.DrawBadge(20, 158, "ACTIVE (No Dialog Disconnect)", ColSuccess, ColWhite)

				fb.DrawText(20, 196, "Car Hotspot: Tesla-CarPlay", ColTextMuted, 1)
				fb.DrawText(20, 220, "Hotspot PWD: diplay123456", ColTextMuted, 1)

				// Right Buttons
				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 3: // ⚙️ System
				// Left Card: System Diagnostics
				fb.DrawCard(10, 42, 245, 218, "System Health")
				fb.DrawText(20, 78, fmt.Sprintf("CPU Temp: %.1f'C", status.CpuTemp), ColWhite, 1)
				fb.DrawText(20, 106, fmt.Sprintf("Uptime:   %s", status.UptimeStr), ColTextMuted, 1)
				fb.DrawText(20, 134, fmt.Sprintf("Memory:   %d MB / %d MB", status.MemUsedMB, status.MemTotalMB), ColTextMuted, 1)
				fb.DrawText(20, 162, "Kernel:   Linux 6.6 aarch64", ColTextMuted, 1)
				fb.DrawText(20, 190, "Display:  3.5in ILI9486 (480x320)", ColPrimary, 1)
				fb.DrawText(20, 218, "Touch:    XPT2046 SPI Active", ColSuccess, 1)

				// Right Buttons
				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}
			}

			// 4. Render Bottom Dock (Y: 266 ~ 316)
			dockY := 266
			tabW := (ScreenWidth - 16) / len(tabs)
			for i, t := range tabs {
				tx := 8 + i*tabW
				isActive := (i == currentTab)
				fb.DrawButton(tx, dockY, tabW-4, 48, t, isActive, false, ColPrimary)
			}

			// 5. Toast Banner Message (if any)
			if time.Now().Before(status.BannerExpires) && status.BannerMsg != "" {
				bannerW := 420
				bannerH := 40
				bx := (ScreenWidth - bannerW) / 2
				by := 115
				fb.FillRoundRect(bx, by, bannerW, bannerH, 8, ColActiveTab)
				fb.DrawRoundRect(bx, by, bannerW, bannerH, 8, ColPrimary)
				fb.DrawText(bx+18, by+12, status.BannerMsg, ColWhite, 1)
			}

			// 6. Draw touch ripple feedback
			if touchRippleR > 0 && touchRippleR < 18 {
				fb.DrawRoundRect(touchRippleX-touchRippleR, touchRippleY-touchRippleR, touchRippleR*2, touchRippleR*2, touchRippleR, ColPrimary)
				touchRippleR += 3
			}

			// 7. Push buffer to Framebuffer (0-flicker double buffering)
			_ = fb.SwapBuffers()
		}
	}
}
