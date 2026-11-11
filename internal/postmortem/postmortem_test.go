package postmortem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentops/internal/trace"
)

const modelReply = `{"result":"{\"failed_step\": 2, \"cause\": \"Тест не прошёл.\", \"fix\": \"Уточнить задание.\"}","is_error":false,"total_cost_usd":0.012}`

// fakeClaude installs a stand-in for the claude binary and returns the marker
// file it creates when started.
func fakeClaude(t *testing.T, reply string) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	script := "#!/bin/sh\ncat >/dev/null\ntouch '" + marker + "'\ncat <<'EOF'\n" + reply + "\nEOF\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	return marker
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// noRule is a failed run no rule can explain.
func noRule() trace.Run {
	steps := []trace.Step{
		{N: 1, Tool: "Read", Summary: "stats.go", Output: "OUT-READ"},
		{N: 2, Tool: "Bash", Summary: "go test", Output: "OUT-BASH-ERR", IsError: true},
		{N: 3, Tool: "Read", Summary: "a.go", Output: "OUT-3"},
		{N: 4, Tool: "Read", Summary: "b.go", Output: "OUT-4"},
		{N: 5, Tool: "Read", Summary: "c.go", Output: "OUT-5"},
		{N: 6, Tool: "Read", Summary: "d.go", Output: "OUT-6"},
	}
	return trace.Run{
		ID: "run-09", Scenario: "secret-scenario", StopReason: "success", Steps: steps,
		Prompt: "PROMPT-TEXT", Result: "Готово.",
		Verdict: &trace.Verdict{Passed: false, TestOutput: "--- FAIL: TestMean"},
	}
}

