package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"agentops/internal/trace"
)

const testbedDir = "../../testbed"

// fakeAgent is a shell script standing in for claude. MODE selects what it does
// to the workdir: fix (copy the fixed sources in), drop (copy the files of
// $DROP in), tamper (rewrite a test file instead of fixing), hang (fix, then
// start a child and wait, writing both pids to $PIDS), linger (hang after
// printing a result line), escape (start a grandchild in its own process
// group that keeps stdout open, then hang), none.
const fakeAgent = `#!/bin/bash
case "$MODE" in
  fix) cp "$FIXED"/*.go . ;;
  drop) cp -R "$DROP"/. . ;;
  hang) cp "$FIXED"/*.go .; sleep 30 & echo "$$ $!" > "$PIDS"; wait ;;
  escape) perl -e 'setpgrp(0, 0); sleep 30' & echo "$!" > "$PIDS"; sleep 30 ;;
  linger) cp "$FIXED"/*.go .
          printf '{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.02,"num_turns":3}\n'
          sleep 30 & echo "$$ $!" > "$PIDS"; wait ;;
  tamper) rm -f ./*_test.go
          printf 'package shop\nimport "testing"\nfunc TestNothing(t *testing.T) {}\n' > stats_test.go ;;
  extra) printf 'package shop\nimport ("os"; "testing")\nfunc TestMain(m *testing.M) { os.Exit(0) }\n' > zz_test.go ;;
esac
echo "$@" > "$ARGS_OUT"
echo "TZ=$TZ" >> "$ARGS_OUT"
printf '{"type":"system","subtype":"init","session_id":"s1","model":"claude-haiku-5-5","permissionMode":"acceptEdits","claude_code_version":"1","tools":["Read"],"cwd":"%s","plugins":["corp-plugin"],"home":"%s"}\n' "$PWD" "$HOME"
printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"%s/stats.go"}}]}}\n' "$(pwd -P)"
printf '{"type":"result","subtype":"success","is_error":false,"result":"see %s/x and %s/y by %s (%s) on %s","num_turns":1}\n' "$PWD" "$HOME" "$(id -un)" "$FULLNAME" "$(hostname)"
echo "warning from $PWD" >&2
[ "$MODE" = crash ] && { printf '%0600d\nfatal in %s\n' 0 "$PWD" >&2; exit 3; }
exit 0
`

type env struct {
	runs, home string
	args       string
}

func setup(t *testing.T, mode string) env {
	t.Helper()
	tmp := t.TempDir()
	e := env{runs: filepath.Join(tmp, "runs"), home: filepath.Join(tmp, "home", "someuser"), args: filepath.Join(tmp, "args.txt")}
	// The temp directory is scrubbed before home, so the fake home must not lie inside it.
	sysTmp := filepath.Join(tmp, "tmp")
	for _, d := range []string{e.home, sysTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", sysTmp)
	bin := filepath.Join(tmp, "fake-claude")
	if err := os.WriteFile(bin, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	fixed := filepath.Join(tmp, "fixed")
	copyOrDie(t, fixed)
	patch(t, filepath.Join(fixed, "stats.go"), "float64(sum / len(xs))", "float64(sum) / float64(len(xs))")
	patch(t, filepath.Join(fixed, "cart.go"), "total + total*", "total - total*")
	patch(t, filepath.Join(fixed, "text.go"), "len(s) <= n", "len([]rune(s)) <= n")
	patch(t, filepath.Join(fixed, "text.go"), "s[:n-1] + ", "string([]rune(s)[:n-1]) + ")

	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	// The fake agents are steered through the environment, which agents otherwise do not inherit.
	old := agentEnvAllow
	agentEnvAllow = append(slices.Clone(old), "MODE", "FIXED", "ARGS_OUT", "FULLNAME", "PIDS", "DROP", "SNAP")
	t.Cleanup(func() { agentEnvAllow = old })
	// A fake HOME would otherwise give every go test a cold build cache.
	cache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCACHE", strings.TrimSpace(string(cache)))
	t.Setenv("HOME", e.home)
	t.Setenv("MODE", mode)
	t.Setenv("FIXED", fixed)
	t.Setenv("ARGS_OUT", e.args)
	return e
}

func copyOrDie(t *testing.T, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(testbedDir, dst); err != nil {
		t.Fatal(err)
	}
	// fixed/ holds sources only; the originals stay authoritative for tests.
	tests, _ := filepath.Glob(filepath.Join(dst, "*_test.go"))
	for _, f := range tests {
		os.Remove(f)
	}
}

func patch(t *testing.T, file, from, to string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), from) {
		t.Fatalf("%s: %q not found", file, from)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(b), from, to, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func doRun(t *testing.T, e env, sc string) (string, error) {
	t.Helper()
	s, _ := FindScenario(sc)
	return Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: testbedDir, Model: "haiku", Scenario: s})
}

