package trace

import "time"

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
	CostUSD     float64       // result.total_cost_usd
	Turns       int           // result.num_turns
	Steps       []Step
	Result      string   // result.result: the agent's final message
	ResultError bool     // result.is_error
	StopReason  string   // result.subtype; "incomplete" when the trace has no result line
	Denials     []string // unique tool names of denied steps, in order of first appearance

	Verdict    *Verdict
	Postmortem *Postmortem
}

type Step struct {
	N        int    // 1-based
	Tool     string // tool_use name: Read, Edit, Bash, …
	Summary  string // one line, see "Step summary"
	Line     string // Summary before the 60-rune cut; TUI only, not in JSON output
	Input    string // tool input as indented JSON
	Output   string // tool_result content flattened to text
	IsError  bool   // tool_result.is_error
	Denied   bool   // blocked: listed in result.permission_denials, or the tool is disabled for the session
	Duration time.Duration
}

type Verdict struct {
	Passed     bool   `json:"passed"`
	TestOutput string `json:"test_output"`
}

type Postmortem struct {
	FailedStep int     `json:"failed_step"`
	Cause      string  `json:"cause"`
	Fix        string  `json:"fix"`
	Source     string  `json:"source"`   // "rule" or "model"
	CostUSD    float64 `json:"cost_usd"` // 0 for rules
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
	Tool string
	N    int
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
