package runner

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const skillHidden = "[текст навыка не сохраняется]"

// minSkillLine: shorter lines ("---", "# Heading", "4. Run go test") are too common to prove anything.
const minSkillLine = 24

// readSkillTexts reads every file under <plugin>/skills: the skill files the runner gave the agent.
func readSkillTexts(pluginDir string) []string {
	var texts []string
	_ = filepath.WalkDir(filepath.Join(pluginDir, "skills"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, rerr := os.ReadFile(p); rerr == nil {
				texts = append(texts, string(b))
			}
		}
		return nil
	})
	return texts
}

// carriesSkill: the text contains, verbatim, a long enough line of a skill file.
// It does not depend on how the file was read (cat, a glob, find -exec, a pipe, the Skill tool).
func (s Scrub) carriesSkill(text string) bool {
	for _, t := range s.SkillTexts {
		for _, l := range strings.Split(t, "\n") {
			if l = strings.TrimSpace(l); len(l) >= minSkillLine && strings.Contains(text, l) {
				return true
			}
		}
	}
	return false
}

// hideStr takes a JSON string value; anything else, or a string without skill text, is returned as it is.
func hideStr(raw json.RawMessage, s Scrub) json.RawMessage {
	var str string
	if json.Unmarshal(raw, &str) == nil && s.carriesSkill(str) {
		return json.RawMessage(`"` + skillHidden + `"`)
	}
	return raw
}

// hideInBlocks applies hideStr to key of every object in a JSON array.
func hideInBlocks(raw json.RawMessage, key string, s Scrub) json.RawMessage {
	var blocks []obj
	if json.Unmarshal(raw, &blocks) != nil {
		return raw
	}
	for _, b := range blocks {
		if v, ok := b[key]; ok {
			b[key] = hideStr(v, s)
		}
	}
	return marshal(blocks)
}

// hideSkillInUser hides skill text in the tool results and text blocks of a
// whitelisted Claude user line. The agent's own messages and tool inputs are not touched.
func hideSkillInUser(line []byte, s Scrub) []byte {
	var m obj
	var typ string
	if len(s.SkillTexts) == 0 || json.Unmarshal(line, &m) != nil || json.Unmarshal(m["type"], &typ) != nil || typ != "user" {
		return line
	}
	var msg obj
	if json.Unmarshal(m["message"], &msg) != nil {
		return line
	}
	content := msg["content"]
	var blocks []obj
	if json.Unmarshal(content, &blocks) != nil {
		msg["content"] = hideStr(content, s) // plain string content
	} else {
		for _, b := range blocks {
			var bt string
			_ = json.Unmarshal(b["type"], &bt)
			switch bt {
			case "tool_result":
				if c, ok := b["content"]; ok {
					b["content"] = hideInBlocks(hideStr(c, s), "text", s)
				}
			case "text":
				if c, ok := b["text"]; ok {
					b["text"] = hideStr(c, s)
				}
			}
		}
		msg["content"] = marshal(blocks)
	}
	m["message"] = marshal(msg)
	return marshal(m)
}

// hideSkillInCodexOutput hides skill text in the output of a command, whatever the command was.
func hideSkillInCodexOutput(o obj, s Scrub) {
	var item obj
	if len(s.SkillTexts) == 0 || json.Unmarshal(o["item"], &item) != nil {
		return
	}
	if out, ok := item["aggregated_output"]; ok {
		item["aggregated_output"] = hideStr(out, s)
		o["item"] = marshal(item)
	}
}
