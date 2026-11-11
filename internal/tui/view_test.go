package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"agentops/internal/trace"
)

func TestViewShowsSummaryRunsStepsAndPostmortem(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	out := plain(m)
	for _, want := range []string{"3 прогона", "✓ 1", "✗ 2", "в среднем", "ПРОГОНЫ", "run-01", "run-02", "run-03", "▶ ✓ run-01", "↑↓ выбор"} {
		if !strings.Contains(out, want) {
			t.Errorf("main screen lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "РАЗБОР") {
		t.Error("a passed run has no diagnosis block")
	}
	for _, s := range m.selected().Steps {
		if !strings.Contains(out, s.Tool) {
			t.Errorf("step %d (%s) missing", s.N, s.Tool)
		}
	}

	m = press(m, "j")
	out = plain(m)
	for _, want := range []string{"run-02 · ✗ провал · haiku · no-edit · fix-tests", "stats.go · запрещено", "РАЗБОР · правило", "Причина на шаге 3", "Причина: Агенту запрещено менять файлы", "Что поправить: Разрешить агенту Edit", "✗ провал"} {
		if !strings.Contains(out, want) {
			t.Errorf("run-02 lacks %q:\n%s", want, out)
		}
	}

	m = press(m, "j")
	if out = plain(m); !strings.Contains(out, "разбора ещё нет — выполни команду analyze") {
		t.Errorf("run-03 must point to analyze:\n%s", out)
	}
}

func TestModelSourceShowsCost(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "j")
	m.selected().Postmortem.Source = "model"
	m.selected().Postmortem.CostUSD = 0.01
	if out := plain(m); !strings.Contains(out, "РАЗБОР · модель $0.01") {
		t.Fatalf("model diagnosis title missing:\n%s", out)
	}
}

func TestRunningRunShowsSpinner(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "run-01", "", "")
	m := sized(New(root), 100, 30)
	a := plain(m)
	m = send(m, spinMsg{})
	b := plain(m)
	if !strings.Contains(a, "идёт") || a == b {
		t.Fatalf("running run must show an animated glyph:\n%s\n%s", a, b)
	}
	if strings.Contains(a, "РАЗБОР") {
		t.Fatal("a running run has no diagnosis")
	}
}

// Box drawing breaks on any line whose cell width differs from the terminal width.
func TestEveryLineHasTerminalWidth(t *testing.T) {
	for _, size := range [][2]int{{80, 16}, {100, 30}, {133, 41}} {
		m := sized(New(fixtures), size[0], size[1])
		screens := map[string]Model{
			"main": m, "run-02": press(m, "j"), "steps": press(m, "j", "tab", "j"),
			"step": press(m, "j", "tab", "enter"), "help": press(m, "?"), "filter": press(m, "f"),
		}
		for name, s := range screens {
			lines := strings.Split(plain(s), "\n")
			if len(lines) != size[1] {
				t.Errorf("%s %v: %d lines", name, size, len(lines))
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w != size[0] {
					t.Errorf("%s %v line %d: width %d\n%s", name, size, i, w, l)
				}
			}
		}
	}
}

func TestViewEnablesAltScreenAndMouse(t *testing.T) {
	v := sized(New(fixtures), 100, 30).View()
	if !v.AltScreen || v.MouseMode == 0 {
		t.Fatal("alt screen and mouse must be on")
	}
}

func TestRowFitsLongContent(t *testing.T) {
	line := row(26, true, true, "▶", "", "✓", green, "очень-длинный-идентификатор-прогона", "12с")
	if w := lipgloss.Width(line); w != 26 {
		t.Fatalf("width %d", w)
	}
}

func TestListFitsFourDecimalCost(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m.runs[0].CostUSD = 0.0034
	if out := plain(m); !strings.Contains(out, "$0.0034 │") {
		t.Errorf("cost column cut:\n%s", out)
	}
}

func TestDeniedTagDroppedWhenNarrow(t *testing.T) {
	m := press(sized(New(fixtures), 100, 30), "j")
	if !strings.Contains(plain(m), "stats.go · запрещено") {
		t.Fatal("tag missing at 100 columns")
	}
	m = press(sized(New(fixtures), 80, 30), "j")
	m.runs[m.visible[m.sel]].Steps[2].Line = "internal/shop/pricing/discount.go"
	out := plain(m)
	if strings.Contains(out, "· запрещ") || !strings.Contains(out, "internal/shop") {
		t.Errorf("tag must be dropped, summary kept:\n%s", out)
	}
}

