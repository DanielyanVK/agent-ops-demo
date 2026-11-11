package main

import "agentops/internal/trace"

// The JSON output of every command is one of these types. Their `doc` and `enum`
// tags are the source of schema.json (see schema.go); do not edit that file by hand.

type lsOutput struct {
	Stats trace.StatsJSON  `json:"stats" doc:"Summary over all runs, not only the listed ones"`
	Runs  []trace.RunBrief `json:"runs" doc:"Listed runs; only failed ones with --failed"`
}

type showOutput struct {
	trace.RunBrief
	Denials []string          `json:"denials" doc:"Tools blocked by permissions, in order of first appearance"`
	Verdict *trace.Verdict    `json:"verdict" doc:"Result of the original tests; null while the run is going"`
	Steps   []trace.StepBrief `json:"steps" doc:"Tool calls of the run, without their text"`
}

type stepDetail struct {
	trace.StepBrief
	Input  string `json:"input" doc:"Tool input as indented JSON; cut to --max-bytes"`
	Output string `json:"output" doc:"Tool result as text; cut to --max-bytes"`
}

type showStepOutput struct {
	ID   string     `json:"id" doc:"Run id"`
	Step stepDetail `json:"step" doc:"The requested step with its text"`
}

type whyOutput struct {
	ID         string            `json:"id" doc:"Run id"`
	Status     string            `json:"status" doc:"Verdict of the original tests" enum:"passed,failed,running"`
	Postmortem *trace.Postmortem `json:"postmortem" doc:"Diagnosis of a failed run; null until analyze has run"`
}

type checkResult struct {
	Finished int  `json:"finished" doc:"Runs with a verdict (passed or failed)"`
	Passed   int  `json:"passed" doc:"Runs whose original tests passed"`
	Percent  int  `json:"percent" doc:"Passed share of finished runs, integer percent"`
	Min      int  `json:"min" doc:"Threshold in percent (--min)"`
	OK       bool `json:"ok" doc:"percent is at least min; false also makes the exit code 1"`
}

type runOutput struct {
	ID       string `json:"id" doc:"Id of the recorded run"`
	Status   string `json:"status" doc:"Verdict of the original tests" enum:"passed,failed,running"`
	Scenario string `json:"scenario" doc:"Scenario the run was recorded with"`
	Model    string `json:"model" doc:"Model the agent ran on"`
}

// analyzeOutput is one line per diagnosed run: error on failure, source and cost on success.
type analyzeOutput struct {
	ID      string   `json:"id" doc:"Run id"`
	OK      bool     `json:"ok" doc:"The diagnosis was made and saved"`
	Error   string   `json:"error,omitempty" doc:"Why the diagnosis failed; only when ok is false"`
	Source  *string  `json:"source,omitempty" doc:"Who made the diagnosis; only when ok is true" enum:"rule,model"`
	CostUSD *float64 `json:"cost_usd,omitempty" doc:"Cost of the diagnosis, USD; only when ok is true"`
}
