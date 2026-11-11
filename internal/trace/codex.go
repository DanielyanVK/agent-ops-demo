package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

type codexLine struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
	Item struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Status           string `json:"status"`
		Changes          []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		Server  string `json:"server"`
		Tool    string `json:"tool"`
		Query   string `json:"query"`
		Message string `json:"message"`
	} `json:"item"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

var (
	shellWrapper = regexp.MustCompile(`^/bin/(?:zsh|bash|sh) -lc +`)
	skillPath    = regexp.MustCompile(`/skills/(?:[^/\s'"]+/)*?([^/\s'"]+)/SKILL\.md`)
	segmentSplit = regexp.MustCompile(`[;|&\n]`)
	// quoteIdiom is how a shell script with single quotes inside double quotes
	// is written: '"'text'"' stands for 'text'.
	quoteIdiom = regexp.MustCompile(`'"'([^'"]*)'"'`)
)

// dqEscapes are the backslash escapes the shell removes inside double quotes.
var dqEscapes = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, "$", "\\`", "`")

var skillReaders = []string{"cat", "sed", "head", "tail", "less", "bat", "nl", "awk", "rg", "grep"}

// words splits a command segment and drops what runs the real command: a
// leading command/env/time, variable assignments and the directory of the program.
func words(seg string) []string {
	f := strings.Fields(seg)
	for len(f) > 0 && (f[0] == "command" || f[0] == "env" || f[0] == "time" || strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-")) {
		f = f[1:]
	}
	if len(f) > 0 {
		f[0] = path.Base(f[0])
	}
	return f
}

// SkillReads returns the names of the skills a Codex shell command reads: a
// segment of the command (split on & && | || ; and newlines) must start with a
// reader and name a path .../skills/<name>/SKILL.md that is a file argument:
// not a redirect target, not the search pattern of grep or rg. Writes, listings,
// globs and tests of such a path are not reads. The parser uses it to tell a
// skill step from a shell step.
func SkillReads(command string) []string {
	var names []string
	for _, seg := range segmentSplit.Split(unwrapShell(command), -1) {
		f := words(seg)
		if len(f) == 0 || !slices.Contains(skillReaders, f[0]) {
			continue
		}
		for i, tok := range f {
			if strings.Contains(tok, ">") || i > 0 && strings.HasSuffix(f[i-1], ">") {
				continue
			}
			for _, m := range skillPath.FindAllStringSubmatch(tok, -1) {
				if strings.ContainsAny(m[1], "*?[<$") || slices.Contains(names, m[1]) || searchPattern(f, i) {
					continue
				}
				names = append(names, m[1])
			}
		}
	}
	return names
}

// searchPattern: for grep and rg the first non-option argument is the pattern,
// so a skill path followed by another argument is a pattern, not a file.
func searchPattern(f []string, i int) bool {
	if f[0] != "grep" && f[0] != "rg" {
		return false
	}
	for _, t := range f[i+1:] {
		if !strings.HasPrefix(t, "-") {
			return true
		}
	}
	return false
}

// cannotPrint are the commands that cannot show the contents of a file.
var cannotPrint = []string{"ls", "find", "test", "[", "echo", "rm", "stat", "printf", "wc"}

// MentionsSkillText reports whether the output of a command may contain the
// text of a skill file. It fails closed: any command that names SKILL.md is
// suspect unless each segment naming it starts with a command that cannot print
// file contents. Indirection (xargs, command substitution, -exec) is suspect too.
func MentionsSkillText(command string) bool {
	if !strings.Contains(command, "SKILL.md") {
		return false
	}
	script := unwrapShell(command)
	if strings.Contains(script, "$(") || strings.Contains(script, "`") {
		return true
	}
	for _, seg := range segmentSplit.Split(script, -1) {
		f := words(seg)
		if len(f) > 0 && (f[0] == "xargs" || slices.Contains(f, "-exec")) {
			return true
		}
		if strings.Contains(seg, "SKILL.md") && (len(f) == 0 || !slices.Contains(cannotPrint, f[0])) {
			return true
		}
	}
	return false
}

