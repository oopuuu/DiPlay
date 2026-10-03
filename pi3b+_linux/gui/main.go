package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
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
	return x >= b.X-10 && x < b.X+b.W+10 && y >= b.Y-8 && y < b.Y+b.H+8
}

type CalibPoint struct {
	TargetX, TargetY int
	RawX, RawY       int
}

func solveAffine(points []CalibPoint) CalibrationData {
	n := float64(len(points))
	if n < 3 {
		log.Printf("[Calib-Math] Error: points count < 3 (%d)", len(points))
		return CalibrationData{Valid: false}
	}

	// 1. Calculate centroid means
	var meanRx, meanRy, meanSx, meanSy float64
	var minRx, maxRx, minRy, maxRy int
	minRx, maxRx = points[0].RawX, points[0].RawX
	minRy, maxRy = points[0].RawY, points[0].RawY

	for i, p := range points {
		log.Printf("[Calib-Math] Point %d: Target=(%d, %d), Raw=(%d, %d)", i, p.TargetX, p.TargetY, p.RawX, p.RawY)
		meanRx += float64(p.RawX)
		meanRy += float64(p.RawY)
		meanSx += float64(p.TargetX)
		meanSy += float64(p.TargetY)

		if p.RawX < minRx {
			minRx = p.RawX
		}
		if p.RawX > maxRx {
			maxRx = p.RawX
		}
		if p.RawY < minRy {
			minRy = p.RawY
		}
		if p.RawY > maxRy {
			maxRy = p.RawY
		}
	}
	meanRx /= n
	meanRy /= n
	meanSx /= n
	meanSy /= n

	// Check point diversity: span must be reasonable (> 300 raw units)
	spanX := maxRx - minRx
	spanY := maxRy - minRy
	log.Printf("[Calib-Math] Raw span: dX=%d, dY=%d", spanX, spanY)
	if spanX < 300 || spanY < 300 {
		log.Printf("[Calib-Math] Points clustered together! Span too small.")
		return CalibrationData{Valid: false}
	}

	// 2. Centered Normal Equations (2x2 Covariance System)
	var sUU, sVV, sUV float64
	var sUX, sVX float64
	var sUY, sVY float64

	for _, p := range points {
		u := float64(p.RawX) - meanRx
		v := float64(p.RawY) - meanRy
		xPrime := float64(p.TargetX) - meanSx
		yPrime := float64(p.TargetY) - meanSy

		sUU += u * u
		sVV += v * v
		sUV += u * v

		sUX += u * xPrime
		sVX += v * xPrime

		sUY += u * yPrime
		sVY += v * yPrime
	}

	det := sUU*sVV - sUV*sUV
	log.Printf("[Calib-Math] Covariance 2x2 Determinant = %e", det)
	if det <= 1e-4 {
		log.Printf("[Calib-Math] Points are collinear or degenerate!")
		return CalibrationData{Valid: false}
	}

	// Solve for A, B, C: X = A*rx + B*ry + C
	A := (sUX*sVV - sVX*sUV) / det
	B := (sUU*sVX - sUV*sUX) / det
	C := meanSx - A*meanRx - B*meanRy

	// Solve for D, E, F: Y = D*rx + E*ry + F
	D := (sUY*sVV - sVY*sUV) / det
	E := (sUU*sVY - sUV*sUY) / det
	F := meanSy - D*meanRx - E*meanRy

	log.Printf("[Calib-Math] Solved Affine: X = %.5f*rx + %.5f*ry + %.2f, Y = %.5f*rx + %.5f*ry + %.2f", A, B, C, D, E, F)

	return CalibrationData{
		A:     A,
		B:     B,
		C:     C,
		D:     D,
		E:     E,
		F:     F,
		Valid: true,
	}
}