func load(t *testing.T, e env, id string) trace.Run {
	t.Helper()
	r, err := trace.LoadRun(filepath.Join(e.runs, id))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestVerdictPassedWhenAgentFixesCode(t *testing.T) {
	e := setup(t, "fix")
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	r := load(t, e, id)
	if r.Status() != trace.Passed {
		t.Fatalf("status %v, verdict %+v", r.Status(), r.Verdict)
	}
}

func TestVerdictFailedWhenAgentDoesNothing(t *testing.T) {
	e := setup(t, "none")
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	r := load(t, e, id)
	if r.Status() != trace.Failed {
		t.Fatalf("status %v", r.Status())
	}
	if !strings.Contains(r.Verdict.TestOutput, "--- FAIL: TestTotal (0.00s)\n    cart_test.go:11:") {
		t.Errorf("a failure must be followed by its message: %q", r.Verdict.TestOutput)
	}
}

func TestVerdictFailedWhenAgentRewritesTests(t *testing.T) {
	e := setup(t, "tamper")
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if r := load(t, e, id); r.Status() != trace.Failed {
		t.Fatalf("tampering with tests must not pass, got %v", r.Status())
	}
}

func TestAgentCrashStillRecorded(t *testing.T) {
	e := setup(t, "crash")
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	r := load(t, e, id)
	if r.Verdict == nil {
		t.Error("run must carry a verdict")
	}
	if !strings.HasSuffix(r.Stderr, "\nfatal in /work\n") || len(r.Stderr) != 500 {
		t.Errorf("stderr must be the scrubbed last 500 bytes, got %d: %q", len(r.Stderr), r.Stderr)
	}
}

func TestMissingBinaryIsError(t *testing.T) {
	e := setup(t, "none")
	t.Setenv("AGENTOPS_CLAUDE_BIN", filepath.Join(t.TempDir(), "nope"))
	if _, err := doRun(t, e, "baseline"); err == nil {
		t.Fatal("want error")
	}
	if entries, _ := os.ReadDir(e.runs); len(entries) != 0 {
		t.Errorf("run directory of a run that never started must be removed, found %d entries", len(entries))
	}
}

func TestExtraTestFileCannotHideFailures(t *testing.T) {
	e := setup(t, "extra")
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if r := load(t, e, id); r.Status() != trace.Failed {
		t.Fatalf("a TestMain added by the agent must not pass, got %v", r.Status())
	}
}

// dropFiles makes the fake agent write these files into its workdir.
func dropFiles(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MODE", "drop")
	t.Setenv("DROP", dir)
}

// statsWith is the original, still buggy stats.go with extra imports and declarations.
func statsWith(t *testing.T, imports, decls string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(testbedDir, "stats.go"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(src), `import "sort"`, "import (\n\t\"sort\"\n"+imports+")", 1) + "\n" + decls + "\n"
}

func TestVerdictCannotBeForged(t *testing.T) {
	const rejected = "вердикт отклонён: stats.go "
	const notPure = " (новые импорты разрешены только из набора чистых пакетов)"
	forgedPass := "\tfmt.Print(\"--- PASS: TestTotal (0.00s)\\n--- PASS: TestMean (0.00s)\\n" +
		"--- PASS: TestMedian (0.00s)\\n--- PASS: TestWordCount (0.00s)\\n--- PASS: TestTruncate (0.00s)\\nPASS\\n\")\n"
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"added file with an early exit", map[string]string{"zz.go": "package shop\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n" +
			"func init() {\n\tif testing.Testing() {\n\t\tos.Exit(0)\n\t}\n}\n"}, "--- FAIL: TestMean"},
		{"early exit in an existing file", map[string]string{"stats.go": statsWith(t, "\t\"os\"\n\t\"testing\"\n",
			"func init() {\n\tif testing.Testing() {\n\t\tos.Exit(0)\n\t}\n}")},
			rejected + "добавляет импорт os" + notPure + "\n" + rejected + "добавляет импорт testing" + notPure + "\n" +
				rejected + "добавляет init()"},
		{"forged PASS lines and an exit through flag", map[string]string{"stats.go": statsWith(t, "\t\"flag\"\n\t\"fmt\"\n",
			"var _ = func() int {\n"+forgedPass+"\tflag.CommandLine.Parse([]string{\"-h\"})\n\treturn 0\n}()")},
			rejected + "добавляет импорт flag" + notPure},
		{"tests filtered out through testing.Init and flag.Set", map[string]string{"stats.go": statsWith(t, "\t\"flag\"\n\t\"testing\"\n",
			"var _ = func() int {\n\ttesting.Init()\n\tflag.Set(\"test.run\", \"^$\")\n\treturn 0\n}()")},
			rejected + "добавляет импорт flag" + notPure},
		{"added fmt", map[string]string{"stats.go": statsWith(t, "\t\"fmt\"\n", "var _ = fmt.Sprint")},
			rejected + "добавляет импорт fmt" + notPure},
		{"added init without new imports", map[string]string{"stats.go": statsWith(t, "", "func init() {}")},
			rejected + "добавляет init()"},
		{"file that does not parse", map[string]string{"stats.go": "package shop\n\nfunc broken( {\n"},
			rejected + "не разбирается как Go-код"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, "drop")
			dropFiles(t, c.files)
			id, err := doRun(t, e, "baseline")
			if err != nil {
				t.Fatal(err)
			}
			r := load(t, e, id)
			if r.Status() != trace.Failed || !strings.Contains(r.Verdict.TestOutput, c.want) {
				t.Fatalf("forged run must fail with %q, got %v: %q", c.want, r.Status(), r.Verdict.TestOutput)
			}
		})
	}
}

