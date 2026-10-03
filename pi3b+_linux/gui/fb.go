package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
)

const (
	ScreenWidth  = 480
	ScreenHeight = 320
	BytesPerPix  = 2
	BufferSize   = ScreenWidth * ScreenHeight * BytesPerPix
)

// RGB565 Helper
func RGB565(r, g, b uint8) uint16 {
	return uint16((uint16(r>>3) << 11) | (uint16(g>>2) << 5) | uint16(b>>3))
}

// Tesla Cyberpunk Dark Theme Color Palette
var (
	ColBlack     = RGB565(0, 0, 0)
	ColBg        = RGB565(13, 17, 23)      // #0d1117 深黑灰
	ColCardBg    = RGB565(22, 27, 34)      // #161b22 卡片深灰
	ColCardHover = RGB565(33, 40, 50)      // 卡片触控高亮
	ColBorder    = RGB565(48, 54, 61)      // #30363d 边框灰
	ColPrimary   = RGB565(0, 240, 255)     // #00f0ff 赛博电光青蓝
	ColSuccess   = RGB565(34, 197, 94)     // #22c55e 荧光绿
	ColWarning   = RGB565(245, 158, 11)    // #f59e0b 琥珀橙
	ColDanger    = RGB565(239, 68, 68)     // #ef4444 警示红
	ColWhite     = RGB565(255, 255, 255)   // #ffffff
	ColTextMuted = RGB565(139, 148, 158)   // #8b949e 银灰
	ColActiveTab = RGB565(26, 45, 68)      // 选中 Tab 浅蓝底
)

type Framebuffer struct {
	file       *os.File
	backBuffer []byte
}

func NewFramebuffer(devPath string) (*Framebuffer, error) {
	if devPath == "" {
		if _, err := os.Stat("/dev/fb1"); err == nil {
			devPath = "/dev/fb1"
		} else {
			devPath = "/dev/fb0"
		}
	}

	f, err := os.OpenFile(devPath, os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %v", devPath, err)
	}

	// Hide blinking cursor on Linux console
	_ = os.WriteFile("/sys/class/graphics/fbcon/cursor_blink", []byte("0"), 0644)

	return &Framebuffer{
		file:       f,
		backBuffer: make([]byte, BufferSize),
	}, nil
}

func (fb *Framebuffer) Close() {
	if fb.file != nil {
		// Restore cursor
		_ = os.WriteFile("/sys/class/graphics/fbcon/cursor_blink", []byte("1"), 0644)
		_ = fb.file.Close()
	}
}

func (fb *Framebuffer) SwapBuffers() error {
	_, err := fb.file.WriteAt(fb.backBuffer, 0)
	return err
}

func (fb *Framebuffer) Clear(color uint16) {
	colBytes := [2]byte{byte(color), byte(color >> 8)}
	for i := 0; i < BufferSize; i += 2 {
		fb.backBuffer[i] = colBytes[0]
		fb.backBuffer[i+1] = colBytes[1]
	}
}

func (fb *Framebuffer) SetPixel(x, y int, color uint16) {
	if x < 0 || x >= ScreenWidth || y < 0 || y >= ScreenHeight {
		return
	}
	idx := (y*ScreenWidth + x) * BytesPerPix
	binary.LittleEndian.PutUint16(fb.backBuffer[idx:idx+2], color)
}

func (fb *Framebuffer) DrawHLine(x, y, w int, color uint16) {
	if y < 0 || y >= ScreenHeight || w <= 0 {
		return
	}
	x1 := x + w
	if x < 0 {
		x = 0
	}
	if x1 > ScreenWidth {
		x1 = ScreenWidth
	}
	if x >= x1 {
		return
	}
	idx := (y*ScreenWidth + x) * BytesPerPix
	colBytes := [2]byte{byte(color), byte(color >> 8)}
	for cur := idx; cur < (y*ScreenWidth+x1)*BytesPerPix; cur += 2 {
		fb.backBuffer[cur] = colBytes[0]
		fb.backBuffer[cur+1] = colBytes[1]
	}
}

func (fb *Framebuffer) DrawVLine(x, y, h int, color uint16) {
	if x < 0 || x >= ScreenWidth || h <= 0 {
		return
	}
	for i := 0; i < h; i++ {
		fb.SetPixel(x, y+i, color)
	}
}

func (fb *Framebuffer) FillRect(x, y, w, h int, color uint16) {
	if w <= 0 || h <= 0 {
		return
	}
	for row := 0; row < h; row++ {
		fb.DrawHLine(x, y+row, w, color)
	}
}

func (fb *Framebuffer) DrawRect(x, y, w, h int, color uint16) {
	if w <= 0 || h <= 0 {
		return
	}
	fb.DrawHLine(x, y, w, color)
	fb.DrawHLine(x, y+h-1, w, color)
	fb.DrawVLine(x, y, h, color)
	fb.DrawVLine(x+w-1, y, h, color)
}

func (fb *Framebuffer) FillRoundRect(x, y, w, h, r int, color uint16) {
	if r <= 0 {
		fb.FillRect(x, y, w, h, color)
		return
	}
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}

	fb.FillRect(x+r, y, w-2*r, h, color)
	for i := 0; i < r; i++ {
		dx := r - int(sqrt(float64(r*r-(r-i)*(r-i))))
		fb.DrawHLine(x+dx, y+i, w-2*dx, color)
		fb.DrawHLine(x+dx, y+h-1-i, w-2*dx, color)
	}
}

