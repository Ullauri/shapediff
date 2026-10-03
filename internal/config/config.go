// Package config loads .shapediff.yml (spec section 13), applies defaults and
// hashes the result into extractor.config_hash.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the repo-root config file.
const FileName = ".shapediff.yml"

// Config is the loaded repo configuration with defaults applied.
type Config struct {
	Modules       []string                  `yaml:"modules"`
	BuildTags     []string                  `yaml:"build_tags"`
	Ignore        []string                  `yaml:"ignore"`
	Entrypoints   map[string][]EntryPattern `yaml:"entrypoints"`
	Sinks         map[string][]SinkPattern  `yaml:"sinks"`
	Budget        Budget                    `yaml:"budget"`
	NarrowTargets int                       `yaml:"narrow_targets"`
	Narration     string                    `yaml:"narration"`
	// Baseline is the "where this lands" section of the note (spec 10.1):
	// on (default) or off.
	Baseline string `yaml:"baseline"`
	// WeeklyURL points a reader who is behind on the repo at the weekly
	// structure brief (section 14.2). The per-MR note answers "what did this
	// MR change"; the brief answers "what has this repo become", which is a
	// different question and a different cadence.
	WeeklyURL string `yaml:"weekly_url,omitempty"`
	// IgnoredLLM accepts the llm section an earlier version read, so that a
	// file written for it still loads. It is never used: the rewrite backend
	// (section 11) is configured by the CI environment, SHAPEDIFF_LLM_ENDPOINT,
	// SHAPEDIFF_LLM_MODEL and SHAPEDIFF_LLM_KEY, and never by the repo. The
	// repo config travels with the change under review, so a value here
	// would let any branch choose the server a CI secret is sent to. Load
	// records a warning and zeroes it.
	IgnoredLLM llmSettings `yaml:"llm,omitempty"`
	// Warnings is what Load accepted but wants the user to know about, one
	// line each. Callers print them; the hash ignores them.
	Warnings []string `yaml:"-"`
}

