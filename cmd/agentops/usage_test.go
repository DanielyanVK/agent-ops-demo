package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// usageRuns holds a claude run and a codex run that used skills, MCP, subagents and the web.
func usageRuns(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for id, f := range map[string]string{"run-01": "claude-tools.jsonl", "run-02": "codex-tools.jsonl", "run-03": "codex-fix.jsonl"} {
		agent := "codex"
		if id == "run-01" {
			agent = "claude"
		}
		b, err := os.ReadFile("../../internal/trace/testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		os.Mkdir(filepath.Join(dir, id), 0o755)
		meta := `{"id":"` + id + `","task":"fix-tests","agent":"` + agent + `","model":"default","scenario":"skill","verdict":{"passed":true,"test_output":""}}`
		os.WriteFile(filepath.Join(dir, id, "meta.json"), []byte(meta), 0o644)
		os.WriteFile(filepath.Join(dir, id, "trace.jsonl"), b, 0o644)
	}
	return dir
}

func TestLsShowsAgentBeforeModel(t *testing.T) {
	var out bytes.Buffer
	if err := runLs([]string{"--runs", usageRuns(t)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if f := strings.Fields(lines[2]); f[3] != "АГЕНТ" || f[4] != "МОДЕЛЬ" {
		t.Errorf("header: %q", lines[2])
	}
	if f := strings.Fields(lines[4]); f[3] != "codex" || f[4] != "default" || f[len(f)-1] != "—" {
		t.Errorf("codex row has no cost: %q", lines[4])
	}
}

func TestShowHumanListsWhatTheRunUsed(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", usageRuns(t), "run-01"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"токены: 420 → 40", "навыки: go-test-fixer, other", "MCP: notion.search ×2, notion.fetch__page", "подагенты: 2", "веб: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if !regexp.MustCompile(`(?m)^ *1 ✓ Skill +go-test-fixer `).MatchString(got) {
		t.Errorf("a skill row shows the skill:\n%s", got)
	}

	out.Reset()
	if err := runShow([]string{"--runs", usageRuns(t), "run-03"}, &out); err != nil {
		t.Fatal(err)
	}
	got = out.String()
	if !strings.Contains(got, "токены: 86212 → 840") || strings.Contains(got, "навыки") || strings.Contains(got, "MCP") || strings.Contains(got, "веб") || strings.Contains(got, "подагенты") {
		t.Errorf("only the tokens are known for this run:\n%s", got)
	}
}

func TestShowJSONCarriesUsage(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", usageRuns(t), "run-02", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Agent  string
		Skills []string
		MCP    []struct {
			Tool string
			N    int
		}
		Subagents int
		WebCalls  int  `json:"web_calls"`
		TokensIn  *int `json:"tokens_in"`
		Steps     []struct{ Kind, Name string }
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Agent != "codex" || len(got.Skills) != 3 || len(got.MCP) != 2 || got.MCP[0].Tool != "notion.search" || got.MCP[0].N != 2 ||
		got.WebCalls != 1 || got.TokensIn == nil || got.Steps[0].Kind != "skill" || got.Steps[0].Name != "systematic-debugging" {
		t.Errorf("%+v\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), `"subagents":0`) || !strings.Contains(out.String(), `"mcp":[{"tool":"notion.search","n":2}`) {
		t.Errorf("zero counts must be present: %s", out.String())
	}
	// A step without a name has no name key.
	if strings.Count(out.String(), `"name"`) != 5 { // 2 skill steps + 3 mcp steps
		t.Errorf("name appears only for named steps: %s", out.String())
	}
}

func TestEmptyListsAreArraysNotNull(t *testing.T) {
	var out bytes.Buffer
	if err := runShow([]string{"--runs", fixtureRuns, "run-01", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"skills":[]`, `"mcp":[]`, `"kind":"read"`, `"agent":"claude"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s in %s", want, out.String())
		}
	}
	out.Reset()
	if err := runLs([]string{"--runs", fixtureRuns, "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"skills":[]`) || strings.Contains(out.String(), `"mcp"`) {
		t.Errorf("ls brief carries skills, not mcp: %s", out.String())
	}
}

func TestSkillStepIsLabelledSkillForBothAgents(t *testing.T) {
	runs := usageRuns(t)
	var out bytes.Buffer
	if err := runShow([]string{"--runs", runs, "run-02"}, &out); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^ *1 ✓ Skill +systematic-debugging`).MatchString(out.String()) || strings.Contains(out.String(), "Shell systematic") {
		t.Errorf("row:\n%s", out.String())
	}
	out.Reset()
	if err := runShow([]string{"--runs", runs, "run-02", "--step", "1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Шаг 1 · Skill") {
		t.Errorf("step title:\n%s", out.String())
	}
	out.Reset()
	if err := runShow([]string{"--runs", runs, "run-02", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"tool":"Shell","kind":"skill","name":"systematic-debugging"`) {
		t.Errorf("JSON tool must stay Shell: %s", out.String())
	}
}
