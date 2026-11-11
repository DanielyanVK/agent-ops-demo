package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWhyHuman(t *testing.T) {
	runs04 := runningRuns(t)
	cases := []struct {
		dir, id, want string
	}{
		{fixtureRuns, "run-01", "run-01: прогон успешен, разбирать нечего\n"},
		{fixtureRuns, "run-03", "run-03: разбора ещё нет — выполни команду analyze\n"},
		{runs04, "run-04", "run-04: прогон ещё идёт\n"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := runWhy([]string{"--runs", c.dir, c.id}, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != c.want {
			t.Errorf("%s: got %q, want %q", c.id, out.String(), c.want)
		}
	}
}

func TestWhyPostmortem(t *testing.T) {
	var out bytes.Buffer
	if err := runWhy([]string{"--runs", fixtureRuns, "run-02"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "run-02 · ✗ провал\n\nСломалось на шаге 3: Edit ") ||
		!strings.Contains(got, "\nПричина: Агенту запрещено менять файлы") || !strings.Contains(got, "\nЧто поправить: Разрешить агенту Edit") {
		t.Errorf("got:\n%s", got)
	}
}

func TestWhyUnboundStep(t *testing.T) {
	dir := runningRuns(t)
	// Reuse the running dir: a verdict plus a postmortem not tied to a step.
	writeFile(t, dir+"/run-04/meta.json", `{"id":"run-04","verdict":{"passed":false,"test_output":"FAIL"}}`)
	writeFile(t, dir+"/run-04/postmortem.json", `{"failed_step":0,"cause":"c","fix":"f","source":"rule","cost_usd":0}`)
	var out bytes.Buffer
	if err := runWhy([]string{"--runs", dir, "run-04"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n\nСбой не привязан к одному шагу\nПричина: c\nЧто поправить: f\n") {
		t.Errorf("got:\n%s", out.String())
	}
}

func TestWhyUnknown(t *testing.T) {
	var out bytes.Buffer
	err := runWhy([]string{"--runs", fixtureRuns, "run-99"}, &out)
	if err == nil || err.Error() != `прогон "run-99" не найден` {
		t.Errorf("err %v", err)
	}
}

func TestWhyJSON(t *testing.T) {
	for id, wantPM := range map[string]bool{"run-02": true, "run-01": false} {
		var out bytes.Buffer
		if err := runWhy([]string{"--runs", fixtureRuns, "--json", id}, &out); err != nil {
			t.Fatal(err)
		}
		var v struct {
			ID         string          `json:"id"`
			Status     string          `json:"status"`
			Postmortem json.RawMessage `json:"postmortem"`
		}
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v.ID != id || (string(v.Postmortem) != "null") != wantPM {
			t.Errorf("%s: %+v", id, v)
		}
	}
}
