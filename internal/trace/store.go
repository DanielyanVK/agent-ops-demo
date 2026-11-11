package trace

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Stats struct {
	Total, Passed, Failed, Running int
	CostUSD                        float64 // the agent runs
	PostmortemCostUSD              float64 // the model diagnoses
	AvgDuration                    time.Duration
}

func LoadRun(dir string) (Run, error) {
	var run Run
	var meta struct {
		ID        string    `json:"id"`
		Task      string    `json:"task"`
		Agent     string    `json:"agent"`
		Model     string    `json:"model"`
		Scenario  string    `json:"scenario"`
		Prompt    string    `json:"prompt"`
		Stderr    string    `json:"stderr"`
		StartedAt time.Time `json:"started_at"`
		Verdict   *Verdict  `json:"verdict"`
	}
	if err := readJSON(filepath.Join(dir, "meta.json"), &meta); err != nil {
		return run, err
	}

	// A run that has just started has no trace yet.
	if f, err := os.Open(filepath.Join(dir, "trace.jsonl")); err == nil {
		defer f.Close()
		if run, err = ParseClaude(f); err != nil {
			return run, fmt.Errorf("trace.jsonl: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return run, err
	}

	run.ID, run.Task, run.Agent, run.Scenario = meta.ID, meta.Task, meta.Agent, meta.Scenario
	run.Prompt, run.Stderr, run.StartedAt, run.Verdict = meta.Prompt, meta.Stderr, meta.StartedAt, meta.Verdict
	if meta.Model != "" { // meta carries the short alias ("haiku"), the trace the full model id
		run.Model = meta.Model
	}

	var pm Postmortem
	switch err := readJSON(filepath.Join(dir, "postmortem.json"), &pm); {
	case err == nil:
		run.Postmortem = &pm
	case !errors.Is(err, fs.ErrNotExist):
		return run, err
	}
	return run, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// WriteJSON writes v as indented JSON via a temp file and a rename, so a viewer
// polling the run directory never reads a half-written file.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // a no-op after the rename
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func LoadDir(runsDir string) (runs []Run, warnings []string, err error) {
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		dir := filepath.Join(runsDir, e.Name())
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
			continue
		}
		run, err := LoadRun(dir)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		runs = append(runs, run)
	}
	slices.SortFunc(runs, func(a, b Run) int { return compareIDs(a.ID, b.ID) })
	return runs, warnings, nil
}

var runIDRe = regexp.MustCompile(`^run-([0-9]+)$`)

// compareIDs is a total order: run-<number> ids by number (run-100 after
// run-11), then every other name, as text.
func compareIDs(a, b string) int {
	ma, mb := runIDRe.FindStringSubmatch(a), runIDRe.FindStringSubmatch(b)
	switch {
	case ma != nil && mb != nil:
		na, _ := strconv.Atoi(ma[1])
		nb, _ := strconv.Atoi(mb[1])
		return cmp.Or(cmp.Compare(na, nb), strings.Compare(a, b))
	case ma != nil:
		return -1
	case mb != nil:
		return 1
	}
	return strings.Compare(a, b)
}

func Summarize(runs []Run) Stats {
	var s Stats
	var total time.Duration
	var finished int
	for _, r := range runs {
		s.Total++
		s.CostUSD += r.CostUSD
		if r.Postmortem != nil {
			s.PostmortemCostUSD += r.Postmortem.CostUSD
		}
		switch r.Status() {
		case Passed:
			s.Passed++
		case Failed:
			s.Failed++
		default:
			s.Running++
			continue
		}
		total += r.Duration
		finished++
	}
	if finished > 0 {
		s.AvgDuration = total / time.Duration(finished)
	}
	return s
}
