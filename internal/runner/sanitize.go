package runner

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type obj = map[string]json.RawMessage
type subs = map[string]func(json.RawMessage) json.RawMessage

// The published trace is a whitelist: raw lines carry account and rate-limit
// data, credential expiry, request ids, host paths, plugin lists and duplicated
// tool payloads, and an unknown field cannot be proven clean.
var (
	tokenKeys = []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"}
	usage     = func(r json.RawMessage) json.RawMessage { return filter(r, tokenKeys, nil) }
	blockText = each([]string{"type", "text"}, nil, true)

	assistantBlocks = each([]string{"type", "text", "thinking", "id", "name", "input"}, nil, false)
	userBlocks      = each([]string{"type", "text", "tool_use_id", "content", "is_error"}, subs{"content": blockText}, true)

	// No "tools": the list fingerprints the account's enabled features and nothing reads it.
	initKeys = []string{"type", "subtype", "model", "permissionMode", "claude_code_version"}
	denials  = each([]string{"tool_name", "tool_use_id"}, nil, false)
)

// Scrub names what must not leave the machine: tool output can carry the
// workdir, the temp directory (go build paths), the home directory, the login
// and full name (an `ls -la` owner column, a git author) and the host name.
type Scrub struct{ Workdir, TempDir, Home, User, FullName, Host, PluginDir, CodexHome string }

// Sanitize returns nil when the line must not be published.
func Sanitize(line []byte, s Scrub) []byte {
	out := whitelist(line)
	if out == nil {
		return nil
	}
	return scrub(out, s)
}

// scrub is shared by trace lines, the verdict's test output and the agent's stderr.
// The workdir lies inside the temp directory, so it goes first.
func scrub(b []byte, s Scrub) []byte {
	replace := func(from, to string) {
		if len(from) >= 3 { // a tiny value would eat unrelated text
			b = bytes.ReplaceAll(b, []byte(from), []byte(to))
		}
	}
	replacePath := func(dir, to string) {
		if dir != "" {
			for _, d := range pathSpellings(dir) {
				replace(d, to)
			}
		}
	}
	replacePath(s.Workdir, "/work")
	replacePath(s.PluginDir, "/plugin") // a skill's output names its directory, inside the checkout
	b = scratchRe.ReplaceAll(b, []byte("/scratch$1"))
	replacePath(s.TempDir, "/tmp")
	if s.CodexHome != "" && (s.Home == "" || !strings.HasPrefix(s.CodexHome, s.Home)) {
		replacePath(s.CodexHome, "~/.codex")
	}
	replace(s.Home, "~")
	for _, r := range secretRes {
		b = r.re.ReplaceAll(b, []byte(r.to))
	}
	short, _, _ := strings.Cut(s.Host, ".")
	names := [][2]string{{s.Host, "host"}, {short, "host"}, {s.FullName, "user"}, {s.User, "user"}}
	// Longest first: a full name may contain the login.
	slices.SortStableFunc(names, func(a, b [2]string) int { return len(b[0]) - len(a[0]) })
	for _, n := range names {
		if utf8.RuneCountInString(n[0]) >= 3 {
			b = replaceWord(b, n[0], n[1])
		}
	}
	return b
}

// secretRes redact credentials that a tool output or an error message may echo.
var secretRes = []struct {
	re *regexp.Regexp
	to string
}{
	{regexp.MustCompile(`sk-[A-Za-z0-9_*-]{8,}`), "sk-…"},
	{regexp.MustCompile(`org-[A-Za-z0-9]{8,}`), "org-…"},
	{regexp.MustCompile(`req_[A-Za-z0-9]{8,}`), "req_…"},
	{regexp.MustCompile(`Bearer [A-Za-z0-9._-]{8,}`), "Bearer …"},
}

// pathEnd are the characters that end a path in a serialized line or a shell
// command, so the text after it survives.
const pathEnd = `"\\\s` + "`" + `'),;:|\]}<>`

// scratchRe is Claude Code's own scratch directory: its name spells out the
// per-user temp directory and the run. The root alone counts too; a longer
// name such as /tmp/claude-501-foo is someone else's and is left alone.
var scratchRe = regexp.MustCompile(`(?:/private)?/tmp/claude-[0-9]+(?:/[^` + pathEnd + `]*)?([` + pathEnd + `]|$)`)

