package runner

import (
	"encoding/json"
	"time"
)

// tsLayout: UTC, RFC 3339 with milliseconds.
const tsLayout = "2006-01-02T15:04:05.000Z"

var (
	str = func(r json.RawMessage) json.RawMessage {
		var v string
		if json.Unmarshal(r, &v) != nil {
			return nil
		}
		return r
	}
	num = func(r json.RawMessage) json.RawMessage {
		var v *float64 // null is fine: exit_code of a command still running
		if json.Unmarshal(r, &v) != nil {
			return nil
		}
		return r
	}
	codexUsage = func(r json.RawMessage) json.RawMessage {
		return filter(r, tokenKeysCodex, subs{"input_tokens": num, "cached_input_tokens": num, "output_tokens": num, "reasoning_output_tokens": num})
	}
	tokenKeysCodex = []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens"}
	codexChanges   = each([]string{"path", "kind"}, subs{"path": str, "kind": str}, false)
)

// codexItem keeps, per item type, only the fields the parser reads. An unknown
// item type is dropped whole: its fields cannot be proven clean.
func codexItem(r json.RawMessage) json.RawMessage {
	var h struct{ Type string }
	if json.Unmarshal(r, &h) != nil {
		return nil
	}
	var keys []string
	nested := subs{"id": str, "type": str}
	add := func(k string, f func(json.RawMessage) json.RawMessage) { keys = append(keys, k); nested[k] = f }
	keys = []string{"id", "type"}
	switch h.Type {
	case "agent_message", "reasoning":
		add("text", str)
	case "command_execution":
		add("command", str)
		add("aggregated_output", str)
		add("exit_code", num)
		add("status", str)
	case "file_change":
		add("changes", codexChanges)
		add("status", str)
	case "mcp_tool_call":
		add("server", str)
		add("tool", str)
		add("status", str)
	case "web_search":
		add("query", str)
	case "error":
		add("message", str)
	default:
		return nil
	}
	return filter(r, keys, nested)
}

var codexStarted = func(r json.RawMessage) json.RawMessage {
	return filter(r, []string{"id", "type"}, subs{"id": str, "type": str})
}

// SanitizeCodex is the whitelist for `codex exec --json` lines, with the same
// scrub as for Claude. It adds "ts": the agent's events carry no time. Returns
// nil when the line must not be published.
func SanitizeCodex(line []byte, s Scrub, now time.Time) []byte {
	var m obj
	if json.Unmarshal(line, &m) != nil || m == nil {
		return nil
	}
	var typ string
	if json.Unmarshal(m["type"], &typ) != nil {
		return nil
	}
	var keys []string
	var nested subs
	switch typ {
	case "thread.started", "turn.started": // thread_id is never kept
		keys = []string{"type"}
	case "turn.completed":
		keys, nested = []string{"type", "usage"}, subs{"usage": codexUsage}
	case "turn.failed":
		keys = []string{"type", "error"}
		nested = subs{"error": func(r json.RawMessage) json.RawMessage { return filter(r, []string{"message"}, subs{"message": str}) }}
	case "item.started":
		keys, nested = []string{"type", "item"}, subs{"item": codexStarted}
	case "item.completed":
		keys, nested = []string{"type", "item"}, subs{"item": codexItem}
	default:
		return nil
	}
	out := filter(line, keys, nested)
	if out == nil {
		return nil
	}
	if (typ == "item.started" || typ == "item.completed") && !hasKey(out, "item") {
		return nil
	}
	var o obj
	_ = json.Unmarshal(out, &o)
	o["ts"] = json.RawMessage(`"` + now.UTC().Format(tsLayout) + `"`)
	return scrub(marshal(o), s)
}

func hasKey(raw []byte, k string) bool {
	var m obj
	_ = json.Unmarshal(raw, &m)
	_, ok := m[k]
	return ok
}
