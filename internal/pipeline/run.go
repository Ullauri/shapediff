package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/cache"
	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Marker is the first line of every note the job posts; it is how the job
// finds its own note on the next push.
const Marker = "<!-- shapediff:v1 -->"

// ArtifactPath is the delta artifact, relative to the project dir. It is
// always written, structural or not, so a reader can tell "nothing
// structural" from "the job did not run"; its outcome field says which.
const ArtifactPath = "shapediff/delta.json"

// NotePath is the note the job would post, or did post, written beside the
// delta whenever the job got as far as rendering one: what a dry run shows,
// and what a pull request whose token cannot comment still gets.
const NotePath = "shapediff/note.md"

// NotesAPI is the slice of the forge's client the job needs: the notes of a
// GitLab MR, or the comments of a GitHub pull request.
type NotesAPI interface {
	FindMarkedNote(ctx context.Context, iid int, marker string) (noteID int, body string, found bool, err error)
	CreateNote(ctx context.Context, iid int, body string) error
	UpdateNote(ctx context.Context, iid, noteID int, body string) error
}

// DryRunNotes is the NotesAPI of a dry run: it looks the existing note up
// through the real API when there is one (a read), and says what it would
// create or update instead of doing it.
type DryRunNotes struct {
	Real NotesAPI
	Log  io.Writer
}

func (d DryRunNotes) FindMarkedNote(ctx context.Context, iid int, marker string) (int, string, bool, error) {
	if d.Real == nil {
		return 0, "", false, nil
	}
	return d.Real.FindMarkedNote(ctx, iid, marker)
}

func (d DryRunNotes) CreateNote(_ context.Context, iid int, body string) error {
	fmt.Fprintf(d.Log, "dry run: would create the note on %d (%d bytes)\n", iid, len(body))
	return nil
}

func (d DryRunNotes) UpdateNote(_ context.Context, iid, noteID int, body string) error {
	fmt.Fprintf(d.Log, "dry run: would update note %d on %d (%d bytes)\n", noteID, iid, len(body))
	return nil
}

// KeyParts are the non-SHA parts of the graph cache key, known before
// extraction because they describe the extractor, not the code.
type KeyParts struct {
	Version     string
	Toolchain   string
	BuilderMode string
	ConfigHash  string
}

// Deps are the injected collaborators.
type Deps struct {
	// Extract produces the graph for sha, given a checkout at repoDir.
	Extract func(ctx context.Context, repoDir, sha string) (*graph.Document, error)
	// Delta subtracts base from head.
	Delta func(base, head *graph.Document) (*delta.Delta, error)
	// Render produces the full MR note body (without the marker).
	Render func(d *delta.Delta, base, head *graph.Document) (string, error)
	// NoStructural produces the one-line note body for "no structural change at <sha>".
	NoStructural func(headSHA string) string
	Notes        NotesAPI
	// DryRun does everything but post: the note is rendered and written
	// to NotePath, the existing note is looked up, and Outcome says what
	// would have happened.
	DryRun   bool
	Graphs   cache.Store
	Deltas   cache.Store
	KeyParts KeyParts
	Log      io.Writer
	// Checkout materializes sha into a directory and returns a cleanup. Nil
	// means AddWorktree. Tests inject a no-op.
	Checkout func(ctx context.Context, repoDir, sha string) (dir string, cleanup func(), err error)
	// Now is for the elapsed time in the note; nil means time.Now.
	Now func() time.Time
}

// Outcome is what happened. Run never fails the pipeline for a reason the
// spec says must not fail it; those reasons land in Warning.
type Outcome struct {
	Skipped    bool   // not a merge_request_event
	Structural bool   // stats.structural on this push
	Posted     bool   // a new note was created (would be, on a dry run)
	Updated    bool   // the existing marked note was updated (would be, on a dry run)
	DryRun     bool   // nothing was posted; Posted and Updated say what would have been
	Artifact   string // path of the delta artifact written
	Note       string // path of the note written beside it, when one was rendered
	Warning    string // one line, when something went wrong but the job exits 0
	HeadSHA    string
	BaseSHA    string
}

func (d *Deps) logf(format string, args ...any) {
	if d.Log != nil {
		fmt.Fprintf(d.Log, format+"\n", args...)
	}
}