// escapeBefore is what may end right before a name in serialized JSON and still
// be a boundary although its last character is a letter: \n, \t, \r, a \uXXXX
// escape (control characters), or an ANSI sequence such as a colour code.
var escapeBefore = regexp.MustCompile(`(\\[ntr]|\\u[0-9a-fA-F]{4}|(\\u001[bB]|\x1b)\[[0-9;?]*[@-~])$`)

// replaceWord replaces v case-insensitively where it stands as a whole word, so
// that with login "max" the enum value error_max_turns survives.
//
// ponytail: works on raw JSON, so a login equal to a JSON key ("type") would
// rewrite that key; scrub the decoded strings if such a login ever shows up.
func replaceWord(b []byte, v, to string) []byte {
	var out []byte
	last := 0
	for _, m := range regexp.MustCompile(`(?i)`+regexp.QuoteMeta(v)).FindAllIndex(b, -1) {
		before, _ := utf8.DecodeLastRune(b[:m[0]])
		after, _ := utf8.DecodeRune(b[m[1]:])
		escaped := escapeBefore.Match(b[max(0, m[0]-32):m[0]])
		if (isWordRune(before) && !escaped) || isWordRune(after) {
			continue
		}
		out = append(append(out, b[last:m[0]]...), to...)
		last = m[1]
	}
	return append(out, b[last:]...)
}

// marshal is json.Marshal without HTML escaping: "<alice>" written as
// "\u003calice\u003e" would hide the word boundary from replaceWord.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func whitelist(line []byte) []byte {
	var m obj
	if json.Unmarshal(line, &m) != nil || m == nil {
		return nil
	}
	var typ, sub string
	json.Unmarshal(m["type"], &typ)
	json.Unmarshal(m["subtype"], &sub)

	var keys []string
	var nested subs
	switch {
	case typ == "system" && sub == "init":
		keys = initKeys
	case typ == "assistant":
		keys = []string{"type", "timestamp", "parent_tool_use_id", "message"}
		nested = subs{"message": func(r json.RawMessage) json.RawMessage {
			return filter(r, []string{"role", "model", "content", "usage"}, subs{"content": assistantBlocks, "usage": usage})
		}}
	case typ == "user":
		keys = []string{"type", "timestamp", "parent_tool_use_id", "message"}
		nested = subs{"message": func(r json.RawMessage) json.RawMessage {
			return filter(r, []string{"role", "content"}, subs{"content": userBlocks})
		}}
	case typ == "result":
		keys = []string{"type", "subtype", "is_error", "duration_ms", "duration_api_ms", "num_turns", "result",
			"total_cost_usd", "permission_denials", "stop_reason", "terminal_reason", "usage"}
		nested = subs{"usage": usage, "permission_denials": denials}
	default:
		return nil
	}
	return filter(line, keys, nested)
}

// filter returns nil when raw is not an object, and a nil result of a nested
// func drops that key: a value of an unexpected shape cannot be proven clean.
func filter(raw json.RawMessage, keys []string, nested subs) json.RawMessage {
	var m obj
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil
	}
	out := obj{}
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if f := nested[k]; f != nil {
			if v = f(v); v == nil {
				continue
			}
		}
		out[k] = v
	}
	return marshal(out)
}

// each applies filter to every element of a JSON array. A string passes through
// only where a plain string is a legitimate value (tool_result content).
func each(keys []string, nested subs, allowString bool) func(json.RawMessage) json.RawMessage {
	return func(raw json.RawMessage) json.RawMessage {
		var str string
		if allowString && json.Unmarshal(raw, &str) == nil {
			return raw
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return nil
		}
		for i, it := range items {
			if items[i] = filter(it, keys, nested); items[i] == nil {
				return nil
			}
		}
		return marshal(items)
	}
}

// pathSpellings returns the directory as given and symlink-resolved, longest
// first: on macOS /var/folders/… and /private/var/folders/… name the same place,
// and replacing the short one first would leave a "/private/work" behind.
func pathSpellings(dir string) []string {
	out := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		out = append(out, real)
	}
	if len(out) == 2 && len(out[1]) > len(out[0]) {
		out[0], out[1] = out[1], out[0]
	}
	return out
}
