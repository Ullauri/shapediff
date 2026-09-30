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
					comment(1, "someone", "plain"),
					comment(2, "github-actions[bot]", "> <!-- shapediff:v1 -->\n> quoted in a reply"),
					comment(4, "stranger", "<!-- shapediff:v1 -->\nplanted before the real note"),
				})
			} else {
				json.NewEncoder(w).Encode([]Comment{comment(3000000001, "github-actions[bot]", "<!-- shapediff:v1 -->\nx")})
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
	got, err := c.FindMarkedComment(ctx, "o/r", 7, "<!-- shapediff:v1 -->", ActionsBot)
	if err != nil || got == nil || got.ID != 3000000001 {
		t.Fatalf("FindMarkedComment = %v, %v (want the bot's note on page 2, not the quoting reply or the stranger's)", got, err)
	}
	if _, err := c.FindMarkedComment(ctx, "o/r", 7, "<!-- shapediff:v1 -->", ""); err == nil {
		t.Fatal("FindMarkedComment with no author should refuse")
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
	got, err := c.FindMarkedComment(context.Background(), "o/r", 7, "<!-- shapediff:v1 -->", ActionsBot)
	if err != nil || got != nil {
		t.Fatalf("FindMarkedComment = %v, %v; want nil, nil", got, err)
	}
}

func comment(id int64, login, body string) Comment {
	c := Comment{ID: id, Body: body}
	c.User.Login = login
	return c
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
	_, err := c.UpdateComment(context.Background(), "o/r", 5, "x")
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 || !strings.Contains(e.Body, "not accessible") {
		t.Fatalf("err = %v; want the 403 after one 5xx retry", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

// A POST whose response was lost may have created the comment; retrying it
// would post a second one.
func TestCreateIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(502)
	})
	if _, err := c.CreateComment(context.Background(), "o/r", 7, "x"); err == nil {
		t.Fatal("want the 502")
	}
	if calls.Load() != 1 {
		t.Fatalf("POST sent %d times, want 1", calls.Load())
	}
}

func TestLogin(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer installation" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{"login":"Ullauri"}`))
	})
	if got, err := c.Login(context.Background()); err != nil || got != "Ullauri" {
		t.Fatalf("Login = %q, %v", got, err)
	}
	c.Token = "installation"
	if _, err := c.Login(context.Background()); err == nil {
		t.Fatal("an installation token cannot read /user; Login should fail so the caller falls back to the bot")
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

	// The weekly brief's link follows the Details section; dropping the
	// section keeps it.
	weekly := "\nBehind on this repo? The weekly structure brief covers what it has become: https://w/brief\n"
	got, changed = Fit(head + details + weekly)
	if !changed || len(got) > MaxBody || strings.Contains(got, "<details>") || !strings.HasSuffix(got, weekly) || strings.Contains(got, "cut short") {
		t.Fatalf("Fit lost the weekly link or cut instead of dropping Details: changed=%v len=%d tail=%q", changed, len(got), got[len(got)-200:])
	}

	// Too long before the Details section even starts: the cut lands inside
	// nested collapsed sections, which must be closed so the notice is not
	// hidden in one.
	stacks := "\n<details><summary>12 more call stacks</summary>\n\n" + strings.Repeat("<details><summary>`a` calls `b`</summary>\n\nsignature and doc\n\n</details>\n", 1500) + "</details>\n"
	got, changed = Fit(head + stacks + details + weekly)
	if !changed || len(got) > MaxBody || !strings.Contains(got, "cut short") || !strings.HasSuffix(got, weekly) {
		t.Fatalf("Fit did not cut cleanly: changed=%v len=%d", changed, len(got))
	}
	if o, c := strings.Count(got, "<details>"), strings.Count(got, "</details>"); o != c {
		t.Fatalf("Fit left <details> open: %d opened, %d closed", o, c)
	}
	if i, j := strings.LastIndex(got, "</details>"), strings.Index(got, "cut short"); j < i {
		t.Fatal("the cut notice sits inside a collapsed section")
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
