package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type MenuItem struct {
	ID       string
	Title    string
	Subtitle func(s *SystemStatus) string
	Color    uint16
	Action   func(mgr *Manager)
}

func main() {
	fbPath := flag.String("fb", "", "Path to framebuffer device (empty for auto-detect /dev/fb1 or /dev/fb0)")
	touchPath := flag.String("touch", "/dev/input/event4", "Path to touchscreen event device")
	mousePath := flag.String("mouse", "/dev/input/mice", "Path to mouse device")
	flag.Parse()

	log.Printf("[DiPlay-GUI] Initializing Gesture-Driven 3.5\" Car GUI (480x320)...")

	fb, err := NewFramebuffer(*fbPath)
	if err != nil {
		log.Fatalf("[DiPlay-GUI] Framebuffer init failed: %v", err)
	}
	defer fb.Close()

	input := NewInputManager(*touchPath, *mousePath)
	defer input.Close()

	mgr := NewManager()

	// Screen Sleep/Wake State
	isScreenOff := false

	// Menu Item Definitions (4 Core Essential Actions)
	menuItems := []MenuItem{
		{
			ID:    "restart_stream",
			Title: "重启投屏程序",
			Subtitle: func(s *SystemStatus) string {
				if s.Fps > 0 {
					return fmt.Sprintf("运行中: %d FPS · 重启服务", s.Fps)
				}
				return "重启AirPlay服务并唤醒手机"
			},
			Color: ColPrimary,
			Action: func(m *Manager) {
				m.RestartDiPlayService()
			},
		},
		{
			ID:    "toggle_wifi",
			Title: "切换网络模式",
			Subtitle: func(s *SystemStatus) string {
				if s.IsAPMode {
					band := "热点"
					if strings.Contains(s.NetworkMode, "5GHz") {
						band = "5G热点"
					} else if strings.Contains(s.NetworkMode, "2.4GHz") {
						band = "2.4G热点"
					}
					return fmt.Sprintf("当前: %s · 点击切家用网", band)
				}
				return "当前: 家用网 · 点击切热点"
			},
			Color: ColSuccess,
			Action: func(m *Manager) {
				m.ToggleNetworkMode()
			},
		},
		{
			ID:    "toggle_screen",
			Title: "关闭LCD屏幕",
			Subtitle: func(s *SystemStatus) string {
				return "息屏黑屏休眠 · 轻触屏幕唤醒"
			},
			Color: ColWarning,
			Action: func(m *Manager) {
				isScreenOff = true
				m.SetBanner("屏幕已进入休眠模式")
			},
		},
		{
			ID:    "reboot_system",
			Title: "重启树莓派",
			Subtitle: func(s *SystemStatus) string {
				return "长按确认安全重启Linux系统"
			},
			Color: ColDanger,
			Action: func(m *Manager) {
				m.RebootPi()
			},
		},
	}

	selectedItem := 0

	// Gesture State Machine (Location-Agnostic Blind Touch Control)
	isFingerDown := false
	pressStartTime := time.Time{}
	longPressFired := false
	lastShortReleaseTime := time.Time{}
	hasPendingSingleTap := false
	justWokeUp := false

	// Exit signals
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Dynamic Frame Rate Ticker: 30 FPS when active, 2 FPS when screen is asleep
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	log.Println("[DiPlay-GUI] Gesture Engine Ready: [Single Tap] Next | [Long Press] Confirm | [Double Tap] Toggle Screen")

	for {
		select {
		case <-sigChan:
			log.Println("[DiPlay-GUI] Exiting...")
			fb.Clear(ColBlack)
			_ = fb.SwapBuffers()
			return

		case ev := <-input.Events:
			if ev.Pressed {
				if !isFingerDown {
					isFingerDown = true
					pressStartTime = time.Now()
					longPressFired = false

					// If screen was off, any touch immediately wakes up the screen
					if isScreenOff {
						isScreenOff = false
						justWokeUp = true
						longPressFired = true // Do not trigger underlying actions on wake tap
						hasPendingSingleTap = false
						mgr.SetBanner("屏幕已唤醒")
						log.Println("[Gesture] Screen woken up from touch!")
					}
				}
			} else if ev.Released {
				if isFingerDown {
					isFingerDown = false

					if justWokeUp {
						justWokeUp = false
						continue
					}

					if longPressFired {
						// Long press was already executed while holding down
						continue
					}

					// It's a short release (< 800ms)
					pressDuration := time.Since(pressStartTime)
					if pressDuration < 800*time.Millisecond {
						now := time.Now()
						// Check for Double Tap (release interval < 380ms)
						if !lastShortReleaseTime.IsZero() && now.Sub(lastShortReleaseTime) < 380*time.Millisecond {
							// Double Tap Triggered!
							hasPendingSingleTap = false
							lastShortReleaseTime = time.Time{}
							log.Println("[Gesture] >>> Double Tap Detected! Toggling Screen Sleep <<<")
							isScreenOff = !isScreenOff
							if isScreenOff {
								mgr.SetBanner("双击快捷息屏")
							} else {
								mgr.SetBanner("双击唤醒屏幕")
							}
						} else {
							// Record first tap, wait for potential double-tap
							hasPendingSingleTap = true
							lastShortReleaseTime = now
						}
					}
				}
			}

		case <-ticker.C:
			now := time.Now()

			// Check for Long Press (holding >= 800ms)
			pressProgress := 0.0
			if isFingerDown && !longPressFired && !isScreenOff {
				duration := now.Sub(pressStartTime)
				pressProgress = float64(duration) / float64(800*time.Millisecond)
				if pressProgress >= 1.0 {
					pressProgress = 1.0
					longPressFired = true
					hasPendingSingleTap = false

					// Execute Focused Action!
					actionItem := menuItems[selectedItem]
					log.Printf("[Gesture] >>> Long Press Confirmed! Executing: %s <<<", actionItem.Title)
					mgr.SetBanner(fmt.Sprintf("已确认执行: %s", actionItem.Title))
					actionItem.Action(mgr)
				}
			}

			// Check for Single Tap Timeout (> 380ms after release with no second tap)
			if hasPendingSingleTap && !isFingerDown && !isScreenOff {
				if now.Sub(lastShortReleaseTime) >= 380*time.Millisecond {
					hasPendingSingleTap = false
					lastShortReleaseTime = time.Time{}

					// Cycle to next menu item
					selectedItem = (selectedItem + 1) % len(menuItems)
					log.Printf("[Gesture] >>> Single Tap Detected! Cycled focus to: [%d] %s <<<", selectedItem, menuItems[selectedItem].Title)
					mgr.SetBanner(fmt.Sprintf("已选中: %s", menuItems[selectedItem].Title))
				}
			}

			// Render UI
			if isScreenOff {
				// Sleep / Blanking Mode: pure black + faint hint, minimal power & CPU
				fb.Clear(ColBlack)
				fb.DrawText(120, 150, "【 屏幕休眠中 · 触摸任意位置唤醒 】", ColBorder, 1)
				_ = fb.SwapBuffers()
				continue
			}

			mgr.Status.mu.RLock()
			status := *mgr.Status
			mgr.Status.mu.RUnlock()

			fb.Clear(ColBg)

			// 1. Top Header Bar (Y: 0 ~ 36)
			fb.FillRect(0, 0, ScreenWidth, 36, ColCardBg)
			fb.DrawHLine(0, 36, ScreenWidth, ColBorder)

			fb.DrawIcon(12, 10, "car", ColPrimary)
			fb.DrawText(36, 10, "DiPlay 车机系统", ColPrimary, 1)

			nowStr := now.Format("15:04:05")
			timeInfo := fmt.Sprintf("%s · %.0f度", nowStr, status.CpuTemp)
			fb.DrawText(186, 10, timeInfo, ColWhite, 1)

			ipText := "IP: " + status.LocalIP
			if status.IsAPMode {
				ipText = "热点: 100.96.0.1"
			}
			fb.DrawText(330, 10, ipText, ColTextMuted, 1)

			// 2. 2x2 Large Action Cards (Y: 46 ~ 258)
			cardW := 224
			cardH := 98
			coords := [][2]int{
				{12, 46},  // Card 0: Top-Left
				{244, 46}, // Card 1: Top-Right
				{12, 154}, // Card 2: Bottom-Left
				{244, 154},// Card 3: Bottom-Right
			}

			for i, item := range menuItems {
				cx := coords[i][0]
				cy := coords[i][1]
				isSelected := (i == selectedItem)

				if isSelected {
					// Highlighted Active Card with Cyan Glow
					fb.FillRoundRect(cx, cy, cardW, cardH, 8, ColCardHover)
					fb.DrawRoundRect(cx, cy, cardW, cardH, 8, item.Color)
					fb.DrawRoundRect(cx+1, cy+1, cardW-2, cardH-2, 7, item.Color)

					// Focused badge
					fb.FillRoundRect(cx+10, cy+10, 10, 10, 5, item.Color)
					fb.DrawText(cx+26, cy+8, item.Title, ColWhite, 1)

					// "长按确认" tag
					fb.DrawText(cx+cardW-68, cy+8, "长按执行", item.Color, 1)
				} else {
					// Normal Inactive Card
					fb.FillRoundRect(cx, cy, cardW, cardH, 8, ColCardBg)
					fb.DrawRoundRect(cx, cy, cardW, cardH, 8, ColBorder)

					fb.DrawText(cx+16, cy+8, item.Title, ColTextMuted, 1)
				}

				// Subtitle text
				sub := item.Subtitle(&status)
				subCol := ColTextMuted
				if isSelected {
					subCol = ColWhite
				}
				fb.DrawText(cx+16, cy+38, sub, subCol, 1)

				// Status indicator dot or icon
				switch item.ID {
				case "restart_stream":
					if status.DiPlayActive && status.Fps > 0 {
						fb.DrawBadge(cx+16, cy+64, fmt.Sprintf("推流正常 (%d FPS)", status.Fps), ColSuccess, ColBlack)
					} else {
						fb.DrawBadge(cx+16, cy+64, "待机唤醒模式", ColCardBg, ColTextMuted)
					}
				case "toggle_wifi":
					if status.IsAPMode {
						badgeText := "车载热点模式"
						if strings.Contains(status.NetworkMode, "5GHz") {
							badgeText = "车载 5GHz 高速热点"
						} else if strings.Contains(status.NetworkMode, "2.4GHz") {
							badgeText = "车载 2.4GHz 热点"
						}
						fb.DrawBadge(cx+16, cy+64, badgeText, ColSuccess, ColBlack)
					} else {
						fb.DrawBadge(cx+16, cy+64, "家庭局域网模式", ColWarning, ColBlack)
					}
				case "toggle_screen":
					fb.DrawBadge(cx+16, cy+64, "触摸任意处立即唤醒", ColCardBg, ColTextMuted)
				case "reboot_system":
					fb.DrawBadge(cx+16, cy+64, "保持系统健康", ColCardBg, ColTextMuted)
				}
			}

			// 3. Long-Press Charging Progress Popup
			if isFingerDown && pressProgress > 0 && !longPressFired {
				popW := 360
				popH := 82
				px := (ScreenWidth - popW) / 2
				py := 115

				fb.FillRoundRect(px, py, popW, popH, 10, ColCardHover)
				fb.DrawRoundRect(px, py, popW, popH, 10, ColPrimary)
				fb.DrawRoundRect(px+1, py+1, popW-2, popH-2, 9, ColPrimary)

				hintTitle := fmt.Sprintf("按住确认: %s", menuItems[selectedItem].Title)
				fb.DrawText(px+18, py+12, hintTitle, ColWhite, 1)

				// Progress bar
				barW := popW - 36
				barH := 16
				bx := px + 18
				by := py + 42

				fb.FillRoundRect(bx, by, barW, barH, 4, ColCardBg)
				fb.DrawRoundRect(bx, by, barW, barH, 4, ColBorder)

				fillW := int(float64(barW) * pressProgress)
				if fillW > 0 {
					fillCol := ColPrimary
					if pressProgress > 0.85 {
						fillCol = ColSuccess
					}
					fb.FillRoundRect(bx+1, by+1, fillW-2, barH-2, 3, fillCol)
				}
				pctStr := fmt.Sprintf("%d%%", int(pressProgress*100))
				fb.DrawText(px+popW-56, py+12, pctStr, ColPrimary, 1)
			}

			// 4. Toast Banner (if any)
			if now.Before(status.BannerExpires) && status.BannerMsg != "" && (!isFingerDown || pressProgress == 0) {
				bannerW := 400
				bannerH := 36
				bx := (ScreenWidth - bannerW) / 2
				by := 115
				fb.FillRoundRect(bx, by, bannerW, bannerH, 8, ColActiveTab)
				fb.DrawRoundRect(bx, by, bannerW, bannerH, 8, ColPrimary)
				fb.DrawText(bx+16, by+10, status.BannerMsg, ColWhite, 1)
			}

			// 5. Bottom Gesture Guide Dock (Y: 264 ~ 312)
			fb.FillRoundRect(12, 264, ScreenWidth-24, 46, 8, ColCardBg)
			fb.DrawRoundRect(12, 264, ScreenWidth-24, 46, 8, ColBorder)

			guideText := "【单击】切换选择   【长按】确认执行   【双击】开关屏幕"
			fb.DrawText(24, 280, guideText, ColPrimary, 1)

			_ = fb.SwapBuffers()
		}
	}
}
