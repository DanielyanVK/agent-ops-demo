package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"agentops/internal/trace"
)

func TestSanitizeInitKeepsOnlyWhitelist(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"s","model":"m","permissionMode":"acceptEdits","claude_code_version":"1","tools":["Read"],"cwd":"/Users/me/x","plugins":[{"name":"corp"}],"mcp_servers":[],"skills":["a"]}`
	var got map[string]json.RawMessage
	if err := json.Unmarshal(Sanitize([]byte(line), Scrub{Workdir: "/tmp/w", Home: "/Users/me"}), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(initKeys) || got["tools"] != nil {
		t.Errorf("got keys %v, want exactly %v", got, initKeys)
	}
}

func TestSanitizeDenialsKeepOnlyToolAndID(t *testing.T) {
	line := `{"type":"result","permission_denials":[{"tool_name":"Edit","tool_use_id":"t1","tool_input":{"file_path":"/Users/me/x.go"}}]}`
	got := string(Sanitize([]byte(line), Scrub{Workdir: "/tmp/w"}))
	if want := `{"permission_denials":[{"tool_name":"Edit","tool_use_id":"t1"}],"type":"result"}`; got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	if got := string(Sanitize([]byte(`{"type":"result","permission_denials":[]}`), Scrub{})); got != `{"permission_denials":[],"type":"result"}` {
		t.Errorf("empty list: %s", got)
	}
}

func TestSanitizeDropsLines(t *testing.T) {
	for _, line := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{}}`,
		`{"type":"system","subtype":"ui_invalidate"}`,
		`{"type":"system","subtype":"task_summary"}`,
		`{"type":"system","subtype":"post_turn_summary"}`,
		`{"type":"something_new"}`,
		`{"no_type":1}`,
		`not json at /tmp/w`,
		`null`,
		``,
	} {
		if got := Sanitize([]byte(line), Scrub{Workdir: "/tmp/w", Home: "/Users/me"}); got != nil {
			t.Errorf("%q kept as %s", line, got)
		}
	}
}

func TestSanitizeFiltersNestedKeys(t *testing.T) {
	line := `{"type":"assistant","request_id":"r","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"hi","signature":"x"}],"usage":{"input_tokens":1,"service_tier":"t"}}}`
	got := string(Sanitize([]byte(line), Scrub{Workdir: "/tmp/w", Home: ""}))
	for _, leak := range []string{"request_id", `"id":"m"`, "signature", "service_tier"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	if !strings.Contains(got, `"text":"hi"`) || !strings.Contains(got, `"input_tokens":1`) {
		t.Errorf("kept fields lost: %s", got)
	}
}

func TestSanitizeRealSample(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-real-raw.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if clean := Sanitize(line, Scrub{Workdir: "/private/var/folders/zz/T/agentops-work", Home: "/Users/tester"}); clean != nil {
			var h struct{ Type string }
			if err := json.Unmarshal(clean, &h); err != nil {
				t.Fatal(err)
			}
			switch h.Type {
			case "system", "assistant", "user", "result":
			default:
				t.Errorf("kept line type %q", h.Type)
			}
			out.Write(append(clean, '\n'))
		}
	}
	for _, leak := range []string{"rate_limit", "credential_expires_at", "request_id", "wire_tool_inputs", "tool_use_result",
		"plugins", "example-plugin", "/private/var/folders", "/Users/tester", "startup_timing", "ui_invalidate",
		"credential_expires_at", "task_summary", "post_turn_summary", "messaging_socket_path", `"tools"`} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("sanitized trace contains %q", leak)
		}
	}
	run, err := trace.ParseClaude(&out)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Steps) != 4 {
		t.Fatalf("steps %d, want 4", len(run.Steps))
	}
	var edit *trace.Step
	for i := range run.Steps {
		if run.Steps[i].Tool == "Edit" {
			edit = &run.Steps[i]
		}
	}
	if edit == nil || edit.Summary != "calc.go" {
		t.Errorf("edit step %+v", edit)
	}
	if run.CostUSD <= 0 || run.StopReason != "success" {
		t.Errorf("cost %v stop %q", run.CostUSD, run.StopReason)
	}
}

