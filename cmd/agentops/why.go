package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "why",
		usage: "почему прогон упал",
		run:   func(args []string) error { return runWhy(args, os.Stdout) },
	})
}

func runWhy(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("why", flag.ContinueOnError)
	runsDir := runsFlag(flags)
	asJSON := jsonFlag(flags)
	ids, err := parseFlags(flags, args, stdout)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("нужен ровно один id прогона")
	}
	run, err := loadRun(*runsDir, ids[0])
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(stdout, whyOutput{run.ID, run.Status().String(), run.Postmortem})
	}

	// The step and the model's text come from the agent's run: cleaned like show --step.
	var text string
	switch {
	case run.Status() == trace.Passed:
		text = fmt.Sprintf("%s: прогон успешен, разбирать нечего\n", run.ID)
	case run.Status() == trace.Running:
		text = fmt.Sprintf("%s: прогон ещё идёт\n", run.ID)
	case run.Postmortem == nil:
		text = fmt.Sprintf("%s: разбора ещё нет — выполни команду analyze\n", run.ID)
	default:
		pm := run.Postmortem
		where := "Сбой не привязан к одному шагу"
		switch {
		case pm.FailedStep >= 1 && pm.FailedStep <= len(run.Steps):
			s := run.Steps[pm.FailedStep-1]
			where = fmt.Sprintf("Причина на шаге %d: %s %s", pm.FailedStep, s.Label(), s.Summary)
		case pm.FailedStep != 0: // postmortem written for a different trace; don't invent a tool
			where = fmt.Sprintf("Причина на шаге %d", pm.FailedStep)
		}
		text = fmt.Sprintf("%s · %s %s\n\n%s\nПричина: %s\nЧто поправить: %s\n",
			run.ID, run.Status().Glyph(), statusWord(run.Status()), where, pm.Cause, pm.Fix)
	}
	_, err = fmt.Fprint(stdout, trace.Clean(text))
	return err
}
