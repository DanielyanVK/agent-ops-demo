package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copyRuns(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("..", "..", "internal", "trace", "testdata", "runs")
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// failingClaude makes any attempt to start the model fail the test.
func failingClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	t.Cleanup(func() {
		if _, err := os.Stat(marker); err == nil {
			t.Error("claude was started")
		}
	})
}

func TestAnalyze(t *testing.T) {
	failingClaude(t)
	runs := copyRuns(t)
	before, _ := os.ReadFile(filepath.Join(runs, "run-02", "postmortem.json"))

	var out bytes.Buffer
	if err := runAnalyze([]string{"--runs", runs}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "run-03  разбор готов · правило") ||
		strings.Contains(out.String(), "run-02") ||
		!strings.Contains(out.String(), "правилами: 1 · моделью: 0 · потрачено $0") {
		t.Errorf("output:\n%s", out.String())
	}
	b, err := os.ReadFile(filepath.Join(runs, "run-03", "postmortem.json"))
	if err != nil || !strings.Contains(string(b), `"source": "rule"`) {
		t.Errorf("run-03 postmortem: %s, %v", b, err)
	}
	after, _ := os.ReadFile(filepath.Join(runs, "run-02", "postmortem.json"))
	if !bytes.Equal(before, after) {
		t.Error("run-02 postmortem was rewritten without --force")
	}

	out.Reset()
	if err := runAnalyze([]string{"--runs", runs}, &out, &out); err != nil || strings.TrimSpace(out.String()) != "разбирать нечего" {
		t.Errorf("second run: %q, %v", out.String(), err)
	}
}

func TestAnalyzeJSON(t *testing.T) {
	failingClaude(t)
	runs := copyRuns(t)
	var out bytes.Buffer
	if err := runAnalyze([]string{"run-03", "--runs", runs, "--force", "--json"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID      string  `json:"id"`
		OK      bool    `json:"ok"`
		Source  string  `json:"source"`
		CostUSD float64 `json:"cost_usd"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != "run-03" || !got.OK || got.Source != "rule" || got.CostUSD != 0 {
		t.Errorf("%q, %+v, %v", out.String(), got, err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("expected one line: %q", out.String())
	}
}

func TestAnalyzeModelAndFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	reply := `{"is_error":false,"total_cost_usd":0.014,"result":"{\"failed_step\": 1, \"cause\": \"C\", \"fix\": \"F\"}"}`
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EOF'\n"+reply+"\nEOF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)

	// A run no rule matches: one step, success stop reason, no denials.
	runs := t.TempDir()
	rd := filepath.Join(runs, "run-05")
	if err := os.MkdirAll(rd, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"run-05","task":"fix-tests","agent":"claude","model":"haiku","scenario":"x","started_at":"2026-11-11T10:00:00Z","verdict":{"passed":false,"test_output":"FAIL"}}`
	trace := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":1000,"num_turns":1,"result":"ok","total_cost_usd":0.01}` + "\n"
	os.WriteFile(filepath.Join(rd, "meta.json"), []byte(meta), 0o644)
	os.WriteFile(filepath.Join(rd, "trace.jsonl"), []byte(trace), 0o644)

	var out bytes.Buffer
	if err := runAnalyze([]string{"--runs", runs}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "run-05  разбор готов · модель $0.01") ||
		!strings.Contains(out.String(), "правилами: 0 · моделью: 1 · потрачено $0.01") {
		t.Errorf("output:\n%s", out.String())
	}

	t.Setenv("AGENTOPS_CLAUDE_BIN", filepath.Join(dir, "missing"))
	out.Reset()
	err := runAnalyze([]string{"--runs", runs, "--force"}, &out, &out)
	if err == nil || !strings.Contains(out.String(), "run-05  ошибка: ") {
		t.Errorf("want per-run error and final error, got %v:\n%s", err, out.String())
	}
}

func TestAnalyzeUnknownID(t *testing.T) {
	failingClaude(t)
	var out bytes.Buffer
	err := runAnalyze([]string{"run-03", "run-99", "--runs", copyRuns(t)}, &out, &out)
	if err == nil || err.Error() != `прогон "run-99" не найден` || out.Len() != 0 {
		t.Errorf("err %v, out %q", err, out.String())
	}
}
