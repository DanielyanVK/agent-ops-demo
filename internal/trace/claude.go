package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type line struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	IsError      bool    `json:"is_error"`
	DurationMS   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	PermissionDenials []struct {
		ToolUseID string `json:"tool_use_id"`
	} `json:"permission_denials"`
}

type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Text      string          `json:"text"`
}

// ParseClaude reads Claude Code `--output-format stream-json`. The meta fields of
// Run are left empty: they come from meta.json, not from the trace.
func ParseClaude(r io.Reader) (Run, error) {
	run := Run{StopReason: "incomplete"}
	index := map[string]int{}         // tool_use id -> position in run.Steps
	started := map[string]time.Time{} // tool_use id -> assistant line timestamp

	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		raw, err := br.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return run, err
		}
		if raw = bytes.TrimSpace(raw); len(raw) > 0 {
			var l line
			if jerr := json.Unmarshal(raw, &l); jerr != nil {
				if err == io.EOF {
					// The writer may be mid-line on a live run; the run is just incomplete.
					finish(&run)
					return run, nil
				}
				return run, fmt.Errorf("строка %d: %w", n, jerr)
			}
			apply(&run, &l, index, started)
		}
		if err == io.EOF {
			finish(&run)
			return run, nil
		}
	}
}

// finish runs without a result line too: a cut-off run still shows its blocked tools.
func finish(run *Run) {
	// A tool disabled for the session fails in-band and is absent from permission_denials.
	for i := range run.Steps {
		s := &run.Steps[i]
		if s.IsError && strings.Contains(s.Output, "No such tool available") {
			s.Denied = true
		}
	}
	run.collect()
}

func apply(run *Run, l *line, index map[string]int, started map[string]time.Time) {
	ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			run.Model = l.Model
		}
	case "assistant":
		for _, b := range blocks(l.Message.Content) {
			if b.Type != "tool_use" {
				continue
			}
			s := Step{N: len(run.Steps) + 1, Tool: b.Name, Line: stepLine(b.Name, b.Input)}
			s.Kind, s.Name = claudeKind(b.Name, b.Input)
			if s.Line == "" {
				s.Line = s.Name
			}
			s.Summary = cut60(s.Line)
			var buf bytes.Buffer
			if json.Indent(&buf, b.Input, "", "  ") == nil {
				s.Input = buf.String()
			}
			index[b.ID] = len(run.Steps)
			started[b.ID] = ts
			run.Steps = append(run.Steps, s)
		}
	case "user":
		for _, b := range blocks(l.Message.Content) {
			i, ok := index[b.ToolUseID]
			if b.Type != "tool_result" || !ok {
				continue
			}
			s := &run.Steps[i]
			s.Output = flatten(b.Content)
			s.IsError = b.IsError
			if t := started[b.ToolUseID]; !t.IsZero() && !ts.IsZero() {
				s.Duration = ts.Sub(t)
			}
		}
	case "result":
		run.Duration = time.Duration(l.DurationMS) * time.Millisecond
		run.CostUSD = l.TotalCostUSD
		run.Turns = l.NumTurns
		run.Result = l.Result
		run.ResultError = l.IsError
		run.StopReason = l.Subtype
		u := l.Usage
		run.TokensIn = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		run.TokensOut = u.OutputTokens
		for _, d := range l.PermissionDenials {
			if i, ok := index[d.ToolUseID]; ok {
				run.Steps[i].Denied = true
			}
		}
	}
}

// blocks tolerates content that is a plain string (a user prompt): it has no blocks.
func blocks(raw json.RawMessage) []block {
	var bs []block
	_ = json.Unmarshal(raw, &bs)
	return bs
}

func flatten(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// claudeKind classifies a tool call and names the skill, MCP tool or subagent it uses.
func claudeKind(tool string, input json.RawMessage) (kind, name string) {
	var in struct {
		Skill        string `json:"skill"`
		SubagentType string `json:"subagent_type"`
	}
	_ = json.Unmarshal(input, &in)
	switch tool {
	case "Bash":
		return "shell", ""
	case "Read":
		return "read", ""
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "edit", ""
	case "Glob", "Grep":
		return "search", ""
	case "Skill":
		return "skill", in.Skill
	case "Task", "Agent":
		return "subagent", in.SubagentType
	case "WebSearch", "WebFetch":
		return "web", ""
	}
	if rest, ok := strings.CutPrefix(tool, "mcp__"); ok {
		server, t, _ := strings.Cut(rest, "__")
		return "mcp", server + "." + t
	}
	return "other", ""
}

// cut60 is the summary cut: 60 runes, the last one an ellipsis when something was dropped.
func cut60(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return s
}

// stepLine is the one-line step text before the summary cut.
func stepLine(tool string, input json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
		Pattern  string `json:"pattern"`
		Query    string `json:"query"`
		URL      string `json:"url"`
	}
	_ = json.Unmarshal(input, &in)
	var s string
	switch tool {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		s = strings.TrimPrefix(in.FilePath, "/work/")
	case "Bash":
		s, _, _ = strings.Cut(in.Command, "\n")
	case "Grep", "Glob":
		s = in.Pattern
	case "WebSearch":
		s = in.Query
	case "WebFetch":
		s = in.URL
	}
	return s
}
