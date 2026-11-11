package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"agentops/internal/runner"
	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "run",
		usage: "записать прогон агента на тестовом проекте",
		run:   func(args []string) error { return runRun(args, os.Stdout) },
	})
}

func runRun(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	runsDir := runsFlag(fs)
	testbed := fs.String("testbed", "testbed", "каталог тестового проекта")
	scenarioName := fs.String("scenario", "baseline", "сценарий: "+runner.ScenarioNames())
	model := fs.String("model", "haiku", "модель агента")
	n := fs.Int("n", 1, "сколько прогонов выполнить")
	asJSON := jsonFlag(fs)
	rest, err := parseFlags(fs, args, stdout)
	if err == nil {
		err = noArgs(rest)
	}
	if err != nil {
		return err
	}
	if *n < 1 {
		return errors.New("--n должно быть не меньше 1")
	}
	sc, ok := runner.FindScenario(*scenarioName)
	if !ok {
		return fmt.Errorf("неизвестный сценарий %q, доступны: %s", *scenarioName, runner.ScenarioNames())
	}

	// Ctrl-C, kill or a closed terminal stop the agent and leave no half-recorded run behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	for range *n {
		id, err := runner.Run(ctx, runner.Options{
			RunsDir: *runsDir, TestbedDir: *testbed, Model: *model, Scenario: sc})
		if ctx.Err() != nil {
			return exitCode(130)
		}
		if err != nil {
			return err
		}
		r, err := trace.LoadRun(filepath.Join(*runsDir, id))
		if err != nil {
			return err
		}
		if *asJSON {
			err = printJSON(stdout, runOutput{id, r.Status().String(), sc.Name, *model})
		} else {
			_, err = fmt.Fprintf(stdout, "%s  %s  %s  %s\n", id, r.Status().Glyph(), sc.Name, *model)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
