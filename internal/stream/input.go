package stream

// InputEvent is one mouse or keyboard event from the player's browser, sent
// as JSON over the "input" data channel.
type InputEvent struct {
	// Type is one of:
	//   mm  relative mouse move by X, Y pixels (pointer locked)
	//   ma  absolute mouse move to X, Y in 0..1 of the screen
	//   md  mouse button B down, mu  mouse button B up
	//   wh  wheel by Y notches (positive is away from the user) and X notches
	//   kd  key down, ku  key up; Code is the browser's KeyboardEvent.code
	Type string  `json:"t"`
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	// Button uses browser numbering: 0 left, 1 middle, 2 right, 3 back, 4 forward.
	Button int    `json:"b,omitempty"`
	Code   string `json:"c,omitempty"`
}

// Injector replays input events on this PC.
type Injector interface {
	Inject(InputEvent) error
}

// scanCode maps a browser KeyboardEvent.code to a PC set 1 scan code. Codes
// above 0xff are extended keys (0xE0 prefix).
var scanCode = map[string]uint16{
	"Escape": 0x01, "Digit1": 0x02, "Digit2": 0x03, "Digit3": 0x04, "Digit4": 0x05, "Digit5": 0x06,
	"Digit6": 0x07, "Digit7": 0x08, "Digit8": 0x09, "Digit9": 0x0a, "Digit0": 0x0b, "Minus": 0x0c,
	"Equal": 0x0d, "Backspace": 0x0e, "Tab": 0x0f, "KeyQ": 0x10, "KeyW": 0x11, "KeyE": 0x12,
	"KeyR": 0x13, "KeyT": 0x14, "KeyY": 0x15, "KeyU": 0x16, "KeyI": 0x17, "KeyO": 0x18, "KeyP": 0x19,
	"BracketLeft": 0x1a, "BracketRight": 0x1b, "Enter": 0x1c, "ControlLeft": 0x1d, "KeyA": 0x1e,
	"KeyS": 0x1f, "KeyD": 0x20, "KeyF": 0x21, "KeyG": 0x22, "KeyH": 0x23, "KeyJ": 0x24, "KeyK": 0x25,
	"KeyL": 0x26, "Semicolon": 0x27, "Quote": 0x28, "Backquote": 0x29, "ShiftLeft": 0x2a,
	"Backslash": 0x2b, "KeyZ": 0x2c, "KeyX": 0x2d, "KeyC": 0x2e, "KeyV": 0x2f, "KeyB": 0x30,
	"KeyN": 0x31, "KeyM": 0x32, "Comma": 0x33, "Period": 0x34, "Slash": 0x35, "ShiftRight": 0x36,
	"NumpadMultiply": 0x37, "AltLeft": 0x38, "Space": 0x39, "CapsLock": 0x3a, "F1": 0x3b, "F2": 0x3c,
	"F3": 0x3d, "F4": 0x3e, "F5": 0x3f, "F6": 0x40, "F7": 0x41, "F8": 0x42, "F9": 0x43, "F10": 0x44,
	"NumLock": 0x45, "ScrollLock": 0x46, "Numpad7": 0x47, "Numpad8": 0x48, "Numpad9": 0x49,
	"NumpadSubtract": 0x4a, "Numpad4": 0x4b, "Numpad5": 0x4c, "Numpad6": 0x4d, "NumpadAdd": 0x4e,
	"Numpad1": 0x4f, "Numpad2": 0x50, "Numpad3": 0x51, "Numpad0": 0x52, "NumpadDecimal": 0x53,
	"IntlBackslash": 0x56, "F11": 0x57, "F12": 0x58,
	"NumpadEnter": 0xe01c, "ControlRight": 0xe01d, "NumpadDivide": 0xe035, "PrintScreen": 0xe037,
	"AltRight": 0xe038, "Home": 0xe047, "ArrowUp": 0xe048, "PageUp": 0xe049, "ArrowLeft": 0xe04b,
	"ArrowRight": 0xe04d, "End": 0xe04f, "ArrowDown": 0xe050, "PageDown": 0xe051, "Insert": 0xe052,
	"Delete": 0xe053, "MetaLeft": 0xe05b, "MetaRight": 0xe05c, "ContextMenu": 0xe05d,
}