func longCmdModel(w int) Model {
	m := press(sized(New(fixtures), w, 30), "j")
	st := &m.runs[m.visible[m.sel]].Steps[1]
	st.Line = "go test ./... " + strings.Repeat("x", 86)
	return m
}

func TestStepLineUsesAvailableWidth(t *testing.T) {
	full := "go test ./... " + strings.Repeat("x", 86)
	if out := plain(longCmdModel(160)); !strings.Contains(out, full) {
		t.Errorf("100-rune command must be shown in full at 160 columns:\n%s", out)
	}
	m := longCmdModel(100)
	out := plain(m)
	if strings.Contains(out, full) || !strings.Contains(out, "xxx…") {
		t.Errorf("command must be cut with an ellipsis at 100 columns:\n%s", out)
	}
	for i, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w != 100 {
			t.Errorf("line %d width %d", i, w)
		}
	}
}

func TestDeniedTagWithLongLine(t *testing.T) {
	for _, c := range []struct {
		w   int
		tag bool
	}{{100, false}, {160, true}} {
		m := longCmdModel(c.w)
		m.runs[m.visible[m.sel]].Steps[1].Line = "go test ./... " + strings.Repeat("x", 56)
		m.runs[m.visible[m.sel]].Steps[1].Denied = true
		if got := strings.Contains(plain(m), "xxx · запрещено"); got != c.tag {
			t.Errorf("width %d: tag shown=%v want %v", c.w, got, c.tag)
		}
	}
}

func lineWith(out, sub string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(ansi.Strip(l), sub) {
			return l
		}
	}
	return ""
}

func TestReverseOnlyOnSelectedRowOfFocusedPane(t *testing.T) {
	const rev = "\x1b[7m"
	hasRev := func(l string) bool {
		return strings.Contains(l, rev) || strings.Contains(l, ";7m") || strings.Contains(l, ";7;")
	}
	m := press(sized(New(fixtures), 100, 30), "tab")
	out := m.render()
	if l := lineWith(out, "run-01 · ✓ успех"); l == "" || hasRev(l) {
		t.Errorf("run header must not be reversed: %q", l)
	}
	if l := lineWith(out, "▶ ✓"); l == "" || !hasRev(l) {
		t.Errorf("selected step must be reversed: %q", l)
	}
	out = press(m, "tab").render()
	if l := lineWith(out, "ПРОГОНЫ"); l == "" || hasRev(l) {
		t.Errorf("ПРОГОНЫ title must not be reversed: %q", l)
	}
}

func TestStepTagsAndGlyphs(t *testing.T) {
	m := press(sized(New(fixtures), 160, 30), "j")
	out := plain(m)
	if !strings.Contains(out, "⊘ Edit  stats.go · запрещено ← разбор") {
		t.Errorf("denied cause step needs both tags and the ⊘ glyph at 160:\n%s", out)
	}
	if strings.Count(out, "← разбор") != 1 {
		t.Errorf("exactly one step carries the link:\n%s", out)
	}
	// Narrow: only the link fits next to the summary.
	m = press(sized(New(fixtures), 80, 30), "j")
	st := &m.runs[m.visible[m.sel]].Steps[2]
	st.Line = strings.Repeat("a", 14)
	out = plain(m)
	if !strings.Contains(out, "a ← разбор") || strings.Contains(out, "запрещено ←") {
		t.Errorf("link must win when both tags do not fit:\n%s", out)
	}
	// Step view status.
	if out = plain(press(sized(New(fixtures), 100, 30), "j", "tab", "j", "j", "enter")); !strings.Contains(out, "Статус: ⊘ запрещено") {
		t.Errorf("step view status:\n%s", out)
	}
	// No link without a diagnosis or with failed_step 0.
	m = sized(New(fixtures), 160, 30)
	m.runs[m.visible[m.sel]].Postmortem = &trace.Postmortem{}
	if strings.Contains(plain(m), "← разбор") {
		t.Error("failed_step 0 has no link")
	}
	for _, size := range [][2]int{{80, 30}, {100, 30}, {160, 30}} {
		m := press(sized(New(fixtures), size[0], size[1]), "j")
		for i, l := range strings.Split(plain(m), "\n") {
			if w := lipgloss.Width(l); w != size[0] {
				t.Errorf("%v line %d width %d", size, i, w)
			}
		}
	}
}
