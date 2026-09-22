package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shapediff/internal/cache"
	"shapediff/internal/delta"
	"shapediff/internal/graph"
)

// Marker is the first line of every note the job posts; it is how the job
// finds its own note on the next push.
const Marker = "<!-- shapediff:v1 -->"

// ArtifactPath is the delta artifact, relative to the project dir. It is
// always written, structural or not, so a reader can tell "nothing
// structural" from "the job did not run".
const ArtifactPath = "shapediff/delta.json"

// NotesAPI is the slice of the GitLab client the job needs.
type NotesAPI interface {
	FindMarkedNote(ctx context.Context, iid int, marker string) (noteID int, body string, found bool, err error)
	CreateNote(ctx context.Context, iid int, body string) error
	UpdateNote(ctx context.Context, iid, noteID int, body string) error
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
	Graphs       cache.Store
	Deltas       cache.Store
	KeyParts     KeyParts
	Log          io.Writer
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
	Posted     bool   // a new note was created
	Updated    bool   // the existing marked note was updated
	Artifact   string // path of the delta artifact written
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
	if !env.IsMergeRequestEvent() {
		out.Skipped = true
		deps.logf("pipeline source %q is not merge_request_event; nothing to do", env.PipelineSource)
		return out, nil
	}
	out.HeadSHA = env.HeadSHA()
	out.BaseSHA = strings.TrimSpace(env.DiffBaseSHA)
	if out.HeadSHA == "" || out.BaseSHA == "" {
		out.Warning = "head or base SHA missing from the CI environment"
		deps.logf("%s", out.Warning)
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

	// Step 5: post only when structural; update the existing note always.
	if deps.Notes == nil || env.MRIID == 0 {
		deps.logf("no notes API or MR iid; artifact only")
		return out, nil
	}
	noteID, _, found, err := deps.Notes.FindMarkedNote(ctx, env.MRIID, Marker)
	if err != nil {
		out.Warning = "could not list MR notes: " + err.Error()
		deps.logf("%s", out.Warning)
		return out, nil
	}
	var body string
	if out.Structural {
		body, err = deps.Render(d, base, head)
		if err != nil {
			out.Warning = "render failed: " + err.Error()
			deps.logf("%s", out.Warning)
			if !found {
				return out, nil
			}
			body = "shapediff: " + out.Warning
		}
	} else {
		if !found {
			deps.logf("no structural change and no existing note; not posting")
			return out, nil
		}
		body = deps.NoStructural(out.HeadSHA)
	}
	body = Marker + "\n" + body
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
		return nil, err
	}
	if b, err := g.Marshal(); err == nil {
		if err := deps.Graphs.Put(ctx, key, b); err != nil {
			deps.logf("cache put %s: %v", key, err)
		}
	}
	return g, nil
}

// extractionFailed is the failure mode: one warning line, posted only when a
// note already exists (otherwise the artifact would carry it, but there is
// no delta to write, so the log carries it), exit 0.
func (deps *Deps) extractionFailed(ctx context.Context, env Env, out Outcome, what string, err error) Outcome {
	out.Warning = fmt.Sprintf("shapediff: %s extraction failed at %s: %v", what, shortSHA(out.HeadSHA), err)
	deps.logf("%s", out.Warning)
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

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
