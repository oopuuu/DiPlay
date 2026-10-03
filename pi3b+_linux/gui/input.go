package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
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
	A     float64 `json:"a"`
	B     float64 `json:"b"`
	C     float64 `json:"c"`
	D     float64 `json:"d"`
	E     float64 `json:"e"`
	F     float64 `json:"f"`
	Valid bool    `json:"valid"`
}

const CalibFilePath = "/opt/diplay/touch_calib.json"

type InputManager struct {
	mu             sync.Mutex
	touchDev       *os.File
	mouseDev       *os.File
	Events         chan TouchEvent
	rawX           int
	rawY           int
	pressure       int
	curX           int
	curY           int
	isPressed      bool
	Calib          CalibrationData
	lastTouchTime  time.Time
	hasFilterState bool
	filterX        float64
	filterY        float64
}

func NewInputManager(touchPath, mousePath string) *InputManager {
	im := &InputManager{
		Events: make(chan TouchEvent, 64),
		curX:   ScreenWidth / 2,
		curY:   ScreenHeight / 2,
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
	// 1. Try /etc/pointercal (official tslib standard output)
	if data, err := os.ReadFile("/etc/pointercal"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 7 {
			var vals [7]float64
			valid := true
			for i := 0; i < 7; i++ {
				if v, err := strconv.ParseFloat(fields[i], 64); err == nil {
					vals[i] = v
				} else {
					valid = false
					break
				}
			}
			if valid && vals[6] != 0 {
				scale := vals[6]
				im.Calib = CalibrationData{
					A:     vals[0] / scale,
					B:     vals[1] / scale,
					C:     vals[2] / scale,
					D:     vals[3] / scale,
					E:     vals[4] / scale,
					F:     vals[5] / scale,
					Valid: true,
				}
				log.Printf("[Input] Loaded official tslib calibration from /etc/pointercal: A=%.5f B=%.5f C=%.1f D=%.5f E=%.5f F=%.1f",
					im.Calib.A, im.Calib.B, im.Calib.C, im.Calib.D, im.Calib.E, im.Calib.F)
				return
			}
		}
	}

	// 2. Try custom calibration file /opt/diplay/touch_calib.json
	if data, err := os.ReadFile(CalibFilePath); err == nil {
		var c CalibrationData
		if err := json.Unmarshal(data, &c); err == nil && c.Valid {
			im.Calib = c
			log.Printf("[Input] Loaded user calibration from %s", CalibFilePath)
			return
		}
	}

	// 3. Fallback to Official GoodTFT LCD-show 3.5" (rotate=90) Golden Calibration
	// Official specification with correct physical orientation:
	// Screen X = (rawY - 268) * 480 / (3880 - 268) = 0.13289*rawY - 35.6146
	// Screen Y = (rawX - 227) * 320 / (3936 - 227) =  0.08627*rawX - 19.5843
	im.Calib = CalibrationData{
		A:     0.0,
		B:     480.0 / (3880.0 - 268.0),
		C:     -268.0 * 480.0 / (3880.0 - 268.0),
		D:     320.0 / (3936.0 - 227.0),
		E:     0.0,
		F:     -227.0 * 320.0 / (3936.0 - 227.0),
		Valid: true,
	}
	log.Printf("[Input] Loaded factory golden calibration for 3.5\" rotate=90 (A=%.5f, B=%.5f, C=%.2f, D=%.5f, E=%.5f, F=%.2f)",
		im.Calib.A, im.Calib.B, im.Calib.C, im.Calib.D, im.Calib.E, im.Calib.F)
}

func (im *InputManager) ApplyTemporaryCalibration(c CalibrationData) {
	c.Valid = true
	im.mu.Lock()
	im.Calib = c
	im.hasFilterState = false
	im.mu.Unlock()
}

func (im *InputManager) SaveCalibration(c CalibrationData) {
	c.Valid = true
	im.mu.Lock()
	im.Calib = c
	im.hasFilterState = false
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
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		im.mu.Lock()
		if im.isPressed && time.Since(im.lastTouchTime) > 180*time.Millisecond {
			im.isPressed = false
			im.hasFilterState = false
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
						im.hasFilterState = false
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
						im.hasFilterState = false
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

				// 过滤未接触或未按紧时的假零跳变噪点
				if im.rawX < 120 || im.rawY < 120 {
					im.mu.Unlock()
					continue
				}

				// 应用仿射校准
				fx := im.Calib.A*float64(im.rawX) + im.Calib.B*float64(im.rawY) + im.Calib.C
				fy := im.Calib.D*float64(im.rawX) + im.Calib.E*float64(im.rawY) + im.Calib.F
				rawSX := int(fx)
				rawSY := int(fy)

				if rawSX < 0 {
					rawSX = 0
				}
				if rawSX >= ScreenWidth {
					rawSX = ScreenWidth - 1
				}
				if rawSY < 0 {
					rawSY = 0
				}
				if rawSY >= ScreenHeight {
					rawSY = ScreenHeight - 1
				}

				// 指数加权移动平均滤波 (EMA Dejitter Filter)，去除电阻屏微小抖动
				if !im.hasFilterState {
					im.filterX = float64(rawSX)
					im.filterY = float64(rawSY)
					im.hasFilterState = true
				} else {
					im.filterX = 0.35*im.filterX + 0.65*float64(rawSX)
					im.filterY = 0.35*im.filterY + 0.65*float64(rawSY)
				}

				sx := int(im.filterX + 0.5)
				sy := int(im.filterY + 0.5)

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
