package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const longCause = "Причина-маркер: команда тестов падала на каждом шаге, потому что файл так и не был изменён, и агент снова и снова запускал одно и то же вместо правки"

// writeLongFailed builds a failed run with n Bash steps and a postmortem. Step 2
// carries a tab and escape sequences in its command.
func writeLongFailed(t *testing.T, root string, n int) {
	t.Helper()
	writeRun(t, root, "run-01", `,"verdict":{"passed":false,"test_output":"FAIL"}`, "")
	var b strings.Builder
	for i := 1; i <= n; i++ {
		cmd := fmt.Sprintf("go test ./pkg%d", i)
		if i == 2 {
			cmd = `echo\tX \u001b[2Jcleared \u001b]0;hacked\u0007end`
		}
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"%s"}}]}}`+"\n", i, cmd)
		fmt.Fprintf(&b, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t%d","content":"FAIL","is_error":true}]}}`+"\n", i)
	}
	dir := filepath.Join(root, "run-01")
	if err := os.WriteFile(filepath.Join(dir, "trace.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	pm := `{"failed_step":30,"cause":"` + longCause + `","fix":"Правка-маркер: измени файл.","source":"rule","cost_usd":0}`
	if err := os.WriteFile(filepath.Join(dir, "postmortem.json"), []byte(pm), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedPostmortemReachableWithManySteps(t *testing.T) {
	root := t.TempDir()
	writeLongFailed(t, root, 30)
	for _, size := range [][2]int{{100, 30}, {80, 16}} {
		base := sized(New(root), size[0], size[1])
		for name, m := range map[string]Model{
			"list": base, "steps-last": press(base, "tab", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j"),
		} {
			out := plain(m)
			if !strings.Contains(out, "Причина-маркер") || !strings.Contains(out, "РАЗБОР · правило") {
				t.Errorf("%v %s: diagnosis not visible:\n%s", size, name, out)
			}
			if name == "steps-last" && (m.step != 29 || !strings.Contains(out, "▶ 30")) {
				t.Errorf("%v: last step not selected/visible (step %d):\n%s", size, m.step, out)
			}
			if rows := strings.Count(out, "go test ./pkg"); rows < 3 {
				t.Errorf("%v %s: only %d step rows visible", size, name, rows)
			}
			for i, l := range strings.Split(out, "\n") {
				if w := lipgloss.Width(l); w != size[0] {
					t.Errorf("%v %s line %d width %d", size, name, i, w)
				}
			}
		}
	}
}

func TestLongPostmortemIsCutWithEllipsis(t *testing.T) {
	root := t.TempDir()
	writeLongFailed(t, root, 30)
	m := sized(New(root), 80, 16)
	if !strings.Contains(plain(m), "…") {
		t.Fatalf("cut diagnosis must end with an ellipsis:\n%s", plain(m))
	}
}

func TestClickOnStepIsOffsetByPinnedBlock(t *testing.T) {
	root := t.TempDir()
	writeLongFailed(t, root, 30)
	m := sized(New(root), 100, 30)
	y := bodyTop + paneHead + m.pmHeight() + 4
	m = send(m, tea.MouseClickMsg{X: 50, Y: y, Button: tea.MouseLeft})
	if m.focus != focusSteps || m.step != 4 {
		t.Fatalf("focus %v step %d", m.focus, m.step)
	}
}

func TestAgentTextCannotBreakTheScreen(t *testing.T) {
	root := t.TempDir()
	writeLongFailed(t, root, 5)
	m := sized(New(root), 100, 30)
	m = press(m, "tab")
	raw := m.render()
	if strings.Contains(raw, "[2J") || strings.Contains(raw, "hacked") || strings.Contains(raw, "\t") || strings.Contains(raw, "\a") {
		t.Fatalf("agent-controlled escape leaked into the screen: %q", raw)
	}
	if !strings.Contains(plain(m), "echo    X cleared") {
		t.Fatalf("summary must keep the visible text:\n%s", plain(m))
	}
	for i, l := range strings.Split(plain(m), "\n") {
		if w := lipgloss.Width(l); w != 100 {
			t.Errorf("line %d width %d", i, w)
		}
	}
}

func TestSpinnerTicksOnlyWhileRunning(t *testing.T) {
	m := sized(New(fixtures), 100, 30)
	if _, cmd := m.Update(spinMsg{}); cmd != nil {
		t.Fatal("no running run: no tick may be armed")
	}
	root := t.TempDir()
	writeRun(t, root, "run-01", "", "")
	m = sized(New(root), 100, 30)
	if !m.spinning {
		t.Fatal("running run at start must spin")
	}
	if _, cmd := m.Update(spinMsg{}); cmd == nil {
		t.Fatal("running run: tick must be re-armed")
	}
	// A run appearing on reload restarts the spinner exactly once.
	idle := sized(New(fixtures), 100, 30)
	next, cmd := idle.Update(reloadMsg{runs: m.runs})
	if !next.(Model).spinning || cmd == nil {
		t.Fatal("reload that brings a running run must arm the spinner")
	}
	again, _ := next.Update(reloadMsg{runs: m.runs})
	if !again.(Model).spinning {
		t.Fatal("spinner flag must stay set while running")
	}
	stopped, cmd := again.Update(reloadMsg{runs: idle.runs})
	if cmd == nil {
		t.Fatal("reload must always re-arm itself")
	}
	next, cmd = stopped.Update(spinMsg{})
	if cmd != nil || next.(Model).spinning {
		t.Fatal("tick after the last run finished must stop the spinner")
	}
}

func TestMissingRunsDirShowsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого")
	m := sized(New(missing), 100, 30)
	out := plain(m)
	if !strings.Contains(out, "не удалось прочитать") || strings.Contains(out, "выполни команду") {
		t.Fatalf("a mistyped directory must not look empty:\n%s", out)
	}
	// Once the directory appears, the next reload clears the error.
	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	runs, err := load(missing)
	m = send(m, reloadMsg{runs, err})
	if m.loadErr != nil || !strings.Contains(plain(m), "выполни команду") {
		t.Fatal("error must clear after a good reload")
	}
}

func TestWheelOverRightPaneScrollsStepsOnly(t *testing.T) {
	root := t.TempDir()
	writeLongFailed(t, root, 30)
	m := sized(New(root), 100, 30)
	m = send(m, tea.MouseWheelMsg{X: 50, Y: 12, Button: tea.MouseWheelDown})
	if m.bodyScroll != 3 || m.step != 0 || m.listScroll != 0 {
		t.Fatalf("bodyScroll %d step %d listScroll %d", m.bodyScroll, m.step, m.listScroll)
	}
	if !strings.Contains(plain(m), "Причина-маркер") {
		t.Fatal("diagnosis must stay pinned while the steps scroll")
	}
	m = send(m, tea.MouseWheelMsg{X: 50, Y: 12, Button: tea.MouseWheelUp})
	if m.bodyScroll != 0 {
		t.Fatalf("bodyScroll %d", m.bodyScroll)
	}
}
