package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return &Client{BaseURL: s.URL + "/api/v4", Token: "tok", HTTP: s.Client()}, s
}

func TestNotesRoundTrip(t *testing.T) {
	var created, updated string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("missing token header")
		}
		switch {
		case r.Method == "GET" && r.URL.EscapedPath() == "/api/v4/projects/group%2Fsvc/merge_requests/7/notes":
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				json.NewEncoder(w).Encode([]Note{{ID: 1, Body: "hello", System: true}, {ID: 2, Body: "plain"}})
			} else {
				json.NewEncoder(w).Encode([]Note{{ID: 3, Body: "<!-- shapediff:v1 -->\nx"}})
			}
		case r.Method == "POST" && r.URL.EscapedPath() == "/api/v4/projects/group%2Fsvc/merge_requests/7/notes":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			created = m["body"]
			json.NewEncoder(w).Encode(Note{ID: 9, Body: created})
		case r.Method == "PUT" && r.URL.EscapedPath() == "/api/v4/projects/group%2Fsvc/merge_requests/7/notes/3":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			updated = m["body"]
			json.NewEncoder(w).Encode(Note{ID: 3, Body: updated})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	p := ProjectPath("group/svc")
	n, err := c.FindMarkedNote(ctx, p, 7, "<!-- shapediff:v1 -->")
	if err != nil || n == nil || n.ID != 3 {
		t.Fatalf("FindMarkedNote = %v, %v", n, err)
	}
	if _, err := c.CreateMRNote(ctx, p, 7, "new"); err != nil || created != "new" {
		t.Fatalf("create: %v %q", err, created)
	}
	if _, err := c.UpdateMRNote(ctx, p, 7, 3, "upd"); err != nil || updated != "upd" {
		t.Fatalf("update: %v %q", err, updated)
	}
}

func TestMergedMRsAndAwards(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/merge_requests"):
			q := r.URL.Query()
			if q.Get("state") != "merged" || q.Get("merged_after") == "" || q.Get("merged_before") == "" {
				t.Errorf("query = %v", q)
			}
			ts := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
			json.NewEncoder(w).Encode([]MergeRequest{{IID: 12, Title: "t", WebURL: "u", MergedAt: &ts, SHA: "abc"}})
		case strings.HasSuffix(r.URL.Path, "/award_emoji"):
			json.NewEncoder(w).Encode([]AwardEmoji{{ID: 1, Name: "thumbsup"}})
		default:
			w.WriteHeader(404)
		}
	})
	mrs, err := c.ListMergedMRs(context.Background(), ProjectID(5), time.Now().Add(-7*24*time.Hour), time.Now())
	if err != nil || len(mrs) != 1 || mrs[0].SHA != "abc" {
		t.Fatalf("mrs = %v %v", mrs, err)
	}
	aw, err := c.ListNoteAwards(context.Background(), ProjectID(5), 12, 3)
	if err != nil || len(aw) != 1 || aw[0].Name != "thumbsup" {
		t.Fatalf("awards = %v %v", aw, err)
	}
}

func TestWikiUpsertAndPackages(t *testing.T) {
	var pages = map[string]WikiPage{}
	var stored = map[string][]byte{}
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/wikis/"):
			slug := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/1/wikis/")
			slug, _ = urlUnescape(slug)
			if r.Method == "GET" {
				pg, ok := pages[slug]
				if !ok {
					w.WriteHeader(404)
					w.Write([]byte(`{"message":"404 Wiki Page Not Found"}`))
					return
				}
				json.NewEncoder(w).Encode(pg)
				return
			}
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			pg := WikiPage{Slug: slug, Title: m["title"], Content: m["content"]}
			pages[slug] = pg
			json.NewEncoder(w).Encode(pg)
		case strings.HasSuffix(r.URL.Path, "/wikis") && r.Method == "POST":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			slug := strings.ReplaceAll(m["title"], " ", "-")
			pg := WikiPage{Slug: slug, Title: m["title"], Content: m["content"]}
			pages[slug] = pg
			json.NewEncoder(w).Encode(pg)
		case strings.Contains(r.URL.Path, "/packages/generic/"):
			if r.Method == "PUT" {
				b, _ := io.ReadAll(r.Body)
				stored[r.URL.Path] = b
				w.WriteHeader(201)
				w.Write([]byte(`{"message":"201 Created"}`))
				return
			}
			b, ok := stored[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(b)
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	p := ProjectID(1)
	if _, err := c.UpsertWikiPage(ctx, p, "Structure/Week-2026-38", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpsertWikiPage(ctx, p, "Structure/Week-2026-38", "v2"); err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages["Structure/Week-2026-38"].Content != "v2" {
		t.Fatalf("pages = %v", pages)
	}
	if _, ok, err := c.DownloadPackageFile(ctx, p, "graphs", "k1", "graph.json"); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	if err := c.UploadPackageFile(ctx, p, "graphs", "k1", "graph.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	b, ok, err := c.DownloadPackageFile(ctx, p, "graphs", "k1", "graph.json")
	if err != nil || !ok || string(b) != "{}" {
		t.Fatalf("download = %q %v %v", b, ok, err)
	}
}

func TestRetryOn5xxAndErrorShape(t *testing.T) {
	var calls int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(502)
			w.Write([]byte("bad gateway"))
			return
		}
		json.NewEncoder(w).Encode([]Note{})
	})
	if _, err := c.ListMRNotes(context.Background(), ProjectID(1), 1); err != nil {
		t.Fatalf("retry should have succeeded: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
	c2, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"403 Forbidden"}`))
	})
	_, err := c2.CreateMRNote(context.Background(), ProjectID(1), 1, "x")
	e, ok := err.(*Error)
	if !ok || e.Status != 403 || !strings.Contains(e.Body, "Forbidden") {
		t.Fatalf("err = %v", err)
	}
}

func urlUnescape(s string) (string, error) {
	r := strings.NewReplacer("%2F", "/", "%2f", "/")
	return r.Replace(s), nil
}
