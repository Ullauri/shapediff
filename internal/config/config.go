// Package config loads .shapediff.yml (spec section 13), applies defaults and
// hashes the result into extractor.config_hash.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	LLM      LLM    `yaml:"llm"`
	// WeeklyURL points a reader who is behind on the repo at the weekly
	// structure brief (section 14.2). The per-MR note answers "what did this
	// MR change"; the brief answers "what has this repo become", which is a
	// different question and a different cadence.
	WeeklyURL string `yaml:"weekly_url,omitempty"`
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

// LLM is the optional narration rewrite backend (section 11). Endpoint is
// any OpenAI-compatible chat completions URL; empty means no rewrite.
type LLM struct {
	Endpoint  string `yaml:"endpoint"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
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
// the normal case. Any other read or parse error is returned.
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
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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