// An honest fix may add a pure import and keeps the imports the originals have.
func TestVerdictPassesHonestFixWithPureImport(t *testing.T) {
	e := setup(t, "drop")
	files := map[string]string{"text.go": "package shop\n\nimport (\n\t\"strings\"\n\t\"unicode/utf8\"\n)\n\n" +
		"func WordCount(s string) int {\n\treturn len(strings.Fields(s))\n}\n\n" +
		"func Truncate(s string, n int) string {\n\tif utf8.RuneCountInString(s) <= n {\n\t\treturn s\n\t}\n" +
		"\treturn string([]rune(s)[:n-1]) + \"…\"\n}\n"}
	for _, f := range []string{"cart.go", "stats.go"} {
		b, err := os.ReadFile(filepath.Join(os.Getenv("FIXED"), f))
		if err != nil {
			t.Fatal(err)
		}
		files[f] = string(b)
	}
	dropFiles(t, files)
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if r := load(t, e, id); r.Status() != trace.Passed {
		t.Fatalf("got %v: %s", r.Status(), r.Verdict.TestOutput)
	}
}

func TestPolicy(t *testing.T) {
	const orig = "package p\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n"
	for agent, want := range map[string]string{
		orig: "",
		"package p\n\nimport (\n\t\"fmt\"\n\t\"unicode/utf8\"\n\t. \"strings\"\n)\n": "",
		"package p\n\nimport f \"flag\"\n":                                           "x.go добавляет импорт flag (новые импорты разрешены только из набора чистых пакетов)",
		"package p\n\nimport . \"os\"\n":                                             "x.go добавляет импорт os (новые импорты разрешены только из набора чистых пакетов)",
		"package p\n\nimport _ \"unsafe\"\n":                                         "x.go добавляет импорт unsafe (новые импорты разрешены только из набора чистых пакетов)",
		"package p\n\nimport \"C\"\n":                                                "x.go добавляет импорт C (новые импорты разрешены только из набора чистых пакетов)",
		"package p\n\nfunc init() {}\n":                                              "x.go добавляет init()",
		"package p\n\nfunc (T) init() {}\n":                                          "",
		"package p\n\nfunc x( {\n":                                                   "x.go не разбирается как Go-код",
	} {
		if got := strings.Join(policy("x.go", []byte(orig), []byte(agent)), "; "); got != want {
			t.Errorf("%q: got %q, want %q", agent, got, want)
		}
	}
}

