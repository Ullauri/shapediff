package main

// post runs the per-MR job of spec section 14.1 from inside a GitLab CI
// merge-request pipeline. It reads the CI environment, gets or extracts
// both graphs through the cache, computes the delta, writes the artifact,
// and posts or updates the MR note. It exits 0 on every failure the spec
// says must not block a pipeline.
//
// It expects wiring.go (main thread) to provide:
//
//	func deps(env pipeline.Env, cfg *config.Config, log io.Writer) (pipeline.Deps, error)
//
// which fills Extract (the Go extractor at a checkout dir and sha), Delta,
// Render, NoStructural, Notes (a gitlab.Client adapter), Graphs/Deltas
// (cache.FromEnv), and KeyParts (Version, toolchain, builder mode, config
// hash). post supplies Log and Checkout itself.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/pipeline"
)

func init() {
	register(command{name: "post", short: "run the per-MR CI job: extract, delta, render, post the MR note", run: runPost})
}

func runPost(args []string) error {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "do everything except talk to the GitLab notes API")
	if err := fs.Parse(args); err != nil {
		return err
	}
	env := pipeline.EnvFrom(os.Getenv)
	cfg, err := config.LoadRepo(env.ProjectDir)
	if err != nil {
		// A broken config must not fail the pipeline either; say so and run with defaults.
		fmt.Fprintf(os.Stderr, "shapediff post: %v; using defaults\n", err)
		cfg = config.Default()
	}
	d, err := deps(env, cfg, os.Stderr)
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
		fmt.Fprintln(os.Stderr, "shapediff post: skipped (not a merge_request_event pipeline)")
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
