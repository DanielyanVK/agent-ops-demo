package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDispatch(t *testing.T) {
	saved := commands
	defer func() { commands = saved }()
	commands = []command{
		{name: "boom", run: func([]string) error { return errors.New("x") }},
		{name: "code", run: func([]string) error { return exitCode(3) }},
		{name: "ok", run: func([]string) error { return nil }},
	}
	var stderr bytes.Buffer
	cases := map[string]int{"nope": 2, "boom": 1, "code": 3, "ok": 0, "help": 0}
	for name, want := range cases {
		stderr.Reset()
		if got := dispatch([]string{name}, &stderr); got != want {
			t.Errorf("%s: exit %d, want %d", name, got, want)
		}
		if name == "code" && stderr.Len() != 0 {
			t.Errorf("exitCode must print nothing, got %q", stderr.String())
		}
	}
}

func TestRunsFlagEnv(t *testing.T) {
	t.Setenv("AGENTOPS_RUNS", "")
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	if got := *runsFlag(fs); got != "runs" {
		t.Fatalf("default %q", got)
	}
	t.Setenv("AGENTOPS_RUNS", "/data")
	fs = flag.NewFlagSet("x", flag.ContinueOnError)
	if got := *runsFlag(fs); got != "/data" {
		t.Fatalf("env %q", got)
	}
}

const fixtures = "../../internal/trace/testdata/runs"

func TestLsTable(t *testing.T) {
	var out, errb bytes.Buffer
	if err := runLs([]string{"--runs", fixtures}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "3 прогона · ✓ 1 · ✗ 2 · $0.18 · в среднем 21с" {
		t.Fatalf("summary: %q", lines[0])
	}
	if !strings.Contains(out.String(), "СТАТУС") || !strings.Contains(out.String(), "no-edit") {
		t.Fatalf("table: %s", out.String())
	}
	// Every row ends at the same column: the cost column fits "$0.0500".
	for _, l := range lines[2:6] {
		if utf8.RuneCountInString(l) != utf8.RuneCountInString(lines[2]) {
			t.Errorf("row %q misaligned", l)
		}
	}
}

func TestLsFailedKeepsSummary(t *testing.T) {
	var out bytes.Buffer
	if err := runLs([]string{"--runs", fixtures, "--failed"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "3 прогона") || strings.Contains(out.String(), "run-01") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestLsJSON(t *testing.T) {
	var out bytes.Buffer
	if err := runLs([]string{"--runs", fixtures, "--failed", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Stats struct {
			Total, Passed, Failed int
			PostmortemCostUSD     *float64 `json:"postmortem_cost_usd"`
		} `json:"stats"`
		Runs []struct {
			ID, Status string
			Postmortem *struct {
				FailedStep int    `json:"failed_step"`
				Cause      string `json:"cause"`
			} `json:"postmortem"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stats.Total != 3 || got.Stats.PostmortemCostUSD == nil || *got.Stats.PostmortemCostUSD != 0 || len(got.Runs) != 2 || got.Runs[0].ID != "run-02" || got.Runs[0].Status != "failed" ||
		got.Runs[0].Postmortem == nil || got.Runs[0].Postmortem.FailedStep != 3 || got.Runs[0].Postmortem.Cause == "" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(out.String(), `"id":"run-03"`) || !strings.Contains(out.String(), `"postmortem":null`) {
		t.Fatalf("a run without a postmortem must carry null: %s", out.String())
	}
	if strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
		t.Fatal("json must be one line")
	}
}

func TestLsEmptyAndWarnings(t *testing.T) {
	var out, errb bytes.Buffer
	if err := runLs([]string{"--runs", t.TempDir() + "/absent"}, &out, &errb); err != nil || out.String() != "прогонов нет\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
}

func TestBadFlagReportedOnce(t *testing.T) {
	saved := commands
	defer func() { commands = saved }()
	var out bytes.Buffer
	commands = []command{{name: "ls", run: func(args []string) error { return runLs(args, io.Discard, &out) }}}
	var stderr bytes.Buffer
	if code := dispatch([]string{"ls", "--bogus"}, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 || strings.Count(stderr.String(), "bogus") != 1 || !strings.HasPrefix(stderr.String(), "agentops: ") {
		t.Fatalf("flag output %q, stderr %q", out.String(), stderr.String())
	}
}

func TestParseFlags(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("demo", flag.ContinueOnError)
		fs.String("runs", "runs", "каталог с прогонами")
		fs.Int("step", 0, "шаг")
		fs.Bool("json", false, "json")
		return fs
	}
	var out bytes.Buffer
	_, helpErr := parseFlags(newFS(), []string{"-h"}, &out)
	if !errors.Is(helpErr, flag.ErrHelp) || !strings.HasPrefix(out.String(), "Использование: agentops demo [флаги]\n\n") ||
		!strings.Contains(out.String(), "-runs string") || !strings.Contains(out.String(), "каталог с прогонами (по умолчанию \"runs\")") ||
		strings.Contains(out.String(), "default") {
		t.Errorf("help: %v\n%s", helpErr, out.String())
	}

	fs := newFS()
	pos, err := parseFlags(fs, []string{"run-06", "--json", "--step", "2", "x", "--runs=d"}, &out)
	if err != nil || strings.Join(pos, " ") != "run-06 x" || fs.Lookup("json").Value.String() != "true" ||
		fs.Lookup("step").Value.String() != "2" || fs.Lookup("runs").Value.String() != "d" {
		t.Errorf("interleaved: %v %v", pos, err)
	}
	for args, want := range map[string]string{
		"--nope":      "неизвестный флаг: --nope",
		"-nope":       "неизвестный флаг: --nope",
		"--step":      "флагу --step нужно значение",
		"--step x":    `неверное значение "x" для флага --step`,
		"a --json=no": `неверное значение "no" для флага --json`,
		"---x":        "неверный флаг: ---x",
	} {
		if _, err := parseFlags(newFS(), strings.Fields(args), &out); err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", args, err, want)
		}
	}
	if got := runCommand(&command{run: func([]string) error { return helpErr }}, nil, io.Discard); got != 0 {
		t.Errorf("ErrHelp exit = %d", got)
	}

	out.Reset()
	if _, err := parseFlags(newFS(), []string{"-nope"}, &out); err == nil || out.Len() != 0 {
		t.Errorf("bad flag: %v, stdout %q", err, out.String())
	}
	var stderr bytes.Buffer
	c := command{run: func(a []string) error { _, err := parseFlags(newFS(), a, &out); return err }}
	if code := runCommand(&c, []string{"-nope"}, &stderr); code != 1 || strings.Count(stderr.String(), "agentops:") != 1 {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestStrayArgumentsRejected(t *testing.T) {
	runs := t.TempDir()
	for name, run := range map[string]func([]string) error{
		"ls":    func(a []string) error { return runLs(a, io.Discard, io.Discard) },
		"check": func(a []string) error { return runCheck(a, io.Discard, io.Discard) },
		"run":   func(a []string) error { return runRun(a, io.Discard) },
		"tui":   runTUI,
	} {
		err := run([]string{"--runs", runs, "one-turn"})
		if err == nil || err.Error() != "лишний аргумент: one-turn" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if entries, _ := os.ReadDir(runs); len(entries) != 0 {
		t.Errorf("a rejected run recorded something: %v", entries)
	}
}
