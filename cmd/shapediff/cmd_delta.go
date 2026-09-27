package main

import (
	"os"
	"os/exec"
	"strings"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

func init() {
	register(command{name: "delta", synopsis: "--base base.json --head head.json [--repo .] [--mr-iid N] [-o delta.json]", short: "subtract a base graph from a head graph", run: runDelta})
}

func runDelta(args []string) error {
	fs := newFlags("delta")
	basePath := fs.String("base", "", "base graph (required)")
	headPath := fs.String("head", "", "head graph (required)")
	repoDir := fs.String("repo", "", "checkout for git rename detection (optional)")
	cfgPath := fs.String("config", "", "repo config for narrow_targets (optional)")
	mrIID := fs.Int("mr-iid", 0, "merge request iid to record")
	out := fs.String("o", "", "output file (default stdout)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *basePath == "" || *headPath == "" {
		return usagef("--base and --head are required")
	}
	base, err := graph.ReadFile(*basePath)
	if err != nil {
		return err
	}
	head, err := graph.ReadFile(*headPath)
	if err != nil {
		return err
	}
	cfg := config.Default()
	if *cfgPath != "" {
		if cfg, err = config.Load(*cfgPath); err != nil {
			return err
		}
	}
	opts := delta.Options{NarrowTargets: cfg.NarrowTargets, MRIID: *mrIID}
	if *repoDir != "" {
		opts.GitRenames = gitRenames(*repoDir, base.SHA, head.SHA)
	}
	d := delta.Compute(base, head, opts)
	if *out == "" {
		return d.Write(os.Stdout)
	}
	return d.WriteFile(*out)
}

// gitRenames returns base path -> head path for files git reports as
// renamed between the two commits. Any failure yields an empty map; the
// engine only uses it to widen similarity matching.
func gitRenames(dir, base, head string) map[string]string {
	out := map[string]string{}
	if base == "" || head == "" {
		return out
	}
	cmd := exec.Command("git", "diff", "-M", "--name-status", base, head)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && strings.HasPrefix(f[0], "R") {
			out[f[1]] = f[2]
		}
	}
	return out
}
