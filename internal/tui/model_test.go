package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agentops/internal/trace"
)

const fixtures = "../trace/testdata/runs"

func sized(m Model, w, h int) Model {
	return send(m, tea.WindowSizeMsg{Width: w, Height: h})
}

func send(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func press(m Model, keys ...string) Model {
	codes := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"tab": tea.KeyTab, "enter": tea.KeyEnter, "esc": tea.KeyEscape, "pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp}
	for _, k := range keys {
		if c, ok := codes[k]; ok {
			m = send(m, tea.KeyPressMsg{Code: c})
		} else {
			m = send(m, tea.KeyPressMsg{Code: []rune(k)[0], Text: k})
		}
	}
	return m
}

func plain(m Model) string { return ansi.Strip(m.render()) }

// writeRun makes a run directory; verdict is a raw JSON fragment or "" for a running run.
func writeRun(t *testing.T, root, id, verdict, traceLines string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"` + id + `","task":"t","agent":"claude","model":"haiku","scenario":"s"` + verdict + `}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if traceLines != "" {
		if err := os.WriteFile(filepath.Join(dir, "trace.jsonl"), []byte(traceLines), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKeyNames(t *testing.T) {
	for want, code := range map[string]rune{"up": tea.KeyUp, "pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "esc": tea.KeyEscape, "tab": tea.KeyTab, "enter": tea.KeyEnter} {
		if got := (tea.KeyPressMsg{Code: code}).String(); got != want {
			t.Errorf("key %q is named %q", want, got)
		}
	}
}

func TestSelectionMovesWithinBounds(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "up")
	if m.selID != "run-01" {
		t.Fatalf("selection left the list at the top: %s", m.selID)
	}
	m = press(m, "j", "down", "down", "down")
	if m.selID != "run-03" {
		t.Fatalf("selection left the list at the bottom: %s", m.selID)
	}
	m = press(m, "k")
	if m.selID != "run-02" {
		t.Fatalf("k did not move up: %s", m.selID)
	}
}

func TestFilterKeepsValidSelection(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "f")
	if len(m.visible) != 2 || m.selID != "run-02" {
		t.Fatalf("passed run-01 must drop out and selection move to the next: visible=%d sel=%s", len(m.visible), m.selID)
	}
	m = press(m, "j", "f")
	if len(m.visible) != 3 || m.selID != "run-03" {
		t.Fatalf("selection must survive turning the filter off: visible=%d sel=%s", len(m.visible), m.selID)
	}
}

func TestFilterWithNoFailures(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "run-01", `,"verdict":{"passed":true,"test_output":""}`, "")
	m := sized(New(root), 100, 30)
	m = press(m, "f", "tab", "enter")
	if m.selected() != nil || m.focus != focusList || m.screen != screenMain {
		t.Fatal("with nothing to show, focus must stay on the list")
	}
	if !strings.Contains(plain(m), "провалов нет") {
		t.Fatal("empty filter must say so")
	}
}

func TestFocusSwitching(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	for _, k := range []string{"tab", "right", "l", "enter"} {
		m = press(m, k)
		if m.focus != focusSteps {
			t.Fatalf("%s did not focus steps", k)
		}
		m = press(m, "esc")
		if m.focus != focusList {
			t.Fatal("esc did not return to the list")
		}
	}
	m = press(m, "tab", "tab")
	if m.focus != focusList {
		t.Fatal("tab in steps must go back to the list")
	}
	m = press(m, "tab", "h")
	if m.focus != focusList {
		t.Fatal("h in steps must go back to the list")
	}
}

func TestStepSelectionAndReset(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "tab", "j", "j")
	if m.step != 2 {
		t.Fatalf("step = %d", m.step)
	}
	m = press(m, "j", "j", "j", "j", "j", "j", "j", "j", "j", "j")
	if last := len(m.selected().Steps) - 1; m.step != last {
		t.Fatalf("step %d out of bounds, last is %d", m.step, last)
	}
	m = press(m, "esc", "j")
	if m.step != 0 {
		t.Fatal("changing the run must reset the step")
	}
}

func TestStepViewAndHelp(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "enter", "enter")
	if m.screen != screenStep {
		t.Fatal("enter on a step must open the step view")
	}
	out := plain(m)
	for _, want := range []string{"ВХОД", "ВЫХОД", "Инструмент:", "Статус:"} {
		if !strings.Contains(out, want) {
			t.Errorf("step view lacks %q", want)
		}
	}
	m = press(m, "pgdown", "pgdown", "up")
	m = press(m, "esc")
	if m.screen != screenMain || m.focus != focusSteps {
		t.Fatal("esc must return to the steps")
	}
	m = press(m, "?")
	if m.screen != screenHelp || !strings.Contains(plain(m), "Шаг на весь экран") {
		t.Fatal("? must open the help overlay")
	}
	m = press(m, "x")
	if m.screen != screenMain {
		t.Fatal("any key must close help")
	}
	m = press(m, "enter", "q")
	if m.screen != screenMain {
		t.Fatal("q in the step view goes back, it must not quit")
	}
}

