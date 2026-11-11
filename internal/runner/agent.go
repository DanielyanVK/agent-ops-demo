package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// agent is what differs between the agents; the timeout, interrupt, verdict and
// cleanup paths in Run are shared.
type agent struct {
	name, binEnv string
	// prepare returns the command line and the extra environment for a run in workdir.
	prepare func(o Options, workdir string) (args, env []string, err error)
	// sanitize returns nil when the line must not be published.
	sanitize func(line []byte, s Scrub, now time.Time) []byte
	// finished: the sanitized line is the agent's final report.
	finished func(clean []byte) bool
	// keys are the agent's own credentials and settings; the common list is added.
	keys []string
	// timeoutLine ends a trace of an agent that ran out of time.
	timeoutLine func(elapsed time.Duration, now time.Time) string
}

var agents = map[string]agent{
	"claude": {
		name: "claude", binEnv: "AGENTOPS_CLAUDE_BIN", keys: []string{"ANTHROPIC_API_KEY"},
		prepare: func(o Options, _ string) ([]string, []string, error) {
			return agentArgs(o), []string{"TZ=UTC"}, nil // tool output such as `ls -la` prints local time
		},
		sanitize: func(line []byte, s Scrub, _ time.Time) []byte { return Sanitize(line, s) },
		finished: func(clean []byte) bool { return lineType(clean) == "result" },
		timeoutLine: func(elapsed time.Duration, _ time.Time) string {
			return fmt.Sprintf(`{"type":"result","subtype":"timeout","is_error":true,"duration_ms":%d}`, elapsed.Milliseconds())
		},
	},
	"codex": {
		name: "codex", binEnv: "AGENTOPS_CODEX_BIN", keys: []string{"OPENAI_API_KEY", "CODEX_HOME"},
		prepare:  codexPrepare,
		sanitize: SanitizeCodex,
		finished: func(clean []byte) bool { t := lineType(clean); return t == "turn.completed" || t == "turn.failed" },
		// Codex has no timeout event of its own: the parser reads this line as stop reason "timeout".
		timeoutLine: func(_ time.Duration, now time.Time) string {
			return `{"type":"timeout","ts":"` + now.UTC().Format(tsLayout) + `"}`
		},
	},
}

func lineType(clean []byte) string {
	var h struct{ Type string }
	if json.Unmarshal(clean, &h) != nil {
		return ""
	}
	return h.Type
}

// codexIsolation: the profile's plugins, skills, hooks, memories and connectors
// would otherwise enter the agent's context and the published trace.
var codexIsolation = []string{"apps", "plugins", "remote_plugin", "plugin_sharing", "hooks", "memories", "skill_search",
	"skill_mcp_dependency_install", "multi_agent", "browser_use", "browser_use_external", "computer_use",
	"image_generation", "goals", "tool_suggest"}

// codexSkillFeatures stay enabled when the scenario is about skills. Plugins do
// not: with them on, the agent reads whatever third-party skills the profile has;
// the demo skill is placed in the project instead (see installSkills).
var codexSkillFeatures = []string{"skill_search", "skill_mcp_dependency_install"}

func codexPrepare(o Options, workdir string) ([]string, []string, error) {
	// Inside the sandbox Go's default build cache is not writable and every `go test` would fail.
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, nil, err
	}
	gocache := filepath.Join(cache, "agentops", "gocache")
	if err := os.MkdirAll(gocache, 0o755); err != nil {
		return nil, nil, err
	}
	args := []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
		"-s", o.Scenario.CodexSandbox}
	for _, f := range codexIsolation {
		if o.Scenario.Skills && slices.Contains(codexSkillFeatures, f) {
			continue
		}
		args = append(args, "--disable", f)
	}
	args = append(args, "--add-dir", gocache, "-c", "shell_environment_policy.inherit=all")
	if o.Model != "" {
		args = append(args, "-m", o.Model)
	}
	args = append(args, "-C", workdir, o.Scenario.Prompt)
	return args, []string{"GOCACHE=" + gocache, "TZ=UTC"}, nil
}

func checkAgent(o Options) (agent, error) {
	a, ok := agents[o.Agent]
	if !ok {
		return a, fmt.Errorf("неизвестный агент %q, доступны: claude, codex", o.Agent)
	}
	if a.name == "codex" && o.Scenario.CodexSandbox == "" {
		return a, fmt.Errorf("сценарий %s не поддерживается агентом codex", o.Scenario.Name)
	}
	return a, nil
}

// agentEnvAllow is what every agent may inherit; each agent adds only its own
// provider key (agent.keys). Nothing else from the parent shell reaches the trace.
// A proxy and a certificate file are here because an agent behind a corporate
// proxy cannot reach its model without them.
var agentEnvAllow = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TERM",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE"}

// agentEnv is the allow-listed part of the current environment plus extra.
func (a agent) env(extra []string) []string {
	var env []string
	for _, k := range append(slices.Clone(agentEnvAllow), a.keys...) {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// installSkills puts the skills of the plugin where Codex looks for project skills.
func installSkills(pluginDir, workdir string) error {
	entries, err := os.ReadDir(filepath.Join(pluginDir, "skills"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dst := filepath.Join(workdir, ".agents", "skills", e.Name())
		if err := copyTree(filepath.Join(pluginDir, "skills", e.Name()), dst); err != nil {
			return err
		}
	}
	return nil
}
