package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const fixtureRuns = "../../internal/trace/testdata/runs"

// runningRuns builds a runs dir with one run that has no verdict yet.
func runningRuns(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "run-04"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"run-04","task":"fix-tests","agent":"claude","model":"haiku","scenario":"baseline","started_at":"2026-11-11T10:30:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "run-04", "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestShowHuman(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", fixtureRuns, "run-02"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "run-02 · fix-tests · claude · haiku · no-edit · ✗ провал\n") {
		t.Errorf("header: %q", got)
	}
	for _, want := range []string{"остановка: ", "заблокировано: Edit", "\n1 ✓ Read", "\n3 ⊘ Edit stats.go", "0.2с · запрещено ← разбор\n4 ✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestShowNoDenialsSegmentWhenNone(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", fixtureRuns, "run-01"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "заблокировано") || !strings.Contains(out.String(), "✓ успех") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestShowRunning(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", runningRuns(t), "run-04"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "… идёт") {
		t.Errorf("got %q", out.String())
	}
}

func TestShowStep(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", fixtureRuns, "--step", "3", "run-02"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Шаг 3 · Edit", "\n\nВХОД\n", "\n\nВЫХОД\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestShowErrors(t *testing.T) {
	cases := map[string][]string{
		`прогон "run-99" не найден`: {"run-99"},
		"шага 99 нет":               {"--step", "99", "run-02"},
		"ровно один id":             {},
	}
	for want, args := range cases {
		var out bytes.Buffer
		err := runShow(append([]string{"--runs", fixtureRuns}, args...), &out)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err %v, want %q", args, err, want)
		}
	}
}

func TestShowJSON(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"run-02", "--runs", fixtureRuns, "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID         string                 `json:"id"`
		Status     string                 `json:"status"`
		Denials    []string               `json:"denials"`
		Verdict    *struct{ Passed bool } `json:"verdict"`
		Postmortem *struct {
			FailedStep int `json:"failed_step"`
		} `json:"postmortem"`
		Steps []map[string]any `json:"steps"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	if v.ID != "run-02" || v.Status != "failed" || v.Verdict == nil || v.Verdict.Passed ||
		v.Postmortem == nil || v.Postmortem.FailedStep != 3 || len(v.Steps) == 0 || len(v.Denials) == 0 {
		t.Errorf("unexpected: %+v", v)
	}
	if _, bulky := v.Steps[0]["input"]; bulky {
		t.Error("step list must not carry input")
	}

	out.Reset()
	if err := runShow([]string{"--runs", runningRuns(t), "--json", "run-04"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"verdict":null`) || !strings.Contains(out.String(), `"postmortem":null`) ||
		!strings.Contains(out.String(), `"denials":[]`) {
		t.Errorf("running: %q", out.String())
	}
}

func TestShowStepJSON(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", fixtureRuns, "--json", "--step", "3", "run-02"}, &out); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID   string `json:"id"`
		Step struct {
			N      int    `json:"n"`
			Tool   string `json:"tool"`
			Input  string `json:"input"`
			Output string `json:"output"`
		} `json:"step"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != "run-02" || v.Step.N != 3 || v.Step.Tool != "Edit" || v.Step.Input == "" {
		t.Errorf("unexpected: %+v", v)
	}
}

func TestShowRowsAligned(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "run-05"), 0o755)
	meta := `{"id":"run-05","task":"t","agent":"claude","model":"haiku","scenario":"baseline","started_at":"2026-11-11T10:30:00Z"}`
	os.WriteFile(filepath.Join(dir, "run-05", "meta.json"), []byte(meta), 0o644)
	var tr []string
	for i, u := range []struct{ name, input string }{
		{"Read", `{"file_path":"` + strings.Repeat("я", 60) + `"}`},
		{"Task", `{}`}, // empty summary
		{"Bash", `{"command":"ls"}`}, {"Bash", `{}`}, {"Bash", `{}`}, {"Bash", `{}`}, {"Bash", `{}`},
		{"Bash", `{}`}, {"Bash", `{}`}, {"Bash", `{}`},
	} {
		tr = append(tr, fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t%d","name":%q,"input":%s}]}}`, i, u.name, u.input))
	}
	os.WriteFile(filepath.Join(dir, "run-05", "trace.jsonl"), []byte(strings.Join(tr, "\n")+"\n"), 0o644)

	var out bytes.Buffer
	if err := runShow([]string{"--runs", dir, "run-05"}, &out); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")[3:]
	if len(rows) != 10 || !strings.HasPrefix(rows[0], " 1 ✓ Read") || !strings.HasPrefix(rows[9], "10 ✓") {
		t.Fatalf("rows:\n%s", out.String())
	}
	for _, r := range rows {
		if n := utf8.RuneCountInString(r); n != 2+1+1+1+4+1+60+1+6 /* num, glyph, tool, summary, duration */ {
			t.Errorf("row width %d: %q", n, r)
		}
	}
}

func TestShowStepCleansAgentText(t *testing.T) {
	dir := runningRuns(t)
	tr := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo \u001b]0;pwned\u0007hi"}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"\u001b[2Jok\r\tdone"}]}}` + "\n"
	writeFile(t, filepath.Join(dir, "run-04", "trace.jsonl"), tr)
	var out bytes.Buffer
	if err := runShow([]string{"run-04", "--step", "1", "--runs", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\a\r\t") || !strings.Contains(out.String(), "ВЫХОД\nok    done\n") {
		t.Errorf("got %q", out.String())
	}
}

func TestShowAndWhyCleanAgentText(t *testing.T) {
	dir := runningRuns(t)
	writeFile(t, filepath.Join(dir, "run-04", "meta.json"), `{"id":"run-04","verdict":{"passed":false,"test_output":"FAIL"}}`)
	writeFile(t, filepath.Join(dir, "run-04", "trace.jsonl"),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo \u001b]0;pwned\u0007hi\tthere"}}]}}`+"\n")
	writeFile(t, filepath.Join(dir, "run-04", "postmortem.json"),
		`{"failed_step":1,"cause":"\u001b[2Jcleared","fix":"f\u0007","source":"model","cost_usd":0.01}`)
	var show, why bytes.Buffer
	if err := runShow([]string{"run-04", "--runs", dir}, &show); err != nil {
		t.Fatal(err)
	}
	if err := runWhy([]string{"run-04", "--runs", dir}, &why); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"show": show.String(), "why": why.String()} {
		if strings.ContainsAny(out, "\x1b\a\t") || strings.Contains(out, "pwned") {
			t.Errorf("%s: %q", name, out)
		}
	}
	if !strings.Contains(show.String(), "\n1 ✓ Bash echo hi    there") || !strings.Contains(why.String(), "Причина: cleared\n") {
		t.Errorf("visible text lost:\n%s\n%s", show.String(), why.String())
	}
}