func main() {
	fbPath := flag.String("fb", "/dev/fb1", "Path to framebuffer device")
	touchPath := flag.String("touch", "/dev/input/event4", "Path to touchscreen event device")
	mousePath := flag.String("mouse", "/dev/input/mice", "Path to mouse device")
	startCalib := flag.Bool("calib", false, "Start directly in calibration mode")
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

	go func() {
		time.Sleep(1 * time.Second)
		mgr.MakeDiscoverable()
	}()

	currentTab := 0 // 0: 配对, 1: 投屏, 2: 网络, 3: 系统
	tabs := []string{"配对", "投屏", "网络", "系统"}

	// Calibration state machine
	// Calibration state machine: start only if flag -calib is passed
	isCalibrating := *startCalib
	calibStage := 0 // 0: 4点采样向导, 1: 触控验证画板
	calibStep := 0
	type CalibTargetDef struct {
		x, y int
		name string
	}
	calibTargets := []CalibTargetDef{
		{x: 40, y: 40, name: "左上角"},
		{x: 440, y: 40, name: "右上角"},
		{x: 440, y: 280, name: "右下角"},
		{x: 40, y: 280, name: "左下角"},
	}
	var calibCollected []CalibPoint
	var calibResult CalibrationData
	type TrailPoint struct {
		X, Y int
	}
	var testTrail []TrailPoint
	calibIgnoredMsg := ""
	calibIgnoredExpires := time.Time{}
	rebootConfirming := false
	touchDownDuringCalib := false
	pointCapturedFeedback := false
	pointCapturedFeedbackTime := time.Time{}

	// Handle graceful shutdown and SIGUSR1 for calibration
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)

	ticker := time.NewTicker(33 * time.Millisecond) // ~30 FPS
	defer ticker.Stop()

	// Watchdog for calibration trigger file
	calibWatchTicker := time.NewTicker(500 * time.Millisecond)
	defer calibWatchTicker.Stop()

	var tabButtons []*Button
	var rebuildButtons func()

	rebuildButtons = func() {
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
				ID: "btn_reset_calib", X: 265, Y: 46, W: 205, H: 44,
				Text: "恢复出厂校准",
				OnClick: func() {
					_ = os.Remove(CalibFilePath)
					_ = os.Remove("/etc/pointercal")
					input.LoadCalibration()
					mgr.SetBanner("已恢复官方出厂黄金校准！")
				},
			})
			tabButtons = append(tabButtons, &Button{
				ID: "btn_ts_calib", X: 265, Y: 100, W: 205, H: 44,
				Text: "启动官方驱动校准",
				OnClick: func() {
					mgr.SetBanner("正在调用官方驱动校准 ts_calibrate...")
					go func() {
						cmd := exec.Command("sh", "-c", "sudo TSLIB_FBDEVICE=/dev/fb1 TSLIB_TSDEVICE=/dev/input/event4 ts_calibrate")
						_ = cmd.Run()
						input.LoadCalibration()
					}()
				},
			})
			rebootText := "重启树莓派"
			if rebootConfirming {
				rebootText = "再次点击确认重启"
			}
			tabButtons = append(tabButtons, &Button{
				ID: "btn_reboot", X: 265, Y: 154, W: 205, H: 44,
				Text: rebootText,
				OnClick: func() {
					if rebootConfirming {
						mgr.RebootPi()
					} else {
						rebootConfirming = true
						mgr.SetBanner("请再次点击按钮确认重启系统")
						rebuildButtons()
						go func() {
							time.Sleep(5 * time.Second)
							rebootConfirming = false
							rebuildButtons()
						}()
					}
				},
			})
		}
	}

	rebuildButtons()

	// Touch cursor tracking
	touchX := -1
	touchY := -1
	touchRawX := 0
	touchRawY := 0
	lastTouchTime := time.Time{}
	lastTriggerTime := time.Time{}

	for {
		select {
		case s := <-sigChan:
			if s == syscall.SIGUSR1 {
				isCalibrating = true
				calibStage = 0
				calibStep = 0
				calibCollected = nil
				testTrail = nil
				log.Println("[DiPlay-GUI] Calibration triggered via SIGUSR1!")
				continue
			}
			log.Println("[DiPlay-GUI] Exiting...")
			fb.Clear(ColBg)
			_ = fb.SwapBuffers()
			return

		case <-calibWatchTicker.C:
			if _, err := os.Stat("/tmp/trigger_calib"); err == nil {
				_ = os.Remove("/tmp/trigger_calib")
				isCalibrating = true
				calibStage = 0
				calibStep = 0
				calibCollected = nil
				testTrail = nil
				log.Println("[DiPlay-GUI] Calibration triggered via /tmp/trigger_calib!")
			}

		case ev := <-input.Events:
			touchX = ev.X
			touchY = ev.Y
			touchRawX = ev.RawX
			touchRawY = ev.RawY
			lastTouchTime = time.Now()

			if isCalibrating {
				if calibStage == 0 {
					// 自动检测手指是否已经抬起离开屏幕
					if ev.Released || time.Since(lastTouchTime) > 150*time.Millisecond {
						touchDownDuringCalib = false
					}

					if ev.Pressed {
						if touchDownDuringCalib {
							// 核心防连击：手指未曾抬起，坚决不触发下一次采样！
							continue
						}
						touchDownDuringCalib = true

						// 关键防误触：如果用户点在屏幕中央（例如 RawX 和 RawY 在 1400~2600 范围内）
						if ev.RawX > 1400 && ev.RawX < 2600 && ev.RawY > 1400 && ev.RawY < 2600 {
							calibIgnoredMsg = "已忽略: 检测到点击屏幕中央，请点击角上的靶心！"
							calibIgnoredExpires = time.Now().Add(2500 * time.Millisecond)
							log.Println("[Calib] Ignored center click")
							continue
						}

						target := calibTargets[calibStep]
						calibCollected = append(calibCollected, CalibPoint{
							TargetX: target.x,
							TargetY: target.y,
							RawX:    ev.RawX,
							RawY:    ev.RawY,
						})
						log.Printf("[Calib] Recorded Step %d/4 (%s): Target=(%d, %d), Raw=(%d, %d)",
							calibStep+1, target.name, target.x, target.y, ev.RawX, ev.RawY)

						pointCapturedFeedback = true
						pointCapturedFeedbackTime = time.Now()
						calibStep++

						if calibStep >= len(calibTargets) {
							// 4点采集完成，解算仿射变换矩阵
							res := solveAffine(calibCollected)
							if res.Valid {
								calibResult = res
								input.ApplyTemporaryCalibration(res)
								calibStage = 1 // 进入验证画板
								testTrail = nil
								log.Println("[Calib] 4-point calibration successfully solved! Entering test canvas.")
							} else {
								calibStep = 0
								calibCollected = nil
								calibIgnoredMsg = "采样点分布异常，请点击四个不同角落！"
								calibIgnoredExpires = time.Now().Add(3 * time.Second)
								log.Println("[Calib] Calibration solve failed, resetting.")
							}
						}
					}
					continue
				} else if calibStage == 1 {
					// 验证画板阶段
					if ev.Pressed {
						// 检查是否点击了左下角【重新校准】按钮: X: 20~220, Y: 250~315
						if ev.X >= 20 && ev.X <= 220 && ev.Y >= 250 && ev.Y <= 315 {
							calibStage = 0
							calibStep = 0
							calibCollected = nil
							testTrail = nil
							continue
						}
						// 检查是否点击了右下角【确认保存】按钮: X: 260~460, Y: 250~315
						if ev.X >= 260 && ev.X <= 460 && ev.Y >= 250 && ev.Y <= 315 {
							input.SaveCalibration(calibResult)
							isCalibrating = false
							calibStage = 0
							mgr.SetBanner("校准成功！已保存并生效")
							rebuildButtons()
							continue
						}
					}

					// 记录手指在画板上的连续划线轨迹
					if ev.Pressed || time.Since(lastTouchTime) < 150*time.Millisecond {
						if ev.Y < 245 { // 仅在绘图区记录
							testTrail = append(testTrail, TrailPoint{X: ev.X, Y: ev.Y})
							if len(testTrail) > 100 {
								testTrail = testTrail[len(testTrail)-100:]
							}
						}
					}
					continue
				}
			}

			if ev.Pressed && time.Since(lastTriggerTime) > 200*time.Millisecond {
				lastTriggerTime = time.Now()

				// Normal UI interaction
				if ev.Y >= 260 {
					tabW := (ScreenWidth - 16) / len(tabs)
					for i := range tabs {
						tx := 8 + i*tabW
						if ev.X >= tx-8 && ev.X < tx+tabW+8 {
							if currentTab != i {
								currentTab = i
								rebuildButtons()
								mgr.SetBanner("切换到: " + tabs[i])
							}
							break
						}
					}
				} else {
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
			mgr.Status.mu.RLock()
			status := *mgr.Status
			mgr.Status.mu.RUnlock()

			// Calibration Wizard Fullscreen View
			if isCalibrating {
				if calibStage == 0 {
					fb.Clear(ColBg)

					target := calibTargets[calibStep]

					// 屏幕中央大字指引
					stepTitle := fmt.Sprintf("第 %d / 4 步: 请点击 [%s] 靶心", calibStep+1, target.name)
					fb.DrawText(50, 110, stepTitle, ColPrimary, 1)

					if pointCapturedFeedback && time.Since(pointCapturedFeedbackTime) < 500*time.Millisecond {
						fb.DrawText(50, 140, "采集成功！请松开手指...", ColSuccess, 1)
					} else {
						fb.DrawText(50, 140, "注意: 靶心在屏幕边缘角落，请勿点击中央", ColWarning, 1)
					}
					fb.DrawText(50, 170, fmt.Sprintf("当前原始采样: X=%d, Y=%d", touchRawX, touchRawY), ColTextMuted, 1)

					if time.Now().Before(calibIgnoredExpires) && calibIgnoredMsg != "" {
						fb.DrawText(30, 200, calibIgnoredMsg, ColDanger, 1)
					}

					// 绘制角上的动态同心圆靶心
					tx, ty := target.x, target.y
					targetCol := ColDanger
					if pointCapturedFeedback && time.Since(pointCapturedFeedbackTime) < 500*time.Millisecond {
						targetCol = ColSuccess
					}

					fb.FillRoundRect(tx-8, ty-8, 16, 16, 8, targetCol)
					fb.DrawCircle(tx, ty, 16, ColPrimary)
					fb.DrawCircle(tx, ty, 26, ColWhite)
					fb.DrawHLine(tx-35, ty, 70, ColPrimary)
					fb.DrawVLine(tx, ty-35, 70, ColPrimary)

					// 靶心旁边的箭头文字
					switch calibStep {
					case 0: // 左上角
						fb.DrawText(tx+35, ty+10, "<- 请点击这里", ColSuccess, 1)
					case 1: // 右上角
						fb.DrawText(tx-135, ty+10, "请点击这里 ->", ColSuccess, 1)
					case 2: // 右下角
						fb.DrawText(tx-135, ty-20, "请点击这里 ->", ColSuccess, 1)
					case 3: // 左下角
						fb.DrawText(tx+35, ty-20, "<- 请点击这里", ColSuccess, 1)
					}

					_ = fb.SwapBuffers()
					continue
				} else if calibStage == 1 {
					// 验证画板模式
					fb.Clear(ColBg)

					fb.DrawCard(10, 10, ScreenWidth-20, 235, "触控校准验证: 请在屏幕滑动划线")
					fb.DrawText(30, 45, "手指在上方划线滑动，观察十字准星是否紧跟手指", ColWhite, 1)
					fb.DrawText(30, 70, "如果位置准确无误，请点击右下方【确认并保存】", ColSuccess, 1)

					// 绘制划线轨迹
					for i := 1; i < len(testTrail); i++ {
						p1 := testTrail[i-1]
						p2 := testTrail[i]
						fb.DrawLine(p1.X, p1.Y, p2.X, p2.Y, ColPrimary)
					}

					// 绘制当前手指触摸的十字准星
					if time.Since(lastTouchTime) < 2*time.Second && touchX >= 0 {
						fb.FillRoundRect(touchX-5, touchY-5, 10, 10, 5, ColDanger)
						fb.DrawRoundRect(touchX-15, touchY-15, 30, 30, 15, ColWarning)
						fb.DrawHLine(touchX-25, touchY, 50, ColWarning)
						fb.DrawVLine(touchX, touchY-25, 50, ColWarning)
					}

					// 绘制底部两个大按钮
					fb.DrawButton(30, 255, 190, 52, "重新校准", false, false, ColDanger)
					fb.DrawButton(260, 255, 190, 52, "确认并保存", true, false, ColSuccess)

					_ = fb.SwapBuffers()
					continue
				}
			}

			// Normal GUI View
			fb.Clear(ColBg)

			// 1. Header (Y: 0 ~ 34)
			fb.FillRect(0, 0, ScreenWidth, 34, ColCardBg)
			fb.DrawIcon(12, 9, "car", ColPrimary)
			fb.DrawText(36, 9, "DiPlay", ColPrimary, 1)

			nowStr := time.Now().Format("15:04:05")
			timeText := fmt.Sprintf("%s · %.1f度", nowStr, status.CpuTemp)
			fb.DrawText(155, 9, timeText, ColWhite, 1)

			rightInfo := status.LocalIP
			if time.Since(lastTouchTime) < 3*time.Second && touchX >= 0 {
				rightInfo = fmt.Sprintf("触控:%d,%d 原:%d,%d", touchX, touchY, touchRawX, touchRawY)
			}
			fb.DrawText(275, 9, rightInfo, ColSuccess, 1)
			fb.DrawHLine(0, 34, ScreenWidth, ColBorder)

			// 2. Main Content based on Tab (Y: 42 ~ 258)
			switch currentTab {
			case 0: // 配对
				fb.DrawCard(10, 42, 245, 218, "CarPlay 蓝牙配对")
				fb.DrawIcon(20, 78, "bluetooth", ColPrimary)
				fb.DrawText(38, 77, "设备: "+status.BtAlias, ColWhite, 1)

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

				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 1: // 投屏
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

			case 2: // 网络
				fb.DrawCard(10, 42, 245, 218, "网络连接状态")
				fb.DrawText(20, 78, "模式: "+status.NetworkMode, ColWhite, 1)
				fb.DrawText(20, 106, "IP: "+status.LocalIP, ColPrimary, 1)

				fb.DrawText(20, 138, "特斯拉 204 劫持:", ColWhite, 1)
				fb.DrawBadge(20, 158, "已激活 (杜绝断网弹窗)", ColSuccess, ColWhite)

				fb.DrawText(20, 196, "车载热点: Tesla-CarPlay", ColTextMuted, 1)
				fb.DrawText(20, 220, "热点密码: diplay123456", ColTextMuted, 1)

				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}

			case 3: // 系统
				fb.DrawCard(10, 42, 245, 218, "系统运行状态")
				fb.DrawText(20, 78, fmt.Sprintf("核心温度: %.1f度", status.CpuTemp), ColWhite, 1)
				fb.DrawText(20, 106, fmt.Sprintf("开机运行: %s", status.UptimeStr), ColTextMuted, 1)
				fb.DrawText(20, 134, fmt.Sprintf("内存占用: %d MB / %d MB", status.MemUsedMB, status.MemTotalMB), ColTextMuted, 1)
				fb.DrawText(20, 162, "内核: Linux 6.6 aarch64", ColTextMuted, 1)
				fb.DrawText(20, 190, "屏幕: 3.5寸 480x320", ColPrimary, 1)
				fb.DrawText(20, 218, "触控: XPT2046 驱动就绪", ColSuccess, 1)

				for _, b := range tabButtons {
					fb.DrawButton(b.X, b.Y, b.W, b.H, b.Text, false, b.Pressed, ColPrimary)
				}
			}

			// 3. Bottom Dock (Y: 264 ~ 316)
			dockY := 264
			tabW := (ScreenWidth - 16) / len(tabs)
			for i, t := range tabs {
				tx := 8 + i*tabW
				isActive := (i == currentTab)
				fb.DrawButton(tx, dockY, tabW-4, 50, t, isActive, false, ColPrimary)
			}

			// 4. Toast Banner
			if time.Now().Before(status.BannerExpires) && status.BannerMsg != "" {
				bannerW := 400
				bannerH := 40
				bx := (ScreenWidth - bannerW) / 2
				by := 115
				fb.FillRoundRect(bx, by, bannerW, bannerH, 8, ColActiveTab)
				fb.DrawRoundRect(bx, by, bannerW, bannerH, 8, ColPrimary)
				fb.DrawText(bx+20, by+12, status.BannerMsg, ColWhite, 1)
			}

			// 5. Draw Touch Indicator (Red cursor with crosshair for instant touch feedback)
			if time.Since(lastTouchTime) < 2*time.Second && touchX >= 0 {
				fb.FillRoundRect(touchX-4, touchY-4, 8, 8, 4, ColDanger)
				fb.DrawRoundRect(touchX-12, touchY-12, 24, 24, 12, ColWarning)
				fb.DrawHLine(touchX-18, touchY, 36, ColWarning)
				fb.DrawVLine(touchX, touchY-18, 36, ColWarning)
			}

			_ = fb.SwapBuffers()
		}
	}
}