func TestSanitizeFailsClosedOnUnexpectedShapes(t *testing.T) {
	cases := map[string]string{
		"message not object":    `{"type":"assistant","message":"/Users/me/secret"}`,
		"block not object":      `{"type":"assistant","message":{"content":["/Users/me/secret"]}}`,
		"assistant string":      `{"type":"assistant","message":{"content":"/Users/me/secret"}}`,
		"user content number":   `{"type":"user","message":{"content":42,"role":"user"}}`,
		"nested content number": `{"type":"user","message":{"content":[{"type":"tool_result","content":42}]}}`,
		"nested block string":   `{"type":"user","message":{"content":[{"type":"tool_result","content":["/Users/me/secret"]}]}}`,
		"usage not object":      `{"type":"result","usage":"/Users/me/secret"}`,
		"usage null":            `{"type":"result","usage":null}`,
	}
	for name, line := range cases {
		got := string(Sanitize([]byte(line), Scrub{Workdir: "/tmp/w", Home: ""}))
		if strings.Contains(got, "secret") || strings.Contains(got, "42") || strings.Contains(got, "null") {
			t.Errorf("%s: value survived: %s", name, got)
		}
	}
}

func TestSanitizeKeepsStringToolResult(t *testing.T) {
	got := string(Sanitize([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}`), Scrub{Workdir: "/tmp/w", Home: ""}))
	if !strings.Contains(got, `"content":"ok"`) {
		t.Errorf("got %s", got)
	}
}

func TestSanitizePaths(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	real, _ = filepath.EvalSymlinks(real)
	// The link is the shorter-or-equal spelling here; both must go either way.
	line := `{"type":"result","result":"` + link + `/x.go ` + real + `/y.go /Users/me/proj/z.go"}`
	got := string(Sanitize([]byte(line), Scrub{Workdir: link, Home: "/Users/me"}))
	want := `{"result":"/work/x.go /work/y.go ~/proj/z.go","type":"result"}`
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestSanitizeEmptyHomeDoesNotMangle(t *testing.T) {
	if got := string(Sanitize([]byte(`{"type":"result","result":"b"}`), Scrub{Workdir: "/tmp/w", Home: ""})); got != `{"result":"b","type":"result"}` {
		t.Errorf("got %s", got)
	}
}

func TestPathSpellingsLongestFirst(t *testing.T) {
	got := pathSpellings(t.TempDir())
	for i := 1; i < len(got); i++ {
		if len(got[i]) > len(got[i-1]) {
			t.Errorf("not longest first: %v", got)
		}
	}
}

func TestSanitizeDropsSessionID(t *testing.T) {
	for _, typ := range []string{`"type":"system","subtype":"init"`, `"type":"assistant"`, `"type":"user"`, `"type":"result"`} {
		got := Sanitize([]byte(`{`+typ+`,"session_id":"s-1"}`), Scrub{Workdir: "/tmp/w", Home: "/Users/me"})
		if got == nil || strings.Contains(string(got), "session_id") {
			t.Errorf("%s: %s", typ, got)
		}
	}
}

func TestScrubLoginAndHost(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"-rw-r--r--  1 alice  staff  120 stats.go\nbuilt on mbp-7.example.com (mbp-7) in /tmp/w/x"}]}}`
	got := string(Sanitize([]byte(line), Scrub{Workdir: "/tmp/w", Home: "/Users/alice", User: "alice", Host: "mbp-7.example.com"}))
	for _, bad := range []string{"alice", "mbp", "/tmp/w"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived: %s", bad, got)
		}
	}
	if !strings.Contains(got, "1 user  staff") || !strings.Contains(got, "built on host (host) in /work/x") {
		t.Errorf("unexpected: %s", got)
	}
}

func TestScrubOrderAndWordBoundaries(t *testing.T) {
	s := Scrub{Workdir: "/vf/T/agentops-work-1", TempDir: "/vf/T", Home: "/Users/alice", User: "alice",
		FullName: "Alice Smith", Host: "Alice-MBP.example.com"}
	in := `/vf/T/agentops-work-1/x.go /vf/T/go-build/y /Users/alice/z ALICE alice-mbp.EXAMPLE.com Alice-MBP ` +
		`alice smith aliceX error_alice_turns \nalice\talice "alice" Алиса`
	want := `/work/x.go /tmp/go-build/y ~/z user host host user aliceX error_alice_turns \nuser\tuser "user" Алиса`
	if got := string(scrub([]byte(in), s)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// Non-ASCII names: boundaries are Unicode letters, not \b.
	if got := string(scrub([]byte("Иван Петров, Иван Петровский"), Scrub{FullName: "иван петров"})); got != "user, Иван Петровский" {
		t.Errorf("got %s", got)
	}
}

func TestScrubReplacesClaudeScratchDir(t *testing.T) {
	dir := "/private/tmp/claude-501/-private-var-folders-2v-abc-T-agentops-1/00000000-0000-4000-8000-000000000001/scratchpad"
	cmd := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash",` +
		`"input":{"command":"mkdir -p ` + dir + `; cp stats.go ` + dir + `/stats.go.bak; ls /tmp/claude-0/x\nok"}}]}}`
	prose := `{"type":"result","result":"Original files are backed up in ` + "`" + dir + "`" + `."}`
	for line, want := range map[string]string{
		cmd:   `"command":"mkdir -p /scratch; cp stats.go /scratch; ls /scratch\nok"`,
		prose: `"result":"Original files are backed up in ` + "`/scratch`" + `."`,
	} {
		got := string(Sanitize([]byte(line), Scrub{Workdir: "/tmp/w", TempDir: "/private/var/folders/2v/abc/T"}))
		if !strings.Contains(got, want) || strings.Contains(got, "claude-") {
			t.Errorf("got %s\nwant %s", got, want)
		}
	}
}

func TestScrubScratchDirEdges(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/claude-501/a/b.txt: no such file": "/scratch: no such file",
		"cat /tmp/claude-501/x|grep foo":        "cat /scratch|grep foo",
		"ls /private/tmp/claude-501":            "ls /scratch",
		"cd /tmp/claude-501 && ls":              "cd /scratch && ls",
		"[/tmp/claude-501/x] {/tmp/claude-1/y}": "[/scratch] {/scratch}",
		"<a href=/tmp/claude-501/z>":            "<a href=/scratch>",
		"/tmp/claude-501-foo/bar":               "/tmp/claude-501-foo/bar",
	} {
		if got := string(scrub([]byte(in), Scrub{})); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestScrubFindsNamesNextToEscapes(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t",` +
		`"content":[{"type":"text","text":"owner <alice> host>MSK-BOX1 a&alice \u001b[1malice\u001b[0m \u001balice"}]}]}}`
	got := string(Sanitize([]byte(line), Scrub{User: "alice", Host: "MSK-BOX1.local"}))
	want := `"text":"owner <user> host>host a&user \u001b[1muser\u001b[0m \u001buser"`
	if !strings.Contains(got, want) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// Plain text (test output, stderr) carries the escape byte itself.
	if got := string(scrub([]byte("\x1b[31malice\x1b[0m"), Scrub{User: "alice"})); got != "\x1b[31muser\x1b[0m" {
		t.Errorf("raw ANSI: %q", got)
	}
}

func TestScrubKeepsJSONKeysAndEnums(t *testing.T) {
	line := `{"type":"result","subtype":"error_max_turns","result":"max wrote to /home/max as MAX"}`
	got := string(Sanitize([]byte(line), Scrub{Home: "/home/max", User: "max"}))
	if want := `{"result":"user wrote to ~ as user","subtype":"error_max_turns","type":"result"}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScrubSkipsShortValues(t *testing.T) {
	line := `{"type":"result","result":"a ab abc /w"}`
	got := string(Sanitize([]byte(line), Scrub{Workdir: "", Home: "~", User: "ab", Host: "a.b"}))
	if !strings.Contains(got, `a ab abc /w`) {
		t.Errorf("short values must not be replaced: %s", got)
	}
}

func TestLeakcheck(t *testing.T) {
	u, err := user.Current()
	if err != nil || len(u.Username) < 3 {
		t.Skip("no usable login name")
	}
	leakcheck := func(content string) (int, string) {
		t.Helper()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "trace.jsonl"), []byte(`{"type":"result","result":"/work/x.go ~/y by user on host"}`+"\n"), 0o644)
		os.Mkdir(filepath.Join(dir, "run-01"), 0o755)
		os.WriteFile(filepath.Join(dir, "run-01", "meta.json"), []byte(content), 0o644)
		out, err := exec.Command("../../scripts/leakcheck.sh", dir).CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), string(out)
		}
		return 0, string(out)
	}
	cases := map[string]int{
		`{"stderr":"by ` + strings.ToUpper(u.Username) + `"}`:           1, // a login in any case
		`{"stderr":"x_` + u.Username + `_y"}`:                           0, // inside a word: not the login
		`{"stderr":"saved to /tmp/claude-501/x"}`:                       1,
		`{"stderr":"/private/var/folders/x"}`:                           1,
		`{"stderr":"-private-var-folders-2v-abc-T-agentops-1/scratch"}`: 1,
	}
	if email, _ := exec.Command("git", "config", "user.email").Output(); len(strings.TrimSpace(string(email))) >= 3 {
		cases[`{"stderr":"`+strings.TrimSpace(string(email))+`"}`] = 1
	}
	for content, want := range cases {
		code, out := leakcheck(content)
		switch {
		case code != want:
			t.Errorf("%s: exit %d, want %d: %s", content, code, want, out)
		case want == 0 && out != "утечек не найдено\n":
			t.Errorf("%s: %q", content, out)
		case want == 1 && (!strings.Contains(out, "run-01/meta.json") || strings.Contains(out, "trace.jsonl")):
			t.Errorf("%s: must list only the leaking file: %s", content, out)
		}
	}
}
