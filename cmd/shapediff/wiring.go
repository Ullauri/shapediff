package main

// wiring.go joins the packages for the post and weekly commands: the Go
// extractor, the delta engine, the renderer, the GitLab and GitHub clients
// and the caches. Everything here is glue; the behaviour lives in the
// packages.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Ullauri/shapediff/internal/cache"
	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/conform"
	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/forge"
	"github.com/Ullauri/shapediff/internal/github"
	"github.com/Ullauri/shapediff/internal/gitlab"
	"github.com/Ullauri/shapediff/internal/goextract"
	"github.com/Ullauri/shapediff/internal/graph"
	"github.com/Ullauri/shapediff/internal/pipeline"
	"github.com/Ullauri/shapediff/internal/render"
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

// commentsAdapter is the GitHub side of pipeline.NotesAPI: the comments of
// a pull request. It trims a note to GitHub's comment limit on the way out,
// and only takes a comment for its note when the token's own user wrote it.
type commentsAdapter struct {
	c   *github.Client
	r   github.Repo
	log io.Writer
	// author is whose comment is the note: SHAPEDIFF_COMMENT_AUTHOR, else
	// the token's login, else GITHUB_TOKEN's bot. Resolved on first use.
	author string
}

func (a *commentsAdapter) whose(ctx context.Context) string {
	if a.author == "" {
		if login, err := a.c.Login(ctx); err == nil {
			a.author = login
		} else {
			a.author = github.ActionsBot
		}
	}
	return a.author
}

func (a *commentsAdapter) FindMarkedNote(ctx context.Context, number int, marker string) (int, string, bool, error) {
	c, err := a.c.FindMarkedComment(ctx, a.r, number, marker, a.whose(ctx))
	if err != nil || c == nil {
		return 0, "", false, err
	}
	return int(c.ID), c.Body, true, nil
}

func (a *commentsAdapter) fit(body string) string {
	fitted, trimmed := github.Fit(body)
	if trimmed {
		fmt.Fprintf(a.log, "shapediff: note is %d bytes, over GitHub's %d; posted %d\n", len(body), github.MaxBody, len(fitted))
	}
	return fitted
}

func (a *commentsAdapter) CreateNote(ctx context.Context, number int, body string) error {
	_, err := a.c.CreateComment(ctx, a.r, number, a.fit(body))
	return err
}