type llmSettings struct {
	Endpoint  string `yaml:"endpoint,omitempty"`
	Model     string `yaml:"model,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
}

// EntryPattern is a configured entry point registration: a call to a
// function with (name literal, handler) shape.
type EntryPattern struct {
	Call string `yaml:"call"` // fully qualified function or method, e.g. github.com/acme/jobs.Register
}

// SinkPattern is a configured sink: calls into a package.
type SinkPattern struct {
	Package string `yaml:"package"`
}

// Budget is section 9's S, B and E.
type Budget struct {
	Chars int `yaml:"chars"`
	Nodes int `yaml:"nodes"`
	Edges int `yaml:"edges"`
}

// Default returns the configuration with every field at its default.
func Default() *Config {
	c := base()
	c.finalize()
	return c
}

// base is the configuration before derived fields (edges from nodes) are
// filled, so a file that sets nodes alone gets the derived edge cap.
func base() *Config {
	return &Config{
		Modules:       []string{"."},
		BuildTags:     []string{},
		Ignore:        []string{"**/mocks/**", "**/testdata/**"},
		Entrypoints:   map[string][]EntryPattern{},
		Sinks:         map[string][]SinkPattern{},
		Budget:        Budget{Chars: 1800, Nodes: 24},
		NarrowTargets: 2,
		Narration:     "template",
		Baseline:      "on",
	}
}

// Load reads the config at path. A missing file yields the defaults; that is
// the normal case. Any other read or parse error is returned, and so is a
// key the config does not have: a misspelled key would otherwise be
// indistinguishable from an omitted one, and the file says every key is
// optional.
func Load(path string) (*Config, error) {
	c := base()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		c.finalize()
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, describe(err))
	}
	if c.IgnoredLLM != (llmSettings{}) {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%s: the llm section is ignored; the rewrite backend is set by SHAPEDIFF_LLM_ENDPOINT, SHAPEDIFF_LLM_MODEL and SHAPEDIFF_LLM_KEY in the CI environment, never by the repo config", path))
		c.IgnoredLLM = llmSettings{}
	}
	c.finalize()
	switch c.Narration {
	case "template", "llm", "off":
	case "":
		c.Narration = "template"
	default:
		return nil, fmt.Errorf("%s: narration must be template, llm or off, got %q", path, c.Narration)
	}
	switch c.Baseline {
	case "on", "off":
	case "":
		c.Baseline = "on"
	default:
		return nil, fmt.Errorf("%s: baseline must be on or off, got %q", path, c.Baseline)
	}
	return c, nil
}

func (c *Config) finalize() {
	if len(c.Modules) == 0 {
		c.Modules = []string{"."}
	}
	if c.Budget.Chars <= 0 {
		c.Budget.Chars = 1800
	}
	if c.Budget.Nodes <= 0 {
		c.Budget.Nodes = 24
	}
	if c.Budget.Edges <= 0 {
		c.Budget.Edges = c.Budget.Nodes * 3 / 2
	}
	if c.NarrowTargets <= 0 {
		c.NarrowTargets = 2
	}
}

// LoadRepo loads the config from a repo root.
func LoadRepo(repoDir string) (*Config, error) {
	return Load(filepath.Join(repoDir, FileName))
}

// Hash is the sha256 of the config as loaded with defaults applied, encoded
// as yaml so the hash is independent of the source file's formatting.
func (c *Config) Hash() string {
	b, err := yaml.Marshal(c)
	if err != nil {
		panic(err) // yaml.Marshal of a plain struct cannot fail
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---- error messages ----

// unknownField is the shape of yaml.v3's strict-decoding complaint.
var unknownField = regexp.MustCompile(`field (\S+) not found in type config\.(\w+)`)

// sections maps the type names yaml.v3 reports to the structs, so an
// unknown key can be answered with the keys that section does have.
var sections = map[string]reflect.Type{
	"Config":       reflect.TypeOf(Config{}),
	"Budget":       reflect.TypeOf(Budget{}),
	"EntryPattern": reflect.TypeOf(EntryPattern{}),
	"SinkPattern":  reflect.TypeOf(SinkPattern{}),
	"llmSettings":  reflect.TypeOf(llmSettings{}),
}

// describe rewrites a decoding error in the user's terms: "unknown key
// budgte (did you mean budget?)" rather than a Go type name, one line per
// problem, keeping the line numbers yaml.v3 reports.
func describe(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err
	}
	lines := make([]string, 0, len(te.Errors))
	for _, e := range te.Errors {
		m := unknownField.FindStringSubmatch(e)
		if m == nil {
			lines = append(lines, e)
			continue
		}
		key, typ := m[1], m[2]
		msg := fmt.Sprintf("unknown key %q", key)
		if s := suggest(key, keysOf(typ)); s != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", s)
		}
		lines = append(lines, unknownField.ReplaceAllLiteralString(e, msg))
	}
	if len(lines) == 1 {
		return errors.New(lines[0])
	}
	return errors.New("\n  " + strings.Join(lines, "\n  "))
}

// keysOf lists the yaml keys of a section's struct, sorted.
func keysOf(typ string) []string {
	t, ok := sections[typ]
	if !ok {
		return nil
	}
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if tag != "" && tag != "-" && tag != "llm" {
			keys = append(keys, tag)
		}
	}
	sort.Strings(keys)
	return keys
}

// suggest is the known key closest to a mistyped one, or "" when none is
// close: within two edits, or a prefix of at least three characters.
func suggest(key string, known []string) string {
	best, bestD := "", 3
	for _, k := range known {
		d := editDistance(key, k)
		if len(key) >= 3 && strings.HasPrefix(k, key) {
			d = 1
		}
		if d < bestD || (d == bestD && k < best) {
			best, bestD = k, d
		}
	}
	return best
}

// editDistance is the Damerau-Levenshtein distance restricted to adjacent
// transpositions, so "budgte" is one edit from "budget".
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}
