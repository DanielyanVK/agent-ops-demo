package trace

type RunBrief struct {
	ID         string      `json:"id" doc:"Run id, the directory name under runs/"`
	Status     string      `json:"status" doc:"Verdict of the original tests, never the agent's own claim" enum:"passed,failed,running"`
	Task       string      `json:"task" doc:"Name of the task the agent was given"`
	Agent      string      `json:"agent" doc:"Agent that made the run"`
	Model      string      `json:"model" doc:"Model the agent ran on"`
	Scenario   string      `json:"scenario" doc:"Recording scenario, e.g. baseline or no-edit"`
	Steps      int         `json:"steps" doc:"Number of tool calls in the run"`
	DurationMS int64       `json:"duration_ms" doc:"Run duration in milliseconds"`
	CostUSD    float64     `json:"cost_usd" doc:"What the agent run cost, USD"`
	StopReason string      `json:"stop_reason" doc:"How the agent stopped: success, error_max_turns, timeout, incomplete"`
	Postmortem *Postmortem `json:"postmortem" doc:"Diagnosis of a failed run; null until analyze has run"` // null when there is none
}

type StepBrief struct {
	N          int    `json:"n" doc:"Step number, starting at 1"`
	Tool       string `json:"tool" doc:"Tool the agent called: Read, Edit, Bash, ..."`
	Summary    string `json:"summary" doc:"One-line summary of the call"`
	Error      bool   `json:"error" doc:"The tool result was an error"`
	Denied     bool   `json:"denied" doc:"The call was blocked by permissions; error is also true"`
	DurationMS int64  `json:"duration_ms" doc:"Step duration in milliseconds"`
}

type StatsJSON struct {
	Total             int     `json:"total" doc:"Number of runs"`
	Passed            int     `json:"passed" doc:"Runs whose original tests passed"`
	Failed            int     `json:"failed" doc:"Runs whose original tests failed"`
	Running           int     `json:"running" doc:"Runs without a verdict yet"`
	CostUSD           float64 `json:"cost_usd" doc:"Total cost of the agent runs, USD"`
	PostmortemCostUSD float64 `json:"postmortem_cost_usd" doc:"Total cost of model diagnoses, USD"`
	AvgDurationMS     int64   `json:"avg_duration_ms" doc:"Average run duration in milliseconds"`
}

func (s Status) String() string {
	switch s {
	case Passed:
		return "passed"
	case Failed:
		return "failed"
	}
	return "running"
}

func (r Run) Brief() RunBrief {
	return RunBrief{
		ID: r.ID, Status: r.Status().String(), Task: r.Task, Agent: r.Agent,
		Model: r.Model, Scenario: r.Scenario, Steps: len(r.Steps),
		DurationMS: r.Duration.Milliseconds(), CostUSD: r.CostUSD,
		StopReason: r.StopReason, Postmortem: r.Postmortem,
	}
}

func (s Step) Brief() StepBrief {
	return StepBrief{N: s.N, Tool: s.Tool, Summary: s.Summary, Error: s.IsError, Denied: s.Denied, DurationMS: s.Duration.Milliseconds()}
}

func (s Stats) JSON() StatsJSON {
	return StatsJSON{
		Total: s.Total, Passed: s.Passed, Failed: s.Failed, Running: s.Running,
		CostUSD: s.CostUSD, PostmortemCostUSD: s.PostmortemCostUSD, AvgDurationMS: s.AvgDuration.Milliseconds(),
	}
}
