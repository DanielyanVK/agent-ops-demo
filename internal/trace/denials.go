package trace

import (
	"cmp"
	"slices"
	"strings"
)

// Reason classes of a blocked call.
const (
	DenyCompound = "compound"
	DenyVariable = "variable"
	DenyDisabled = "disabled"
	DenyApproval = "approval"
	DenySandbox  = "sandbox"
	DenyOther    = "other"
)

type DenialReason struct {
	Class string
	Calls int         // blocked calls of this class
	Runs  int         // runs that have at least one
	Items []ToolCount // items with counts, most frequent first, ties by name
}

type DenialGroup struct { // one line per agent+scenario
	Agent, Scenario string
	Runs            int // runs in the group
	Steps           int
	Denied          int
}

type DenialSummary struct {
	Runs, RunsWithDenials, Steps, Denied int
	Reasons                              []DenialReason // most calls first, ties by class name
	Groups                               []DenialGroup  // sorted by agent, then scenario
}

// ClassifyDenial decides the reason class of a denied step by its output (first
// match wins) and returns the items the step contributes. codex is true for a
// step of a Codex run, where every denied step is a sandbox refusal.
func ClassifyDenial(s Step, codex bool) (class string, items []string) {
	low := strings.ToLower(s.Output)
	switch {
	case strings.Contains(s.Output, "contains multiple operations"):
		_, parts, _ := strings.Cut(s.Output, "approval: ")
		parts, _, _ = strings.Cut(parts, "\n")
		for p := range strings.SplitSeq(parts, ", ") {
			items = append(items, chainHeads(p, true)...)
		}
		return DenyCompound, items
	case strings.Contains(s.Output, "can't be checked before it runs"):
		return DenyVariable, nil
	case strings.Contains(s.Output, "No such tool available"):
		return DenyDisabled, []string{s.Tool}
	case strings.Contains(s.Output, "requires approval"):
		return DenyApproval, chainHeads(s.Line, true)
	case codex || strings.Contains(low, "operation not permitted") || strings.Contains(low, "read-only file system"):
		if s.Kind != "shell" { // an edit has no command, only paths
			return DenySandbox, []string{s.Tool}
		}
		if h := chainHeads(s.Line, false); len(h) > 0 {
			return DenySandbox, h[:1]
		}
		return DenySandbox, nil
	}
	return DenyOther, nil
}

// chainHeads splits a command on && || ; | outside quotes and returns the head
// of every segment; with dropAllowed, `go test` is left out.
func chainHeads(cmd string, dropAllowed bool) []string {
	var heads []string
	for _, seg := range splitChain(cmd) {
		if h := commandHead(seg); h != "" && !(dropAllowed && h == "go test") {
			heads = append(heads, h)
		}
	}
	return heads
}

func splitChain(s string) []string {
	var segs []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ';' || c == '|' || c == '&' && i+1 < len(s) && s[i+1] == '&':
			segs = append(segs, s[start:i])
			start = i + 1 // a doubled || leaves an empty segment, skipped by the caller
			if c == '&' {
				start, i = i+2, i+1
			}
		}
	}
	return append(segs, s[start:])
}

func commandHead(seg string) string {
	words := strings.Fields(seg)
	for len(words) > 0 {
		w := words[0]
		if i := strings.IndexByte(w, '='); w == "env" || i > 0 && !strings.ContainsAny(w[:i], "/\"'") {
			words = words[1:]
			continue
		}
		break
	}
	if len(words) == 0 {
		return ""
	}
	switch head := words[0]; head {
	case "go", "git", "npm", "cargo", "docker", "kubectl":
		if len(words) > 1 {
			return head + " " + words[1]
		}
	case "sed":
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-i") {
				return "sed -i"
			}
		}
	}
	return words[0]
}

// Denials aggregates the blocked calls of runs by reason and by agent+scenario.
func Denials(runs []Run) DenialSummary {
	sum := DenialSummary{Runs: len(runs)}
	type acc struct {
		calls, runs int
		items       map[string]int
	}
	reasons := map[string]*acc{}
	groups := map[[2]string]*DenialGroup{}
	for _, r := range runs {
		g := groups[[2]string{r.Agent, r.Scenario}]
		if g == nil {
			g = &DenialGroup{Agent: r.Agent, Scenario: r.Scenario}
			groups[[2]string{r.Agent, r.Scenario}] = g
		}
		g.Runs++
		g.Steps += len(r.Steps)
		sum.Steps += len(r.Steps)
		seen := map[string]bool{}
		for _, s := range r.Steps {
			if !s.Denied {
				continue
			}
			g.Denied++
			sum.Denied++
			class, items := ClassifyDenial(s, r.Agent == "codex")
			a := reasons[class]
			if a == nil {
				a = &acc{items: map[string]int{}}
				reasons[class] = a
			}
			a.calls++
			if !seen[class] {
				seen[class] = true
				a.runs++
			}
			for _, it := range items {
				a.items[it]++
			}
		}
		if len(seen) > 0 {
			sum.RunsWithDenials++
		}
	}
	for class, a := range reasons {
		var items []ToolCount
		for name, n := range a.items {
			items = append(items, ToolCount{name, n})
		}
		slices.SortFunc(items, func(x, y ToolCount) int { return cmp.Or(cmp.Compare(y.N, x.N), cmp.Compare(x.Tool, y.Tool)) })
		sum.Reasons = append(sum.Reasons, DenialReason{class, a.calls, a.runs, items})
	}
	slices.SortFunc(sum.Reasons, func(x, y DenialReason) int {
		return cmp.Or(cmp.Compare(y.Calls, x.Calls), cmp.Compare(x.Class, y.Class))
	})
	for _, g := range groups {
		sum.Groups = append(sum.Groups, *g)
	}
	slices.SortFunc(sum.Groups, func(x, y DenialGroup) int {
		return cmp.Or(cmp.Compare(x.Agent, y.Agent), cmp.Compare(x.Scenario, y.Scenario))
	})
	return sum
}

// Hint is one English sentence for an agent's instructions; empty for "other".
func (r DenialReason) Hint() string {
	var names []string
	for _, it := range r.Items[:min(len(r.Items), 5)] {
		names = append(names, "`"+it.Tool+"`")
	}
	if len(r.Items) > 5 {
		names = append(names, "and others")
	}
	items := strings.Join(names, ", ")
	switch r.Class {
	case DenyCompound:
		if items == "" {
			return "Run one command per call: do not chain several commands with && ; or | — the whole call is rejected when any part needs approval."
		}
		return "Run one command per call: do not chain " + items + " with other commands using && ; or | — the whole call is rejected when any part needs approval."
	case DenyVariable:
		return "Do not use shell loops or variables to handle several files in one call; read each file with the file-reading tool and run each command separately."
	case DenyDisabled:
		what := "A tool the task needs is"
		if items != "" {
			what = "The " + items + " tool is"
		}
		return what + " disabled in this environment: if the task cannot be done without it, say so and stop instead of trying the same change through the shell."
	case DenyApproval:
		if items == "" {
			return "Do not call commands that are not allowed in this environment."
		}
		return "Do not call " + items + ": these commands are not allowed in this environment."
	case DenySandbox:
		return "The workspace is read-only in this environment: commands that write files, build caches or temporary directories fail, so report what you would change instead of retrying."
	}
	return ""
}