func TestVerdictIgnoresGoModEdit(t *testing.T) {
	e := setup(t, "drop")
	files := map[string]string{"go.mod": "module testbed\n\ngo 1.27\n\nnot a directive\n"}
	for _, f := range []string{"cart.go", "stats.go", "text.go"} {
		b, err := os.ReadFile(filepath.Join(os.Getenv("FIXED"), f))
		if err != nil {
			t.Fatal(err)
		}
		files[f] = string(b)
	}
	dropFiles(t, files)
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	r := load(t, e, id)
	if r.Status() != trace.Passed {
		t.Fatalf("an honest fix with a broken go.mod must pass, got %v: %s", r.Status(), r.Verdict.TestOutput)
	}
	if strings.Contains(r.Verdict.TestOutput, "=== RUN") {
		t.Errorf("test output carries -v framing: %s", r.Verdict.TestOutput)
	}
}

func TestVerdictKillsLoopingTestBinary(t *testing.T) {
	e := setup(t, "drop")
	t.Setenv("GOTMPDIR", "") // the test binary is built under TMPDIR, which is unique to this test
	dropFiles(t, map[string]string{"stats.go": statsWith(t, "", "var _ = func() int {\n\tfor {\n\t}\n}()")})
	saved := verdictTimeout
	verdictTimeout = 5 * time.Second // the test binary must get built and started first
	defer func() { verdictTimeout = saved }()

	// The looping code may not write files (only pure imports), so the test
	// binary is watched from outside, by its path under the build directory.
	binary := "^" + regexp.QuoteMeta(os.Getenv("TMPDIR")) + "/go-build[0-9]+/b[0-9]+/testbed\\.test"
	running := func() bool { return exec.Command("pgrep", "-f", binary).Run() == nil }
	done, seen := make(chan struct{}), make(chan bool)
	go func() {
		was := false
		for {
			select {
			case <-done:
				seen <- was
				return
			case <-time.After(50 * time.Millisecond):
				was = was || running()
			}
		}
	}()

	start := time.Now()
	id, err := doRun(t, e, "baseline")
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("run took %v", took)
	}
	r := load(t, e, id)
	if r.Status() != trace.Failed || !strings.Contains(r.Verdict.TestOutput, "не уложились") {
		t.Fatalf("got %v: %q", r.Status(), r.Verdict.TestOutput)
	}
	if !<-seen {
		t.Fatal("the test binary never started: raise the timeout")
	}
	for i := 0; running(); i++ {
		if i == 50 {
			t.Fatal("test binary still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestVerdictNeedsEveryTestToPass(t *testing.T) {
	e := setup(t, "none")
	s, _ := FindScenario("baseline")
	for body, want := range map[string]string{
		"func TestA(t *testing.T) { t.Skip(\"later\") }\nfunc TestB(t *testing.T) {}\n": "нет отметки о прохождении: TestA\n",
		"func helper(t *testing.T) {}\n":                                                "в исходных тестах не найдено ни одного теста",
	} {
		tb := t.TempDir()
		os.WriteFile(filepath.Join(tb, "go.mod"), []byte("module x\n\ngo 1.27\n"), 0o644)
		os.WriteFile(filepath.Join(tb, "a_test.go"), []byte("package x\n\nimport \"testing\"\n\n"+body), 0o644)
		id, err := Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: tb, Model: "haiku", Scenario: s})
		if err != nil {
			t.Fatal(err)
		}
		if r := load(t, e, id); r.Status() != trace.Failed || !strings.HasSuffix(r.Verdict.TestOutput, want) {
			t.Errorf("got %v: %q, want suffix %q", r.Status(), r.Verdict.TestOutput, want)
		}
	}
}

