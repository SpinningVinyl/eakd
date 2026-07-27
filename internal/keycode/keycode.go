package keycode

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	KeyEsc = 1
	Key1   = 2
	Key0   = 11

	KeyA = 30
	KeyB = 48
	KeyC = 46
	KeyD = 32
	KeyE = 18
	KeyF = 33
	KeyG = 34
	KeyH = 35
	KeyI = 23
	KeyJ = 36
	KeyK = 37
	KeyL = 38
	KeyM = 50
	KeyN = 49
	KeyO = 24
	KeyP = 25
	KeyQ = 16
	KeyR = 19
	KeyS = 31
	KeyT = 20
	KeyU = 22
	KeyV = 47
	KeyW = 17
	KeyX = 45
	KeyY = 21
	KeyZ = 44

	KeyEnter        = 28
	KeyLeftCtrl     = 29
	KeyLeftShift    = 42
	KeyRightShift   = 54
	KeyKPAsterisk   = 55
	KeyLeftAlt      = 56
	KeySpace        = 57
	KeyCapsLock     = 58
	KeyF1           = 59
	KeyF10          = 68
	KeyNumLock      = 69
	KeyScrollLock   = 70
	KeyKP7          = 71
	KeyKP8          = 72
	KeyKP9          = 73
	KeyKPMinus      = 74
	KeyKP4          = 75
	KeyKP5          = 76
	KeyKP6          = 77
	KeyKPPlus       = 78
	KeyKP1          = 79
	KeyKP2          = 80
	KeyKP3          = 81
	KeyKP0          = 82
	KeyKPDot        = 83
	KeyF11          = 87
	KeyF12          = 88
	KeyKPJPComma    = 95
	KeyKPEnter      = 96
	KeyRightCtrl    = 97
	KeyKPSlash      = 98
	KeyRightAlt     = 100
	KeyKPEqual      = 117
	KeyKPPlusMinus  = 118
	KeyKPComma      = 121
	KeyLeftMeta     = 125
	KeyRightMeta    = 126
	KeyCompose      = 127
	KeyKPLeftParen  = 179
	KeyKPRightParen = 180
	KeyMax          = 0x2ff
)

// Logical is a matching key. Values below logicalBase are real Linux key
// codes. Higher values combine the left and right variants of modifiers.
type Logical uint32

const logicalBase Logical = 1 << 16

const (
	LogicalCtrl Logical = logicalBase + iota
	LogicalShift
	LogicalAlt
	LogicalLogo
)

var fixedNames = map[string]uint16{
	"KEY_ESC":        KeyEsc,
	"KEY_ENTER":      KeyEnter,
	"KEY_SPACE":      KeySpace,
	"KEY_TAB":        15,
	"KEY_BACKSPACE":  14,
	"KEY_MINUS":      12,
	"KEY_EQUAL":      13,
	"KEY_LEFTBRACE":  26,
	"KEY_RIGHTBRACE": 27,
	"KEY_SEMICOLON":  39,
	"KEY_APOSTROPHE": 40,
	"KEY_GRAVE":      41,
	"KEY_BACKSLASH":  43,
	"KEY_COMMA":      51,
	"KEY_DOT":        52,
	"KEY_SLASH":      53,
	"KEY_CAPSLOCK":   KeyCapsLock,
	"KEY_NUMLOCK":    KeyNumLock,
	"KEY_SCROLLLOCK": KeyScrollLock,
	"KEY_SYSRQ":      99,
	"KEY_HOME":       102,
	"KEY_UP":         103,
	"KEY_PAGEUP":     104,
	"KEY_LEFT":       105,
	"KEY_RIGHT":      106,
	"KEY_END":        107,
	"KEY_DOWN":       108,
	"KEY_PAGEDOWN":   109,
	"KEY_INSERT":     110,
	"KEY_DELETE":     111,
	"KEY_MUTE":       113,
	"KEY_VOLUMEDOWN": 114,
	"KEY_VOLUMEUP":   115,
	"KEY_PAUSE":      119,
	"KEY_LEFTCTRL":   KeyLeftCtrl,
	"KEY_RIGHTCTRL":  KeyRightCtrl,
	"KEY_LEFTSHIFT":  KeyLeftShift,
	"KEY_RIGHTSHIFT": KeyRightShift,
	"KEY_LEFTALT":    KeyLeftAlt,
	"KEY_RIGHTALT":   KeyRightAlt,
	"KEY_LEFTMETA":   KeyLeftMeta,
	"KEY_RIGHTMETA":  KeyRightMeta,
	"KEY_COMPOSE":    KeyCompose,

	"KEY_KPASTERISK":   KeyKPAsterisk,
	"KEY_KP0":          KeyKP0,
	"KEY_KP1":          KeyKP1,
	"KEY_KP2":          KeyKP2,
	"KEY_KP3":          KeyKP3,
	"KEY_KP4":          KeyKP4,
	"KEY_KP5":          KeyKP5,
	"KEY_KP6":          KeyKP6,
	"KEY_KP7":          KeyKP7,
	"KEY_KP8":          KeyKP8,
	"KEY_KP9":          KeyKP9,
	"KEY_KPMINUS":      KeyKPMinus,
	"KEY_KPPLUS":       KeyKPPlus,
	"KEY_KPDOT":        KeyKPDot,
	"KEY_KPJPCOMMA":    KeyKPJPComma,
	"KEY_KPENTER":      KeyKPEnter,
	"KEY_KPSLASH":      KeyKPSlash,
	"KEY_KPEQUAL":      KeyKPEqual,
	"KEY_KPPLUSMINUS":  KeyKPPlusMinus,
	"KEY_KPCOMMA":      KeyKPComma,
	"KEY_KPLEFTPAREN":  KeyKPLeftParen,
	"KEY_KPRIGHTPAREN": KeyKPRightParen,
}

