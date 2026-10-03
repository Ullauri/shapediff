package main

// post runs the per-MR job of spec section 14.1 from inside a GitLab CI
// merge-request pipeline or a GitHub Actions pull_request workflow. It reads
// the CI environment, gets or extracts both graphs through the cache,
// computes the delta, writes the artifact, and posts or updates the note
// (a GitLab MR note or a GitHub PR comment). It exits 0 on every failure the
// spec says must not block a pipeline.
//
// It expects wiring.go (main thread) to provide:
//
//	func deps(env pipeline.Env, cfg *config.Config, extractor string, log io.Writer) (pipeline.Deps, error)
//
// which fills Extract (the Go extractor, or the plug-in command, at a
// checkout dir and sha), Delta, Render, NoStructural, Notes (a gitlab.Client
// or github.Client adapter, by env.Forge), Graphs/Deltas (cache.FromEnv),
// and KeyParts (Version, toolchain, builder mode, config hash). post
// supplies Log and Checkout itself.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/pipeline"
)

func init() {
	register(command{name: "post", synopsis: "[--dry-run] [--extractor <exe>]    (reads the GitLab CI or GitHub Actions environment)", short: "run the per-MR CI job: extract, delta, render, post the MR note or PR comment", run: runPost})
}

func runPost(args []string) error {
	fs := newFlags("post")
	dryRun := fs.Bool("dry-run", false, "do everything except post the note or comment")
	extractor := fs.String("extractor", os.Getenv("SHAPEDIFF_EXTRACTOR"), "external extractor command (plug-in contract, spec 6.3), e.g. \"python3 shapediff-py\"; default $SHAPEDIFF_EXTRACTOR, else the built-in Go extractor")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	env := pipeline.EnvFrom(os.Getenv)
	env, note := pipeline.ResolveDiffBase(context.Background(), env)
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	cfg, err := loadRepoConfig(env.ProjectDir)
	if err != nil {
		// A broken config must not fail the pipeline either; say so and run with defaults.
		fmt.Fprintf(os.Stderr, "shapediff post: %v; using defaults\n", err)
		cfg = config.Default()
	}
	d, err := deps(env, cfg, *extractor, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shapediff post: %v\n", err)
		return nil
	}
	if *dryRun {
		d.Notes = nil
	}
	d.Log = os.Stderr
	out, err := pipeline.Run(context.Background(), env, d)
	if err != nil {
		// Only a programming error reaches here; still exit 0 per 14.1.
		fmt.Fprintf(os.Stderr, "shapediff post: %v\n", err)
		return nil
	}
	switch {
	case out.Skipped:
		fmt.Fprintf(os.Stderr, "shapediff post: skipped (not a %s run)\n", env.EventName())
	case out.Warning != "":
		fmt.Fprintln(os.Stderr, out.Warning)
	default:
		fmt.Fprintf(os.Stderr, "shapediff post: structural=%v posted=%v updated=%v artifact=%s\n", out.Structural, out.Posted, out.Updated, rel(env.ProjectDir, out.Artifact))
	}
	return nil
}

func rel(base, p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}
