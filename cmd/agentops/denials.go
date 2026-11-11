package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "denials",
		usage: "отказы в правах по всем прогонам и подсказка агенту",
		run:   func(args []string) error { return runDenials(args, os.Stdout, os.Stderr) },
	})
}

var denialTitles = map[string]string{
	trace.DenyCompound: "составная команда",
	trace.DenyVariable: "переменная или цикл в команде",
	trace.DenyDisabled: "инструмент отключён",
	trace.DenyApproval: "команда не разрешена",
	trace.DenySandbox:  "песочница запрещает запись",
	trace.DenyOther:    "другое",
}

func runDenials(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("denials", flag.ContinueOnError)
	runsDir := runsFlag(fs)
	agent := fs.String("agent", "", "только прогоны этого агента")
	scenario := fs.String("scenario", "", "только прогоны этого сценария")
	hintsOnly := fs.Bool("hints", false, "только подсказки агенту, по одной в строке")
	asJSON := jsonFlag(fs)
	rest, err := parseFlags(fs, args, stdout)
	if err == nil {
		err = noArgs(rest)
	}
	if err != nil {
		return err
	}
	all, warnings, err := trace.LoadDir(*runsDir)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "предупреждение:", w)
	}
	var runs []trace.Run
	for _, r := range all {
		if (*agent == "" || r.Agent == *agent) && (*scenario == "" || r.Scenario == *scenario) {
			runs = append(runs, r)
		}
	}
	sum := trace.Denials(runs)

	hints := []string{}
	out := denialsOutput{Runs: sum.Runs, RunsWithDenials: sum.RunsWithDenials, Steps: sum.Steps, Denied: sum.Denied,
		Reasons: []denialReasonJSON{}, Groups: []denialGroupJSON{}}
	if sum.Steps > 0 {
		out.Percent = sum.Denied * 100 / sum.Steps
	}
	for _, r := range sum.Reasons {
		items := r.Items
		if items == nil {
			items = []trace.ToolCount{}
		}
		h := r.Hint()
		if h != "" {
			hints = append(hints, h)
		}
		out.Reasons = append(out.Reasons, denialReasonJSON{r.Class, denialTitles[r.Class], r.Calls, r.Runs, items, h})
	}
	for _, g := range sum.Groups {
		out.Groups = append(out.Groups, denialGroupJSON(g))
	}

	switch {
	case *asJSON && *hintsOnly:
		return printJSON(stdout, struct {
			Hints []string `json:"hints"`
		}{hints})
	case *asJSON:
		return printJSON(stdout, out)
	case *hintsOnly:
		for _, h := range hints {
			fmt.Fprintln(stdout, h)
		}
		return nil
	case sum.Runs == 0:
		fmt.Fprintln(stdout, "прогонов нет")
		return nil
	}

	if sum.Denied == 0 {
		fmt.Fprintln(stdout, "отказов нет")
	} else {
		fmt.Fprintf(stdout, "Отказы: %d из %d %s (%d%%) в %d из %d %s\n\n", sum.Denied, sum.Steps,
			trace.Plural(sum.Steps, "шага", "шагов", "шагов"), out.Percent,
			sum.RunsWithDenials, sum.Runs, trace.Plural(sum.Runs, "прогона", "прогонов", "прогонов"))
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "ПРИЧИНА\t%7s\t%8s\tЧТО\n", "ВЫЗОВОВ", "ПРОГОНОВ")
		for _, r := range out.Reasons {
			what := trace.FmtBlocked(r.Items)
			if what != "" {
				what = "\t" + what // no empty last cell: it would pad the line with spaces
			}
			fmt.Fprintf(tw, "%s\t%7d\t%8d%s\n", r.Title, r.Calls, r.Runs, what)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "АГЕНТ\tСЦЕНАРИЙ\t%8s\t%5s\t%7s\t%9s\n", "ПРОГОНОВ", "ШАГОВ", "ОТКАЗОВ", "НА ПРОГОН")
	for _, g := range out.Groups {
		per := "0"
		if g.Denied > 0 {
			per = fmt.Sprintf("%.1f", float64(g.Denied)/float64(g.Runs))
		}
		fmt.Fprintf(tw, "%s\t%s\t%8d\t%5d\t%7d\t%9s\n", g.Agent, g.Scenario, g.Runs, g.Steps, g.Denied, per)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(hints) > 0 {
		fmt.Fprint(stdout, "\nПодсказка агенту:\n")
		for _, h := range hints {
			fmt.Fprintln(stdout, "- "+h)
		}
	}
	return nil
}
