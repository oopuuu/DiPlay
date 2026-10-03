package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

type TouchEvent struct {
	X        int
	Y        int
	RawX     int
	RawY     int
	Pressed  bool
	Released bool
}

type CalibrationData struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
	C float64 `json:"c"`
	D float64 `json:"d"`
	E float64 `json:"e"`
	F float64 `json:"f"`
	Valid bool `json:"valid"`
}

const CalibFilePath = "/opt/diplay/touch_calib.json"

type InputManager struct {
	mu            sync.Mutex
	touchDev      *os.File
	mouseDev      *os.File
	Events        chan TouchEvent
	rawX          int
	rawY          int
	pressure      int
	curX          int
	curY          int
	isPressed     bool
	Calib         CalibrationData
	swapXY        bool
	invX          bool
	invY          bool
	lastTouchTime time.Time
}

func NewInputManager(touchPath, mousePath string) *InputManager {
	im := &InputManager{
		Events: make(chan TouchEvent, 64),
		curX:   ScreenWidth / 2,
		curY:   ScreenHeight / 2,
		// Fallback defaults if not calibrated
		swapXY: true,
		invX:   false,
		invY:   true,
	}

	im.LoadCalibration()

	if touchPath == "" {
		touchPath = "/dev/input/event4"
	}

	if f, err := os.OpenFile(touchPath, os.O_RDONLY, 0); err == nil {
		im.touchDev = f
		go im.readTouchLoop()
		go im.watchdogReleaseLoop()
		log.Printf("[Input] Touchscreen opened: %s", touchPath)
	} else {
		log.Printf("[Input] Touchscreen not opened: %v", err)
	}

	if mousePath == "" {
		mousePath = "/dev/input/mice"
	}
	if f, err := os.OpenFile(mousePath, os.O_RDONLY, 0); err == nil {
		im.mouseDev = f
		go im.readMouseLoop()
		log.Printf("[Input] Mouse device opened: %s", mousePath)
	}

	return im
}

func (im *InputManager) LoadCalibration() {
	if data, err := os.ReadFile(CalibFilePath); err == nil {
		var c CalibrationData
		if err := json.Unmarshal(data, &c); err == nil && c.Valid {
			im.Calib = c
			log.Printf("[Input] Loaded calibration from %s", CalibFilePath)
			return
		}
	}
	im.Calib.Valid = false
}

func (im *InputManager) SaveCalibration(c CalibrationData) {
	c.Valid = true
	im.mu.Lock()
	im.Calib = c
	im.mu.Unlock()

	data, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(CalibFilePath, data, 0644)
	log.Printf("[Input] Saved new calibration to %s", CalibFilePath)
}

func (im *InputManager) Close() {
	if im.touchDev != nil {
		_ = im.touchDev.Close()
	}
	if im.mouseDev != nil {
		_ = im.mouseDev.Close()
	}
}

func (im *InputManager) watchdogReleaseLoop() {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		im.mu.Lock()
		if im.isPressed && time.Since(im.lastTouchTime) > 250*time.Millisecond {
			im.isPressed = false
			evt := TouchEvent{
				X:        im.curX,
				Y:        im.curY,
				RawX:     im.rawX,
				RawY:     im.rawY,
				Pressed:  false,
				Released: true,
			}
			im.mu.Unlock()
			select {
			case im.Events <- evt:
			default:
			}
		} else {
			im.mu.Unlock()
		}
	}
}

