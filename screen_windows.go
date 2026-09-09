package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	smXVirtualScreen       = 76
	smYVirtualScreen       = 77
	smCXVirtualScreen      = 78
	smCYVirtualScreen      = 79
	srccopy                = 0x00CC0020
	captureBLT             = 0x40000000
	dibRGBColors           = 0
	inputMouse             = 0
	inputKeyboard          = 1
	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000
	keyeventfKeyUp         = 0x0002
	keyeventfUnicode       = 0x0004
)

var user32 = syscall.NewLazyDLL("user32.dll")
var gdi32 = syscall.NewLazyDLL("gdi32.dll")
var getSystemMetrics = user32.NewProc("GetSystemMetrics")
var getDC = user32.NewProc("GetDC")
var releaseDC = user32.NewProc("ReleaseDC")
var sendInput = user32.NewProc("SendInput")
var createCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
var deleteDC = gdi32.NewProc("DeleteDC")
var createCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
var selectObject = gdi32.NewProc("SelectObject")
var deleteObject = gdi32.NewProc("DeleteObject")
var bitBlt = gdi32.NewProc("BitBlt")
var getDIBits = gdi32.NewProc("GetDIBits")

type bitmapInfoHeader struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}
type screenFrame struct {
	Data          []byte
	Width, Height int
}

// Windows supplies BGRX bytes through a top-down 32-bit DIB. GDI capture is
// intentionally limited to 1280px wide; browser polling keeps the UI simple
// and bounded for VDI networks.
func captureScreenJPEG(quality, maxWidth int) (screenFrame, error) {
	x := metric(smXVirtualScreen)
	y := metric(smYVirtualScreen)
	width := metric(smCXVirtualScreen)
	height := metric(smCYVirtualScreen)
	if width < 1 || height < 1 {
		return screenFrame{}, errors.New("no active desktop")
	}
	source, _, e := getDC.Call(0)
	if source == 0 {
		return screenFrame{}, fmt.Errorf("GetDC: %w", e)
	}
	defer releaseDC.Call(0, source)
	mem, _, e := createCompatibleDC.Call(source)
	if mem == 0 {
		return screenFrame{}, fmt.Errorf("CreateCompatibleDC: %w", e)
	}
	defer deleteDC.Call(mem)
	bitmap, _, e := createCompatibleBitmap.Call(source, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return screenFrame{}, fmt.Errorf("CreateCompatibleBitmap: %w", e)
	}
	defer deleteObject.Call(bitmap)
	old, _, _ := selectObject.Call(mem, bitmap)
	ok, _, e := bitBlt.Call(mem, 0, 0, uintptr(width), uintptr(height), source, uintptr(x), uintptr(y), srccopy|captureBLT)
	if ok == 0 {
		return screenFrame{}, fmt.Errorf("BitBlt: %w", e)
	}
	selectObject.Call(mem, old)
	info := bitmapInfo{Header: bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}}
	raw := make([]byte, width*height*4)
	lines, _, e := getDIBits.Call(mem, bitmap, 0, uintptr(height), uintptr(unsafe.Pointer(&raw[0])), uintptr(unsafe.Pointer(&info)), dibRGBColors)
	if int(lines) != height {
		return screenFrame{}, fmt.Errorf("GetDIBits: %w", e)
	}
	imageOut := image.NewRGBA(image.Rect(0, 0, width, height))
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			i := (row*width + col) * 4
			j := row*imageOut.Stride + col*4
			imageOut.Pix[j] = raw[i+2]
			imageOut.Pix[j+1] = raw[i+1]
			imageOut.Pix[j+2] = raw[i]
			imageOut.Pix[j+3] = 255
		}
	}
	if width > maxWidth {
		ratio := float64(maxWidth) / float64(width)
		newHeight := int(math.Round(float64(height) * ratio))
		scaled := image.NewRGBA(image.Rect(0, 0, maxWidth, newHeight))
		for dy := 0; dy < newHeight; dy++ {
			sy := int(float64(dy) / ratio)
			for dx := 0; dx < maxWidth; dx++ {
				sx := int(float64(dx) / ratio)
				scaled.Set(dx, dy, imageOut.At(sx, sy))
			}
		}
		imageOut = scaled
		width = maxWidth
		height = newHeight
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, imageOut, &jpeg.Options{Quality: quality}); err != nil {
		return screenFrame{}, err
	}
	return screenFrame{encoded.Bytes(), width, height}, nil
}
func metric(which int) int { v, _, _ := getSystemMetrics.Call(uintptr(which)); return int(int32(v)) }