func (a *commentsAdapter) UpdateNote(ctx context.Context, _ int, id int, body string) error {
	_, err := a.c.UpdateComment(ctx, a.r, int64(id), a.fit(body))
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

// pluginExtractFn drives an external extractor over the plug-in contract of
// section 6.3, the same seam the extract and conform commands use.
func pluginExtractFn(exe string) func(ctx context.Context, repoDir, sha string) (*graph.Document, error) {
	return func(ctx context.Context, repoDir, sha string) (*graph.Document, error) {
		// The config comes from the worktree being extracted, not the
		// checkout: weeklyGraph hands this an older sha, and that sha's own
		// .shapediff.yml is the one that described it.
		doc, err := conform.Exe{Path: exe}.Extract(ctx, repoDir, filepath.Join(repoDir, config.FileName))
		if err != nil {
			return nil, err
		}
		if probs := graph.Validate(doc); len(probs) > 0 {
			return nil, fmt.Errorf("external extractor output violates the schema:\n  %s", strings.Join(probs, "\n  "))
		}
		return doc, nil
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

// pluginToolchain stands in for the Go toolchain in the cache key when the
// graphs come from a plug-in extractor: a hash of the command line and of
// every file it names, the interpreter included when it is one. Output of
// the Python extractor differs between interpreters (3.9 has no
// sys.stdlib_module_names, so it takes the standard library for third-party
// modules), and a key that did not change with them would serve one
// interpreter's graph to another.
func pluginToolchain(command string) string {
	h := sha256.New()
	h.Write([]byte(command))
	for _, f := range strings.Fields(command) {
		p := f
		if lp, err := exec.LookPath(f); err == nil {
			p = lp
		}
		if b, err := os.ReadFile(p); err == nil {
			h.Write([]byte{0})
			h.Write(b)
		}
	}
	return "plugin:" + hex.EncodeToString(h.Sum(nil))[:16]
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

// deps builds the per-MR pipeline dependencies (see cmd_post.go). extractor
// is a plug-in command (spec 6.3); "" is the built-in Go extractor.
func deps(env pipeline.Env, cfg *config.Config, extractor string, log io.Writer) (pipeline.Deps, error) {
	ctx := context.Background()
	repoDir := env.ProjectDir
	if repoDir == "" {
		repoDir = "."
	}
	repo := env.Repo()
	var client *gitlab.Client
	if env.Forge != forge.GitHub && env.APIURL != "" && env.Token != "" {
		client = &gitlab.Client{BaseURL: env.APIURL, Token: env.Token}
	}
	if env.Forge == forge.GitHub && strings.TrimSpace(env.CacheSpec) == "gitlab" {
		return pipeline.Deps{}, fmt.Errorf("cache: SHAPEDIFF_CACHE=gitlab is the GitLab package registry; on GitHub leave it unset and cache .shapediff-cache/ with actions/cache, or use dir:<path>")
	}
	store, err := cache.FromEnv(cache.EnvSpec{Value: env.CacheSpec, ProjectDir: repoDir, Client: client, Project: projectOf(env)})
	if err != nil {
		return pipeline.Deps{}, fmt.Errorf("cache: %w", err)
	}
	extract, kp := extractFn(cfg, repo), keyParts(ctx, cfg, repoDir)
	if extractor != "" {
		extract = pluginExtractFn(extractor)
		kp.Toolchain, kp.BuilderMode = pluginToolchain(extractor), "plugin"
	}
	d := pipeline.Deps{
		Extract: extract,
		Delta:   deltaFn(cfg, repoDir, env.MRIID),
		Render: func(dl *delta.Delta, base, head *graph.Document) (string, error) {
			started := time.Now()
			note, _, warnings, err := renderNote(dl, graph.NewIndex(head), graph.NewIndex(base), cfg, env.ProjectURL, env.Forge, started)
			for _, w := range warnings {
				fmt.Fprintln(log, "shapediff: "+w)
			}
			return stripMarker(note), err
		},
		NoStructural: func(sha string) string { return stripMarker(render.NoStructuralNote(sha)) },
		Graphs:       store,
		Deltas:       store,
		KeyParts:     kp,
		Log:          log,
	}
	switch {
	case env.Forge == forge.GitHub && env.APIURL != "" && env.Token != "":
		d.Notes = &commentsAdapter{c: &github.Client{BaseURL: env.APIURL, Token: env.Token}, r: github.Repo(env.ProjectPath), log: log, author: env.CommentAuthor}
	case env.Forge == forge.GitHub:
		fmt.Fprintln(log, "shapediff: no GITHUB_TOKEN or SHAPEDIFF_TOKEN; the comment will not be posted")
	case client != nil:
		d.Notes = notesAdapter{c: client, p: projectOf(env)}
	default:
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
	// Cleanup, when set, is called once the run is over.
	Cleanup func()
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
	cfg, err := loadRepoConfig(first.Dir)
	if err != nil {
		cfg = config.Default()
	}
	wd.Extract = func(ctx context.Context, repoDir, sha string) (*graph.Document, error) {
		rc, err := loadRepoConfig(repoDir)
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

// weeklyLocalDeps builds the weekly job for one local checkout, with no repos
// file, no GitLab and no wiki. It is what makes the brief of section 14.2
// runnable by a developer who is behind on a repo rather than only by the
// scheduled job — see the reader named in section 2.
//
// The graph cache is deliberately a fresh temporary directory. The cache key
// of section 14.1 covers the extractor version and toolchain, and neither is
// known before the first extraction when the extractor is a plug-in; a local
// run is two extractions of a few hundred milliseconds, so there is nothing
// to gain by risking a stale hit against a different interpreter.
func weeklyLocalDeps(repoDir, branch, extractor string) (weeklyDepsT, error) {
	dir, err := filepath.Abs(repoDir)
	if err != nil {
		return weeklyDepsT{}, err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return weeklyDepsT{}, fmt.Errorf("%s is not a git checkout", dir)
	}
	if branch == "" {
		branch, err = gitBranch(dir)
		if err != nil {
			return weeklyDepsT{}, err
		}
	}
	cfg, err := loadRepoConfig(dir)
	if err != nil {
		cfg = config.Default()
	}
	slug := repoSlug(dir)
	tmp, err := os.MkdirTemp("", "shapediff-weekly-")
	if err != nil {
		return weeklyDepsT{}, err
	}
	store := cache.Dir{Path: tmp}
	wd := weeklyDepsT{
		Repos:  []weeklyRepo{{Slug: slug, Dir: dir, Branch: branch}},
		Graphs: store,
		Deltas: store,
		// Neither the temp directory nor the checkout: weeklyRepoWeek writes
		// the diagram SVG here and tells the reader to go and find it, so it
		// has to outlive the run — but shapediff has no standing to leave an
		// untracked directory in someone else's repo.
		LayoutDir: localLayoutDir(slug),
		Delta:     deltaFn(cfg, "", 0),
		KeyParts:  keyParts(context.Background(), cfg, dir),
		Cleanup:   func() { os.RemoveAll(tmp) },
	}
	if extractor != "" {
		wd.Extract = pluginExtractFn(extractor)
	} else {
		wd.Extract = extractFn(cfg, slug)
	}
	return wd, nil
}

// gitBranch is the checkout's current branch. It does not guess: defaulting
// to "main" on a detached HEAD gave a silent "not reported this week" and a
// zero exit on a repo whose branch is called something else.
func gitBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the current branch of %s; pass --branch: %w", dir, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" || s == "HEAD" {
		return "", fmt.Errorf("%s is on a detached HEAD; pass --branch", dir)
	}
	return s, nil
}

// localLayoutDir is where a local brief keeps pinned layout positions and the
// diagram SVG, under the user's cache directory rather than the target repo.
func localLayoutDir(slug string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "shapediff", "layouts", strings.ReplaceAll(slug, "/", "__"))
}
