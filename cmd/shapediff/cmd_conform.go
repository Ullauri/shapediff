package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shapediff/internal/config"
	"shapediff/internal/conform"
	"shapediff/internal/goextract"
	"shapediff/internal/graph"
)

func init() {
	register(command{name: "conform", short: "check a graph document, or drive an extractor over the fixture scenarios", run: runConform})
}

func runConform(args []string) error {
	fs := flag.NewFlagSet("conform", flag.ContinueOnError)
	check := fs.String("check", "", "graph document to check against the section 6.3 invariants")
	fixtures := fs.String("fixtures", "", "fixture directory (conformance/<language>) to run the scenarios in")
	extractor := fs.String("extractor", "", "external extractor executable; default is the built-in Go extractor")
	only := fs.String("only", "", "comma-separated scenario names to run")
	narrow := fs.Int("narrow-targets", 2, "k for reachability")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check != "" {
		doc, err := graph.ReadFile(*check)
		if err != nil {
			return err
		}
		probs := graph.Validate(doc)
		if len(probs) == 0 {
			fmt.Printf("%s: conforms (%d packages, %d nodes, %d edges)\n", *check, len(doc.Packages), len(doc.Nodes), len(doc.Edges))
			return nil
		}
		for _, p := range probs {
			fmt.Println(p)
		}
		return fmt.Errorf("%d violations", len(probs))
	}
	if *fixtures == "" {
		return fmt.Errorf("--check <graph.json> or --fixtures <dir> is required")
	}
	dir, err := filepath.Abs(*fixtures)
	if err != nil {
		return err
	}
	var ex conform.Extractor
	if *extractor != "" {
		ex = conform.Exe{Path: *extractor}
	} else {
		ex = builtinExtractor()
	}
	r := &conform.Runner{Extractor: ex, FixturesDir: dir, NarrowTargets: *narrow}
	if *only != "" {
		r.Only = strings.Split(*only, ",")
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		return err
	}
	rep.Write(os.Stdout)
	if !rep.OK() {
		return fmt.Errorf("conformance failed")
	}
	return nil
}

// builtinExtractor adapts the Go extractor to the plug-in contract.
func builtinExtractor() conform.Extractor {
	return conform.Func{Label: "builtin go-xtools " + Version, Fn: func(ctx context.Context, repoDir, configPath string) (*graph.Document, error) {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, err
		}
		return goextract.ExtractAll(ctx, goextract.Options{RepoDir: repoDir, Repo: filepath.Base(filepath.Dir(repoDir)), SHA: filepath.Base(repoDir), Version: Version, Config: cfg})
	}}
}
