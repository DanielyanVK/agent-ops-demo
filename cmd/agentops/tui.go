package main

import (
	"flag"
	"os"

	tea "charm.land/bubbletea/v2"

	"agentops/internal/tui"
)

func init() {
	commands = append(commands, command{
		name:  "tui",
		usage: "интерактивный просмотр прогонов",
		run:   func(args []string) error { return runTUI(args) },
	})
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	runsDir := runsFlag(fs)
	rest, err := parseFlags(fs, args, os.Stdout)
	if err == nil {
		err = noArgs(rest)
	}
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(tui.New(*runsDir)).Run()
	return err
}