// ParseCodex reads `codex exec --json` lines as the runner records them (with
// a "ts" added). The meta fields of Run come from meta.json, not from the trace.
func ParseCodex(r io.Reader) (Run, error) {
	run := Run{StopReason: "incomplete"}
	started := map[string]time.Time{} // item id -> item.started ts
	var first, last time.Time
	var lastMessage, failure string

	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		raw, err := br.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return run, err
		}
		if raw = bytes.TrimSpace(raw); len(raw) > 0 {
			var l codexLine
			if jerr := json.Unmarshal(raw, &l); jerr != nil {
				if json.Valid(raw) {
					// A line of a kept type with a value of the wrong shape: skip it, keep the trace.
				} else if err != io.EOF {
					return run, fmt.Errorf("строка %d: %w", n, jerr)
				}
				// else: the writer may be mid-line on a live run; the run is just incomplete.
			} else {
				ts, _ := time.Parse(time.RFC3339Nano, l.TS)
				if !ts.IsZero() {
					if first.IsZero() {
						first = ts
					}
					last = ts
				}
				switch l.Type {
				case "item.started":
					started[l.Item.ID] = ts
				case "item.completed":
					if l.Item.Type == "agent_message" {
						lastMessage = l.Item.Text
					} else if s, ok := codexStep(&l); ok {
						s.N = len(run.Steps) + 1
						if t := started[l.Item.ID]; !t.IsZero() && !ts.IsZero() {
							s.Duration = ts.Sub(t)
						}
						run.Steps = append(run.Steps, s)
					}
				case "turn.completed":
					run.Turns++
					run.TokensIn, run.TokensOut = l.Usage.InputTokens, l.Usage.OutputTokens
					run.StopReason = "success"
				case "turn.failed":
					run.StopReason, run.ResultError, failure = "error_during_execution", true, l.Error.Message
				case "timeout": // written by the runner: Codex reports nothing when it is killed
					run.StopReason, run.ResultError = "timeout", true
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	run.Result = lastMessage
	if run.Result == "" {
		run.Result = failure
	}
	if !first.IsZero() {
		run.Duration = last.Sub(first)
	}
	run.collect()
	return run, nil
}

// codexStep turns a completed item into a step; other items are not steps.
func codexStep(l *codexLine) (Step, bool) {
	it := l.Item
	var s Step
	switch it.Type {
	case "command_execution":
		s.Tool, s.Kind = "Shell", "shell"
		s.Line, _, _ = strings.Cut(unwrapShell(it.Command), "\n")
		s.Input, s.Output = it.Command, it.AggregatedOutput
		s.IsError = it.ExitCode != nil && *it.ExitCode != 0 || it.ExitCode == nil && it.Status == "failed"
		if names := SkillReads(it.Command); len(names) > 0 {
			s.Kind, s.Name = "skill", strings.Join(names, ", ")
		}
	case "file_change":
		s.Tool, s.Kind = "Edit", "edit"
		paths := make([]string, len(it.Changes))
		for i, c := range it.Changes {
			paths[i] = strings.TrimPrefix(c.Path, "/work/")
		}
		s.Line = strings.Join(paths, ", ")
		if b, err := json.MarshalIndent(it.Changes, "", "  "); err == nil {
			s.Input = string(b)
		}
		s.IsError = it.Status != "completed"
		s.Denied = s.IsError // the item has no output to tell a refusal from another failure
	case "mcp_tool_call":
		s.Tool, s.Kind, s.Name = "MCP", "mcp", it.Server+"."+it.Tool
		s.Line, s.Input = s.Name, s.Name
		s.IsError = it.Status == "failed"
	case "web_search":
		s.Tool, s.Kind = "WebSearch", "web"
		s.Line, s.Input = it.Query, it.Query
	default:
		return s, false
	}
	s.Summary = cut60(s.Line)
	low := strings.ToLower(s.Output)
	s.Denied = s.Denied || s.IsError && (strings.Contains(low, "operation not permitted") || strings.Contains(low, "read-only file system"))
	return s, true
}

// unwrapShell drops the `/bin/zsh -lc` wrapper Codex puts around every command
// and the one pair of quotes around the script. A script that does not have
// the expected shape is left as it is rather than guessed at.
func unwrapShell(cmd string) string {
	rest := shellWrapper.ReplaceAllString(cmd, "")
	if rest == cmd || len(rest) < 2 {
		return cmd
	}
	q := rest[0]
	if (q != '"' && q != '\'') || rest[len(rest)-1] != q {
		return rest
	}
	inner := rest[1 : len(rest)-1]
	if q == '"' {
		inner = quoteIdiom.ReplaceAllString(inner, "'$1'")
		if strings.Contains(strings.ReplaceAll(inner, `\"`, ""), `"`) {
			return rest // not one plain double-quoted string
		}
		inner = dqEscapes.Replace(inner)
	} else if strings.Contains(inner, "'") {
		return rest
	}
	return inner
}