func (fb *Framebuffer) DrawRoundRect(x, y, w, h, r int, color uint16) {
	if r <= 0 {
		fb.DrawRect(x, y, w, h, color)
		return
	}
	fb.DrawHLine(x+r, y, w-2*r, color)
	fb.DrawHLine(x+r, y+h-1, w-2*r, color)
	fb.DrawVLine(x, y+r, h-2*r, color)
	fb.DrawVLine(x+w-1, y+r, h-2*r, color)
}

func sqrt(v float64) float64 {
	// Simple Newton-Raphson for zero dependency
	if v <= 0 {
		return 0
	}
	x := v
	for i := 0; i < 6; i++ {
		x = 0.5 * (x + v/x)
	}
	return x
}

// DrawText draws ASCII or embedded Chinese characters
func (fb *Framebuffer) DrawText(x, y int, str string, color uint16, scale int) int {
	if scale < 1 {
		scale = 1
	}

	curX := x
	runes := []rune(str)
	for _, r := range runes {
		if curX+8*scale > ScreenWidth {
			break
		}

		// Check Chinese 16x16
		if bmp, ok := chinese16x16[r]; ok {
			for row := 0; row < 16; row++ {
				rowBytes := bmp[row]
				for col := 0; col < 16; col++ {
					byteIdx := col / 8
					bitIdx := 7 - (col % 8)
					if (rowBytes[byteIdx] & (1 << bitIdx)) != 0 {
						if scale == 1 {
							fb.SetPixel(curX+col, y+row, color)
						} else {
							fb.FillRect(curX+col*scale, y+row*scale, scale, scale, color)
						}
					}
				}
			}
			curX += 16 * scale
			continue
		}

		// Check ASCII
		if r >= 32 && r <= 126 {
			asciiIdx := int(r - 32)
			bmp := font8x16[asciiIdx]
			for row := 0; row < 16; row++ {
				b := bmp[row]
				for col := 0; col < 8; col++ {
					if (b & (0x80 >> col)) != 0 {
						if scale == 1 {
							fb.SetPixel(curX+col, y+row, color)
						} else {
							fb.FillRect(curX+col*scale, y+row*scale, scale, scale, color)
						}
					}
				}
			}
			curX += 8 * scale
			continue
		}

		// Fallback for unknown: small box
		curX += 8 * scale
	}
	return curX - x
}

// DrawBadge renders a pill badge
func (fb *Framebuffer) DrawBadge(x, y int, text string, bgColor, fgColor uint16) {
	w := len([]rune(text))*8 + 14
	h := 22
	fb.FillRoundRect(x, y, w, h, 6, bgColor)
	fb.DrawText(x+7, y+3, text, fgColor, 1)
}

// DrawCard renders a sleek dark card container
func (fb *Framebuffer) DrawCard(x, y, w, h int, title string) {
	fb.FillRoundRect(x, y, w, h, 8, ColCardBg)
	fb.DrawRoundRect(x, y, w, h, 8, ColBorder)
	if title != "" {
		fb.DrawText(x+14, y+10, title, ColPrimary, 1)
		fb.DrawHLine(x+10, y+28, w-20, ColBorder)
	}
}

// DrawButton renders an interactive touch button
func (fb *Framebuffer) DrawButton(x, y, w, h int, text string, active, pressed bool, accentColor uint16) {
	bg := ColCardBg
	border := ColBorder
	textCol := ColWhite

	if active {
		bg = ColActiveTab
		border = ColPrimary
		textCol = ColPrimary
	}
	if pressed {
		bg = ColCardHover
		border = ColWhite
	}

	fb.FillRoundRect(x, y, w, h, 6, bg)
	fb.DrawRoundRect(x, y, w, h, 6, border)

	// Center text
	textWidth := len([]rune(text)) * 8
	// Chinese characters are 16px wide
	for _, r := range []rune(text) {
		if _, ok := chinese16x16[r]; ok {
			textWidth += 8
		}
	}
	tx := x + (w-textWidth)/2
	ty := y + (h-16)/2
	fb.DrawText(tx, ty, text, textCol, 1)
}

// DrawIcons renders vector-like icons
func (fb *Framebuffer) DrawIcon(x, y int, name string, color uint16) {
	switch name {
	case "bluetooth":
		// Draw classic Bluetooth Rune
		fb.DrawVLine(x+6, y+2, 14, color)
		fb.DrawLine(x+2, y+6, x+10, y+13, color)
		fb.DrawLine(x+2, y+12, x+10, y+5, color)
		fb.DrawLine(x+10, y+5, x+6, y+2, color)
		fb.DrawLine(x+10, y+13, x+6, y+16, color)
	case "wifi":
		// Concentric arcs
		fb.FillRoundRect(x+2, y+2, 14, 12, 4, color)
		fb.FillRoundRect(x+4, y+5, 10, 8, 3, ColBg)
		fb.FillRoundRect(x+6, y+8, 6, 5, 2, color)
		fb.FillRoundRect(x+7, y+10, 4, 3, 1, ColBg)
		fb.FillRect(x+8, y+13, 2, 2, color)
	case "dot":
		fb.FillRoundRect(x, y, 8, 8, 4, color)
	case "car":
		fb.FillRoundRect(x+1, y+5, 16, 7, 2, color)
		fb.FillRoundRect(x+4, y+1, 10, 5, 2, color)
		fb.FillRect(x+3, y+11, 4, 3, ColBg)
		fb.FillRect(x+11, y+11, 4, 3, ColBg)
	}
}

func (fb *Framebuffer) DrawLine(x0, y0, x1, y1 int, color uint16) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx + dy
	for {
		fb.SetPixel(x0, y0, color)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Dummy to prevent unused syscall warning
var _ = syscall.SYS_IOCTL
