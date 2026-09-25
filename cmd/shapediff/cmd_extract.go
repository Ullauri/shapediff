package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/conform"
	"github.com/Ullauri/shapediff/internal/goextract"
	"github.com/Ullauri/shapediff/internal/graph"
)

func init() {
	register(command{name: "extract", short: "extract the graph of a checkout (schema v1 on stdout or -o)", run: runExtract})
}

func runExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	repo := fs.String("repo", ".", "checkout root")
	cfgPath := fs.String("config", "", "config path (default <repo>/.shapediff.yml)")
	sha := fs.String("sha", "", "commit sha to record (default: git rev-parse HEAD)")
	slug := fs.String("repo-slug", "", "repo id to record (default: from the git remote, else the directory name)")
	extractor := fs.String("extractor", "", "external extractor executable (plug-in contract, spec 6.3); default is the built-in Go extractor")
	out := fs.String("o", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repoDir, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if *cfgPath == "" {
		*cfgPath = filepath.Join(repoDir, config.FileName)
	}
	ctx := context.Background()
	var doc *graph.Document
	if *extractor != "" {
		doc, err = conform.Exe{Path: *extractor}.Extract(ctx, repoDir, *cfgPath)
		if err != nil {
			return err
		}
		if probs := graph.Validate(doc); len(probs) > 0 {
			return fmt.Errorf("external extractor output violates the schema:\n  %s", strings.Join(probs, "\n  "))
		}
	} else {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		if *sha == "" {
			*sha = gitHead(repoDir)
		}
		if *slug == "" {
			*slug = repoSlug(repoDir)
		}
		doc, err = goextract.ExtractAll(ctx, goextract.Options{RepoDir: repoDir, Repo: *slug, SHA: *sha, Version: Version, Config: cfg})
		if err != nil {
			return err
		}
	}
	if *out == "" {
		return doc.Write(os.Stdout)
	}
	return doc.WriteFile(*out)
}

func gitHead(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

// repoSlug derives "group/name" from the origin remote, else the directory
// name.
func repoSlug(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	b, err := cmd.Output()
	if err == nil {
		u := strings.TrimSpace(string(b))
		u = strings.TrimSuffix(u, ".git")
		if i := strings.LastIndex(u, ":"); i >= 0 && !strings.Contains(u[i:], "/") {
			u = u[i+1:]
		}
		parts := strings.Split(strings.Trim(u, "/"), "/")
		if len(parts) >= 2 {
			return strings.Join(parts[len(parts)-2:], "/")
		}
	}
	return filepath.Base(dir)
}
