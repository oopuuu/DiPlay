package main

import (
	"encoding/binary"
	"io"
	"log"
	"os"
	"sync"
)

type TouchEvent struct {
	X        int
	Y        int
	Pressed  bool
	Released bool
}

type InputManager struct {
	mu           sync.Mutex
	touchDev     *os.File
	mouseDev     *os.File
	Events       chan TouchEvent
	rawX         int
	rawY         int
	curX         int
	curY         int
	isPressed    bool
	swapXY       bool
	invX         bool
	invY         bool
	cursorVisible bool
}

func NewInputManager(touchPath, mousePath string) *InputManager {
	im := &InputManager{
		Events: make(chan TouchEvent, 32),
		curX:   ScreenWidth / 2,
		curY:   ScreenHeight / 2,
		swapXY: true,  // ADS7846 on 90deg rotated TFT
		invX:   false,
		invY:   true,
	}

	// Try open touch device
	if touchPath == "" {
		candidates := []string{
			"/dev/input/event4",
			"/dev/input/by-path/platform-3f204000.spi-cs-1-event",
			"/dev/input/event3",
			"/dev/input/event2",
		}
		for _, p := range candidates {
			if f, err := os.OpenFile(p, os.O_RDONLY, 0); err == nil {
				touchPath = p
				f.Close()
				break
			}
		}
	}

	if f, err := os.OpenFile(touchPath, os.O_RDONLY, 0); err == nil {
		im.touchDev = f
		go im.readTouchLoop()
		log.Printf("[Input] Touchscreen opened: %s", touchPath)
	} else {
		log.Printf("[Input] Touchscreen not opened: %v", err)
	}

	// Try open mouse device for fallback USB mouse
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

func (im *InputManager) Close() {
	if im.touchDev != nil {
		_ = im.touchDev.Close()
	}
	if im.mouseDev != nil {
		_ = im.mouseDev.Close()
	}
}

// readTouchLoop parses Linux evdev input_event
func (im *InputManager) readTouchLoop() {
	// struct input_event for 64-bit ARM Linux:
	// timeval: 16 bytes (sec 8B, usec 8B)
	// type:    2 bytes
	// code:    2 bytes
	// value:   4 bytes
	// total = 24 bytes
	buf := make([]byte, 24)

	var lastX, lastY int
	hasMoved := false

	for {
		_, err := io.ReadFull(im.touchDev, buf)
		if err != nil {
			log.Printf("[Input] Touch read error: %v", err)
			return
		}

		evType := binary.LittleEndian.Uint16(buf[16:18])
		evCode := binary.LittleEndian.Uint16(buf[18:20])
		evVal := int32(binary.LittleEndian.Uint32(buf[20:24]))

		switch evType {
		case 3: // EV_ABS
			if evCode == 0 { // ABS_X
				im.rawX = int(evVal)
				hasMoved = true
			} else if evCode == 1 { // ABS_Y
				im.rawY = int(evVal)
				hasMoved = true
			}
		case 1: // EV_KEY
			if evCode == 330 { // BTN_TOUCH
				if evVal == 1 {
					im.isPressed = true
				} else {
					im.isPressed = false
					// Touch Released
					im.Events <- TouchEvent{
						X:        im.curX,
						Y:        im.curY,
						Pressed:  false,
						Released: true,
					}
				}
			}
		case 0: // EV_SYN
			if hasMoved && im.isPressed {
				// Map raw coords (200~3900) to 480x320
				rx := im.rawX
				ry := im.rawY

				// Clamp
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

				var sx, sy int
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

				im.curX = sx
				im.curY = sy

				if im.curX != lastX || im.curY != lastY {
					lastX = im.curX
					lastY = im.curY
					im.Events <- TouchEvent{
						X:       im.curX,
						Y:       im.curY,
						Pressed: true,
					}
				}
				hasMoved = false
			}
		}
	}
}

// readMouseLoop parses standard PS/2 mice packets
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

		im.curX += dx
		im.curY -= dy // mouse y is inverted

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

		im.cursorVisible = true

		if isLeftDown && !wasLeftDown {
			wasLeftDown = true
			im.Events <- TouchEvent{
				X:       im.curX,
				Y:       im.curY,
				Pressed: true,
			}
		} else if !isLeftDown && wasLeftDown {
			wasLeftDown = false
			im.Events <- TouchEvent{
				X:        im.curX,
				Y:        im.curY,
				Pressed:  false,
				Released: true,
			}
		}
	}
}
