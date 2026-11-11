package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "check",
		usage: "проверить долю успешных прогонов (код выхода 1 ниже порога)",
		run:   func(args []string) error { return runCheck(args, os.Stdout, os.Stderr) },
	})
}

type checkResult struct {
	Finished int  `json:"finished"`
	Passed   int  `json:"passed"`
	Percent  int  `json:"percent"`
	Min      int  `json:"min"`
	OK       bool `json:"ok"`
}

func runCheck(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	runsDir := runsFlag(flags)
	threshold := flags.Int("min", 80, "порог успешных прогонов, %")
	asJSON := jsonFlag(flags)
	rest, err := parseFlags(flags, args, stdout)
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
	s := trace.Summarize(runs)
	res := checkResult{Finished: s.Passed + s.Failed, Passed: s.Passed, Min: *threshold}

	if res.Finished == 0 {
		if *asJSON {
			err = printJSON(stdout, res)
		} else {
			_, err = fmt.Fprintln(stdout, "завершённых прогонов нет")
		}
		if err != nil {
			return err
		}
		return exitCode(1)
	}

	res.Percent = res.Passed * 100 / res.Finished
	res.OK = res.Percent >= res.Min
	if *asJSON {
		err = printJSON(stdout, res)
	} else {
		verdict := "ниже порога"
		if res.OK {
			verdict = "в норме"
		}
		_, err = fmt.Fprintf(stdout, "успешных %d из %d (%d%%) · порог %d%% · %s\n",
			res.Passed, res.Finished, res.Percent, res.Min, verdict)
	}
	if err != nil {
		return err
	}
	if !res.OK {
		return exitCode(1)
	}
	return nil
}