func TestByRule(t *testing.T) {
	t.Run("never started", func(t *testing.T) {
		pm, ok := ByRule(trace.Run{ResultError: true, Result: strings.Repeat("я", 300), Stderr: "ignored"})
		if !ok || pm.FailedStep != 0 || pm.Source != "rule" || pm.CostUSD != 0 {
			t.Fatalf("got %+v, %v", pm, ok)
		}
		if pm.Cause != "Агент не начал работу: "+strings.Repeat("я", 200) {
			t.Errorf("cause not cut to 200 runes: %q", pm.Cause)
		}
		if pm.Fix != "Проверить, что агент запускается и имеет доступ к модели, затем повторить прогон" {
			t.Errorf("fix = %q", pm.Fix)
		}
		if pm, _ := ByRule(trace.Run{Stderr: "Error: invalid model\n"}); pm.Cause != "Агент не начал работу: Error: invalid model" {
			t.Errorf("stderr fallback: %q", pm.Cause)
		}
		if pm, _ := ByRule(trace.Run{StopReason: "incomplete"}); pm.Cause != "Агент не начал работу и ничего не сообщил" {
			t.Errorf("silent: %q", pm.Cause)
		}
	})
	t.Run("max turns", func(t *testing.T) {
		pm, ok := ByRule(trace.Run{StopReason: "error_max_turns", Turns: 5, Steps: []trace.Step{{N: 1}}})
		if !ok || pm.FailedStep != 0 ||
			pm.Cause != "Агент остановлен по лимиту ходов, не закончив задачу" ||
			pm.Fix != "Поднять лимит ходов или сузить задачу" {
			t.Fatalf("got %+v, %v", pm, ok)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		pm, ok := ByRule(trace.Run{StopReason: "timeout", Steps: []trace.Step{{N: 1}}})
		if !ok || pm.FailedStep != 0 || pm.Cause != "Агент не уложился в отведённое время" ||
			pm.Fix != "Увеличить лимит времени или сузить задачу" {
			t.Fatalf("got %+v, %v", pm, ok)
		}
	})
	t.Run("order", func(t *testing.T) {
		if pm, _ := ByRule(trace.Run{StopReason: "error_max_turns"}); !strings.HasPrefix(pm.Cause, "Агент не начал работу") {
			t.Errorf("no steps must win over the turn limit: %q", pm.Cause)
		}
		blocked := trace.Run{StopReason: "error_max_turns", Steps: []trace.Step{{N: 1, Tool: "Edit", Kind: "edit", Denied: true}}}
		if pm, _ := ByRule(blocked); !strings.HasPrefix(pm.Cause, "Агенту запрещено менять файлы") {
			t.Errorf("a blocked edit must win over the turn limit: %q", pm.Cause)
		}
	})
	t.Run("blocked edit", func(t *testing.T) {
		r := trace.Run{Steps: []trace.Step{
			{N: 1, Tool: "Bash", Denied: true},
			{N: 2, Tool: "Edit", Kind: "edit", Denied: true},
			{N: 3, Tool: "Write", Kind: "edit", Denied: true},
			{N: 4, Tool: "Edit", Kind: "edit", Denied: true},
		}}
		pm, ok := ByRule(r)
		if !ok || pm.FailedStep != 2 ||
			pm.Cause != "Агенту запрещено менять файлы (заблокировано: Edit ×2, Write), исправления не применены" ||
			pm.Fix != "Разрешить агенту Edit, Write для этой задачи или убрать из неё правку файлов" {
			t.Fatalf("got %+v, %v", pm, ok)
		}
	})
	t.Run("bash-only denials go to the model", func(t *testing.T) {
		r := noRule()
		r.Steps[1].Denied = true
		if _, ok := ByRule(r); ok {
			t.Fatal("rule matched")
		}
	})
	t.Run("real sample", func(t *testing.T) {
		r, err := trace.LoadRun("testdata/run-02")
		if err != nil {
			t.Fatal(err)
		}
		pm, ok := ByRule(r)
		if !ok || pm.FailedStep != 10 || !strings.Contains(pm.Cause, "Edit ×3") {
			t.Fatalf("got %+v, %v", pm, ok)
		}
	})
	t.Run("fixture postmortem is the rule's own", func(t *testing.T) {
		r, err := trace.LoadRun("../trace/testdata/runs/run-02")
		if err != nil {
			t.Fatal(err)
		}
		if pm, ok := ByRule(r); !ok || pm != *r.Postmortem {
			t.Fatalf("rule %+v, fixture %+v", pm, *r.Postmortem)
		}
	})
	t.Run("no match", func(t *testing.T) {
		if _, ok := ByRule(noRule()); ok {
			t.Fatal("rule matched")
		}
	})
}

func TestBuildPrompt(t *testing.T) {
	r := noRule()
	r.Stderr = "STDERR-TEXT"
	p := BuildPrompt(r)
	for _, want := range []string{"Задание агенту: PROMPT-TEXT\n", "STDERR-TEXT", "1. [✓] Read stats.go", "2. [✗] Bash go test",
		"OUT-BASH-ERR", "OUT-4", "OUT-5", "OUT-6", "Готово.", "--- FAIL: TestMean", "failed_step", "собственное действие", "0, если причина в формулировке задания, правах или лимитах"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, bad := range []string{"secret-scenario", "OUT-READ", "OUT-3"} {
		if strings.Contains(p, bad) {
			t.Errorf("prompt contains %q", bad)
		}
	}
}

func TestBuildPromptDenied(t *testing.T) {
	r := noRule()
	r.Steps[1].Denied = true // IsError too: boilerplate text must not be sent
	r.Steps[0].IsError, r.Steps[0].Output = true, "OUT-REAL-ERR"
	p := BuildPrompt(r)
	if !strings.Contains(p, "2. [✗] Bash go test (запрещено)\n") || strings.Contains(p, "OUT-BASH-ERR") {
		t.Errorf("denied step:\n%s", p)
	}
	if !strings.Contains(p, "OUT-REAL-ERR") {
		t.Error("non-denied error output missing")
	}
}

func TestParse(t *testing.T) {
	inner := `{"failed_step": 2, "cause": "C", "fix": "F"}`
	wrap := func(reply string) []byte {
		return []byte(`{"is_error":false,"total_cost_usd":0.5,"result":` + quote(reply) + `}`)
	}
	for name, reply := range map[string]string{
		"plain":  inner,
		"fenced": "```json\n" + inner + "\n```",
		"prose":  "Вот разбор: " + inner + " Надеюсь, помогло.",
	} {
		pm, err := Parse(wrap(reply))
		if err != nil || pm.FailedStep != 2 || pm.Cause != "C" || pm.Fix != "F" || pm.Source != "model" || pm.CostUSD != 0.5 {
			t.Errorf("%s: %+v, %v", name, pm, err)
		}
	}
	bad := map[string][]byte{
		"garbage":      []byte("не json"),
		"api error":    []byte(`{"is_error":true,"result":"rate limit"}`),
		"no object":    wrap("просто текст"),
		"empty cause":  wrap(`{"failed_step":1,"cause":"","fix":"F"}`),
		"broken inner": wrap(`{oops}`),
	}
	for name, out := range bad {
		if _, err := Parse(out); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Parse(bad["api error"]); !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("is_error message lacks result: %v", err)
	}
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestAnalyze(t *testing.T) {
	marker := fakeClaude(t, modelReply)

	rule := trace.Run{StopReason: "error_max_turns", Turns: 3}
	pm, err := Analyze(context.Background(), rule, "haiku")
	if err != nil || pm.Source != "rule" || exists(marker) {
		t.Fatalf("rule path: %+v, %v, called=%v", pm, err, exists(marker))
	}

	pm, err = Analyze(context.Background(), noRule(), "haiku")
	if err != nil || pm.Source != "model" || pm.CostUSD != 0.012 || pm.FailedStep != 2 || !exists(marker) {
		t.Fatalf("model path: %+v, %v, called=%v", pm, err, exists(marker))
	}
}

func TestAnalyzeRunsBareModel(t *testing.T) {
	dir := t.TempDir()
	args, wd := filepath.Join(dir, "args"), filepath.Join(dir, "wd")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '[%s]' \"$@\" > '" + args + "'\npwd > '" + wd + "'\nls -A | wc -l >> '" + wd + "'\n" +
		"cat <<'EOF'\n" + modelReply + "\nEOF\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	if _, err := Analyze(context.Background(), noRule(), "haiku"); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(args)
	if !strings.Contains(string(a), "[--safe-mode][--strict-mcp-config][--restricted]") ||
		!strings.Contains(string(a), "[--tools][][--system-prompt][Ты разбираешь неудачный прогон агента. Отвечай только JSON-объектом.]") {
		t.Errorf("args: %s", a)
	}
	w, _ := os.ReadFile(wd)
	cwd, files, _ := strings.Cut(string(w), "\n")
	if strings.TrimSpace(files) != "0" || !strings.Contains(cwd, "agentops-analyze-") {
		t.Errorf("working directory %q holds %s files", cwd, files)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Error("working directory not removed")
	}
}

func TestAnalyzeClampsStep(t *testing.T) {
	fakeClaude(t, `{"is_error":false,"total_cost_usd":0,"result":"{\"failed_step\": 99, \"cause\": \"C\", \"fix\": \"F\"}"}`)
	pm, err := Analyze(context.Background(), noRule(), "haiku")
	if err != nil || pm.FailedStep != 0 {
		t.Fatalf("%+v, %v", pm, err)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	want := trace.Postmortem{FailedStep: 2, Cause: "C", Fix: "F", Source: "model", CostUSD: 0.01}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "postmortem.json"))
	if err != nil || !strings.Contains(string(b), "\n  \"cause\": \"C\"") {
		t.Fatalf("%q, %v", b, err)
	}
}

func TestAnalyzeFailures(t *testing.T) {
	script := func(body string) {
		bin := filepath.Join(t.TempDir(), "claude")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	}
	script("sleep 5\n")
	_, err := analyze(context.Background(), noRule(), "haiku", 100*time.Millisecond)
	if err == nil || err.Error() != "модель не ответила за 2 минуты" {
		t.Errorf("timeout: %v", err)
	}
	script("exit 3\n")
	if _, err = Analyze(context.Background(), noRule(), "haiku"); err == nil || strings.HasSuffix(err.Error(), ": ") {
		t.Errorf("empty stderr: %q", err)
	}
	script("echo boom >&2; exit 3\n")
	if _, err = Analyze(context.Background(), noRule(), "haiku"); err == nil || !strings.HasSuffix(err.Error(), ": boom") {
		t.Errorf("stderr: %q", err)
	}
}

func TestByRuleOnCodexRuns(t *testing.T) {
	parse := func(lines string) trace.Run {
		r, err := trace.ParseCodex(strings.NewReader(lines))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	empty := parse(`{"type":"turn.started"}` + "\n")
	if pm, ok := ByRule(empty); !ok || !strings.HasPrefix(pm.Cause, "Агент не начал работу") {
		t.Errorf("no steps: %+v, %v", pm, ok)
	}
	blocked := parse(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"ls","aggregated_output":"","exit_code":0,"status":"completed"}}` + "\n" +
		`{"type":"item.completed","item":{"id":"b","type":"file_change","changes":[{"path":"/work/calc.go","kind":"update"}],"status":"failed"}}` + "\n")
	if pm, ok := ByRule(blocked); !ok || pm.FailedStep != 2 || !strings.Contains(pm.Cause, "Агенту запрещено менять файлы") {
		t.Errorf("blocked edit: %+v, %v", pm, ok)
	}
	timeout := parse(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}` + "\n" + `{"type":"timeout"}` + "\n")
	if pm, ok := ByRule(timeout); !ok || pm.Cause != "Агент не уложился в отведённое время" {
		t.Errorf("timeout: %+v, %v", pm, ok)
	}
}