// Run executes steps 1 to 6 of section 14.1. The returned error is non-nil
// only for programming errors (missing deps); every runtime failure is an
// Outcome.Warning so the job exits 0.
func Run(ctx context.Context, env Env, deps Deps) (Outcome, error) {
	var out Outcome
	if deps.Extract == nil || deps.Delta == nil || deps.Render == nil || deps.Graphs == nil || deps.Deltas == nil {
		return out, fmt.Errorf("pipeline: missing dependency")
	}
	if deps.NoStructural == nil {
		deps.NoStructural = func(sha string) string { return fmt.Sprintf("no structural change at %s", sha) }
	}
	if deps.Checkout == nil {
		deps.Checkout = func(ctx context.Context, repoDir, sha string) (string, func(), error) {
			wt, err := AddWorktree(ctx, repoDir, sha)
			if err != nil {
				return "", nil, err
			}
			return wt.Dir, func() { wt.Remove() }, nil
		}
	}
	if deps.DryRun {
		deps.Notes = DryRunNotes{Real: deps.Notes, Log: logWriter(deps.Log)}
		out.DryRun = true
	}
	if !env.IsMergeRequestEvent() {
		out.Skipped = true
		deps.logf("pipeline source %q is not %s; nothing to do", env.PipelineSource, env.EventName())
		return out, nil
	}
	out.HeadSHA = env.HeadSHA()
	out.BaseSHA = strings.TrimSpace(env.DiffBaseSHA)
	if out.HeadSHA == "" || out.BaseSHA == "" {
		out.Warning = "head or base SHA missing from the CI environment"
		deps.logf("%s", out.Warning)
		out.Artifact = WriteFailureArtifact(env, out.Warning)
		return out, nil
	}
	repo := env.Repo()

	head, err := deps.graphFor(ctx, env, repo, out.HeadSHA, true)
	if err != nil {
		return deps.extractionFailed(ctx, env, out, "head", err), nil
	}
	base, err := deps.graphFor(ctx, env, repo, out.BaseSHA, false)
	if err != nil {
		return deps.extractionFailed(ctx, env, out, "base", err), nil
	}

	d, err := deps.Delta(base, head)
	if err != nil {
		return deps.extractionFailed(ctx, env, out, "delta", err), nil
	}
	d.MRIID = env.MRIID
	d.Repo = repo
	d.BaseSHA, d.HeadSHA = out.BaseSHA, out.HeadSHA
	if len(base.Packages) == 0 {
		d.Warnings = append(d.Warnings, "base "+shortSHA(out.BaseSHA)+" has no packages, so every definition at head counts as added")
	}
	d.Outcome = delta.OutcomeOK
	out.Structural = d.Stats.Structural

	// Step 4: always publish the delta, as artifact and in the MR cache.
	art := filepath.Join(env.ProjectDir, ArtifactPath)
	if err := os.MkdirAll(filepath.Dir(art), 0o755); err == nil {
		if err := d.WriteFile(art); err != nil {
			deps.logf("write artifact: %v", err)
		} else {
			out.Artifact = art
		}
	}
	if b, err := d.Marshal(); err == nil {
		if err := deps.Deltas.Put(ctx, cache.MRKey(repo, env.MRIID, out.HeadSHA), b); err != nil {
			deps.logf("cache delta: %v", err)
		}
	}

	// Step 5: the note. It is rendered whenever the change is structural
	// and written beside the delta, so a dry run, a pull request whose
	// token cannot comment, and the artifact's reader all get it; it is
	// posted only when there is an API and a merge request, and then only
	// when structural (an existing note is always updated).
	var body string
	var renderErr error
	if out.Structural {
		body, renderErr = deps.Render(d, base, head)
		if renderErr != nil {
			out.Warning = "render failed: " + renderErr.Error()
			deps.logf("%s", out.Warning)
			body = "shapediff: " + out.Warning
		}
	} else {
		body = deps.NoStructural(out.HeadSHA)
	}
	body = Marker + "\n" + body
	out.Note = writeNote(env, body, &deps)
	switch {
	case env.MRIID == 0:
		deps.logf("no merge request iid in the environment; artifact only")
		return out, nil
	case deps.Notes == nil:
		deps.logf("no notes API (no token); artifact only")
		return out, nil
	}
	noteID, _, found, err := deps.Notes.FindMarkedNote(ctx, env.MRIID, Marker)
	if err != nil {
		out.Warning = "could not list MR notes: " + err.Error()
		deps.logf("%s", out.Warning)
		return out, nil
	}
	if !found {
		switch {
		case renderErr != nil:
			return out, nil
		case !out.Structural:
			deps.logf("no structural change and no existing note; not posting")
			return out, nil
		}
	}
	if found {
		if err := deps.Notes.UpdateNote(ctx, env.MRIID, noteID, body); err != nil {
			out.Warning = "update note: " + err.Error()
			deps.logf("%s", out.Warning)
			return out, nil
		}
		out.Updated = true
		return out, nil
	}
	if err := deps.Notes.CreateNote(ctx, env.MRIID, body); err != nil {
		out.Warning = "create note: " + err.Error()
		deps.logf("%s", out.Warning)
		return out, nil
	}
	out.Posted = true
	return out, nil
}