type screenInput struct {
	Type   string  `json:"type"`
	Action string  `json:"action,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button int     `json:"button,omitempty"`
	Delta  int     `json:"delta,omitempty"`
	Code   string  `json:"code,omitempty"`
	Key    string  `json:"key,omitempty"`
}
type winInput struct {
	Type uint32
	Pad  uint32
	Data [32]byte
}
type mouseInput struct {
	DX, DY                 int32
	MouseData, Flags, Time uint32
	ExtraInfo              uintptr
}
type keyInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	ExtraInfo   uintptr
}

func inputMouseEvent(dx, dy int32, data, flags uint32) winInput {
	var in winInput
	in.Type = inputMouse
	mi := mouseInput{DX: dx, DY: dy, MouseData: data, Flags: flags}
	*(*mouseInput)(unsafe.Pointer(&in.Data[0])) = mi
	return in
}
func inputKeyEvent(vk, scan uint16, flags uint32) winInput {
	var in winInput
	in.Type = inputKeyboard
	ki := keyInput{VK: vk, Scan: scan, Flags: flags}
	*(*keyInput)(unsafe.Pointer(&in.Data[0])) = ki
	return in
}
func inject(inputs ...winInput) error {
	if len(inputs) == 0 {
		return nil
	}
	got, _, e := sendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	runtime.KeepAlive(inputs)
	if int(got) != len(inputs) {
		return fmt.Errorf("SendInput accepted %d/%d events: %w", got, len(inputs), e)
	}
	return nil
}

func sendScreenInput(event screenInput) error {
	switch event.Type {
	case "mouse":
		if event.X < 0 || event.X > 1 || event.Y < 0 || event.Y > 1 {
			return errors.New("invalid mouse coordinates")
		}
		w, h := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
		if w < 1 || h < 1 {
			return errors.New("no active desktop")
		}
		dx := int32(math.Round(event.X * 65535))
		dy := int32(math.Round(event.Y * 65535))
		move := inputMouseEvent(dx, dy, 0, mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk)
		switch event.Action {
		case "move":
			return inject(move)
		case "down":
			b, e := mouseButton(event.Button, true)
			if e != nil {
				return e
			}
			return inject(move, b)
		case "up":
			b, e := mouseButton(event.Button, false)
			if e != nil {
				return e
			}
			return inject(move, b)
		case "wheel":
			if event.Delta < -1200 || event.Delta > 1200 {
				return errors.New("invalid wheel delta")
			}
			return inject(move, inputMouseEvent(0, 0, uint32(int32(event.Delta)), mouseeventfWheel))
		default:
			return errors.New("invalid mouse action")
		}
	case "key":
		if event.Action == "unicode" {
			if utf16Len(event.Key) > 16 {
				return errors.New("text input too long")
			}
			var inputs []winInput
			for _, r := range event.Key {
				if r == '\r' {
					r = '\n'
				}
				if r > 0xffff {
					continue
				}
				inputs = append(inputs, inputKeyEvent(0, uint16(r), keyeventfUnicode), inputKeyEvent(0, uint16(r), keyeventfUnicode|keyeventfKeyUp))
			}
			return inject(inputs...)
		}
		vk, ok := virtualKey(event.Code)
		if !ok {
			return errors.New("unsupported key")
		}
		flags := uint32(0)
		if event.Action == "up" {
			flags = keyeventfKeyUp
		} else if event.Action != "down" {
			return errors.New("invalid key action")
		}
		return inject(inputKeyEvent(vk, 0, flags))
	default:
		return errors.New("invalid input type")
	}
}
func utf16Len(s string) int { return len([]rune(s)) }
func mouseButton(button int, down bool) (winInput, error) {
	var flag uint32
	switch button {
	case 0:
		if down {
			flag = mouseeventfLeftDown
		} else {
			flag = mouseeventfLeftUp
		}
	case 1:
		if down {
			flag = mouseeventfMiddleDown
		} else {
			flag = mouseeventfMiddleUp
		}
	case 2:
		if down {
			flag = mouseeventfRightDown
		} else {
			flag = mouseeventfRightUp
		}
	default:
		return winInput{}, errors.New("unsupported mouse button")
	}
	return inputMouseEvent(0, 0, 0, flag), nil
}
func virtualKey(code string) (uint16, bool) {
	if len(code) == 4 && strings.HasPrefix(code, "Key") {
		return uint16(code[3]), code[3] >= 'A' && code[3] <= 'Z'
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") {
		return uint16(code[5]), code[5] >= '0' && code[5] <= '9'
	}
	m := map[string]uint16{"Enter": 0x0D, "Escape": 0x1B, "Backspace": 0x08, "Tab": 0x09, "Space": 0x20, "ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28, "Delete": 0x2E, "Home": 0x24, "End": 0x23, "PageUp": 0x21, "PageDown": 0x22, "ShiftLeft": 0xA0, "ShiftRight": 0xA1, "ControlLeft": 0xA2, "ControlRight": 0xA3, "AltLeft": 0xA4, "AltRight": 0xA5, "MetaLeft": 0x5B, "MetaRight": 0x5C}
	v, ok := m[code]
	return v, ok
}
