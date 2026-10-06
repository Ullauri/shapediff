package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAndHash(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Budget.Nodes != 24 || c.NarrowTargets != 2 || c.Narration != "template" {
		t.Fatalf("defaults: %+v", c)
	}
	h1 := c.Hash()
	os.WriteFile(filepath.Join(dir, FileName), []byte("budget:\n  nodes: 24\n"), 0o644)
	c2, _ := LoadRepo(dir)
	if c2.Hash() != h1 {
		t.Error("explicit default should hash the same as the implicit one")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("budget:\n  nodes: 30\nnarration: off\n"), 0o644)
	c3, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Hash() == h1 || c3.Budget.Edges != 45 || c3.Narration != "off" {
		t.Errorf("changed config: %+v", c3)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("narration: prose\n"), 0o644)
	if _, err := LoadRepo(dir); err == nil {
		t.Error("bad narration value accepted")
	}
	if c.Baseline != "on" {
		t.Errorf("baseline default = %q", c.Baseline)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("baseline: off\n"), 0o644)
	if c4, err := LoadRepo(dir); err != nil || c4.Baseline != "off" || c4.Hash() == h1 {
		t.Errorf("baseline: off: %+v %v", c4, err)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("baseline: maybe\n"), 0o644)
	if _, err := LoadRepo(dir); err == nil {
		t.Error("bad baseline value accepted")
	}
}

func TestUnknownKeysAreErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ yml, want string }{
		{"budgte:\n  nodes: 3\n", `unknown key "budgte" (did you mean "budget"?)`},
		{"budget:\n  chras: 900\n", `unknown key "chras" (did you mean "chars"?)`},
		{"narraton: off\n", `unknown key "narraton" (did you mean "narration"?)`},
		{"ignor: []\n", `unknown key "ignor" (did you mean "ignore"?)`},
		{"entrypoints:\n  worker:\n    - calls: x.Register\n", `unknown key "calls" (did you mean "call"?)`},
		{"components:\n  - id: storage\n", `unknown key "components"`},
	}
	for _, tc := range cases {
		os.WriteFile(filepath.Join(dir, FileName), []byte(tc.yml), 0o644)
		_, err := LoadRepo(dir)
		if err == nil {
			t.Errorf("%q: accepted", tc.yml)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q does not say %q", tc.yml, err, tc.want)
		}
	}
}

func TestLLMSectionIsIgnoredWithAWarning(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("narration: llm\nllm:\n  endpoint: https://attacker.example/v1\n  api_key_env: SHAPEDIFF_TOKEN\n"), 0o644)
	c, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Narration != "llm" {
		t.Errorf("narration = %q", c.Narration)
	}
	if c.IgnoredLLM != (llmSettings{}) {
		t.Errorf("llm section kept: %+v", c.IgnoredLLM)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "SHAPEDIFF_LLM_ENDPOINT") {
		t.Errorf("warnings = %q", c.Warnings)
	}
	// The ignored section must not change the cache key either.
	os.WriteFile(filepath.Join(dir, FileName), []byte("narration: llm\n"), 0o644)
	plain, _ := LoadRepo(dir)
	if plain.Hash() != c.Hash() {
		t.Error("an ignored llm section changed the config hash")
	}
}

func TestEmptyFileIsTheDefaults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("# nothing set\n"), 0o644)
	c, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash() != Default().Hash() {
		t.Error("an empty file should hash as the defaults")
	}
}

func TestExampleFileShowsTheDefaults(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", ".shapediff.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("example warns: %q", c.Warnings)
	}
	if c.Hash() != Default().Hash() {
		t.Errorf("the example config does not load as the defaults: %+v", c)
	}
}
