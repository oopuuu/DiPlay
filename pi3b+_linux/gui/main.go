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
	// Generous 10px margin around button for comfortable resistive screen taps
	return x >= b.X-10 && x < b.X+b.W+10 && y >= b.Y-8 && y < b.Y+b.H+8
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

	currentTab := 0 // 0: 配对, 1: 投屏, 2: 网络, 3: 系统
	tabs := []string{"配对", "投屏", "网络", "系统"}

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
		case 0: // 配对
			tabButtons = append(tabButtons, &Button{
				ID: "btn_discoverable", X: 265, Y: 46, W: 205, H: 44,
				Text: "开启广播",
				OnClick: func() {
					mgr.MakeDiscoverable()
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_scan", X: 265, Y: 100, W: 205, H: 44,
				Text: "扫描附近设备",
				OnClick: func() {
					mgr.ScanNearbyDevices()
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_reset_bt", X: 265, Y: 154, W: 205, H: 44,
				Text: "重置蓝牙",
				OnClick: func() {
					mgr.ResetBluetooth()
				},
			})
		case 1: // 投屏
			tabButtons = append(tabButtons, &Button{
				ID: "btn_restart_diplay", X: 265, Y: 154, W: 205, H: 44,
				Text: "重启投屏服务",
				OnClick: func() {
					mgr.RestartDiPlayService()
				},
			})
		case 2: // 网络
			tabButtons = append(tabButtons, &Button{
				ID: "btn_hotspot", X: 265, Y: 56, W: 205, H: 48,
				Text: "开启车载热点",
				OnClick: func() {
					mgr.SwitchToCarHotspot()
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_home_wifi", X: 265, Y: 120, W: 205, H: 48,
				Text: "连接家庭网络",
				OnClick: func() {
					mgr.SwitchToHomeWifi()
				},
			})
		case 3: // 系统
			tabButtons = append(tabButtons, &Button{
				ID: "btn_reboot", X: 265, Y: 70, W: 205, H: 48,
				Text: "重启树莓派",
				OnClick: func() {
					mgr.RebootPi()
				},
			})
		}
	}

	rebuildButtons()

	// Touch cursor visualization
	touchX := -1
	touchY := -1
	touchRawX := 0
	touchRawY := 0
	lastTouchTime := time.Time{}
	lastTriggerTime := time.Time{}

	for {
		select {
		case <-sigChan:
			log.Println("[DiPlay-GUI] Exiting...")
			fb.Clear(ColBg)
			_ = fb.SwapBuffers()
			return

		case ev := <-input.Events:
			touchX = ev.X
			touchY = ev.Y
			touchRawX = ev.RawX
			touchRawY = ev.RawY
			lastTouchTime = time.Now()

			// Trigger immediately on touch press (fast responsive touch)
			// Debounce 180ms to avoid double tap bouncing
			if ev.Pressed && time.Since(lastTriggerTime) > 180*time.Millisecond {
				lastTriggerTime = time.Now()

				// 1. Check Bottom Dock Tabs (Y: 260 ~ 320)
				if ev.Y >= 260 {
					tabW := (ScreenWidth - 16) / len(tabs)
					for i := range tabs {
						tx := 8 + i*tabW
						if ev.X >= tx-8 && ev.X < tx+tabW+8 {
							if currentTab != i {
								currentTab = i
								rebuildButtons()
								mgr.SetBanner("切换到标签: " + tabs[i])
							}
							break
						}
					}
				} else {
					// 2. Check active Tab buttons
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
			fb.DrawText(36, 9, "DiPlay", ColPrimary, 1)

			// Center Time & Temp
			nowStr := time.Now().Format("15:04:05")
			timeText := fmt.Sprintf("%s · %.1f度", nowStr, status.CpuTemp)
			fb.DrawText(155, 9, timeText, ColWhite, 1)

			// Right Network / Touch Coordinates Badge
			rightInfo := status.LocalIP
			if time.Since(lastTouchTime) < 3*time.Second && touchX >= 0 {
				rightInfo = fmt.Sprintf("触控:%d,%d 原:%d,%d", touchX, touchY, touchRawX, touchRawY)
			}
			fb.DrawText(300, 9, rightInfo, ColSuccess, 1)
			fb.DrawHLine(0, 34, ScreenWidth, ColBorder)

			// 3. Render Main Content based on Tab (Y: 42 ~ 258)
			switch currentTab {
			case 0: // 📱 配对
				// Left Card: Bluetooth & CarPlay Info
				fb.DrawCard(10, 42, 245, 218, "CarPlay 蓝牙配对")
				fb.DrawIcon(20, 78, "bluetooth", ColPrimary)
				fb.DrawText(38, 77, "设备: "+status.BtAlias, ColWhite, 1)

				// Status badge
				if status.BtDiscoverable {
					fb.DrawBadge(20, 102, "正在广播中", ColSuccess, ColWhite)
				} else {
					fb.DrawBadge(20, 102, "未开启广播", ColWarning, ColBlack)
				}

				fb.DrawText(20, 134, "地址: "+status.BtMac, ColTextMuted, 1)
				fb.DrawText(20, 154, "密码: 自动免密 (0000)", ColTextMuted, 1)

				fb.DrawText(20, 180, "连接手机:", ColTextMuted, 1)
				phoneDisplay := status.ConnectedPhone
				if phoneDisplay == "" || phoneDisplay == "Waiting for iPhone..." {
					phoneDisplay = "等待 iPhone 连接..."
				}
				if len([]rune(phoneDisplay)) > 14 {
					phoneDisplay = string([]rune(phoneDisplay)[:14]) + "..."
				}
				fb.DrawText(20, 198, phoneDisplay, ColPrimary, 1)

				fb.DrawText(20, 226, "指引: 手机蓝牙搜索连接", ColTextMuted, 1)

				// Right Action Buttons
				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 1: // 🚗 投屏
				// Left Card: Streaming Pipeline
				fb.DrawCard(10, 42, 245, 218, "车载投屏管道")

				if status.DiPlayActive {
					fb.DrawBadge(20, 76, "服务在线 (8088)", ColSuccess, ColWhite)
				} else {
					fb.DrawBadge(20, 76, "服务离线", ColDanger, ColWhite)
				}

				clientStr := fmt.Sprintf("车机连接: %d 台", status.WebClients)
				fb.DrawText(20, 110, clientStr, ColWhite, 1)

				vStr := "视频推流: 7001 (就绪 60帧)"
				if !status.VideoFeedReady {
					vStr = "视频推流: 7001 (等待推流)"
				}
				fb.DrawText(20, 135, vStr, ColTextMuted, 1)

				aStr := "音频推流: 7002 (就绪 PCM)"
				if !status.AudioFeedReady {
					aStr = "音频推流: 7002 (等待推流)"
				}
				fb.DrawText(20, 160, aStr, ColTextMuted, 1)
				fb.DrawText(20, 188, "极速低延迟解码", ColPrimary, 1)
				fb.DrawText(20, 212, "车机全屏自适应", ColTextMuted, 1)

				// Right Card: Access Guide & Button
				fb.DrawCard(265, 42, 205, 96, "车机访问网址")
				url := fmt.Sprintf("http://%s:8088", status.LocalIP)
				if status.IsAPMode {
					url = "http://192.168.43.1"
				}
				fb.DrawText(275, 78, url, ColPrimary, 1)
				fb.DrawText(275, 102, "车机浏览器输入网址", ColTextMuted, 1)

				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 2: // 🌐 网络
				// Left Card: Current Network
				fb.DrawCard(10, 42, 245, 218, "网络连接状态")
				fb.DrawText(20, 78, "模式: "+status.NetworkMode, ColWhite, 1)
				fb.DrawText(20, 106, "IP: "+status.LocalIP, ColPrimary, 1)

				fb.DrawText(20, 138, "特斯拉 204 劫持:", ColWhite, 1)
				fb.DrawBadge(20, 158, "已激活 (杜绝断网弹窗)", ColSuccess, ColWhite)

				fb.DrawText(20, 196, "车载热点: Tesla-CarPlay", ColTextMuted, 1)
				fb.DrawText(20, 220, "热点密码: diplay123456", ColTextMuted, 1)

				// Right Buttons
				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 3: // ⚙️ 系统
				// Left Card: System Diagnostics
				fb.DrawCard(10, 42, 245, 218, "系统运行状态")
				fb.DrawText(20, 78, fmt.Sprintf("核心温度: %.1f度", status.CpuTemp), ColWhite, 1)
				fb.DrawText(20, 106, fmt.Sprintf("开机运行: %s", status.UptimeStr), ColTextMuted, 1)
				fb.DrawText(20, 134, fmt.Sprintf("内存占用: %d MB / %d MB", status.MemUsedMB, status.MemTotalMB), ColTextMuted, 1)
				fb.DrawText(20, 162, "内核: Linux 6.6 aarch64", ColTextMuted, 1)
				fb.DrawText(20, 190, "屏幕: 3.5寸 480x320", ColPrimary, 1)
				fb.DrawText(20, 218, "触控: XPT2046 驱动就绪", ColSuccess, 1)

				// Right Buttons
				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}
			}

			// 4. Render Bottom Dock (Y: 264 ~ 316)
			dockY := 264
			tabW := (ScreenWidth - 16) / len(tabs)
			for i, t := range tabs {
				tx := 8 + i*tabW
				isActive := (i == currentTab)
				fb.DrawButton(tx, dockY, tabW-4, 50, t, isActive, false, ColPrimary)
			}

			// 5. Toast Banner Message (if any)
			if time.Now().Before(status.BannerExpires) && status.BannerMsg != "" {
				bannerW := 400
				bannerH := 40
				bx := (ScreenWidth - bannerW) / 2
				by := 115
				fb.FillRoundRect(bx, by, bannerW, bannerH, 8, ColActiveTab)
				fb.DrawRoundRect(bx, by, bannerW, bannerH, 8, ColPrimary)
				fb.DrawText(bx+20, by+12, status.BannerMsg, ColWhite, 1)
			}

			// 6. Draw Touch Indicator (Red cursor with crosshair for instant touch feedback)
			if time.Since(lastTouchTime) < 1*time.Second && touchX >= 0 {
				fb.FillRoundRect(touchX-5, touchY-5, 10, 10, 5, ColDanger)
				fb.DrawRoundRect(touchX-12, touchY-12, 24, 24, 12, ColWarning)
				fb.DrawHLine(touchX-18, touchY, 36, ColWarning)
				fb.DrawVLine(touchX, touchY-18, 36, ColWarning)
			}

			// 7. Push buffer to Framebuffer (0-flicker double buffering)
			_ = fb.SwapBuffers()
		}
	}
}
