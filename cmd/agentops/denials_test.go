package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const recordedRuns = "../../runs"

// copyRuns copies the named recorded runs into a temp dir.
func pickRuns(t *testing.T, ids ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, id := range ids {
		if err := os.CopyFS(filepath.Join(dir, id), os.DirFS(filepath.Join(recordedRuns, id))); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func denialsOut(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runDenials(args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestDenialsRecordedRuns(t *testing.T) {
	var got denialsOutput
	var out bytes.Buffer
	if err := runDenials([]string{"--runs", recordedRuns, "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	strict(t, "denials", out.Bytes(), &got)
	if got.Denied != 19 || got.Steps != 138 || got.Runs != 17 {
		t.Errorf("totals: %+v", got)
	}
	calls := map[string]int{}
	for _, r := range got.Reasons {
		calls[r.Class] = r.Calls
	}
	if len(calls) != 4 || calls["compound"] != 9 || calls["variable"] != 4 || calls["disabled"] != 3 || calls["sandbox"] != 3 {
		t.Errorf("classes: %v", calls)
	}
	for _, g := range got.Groups {
		if g.Agent == "claude" && g.Scenario == "skill" && g.Denied != 0 {
			t.Errorf("claude skill group: %+v", g)
		}
	}
}

func TestDenialsHuman(t *testing.T) {
	dir := pickRuns(t, "run-01", "run-11")
	out := denialsOut(t, "--runs", dir)
	for _, want := range []string{"Отказы: ", "ПРИЧИНА", "ВЫЗОВОВ", "составная команда", "АГЕНТ", "claude  baseline", "claude  skill", "Подсказка агенту:\n- Do not call `go vet`"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "Отказы: 2 из 26 шагов (7%) в 1 из 2 прогонов\n") {
		t.Errorf("header:\n%s", out)
	}
	if !strings.Contains(out, "claude  skill            1     13        0          0") {
		t.Errorf("group row:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("trailing spaces: %q", l)
		}
	}
}

func TestDenialsFilters(t *testing.T) {
	dir := pickRuns(t, "run-01", "run-11", "run-15")
	out := denialsOut(t, "--runs", dir, "--agent", "codex")
	if !strings.Contains(out, "codex") || strings.Contains(out, "claude") {
		t.Errorf("--agent:\n%s", out)
	}
	out = denialsOut(t, "--runs", dir, "--scenario", "skill")
	if !strings.HasPrefix(out, "отказов нет\n") || !strings.Contains(out, "skill") || strings.Contains(out, "baseline") {
		t.Errorf("--scenario:\n%s", out)
	}
	if out := denialsOut(t, "--runs", dir, "--agent", "nobody"); out != "прогонов нет\n" {
		t.Errorf("unknown agent: %q", out)
	}
}

func TestDenialsHints(t *testing.T) {
	dir := pickRuns(t, "run-01", "run-15")
	out := denialsOut(t, "--runs", dir, "--hints")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.HasPrefix(out, "-") || !strings.Contains(lines[0], "read-only") || !strings.HasPrefix(lines[1], "Do not call `go vet`") {
		t.Errorf("hints:\n%s", out)
	}
	if out := denialsOut(t, "--runs", pickRuns(t, "run-11"), "--hints"); out != "" {
		t.Errorf("no hints must print nothing, got %q", out)
	}
	if out := denialsOut(t, "--runs", dir, "--hints", "--json"); !strings.HasPrefix(out, `{"hints":["The workspace`) {
		t.Errorf("hints json: %s", out)
	}
	if out := denialsOut(t, "--runs", pickRuns(t, "run-11"), "--hints", "--json"); out != `{"hints":[]}`+"\n" {
		t.Errorf("empty hints json: %s", out)
	}
}

func TestDenialsJSONEmpty(t *testing.T) {
	var got denialsOutput
	strict(t, "denials", []byte(denialsOut(t, "--runs", t.TempDir(), "--json")), &got)
	if got.Runs != 0 || got.Percent != 0 || got.Reasons == nil || got.Groups == nil {
		t.Errorf("empty: %+v", got)
	}
	if out := denialsOut(t, "--runs", t.TempDir()); out != "прогонов нет\n" {
		t.Errorf("empty human: %q", out)
	}
}
