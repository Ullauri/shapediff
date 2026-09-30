package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newServer(t *testing.T, h http.HandlerFunc) *Client {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return &Client{BaseURL: s.URL, Token: "tok", HTTP: s.Client()}
}

func TestCommentsRoundTrip(t *testing.T) {
	var created, updated string
	var base string
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues/7/comments":
			if r.URL.Query().Get("page") == "" {
				if r.URL.Query().Get("per_page") != "100" {
					t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
				}
				w.Header().Set("Link", `<`+base+`/repos/o/r/issues/7/comments?per_page=100&page=2>; rel="next", <`+base+`/repos/o/r/issues/7/comments?per_page=100&page=2>; rel="last"`)
				json.NewEncoder(w).Encode([]Comment{
					{ID: 1, Body: "plain"},
					{ID: 2, Body: "> <!-- shapediff:v1 -->\n> quoted in a reply"},
				})
			} else {
				json.NewEncoder(w).Encode([]Comment{{ID: 3000000001, Body: "<!-- shapediff:v1 -->\nx"}})
			}
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/issues/7/comments":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			created = m["body"]
			json.NewEncoder(w).Encode(Comment{ID: 9, Body: created})
		case r.Method == "PATCH" && r.URL.Path == "/repos/o/r/issues/comments/3000000001":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			updated = m["body"]
			json.NewEncoder(w).Encode(Comment{ID: 3000000001, Body: updated})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	base = c.BaseURL
	ctx := context.Background()
	got, err := c.FindMarkedComment(ctx, "o/r", 7, "<!-- shapediff:v1 -->")
	if err != nil || got == nil || got.ID != 3000000001 {
		t.Fatalf("FindMarkedComment = %v, %v (want the note on page 2, not the quoting reply)", got, err)
	}
	if _, err := c.CreateComment(ctx, "o/r", 7, "new"); err != nil || created != "new" {
		t.Fatalf("create: %v %q", err, created)
	}
	if _, err := c.UpdateComment(ctx, "o/r", 3000000001, "upd"); err != nil || updated != "upd" {
		t.Fatalf("update: %v %q", err, updated)
	}
}

func TestFindMarkedCommentNone(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Comment{{ID: 1, Body: "no marker here"}})
	})
	got, err := c.FindMarkedComment(context.Background(), "o/r", 7, "<!-- shapediff:v1 -->")
	if err != nil || got != nil {
		t.Fatalf("FindMarkedComment = %v, %v; want nil, nil", got, err)
	}
}

func TestErrorsAndRetry(t *testing.T) {
	var calls atomic.Int32
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	})
	_, err := c.CreateComment(context.Background(), "o/r", 7, "x")
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 || !strings.Contains(e.Body, "not accessible") {
		t.Fatalf("err = %v; want the 403 after one 5xx retry", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestNextLink(t *testing.T) {
	for h, want := range map[string]string{
		"": "",
		`<https://api/x?page=3>; rel="next", <https://api/x?page=9>; rel="last"`:  "https://api/x?page=3",
		`<https://api/x?page=1>; rel="prev", <https://api/x?page=1>; rel="first"`: "",
	} {
		if got := nextLink(h); got != want {
			t.Errorf("nextLink(%q) = %q, want %q", h, got, want)
		}
	}
}

func TestFit(t *testing.T) {
	short := "<!-- shapediff:v1 -->\nsmall note\n"
	if got, changed := Fit(short); changed || got != short {
		t.Fatalf("Fit changed a note under the limit")
	}

	head := "<!-- shapediff:v1 -->\n#### What changed\n\n```mermaid\nflowchart LR\n  a --> b\n```\n"
	details := "\n<details><summary>Details</summary>\n\n| node | status |\n|---|---|\n" + strings.Repeat("| `x` | added |\n", 6000) + "</details>\n"
	got, changed := Fit(head + details)
	if !changed || len(got) > MaxBody || !strings.HasPrefix(got, head) || strings.Contains(got, "<details>") || !strings.Contains(got, "details table is left out") {
		t.Fatalf("Fit did not drop the Details section cleanly: changed=%v len=%d", changed, len(got))
	}

	// Still too long with no Details section: cut at a line, and close the
	// fence the cut landed in.
	huge := "<!-- shapediff:v1 -->\n```mermaid\nflowchart LR\n" + strings.Repeat("  n1 --> n2\n", 8000) + "```\n"
	got, changed = Fit(huge)
	if !changed || len(got) > MaxBody || !strings.Contains(got, "cut short") {
		t.Fatalf("Fit did not cut: changed=%v len=%d", changed, len(got))
	}
	if strings.Count(got, "```")%2 != 0 {
		t.Fatalf("Fit left a code fence open")
	}
}
