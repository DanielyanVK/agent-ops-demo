package runner

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentops/internal/trace"
)

type Options struct {
	RunsDir    string
	TestbedDir string
	Agent      string // "claude" (default) or "codex"
	Model      string // empty for codex = the agent's own default
	Scenario   Scenario
	PluginDir  string // claude plugin with the skill, for scenarios that use skills
}

type meta struct {
	ID        string         `json:"id"`
	Task      string         `json:"task"`
	Agent     string         `json:"agent"`
	Model     string         `json:"model"`
	Scenario  string         `json:"scenario"`
	Prompt    string         `json:"prompt"`
	StartedAt time.Time      `json:"started_at"`
	Stderr    string         `json:"stderr,omitempty"`
	Verdict   *trace.Verdict `json:"verdict,omitempty"`
}

var runDirRe = regexp.MustCompile(`^run-(\d+)$`)

// agentTimeout is a var so a test of a hung agent takes seconds, not ten minutes.
var agentTimeout = 10 * time.Minute

// Run records one run. Any failure before the final meta.json removes the run
// directory: a run left without a verdict would show as running forever.
func Run(ctx context.Context, o Options) (id string, err error) {
	if o.Agent == "" {
		o.Agent = "claude"
	}
	ag, err := checkAgent(o)
	if err != nil {
		return "", err
	}
	if o.Scenario.Skills {
		if o.PluginDir, err = filepath.Abs(o.PluginDir); err != nil {
			return "", err
		}
		if fi, err := os.Stat(o.PluginDir); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("каталог плагина не найден: %s (сценарию %s нужен плагин, см. --plugin)", o.PluginDir, o.Scenario.Name)
		}
	}
	if fi, err := os.Stat(o.TestbedDir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("каталог подопытного проекта не найден: %s", o.TestbedDir)
	}
	workdir, err := os.MkdirTemp("", "agentops-work-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workdir)
	if err := copyTree(o.TestbedDir, workdir); err != nil {
		return "", err
	}
	if o.Agent == "codex" && o.Scenario.Skills {
		if err := installSkills(o.PluginDir, workdir); err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(o.RunsDir, 0o755); err != nil {
		return "", err
	}
	id, err = nextID(o.RunsDir)
	if err != nil {
		return "", err
	}
	runDir := filepath.Join(o.RunsDir, id)
	defer func() {
		if err != nil {
			os.RemoveAll(runDir)
		}
	}()
	m := meta{ID: id, Task: "fix-tests", Agent: o.Agent, Model: cmp.Or(o.Model, "default"), Scenario: o.Scenario.Name,
		Prompt: o.Scenario.Prompt, StartedAt: time.Now().UTC().Truncate(time.Second)}
	if err := writeMeta(runDir, m); err != nil {
		return id, err
	}

	home, _ := os.UserHomeDir()
	s := Scrub{Workdir: workdir, TempDir: filepath.Clean(os.TempDir()), Home: home, PluginDir: o.PluginDir, CodexHome: os.Getenv("CODEX_HOME")}
	if u, err := user.Current(); err == nil {
		s.User, s.FullName = u.Username, u.Name
	}
	s.Host, _ = os.Hostname()
	timedOut, stderr, err := runAgent(ctx, ag, o, s, filepath.Join(runDir, "trace.jsonl"))
	if err != nil {
		return id, err
	}

	m.Verdict, err = verdict(ctx, o.TestbedDir, s)
	if err != nil {
		return id, err
	}
	m.Verdict.Passed = m.Verdict.Passed && !timedOut
	m.Stderr = stderr
	return id, writeMeta(runDir, m)
}

// nextID creates the run directory itself and moves on when the name is taken:
// two concurrent `run` processes may read the same highest number.
func nextID(runsDir string) (string, error) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return "", err
	}
	highest := 0
	for _, e := range entries {
		if sub := runDirRe.FindStringSubmatch(e.Name()); sub != nil {
			n, _ := strconv.Atoi(sub[1])
			highest = max(highest, n)
		}
	}
	for n := highest + 1; ; n++ {
		id := fmt.Sprintf("run-%02d", n)
		if err := os.Mkdir(filepath.Join(runsDir, id), 0o755); !errors.Is(err, fs.ErrExist) {
			return id, err
		}
	}
}

func writeMeta(runDir string, m meta) error {
	return trace.WriteJSON(filepath.Join(runDir, "meta.json"), m)
}

func agentArgs(o Options) []string {
	s := o.Scenario
	// Safe mode would disable skills; --restricted and --strict-mcp-config
	// still keep the user's own plugins, CLAUDE.md and MCP servers out.
	isolation := []string{"--safe-mode", "--strict-mcp-config", "--restricted"}
	if s.Skills {
		isolation = []string{"--plugin-dir", o.PluginDir, "--strict-mcp-config", "--restricted"}
	}
	// --restricted: Read and Bash cannot reach files outside the workdir, whose
	// contents would otherwise end up in the published trace.
	args := append([]string{"-p", s.Prompt}, isolation...)
	args = append(args, "--model", o.Model,
		"--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits",
		"--max-turns", strconv.Itoa(s.MaxTurns), "--tools", strings.Join(s.Tools, ","))
	if len(s.AllowedTools) > 0 {
		args = append(args, append([]string{"--allowedTools"}, s.AllowedTools...)...)
	}
	if len(s.DisallowedTools) > 0 {
		args = append(args, append([]string{"--disallowedTools"}, s.DisallowedTools...)...)
	}
	return args
}

