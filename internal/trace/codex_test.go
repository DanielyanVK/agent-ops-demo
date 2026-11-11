package trace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func parseCodexFile(t *testing.T, name string) Run {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	run, err := ParseCodex(f)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestParseCodexFix(t *testing.T) {
	r := parseCodexFile(t, "codex-fix.jsonl")
	if len(r.Steps) != 5 || r.Turns != 1 || r.StopReason != "success" || r.ResultError || r.CostUSD != 0 ||
		r.TokensIn != 86212 || r.TokensOut != 840 || !strings.HasPrefix(r.Result, "Fixed the off-by-one error") {
		t.Fatalf("unexpected run: %+v", r)
	}
	wantKinds := []string{"shell", "shell", "shell", "edit", "shell"}
	for i, s := range r.Steps {
		if s.N != i+1 || s.Kind != wantKinds[i] {
			t.Errorf("step %d: %+v", i+1, s)
		}
	}
	// The wrapper and one pair of outer quotes are gone; the first line is the step line.
	if got := r.Steps[1].Line; got != "git status --short; rg --files | sed -n '1,200p'" {
		t.Errorf("line: %q", got)
	}
	if got := r.Steps[4].Line; got != "gofmt -w calc.go && go test ./... && go vet ./..." {
		t.Errorf("single-quoted wrapper: %q", got)
	}
	if !strings.HasPrefix(r.Steps[1].Input, `/bin/zsh -lc "git status`) {
		t.Errorf("input must be the raw command: %q", r.Steps[1].Input)
	}
	if r.Steps[0].Output != "/private/var/folders/zz/T/agentops-work\n" || !r.Steps[0].IsError || r.Steps[1].IsError {
		t.Errorf("exit codes: %+v", r.Steps[0])
	}
	if s := r.Steps[3]; s.Tool != "Edit" || s.Line != "/private/var/folders/zz/T/agentops-work/calc.go" || s.IsError {
		t.Errorf("edit step: %+v", s)
	}
	if len([]rune(r.Steps[0].Summary)) > 60 || !strings.HasSuffix(r.Steps[0].Summary, "…") {
		t.Errorf("summary: %q", r.Steps[0].Summary)
	}
	if len(r.Denials) != 0 || r.Skills != nil || r.MCP != nil {
		t.Errorf("nothing denied or used: %+v", r)
	}
	if r.Duration != 0 || r.Steps[0].Duration != 0 {
		t.Errorf("a trace without ts has no durations: %v %v", r.Duration, r.Steps[0].Duration)
	}
}

func TestParseCodexReadOnlyIsDenied(t *testing.T) {
	r := parseCodexFile(t, "codex-readonly.jsonl")
	if len(r.Steps) != 2 || r.TokensIn != 53876 || r.TokensOut != 681 || r.StopReason != "success" {
		t.Fatalf("unexpected run: %+v", r)
	}
	if !r.Steps[0].IsError || !r.Steps[0].Denied || r.Steps[1].Denied {
		t.Errorf("steps: %+v", r.Steps)
	}
	if !reflect.DeepEqual(r.Denials, []string{"Shell"}) {
		t.Errorf("denials: %v", r.Denials)
	}
	if !strings.HasPrefix(r.Result, "Found the bug") {
		t.Errorf("result is the last message: %q", r.Result)
	}
}

func TestParseCodexWarningIsNotAStep(t *testing.T) {
	r := parseCodexFile(t, "codex-warning.jsonl")
	if len(r.Steps) != 4 || r.Steps[0].Line != "pwd && rg --files -g '\"'!*vendor*'\"' | sed -n '1,160p' && go test ./..." {
		t.Fatalf("steps: %+v", r.Steps)
	}
	if !r.Steps[1].IsError || r.Steps[1].Denied { // exit 128 is a failure, not a sandbox refusal
		t.Errorf("step 2: %+v", r.Steps[1])
	}
	if r.TokensIn != 84189 || r.TokensOut != 838 {
		t.Errorf("tokens %d %d", r.TokensIn, r.TokensOut)
	}
}

func TestParseCodexToolsAndFailedTurn(t *testing.T) {
	r := parseCodexFile(t, "codex-tools.jsonl")
	kinds := make([]string, len(r.Steps))
	for i, s := range r.Steps {
		kinds[i] = s.Kind
	}
	if want := []string{"skill", "skill", "shell", "mcp", "mcp", "mcp", "web", "edit"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	if s := r.Steps[0]; s.Name != "systematic-debugging" || s.Tool != "Shell" ||
		s.Line != "sed -n '1,200p' /Users/tester/.codex/plugins/cache/x/superpowers/7.0.1/skills/systematic-debugging/SKILL.md" ||
		s.Duration != 400*time.Millisecond {
		t.Errorf("skill step: %+v", s)
	}
	if s := r.Steps[1]; s.Name != "go-test-fixer, other" || s.Line != "cat ~/.codex/skills/go-test-fixer/SKILL.md ~/.codex/skills/go-test-fixer/SKILL.md ~/.codex/skills/other/SKILL.md" {
		t.Errorf("two skills in one command: %+v", s)
	}
	if s := r.Steps[3]; s.Tool != "MCP" || s.Name != "notion.search" || s.Line != "notion.search" || s.Duration != time.Second || s.IsError {
		t.Errorf("mcp step: %+v", s)
	}
	if !r.Steps[4].IsError || r.Steps[4].Denied {
		t.Errorf("failed mcp call: %+v", r.Steps[4])
	}
	if s := r.Steps[6]; s.Tool != "WebSearch" || s.Line != "go slices.Sort" {
		t.Errorf("web step: %+v", s)
	}
	if s := r.Steps[7]; s.Tool != "Edit" || s.Line != "calc.go, util/x.go" || !s.IsError {
		t.Errorf("edit step: %+v", s)
	}
	if !reflect.DeepEqual(r.Skills, []string{"systematic-debugging", "go-test-fixer", "other"}) ||
		!reflect.DeepEqual(r.MCP, []ToolCount{{"notion.search", 2}, {"notion.fetch", 1}}) || r.WebCalls != 1 || r.Subagents != 0 {
		t.Errorf("used: skills %v mcp %v web %d", r.Skills, r.MCP, r.WebCalls)
	}
	if r.StopReason != "error_during_execution" || !r.ResultError || r.Result != "stream disconnected" || r.Turns != 0 {
		t.Errorf("failed turn: %+v", r)
	}
	if r.Duration != 6*time.Second {
		t.Errorf("duration %v", r.Duration)
	}
}

func TestParseCodexIncompleteAndTimeout(t *testing.T) {
	r, err := ParseCodex(strings.NewReader(`{"type":"turn.started"}` + "\n" + `{"type":"item.comp`))
	if err != nil || r.StopReason != "incomplete" || len(r.Steps) != 0 {
		t.Errorf("cut-off trace: %+v, %v", r, err)
	}
	r, err = ParseCodex(strings.NewReader(`{"type":"turn.started","ts":"2026-11-01T10:00:00.000Z"}` + "\n" + `{"type":"timeout","ts":"2026-11-01T10:10:00.000Z"}` + "\n"))
	if err != nil || r.StopReason != "timeout" || !r.ResultError || r.Duration != 10*time.Minute {
		t.Errorf("timeout: %+v, %v", r, err)
	}
	if _, err := ParseCodex(strings.NewReader("garbage\n" + `{"type":"turn.started"}` + "\n")); err == nil {
		t.Error("garbage before the last line must be an error")
	}
}

func TestParseCodexTokensAreOfTheLastTurn(t *testing.T) {
	r, _ := ParseCodex(strings.NewReader(`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":30,"output_tokens":4}}` + "\n"))
	if r.Turns != 2 || r.TokensIn != 30 || r.TokensOut != 4 {
		t.Errorf("%+v", r)
	}
}

func TestLoadRunPicksParserByAgent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	codex, _ := os.ReadFile("testdata/codex-fix.jsonl")
	write("trace.jsonl", string(codex))
	write("meta.json", `{"id":"run-01","agent":"codex","model":"default","scenario":"baseline"}`)
	r, err := LoadRun(dir)
	if err != nil || r.Agent != "codex" || r.Model != "default" || len(r.Steps) != 5 || r.TokensIn != 86212 {
		t.Fatalf("codex: %+v, %v", r, err)
	}
	write("meta.json", `{"id":"run-01","agent":"claude","model":"haiku"}`)
	if r, err = LoadRun(dir); err != nil || len(r.Steps) != 0 || r.StopReason != "incomplete" {
		t.Fatalf("a codex trace read as claude has no steps: %+v, %v", r, err)
	}
}
