package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunUnknownScenario(t *testing.T) {
	var out bytes.Buffer
	err := runRun([]string{"--scenario", "nope", "--runs", t.TempDir()}, &out)
	if err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("want error listing valid names, got %v", err)
	}
}

func TestRunPrintsOneLinePerRun(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "fake")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\necho '{\"type\":\"result\",\"subtype\":\"success\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	var out bytes.Buffer
	args := []string{"--runs", filepath.Join(tmp, "runs"), "--testbed", "../../testbed", "--scenario", "no-edit"}
	if err := runRun(append(args, "--n", "2"), &out); err != nil {
		t.Fatal(err)
	}
	want := "run-01  ✗  no-edit  haiku\nrun-02  ✗  no-edit  haiku\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := runRun(append(args, "--json"), &out); err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"run-03","status":"failed","scenario":"no-edit","model":"haiku"}` + "\n"; out.String() != want {
		t.Errorf("got %q", out.String())
	}
}

func TestRunRejectsNonPositiveN(t *testing.T) {
	var out bytes.Buffer
	err := runRun([]string{"--n", "0", "--runs", t.TempDir()}, &out)
	if err == nil || err.Error() != "--n должно быть не меньше 1" {
		t.Fatalf("got %v", err)
	}
}

func TestRunInterruptExits130(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGHUP} {
		tmp := t.TempDir()
		ready := filepath.Join(tmp, "ready")
		bin := filepath.Join(tmp, "fake")
		if err := os.WriteFile(bin, []byte("#!/bin/bash\ntouch '"+ready+"'\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
		go func() {
			for range 500 {
				if _, err := os.Stat(ready); err == nil {
					syscall.Kill(os.Getpid(), sig)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		runs := filepath.Join(tmp, "runs")
		var out bytes.Buffer
		err := runRun([]string{"--runs", runs, "--testbed", "../../testbed"}, &out)
		if err != exitCode(130) || out.Len() != 0 {
			t.Fatalf("%v: err %v, out %q", sig, err, out.String())
		}
		if entries, _ := os.ReadDir(runs); len(entries) != 0 {
			t.Errorf("%v: interrupted run left behind: %v", sig, entries)
		}
	}
}

func TestRunMissingTestbed(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	err := runRun([]string{"--runs", runs, "--testbed", "nope"}, &bytes.Buffer{})
	if err == nil || err.Error() != "каталог подопытного проекта не найден: nope" {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(runs); err == nil {
		t.Error("runs directory created")
	}
}

func TestRunAgentFlag(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "fake-codex")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\necho '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CODEX_BIN", bin)
	t.Setenv("HOME", tmp)
	runs := filepath.Join(tmp, "runs")
	args := []string{"--runs", runs, "--testbed", "../../testbed", "--agent", "codex", "--scenario", "no-edit"}
	var out bytes.Buffer
	if err := runRun(args, &out); err != nil {
		t.Fatal(err)
	}
	if want := "run-01  ✗  no-edit  default\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
	meta, _ := os.ReadFile(filepath.Join(runs, "run-01", "meta.json"))
	if !strings.Contains(string(meta), `"agent": "codex"`) {
		t.Errorf("meta: %s", meta)
	}

	err := runRun(append(args[:6:6], "--scenario", "one-turn"), &out)
	if err == nil || err.Error() != "сценарий one-turn не поддерживается агентом codex" {
		t.Errorf("got %v", err)
	}
	if err := runRun([]string{"--runs", runs, "--agent", "gemini"}, &out); err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Errorf("got %v", err)
	}
}

func TestRunSkillScenarioNeedsPlugin(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	err := runRun([]string{"--runs", runs, "--testbed", "../../testbed", "--scenario", "skill", "--plugin", "nope"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "каталог плагина не найден") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(runs); err == nil {
		t.Error("runs directory created")
	}
}

func TestRunCodexSkillScenarioNeedsPlugin(t *testing.T) {
	err := runRun([]string{"--runs", filepath.Join(t.TempDir(), "runs"), "--testbed", "../../testbed", "--agent", "codex", "--scenario", "skill", "--plugin", "nope"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "каталог плагина не найден") {
		t.Fatalf("got %v", err)
	}
}
