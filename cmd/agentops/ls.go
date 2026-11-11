package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "ls",
		usage: "список прогонов и сводка",
		run:   func(args []string) error { return runLs(args, os.Stdout, os.Stderr) },
	})
}

func runLs(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	runsDir := runsFlag(fs)
	failedOnly := fs.Bool("failed", false, "только упавшие прогоны")
	asJSON := jsonFlag(fs)
	rest, err := parseFlags(fs, args, stdout)
	if err == nil {
		err = noArgs(rest)
	}
	if err != nil {
		return err
	}

	runs, warnings, err := trace.LoadDir(*runsDir)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "предупреждение:", w)
	}
	stats := trace.Summarize(runs)

	shown := runs
	if *failedOnly {
		shown = nil
		for _, r := range runs {
			if r.Status() == trace.Failed {
				shown = append(shown, r)
			}
		}
	}

	if *asJSON {
		briefs := make([]trace.RunBrief, 0, len(shown))
		for _, r := range shown {
			briefs = append(briefs, r.Brief())
		}
		return printJSON(stdout, struct {
			Stats trace.StatsJSON  `json:"stats"`
			Runs  []trace.RunBrief `json:"runs"`
		}{stats.JSON(), briefs})
	}

	if stats.Total == 0 {
		fmt.Fprintln(stdout, "прогонов нет")
		return nil
	}
	parts := []string{
		fmt.Sprintf("%d %s", stats.Total, trace.Plural(stats.Total, "прогон", "прогона", "прогонов")),
		fmt.Sprintf("✓ %d", stats.Passed),
		fmt.Sprintf("✗ %d", stats.Failed),
	}
	if stats.Running > 0 {
		parts = append(parts, fmt.Sprintf("… %d", stats.Running))
	}
	parts = append(parts, trace.FmtCost(stats.CostUSD), "в среднем "+trace.FmtDuration(stats.AvgDuration))
	fmt.Fprintf(stdout, "%s\n\n", strings.Join(parts, " · "))

	// tabwriter aligns whole tables only, so numeric columns are right-aligned by hand
	// (widest values: "1м 05с", "$0.0043").
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\tСТАТУС\tСЦЕНАРИЙ\tМОДЕЛЬ\t%5s\t%6s\t%7s\n", "ШАГОВ", "ВРЕМЯ", "ЦЕНА")
	for _, r := range shown {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%5d\t%6s\t%7s\n", r.ID, r.Status().Glyph(), r.Scenario,
			r.Model, len(r.Steps), trace.FmtDuration(r.Duration), trace.FmtCost(r.CostUSD))
	}
	return tw.Flush()
}
