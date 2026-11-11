// Package postmortem diagnoses failed runs: deterministic rules first, a model
// only for what rules cannot decide.
package postmortem

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agentops/internal/trace"
)

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var editTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit"}

func blockedEdits(r trace.Run) []trace.ToolCount {
	var out []trace.ToolCount
	for _, b := range r.Blocked() {
		if slices.Contains(editTools, b.Tool) {
			out = append(out, b)
		}
	}
	return out
}

func ByRule(r trace.Run) (trace.Postmortem, bool) {
	pm := trace.Postmortem{Source: "rule"}
	edits := blockedEdits(r)
	switch {
	case len(r.Steps) == 0:
		pm.Cause = "Агент не начал работу и ничего не сообщил"
		if msg := cmp.Or(strings.TrimSpace(r.Result), strings.TrimSpace(r.Stderr)); msg != "" {
			pm.Cause = "Агент не начал работу: " + cut(msg, 200)
		}
		pm.Fix = "Проверить, что агент запускается и имеет доступ к модели, затем повторить прогон"
	case edits != nil:
		// ponytail: a blocked file edit is assumed to be the cause whenever one is
		// present; if runs appear where the agent was blocked but failed for another
		// reason, send those to the model too.
		for _, s := range r.Steps {
			if s.Denied && slices.Contains(editTools, s.Tool) {
				pm.FailedStep = s.N
				break
			}
		}
		names := make([]string, len(edits))
		for i, e := range edits {
			names[i] = e.Tool
		}
		pm.Cause = "Агенту запрещено менять файлы (заблокировано: " + trace.FmtBlocked(edits) + "), исправления не применены"
		pm.Fix = "Разрешить агенту " + strings.Join(names, ", ") + " для этой задачи или убрать из неё правку файлов"
	case r.StopReason == "error_max_turns":
		// No number: result.num_turns is not the configured --max-turns.
		pm.Cause = "Агент остановлен по лимиту ходов, не закончив задачу"
		pm.Fix = "Поднять лимит ходов или сузить задачу"
	case r.StopReason == "timeout":
		pm.Cause = "Агент не уложился в отведённое время"
		pm.Fix = "Увеличить лимит времени или сузить задачу"
	default:
		return trace.Postmortem{}, false
	}
	return pm, true
}

// BuildPrompt deliberately leaves out r.Scenario: the analyst must find the
// cause in the evidence, not in the label.
func BuildPrompt(r trace.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Задание агенту: %s\n", r.Prompt)
	fmt.Fprintf(&b, "Причина остановки: %s\n\nШаги:\n", r.StopReason)
	for i, s := range r.Steps {
		mark := "✓"
		if s.IsError {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%d. [%s] %s %s", s.N, mark, s.Tool, s.Summary)
		if s.Denied {
			b.WriteString(" (запрещено)")
		}
		b.WriteString("\n")
		// A denial's text is boilerplate.
		if (s.IsError && !s.Denied) || i >= len(r.Steps)-3 {
			if out := cut(s.Output, 400); out != "" {
				b.WriteString("   вывод: " + out + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "\nИтоговое сообщение агента: %s\n", cut(r.Result, 400))
	if r.Stderr != "" {
		fmt.Fprintf(&b, "\nStderr агента:\n%s\n", r.Stderr)
	}
	if r.Verdict != nil {
		fmt.Fprintf(&b, "\nВывод тестов:\n%s\n", cut(r.Verdict.TestOutput, 1200))
	}
	b.WriteString("\nОтветь одним JSON-объектом и ничего больше: " +
		`{"failed_step": <номер шага или 0>, ` +
		`"cause": "<одна главная причина сбоя, не больше двух коротких предложений по-русски>", ` +
		`"fix": "<что изменить в формулировке задания, правах или лимитах агента, и ничего больше; не больше двух коротких предложений по-русски>"}` + "\n")
	return b.String()
}

func Parse(stdout []byte) (trace.Postmortem, error) {
	var outer struct {
		Result  string  `json:"result"`
		IsError bool    `json:"is_error"`
		Cost    float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(stdout, &outer); err != nil {
		return trace.Postmortem{}, fmt.Errorf("ответ модели не JSON: %w", err)
	}
	if outer.IsError {
		return trace.Postmortem{}, fmt.Errorf("модель вернула ошибку: %s", outer.Result)
	}
	i, j := strings.Index(outer.Result, "{"), strings.LastIndex(outer.Result, "}")
	if i < 0 || j < i {
		return trace.Postmortem{}, errors.New("в ответе модели нет JSON-объекта")
	}
	var pm trace.Postmortem
	if err := json.Unmarshal([]byte(outer.Result[i:j+1]), &pm); err != nil {
		return trace.Postmortem{}, fmt.Errorf("разбор ответа модели: %w", err)
	}
	if pm.Cause == "" {
		return trace.Postmortem{}, errors.New("в ответе модели пустая причина")
	}
	pm.Source, pm.CostUSD = "model", outer.Cost
	return pm, nil
}

const modelTimeout = 120 * time.Second

const systemPrompt = "Ты разбираешь неудачный прогон агента. Отвечай только JSON-объектом."

func Analyze(ctx context.Context, r trace.Run, model string) (trace.Postmortem, error) {
	return analyze(ctx, r, model, modelTimeout)
}

func analyze(ctx context.Context, r trace.Run, model string, timeout time.Duration) (trace.Postmortem, error) {
	if pm, ok := ByRule(r); ok {
		return pm, nil
	}
	bin := os.Getenv("AGENTOPS_CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	// The analyst needs no tools and no project: an empty directory keeps the
	// repository's files and instructions out of its context.
	dir, err := os.MkdirTemp("", "agentops-analyze-")
	if err != nil {
		return trace.Postmortem{}, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--safe-mode", "--strict-mcp-config", "--restricted",
		"--model", model, "--output-format", "json", "--max-turns", "1",
		"--tools", "", "--system-prompt", systemPrompt)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(BuildPrompt(r))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second // a stuck grandchild must not hold the pipes past the timeout
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return trace.Postmortem{}, errors.New("модель не ответила за 2 минуты")
		}
		msg := fmt.Sprintf("запуск %s: %v", bin, err)
		if se := cut(strings.TrimSpace(stderr.String()), 200); se != "" {
			msg += ": " + se
		}
		return trace.Postmortem{}, errors.New(msg)
	}
	pm, err := Parse(stdout.Bytes())
	if err != nil {
		return pm, err
	}
	if pm.FailedStep < 0 || pm.FailedStep > len(r.Steps) {
		pm.FailedStep = 0
	}
	return pm, nil
}

func Save(runDir string, p trace.Postmortem) error {
	return trace.WriteJSON(filepath.Join(runDir, "postmortem.json"), p)
}
