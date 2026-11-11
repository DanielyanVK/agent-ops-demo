# agentops: notes for AI agents

agentops records headless runs of a coding agent on `testbed/`, judges each run only by the project's original tests, and explains every failed run with a postmortem (deterministic rules first, a model call only when no rule applies). The commands below only read recorded runs from `runs/` (`--runs DIR` for another directory); they never start an agent.

Build once: `go build -o agentops ./cmd/agentops` (`go run` turns every non-zero exit into 1). Then read results in this order and stop as soon as you have the answer:

1. `./agentops check --json` - is the gate green: `{"finished","passed","percent","min","ok"}`.
2. `./agentops ls --failed --json` - what failed and why, in one call: `stats` and one object per failed run, with its `postmortem`.
3. `./agentops show <id> --json` - one run with its step list, no bulky text.
4. `./agentops show <id> --step N --json` - one step with its full `input` and `output`; only when the postmortem is not enough. A postmortem's `failed_step` N > 0 lets you skip call 3 and go straight here.

Fields:

- `check`: `percent` = passed / finished runs, integer; `min` = the threshold in percent; `ok` = percent >= min.
- `status`: `passed`, `failed` or `running`; decided only by the original tests, never by what the agent claims.
- `stop_reason`: how the agent stopped: `success`, `error_max_turns`, `error_during_execution`, `timeout`, `incomplete` (no result line).
- `postmortem`: `{failed_step, cause, fix, source, cost_usd}`, or `null` until `analyze` has run; `failed_step` N = the step whose own action caused the failure; 0 = the cause is the task wording, permissions or limits, not one step.
- `source`: `rule` (free) or `model` (`cost_usd` is what the call cost).
- `stats.cost_usd`: what the agent runs cost; `stats.postmortem_cost_usd`: what the model diagnoses cost (sum over all runs).
- `steps[]`: `{n, tool, summary, error, denied, duration_ms}`; `denied` = the call was blocked by permissions (not a failure of the run, and not an `error`); `denials` lists blocked tools.
- `verdict.test_output`: the last 2000 bytes of the test run. Durations are integer milliseconds, costs USD floats.

Scenarios: `baseline` is the control; `no-edit` (a permission removed), `one-turn` (a turn limit of one) and `bad-task` (a misleading task) are deliberately sabotaged to show how failures are diagnosed; `few-turns` sits at the edge and may pass or fail.

Exit codes: 0 success; 1 an error, or `check` below its threshold; 2 unknown command; 130 `run` interrupted. Exit 1 from `check` with JSON `"ok": false` means the gate is red, not that the command failed: a command error prints `agentops: …` on stderr and no JSON.

Never read `runs/*/trace.jsonl` directly: it is 10–20 KB per run, and the calls above carry what you need.
