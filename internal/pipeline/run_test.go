package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"shapediff/internal/cache"
	"shapediff/internal/delta"
	"shapediff/internal/graph"
)

type fakeNotes struct {
	notes   map[int]string
	nextID  int
	fail    error
	created int
	updated int
}

func (f *fakeNotes) FindMarkedNote(_ context.Context, _ int, marker string) (int, string, bool, error) {
	if f.fail != nil {
		return 0, "", false, f.fail
	}
	for id, b := range f.notes {
		if bytes.Contains([]byte(b), []byte(marker)) {
			return id, b, true, nil
		}
	}
	return 0, "", false, nil
}
func (f *fakeNotes) CreateNote(_ context.Context, _ int, body string) error {
	f.nextID++
	f.notes[f.nextID] = body
	f.created++
	return nil
}
func (f *fakeNotes) UpdateNote(_ context.Context, _ int, id int, body string) error {
	f.notes[id] = body
	f.updated++
	return nil
}

func doc(sha string) *graph.Document {
	d := &graph.Document{SchemaVersion: 1, Repo: "g/s", SHA: sha, Extractor: graph.Extractor{Name: "x", Version: "0.1.0", Precision: "typed", Toolchain: "go", ConfigHash: "c"}}
	d.Canonicalize()
	return d
}

func env(dir string) Env {
	return EnvFrom(func(k string) string {
		return map[string]string{
			"CI_PIPELINE_SOURCE":                 "merge_request_event",
			"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": "head000000000000",
			"CI_COMMIT_SHA":                      "merge-result",
			"CI_MERGE_REQUEST_DIFF_BASE_SHA":     "base000000000000",
			"CI_MERGE_REQUEST_IID":               "42",
			"CI_PROJECT_PATH":                    "g/s",
			"CI_PROJECT_DIR":                     dir,
		}[k]
	})
}

func deps(t *testing.T, structural bool, notes *fakeNotes) (Deps, *int) {
	extracts := 0
	return Deps{
		Extract: func(_ context.Context, _ string, sha string) (*graph.Document, error) {
			extracts++
			return doc(sha), nil
		},
		Delta: func(base, head *graph.Document) (*delta.Delta, error) {
			d := &delta.Delta{SchemaVersion: 1}
			d.Stats.Structural = structural
			return d, nil
		},
		Render:       func(*delta.Delta, *graph.Document, *graph.Document) (string, error) { return "NOTE", nil },
		NoStructural: func(sha string) string { return "no structural change at " + sha },
		Notes:        notes,
		Graphs:       cache.Dir{Path: t.TempDir()},
		Deltas:       cache.Dir{Path: t.TempDir()},
		KeyParts:     KeyParts{Version: "0.1.0", Toolchain: "go", ConfigHash: "c"},
		Checkout:     func(_ context.Context, repoDir, _ string) (string, func(), error) { return repoDir, func() {}, nil },
	}, &extracts
}

func TestEnvHeadSHARule(t *testing.T) {
	e := env(".")
	if e.HeadSHA() != "head000000000000" {
		t.Fatal("source branch sha should win")
	}
	e.SourceBranchSHA = ""
	if e.HeadSHA() != "merge-result" {
		t.Fatal("fallback to CI_COMMIT_SHA")
	}
	e.PipelineSource = "push"
	out, err := Run(context.Background(), e, Deps{Extract: func(context.Context, string, string) (*graph.Document, error) { return nil, nil }, Delta: func(*graph.Document, *graph.Document) (*delta.Delta, error) { return nil, nil }, Render: func(*delta.Delta, *graph.Document, *graph.Document) (string, error) { return "", nil }, Graphs: cache.Dir{}, Deltas: cache.Dir{}})
	if err != nil || !out.Skipped {
		t.Fatalf("push pipeline should skip: %+v %v", out, err)
	}
}

func TestStructuralPostsThenUpdates(t *testing.T) {
	dir := t.TempDir()
	notes := &fakeNotes{notes: map[int]string{1: "unrelated"}, nextID: 1}
	dp, extracts := deps(t, true, notes)
	out, err := Run(context.Background(), env(dir), dp)
	if err != nil || !out.Posted || out.Updated || !out.Structural {
		t.Fatalf("first push: %+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ArtifactPath)); err != nil {
		t.Fatal("artifact missing")
	}
	if b, ok, _ := dp.Deltas.Get(context.Background(), cache.MRKey("g/s", 42, "head000000000000")); !ok || len(b) == 0 {
		t.Fatal("delta not cached under MR key")
	}
	if *extracts != 2 {
		t.Fatalf("extracts = %d", *extracts)
	}
	// second push at same SHAs: cache hits, note updated not duplicated
	out, _ = Run(context.Background(), env(dir), dp)
	if !out.Updated || out.Posted || notes.created != 1 || notes.updated != 1 {
		t.Fatalf("second push: %+v created=%d updated=%d", out, notes.created, notes.updated)
	}
	if *extracts != 2 {
		t.Fatalf("cache should have served both graphs; extracts = %d", *extracts)
	}
	if !bytes.HasPrefix([]byte(notes.notes[2]), []byte(Marker+"\nNOTE")) {
		t.Fatalf("note body = %q", notes.notes[2])
	}
}

func TestNonStructuralNeverPostsFirst(t *testing.T) {
	dir := t.TempDir()
	notes := &fakeNotes{notes: map[int]string{}}
	dp, _ := deps(t, false, notes)
	out, err := Run(context.Background(), env(dir), dp)
	if err != nil || out.Posted || out.Updated || out.Structural {
		t.Fatalf("%+v %v", out, err)
	}
	if out.Artifact == "" {
		t.Fatal("artifact must always be written")
	}
	// but an existing note is updated to the one-liner when structure went away
	notes.notes[5] = Marker + "\nold structural note"
	out, _ = Run(context.Background(), env(dir), dp)
	if !out.Updated || notes.notes[5] != Marker+"\nno structural change at head000000000000" {
		t.Fatalf("%+v %q", out, notes.notes[5])
	}
}

func TestExtractionFailureWarnsOnlyIfNoteExists(t *testing.T) {
	dir := t.TempDir()
	notes := &fakeNotes{notes: map[int]string{}}
	dp, _ := deps(t, true, notes)
	dp.Extract = func(context.Context, string, string) (*graph.Document, error) {
		return nil, errors.New("go build failed")
	}
	out, err := Run(context.Background(), env(dir), dp)
	if err != nil || out.Warning == "" || out.Posted || out.Updated {
		t.Fatalf("%+v %v", out, err)
	}
	notes.notes[3] = Marker + "\nold"
	out, _ = Run(context.Background(), env(dir), dp)
	if !out.Updated || !bytes.Contains([]byte(notes.notes[3]), []byte("extraction failed")) {
		t.Fatalf("%+v %q", out, notes.notes[3])
	}
	if out.Artifact == "" {
		t.Fatal("extraction failure must still write the delta artifact (spec 14.1 step 4)")
	}
	if ed, err := delta.ReadFile(out.Artifact); err != nil || ed.Stats.Structural || len(ed.Warnings) == 0 {
		t.Fatalf("empty delta artifact: %v %+v", err, ed)
	}
}

func TestNotesAPIFailureIsWarning(t *testing.T) {
	dir := t.TempDir()
	notes := &fakeNotes{notes: map[int]string{}, fail: errors.New("403")}
	dp, _ := deps(t, true, notes)
	out, err := Run(context.Background(), env(dir), dp)
	if err != nil || out.Warning == "" || out.Artifact == "" {
		t.Fatalf("%+v %v", out, err)
	}
}
