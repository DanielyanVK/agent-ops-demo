package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parseFile(t *testing.T, path string) Run {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	run, err := ParseClaude(f)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestParseRun01(t *testing.T) {
	r := parseFile(t, "testdata/runs/run-01/trace.jsonl")
	if r.Model != "claude-haiku-5-5" || len(r.Steps) != 4 || r.Turns != 5 || r.CostUSD != 0.05 ||
		r.Duration != 12*time.Second || r.StopReason != "success" || r.Result != "Fixed the bug." {
		t.Fatalf("unexpected run: %+v", r)
	}
	s := r.Steps[0]
	if s.N != 1 || s.Tool != "Read" || s.Summary != "stats.go" || s.Output != "package shop" || s.Duration != 200*time.Millisecond {
		t.Fatalf("step 1: %+v", s)
	}
	if r.Steps[1].Summary != "go test ./..." || r.Steps[1].Duration != 2500*time.Millisecond {
		t.Fatalf("step 2: %+v", r.Steps[1])
	}
	if !strings.Contains(s.Input, "\n  \"file_path\"") {
		t.Fatalf("input not indented: %q", s.Input)
	}
}

func TestParseRun02(t *testing.T) {
	r := parseFile(t, "testdata/runs/run-02/trace.jsonl")
	if len(r.Steps) != 4 || !r.Steps[2].IsError || r.Steps[2].Tool != "Edit" {
		t.Fatalf("steps: %+v", r.Steps)
	}
	if !r.Steps[2].Denied || len(r.Denials) != 1 || r.Denials[0] != "Edit" {
		t.Fatalf("denials: %v, step 3: %+v", r.Denials, r.Steps[2])
	}
}

func TestParseRun03(t *testing.T) {
	r := parseFile(t, "testdata/runs/run-03/trace.jsonl")
	if len(r.Steps) != 2 || r.StopReason != "error_max_turns" {
		t.Fatalf("run: %+v", r)
	}
	if r.Steps[0].Output != "stats.go:3\nstats.go:9" || r.Steps[0].Summary != "func Mean" || r.Steps[0].Duration != 0 {
		t.Fatalf("step 1: %+v", r.Steps[0])
	}
	if r.Steps[1].N != 2 || r.Steps[1].Output != "" {
		t.Fatalf("step 2: %+v", r.Steps[1])
	}
}

func TestParseRealSample(t *testing.T) {
	r := parseFile(t, "testdata/claude-auth-failed.jsonl")
	if len(r.Steps) != 0 || !r.ResultError || r.Model != "claude-haiku-5-5" {
		t.Fatalf("run: %+v", r)
	}
}

func TestParseMalformedLine(t *testing.T) {
	_, err := ParseClaude(strings.NewReader("{\"type\":\"system\"}\n\n{oops\n"))
	if err == nil || !strings.Contains(err.Error(), "строка 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseNoResultLine(t *testing.T) {
	r, err := ParseClaude(strings.NewReader(`{"type":"system","subtype":"init","model":"m"}`))
	if err != nil || r.StopReason != "incomplete" {
		t.Fatalf("run %+v err %v", r, err)
	}
}

func TestParseLongLine(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	in := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Bash","input":{"command":"c"}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"` + big + `"}]}}`
	r, err := ParseClaude(strings.NewReader(in))
	if err != nil || len(r.Steps[0].Output) != len(big) {
		t.Fatalf("err %v", err)
	}
}

func TestSummaryTruncation(t *testing.T) {
	got := summarize("Bash", []byte(`{"command":"`+strings.Repeat("я", 70)+`"}`))
	if r := []rune(got); len(r) != 60 || r[59] != '…' {
		t.Fatalf("got %q", got)
	}
	if summarize("WebFetch", []byte(`{"url":"u"}`)) != "" {
		t.Fatal("unknown tool must have empty summary")
	}
}

func TestLoadDir(t *testing.T) {
	runs, warns, err := LoadDir("testdata/runs")
	if err != nil || len(warns) != 0 || len(runs) != 3 {
		t.Fatalf("runs %d warns %v err %v", len(runs), warns, err)
	}
	if runs[0].ID != "run-01" || runs[0].Status() != Passed || runs[1].Status() != Failed {
		t.Fatalf("statuses: %v %v", runs[0].Status(), runs[1].Status())
	}
	if runs[1].Postmortem == nil || runs[1].Postmortem.Source != "rule" || runs[2].Postmortem != nil {
		t.Fatalf("postmortems wrong")
	}
	if runs[0].Model != "haiku" || runs[0].Scenario != "baseline" || len(runs[0].Steps) != 4 ||
		!strings.HasPrefix(runs[0].Prompt, "The tests in this Go module fail.") || runs[0].Stderr != "" {
		t.Fatalf("run-01: %+v", runs[0])
	}
}

func TestLoadDirBrokenAndMissing(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bad"), 0o755)
	os.WriteFile(filepath.Join(dir, "bad", "meta.json"), []byte("{"), 0o644)
	os.MkdirAll(filepath.Join(dir, "new"), 0o755) // started, no trace and no verdict yet
	os.WriteFile(filepath.Join(dir, "new", "meta.json"), []byte(`{"id":"new"}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "nometa"), 0o755)

	runs, warns, err := LoadDir(dir)
	if err != nil || len(runs) != 1 || runs[0].Status() != Running || len(warns) != 1 || !strings.HasPrefix(warns[0], "bad:") {
		t.Fatalf("runs %v warns %v err %v", runs, warns, err)
	}
	if runs, warns, err = LoadDir(filepath.Join(dir, "absent")); err != nil || runs != nil || warns != nil {
		t.Fatalf("missing dir: %v %v %v", runs, warns, err)
	}
}

func TestSummarize(t *testing.T) {
	runs := []Run{
		{Verdict: &Verdict{Passed: true}, Duration: 10 * time.Second, CostUSD: 0.1},
		{Verdict: &Verdict{}, Duration: 20 * time.Second, CostUSD: 0.2, Postmortem: &Postmortem{CostUSD: 0.01}},
		{Duration: 99 * time.Second, CostUSD: 0.3},
	}
	s := Summarize(runs)
	if s.Total != 3 || s.Passed != 1 || s.Failed != 1 || s.Running != 1 || s.AvgDuration != 15*time.Second {
		t.Fatalf("%+v", s)
	}
	if j := s.JSON(); j.AvgDurationMS != 15000 || j.Total != 3 || j.PostmortemCostUSD != 0.01 || j.CostUSD != s.CostUSD {
		t.Fatalf("%+v", j)
	}
	if (Summarize(nil) != Stats{}) {
		t.Fatal("empty stats must be zero")
	}
}

func TestFormat(t *testing.T) {
	d := map[time.Duration]string{
		0: "—", 200 * time.Millisecond: "0.2с", 9900 * time.Millisecond: "9.9с", 10 * time.Second: "10с",
		41 * time.Second: "41с", 59900 * time.Millisecond: "59с", time.Minute: "1м 00с", 65 * time.Second: "1м 05с",
	}
	for in, want := range d {
		if got := FmtDuration(in); got != want {
			t.Errorf("FmtDuration(%v) = %q, want %q", in, got, want)
		}
	}
	if FmtCost(0) != "—" || FmtCost(0.14) != "$0.14" || FmtCost(0.0034) != "$0.0034" || FmtCost(0.0712) != "$0.0712" || FmtCost(0.1) != "$0.10" || FmtCost(0.0999) != "$0.0999" {
		t.Error("FmtCost")
	}
	if Passed.Glyph() != "✓" || Failed.Glyph() != "✗" || Running.Glyph() != "…" {
		t.Error("Glyph")
	}
}

func TestBrief(t *testing.T) {
	r := Run{ID: "r", Steps: []Step{{N: 1, Tool: "Bash", IsError: true, Duration: time.Second}}, Duration: 2 * time.Second, Postmortem: &Postmortem{}, Verdict: &Verdict{}}
	b := r.Brief()
	if b.Status != "failed" || b.Steps != 1 || b.DurationMS != 2000 || b.Postmortem != r.Postmortem {
		t.Fatalf("%+v", b)
	}
	if sb := r.Steps[0].Brief(); !sb.Error || sb.DurationMS != 1000 {
		t.Fatalf("%+v", sb)
	}
}

func TestParsePartialLastLine(t *testing.T) {
	good := `{"type":"system","subtype":"init","model":"m"}` + "\n"
	r, err := ParseClaude(strings.NewReader(good + `{"type":"assis`))
	if err != nil || r.Model != "m" || r.StopReason != "incomplete" {
		t.Fatalf("run %+v err %v", r, err)
	}
	// Terminated or non-last malformed lines are real corruption.
	for _, in := range []string{good + "{oops\n", "{oops\n" + good} {
		if _, err := ParseClaude(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), "строка") {
			t.Fatalf("input %q: err = %v", in, err)
		}
	}
}

func TestPlural(t *testing.T) {
	want := map[int]string{0: "прогонов", 1: "прогон", 2: "прогона", 5: "прогонов", 11: "прогонов", 12: "прогонов",
		21: "прогон", 101: "прогон", 112: "прогонов", 24: "прогона"}
	for n, w := range want {
		if got := Plural(n, "прогон", "прогона", "прогонов"); got != w {
			t.Errorf("Plural(%d) = %q, want %q", n, got, w)
		}
	}
}

func TestBlockedAndFmtBlocked(t *testing.T) {
	r := Run{Steps: []Step{
		{Tool: "Edit", Denied: true}, {Tool: "Read"}, {Tool: "Bash", Denied: true},
		{Tool: "Edit", Denied: true}, {Tool: "Edit", Denied: true},
	}}
	b := r.Blocked()
	if got := FmtBlocked(b); got != "Edit ×3, Bash" {
		t.Errorf("FmtBlocked = %q", got)
	}
	if FmtBlocked(nil) != "" || len((Run{}).Blocked()) != 0 {
		t.Error("empty")
	}
}

func TestDeniedByIDAndByDisabledTool(t *testing.T) {
	trace := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Bash","input":{"command":"ls"}},{"type":"tool_use","id":"b","name":"Edit","input":{}},{"type":"tool_use","id":"c","name":"Read","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"no","is_error":true},{"type":"tool_result","tool_use_id":"b","content":"<tool_use_error>No such tool available: Edit. Edit is disabled for this session.</tool_use_error>","is_error":true},{"type":"tool_result","tool_use_id":"c","content":"No such tool available","is_error":false}]}}`,
		`{"type":"result","subtype":"success","permission_denials":[{"tool_name":"Bash","tool_use_id":"a"}]}`,
	}, "\n")
	r, err := ParseClaude(strings.NewReader(trace))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Steps[0].Denied || !r.Steps[1].Denied || r.Steps[2].Denied {
		t.Fatalf("denied flags: %+v", r.Steps)
	}
	if len(r.Denials) != 2 || r.Denials[0] != "Bash" || r.Denials[1] != "Edit" {
		t.Fatalf("denials: %v", r.Denials)
	}
	if !r.Steps[0].Brief().Denied {
		t.Error("StepBrief.Denied")
	}
}

func TestDisabledToolMarkedWithoutResultLine(t *testing.T) {
	in := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b","name":"Edit","input":{}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"No such tool available: Edit","is_error":true}]}}` + "\n"
	r, err := ParseClaude(strings.NewReader(in))
	if err != nil || r.StopReason != "incomplete" || !r.Steps[0].Denied || len(r.Denials) != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestLoadDirSortsNumerically(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"run-100", "notes", "run-11", "run-x", "run-09", "a-run"} {
		os.MkdirAll(filepath.Join(dir, id), 0o755)
		os.WriteFile(filepath.Join(dir, id, "meta.json"), []byte(`{"id":"`+id+`"}`), 0o644)
	}
	runs, _, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.ID)
	}
	if strings.Join(got, " ") != "run-09 run-11 run-100 a-run notes run-x" {
		t.Fatalf("order %v", got)
	}
}

func TestWriteJSONReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")
	os.WriteFile(path, []byte("old"), 0o600)
	if err := WriteJSON(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	entries, _ := os.ReadDir(dir)
	if string(b) != "{\n  \"a\": 1\n}\n" || fi.Mode().Perm() != 0o644 || len(entries) != 1 {
		t.Fatalf("content %q, mode %v, %d entries", b, fi.Mode(), len(entries))
	}
	if err := WriteJSON(filepath.Join(dir, "missing", "x.json"), 1); err == nil {
		t.Fatal("want error for a missing directory")
	}
}

func TestClean(t *testing.T) {
	in := "a\tb\r\n\x1b[2Jcleared \x1b]0;title\x07end \x1b]8;;http://x\x1b\\link \x1b(Bok\a\b\u009b31mЖ\x7f\x1b"
	if got, want := Clean(in), "a    b\ncleared end link ok31mЖ"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCompareIDsIsTotal(t *testing.T) {
	ids := []string{"run-2", "run-02", "run-10", "x", "run-", "run-1a", "a", "run-9999999999999999999999"}
	for _, a := range ids {
		for _, b := range ids {
			if compareIDs(a, b) != -compareIDs(b, a) || (a == b) != (compareIDs(a, b) == 0) {
				t.Errorf("%s vs %s not antisymmetric", a, b)
			}
			for _, c := range ids {
				if compareIDs(a, b) < 0 && compareIDs(b, c) < 0 && compareIDs(a, c) >= 0 {
					t.Errorf("%s < %s < %s not transitive", a, b, c)
				}
			}
		}
	}
}
