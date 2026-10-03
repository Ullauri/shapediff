package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Ullauri/shapediff/internal/config"
)

// loadConfig loads a config file and prints what Load accepted but wants
// the user to know about (an ignored section, say) on stderr, one line
// each, so a warning is seen where the command ran rather than buried in
// a document. A missing file is the defaults, silently.
func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, "shapediff: config:", w)
	}
	return cfg, nil
}

// loadRepoConfig is loadConfig for the file at a repo root.
func loadRepoConfig(repoDir string) (*config.Config, error) {
	return loadConfig(filepath.Join(repoDir, config.FileName))
}