// graphFor is steps 2 and 3: cache get, else extract and cache put. The head
// is assumed to be the job's own checkout when isHead and the checkout's
// HEAD matches; anything else is materialized with Checkout.
func (deps *Deps) graphFor(ctx context.Context, env Env, repo, sha string, isHead bool) (*graph.Document, error) {
	key := cache.Key{Repo: repo, SHA: sha, ExtractorVersion: deps.KeyParts.Version, Toolchain: deps.KeyParts.Toolchain, BuilderMode: deps.KeyParts.BuilderMode, ConfigHash: deps.KeyParts.ConfigHash}.String()
	if b, ok, err := deps.Graphs.Get(ctx, key); err != nil {
		deps.logf("cache get %s: %v (extracting)", key, err)
	} else if ok {
		g, err := graph.Read(strings.NewReader(string(b)))
		if err == nil {
			deps.logf("cache hit %s", key)
			return g, nil
		}
		deps.logf("cache entry %s unreadable: %v (extracting)", key, err)
	}
	dir := env.ProjectDir
	var cleanup func()
	useCheckout := !isHead
	if isHead {
		if cur, err := HeadSHA(ctx, env.ProjectDir); err != nil || cur != sha {
			useCheckout = true
		}
	}
	if useCheckout {
		d, c, err := deps.Checkout(ctx, env.ProjectDir, sha)
		if err != nil {
			return nil, err
		}
		dir, cleanup = d, c
		defer cleanup()
	}
	g, err := deps.Extract(ctx, dir, sha)
	if err != nil {
		if !isHead && errors.Is(err, graph.ErrNoPackages) {
			// The base has nothing to extract: the change adds the first
			// code, or the first go.mod. That is a graph with nothing in
			// it, not a failure. It is not cached, so a base that is empty
			// because of a configuration mistake is looked at again once
			// the mistake is fixed.
			deps.logf("base %s: %v; every definition at head counts as added", shortSHA(sha), err)
			return &graph.Document{SchemaVersion: graph.SchemaVersion, Repo: repo, SHA: sha}, nil
		}
		return nil, err
	}
	if len(g.Packages) == 0 {
		// An extractor that found nothing and said so with an empty
		// document rather than an error. At the head that is a failure:
		// comparing nothing with nothing would read as "no structural
		// change" for a change the job never looked at.
		if isHead {
			return nil, fmt.Errorf("%w in the head graph: the extractor found nothing to extract at %s (is SHAPEDIFF_EXTRACTOR set for this language, and modules: right?)", graph.ErrNoPackages, dir)
		}
		deps.logf("base %s has no packages; every definition at head counts as added", shortSHA(sha))
		return g, nil
	}
	if b, err := g.Marshal(); err == nil {
		if err := deps.Graphs.Put(ctx, key, b); err != nil {
			deps.logf("cache put %s: %v", key, err)
		}
	}
	return g, nil
}

// extractionFailed is the failure mode: one warning line, posted only when a
// note already exists, and an empty delta written as the artifact so that a
// reader can tell "the job failed" from "nothing structural" (spec 14.1
// step 4: the delta document is always published). Exit 0.
func (deps *Deps) extractionFailed(ctx context.Context, env Env, out Outcome, what string, err error) Outcome {
	out.Warning = fmt.Sprintf("shapediff: %s extraction failed at %s: %v", what, shortSHA(out.HeadSHA), err)
	deps.logf("%s", out.Warning)
	out.Artifact = WriteFailureArtifact(env, out.Warning)
	out.Note = writeNote(env, Marker+"\n"+out.Warning, deps)
	if deps.Notes == nil || env.MRIID == 0 {
		return out
	}
	noteID, _, found, ferr := deps.Notes.FindMarkedNote(ctx, env.MRIID, Marker)
	if ferr != nil || !found {
		return out
	}
	if uerr := deps.Notes.UpdateNote(ctx, env.MRIID, noteID, Marker+"\n"+out.Warning); uerr == nil {
		out.Updated = true
	}
	return out
}

// WriteFailureArtifact writes the delta artifact for a run that ended
// before a delta existed: an empty document whose outcome is "failed" and
// whose reason is the one line the log carries. Every exit of the job
// leaves this behind, so a missing artifact means the job never ran at
// all, not that it ran and failed. It returns the path, or "" when even
// that could not be written.
func WriteFailureArtifact(env Env, reason string) string {
	empty := &delta.Delta{SchemaVersion: delta.SchemaVersion, Repo: env.Repo(), BaseSHA: strings.TrimSpace(env.DiffBaseSHA), HeadSHA: env.HeadSHA(), MRIID: env.MRIID,
		Outcome: delta.OutcomeFailed, Reason: reason, Warnings: []string{reason}}
	art := filepath.Join(env.ProjectDir, ArtifactPath)
	if err := os.MkdirAll(filepath.Dir(art), 0o755); err != nil {
		return ""
	}
	if err := empty.WriteFile(art); err != nil {
		return ""
	}
	return art
}

// writeNote writes the note body beside the delta and returns the path,
// or "" when it could not be written (logged, never fatal).
func writeNote(env Env, body string, deps *Deps) string {
	p := filepath.Join(env.ProjectDir, NotePath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		deps.logf("write note: %v", err)
		return ""
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		deps.logf("write note: %v", err)
		return ""
	}
	return p
}

// logWriter is deps.Log, or a sink when none was given.
func logWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
