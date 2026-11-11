package runner

import (
	"slices"
	"strings"
)

type Scenario struct {
	Name, Prompt    string
	Tools           []string // the only tools the session has: the default set costs ~33k input tokens per turn
	AllowedTools    []string
	DisallowedTools []string
	MaxTurns        int
	// CodexSandbox is the sandbox mode of a Codex run; empty = the agent does not support the scenario (Codex has no turn limit).
	CodexSandbox string
	// Skills lifts the isolation that hides skills from the agent.
	Skills bool
}

const basePrompt = `The tests in this Go module fail. Find the bugs and fix them so that "go test ./..." passes. Do not modify the test files.`

var editTools = []string{"Read", "Glob", "Grep", "Edit", "Bash(go test:*)"}

// no-edit keeps Edit here and disallows it, so its run shows a tool that exists but is blocked.
var tools = []string{"Read", "Glob", "Grep", "Edit", "Bash"}

var Scenarios = []Scenario{
	{Name: "baseline", Prompt: basePrompt, Tools: tools, AllowedTools: editTools, MaxTurns: 25, CodexSandbox: "workspace-write"},
	{
		Name: "no-edit", Prompt: basePrompt, Tools: tools,
		AllowedTools:    []string{"Read", "Glob", "Grep", "Bash(go test:*)"},
		DisallowedTools: []string{"Edit", "Write", "MultiEdit", "NotebookEdit"},
		MaxTurns:        25,
		CodexSandbox:    "read-only",
	},
	{Name: "few-turns", Prompt: basePrompt, Tools: tools, AllowedTools: editTools, MaxTurns: 3},
	{Name: "one-turn", Prompt: basePrompt, Tools: tools, AllowedTools: editTools, MaxTurns: 1},
	{
		Name:   "bad-task",
		Prompt: `The tests in this Go module fail. The bug is in stats.go. Fix it so that "go test ./..." passes. Do not touch any other file.`,
		Tools:  tools, AllowedTools: editTools, MaxTurns: 25, CodexSandbox: "workspace-write",
	},
	{
		Name: "skill", Prompt: basePrompt, Skills: true, MaxTurns: 25, CodexSandbox: "workspace-write",
		Tools:        append(slices.Clone(tools), "Skill"),
		AllowedTools: append(slices.Clone(editTools), "Skill"),
	},
}

func FindScenario(name string) (Scenario, bool) {
	for _, s := range Scenarios {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}

func ScenarioNames() string {
	names := make([]string, len(Scenarios))
	for i, s := range Scenarios {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}
