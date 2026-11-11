package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	shellWrapper = regexp.MustCompile(`^/bin/(?:zsh|bash|sh) -lc `)
	skillPath    = regexp.MustCompile(`/skills/([^/\s'"]+)/SKILL\.md`)
	segmentSplit = regexp.MustCompile(`&&|\|\||;|\|`)
	// quoteIdiom is how a shell script with single quotes inside double quotes
	// is written: '"'text'"' stands for 'text'.
	quoteIdiom = regexp.MustCompile(`'"'([^'"]*)'"'`)
)

var skillReaders = []string{"cat", "sed", "head", "tail", "less", "bat", "nl", "awk", "rg", "grep"}

// SkillReads returns the names of the skills a Codex shell command reads: a
// segment of the command (split on && || ; |) must start with a reader and
// name a path .../skills/<name>/SKILL.md that is not a redirect target. Writes,
// listings, globs and tests of such a path are not reads. The sanitizer and the
// parser both use it.
func SkillReads(command string) []string {
	var names []string
	for _, seg := range segmentSplit.Split(unwrapShell(command), -1) {
		f := strings.Fields(seg)
		if len(f) == 0 || !slices.Contains(skillReaders, f[0]) {
			continue
		}
		for _, m := range skillPath.FindAllStringSubmatchIndex(seg, -1) {
			name := seg[m[2]:m[3]]
			if strings.ContainsAny(name, "*?[<$") || redirected(seg[:m[0]]) || slices.Contains(names, name) {
				continue
			}
			names = append(names, name)
		}
	}
	return names
}

// redirected: the path token that ends the prefix is the target of a > or >> redirect.
func redirected(prefix string) bool {
	start := strings.LastIndexAny(prefix, " \t") + 1
	return strings.Contains(prefix[start:], ">") || strings.HasSuffix(strings.TrimRight(prefix[:start], " \t"), ">")
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
	} else if strings.Contains(inner, "'") {
		return rest
	}
	return inner
}
