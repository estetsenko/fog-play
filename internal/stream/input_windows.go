package stream

import (
	"fmt"
	"log/slog"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procSendInput = windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseXDown      = 0x0080
	mouseXUp        = 0x0100
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000
	mouseAbsolute   = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyScanCode = 0x0008

	wheelDelta = 120
)

// mouseInput and keyInput are Win32 INPUT structs (40 bytes on 64-bit).
type mouseInput struct {
	typ       uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	_         uint32
	extra     uintptr
}

type keyInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	_     uint32
	extra uintptr
	_     [8]byte
}

type sendInput struct{ log *slog.Logger }

// NewInjector returns an injector that replays input with SendInput.
// Absolute moves map to the primary display, the one that is captured.
func NewInjector(log *slog.Logger) Injector { return sendInput{log} }

func send(p unsafe.Pointer, size uintptr) error {
	n, _, err := procSendInput.Call(1, uintptr(p), size)
	if n != 1 {
		return fmt.Errorf("SendInput: %w", err)
	}
	return nil
}

func (s sendInput) mouse(dx, dy int32, data, flags uint32) error {
	in := mouseInput{typ: inputMouse, dx: dx, dy: dy, mouseData: data, flags: flags}
	return send(unsafe.Pointer(&in), unsafe.Sizeof(in))
}

func (s sendInput) Inject(e InputEvent) error {
	switch e.Type {
	case "mm":
		return s.mouse(int32(math.Round(e.X)), int32(math.Round(e.Y)), 0, mouseMove)
	case "ma":
		x := int32(math.Round(clamp01(e.X) * 65535))
		y := int32(math.Round(clamp01(e.Y) * 65535))
		return s.mouse(x, y, 0, mouseMove|mouseAbsolute)
	case "md", "mu":
		down := e.Type == "md"
		var flags, data uint32
		switch e.Button {
		case 0:
			flags = pick(down, mouseLeftDown, mouseLeftUp)
		case 1:
			flags = pick(down, mouseMiddleDown, mouseMiddleUp)
		case 2:
			flags = pick(down, mouseRightDown, mouseRightUp)
		case 3, 4:
			flags = pick(down, mouseXDown, mouseXUp)
			data = uint32(e.Button - 2) // XBUTTON1 = 1, XBUTTON2 = 2
		default:
			return nil
		}
		return s.mouse(0, 0, data, flags)
	case "wh":
		if e.Y != 0 {
			if err := s.mouse(0, 0, uint32(int32(math.Round(e.Y*wheelDelta))), mouseWheel); err != nil {
				return err
			}
		}
		if e.X != 0 {
			return s.mouse(0, 0, uint32(int32(math.Round(e.X*wheelDelta))), mouseHWheel)
		}
		return nil
	case "kd", "ku":
		sc, ok := scanCode[e.Code]
		if !ok {
			s.log.Debug("unmapped key", "code", e.Code)
			return nil
		}
		flags := uint32(keyScanCode)
		if sc > 0xff {
			flags |= keyExtended
		}
		if e.Type == "ku" {
			flags |= keyUp
		}
		in := keyInput{typ: inputKeyboard, scan: sc & 0xff, flags: flags}
		return send(unsafe.Pointer(&in), unsafe.Sizeof(in))
	}
	return nil
}

func pick(cond bool, a, b uint32) uint32 {
	if cond {
		return a
	}
	return b
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// Both structs must match the Win32 INPUT size, or SendInput rejects them.
var (
	_ [40]byte = [unsafe.Sizeof(mouseInput{})]byte{}
	_ [40]byte = [unsafe.Sizeof(keyInput{})]byte{}
)
