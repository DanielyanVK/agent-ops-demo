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
if [ "$MODE" = fixagents ]; then
  cp "$FIXED"/*.go .
  mkdir -p .agents
  printf 'package shop\nimport "testing"\nfunc TestZ(t *testing.T) { t.Fatal("x") }\n' > .agents/zz_test.go
fi
echo "$@" > "$ARGS_OUT"
[ -f .agents/skills/go-test-fixer/SKILL.md ] && echo SKILL_INSTALLED >> "$ARGS_OUT"
echo "GOCACHE=$GOCACHE" >> "$ARGS_OUT"
echo "TZ=$TZ" >> "$ARGS_OUT"
printf '{"type":"thread.started","thread_id":"t-secret"}\n'
printf '{"type":"turn.started"}\n'
printf '{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"cat %s/calc.go","aggregated_output":"","exit_code":null,"status":"in_progress"}}\n' "$PWD"
printf '{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"cat %s/calc.go","aggregated_output":"home %s","exit_code":0,"status":"completed"}}\n' "$PWD" "$HOME"
printf '{"type":"item.completed","item":{"id":"item_2","type":"todo_list","items":[{"text":"secret-plan"}]}}\n'
case "$MODE" in
  failed) printf '{"type":"turn.failed","error":{"message":"401 Incorrect API key provided: sk-proj-abcdefgh12345678. Request req_abcdefgh1234"}}\n' ;;
  *) printf '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":5,"output_tokens":3,"reasoning_output_tokens":1}}\n' ;;
esac
echo "warning from $PWD" >&2
[ "$MODE" = crash ] && exit 3
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
	if len(lines) != 5 { // thread.started, turn.started, command started and completed, turn.completed; the todo_list is dropped
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

func skillRun(t *testing.T, e env, agent, pluginDir string) (string, error) {
	t.Helper()
	s, _ := FindScenario("skill")
	return Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: testbedDir,
		Agent: agent, Model: "haiku", Scenario: s, PluginDir: pluginDir})
}

func TestClaudeSkillScenarioLoadsThePlugin(t *testing.T) {
	e := setup(t, "none")
	plugin := filepath.Join(t.TempDir(), "plug")
	os.MkdirAll(plugin, 0o755)
	if _, err := skillRun(t, e, "claude", plugin); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.args)
	got := string(b)
	for _, want := range []string{"--plugin-dir " + plugin + " ", "--strict-mcp-config --restricted", "--tools Read,Glob,Grep,Edit,Bash,Skill",
		"--allowedTools Read Glob Grep Edit Bash(go test:*) Skill"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	if strings.Contains(got, "--safe-mode") {
		t.Errorf("safe mode disables skills: %s", got)
	}
}

func TestSkillScenarioNeedsThePluginDirectory(t *testing.T) {
	e := setup(t, "none")
	_, err := skillRun(t, e, "claude", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "каталог плагина не найден") {
		t.Fatalf("err %v", err)
	}
	if _, serr := os.Stat(e.args); serr == nil {
		t.Error("the agent must not start")
	}
	if entries, _ := os.ReadDir(e.runs); len(entries) != 0 {
		t.Errorf("run directory left: %v", entries)
	}
}

func TestCodexSkillScenarioInstallsTheDemoSkill(t *testing.T) {
	e := setupCodex(t, "fixagents")
	id, err := skillRun(t, e, "codex", "../../plugin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.args)
	got := string(b)
	if !strings.Contains(got, "-s workspace-write ") || !strings.Contains(got, "\nSKILL_INSTALLED\n") {
		t.Errorf("sandbox or installed skill: %s", got)
	}
	for _, lifted := range []string{"skill_search", "skill_mcp_dependency_install"} {
		if strings.Contains(got, "--disable "+lifted+" ") {
			t.Errorf("--disable %s must be lifted: %s", lifted, got)
		}
	}
	for _, kept := range []string{"plugins", "remote_plugin", "plugin_sharing", "apps", "hooks", "memories", "multi_agent", "browser_use", "computer_use", "goals", "tool_suggest"} {
		if !strings.Contains(got, "--disable "+kept+" ") {
			t.Errorf("--disable %s must stay: %s", kept, got)
		}
	}
	// Files the agent adds under .agents (even a failing test) cannot change the verdict.
	if v := load(t, e, id).Verdict; v == nil || !v.Passed {
		t.Errorf("verdict: %+v", v)
	}
}

func TestCodexSkillScenarioNeedsThePluginDirectory(t *testing.T) {
	e := setupCodex(t, "none")
	_, err := skillRun(t, e, "codex", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "каталог плагина не найден") {
		t.Fatalf("err %v", err)
	}
	if _, serr := os.Stat(e.args); serr == nil {
		t.Error("the agent must not start")
	}
}

func TestScrubReplacesPluginDirectory(t *testing.T) {
	s := Scrub{Home: "/Users/tester", PluginDir: "/Users/tester/code/agentops/plugin"}
	got := string(scrub([]byte("Base directory: /Users/tester/code/agentops/plugin/skills/x"), s))
	if got != "Base directory: /plugin/skills/x" {
		t.Errorf("got %q", got)
	}
}

func TestDemoPluginIsWellFormed(t *testing.T) {
	b, err := os.ReadFile("../../plugin/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || m["name"] != "agentops-demo" || m["version"] != "0.1.0" {
		t.Errorf("plugin.json: %s, %v", b, err)
	}
	skill, err := os.ReadFile("../../plugin/skills/go-test-fixer/SKILL.md")
	if err != nil || !strings.HasPrefix(string(skill), "---\nname: go-test-fixer\n") {
		t.Errorf("SKILL.md: %v", err)
	}
}

func TestSanitizeCodexHidesSkillText(t *testing.T) {
	read := sanitizeCodex(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"/bin/zsh -lc \"sed -n '1,200p' .agents/skills/go-test-fixer/SKILL.md\"","aggregated_output":"# secret skill body","exit_code":0,"status":"completed"}}`)
	if strings.Contains(read, "secret skill body") || !strings.Contains(read, "[текст навыка не сохраняется]") || !strings.Contains(read, "go-test-fixer") {
		t.Errorf("skill text kept: %s", read)
	}
	// A listing is not a read: its output stays.
	ls := sanitizeCodex(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"ls .agents/skills/x/SKILL.md","aggregated_output":".agents/skills/x/SKILL.md","exit_code":0,"status":"completed"}}`)
	if strings.Contains(ls, "не сохраняется") {
		t.Errorf("listing hidden: %s", ls)
	}
}

func TestSanitizeCodexNumberShapes(t *testing.T) {
	for _, in := range []string{
		`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"x","exit_code":1.5,"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"x","exit_code":1e0,"status":"failed"}}`,
	} {
		if got := sanitizeCodex(in); strings.Contains(got, "exit_code") {
			t.Errorf("non-integer exit code kept: %s", got)
		}
	}
	if got := sanitizeCodex(`{"type":"item.started","item":{"id":"a","type":"command_execution"}}`); got == "" {
		t.Error("started dropped")
	}
	if got := sanitizeCodex(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"x","exit_code":null,"status":"in_progress"}}`); !strings.Contains(got, `"exit_code":null`) {
		t.Errorf("null exit code lost: %s", got)
	}
	if got := sanitizeCodex(`{"type":"turn.completed","usage":{"input_tokens":1.5,"output_tokens":null,"reasoning_output_tokens":3}}`); strings.Contains(got, "1.5") || strings.Contains(got, "null") || !strings.Contains(got, `"reasoning_output_tokens":3`) {
		t.Errorf("token shapes: %s", got)
	}
}

func TestCodexRunWithAPairOfItemEventsIsParsed(t *testing.T) {
	e := setupCodex(t, "fix")
	id, err := doCodexRun(t, e, "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	r := load(t, e, id)
	if len(r.Steps) != 1 || r.Steps[0].Line != "cat /work/calc.go" || r.Steps[0].Duration < 0 || r.StopReason != "success" || r.TokensIn != 10 || r.Turns != 1 {
		t.Errorf("%+v", r)
	}
	if r.Stderr != "" || r.Status().String() != "passed" {
		t.Errorf("stderr %q status %v", r.Stderr, r.Status())
	}
}

func TestCodexNonZeroExitKeepsScrubbedStderr(t *testing.T) {
	e := setupCodex(t, "crash")
	id, err := doCodexRun(t, e, "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	r := load(t, e, id)
	if !strings.Contains(r.Stderr, "warning from /work") || strings.Contains(r.Stderr, "agentops-work") {
		t.Errorf("stderr %q", r.Stderr)
	}
	if r.Status().String() != "failed" {
		t.Error("a run without a fix fails")
	}
}

func TestCodexFailedTurnIsRecordedAndRedacted(t *testing.T) {
	e := setupCodex(t, "failed")
	id, err := doCodexRun(t, e, "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
	if strings.Contains(string(tr), "abcdefgh") || !strings.Contains(string(tr), "sk-…") || !strings.Contains(string(tr), "req_…") {
		t.Errorf("key not redacted:\n%s", tr)
	}
	r := load(t, e, id)
	if r.StopReason != "error_during_execution" || !r.ResultError || !strings.HasPrefix(r.Result, "401 Incorrect API key") {
		t.Errorf("%+v", r)
	}
}

func TestAgentsDoNotInheritTheEnvironment(t *testing.T) {
	t.Setenv("AGENTOPS_TEST_SECRET", "hunter2-parent-shell")
	t.Setenv("ANTHROPIC_API_KEY", "kept-for-the-agent")
	t.Setenv("OPENAI_API_KEY", "other-providers-key")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	e := setupCodex(t, "none")
	dir := t.TempDir()
	claude := filepath.Join(dir, "claude")
	codex := filepath.Join(dir, "codex")
	os.WriteFile(claude, []byte(`#!/bin/bash
printf '{"type":"result","subtype":"success","is_error":false,"result":"secret=[%s] key=[%s] other=[%s] proxy=[%s] tz=[%s]"}\n' "$AGENTOPS_TEST_SECRET" "$ANTHROPIC_API_KEY" "$OPENAI_API_KEY" "$HTTPS_PROXY" "$TZ"
`), 0o755)
	os.WriteFile(codex, []byte(`#!/bin/bash
printf '{"type":"item.completed","item":{"id":"i","type":"command_execution","command":"env","aggregated_output":"secret=[%s] gocache=[%s] other=[%s] key=[%s] proxy=[%s]","exit_code":0,"status":"completed"}}\n' "$AGENTOPS_TEST_SECRET" "${GOCACHE:+set}" "$ANTHROPIC_API_KEY" "$OPENAI_API_KEY" "$HTTPS_PROXY"
`), 0o755)
	t.Setenv("AGENTOPS_CLAUDE_BIN", claude)
	t.Setenv("AGENTOPS_CODEX_BIN", codex)
	for _, agent := range []string{"claude", "codex"} {
		sc, _ := FindScenario("baseline")
		id, err := Run(context.Background(), Options{RunsDir: e.runs, TestbedDir: testbedDir, Agent: agent, Model: "x", Scenario: sc})
		if err != nil {
			t.Fatal(err)
		}
		tr, _ := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
		got := string(tr)
		if strings.Contains(got, "hunter2") || !strings.Contains(got, "secret=[]") {
			t.Errorf("%s inherited the parent environment:\n%s", agent, got)
		}
		if !strings.Contains(got, "proxy=[http://proxy.example:3128]") {
			t.Errorf("%s lost the proxy:\n%s", agent, got)
		}
		if agent == "claude" && (!strings.Contains(got, "key=[kept-for-the-agent] other=[]") || !strings.Contains(got, "tz=[UTC]")) {
			t.Errorf("claude: own key lost or the other provider's key leaked:\n%s", got)
		}
		if agent == "codex" && (!strings.Contains(got, "gocache=[set]") || !strings.Contains(got, "other=[] key=[other-providers-key]")) {
			t.Errorf("codex: GOCACHE or own key lost, or the other provider's key leaked:\n%s", got)
		}
	}
}

func TestAgentEnvIsTheAllowList(t *testing.T) {
	t.Setenv("AGENTOPS_TEST_SECRET", "x")
	t.Setenv("CODEX_HOME", "/h/.codex")
	t.Setenv("LC_CTYPE", "")
	env := strings.Join(agents["codex"].env([]string{"TZ=UTC"}), "\n")
	for _, want := range []string{"PATH=", "CODEX_HOME=/h/.codex", "LC_CTYPE=", "TZ=UTC"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "AGENTOPS_TEST_SECRET") {
		t.Errorf("env leaks:\n%s", env)
	}
}

func TestCodexTimeoutLineIsParsed(t *testing.T) {
	// the runner-written line, read back by the parser
	e := setupCodex(t, "none")
	bin := filepath.Join(t.TempDir(), "slow-codex")
	os.WriteFile(bin, []byte("#!/bin/bash\nsleep 30\n"), 0o755)
	t.Setenv("AGENTOPS_CODEX_BIN", bin)
	old := agentTimeout
	agentTimeout = 300 * time.Millisecond
	defer func() { agentTimeout = old }()
	id, err := doCodexRun(t, e, "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	if r := load(t, e, id); r.StopReason != "timeout" || !r.ResultError {
		t.Errorf("%+v", r)
	}
}

func TestSanitizeCodexHidesEveryShapeOfASkillRead(t *testing.T) {
	P := "~/.codex/skills/foo/SKILL.md"
	for _, cmd := range []string{"cd /w && cat " + P, "command cat " + P, "/bin/cat " + P, "env cat " + P, "echo " + P + " | xargs cat",
		"pwd\\ncat " + P, "bash -c 'cat " + P + "'", "python3 -c 'print(open(\\\"" + P + "\\\").read())'",
		"cat ~/.codex/skills/.system/foo/SKILL.md", "Q=" + P + "; cat $Q"} {
		line := `{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"` + cmd + `","aggregated_output":"BODY-OF-SKILL","exit_code":0,"status":"completed"}}`
		got := sanitizeCodex(line)
		if got == "" || strings.Contains(got, "BODY-OF-SKILL") {
			t.Errorf("%s: %s", cmd, got)
		}
	}
	for _, cmd := range []string{"ls ~/.codex/skills/*/SKILL.md", "find ~/.codex -name SKILL.md", "test -f " + P, "go test ./..."} {
		line := `{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"` + cmd + `","aggregated_output":"LISTING","exit_code":0,"status":"completed"}}`
		if got := sanitizeCodex(line); !strings.Contains(got, "LISTING") {
			t.Errorf("%s must stay readable: %s", cmd, got)
		}
	}
}