var letterCodes = []uint16{
	KeyA, KeyB, KeyC, KeyD, KeyE, KeyF, KeyG, KeyH, KeyI, KeyJ, KeyK, KeyL, KeyM,
	KeyN, KeyO, KeyP, KeyQ, KeyR, KeyS, KeyT, KeyU, KeyV, KeyW, KeyX, KeyY, KeyZ,
}

// Parse converts configuration names to logical matching keys. CTRL, SHIFT,
// ALT and LOGO intentionally match either the left or right physical key.
// Keypad keys accept both KEY_KP* Linux names and their shorter KP* aliases.
// CODE_<decimal> is available for kernel keys not present in the name table.
func Parse(name string) (Logical, error) {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "CTRL", "CONTROL":
		return LogicalCtrl, nil
	case "SHIFT":
		return LogicalShift, nil
	case "ALT":
		return LogicalAlt, nil
	case "LOGO", "META", "SUPER":
		return LogicalLogo, nil
	}

	if len(n) == 1 && n[0] >= 'A' && n[0] <= 'Z' {
		return Logical(letterCodes[n[0]-'A']), nil
	}
	if len(n) == 1 && n[0] >= '0' && n[0] <= '9' {
		if n == "0" {
			return Logical(Key0), nil
		}
		return Logical(Key1 + uint16(n[0]-'1')), nil
	}
	if strings.HasPrefix(n, "KEY_") && len(n) == 5 {
		return Parse(strings.TrimPrefix(n, "KEY_"))
	}
	if strings.HasPrefix(n, "F") {
		v, err := strconv.Atoi(strings.TrimPrefix(n, "F"))
		if err == nil && v >= 1 && v <= 12 {
			if v <= 10 {
				return Logical(KeyF1 + uint16(v-1)), nil
			}
			return Logical(KeyF11 + uint16(v-11)), nil
		}
	}
	lookupName := n
	if strings.HasPrefix(n, "KP") {
		lookupName = "KEY_" + n
	}
	if code, ok := fixedNames[lookupName]; ok {
		return Canonical(code), nil
	}
	if strings.HasPrefix(n, "CODE_") {
		v, err := strconv.ParseUint(strings.TrimPrefix(n, "CODE_"), 10, 16)
		if err == nil && v <= KeyMax {
			return Canonical(uint16(v)), nil
		}
	}
	return 0, fmt.Errorf("unknown key %q", name)
}

func Canonical(code uint16) Logical {
	switch code {
	case KeyLeftCtrl, KeyRightCtrl:
		return LogicalCtrl
	case KeyLeftShift, KeyRightShift:
		return LogicalShift
	case KeyLeftAlt, KeyRightAlt:
		return LogicalAlt
	case KeyLeftMeta, KeyRightMeta:
		return LogicalLogo
	default:
		return Logical(code)
	}
}

func IsLogicalModifier(key Logical) bool {
	return key >= LogicalCtrl && key <= LogicalLogo
}

func IsPhysicalModifier(code uint16) bool {
	switch code {
	case KeyLeftCtrl, KeyRightCtrl, KeyLeftShift, KeyRightShift,
		KeyLeftAlt, KeyRightAlt, KeyLeftMeta, KeyRightMeta:
		return true
	default:
		return false
	}
}
