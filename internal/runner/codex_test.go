package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var testScrub = Scrub{Workdir: "/private/var/folders/zz/T/agentops-work", Home: "/Users/tester"}
var testNow = time.Date(2026, 11, 1, 12, 0, 0, 123_000_000, time.UTC)

func sanitizeCodex(line string) string {
	return string(SanitizeCodex([]byte(line), testScrub, testNow))
}

func TestSanitizeCodexWhitelistPerLineType(t *testing.T) {
	cases := map[string]string{
		`{"type":"thread.started","thread_id":"abc"}`: `{"ts":"2026-11-01T12:00:00.123Z","type":"thread.started"}`,
		`{"type":"turn.started","x":1}`:               `{"ts":"2026-11-01T12:00:00.123Z","type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":4,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1,"plan":"pro"}}`: `{"ts":"2026-11-01T12:00:00.123Z","type":"turn.completed","usage":{"cached_input_tokens":4,"input_tokens":5,"output_tokens":2,"reasoning_output_tokens":1}}`,
		`{"type":"turn.failed","error":{"message":"boom","code":7}}`:                                                        `{"error":{"message":"boom"},"ts":"2026-11-01T12:00:00.123Z","type":"turn.failed"}`,
		`{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"rm -rf ~","aggregated_output":""}}`: `{"item":{"id":"i1","type":"command_execution"},"ts":"2026-11-01T12:00:00.123Z","type":"item.started"}`,
	}
	for in, want := range cases {
		if got := sanitizeCodex(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestSanitizeCodexItemsKeepOnlyTheirFields(t *testing.T) {
	cases := map[string]string{
		`{"id":"a","type":"agent_message","text":"hi","phase":"x"}`:                                                                  `{"id":"a","text":"hi","type":"agent_message"}`,
		`{"id":"a","type":"reasoning","text":"hm","summary":[]}`:                                                                     `{"id":"a","text":"hm","type":"reasoning"}`,
		`{"id":"a","type":"command_execution","command":"ls","aggregated_output":"x","exit_code":0,"status":"completed","cwd":"/x"}`: `{"aggregated_output":"x","command":"ls","exit_code":0,"id":"a","status":"completed","type":"command_execution"}`,
		`{"id":"a","type":"file_change","changes":[{"path":"/p","kind":"update","diff":"secret"}],"status":"completed"}`:             `{"changes":[{"kind":"update","path":"/p"}],"id":"a","status":"completed","type":"file_change"}`,
		`{"id":"a","type":"mcp_tool_call","server":"s","tool":"t","status":"failed","arguments":{"k":"v"},"result":"r"}`:             `{"id":"a","server":"s","status":"failed","tool":"t","type":"mcp_tool_call"}`,
		`{"id":"a","type":"web_search","query":"q","action":{}}`:                                                                     `{"id":"a","query":"q","type":"web_search"}`,
		`{"id":"a","type":"error","message":"m","extra":1}`:                                                                          `{"id":"a","message":"m","type":"error"}`,
	}
	for item, want := range cases {
		got := sanitizeCodex(`{"type":"item.completed","item":` + item + `}`)
		wantLine := `{"item":` + want + `,"ts":"2026-11-01T12:00:00.123Z","type":"item.completed"}`
		if got != wantLine {
			t.Errorf("%s\n got %s\nwant %s", item, got, wantLine)
		}
	}
}

func TestSanitizeCodexDropsWhatItCannotProveClean(t *testing.T) {
	for _, line := range []string{
		``, `not json`, `null`, `[]`, `{"type":"surprise"}`, `{"type":7}`, `{}`,
		`{"type":"item.completed","item":{"id":"a","type":"todo_list","items":[]}}`, // unknown item type: whole line
		`{"type":"item.completed","item":"text"}`,
		`{"type":"item.completed"}`,
		`{"type":"item.started","item":[1]}`,
	} {
		if got := sanitizeCodex(line); got != "" {
			t.Errorf("%q kept as %s", line, got)
		}
	}
	// Values of an unexpected shape lose their key; the rest of the line stays.
	got := sanitizeCodex(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":{"secret":"x"},"aggregated_output":["y"],"exit_code":"1","status":"failed"}}`)
	if strings.Contains(got, "secret") || strings.Contains(got, `"y"`) || strings.Contains(got, `"command"`) || strings.Contains(got, "exit_code") || !strings.Contains(got, `"status":"failed"`) {
		t.Errorf("unexpected shapes survived: %s", got)
	}
	if got := sanitizeCodex(`{"type":"item.completed","item":{"id":"a","type":"file_change","changes":["/p"],"status":"completed"}}`); strings.Contains(got, "changes") {
		t.Errorf("changes of a wrong shape survived: %s", got)
	}
	if got := sanitizeCodex(`{"type":"turn.completed","usage":{"input_tokens":"many"}}`); strings.Contains(got, "many") {
		t.Errorf("string token count survived: %s", got)
	}
}

func TestSanitizeCodexScrubsPathsAndNames(t *testing.T) {
	got := sanitizeCodex(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"cd /private/var/folders/zz/T/agentops-work && cat /Users/tester/.ssh/x","aggregated_output":"/private/var/folders/zz/T/agentops-work/calc.go","exit_code":0,"status":"completed"}}`)
	for _, leak := range []string{"agentops-work", "/Users/tester", "/private"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaks %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "/work/calc.go") || !strings.Contains(got, "~/.ssh/x") {
		t.Errorf("replacements missing: %s", got)
	}
}

func TestSanitizeCodexRealSamples(t *testing.T) {
	for _, name := range []string{"codex-fix", "codex-readonly", "codex-warning"} {
		f, err := os.Open("testdata/" + name + ".jsonl")
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		kept := 0
		for sc.Scan() {
			out := SanitizeCodex(sc.Bytes(), testScrub, testNow)
			if out == nil {
				continue
			}
			kept++
			if strings.Contains(string(out), "thread_id") || strings.Contains(string(out), "agentops-work") || !strings.Contains(string(out), `"ts":"2026-11-01T12:00:00.123Z"`) {
				t.Errorf("%s: bad line %s", name, out)
			}
		}
		f.Close()
		if kept == 0 {
			t.Errorf("%s: nothing kept", name)
		}
	}
}

// fakeCodex stands in for codex: it records how it was started and prints a
// short trace with fields the sanitizer must remove.
const fakeCodex = `#!/bin/bash
case "$MODE" in
  fix) cp "$FIXED"/*.go . ;;
esac
echo "$@" > "$ARGS_OUT"
echo "GOCACHE=$GOCACHE" >> "$ARGS_OUT"
echo "TZ=$TZ" >> "$ARGS_OUT"
printf '{"type":"thread.started","thread_id":"t-secret"}\n'
printf '{"type":"turn.started"}\n'
printf '{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"cat %s/calc.go","aggregated_output":"home %s","exit_code":0,"status":"completed"}}\n' "$PWD" "$HOME"
printf '{"type":"item.completed","item":{"id":"item_2","type":"todo_list","items":[{"text":"secret-plan"}]}}\n'
printf '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":5,"output_tokens":3,"reasoning_output_tokens":1}}\n'
exit 0
`

func setupCodex(t *testing.T, mode string) env {
	t.Helper()
	e := setup(t, mode)
	bin := filepath.Join(t.TempDir(), "fake-codex")
	if err := os.WriteFile(bin, []byte(fakeCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CODEX_BIN", bin)
	return e
}

func doCodexRun(t *testing.T, e env, sc, model string) (string, error) {
	t.Helper()
	s, _ := FindScenario(sc)
	return Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: testbedDir, Agent: "codex", Model: model, Scenario: s})
}

func TestCodexRunEndToEnd(t *testing.T) {
	e := setupCodex(t, "fix")
	id, err := doCodexRun(t, e, "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(e.runs, id, "meta.json"))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["agent"] != "codex" || m["model"] != "default" || m["scenario"] != "baseline" {
		t.Errorf("meta %v", m)
	}
	if v, _ := m["verdict"].(map[string]any); v == nil || v["passed"] != true {
		t.Errorf("verdict %v", m["verdict"])
	}

	tr, _ := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(tr)), "\n")
	if len(lines) != 4 { // thread.started, turn.started, command, turn.completed; the todo_list is dropped
		t.Fatalf("%d lines:\n%s", len(lines), tr)
	}
	ts := regexp.MustCompile(`"ts":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z"`)
	for _, l := range lines {
		if !ts.MatchString(l) {
			t.Errorf("no ts: %s", l)
		}
	}
	all := string(tr)
	for _, leak := range []string{"t-secret", "thread_id", "secret-plan", "agentops-work", e.home, "/private"} {
		if strings.Contains(all, leak) {
			t.Errorf("trace leaks %q:\n%s", leak, all)
		}
	}
	if !strings.Contains(all, "cat /work/calc.go") || !strings.Contains(all, "home ~") {
		t.Errorf("paths not replaced:\n%s", all)
	}
}

func TestCodexArgsAndEnvironment(t *testing.T) {
	e := setupCodex(t, "none")
	if _, err := doCodexRun(t, e, "no-edit", "gpt-test"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.args)
	got := string(b)
	gocache := filepath.Join(e.home, "Library", "Caches", "agentops", "gocache")
	if cache, _ := os.UserCacheDir(); cache != "" {
		gocache = filepath.Join(cache, "agentops", "gocache")
	}
	for _, want := range []string{"exec --json --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check -s read-only ",
		"--disable apps --disable plugins --disable remote_plugin --disable plugin_sharing --disable hooks --disable memories --disable skill_search --disable skill_mcp_dependency_install --disable multi_agent --disable browser_use --disable browser_use_external --disable computer_use --disable image_generation --disable goals --disable tool_suggest ",
		"--add-dir " + gocache + " -c shell_environment_policy.inherit=all -m gpt-test -C ",
		"GOCACHE=" + gocache + "\n", "\nTZ=UTC\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q:\n%s", want, got)
		}
	}
	if fi, err := os.Stat(gocache); err != nil || !fi.IsDir() {
		t.Errorf("gocache not created: %v", err)
	}
	s, _ := FindScenario("no-edit")
	if !strings.HasSuffix(strings.TrimSpace(strings.SplitN(got, "\n", 2)[0]), s.Prompt) {
		t.Errorf("prompt is not the last argument:\n%s", got)
	}
}

func TestCodexWithoutModelPassesNoFlag(t *testing.T) {
	e := setupCodex(t, "none")
	if _, err := doCodexRun(t, e, "baseline", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.args)
	if strings.Contains(string(b), " -m ") {
		t.Errorf("-m passed without --model:\n%s", b)
	}
}

func TestCodexRejectsScenariosWithTurnLimits(t *testing.T) {
	e := setupCodex(t, "none")
	for _, sc := range []string{"one-turn", "few-turns"} {
		id, err := doCodexRun(t, e, sc, "")
		if err == nil || err.Error() != "сценарий "+sc+" не поддерживается агентом codex" {
			t.Errorf("%s: id %q err %v", sc, id, err)
		}
	}
	if entries, _ := os.ReadDir(e.runs); len(entries) != 0 {
		t.Errorf("rejected runs left directories: %v", entries)
	}
}

func TestCodexTimeoutIsRecorded(t *testing.T) {
	e := setupCodex(t, "none")
	bin := filepath.Join(t.TempDir(), "slow-codex")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTOPS_CODEX_BIN", bin)
	old := agentTimeout
	agentTimeout = 300 * time.Millisecond
	defer func() { agentTimeout = old }()
	id, err := doCodexRun(t, e, "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
	if !strings.Contains(string(tr), `"type":"timeout"`) {
		t.Errorf("no timeout line: %s", tr)
	}
	if load(t, e, id).Status().String() != "failed" {
		t.Error("a timed-out run must fail")
	}
}

func TestUnknownAgentIsRejected(t *testing.T) {
	e := setup(t, "none")
	s, _ := FindScenario("baseline")
	_, err := Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: testbedDir, Agent: "gemini", Model: "x", Scenario: s})
	if err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Errorf("err %v", err)
	}
}
