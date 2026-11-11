package trace

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

func (s Status) Glyph() string {
	switch s {
	case Passed:
		return "✓"
	case Failed:
		return "✗"
	}
	return "…"
}

func FmtDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fс", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%dс", int(d.Seconds()))
	}
	sec := int(d.Seconds())
	return fmt.Sprintf("%dм %02dс", sec/60, sec%60)
}

func FmtCost(usd float64) string {
	if usd == 0 {
		return "—"
	}
	if usd < 0.1 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

func Plural(n int, one, few, many string) string {
	switch m10, m100 := n%10, n%100; {
	case m10 == 1 && m100 != 11:
		return one
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return few
	}
	return many
}

// FmtBlocked renders "Edit ×3, Bash ×4"; a count of 1 is omitted.
func FmtBlocked(b []ToolCount) string {
	parts := make([]string, len(b))
	for i, t := range b {
		parts[i] = t.Tool
		if t.N > 1 {
			parts[i] += fmt.Sprintf(" ×%d", t.N)
		}
	}
	return strings.Join(parts, ", ")
}

// Clean makes agent-controlled text safe to print on a terminal: escape
// sequences and control characters are dropped (they could clear the screen or
// set the window title), tabs become four spaces, newlines stay.
func Clean(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == 0x1b:
			i += escLen(s[i+1:])
		case c == '\t':
			b.WriteString("    ")
		case c == '\n' || c >= 0x20 && c != 0x7f:
			b.WriteByte(c)
		}
	}
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) { // C1 controls such as the 8-bit CSI
			return -1
		}
		return r
	}, b.String())
}

// escLen is the length of the escape sequence whose ESC was just read: CSI runs
// to a final byte, OSC/DCS/APC/PM/SOS strings to BEL or ESC \, the rest is
// ESC, intermediate bytes and one final byte.
func escLen(s string) int {
	if s == "" {
		return 0
	}
	switch s[0] {
	case '[':
		for i := 1; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']', 'P', '_', '^', 'X':
		for i := 1; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		i := 0
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			i++
		}
		return i
	}
	return len(s)
}
