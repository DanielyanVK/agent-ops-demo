package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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
	for _, want := range []string{"run-02 · ✗ провал · haiku · no-edit · fix-tests", "stats.go · запрещено", "РАЗБОР · правило", "Сломалось на шаге 3", "Причина: Агенту запрещено менять файлы", "Что поправить: Разрешить агенту Edit", "✗ провал"} {
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
	m.runs[m.visible[m.sel]].Steps[2].Summary = "internal/shop/pricing/discount.go"
	out := plain(m)
	if strings.Contains(out, "· запрещ") || !strings.Contains(out, "internal/shop") {
		t.Errorf("tag must be dropped, summary kept:\n%s", out)
	}
}
