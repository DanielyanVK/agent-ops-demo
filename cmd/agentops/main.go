package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

type command struct {
	name  string
	usage string // one Russian line for the help listing
	run   func(args []string) error
}

var commands []command // each file appends from its init()

// exitCode is returned by a command that wants a specific non-zero exit status
// without an error message.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("код выхода %d", int(c)) }

func runsFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("AGENTOPS_RUNS")
	if def == "" {
		def = "runs"
	}
	return fs.String("runs", def, "каталог с прогонами")
}

func jsonFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "машиночитаемый вывод")
}

// parseFlags is the only flag-parsing path. Flags may follow positional
// arguments (`show run-06 --json`): the standard parser stops at the first
// positional, so it is taken and the rest parsed again. The FlagSet stays quiet:
// the caller reports the error once, in Russian; -h prints the Russian usage.
func parseFlags(fs *flag.FlagSet, args []string, stdout io.Writer) ([]string, error) {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	var positional []string
	for {
		err := fs.Parse(args)
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stdout, "Использование: agentops %s [флаги]\n\n", fs.Name())
			var b strings.Builder
			fs.SetOutput(&b)
			fs.PrintDefaults()
			fmt.Fprint(stdout, strings.ReplaceAll(b.String(), "(default ", "(по умолчанию "))
			return nil, err
		}
		if err != nil {
			return nil, russianFlagError(err)
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// The flag package's errors are plain English strings; these are all it produces.
var flagErrors = []struct {
	re   *regexp.Regexp
	tmpl string
}{
	{regexp.MustCompile(`^flag provided but not defined: -+(.*)$`), "неизвестный флаг: --$1"},
	{regexp.MustCompile(`^flag needs an argument: -+(.*)$`), "флагу --$1 нужно значение"},
	{regexp.MustCompile(`^invalid (?:boolean )?value (".*") for (?:flag )?-+([^:]*): `), "неверное значение $1 для флага --$2"},
	{regexp.MustCompile(`^bad flag syntax: (.*)$`), "неверный флаг: $1"},
}

func russianFlagError(err error) error {
	msg := err.Error()
	for _, f := range flagErrors {
		if m := f.re.FindStringSubmatchIndex(msg); m != nil {
			return errors.New(string(f.re.ExpandString(nil, f.tmpl, msg, m)))
		}
	}
	return err
}

// noArgs rejects positional arguments of a command that takes none: `run one-turn`
// would otherwise record a paid baseline run.
func noArgs(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("лишний аргумент: %s", args[0])
	}
	return nil
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // hints carry "&&"; agents read this output as is
	return enc.Encode(v)
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stderr))
}

func dispatch(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		if c := findCommand("tui"); c != nil {
			return runCommand(c, nil, stderr)
		}
		printHelp(os.Stdout)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		printHelp(os.Stdout)
		return 0
	}
	c := findCommand(args[0])
	if c == nil {
		fmt.Fprintf(stderr, "agentops: неизвестная команда %q\n\n", args[0])
		printHelp(stderr)
		return 2
	}
	return runCommand(c, args[1:], stderr)
}

func runCommand(c *command, args []string, stderr io.Writer) int {
	err := c.run(args)
	var code exitCode
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	case errors.Is(err, flag.ErrHelp):
		return 0
	}
	fmt.Fprintf(stderr, "agentops: %v\n", err)
	return 1
}

func findCommand(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

func printHelp(w io.Writer) {
	sorted := append([]command(nil), commands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	fmt.Fprintln(w, "Использование: agentops <команда> [флаги]\n\nКоманды:")
	for _, c := range sorted {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.usage)
	}
}
