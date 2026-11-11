package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillScrub(t *testing.T) (Scrub, []string) {
	t.Helper()
	texts := readSkillTexts("../../plugin")
	if len(texts) == 0 {
		t.Fatal("no skill files under plugin/skills")
	}
	var lines []string
	for _, l := range strings.Split(texts[0], "\n") {
		if l = strings.TrimSpace(l); len(l) >= minSkillLine {
			lines = append(lines, l)
		}
	}
	return Scrub{SkillTexts: texts}, lines
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestContentLayerHidesCodexOutputWhateverTheCommand(t *testing.T) {
	s, lines := skillScrub(t)
	body := strings.Join(lines, "\n") + "\n"
	for _, cmd := range []string{
		"cat .agents/skills/foo/*", "head -50 .agents/skills/foo/S*", "cat $(ls .agents/skills/foo/*.md)",
		"find .agents -type f -exec cat {} +", "find .agents -type f -execdir cat {} +", "rg --files .agents | xargs cat",
		"grep -r . .agents", "ls .agents/skills/foo/* | while read f; do cat \"$f\"; done", "python3 reader.py", "go test ./...",
	} {
		line := `{"type":"item.completed","item":{"id":"a","type":"command_execution","command":` + jsonStr(cmd) + `,"aggregated_output":` + jsonStr("header\n"+body) + `,"exit_code":0,"status":"completed"}}`
		got := string(SanitizeCodex([]byte(line), s, testNow))
		if got == "" || strings.Contains(got, lines[0]) || strings.Contains(got, "Fixing failing") && strings.Contains(got, "Run `go test") || !strings.Contains(got, skillHidden) {
			t.Errorf("%s: %s", cmd, got)
		}
	}
}

func TestContentLayerKeepsWhatIsNotSkillText(t *testing.T) {
	s, lines := skillScrub(t)
	codex := func(sc Scrub, out string) string {
		return string(SanitizeCodex([]byte(`{"type":"item.completed","item":{"id":"a","type":"command_execution","command":"ls","aggregated_output":`+jsonStr(out)+`,"exit_code":0,"status":"completed"}}`), sc, testNow))
	}
	// Short lines shared with the skill prove nothing.
	if got := codex(s, "---\nname: go-test-fixer\n"); strings.Contains(got, skillHidden) {
		t.Errorf("short lines hid the output: %s", got)
	}
	// Without a plugin directory nothing changes.
	if got := codex(Scrub{}, lines[0]); strings.Contains(got, skillHidden) || !strings.Contains(got, lines[0]) {
		t.Errorf("no skill texts, yet: %s", got)
	}
	// The agent's own words and its tool inputs are not touched.
	msg := string(SanitizeCodex([]byte(`{"type":"item.completed","item":{"id":"a","type":"agent_message","text":`+jsonStr("I read: "+lines[0])+`}}`), s, testNow))
	if strings.Contains(msg, skillHidden) || !strings.Contains(msg, lines[0]) {
		t.Errorf("agent_message hidden: %s", msg)
	}
	use := string(Sanitize([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":`+jsonStr(lines[0])+`},{"type":"tool_use","id":"t","name":"Bash","input":{"command":`+jsonStr("echo "+lines[0])+`}}]}}`), s))
	if strings.Contains(use, skillHidden) || strings.Count(use, lines[0]) != 2 {
		t.Errorf("assistant line changed: %s", use)
	}
}

func TestContentLayerHidesClaudeUserMessages(t *testing.T) {
	s, lines := skillScrub(t)
	body := strings.Join(lines, "\n")
	for name, line := range map[string]string{
		"skill body as a text block":  `{"type":"user","message":{"role":"user","content":[{"type":"text","text":` + jsonStr("Base directory for this skill: /plugin/skills/go-test-fixer\n\n"+body) + `}]}}`,
		"skill body as plain content": `{"type":"user","message":{"role":"user","content":` + jsonStr("Base directory: /x\n"+body) + `}}`,
		"Read of the skill file":      `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + jsonStr("     1→---\n     5→"+lines[0]+"\n") + `}]}}`,
		"tool result as text blocks":  `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":` + jsonStr(body) + `}]}]}}`,
	} {
		got := string(Sanitize([]byte(line), s))
		if got == "" || strings.Contains(got, lines[0]) || !strings.Contains(got, skillHidden) {
			t.Errorf("%s: %s", name, got)
		}
		if strings.Contains(name, "tool result") || strings.Contains(name, "Read") {
			if !strings.Contains(got, `"tool_use_id":"t1"`) {
				t.Errorf("%s lost its tool_use_id: %s", name, got)
			}
		}
	}
	// A result that only shares short lines stays; so does a user line when nothing is configured.
	plain := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"---\nok  \tshop\t0.2s\n"}]}}`
	if got := string(Sanitize([]byte(plain), s)); strings.Contains(got, skillHidden) {
		t.Errorf("plain result hidden: %s", got)
	}
	long := strings.Replace(plain, "ok  \\tshop\\t0.2s", lines[0], 1)
	if got := string(Sanitize([]byte(long), Scrub{})); strings.Contains(got, skillHidden) {
		t.Errorf("no skill texts, yet hidden: %s", got)
	}
}

func TestSkillBodyNeverReachesTheRecordedTrace(t *testing.T) {
	e := setupCodex(t, "catskill")
	id, err := skillRun(t, e, "codex", "../../plugin")
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := os.ReadFile(filepath.Join(e.runs, id, "trace.jsonl"))
	_, lines := skillScrub(t)
	for _, l := range lines {
		if strings.Contains(string(tr), l) {
			t.Errorf("trace carries skill line %q", l)
		}
	}
	if !strings.Contains(string(tr), skillHidden) {
		t.Errorf("the cat output should be replaced:\n%s", tr)
	}
}