func TestTestTextReadsLikePlainGoTest(t *testing.T) {
	ev := func(action, test, output string) string {
		b, _ := json.Marshal(map[string]string{"Action": action, "Test": test, "Output": output})
		return string(b) + "\n"
	}
	in := ev("run", "TestSub", "") + ev("output", "TestSub", "=== RUN   TestSub\n") +
		ev("output", "TestSub", "    a_test.go:6: parent log\n") +
		ev("output", "TestSub/ok", "    a_test.go:7: passing sub log\n") + ev("output", "TestSub/ok", "--- PASS: TestSub/ok (0.00s)\n") +
		ev("pass", "TestSub/ok", "") +
		ev("output", "TestSub/bad", "    a_test.go:8: sub failed\n") + ev("output", "TestSub/bad", "--- FAIL: TestSub/bad (0.00s)\n") +
		ev("fail", "TestSub/bad", "") +
		ev("output", "TestSub", "--- FAIL: TestSub (0.00s)\n") + ev("fail", "TestSub", "") +
		ev("output", "TestOK", "--- PASS: TestOK (0.00s)\n") + ev("pass", "TestOK", "") +
		ev("output", "TestCut", "    a_test.go:20: started\n") +
		"go: plain text\n" + ev("output", "", "FAIL\tx\t0.3s\n")
	text, passed := testText([]byte(in))
	want := "--- FAIL: TestSub (0.00s)\n    a_test.go:6: parent log\n    --- FAIL: TestSub/bad (0.00s)\n        a_test.go:8: sub failed\n" +
		"go: plain text\nFAIL\tx\t0.3s\n    a_test.go:20: started\n"
	if text != want || !passed["TestOK"] || !passed["TestSub/ok"] || passed["TestSub"] {
		t.Errorf("got\n%s\nwant\n%s\npassed %v", text, want, passed)
	}
}

func TestVerdictOutputReplacesHome(t *testing.T) {
	e := setup(t, "none")
	tb := t.TempDir()
	os.WriteFile(filepath.Join(tb, "go.mod"), []byte("module x\n\ngo 1.27\n"), 0o644)
	os.WriteFile(filepath.Join(tb, "a_test.go"), []byte("package x\nimport (\"os\"; \"testing\")\nfunc TestA(t *testing.T) { wd, _ := os.Getwd(); t.Fatal(os.Getenv(\"HOME\"), wd) }\n"), 0o644)
	s, _ := FindScenario("baseline")
	id, err := Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: tb, Model: "haiku", Scenario: s})
	if err != nil {
		t.Fatal(err)
	}
	out := load(t, e, id).Verdict.TestOutput
	if strings.Contains(out, e.home) || !strings.Contains(out, "~") {
		t.Errorf("home not replaced: %s", out)
	}
	if strings.Contains(out, "agentops-verdict-") || !strings.Contains(out, "~ /work") {
		t.Errorf("verdict directory not replaced: %s", out)
	}
}

