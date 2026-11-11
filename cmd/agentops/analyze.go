package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"agentops/internal/postmortem"
	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "analyze",
		usage: "автоматический разбор упавших прогонов",
		run:   func(args []string) error { return runAnalyze(args, os.Stdout, os.Stderr) },
	})
}

func runAnalyze(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	runsDir := runsFlag(fs)
	model := fs.String("model", "haiku", "модель для разбора, который не решается правилами")
	force := fs.Bool("force", false, "разобрать заново, даже если разбор уже есть")
	asJSON := jsonFlag(fs)
	ids, err := parseFlags(fs, args, stdout)
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

	want := map[string]bool{}
	for _, id := range ids {
		if !slices.ContainsFunc(runs, func(r trace.Run) bool { return r.ID == id }) {
			return fmt.Errorf("прогон %q не найден", id)
		}
		want[id] = true
	}
	var todo []trace.Run
	for _, r := range runs {
		if r.Status() != trace.Failed || (len(want) > 0 && !want[r.ID]) {
			continue
		}
		if r.Postmortem != nil && !*force {
			continue
		}
		todo = append(todo, r)
	}
	if len(todo) == 0 && !*asJSON {
		fmt.Fprintln(stdout, "разбирать нечего")
		return nil
	}

	var byRule, byModel, failed int
	var spent float64
	for _, r := range todo {
		pm, err := postmortem.Analyze(context.Background(), r, *model)
		if err == nil {
			err = postmortem.Save(filepath.Join(*runsDir, r.ID), pm)
		}
		if err != nil {
			failed++
			if *asJSON {
				printJSON(stdout, struct {
					ID    string `json:"id"`
					OK    bool   `json:"ok"`
					Error string `json:"error"`
				}{r.ID, false, err.Error()})
			} else {
				fmt.Fprintf(stdout, "%s  ошибка: %v\n", r.ID, err)
			}
			continue
		}
		spent += pm.CostUSD
		if pm.Source == "rule" {
			byRule++
		} else {
			byModel++
		}
		switch {
		case *asJSON:
			printJSON(stdout, struct {
				ID      string  `json:"id"`
				OK      bool    `json:"ok"`
				Source  string  `json:"source"`
				CostUSD float64 `json:"cost_usd"`
			}{r.ID, true, pm.Source, pm.CostUSD})
		case pm.Source == "rule":
			fmt.Fprintf(stdout, "%s  разбор готов · правило\n", r.ID)
		default:
			fmt.Fprintf(stdout, "%s  разбор готов · модель %s\n", r.ID, trace.FmtCost(pm.CostUSD))
		}
	}
	if !*asJSON {
		cost := "$0"
		if spent > 0 {
			cost = trace.FmtCost(spent)
		}
		fmt.Fprintf(stdout, "\nправилами: %d · моделью: %d · потрачено %s\n", byRule, byModel, cost)
	}
	if failed > 0 {
		return fmt.Errorf("не удалось разобрать прогонов: %d", failed)
	}
	return nil
}