func (im *InputManager) readTouchLoop() {
	buf := make([]byte, 24)
	hasCoordUpdate := false

	for {
		_, err := io.ReadFull(im.touchDev, buf)
		if err != nil {
			return
		}

		evType := binary.LittleEndian.Uint16(buf[16:18])
		evCode := binary.LittleEndian.Uint16(buf[18:20])
		evVal := int32(binary.LittleEndian.Uint32(buf[20:24]))

		im.mu.Lock()

		switch evType {
		case 3: // EV_ABS
			if evCode == 0 { // ABS_X
				im.rawX = int(evVal)
				hasCoordUpdate = true
			} else if evCode == 1 { // ABS_Y
				im.rawY = int(evVal)
				hasCoordUpdate = true
			} else if evCode == 24 { // ABS_PRESSURE
				im.pressure = int(evVal)
				if im.pressure > 30 {
					im.isPressed = true
					im.lastTouchTime = time.Now()
				} else if im.pressure == 0 {
					if im.isPressed {
						im.isPressed = false
						evt := TouchEvent{
							X:        im.curX,
							Y:        im.curY,
							RawX:     im.rawX,
							RawY:     im.rawY,
							Pressed:  false,
							Released: true,
						}
						im.mu.Unlock()
						select {
						case im.Events <- evt:
						default:
						}
						im.mu.Lock()
					}
				}
			}

		case 1: // EV_KEY
			if evCode == 330 { // BTN_TOUCH
				if evVal == 1 {
					im.isPressed = true
					im.lastTouchTime = time.Now()
				} else {
					if im.isPressed {
						im.isPressed = false
						evt := TouchEvent{
							X:        im.curX,
							Y:        im.curY,
							RawX:     im.rawX,
							RawY:     im.rawY,
							Pressed:  false,
							Released: true,
						}
						im.mu.Unlock()
						select {
						case im.Events <- evt:
						default:
						}
						im.mu.Lock()
					}
				}
			}

		case 0: // EV_SYN
			if hasCoordUpdate {
				hasCoordUpdate = false
				im.lastTouchTime = time.Now()
				im.isPressed = true

				var sx, sy int

				if im.Calib.Valid {
					// Standard affine transformation
					fx := im.Calib.A*float64(im.rawX) + im.Calib.B*float64(im.rawY) + im.Calib.C
					fy := im.Calib.D*float64(im.rawX) + im.Calib.E*float64(im.rawY) + im.Calib.F
					sx = int(fx)
					sy = int(fy)
				} else {
					// Fallback bounding box map
					rx := im.rawX
					ry := im.rawY

					if rx < 150 {
						rx = 150
					}
					if rx > 3950 {
						rx = 3950
					}
					if ry < 150 {
						ry = 150
					}
					if ry > 3950 {
						ry = 3950
					}

					normX := (rx - 150) * 1000 / 3800
					normY := (ry - 150) * 1000 / 3800

					if im.swapXY {
						sx = normY * ScreenWidth / 1000
						sy = normX * ScreenHeight / 1000
					} else {
						sx = normX * ScreenWidth / 1000
						sy = normY * ScreenHeight / 1000
					}

					if im.invX {
						sx = ScreenWidth - 1 - sx
					}
					if im.invY {
						sy = ScreenHeight - 1 - sy
					}
				}

				if sx < 0 {
					sx = 0
				}
				if sx >= ScreenWidth {
					sx = ScreenWidth - 1
				}
				if sy < 0 {
					sy = 0
				}
				if sy >= ScreenHeight {
					sy = ScreenHeight - 1
				}

				im.curX = sx
				im.curY = sy

				evt := TouchEvent{
					X:       im.curX,
					Y:       im.curY,
					RawX:    im.rawX,
					RawY:    im.rawY,
					Pressed: true,
				}
				im.mu.Unlock()
				select {
				case im.Events <- evt:
				default:
				}
				im.mu.Lock()
			}
		}

		im.mu.Unlock()
	}
}

func (im *InputManager) readMouseLoop() {
	buf := make([]byte, 3)
	wasLeftDown := false

	for {
		_, err := io.ReadFull(im.mouseDev, buf)
		if err != nil {
			return
		}

		b0 := buf[0]
		dx := int(int8(buf[1]))
		dy := int(int8(buf[2]))

		isLeftDown := (b0 & 0x01) != 0

		im.mu.Lock()
		im.curX += dx
		im.curY -= dy

		if im.curX < 0 {
			im.curX = 0
		}
		if im.curX >= ScreenWidth {
			im.curX = ScreenWidth - 1
		}
		if im.curY < 0 {
			im.curY = 0
		}
		if im.curY >= ScreenHeight {
			im.curY = ScreenHeight - 1
		}

		cx := im.curX
		cy := im.curY
		im.mu.Unlock()

		if isLeftDown && !wasLeftDown {
			wasLeftDown = true
			im.Events <- TouchEvent{
				X:       cx,
				Y:       cy,
				Pressed: true,
			}
		} else if !isLeftDown && wasLeftDown {
			wasLeftDown = false
			im.Events <- TouchEvent{
				X:        cx,
				Y:        cy,
				Pressed:  false,
				Released: true,
			}
		}
	}
}