// runAgent returns the parent context's error when the run was interrupted. An
// agent that runs out of time before reporting a result is a result, not an
// error: its trace gets a result line with subtype "timeout". stderr is the scrubbed tail of the
// agent's stderr when it did not exit cleanly: a run that never got going
// explains itself there.
func runAgent(ctx context.Context, ag agent, o Options, s Scrub, tracePath string) (timedOut bool, stderr string, err error) {
	bin := cmp.Or(os.Getenv(ag.binEnv), ag.name)
	args, env, err := ag.prepare(o, s.Workdir)
	if err != nil {
		return false, "", err
	}
	out, err := os.Create(tracePath)
	if err != nil {
		return false, "", err
	}
	defer out.Close()

	actx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	cmd := exec.CommandContext(actx, bin, args...)
	cmd.Dir = s.Workdir
	cmd.Env = agentEnv(env)
	// Its own process group: Ctrl-C reaches agentops only, and a cancel kills the
	// agent together with the tools it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	// A grandchild that left the process group survives the kill and keeps the
	// pipes open: once the context ends, exec closes our ends of stdout and
	// stderr after this delay, which ends the read loop below.
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, "", err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return false, "", fmt.Errorf("не удалось запустить агента %q: %w", bin, err)
	}

	// Written line by line, unbuffered, so a viewer sees a run in progress.
	var copyErr error
	sawResult := false
	r := bufio.NewReader(stdout)
	for {
		line, rerr := r.ReadBytes('\n')
		if line = bytes.TrimRight(line, "\r\n"); len(line) > 0 {
			if clean := ag.sanitize(line, s, time.Now()); clean != nil {
				if _, copyErr = out.Write(append(clean, '\n')); copyErr != nil {
					break
				}
				sawResult = sawResult || ag.finished(clean)
			}
		}
		if rerr != nil {
			if rerr != io.EOF && actx.Err() == nil {
				copyErr = rerr
			}
			break
		}
	}
	if copyErr != nil {
		cancel() // a stalled reader would block the agent on a full pipe
	}
	// A non-zero exit of the agent is a result like any other, not a runner error.
	if cmd.Wait() != nil {
		stderr = tail(string(scrub(errBuf.Bytes(), s)), 500)
	}

	switch {
	case ctx.Err() != nil:
		return false, "", ctx.Err()
	case copyErr != nil:
		return false, "", copyErr
	// An agent that reported its result and then lingered keeps its own result.
	case errors.Is(actx.Err(), context.DeadlineExceeded) && !sawResult:
		_, err := fmt.Fprintln(out, ag.timeoutLine(time.Since(start), time.Now()))
		return true, stderr, err
	}
	return false, stderr, nil
}

// tail keeps the last n bytes without starting mid-rune.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[len(s)-n:], "")
}

// verdictTimeout is a var so a test of a hung test binary takes seconds, not a minute.
var verdictTimeout = 60 * time.Second

var testFuncRe = regexp.MustCompile(`(?m)^func (Test\w*)\(\w+ \*testing\.T\)`)

// verdict runs the original tests against the agent's versions of the original
// source files only, in a fresh tree: files the agent added, its changes to the
// tests and to the module settings are ignored. An overlaid file that does not
// parse, adds an init() or adds an import outside pureImports is rejected without
// running anything; that removes the known ways to end the test process early or
// to reach the OS. Every original test must then report a pass event.
//
// ponytail: what remains. (1) Code that special-cases the test inputs passes: a
// limit of any test-based verdict. (2) The allow-list fits this testbed and is
// too strict for arbitrary projects; the upgrade path is a sandboxed judge whose
// process the agent's code cannot reach. (3) The agent and the code it writes run
// unsandboxed as the current user: record runs in a container or VM when the
// task is untrusted.
func verdict(ctx context.Context, testbed string, s Scrub) (*trace.Verdict, error) {
	dir, err := os.MkdirTemp("", "agentops-verdict-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := copyTree(testbed, dir); err != nil {
		return nil, err
	}
	var want, rejected []string
	err = filepath.WalkDir(testbed, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		orig, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, "_test.go") {
			for _, m := range testFuncRe.FindAllSubmatch(orig, -1) {
				want = append(want, string(m[1]))
			}
			return nil
		}
		rel, _ := filepath.Rel(testbed, p)
		agent := filepath.Join(s.Workdir, rel)
		if fi, err := os.Stat(agent); err != nil || !fi.Mode().IsRegular() {
			return nil // deleted or replaced by the agent: the original stays
		}
		b, err := os.ReadFile(agent)
		if err != nil {
			return err
		}
		rejected = append(rejected, policy(filepath.ToSlash(rel), orig, b)...)
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if err != nil {
		return nil, err
	}
	switch {
	case len(rejected) > 0:
		return &trace.Verdict{TestOutput: scrubbed("вердикт отклонён: "+strings.Join(rejected, "\nвердикт отклонён: ")+"\n", s)}, nil
	case len(want) == 0:
		return &trace.Verdict{TestOutput: "в исходных тестах не найдено ни одного теста"}, nil
	}

	tctx, cancel := context.WithTimeout(ctx, verdictTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "go", "test", "-count=1", "-timeout", "50s", "-json", "./...")
	cmd.Dir = dir
	// A looping test binary is a grandchild: only killing the whole group stops it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, runErr := cmd.CombinedOutput()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	text, passed := testText(out)
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		text += fmt.Sprintf("\nтесты остановлены: не уложились в %s\n", trace.FmtDuration(verdictTimeout))
	}
	var missing []string
	for _, name := range want {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	if runErr == nil && len(missing) > 0 {
		text += "нет отметки о прохождении: " + strings.Join(missing, ", ") + "\n"
	}
	vs := s
	vs.Workdir = dir
	return &trace.Verdict{Passed: runErr == nil && len(missing) == 0, TestOutput: scrubbed(text, vs)}, nil
}

