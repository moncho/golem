package watch

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Colors are 256-color indexes; glyphs are all single-cell characters.
const (
	cRouter   = 177
	cWatchdog = 75
	cRunning  = 45
	cWaiting  = 214
	cDone     = 78
	cFailed   = 203
	cFaded    = 243
	cDim      = 240
	cText     = 253
	cBright   = 255
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func color(code int, s string) string { return "\x1b[38;5;" + itoa(code) + "m" + s + "\x1b[0m" }
func bold(s string) string            { return "\x1b[1m" + s + "\x1b[0m" }
func invert(s string) string          { return "\x1b[7m" + s + "\x1b[0m" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// plain strips the escape sequences; width counts the terminal cells that
// remain, two for East Asian wide and emoji-presentation runes.
func plain(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func width(s string) int {
	n := 0
	for _, r := range s {
		n += cells(r)
	}
	return n
}

func cells(r rune) int {
	if wide(r) {
		return 2
	}
	return 1
}

// wideRanges are the East Asian Wide and Fullwidth blocks plus the symbols
// with default emoji presentation, which terminals draw two cells wide.
var wideRanges = [][2]rune{
	{0x1100, 0x115F}, {0x231A, 0x231B}, {0x2329, 0x232A}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F}, {0x2693, 0x2693}, {0x26A1, 0x26A1},
	{0x26AA, 0x26AB}, {0x26BD, 0x26BE}, {0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA},
	{0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD}, {0x2705, 0x2705}, {0x270A, 0x270B},
	{0x2728, 0x2728}, {0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50}, {0x2B55, 0x2B55},
	{0x2E80, 0x303E}, {0x3041, 0x33FF}, {0x3400, 0x4DBF}, {0x4E00, 0x9FFF}, {0xA000, 0xA4CF}, {0xAC00, 0xD7A3},
	{0xF900, 0xFAFF}, {0xFE30, 0xFE4F}, {0xFF00, 0xFF60}, {0xFFE0, 0xFFE6},
	{0x1F300, 0x1F64F}, {0x1F680, 0x1F6FF}, {0x1F900, 0x1F9FF}, {0x20000, 0x3FFFD},
}

func wide(r rune) bool {
	for _, rg := range wideRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// clip cuts s to at most w visible cells, keeping escape sequences intact,
// never splitting a wide rune, and ending with a reset so a cut color never
// bleeds into the next cell.
func clip(s string, w int) string {
	if width(plain(s)) <= w {
		return s
	}
	var out strings.Builder
	used := 0
	for i := 0; i < len(s); {
		if loc := ansiPattern.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			out.WriteString(s[i : i+loc[1]])
			i += loc[1]
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if used+cells(r) > w {
			break
		}
		out.WriteRune(r)
		used += cells(r)
		i += size
	}
	out.WriteString("\x1b[0m")
	return out.String()
}

// fit pads or clips a string, which may carry escape sequences, to exactly w
// visible cells.
func fit(s string, w int) string {
	s = clip(s, w)
	if n := width(plain(s)); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// ellipsis shortens a plain string to at most w cells with a trailing mark.
func ellipsis(s string, w int) string {
	if width(s) <= w {
		return s
	}
	if w <= 1 {
		return fit("", w)
	}
	var out strings.Builder
	used := 0
	for _, r := range s {
		if used+cells(r) > w-1 {
			break
		}
		out.WriteRune(r)
		used += cells(r)
	}
	return out.String() + "…"
}
