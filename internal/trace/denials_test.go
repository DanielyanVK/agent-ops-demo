package trace

import (
	"reflect"
	"strings"
	"testing"
)

func TestClassifyDenial(t *testing.T) {
	cases := []struct {
		name  string
		step  Step
		codex bool
		class string
		items []string
	}{
		{"single part", Step{Output: "This Bash command contains multiple operations. The following part requires approval: go vet ./..."}, false, "compound", []string{"go vet"}},
		{"plural parts", Step{Output: "This Bash command contains multiple operations. The following parts require approval: python3 -, gofmt -l ., go vet ./..."}, false, "compound", []string{"python3", "gofmt", "go vet"}},
		{"sed with pipes in quotes", Step{Output: "This Bash command contains multiple operations. The following part requires approval: sed -i 's|a|b|' cart.go"}, false, "compound", []string{"sed -i"}},
		{"chain drops go test", Step{Output: "contains multiple operations. The following part requires approval: gofmt -l . ; go vet ./... && go test -v ./... 2>&1"}, false, "compound", []string{"gofmt", "go vet"}},
		{"env prefix", Step{Output: "contains multiple operations. The following part requires approval: GOCACHE=/x env FOO=1 go build ./... || true"}, false, "compound", []string{"go build", "true"}},
		{"variable", Step{Output: "A variable in this command can't be checked before it runs"}, false, "variable", nil},
		{"disabled", Step{Tool: "Edit", Output: "<tool_use_error>Error: No such tool available: Edit. Edit is disabled for this session, in subagents as well as here.</tool_use_error>"}, false, "disabled", []string{"Edit"}},
		{"approval", Step{Line: "rm -rf build", Output: "This command requires approval"}, false, "approval", []string{"rm"}},
		{"sandbox text", Step{Kind: "shell", Line: "go test ./...", Output: "go: creating work dir: mkdir /tmp/go-build1352967674: operation not permitted"}, false, "sandbox", []string{"go test"}},
		{"read-only fs", Step{Kind: "shell", Line: "touch x && ls", Output: "Read-only file system"}, false, "sandbox", []string{"touch"}},
		{"codex noise", Step{Kind: "shell", Line: "git status --short", Output: "some unrelated noise"}, true, "sandbox", []string{"git status"}},
		{"codex edit", Step{Tool: "Edit", Kind: "edit", Line: "cart.go"}, true, "sandbox", []string{"Edit"}},
		{"other", Step{Output: "nope"}, false, "other", nil},
	}
	for _, c := range cases {
		class, items := ClassifyDenial(c.step, c.codex)
		if class != c.class || !reflect.DeepEqual(items, c.items) {
			t.Errorf("%s: got %s %q, want %s %q", c.name, class, items, c.class, c.items)
		}
	}
}

func denied(tool, kind, line, out string) Step {
	return Step{Tool: tool, Kind: kind, Line: line, Output: out, Denied: true, IsError: true}
}

func TestDenialsAggregate(t *testing.T) {
	comp := "contains multiple operations. The following part requires approval: go vet ./..."
	runs := []Run{
		{ID: "run-1", Agent: "claude", Scenario: "baseline", Steps: []Step{denied("Bash", "shell", "x", comp), denied("Bash", "shell", "x", comp), {Tool: "Read"}}},
		{ID: "run-2", Agent: "claude", Scenario: "baseline", Steps: []Step{denied("Bash", "shell", "x", "can't be checked before it runs"), denied("Bash", "shell", "x", comp)}},
		{ID: "run-3", Agent: "claude", Scenario: "skill", Steps: []Step{{Tool: "Read"}, {Tool: "Bash"}}},
		{ID: "run-4", Agent: "codex", Scenario: "baseline"},
	}
	got := Denials(runs)
	if got.Runs != 4 || got.RunsWithDenials != 2 || got.Steps != 7 || got.Denied != 4 {
		t.Fatalf("totals: %+v", got)
	}
	want := []DenialReason{
		{"compound", 3, 2, []ToolCount{{"go vet", 3}}},
		{"variable", 1, 1, nil},
	}
	if !reflect.DeepEqual(got.Reasons, want) {
		t.Errorf("reasons: %+v", got.Reasons)
	}
	wantGroups := []DenialGroup{
		{"claude", "baseline", 2, 5, 4}, {"claude", "skill", 1, 2, 0}, {"codex", "baseline", 1, 0, 0},
	}
	if !reflect.DeepEqual(got.Groups, wantGroups) {
		t.Errorf("groups: %+v", got.Groups)
	}
	if e := Denials(nil); e.Runs != 0 || len(e.Reasons) != 0 {
		t.Errorf("empty: %+v", e)
	}
}

func TestDenialItemOrder(t *testing.T) {
	out := "contains multiple operations. The following parts require approval: b, a, a, c"
	got := Denials([]Run{{Agent: "claude", Steps: []Step{denied("Bash", "shell", "", out)}}})
	if !reflect.DeepEqual(got.Reasons[0].Items, []ToolCount{{"a", 2}, {"b", 1}, {"c", 1}}) {
		t.Errorf("items: %+v", got.Reasons[0].Items)
	}
}

func TestHints(t *testing.T) {
	items := func(n ...string) []ToolCount {
		var out []ToolCount
		for _, s := range n {
			out = append(out, ToolCount{s, 1})
		}
		return out
	}
	cases := []struct {
		r    DenialReason
		want string
	}{
		{DenialReason{Class: "compound", Items: items("go vet", "gofmt")}, "Run one command per call: do not chain `go vet`, `gofmt` with other commands using && ; or | — the whole call is rejected when any part needs approval."},
		{DenialReason{Class: "compound"}, "Run one command per call: do not chain several commands with && ; or | — the whole call is rejected when any part needs approval."},
		{DenialReason{Class: "variable"}, "Do not use shell loops or variables to handle several files in one call; read each file with the file-reading tool and run each command separately."},
		{DenialReason{Class: "disabled", Items: items("Edit")}, "The `Edit` tool is disabled in this environment: if the task cannot be done without it, say so and stop instead of trying the same change through the shell."},
		{DenialReason{Class: "disabled"}, "A tool the task needs is disabled in this environment: if the task cannot be done without it, say so and stop instead of trying the same change through the shell."},
		{DenialReason{Class: "approval", Items: items("rm", "curl")}, "Do not call `rm`, `curl`: these commands are not allowed in this environment."},
		{DenialReason{Class: "approval"}, "Do not call commands that are not allowed in this environment."},
		{DenialReason{Class: "sandbox"}, "The workspace is read-only in this environment: commands that write files, build caches or temporary directories fail, so report what you would change instead of retrying."},
		{DenialReason{Class: "other"}, ""},
	}
	for _, c := range cases {
		if got := c.r.Hint(); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.r.Class, got, c.want)
		}
	}
	long := DenialReason{Class: "approval", Items: items("a", "b", "c", "d", "e", "f", "g")}
	if got := long.Hint(); !strings.Contains(got, "`d`, `e`, and others:") || strings.Contains(got, "`f`") {
		t.Errorf("long list: %s", got)
	}
}
