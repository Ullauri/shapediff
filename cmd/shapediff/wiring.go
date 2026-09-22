package main

// wiring.go joins the packages for the post and weekly commands: the Go
// extractor, the delta engine, the renderer, the GitLab client and the
// caches. Everything here is glue; the behaviour lives in the packages.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"shapediff/internal/cache"
	"shapediff/internal/config"
	"shapediff/internal/delta"
	"shapediff/internal/gitlab"
	"shapediff/internal/goextract"
	"shapediff/internal/graph"
	"shapediff/internal/pipeline"
	"shapediff/internal/render"
)

// notesAdapter narrows gitlab.Client to what the pipeline needs.
type notesAdapter struct {
	c *gitlab.Client
	p gitlab.Project
}

func (a notesAdapter) FindMarkedNote(ctx context.Context, iid int, marker string) (int, string, bool, error) {
	n, err := a.c.FindMarkedNote(ctx, a.p, iid, marker)
	if err != nil || n == nil {
		return 0, "", false, err
	}
	return n.ID, n.Body, true, nil
}

func (a notesAdapter) CreateNote(ctx context.Context, iid int, body string) error {
	_, err := a.c.CreateMRNote(ctx, a.p, iid, body)
	return err
}

func (a notesAdapter) UpdateNote(ctx context.Context, iid, noteID int, body string) error {
	_, err := a.c.UpdateMRNote(ctx, a.p, iid, noteID, body)
	return err
}

func projectOf(env pipeline.Env) gitlab.Project {
	if env.ProjectID != "" {
		return gitlab.ProjectPath(env.ProjectID)
	}
	return gitlab.ProjectPath(env.ProjectPath)
}

func extractFn(cfg *config.Config, repo string) func(ctx context.Context, repoDir, sha string) (*graph.Document, error) {
	return func(ctx context.Context, repoDir, sha string) (*graph.Document, error) {
		return goextract.ExtractAll(ctx, goextract.Options{RepoDir: repoDir, Repo: repo, SHA: sha, Version: Version, Config: cfg})
	}
}

func deltaFn(cfg *config.Config, repoDir string, mrIID int) func(base, head *graph.Document) (*delta.Delta, error) {
	return func(base, head *graph.Document) (*delta.Delta, error) {
		opts := delta.Options{NarrowTargets: cfg.NarrowTargets, MRIID: mrIID}
		if repoDir != "" {
			opts.GitRenames = gitRenames(repoDir, base.SHA, head.SHA)
		}
		return delta.Compute(base, head, opts), nil
	}
}

func keyParts(ctx context.Context, cfg *config.Config, repoDir string) pipeline.KeyParts {
	tc, err := goextract.Toolchain(ctx, repoDir, nil)
	if err != nil {
		tc = "unknown"
	}
	return pipeline.KeyParts{Version: Version, Toolchain: tc, BuilderMode: goextract.BuilderMode, ConfigHash: cfg.Hash()}
}

func stripMarker(s string) string {
	return strings.TrimPrefix(strings.TrimPrefix(s, render.Marker), "\n")
}

// deps builds the per-MR pipeline dependencies (see cmd_post.go).
func deps(env pipeline.Env, cfg *config.Config, log io.Writer) (pipeline.Deps, error) {
	ctx := context.Background()
	repoDir := env.ProjectDir
	if repoDir == "" {
		repoDir = "."
	}
	repo := env.Repo()
	var client *gitlab.Client
	if env.APIURL != "" && env.Token != "" {
		client = &gitlab.Client{BaseURL: env.APIURL, Token: env.Token}
	}
	store, err := cache.FromEnv(cache.EnvSpec{Value: env.CacheSpec, ProjectDir: repoDir, Client: client, Project: projectOf(env)})
	if err != nil {
		return pipeline.Deps{}, fmt.Errorf("cache: %w", err)
	}
	d := pipeline.Deps{
		Extract: extractFn(cfg, repo),
		Delta:   deltaFn(cfg, repoDir, env.MRIID),
		Render: func(dl *delta.Delta, base, head *graph.Document) (string, error) {
			started := time.Now()
			note, _, warnings, err := renderNote(dl, graph.NewIndex(head), graph.NewIndex(base), cfg, env.ProjectURL, started)
			for _, w := range warnings {
				fmt.Fprintln(log, "shapediff: "+w)
			}
			return stripMarker(note), err
		},
		NoStructural: func(sha string) string { return stripMarker(render.NoStructuralNote(sha)) },
		Graphs:       store,
		Deltas:       store,
		KeyParts:     keyParts(ctx, cfg, repoDir),
		Log:          log,
	}
	if client != nil {
		d.Notes = notesAdapter{c: client, p: projectOf(env)}
	} else {
		fmt.Fprintln(log, "shapediff: no CI_API_V4_URL or SHAPEDIFF_TOKEN; notes will not be posted")
	}
	return d, nil
}

