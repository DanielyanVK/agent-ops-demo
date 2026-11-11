package trace

import (
	"slices"
	"strings"
	"time"
)

type Run struct {
	ID        string
	Task      string
	Agent     string
	Model     string
	Scenario  string
	Prompt    string // task text given to the agent
	Stderr    string // tail of the agent's stderr when it exited non-zero
	StartedAt time.Time

	Duration    time.Duration // result.duration_ms
	CostUSD     float64       // result.total_cost_usd; 0 for Codex, which reports tokens, not money
	TokensIn    int           // all input tokens, cached ones included
	TokensOut   int
	Turns       int // result.num_turns
	Steps       []Step
	Result      string      // result.result: the agent's final message
	ResultError bool        // result.is_error
	StopReason  string      // result.subtype; "incomplete" when the trace has no result line
	Denials     []string    // unique tool names of denied steps, in order of first appearance
	Skills      []string    // unique skill names the agent loaded, in order of first appearance
	MCP         []ToolCount // MCP tool calls per server.tool
	Subagents   int
	WebCalls    int

	Verdict    *Verdict
	Postmortem *Postmortem
}

type Step struct {
	N        int    // 1-based
	Tool     string // tool_use name: Read, Edit, Bash, …
	Kind     string // shell, read, edit, search, skill, mcp, subagent, web or other: the same for both agents
	Name     string // the skill, "server.tool" of an MCP call, or the subagent type; Codex skill steps join several skills with ", "
	Summary  string // one line, see "Step summary"
	Line     string // Summary before the 60-rune cut; TUI only, not in JSON output
	Input    string // tool input as indented JSON
	Output   string // tool_result content flattened to text
	IsError  bool   // tool_result.is_error
	Denied   bool   // blocked: listed in result.permission_denials, or the tool is disabled for the session
	Duration time.Duration
}

type Verdict struct {
	Passed     bool   `json:"passed" doc:"The original tests passed"`
	TestOutput string `json:"test_output" doc:"Last 2000 bytes of the test run output"`
}

type Postmortem struct {
	FailedStep int     `json:"failed_step" doc:"Step whose action caused the failure; 0 = not one step"`
	Cause      string  `json:"cause" doc:"Why the run failed"`
	Fix        string  `json:"fix" doc:"What to change so the run passes"`
	Source     string  `json:"source" doc:"Who made the diagnosis" enum:"rule,model"`
	CostUSD    float64 `json:"cost_usd" doc:"Cost of the diagnosis, USD; 0 for rules"`
}

type Status int

const (
	Running Status = iota // no verdict yet
	Passed
	Failed
)

// Status looks only at the verdict: what the agent claims in Result is not evidence.
func (r Run) Status() Status {
	switch {
	case r.Verdict == nil:
		return Running
	case r.Verdict.Passed:
		return Passed
	}
	return Failed
}

type ToolCount struct {
	Tool string `json:"tool" doc:"Tool name"`
	N    int    `json:"n" doc:"Number of calls"`
}

// Blocked counts denied steps per tool, in order of first appearance.
func (r Run) Blocked() []ToolCount {
	var out []ToolCount
next:
	for _, s := range r.Steps {
		if !s.Denied {
			continue
		}
		for i := range out {
			if out[i].Tool == s.Tool {
				out[i].N++
				continue next
			}
		}
		out = append(out, ToolCount{s.Tool, 1})
	}
	return out
}

// collect fills what the run used from its steps, for both agents.
func (r *Run) collect() {
	for _, b := range r.Blocked() {
		r.Denials = append(r.Denials, b.Tool)
	}
next:
	for _, s := range r.Steps {
		switch s.Kind {
		case "skill":
			for name := range strings.SplitSeq(s.Name, ", ") {
				if name != "" && !slices.Contains(r.Skills, name) {
					r.Skills = append(r.Skills, name)
				}
			}
		case "mcp":
			for i := range r.MCP {
				if r.MCP[i].Tool == s.Name {
					r.MCP[i].N++
					continue next
				}
			}
			r.MCP = append(r.MCP, ToolCount{s.Name, 1})
		case "subagent":
			r.Subagents++
		case "web":
			r.WebCalls++
		}
	}
}