func TestIDAllocation(t *testing.T) {
	e := setup(t, "none")
	for _, d := range []string{"run-03", "run-11", "notes"} {
		if err := os.MkdirAll(filepath.Join(e.runs, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if id != "run-12" {
		t.Errorf("id %q, want run-12", id)
	}
	if id2, _ := doRun(t, e, "baseline"); id2 != "run-13" {
		t.Errorf("id %q, want run-13", id2)
	}
}

func TestMetaFields(t *testing.T) {
	e := setup(t, "none")
	id, _ := doRun(t, e, "no-edit")
	b, err := os.ReadFile(filepath.Join(e.runs, id, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	s, _ := FindScenario("no-edit")
	if m["task"] != "fix-tests" || m["agent"] != "claude" || m["model"] != "haiku" || m["scenario"] != "no-edit" ||
		m["prompt"] != s.Prompt {
		t.Errorf("meta %v", m)
	}
	if _, ok := m["stderr"]; ok {
		t.Error("stderr of an agent that exited 0 must not be kept")
	}
	if !strings.HasSuffix(m["started_at"].(string), "Z") {
		t.Errorf("started_at not UTC: %v", m["started_at"])
	}
}

func TestMetaWrittenTwice(t *testing.T) {
	e := setup(t, "none")
	// The agent reads meta.json of the run in progress: no verdict yet.
	runDirFile := filepath.Join(e.runs, "run-01", "meta.json")
	script := "#!/bin/bash\ncp '" + runDirFile + "' \"$SNAP\"\n"
	bin := filepath.Join(t.TempDir(), "snap")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "snap.json")
	t.Setenv("AGENTOPS_CLAUDE_BIN", bin)
	t.Setenv("SNAP", snap)
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	during, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(during), "verdict") {
		t.Errorf("meta during the run has a verdict: %s", during)
	}
	if load(t, e, id).Verdict == nil {
		t.Error("final meta lacks verdict")
	}
}

func TestTraceIsSanitized(t *testing.T) {
	e := setup(t, "none")
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	all := string(b)
	for _, leak := range []string{"agentops-work-", "/var/folders", "/private", e.home, "someuser", "corp-plugin", `"cwd"`, `"plugins"`} {
		if strings.Contains(all, leak) {
			t.Errorf("trace leaks %q:\n%s", leak, all)
		}
	}
	for _, want := range []string{"/work/stats.go", "see /work/x and ~/y"} {
		if !strings.Contains(all, want) {
			t.Errorf("trace lacks %q:\n%s", want, all)
		}
	}
	if r := load(t, e, id); len(r.Steps) != 1 {
		t.Errorf("steps %d", len(r.Steps))
	}
}

func TestVerdictOutputUsesWorkPath(t *testing.T) {
	e := setup(t, "none")
	id, _ := doRun(t, e, "baseline")
	out := load(t, e, id).Verdict.TestOutput
	if strings.Contains(out, "agentops-work-") || len(out) > 2000 {
		t.Errorf("bad test output (%d bytes): %s", len(out), out)
	}
}

func TestAgentArgs(t *testing.T) {
	e := setup(t, "none")
	if _, err := doRun(t, e, "no-edit"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.args)
	got := string(b)
	for _, want := range []string{"--safe-mode --strict-mcp-config --restricted", "--model haiku", "--max-turns 25",
		"--tools Read,Glob,Grep,Edit,Bash", "--allowedTools Read Glob Grep Bash(go test:*)",
		"--disallowedTools Edit Write MultiEdit NotebookEdit", "\nTZ=UTC\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
}

func TestFindScenario(t *testing.T) {
	if _, ok := FindScenario("nope"); ok {
		t.Error("unknown scenario found")
	}
	if s, ok := FindScenario("few-turns"); !ok || s.MaxTurns != 3 {
		t.Errorf("few-turns: %+v", s)
	}
}

func TestRunScrubsLoginAndHostName(t *testing.T) {
	e := setup(t, "fix")
	u, err := user.Current()
	if err != nil || len(u.Username) < 3 || len([]rune(u.Name)) < 3 {
		t.Skip("no usable login or full name")
	}
	t.Setenv("FULLNAME", u.Name)
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"trace.jsonl", "meta.json"} {
		b, err := os.ReadFile(filepath.Join(e.runs, id, f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), u.Username) || strings.Contains(string(b), u.Name) {
			t.Errorf("%s contains the login or full name", f)
		}
		if h, _ := os.Hostname(); len(h) >= 3 && strings.Contains(string(b), h) {
			t.Errorf("%s contains the host name", f)
		}
	}
	b, _ := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
	if !strings.Contains(string(b), "by user (user) on host") {
		t.Errorf("replacements missing: %s", b)
	}
}

// hang starts a fake agent that never finishes and returns a func that waits
// until it is up and reports its pids.
func hang(t *testing.T, mode string) func() []int {
	t.Helper()
	pidsFile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("MODE", mode)
	t.Setenv("PIDS", pidsFile)
	return func() []int {
		for range 500 {
			if b, err := os.ReadFile(pidsFile); err == nil && strings.HasSuffix(string(b), "\n") {
				var pids []int
				for _, f := range strings.Fields(string(b)) {
					n, _ := strconv.Atoi(f)
					pids = append(pids, n)
				}
				return pids
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("fake agent did not start")
		return nil
	}
}

func assertDead(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		for i := 0; syscall.Kill(pid, 0) == nil; i++ {
			if i == 100 {
				t.Fatalf("process %d still running", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestInterruptKillsAgentAndCleansUp(t *testing.T) {
	e := setup(t, "none")
	started := hang(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	var pids []int
	go func() {
		pids = started()
		cancel()
	}()
	s, _ := FindScenario("baseline")
	_, err := Run(ctx, Options{RunsDir: e.runs, TestbedDir: testbedDir, Model: "haiku", Scenario: s})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	assertDead(t, pids)
	if entries, _ := os.ReadDir(e.runs); len(entries) != 0 {
		t.Errorf("unfinished run directory left: %v", entries)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "agentops-*")); len(left) != 0 {
		t.Errorf("temp directories left: %v", left)
	}
}

// The agent fixes the code and then hangs: green tests do not make a timed-out run pass.
func TestAgentTimeoutIsRecordedAsFailed(t *testing.T) {
	e := setup(t, "none")
	started := hang(t, "hang")
	saved := agentTimeout
	agentTimeout = 2 * time.Second // room for the fake to start on a loaded machine
	defer func() { agentTimeout = saved }()
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	assertDead(t, started())
	r := load(t, e, id)
	if r.Status() != trace.Failed || r.StopReason != "timeout" || r.Duration < 2*time.Second {
		t.Fatalf("status %v, stop %q, duration %v", r.Status(), r.StopReason, r.Duration)
	}
	if strings.Contains(r.Verdict.TestOutput, "FAIL") {
		t.Fatalf("the agent fixed the code before hanging, tests must be green: %s", r.Verdict.TestOutput)
	}
}

func TestLingeringAgentKeepsItsResult(t *testing.T) {
	e := setup(t, "none")
	started := hang(t, "linger")
	saved := agentTimeout
	agentTimeout = 2 * time.Second
	defer func() { agentTimeout = saved }()
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	assertDead(t, started())
	r := load(t, e, id)
	if r.Status() != trace.Passed || r.StopReason != "success" || r.CostUSD != 0.02 || r.Result != "done" {
		t.Fatalf("status %v, stop %q, cost %v, result %q", r.Status(), r.StopReason, r.CostUSD, r.Result)
	}
}

func TestEscapedGrandchildDoesNotBlockTheRun(t *testing.T) {
	e := setup(t, "none")
	started := hang(t, "escape")
	saved := agentTimeout
	agentTimeout = 2 * time.Second
	defer func() { agentTimeout = saved }()
	start := time.Now()
	id, err := doRun(t, e, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range started() {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("run took %v: the read loop waited for the grandchild", took)
	}
	if r := load(t, e, id); r.StopReason != "timeout" {
		t.Errorf("stop reason %q", r.StopReason)
	}
}

func TestMissingTestbedLeavesNoRunDir(t *testing.T) {
	e := setup(t, "none")
	s, _ := FindScenario("baseline")
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: missing, Model: "haiku", Scenario: s})
	if err == nil || err.Error() != "каталог подопытного проекта не найден: "+missing {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(e.runs); err == nil {
		t.Error("runs directory created")
	}
}

func TestConcurrentIDsAreUnique(t *testing.T) {
	dir := t.TempDir()
	ids := make(chan string, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			id, err := nextID(dir)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("id %s allocated twice", id)
		}
		seen[id] = true
	}
}
