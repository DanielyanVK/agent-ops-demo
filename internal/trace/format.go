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

// Glyph: a blocked call is not a failed one.
func (s Step) Glyph() string {
	switch {
	case s.Denied:
		return "⊘"
	case s.IsError:
		return "✗"
	}
	return "✓"
}

// Label is the tool column of a step: a skill read is a Skill for both agents
// (Codex reads it through the shell). The JSON tool field keeps the real tool.
func (s Step) Label() string {
	if s.Kind == "skill" {
		return "Skill"
	}
	return s.Tool
}

// Text is what a step row shows: the name of a skill, MCP tool or subagent,
// otherwise the one-line call.
func (s Step) Text() string {
	switch s.Kind {
	case "skill", "mcp", "subagent":
		if s.Name != "" {
			return s.Name
		}
	}
	return s.Line
}

// ShortText is Text cut like Summary.
func (s Step) ShortText() string { return cut60(s.Text()) }

// CauseStep reports whether the diagnosis points at step s.
func (r Run) CauseStep(s Step) bool {
	return r.Postmortem != nil && r.Postmortem.FailedStep == s.N
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

// FmtTokens renders "86212 → 840"; empty when the agent reported no tokens.
func (r Run) FmtTokens() string {
	if r.TokensIn == 0 && r.TokensOut == 0 {
		return ""
	}
	return fmt.Sprintf("%d → %d", r.TokensIn, r.TokensOut)
}

// UsedParts lists what the run used, only the non-empty kinds: skills, MCP tools, subagents, web calls.
func (r Run) UsedParts() []string {
	var parts []string
	if len(r.Skills) > 0 {
		parts = append(parts, "навыки: "+strings.Join(r.Skills, ", "))
	}
	if len(r.MCP) > 0 {
		parts = append(parts, "MCP: "+FmtBlocked(r.MCP))
	}
	if r.Subagents > 0 {
		parts = append(parts, fmt.Sprintf("подагенты: %d", r.Subagents))
	}
	if r.WebCalls > 0 {
		parts = append(parts, fmt.Sprintf("веб: %d", r.WebCalls))
	}
	return parts
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
