package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite cmd/agentops/schema.json")

func TestSchemaIsCurrent(t *testing.T) {
	got := generateSchema()
	if *update {
		if err := os.WriteFile("schema.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(got, schemaJSON) {
		t.Fatal("schema.json is out of date; run: go test ./cmd/agentops -run TestSchemaIsCurrent -update")
	}
}

func allOutputTypes() []reflect.Type {
	return append(slices.Collect(maps.Values(outputTypes)), slices.Collect(maps.Values(sharedTypes))...)
}

func TestEveryJSONFieldHasDoc(t *testing.T) {
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for _, jf := range jsonFields(typ) {
			d := jf.f.Tag.Get("doc")
			if d == "" || utf8.RuneCountInString(d) > 80 {
				t.Errorf("%s.%s: doc tag must be 1..80 characters, got %q", typ.Name(), jf.f.Name, d)
			}
			walk(jf.f.Type)
		}
	}
	for _, typ := range allOutputTypes() {
		walk(typ)
	}
}

func TestSchemaCommand(t *testing.T) {
	var out bytes.Buffer
	if err := runSchema([]string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ID   string                     `json:"$id"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "agentops-output-v1" {
		t.Errorf("$id = %q", doc.ID)
	}
	for _, name := range []string{"ls", "show", "show_step", "why", "check", "run", "analyze", "denials"} {
		if _, ok := doc.Defs[name]; !ok {
			t.Errorf("no $defs entry %q", name)
		}
	}
}

func strict(t *testing.T, name string, raw []byte, into any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Errorf("%s: %v\n%s", name, err, raw)
	}
}

func TestJSONOutputMatchesTypes(t *testing.T) {
	var out bytes.Buffer
	capture := func(err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		b := bytes.Clone(out.Bytes())
		out.Reset()
		return b
	}
	runs := []string{"--runs", fixtureRuns, "--json"}
	strict(t, "ls", capture(runLs(runs, &out, os.Stderr)), &lsOutput{})
	strict(t, "show", capture(runShow(append([]string{"run-02"}, runs...), &out)), &showOutput{})
	strict(t, "show_step", capture(runShow(append([]string{"run-02", "--step", "3"}, runs...), &out)), &showStepOutput{})
	strict(t, "why", capture(runWhy(append([]string{"run-02"}, runs...), &out)), &whyOutput{})
	strict(t, "denials", capture(runDenials(runs, &out, os.Stderr)), &denialsOutput{})
	strict(t, "check", capture(runCheck(append([]string{"--min", "10"}, runs...), &out, os.Stderr)), &checkResult{})

	// analyze: a rule-only diagnosis on a copy, so no model is called and the fixtures stay untouched.
	t.Setenv("AGENTOPS_CLAUDE_BIN", "/nonexistent")
	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, "run-02"), os.DirFS(filepath.Join(fixtureRuns, "run-02"))); err != nil {
		t.Fatal(err)
	}
	strict(t, "analyze", capture(runAnalyze([]string{"--runs", dir, "--force", "--json"}, &out, os.Stderr)), &analyzeOutput{})
	if err := printJSON(&out, analyzeOutput{ID: "run-02", Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != `{"id":"run-02","ok":false,"error":"boom"}`+"\n" {
		t.Errorf("failed analyze line: %s", got)
	}
	out.Reset()

	// run starts a paid agent, so its line is built from the type.
	b, _ := json.Marshal(runOutput{"run-11", "passed", "baseline", "haiku"})
	strict(t, "run", b, &runOutput{})
	if !strings.Contains(string(b), `"scenario":"baseline"`) {
		t.Errorf("run line: %s", b)
	}
}