func scrubbed(text string, s Scrub) string { return tail(string(scrub([]byte(text), s)), 2000) }

// pureImports are the only packages an agent may add to a source file: pure
// computation. A ban-list loses: flag alone ends the test binary with exit 0
// (-h under ExitOnError), fmt prints forged "--- PASS" lines.
var pureImports = map[string]bool{"bytes": true, "cmp": true, "errors": true, "maps": true, "math": true,
	"math/bits": true, "slices": true, "sort": true, "strconv": true, "strings": true, "unicode": true,
	"unicode/utf8": true, "unicode/utf16": true}

// policy lists why the agent's version of a source file may not be judged: it
// does not parse, adds an init() that runs before the tests, or adds an import
// that is not pure computation. Imports the original already has stay allowed.
func policy(name string, orig, agent []byte) []string {
	a, err := parser.ParseFile(token.NewFileSet(), name, agent, parser.SkipObjectResolution)
	if err != nil {
		return []string{name + " не разбирается как Go-код"}
	}
	o, err := parser.ParseFile(token.NewFileSet(), name, orig, parser.SkipObjectResolution)
	if err != nil {
		o = &ast.File{} // the testbed's own file does not build either; the tests will say so
	}
	had := map[string]bool{}
	for _, im := range o.Imports {
		path, _ := strconv.Unquote(im.Path.Value)
		had[path] = true
	}
	var out []string
	for _, im := range a.Imports {
		path, _ := strconv.Unquote(im.Path.Value)
		if !had[path] && !pureImports[path] {
			out = append(out, name+" добавляет импорт "+path+" (новые импорты разрешены только из набора чистых пакетов)")
			had[path] = true
		}
	}
	if inits(a) > inits(o) {
		out = append(out, name+" добавляет init()")
	}
	return out
}

func inits(f *ast.File) int {
	n := 0
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "init" {
			n++
		}
	}
	return n
}

// testText rebuilds plain `go test` output from `go test -json`: the -v framing
// (=== RUN, passing tests) is dropped, and a failed test's messages, which -json
// streams before its "--- FAIL" line, are printed after it, with failed subtests
// nested under their parent. Lines that are not events (go's own errors, such
// as a broken go.mod) are kept as they are.
func testText(out []byte) (string, map[string]bool) {
	type test struct{ frame, body string }
	var text strings.Builder
	passed := map[string]bool{}
	tests := map[string]*test{}
	var order []string
	for _, line := range strings.SplitAfter(string(out), "\n") {
		var ev struct{ Action, Test, Output string }
		if json.Unmarshal([]byte(line), &ev) != nil {
			text.WriteString(line)
			continue
		}
		if ev.Test == "" {
			text.WriteString(ev.Output)
			continue
		}
		t := tests[ev.Test]
		if t == nil {
			t = &test{}
			tests[ev.Test] = t
			order = append(order, ev.Test)
		}
		switch ev.Action {
		case "output":
			switch {
			case strings.HasPrefix(ev.Output, "=== "):
			case strings.HasPrefix(ev.Output, "--- "):
				t.frame = ev.Output
			default:
				t.body += ev.Output
			}
		case "pass", "skip":
			passed[ev.Test] = ev.Action == "pass"
			delete(tests, ev.Test)
		case "fail":
			delete(tests, ev.Test)
			block := t.frame + t.body
			i := strings.LastIndex(ev.Test, "/")
			if parent := tests[ev.Test[:max(i, 0)]]; i > 0 && parent != nil {
				parent.body += "    " + strings.ReplaceAll(strings.TrimSuffix(block, "\n"), "\n", "\n    ") + "\n"
			} else {
				text.WriteString(block)
			}
		}
	}
	for _, name := range order { // tests cut off by a kill still show what they printed
		if t := tests[name]; t != nil {
			text.WriteString(t.frame + t.body)
		}
	}
	return text.String(), passed
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