// ---- weekly ----

type weeklyRepo struct {
	Slug       string         `yaml:"slug"`
	Dir        string         `yaml:"dir"`
	Branch     string         `yaml:"branch"`
	ProjectRaw string         `yaml:"project"`
	ProjectURL string         `yaml:"url"`
	Project    gitlab.Project `yaml:"-"`
}

type weeklyDepsT struct {
	Repos       []weeklyRepo
	Client      *gitlab.Client
	WikiProject gitlab.Project
	Graphs      cache.Store
	Deltas      cache.Store
	LayoutDir   string
	Extract     func(ctx context.Context, repoDir, sha string) (*graph.Document, error)
	Delta       func(base, head *graph.Document) (*delta.Delta, error)
	KeyParts    pipeline.KeyParts
}

// weeklyReposFile is the YAML the weekly job reads.
type weeklyReposFile struct {
	WikiProject string       `yaml:"wiki_project"`
	LayoutDir   string       `yaml:"layout_dir"`
	Cache       string       `yaml:"cache"` // dir:<path> | gitlab
	Repos       []weeklyRepo `yaml:"repos"`
}

// weeklyDeps reads the repos file (see cmd_weekly.go) and builds the
// dependencies. GitLab access comes from CI_API_V4_URL and SHAPEDIFF_TOKEN.
func weeklyDeps(reposFile string, dryRun bool) (weeklyDepsT, error) {
	b, err := os.ReadFile(reposFile)
	if err != nil {
		return weeklyDepsT{}, err
	}
	var f weeklyReposFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return weeklyDepsT{}, fmt.Errorf("%s: %w", reposFile, err)
	}
	if len(f.Repos) == 0 {
		return weeklyDepsT{}, fmt.Errorf("%s: no repos", reposFile)
	}
	wd := weeklyDepsT{WikiProject: gitlab.ProjectPath(f.WikiProject), LayoutDir: f.LayoutDir}
	if wd.LayoutDir == "" {
		wd.LayoutDir = filepath.Join(filepath.Dir(reposFile), "layouts")
	}
	if !dryRun && os.Getenv("CI_API_V4_URL") != "" && os.Getenv("SHAPEDIFF_TOKEN") != "" {
		wd.Client = &gitlab.Client{BaseURL: os.Getenv("CI_API_V4_URL"), Token: os.Getenv("SHAPEDIFF_TOKEN")}
	}
	store, err := cache.FromEnv(cache.EnvSpec{Value: f.Cache, ProjectDir: filepath.Dir(reposFile), Client: wd.Client, Project: wd.WikiProject})
	if err != nil {
		return weeklyDepsT{}, err
	}
	wd.Graphs, wd.Deltas = store, store
	for i := range f.Repos {
		r := &f.Repos[i]
		if r.Branch == "" {
			r.Branch = "main"
		}
		if r.Dir == "" {
			return weeklyDepsT{}, fmt.Errorf("repo %s: dir is required", r.Slug)
		}
		r.Project = gitlab.ProjectPath(r.ProjectRaw)
		if r.ProjectRaw == "" {
			r.Project = gitlab.ProjectPath(r.Slug)
		}
	}
	wd.Repos = f.Repos
	// one config and key set per run: the weekly job uses each repo's own
	// .shapediff.yml for extraction but the wiki job's defaults for budgets
	first := f.Repos[0]
	cfg, err := config.LoadRepo(first.Dir)
	if err != nil {
		cfg = config.Default()
	}
	wd.Extract = func(ctx context.Context, repoDir, sha string) (*graph.Document, error) {
		rc, err := config.LoadRepo(repoDir)
		if err != nil {
			rc = config.Default()
		}
		return goextract.ExtractAll(ctx, goextract.Options{RepoDir: repoDir, Repo: slugFor(f.Repos, repoDir), SHA: sha, Version: Version, Config: rc})
	}
	wd.Delta = deltaFn(cfg, "", 0)
	wd.KeyParts = keyParts(context.Background(), cfg, first.Dir)
	return wd, nil
}

func slugFor(repos []weeklyRepo, dir string) string {
	for _, r := range repos {
		if r.Dir == dir || strings.HasPrefix(dir, r.Dir) {
			return r.Slug
		}
	}
	return filepath.Base(dir)
}
