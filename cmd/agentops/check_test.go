package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckThreshold(t *testing.T) {
	// fixtures: 1 of 3 finished runs passed, 33%
	cases := []struct {
		min      string
		wantLine string
		wantCode exitCode
	}{
		{"30", "успешных 1 из 3 (33%) · порог 30% · в норме\n", 0},
		{"34", "успешных 1 из 3 (33%) · порог 34% · ниже порога\n", 1},
	}
	for _, c := range cases {
		var out bytes.Buffer
		err := runCheck([]string{"--runs", fixtureRuns, "--min", c.min}, &out, &out)
		var code exitCode
		if c.wantCode == 0 && err != nil || c.wantCode != 0 && !errors.As(err, &code) || code != c.wantCode {
			t.Errorf("min %s: err %v", c.min, err)
		}
		if out.String() != c.wantLine {
			t.Errorf("min %s: got %q, want %q", c.min, out.String(), c.wantLine)
		}
	}
}

func TestCheckNoFinished(t *testing.T) {
	var out bytes.Buffer
	err := runCheck([]string{"--runs", runningRuns(t)}, &out, &out)
	if err != exitCode(1) || out.String() != "завершённых прогонов нет\n" {
		t.Errorf("err %v, out %q", err, out.String())
	}
}

func TestCheckJSON(t *testing.T) {
	var out bytes.Buffer
	err := runCheck([]string{"--runs", fixtureRuns, "--json"}, &out, &out)
	if err != exitCode(1) {
		t.Errorf("exit code must not change with --json, got %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"finished": 3.0, "passed": 1.0, "percent": 33.0, "min": 80.0, "ok": false}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s: got %v, want %v", k, v[k], w)
		}
	}
}
