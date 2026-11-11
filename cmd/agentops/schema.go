package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"io"
	"os"
	"reflect"
	"strings"

	"agentops/internal/trace"
)

func init() {
	commands = append(commands, command{
		name:  "schema",
		usage: "схема JSON-вывода команд",
		run:   func(args []string) error { return runSchema(args, os.Stdout) },
	})
}

// schema.json is generated from the output types by TestSchemaIsCurrent -update.
//
//go:embed schema.json
var schemaJSON []byte

func runSchema(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	jsonFlag(fs) // the output is JSON anyway
	rest, err := parseFlags(fs, args, stdout)
	if err == nil {
		err = noArgs(rest)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(schemaJSON)
	return err
}

// outputTypes: one $defs entry per command output.
var outputTypes = map[string]reflect.Type{
	"ls":        reflect.TypeFor[lsOutput](),
	"denials":   reflect.TypeFor[denialsOutput](),
	"show":      reflect.TypeFor[showOutput](),
	"show_step": reflect.TypeFor[showStepOutput](),
	"why":       reflect.TypeFor[whyOutput](),
	"check":     reflect.TypeFor[checkResult](),
	"run":       reflect.TypeFor[runOutput](),
	"analyze":   reflect.TypeFor[analyzeOutput](),
}

// sharedTypes are emitted once and referenced by $ref.
var sharedTypes = map[string]reflect.Type{
	"run_brief":  reflect.TypeFor[trace.RunBrief](),
	"step_brief": reflect.TypeFor[trace.StepBrief](),
	"stats":      reflect.TypeFor[trace.StatsJSON](),
	"postmortem": reflect.TypeFor[trace.Postmortem](),
	"verdict":    reflect.TypeFor[trace.Verdict](),
}

type jsonField struct {
	name      string
	omitEmpty bool
	f         reflect.StructField
}

// jsonFields lists the JSON fields of a struct in encoding order; embedded structs are flattened as encoding/json does.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	for f := range t.Fields() {
		if f.Anonymous {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		out = append(out, jsonField{name, strings.Contains(opts, "omitempty"), f})
	}
	return out
}

func generateSchema() []byte {
	defs := map[string]any{}
	for name, t := range outputTypes {
		defs[name] = objectSchema(t)
	}
	for name, t := range sharedTypes {
		defs[name] = objectSchema(t)
	}
	doc := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     "agentops-output-v1",
		"title":   "agentops --json output",
		"$defs":   defs,
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func objectSchema(t reflect.Type) map[string]any {
	props, required := map[string]any{}, []string{}
	for _, jf := range jsonFields(t) {
		props[jf.name] = fieldSchema(jf)
		if !jf.omitEmpty {
			required = append(required, jf.name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func fieldSchema(jf jsonField) map[string]any {
	t := jf.f.Type
	ptr := t.Kind() == reflect.Pointer
	if ptr {
		t = t.Elem()
	}
	s := typeSchema(t)
	if d := jf.f.Tag.Get("doc"); d != "" {
		s["description"] = d
	}
	if e := jf.f.Tag.Get("enum"); e != "" {
		vals := []any{}
		for v := range strings.SplitSeq(e, ",") {
			vals = append(vals, v)
		}
		s["enum"] = vals
	}
	// A pointer that is left out when nil (omitempty) never carries null.
	if ptr && !jf.omitEmpty {
		if ref, ok := s["$ref"]; ok {
			delete(s, "$ref")
			s["anyOf"] = []any{map[string]any{"$ref": ref}, map[string]any{"type": "null"}}
		} else {
			s["type"] = []any{s["type"], "null"}
		}
	}
	return s
}

func typeSchema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Struct:
		for name, st := range sharedTypes {
			if st == t {
				return map[string]any{"$ref": "#/$defs/" + name}
			}
		}
		return objectSchema(t)
	}
	panic("schema: unsupported type " + t.String())
}
