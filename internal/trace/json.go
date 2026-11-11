package trace

type RunBrief struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	Task       string      `json:"task"`
	Agent      string      `json:"agent"`
	Model      string      `json:"model"`
	Scenario   string      `json:"scenario"`
	Steps      int         `json:"steps"`
	DurationMS int64       `json:"duration_ms"`
	CostUSD    float64     `json:"cost_usd"`
	StopReason string      `json:"stop_reason"`
	Postmortem *Postmortem `json:"postmortem"` // null when there is none
}

type StepBrief struct {
	N          int    `json:"n"`
	Tool       string `json:"tool"`
	Summary    string `json:"summary"`
	Error      bool   `json:"error"`
	Denied     bool   `json:"denied"`
	DurationMS int64  `json:"duration_ms"`
}

type StatsJSON struct {
	Total             int     `json:"total"`
	Passed            int     `json:"passed"`
	Failed            int     `json:"failed"`
	Running           int     `json:"running"`
	CostUSD           float64 `json:"cost_usd"`
	PostmortemCostUSD float64 `json:"postmortem_cost_usd"`
	AvgDurationMS     int64   `json:"avg_duration_ms"`
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