func TestStepViewScrollIsBounded(t *testing.T) {
	root := t.TempDir()
	out := strings.Repeat("строка вывода\\n", 100)
	tr := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + out + `"}]}}` + "\n"
	writeRun(t, root, "run-01", `,"verdict":{"passed":true,"test_output":""}`, tr)
	m := sized(New(root), 100, 30)
	m = press(m, "enter", "enter")
	for range 20 {
		m = press(m, "pgdown")
	}
	n := len(m.stepLines(98))
	if m.vert != n-m.bodyH() {
		t.Fatalf("scroll = %d, want %d", m.vert, n-m.bodyH())
	}
	if !strings.Contains(plain(m), "строка вывода") {
		t.Fatal("scrolled to the end, output must still be visible")
	}
	m = press(m, "pgup", "pgup", "pgup", "pgup", "pgup", "pgup", "pgup", "pgup")
	if m.vert != 0 {
		t.Fatalf("scroll = %d, want 0", m.vert)
	}
}

func TestQuit(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		if _, cmd := m.Update(k); cmd == nil || cmd() != tea.Quit() {
			t.Errorf("%v must quit", k)
		}
	}
}

func TestReloadKeepsSelectedID(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "j", "tab", "j")
	runs, _, _ := trace.LoadDir(fixtures)
	m = send(m, reloadMsg{runs: append([]trace.Run{{ID: "run-00"}}, runs...)})
	if m.selID != "run-02" || m.step != 1 {
		t.Fatalf("selection must follow the ID and keep the step: %s step %d", m.selID, m.step)
	}
}

func TestReloadWhenSelectedRunDisappears(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = press(m, "j", "j", "enter", "enter")
	runs, _, _ := trace.LoadDir(fixtures)
	m = send(m, reloadMsg{runs: runs[:2]})
	if m.selID != "run-02" || m.screen != screenMain {
		t.Fatalf("selection must fall back to a neighbour and close the step view: %s", m.selID)
	}
	m = send(m, reloadMsg{})
	if m.selected() != nil || m.focus != focusList {
		t.Fatal("empty reload must leave no selection")
	}
	if !strings.Contains(plain(m), `прогонов нет`) || !strings.Contains(plain(m), "выполни команду") {
		t.Fatal("empty directory message missing")
	}
}

func TestReloadErrorKeepsRuns(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	m = send(m, reloadMsg{err: os.ErrPermission})
	if len(m.runs) != 3 {
		t.Fatal("a failed reload must not wipe the list")
	}
}

func TestMouse(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	// Rows start at y=5: border, header, separator, pane title, blank.
	m = send(m, tea.MouseClickMsg{X: 5, Y: 6, Button: tea.MouseLeft})
	if m.selID != "run-02" {
		t.Fatalf("click on a run row: %s", m.selID)
	}
	m = send(m, tea.MouseClickMsg{X: 50, Y: bodyTop + paneHead + m.pmHeight() + 1, Button: tea.MouseLeft})
	if m.focus != focusSteps || m.step != 1 {
		t.Fatalf("click on a step row: focus %v step %d", m.focus, m.step)
	}
	m = send(m, tea.MouseClickMsg{X: 5, Y: 20, Button: tea.MouseLeft})
	if m.selID != "run-02" {
		t.Fatal("click below the rows must change nothing")
	}
}

func TestWheelScrollsPaneUnderCursor(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"run-01", "run-02", "run-03", "run-04", "run-05", "run-06", "run-07", "run-08", "run-09", "run-10"} {
		writeRun(t, root, id, `,"verdict":{"passed":true,"test_output":""}`, "")
	}
	m := sized(New(root), 100, 16) // body is 10 rows: 8 list rows for 10 runs
	m = send(m, tea.MouseWheelMsg{X: 5, Y: 6, Button: tea.MouseWheelDown})
	if m.listScroll == 0 || m.selID != "run-01" || m.bodyScroll != 0 {
		t.Fatalf("wheel over the list: listScroll=%d sel=%s bodyScroll=%d", m.listScroll, m.selID, m.bodyScroll)
	}
	m = send(m, tea.MouseWheelMsg{X: 5, Y: 6, Button: tea.MouseWheelUp})
	if m.listScroll != 0 {
		t.Fatal("wheel up must scroll back")
	}
}

func TestSelectionStaysVisibleInLongList(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"run-01", "run-02", "run-03", "run-04", "run-05", "run-06", "run-07", "run-08", "run-09", "run-10"} {
		writeRun(t, root, id, `,"verdict":{"passed":true,"test_output":""}`, "")
	}
	m := sized(New(root), 100, 16)
	for range 9 {
		m = press(m, "j")
	}
	if !strings.Contains(plain(m), "▶ ✓ run-10") {
		t.Fatalf("selected row scrolled out of view:\n%s", plain(m))
	}
}

func TestResizeTooSmallAndBack(t *testing.T) {
	m := sized(New(fixtures), 79, 30)
	if !strings.Contains(plain(m), "терминал слишком мал (нужно 80×16)") {
		t.Fatal("narrow terminal must show the notice")
	}
	m = sized(m, 100, 15)
	if !strings.Contains(plain(m), "терминал слишком мал") {
		t.Fatal("short terminal must show the notice")
	}
	if m = press(m, "j"); m.selID != "run-01" {
		t.Fatal("keys other than quit are ignored on a too small terminal")
	}
	m = sized(m, 100, 30)
	if !strings.Contains(plain(m), "ПРОГОНЫ") {
		t.Fatal("growing the terminal must bring the layout back")
	}
}
