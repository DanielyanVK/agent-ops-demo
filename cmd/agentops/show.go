package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "show",
		usage: "детали прогона и его шаги",
		run:   func(args []string) error { return runShow(args, os.Stdout) },
	})
}

// loadRun maps a missing run directory to the user-facing "not found" error.
func loadRun(runsDir, id string) (trace.Run, error) {
	run, err := trace.LoadRun(filepath.Join(runsDir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return run, fmt.Errorf("прогон %q не найден", id)
	}
	return run, err
}

func statusWord(s trace.Status) string {
	switch s {
	case trace.Passed:
		return "успех"
	case trace.Failed:
		return "провал"
	}
	return "идёт"
}

func runShow(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("show", flag.ContinueOnError)
	runsDir := runsFlag(flags)
	step := flags.Int("step", 0, "показать один шаг целиком")
	maxBytes := flags.Int("max-bytes", 4000, "предел размера входа и выхода шага в байтах, 0 — без предела")
	asJSON := jsonFlag(flags)
	ids, err := parseFlags(flags, args, stdout)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("нужен ровно один id прогона")
	}
	if *maxBytes < 0 {
		return errors.New("--max-bytes не может быть отрицательным")
	}
	run, err := loadRun(*runsDir, ids[0])
	if err != nil {
		return err
	}

	if *step != 0 {
		if *step < 1 || *step > len(run.Steps) {
			return fmt.Errorf("шага %d нет: в прогоне %s шагов %d", *step, run.ID, len(run.Steps))
		}
		s := run.Steps[*step-1]
		in, inCut := truncateText(s.Input, *maxBytes)
		out, outCut := truncateText(s.Output, *maxBytes)
		if *asJSON {
			return printJSON(stdout, showStepOutput{run.ID, stepDetail{s.Brief(), in, out, len(s.Input), len(s.Output), inCut || outCut}})
		}
		title := fmt.Sprintf("Шаг %d · %s", s.N, s.Tool)
		if s.IsError {
			title += " · ошибка"
		}
		text := fmt.Sprintf("%s\n\n%s\n\nВХОД\n%s\n\nВЫХОД\n%s\n", runHeader(run), title, in, out)
		if inCut || outCut {
			text += fmt.Sprintf("обрезано: вход %d Б, выход %d Б полностью; --max-bytes 0 покажет целиком\n", len(s.Input), len(s.Output))
		}
		// Input and output are agent-controlled: an escape sequence must not reach the terminal.
		fmt.Fprint(stdout, trace.Clean(text))
		return nil
	}

	if *asJSON {
		steps := make([]trace.StepBrief, 0, len(run.Steps))
		for _, s := range run.Steps {
			steps = append(steps, s.Brief())
		}
		denials := run.Denials
		if denials == nil {
			denials = []string{}
		}
		mcp := run.MCP
		if mcp == nil {
			mcp = []trace.ToolCount{}
		}
		return printJSON(stdout, showOutput{run.Brief(), denials, mcp, run.Subagents, run.WebCalls, run.Verdict, steps})
	}

	info := []string{
		trace.FmtDuration(run.Duration), trace.FmtCost(run.CostUSD),
		fmt.Sprintf("%d %s", len(run.Steps), trace.Plural(len(run.Steps), "шаг", "шага", "шагов")),
		"остановка: " + run.StopReason,
	}
	if t := run.FmtTokens(); t != "" {
		info = append(info, "токены: "+t)
	}
	info = append(info, run.UsedParts()...)
	if b := run.Blocked(); len(b) > 0 {
		info = append(info, "заблокировано: "+trace.FmtBlocked(b))
	}
	// Everything below comes from the agent's trace: cleaned before it is
	// measured, so that escape sequences neither reach the terminal nor skew the columns.
	fmt.Fprint(stdout, trace.Clean(runHeader(run)+"\n"+strings.Join(info, " · ")+"\n\n"))
	oneLine := func(s string) string { return strings.ReplaceAll(trace.Clean(s), "\n", " ") }
	numW, toolW := len(strconv.Itoa(len(run.Steps))), 0
	for _, s := range run.Steps {
		toolW = max(toolW, utf8.RuneCountInString(oneLine(s.Tool)))
	}
	for _, s := range run.Steps {
		mark := s.Glyph()
		tag := ""
		if s.Denied {
			tag = " · запрещено"
		}
		if run.CauseStep(s) {
			tag += " ← разбор"
		}
		fmt.Fprintf(stdout, "%*d %s %s %s %6s%s\n", numW, s.N, mark, padRight(oneLine(s.Tool), toolW), padRight(oneLine(s.ShortText()), 60),
			trace.FmtDuration(s.Duration), tag)
	}
	return nil
}

// truncateText keeps the first and last limit/2 bytes (errors are usually at the
// end), cut on rune boundaries, and says how many bytes were dropped. limit 0 means no limit.
func truncateText(s string, limit int) (string, bool) {
	if limit == 0 || len(s) <= limit {
		return s, false
	}
	head, tail := limit/2, len(s)-limit/2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return fmt.Sprintf("%s\n… [обрезано %d байт] …\n%s", s[:head], tail-head, s[tail:]), true
}

func runHeader(r trace.Run) string {
	return fmt.Sprintf("%s · %s · %s · %s · %s · %s %s", r.ID, r.Task, r.Agent, r.Model, r.Scenario,
		r.Status().Glyph(), statusWord(r.Status()))
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-utf8.RuneCountInString(s)))
}
