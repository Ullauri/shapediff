// Package github is the small REST client the per-PR job (spec 14.1, run on
// GitHub Actions) needs: the comments on one pull request. Nothing else.
// A pull request's conversation comments are issue comments; review
// comments, which hang off a line of the diff, are a different API and are
// not used.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxBody is GitHub's limit on a comment body. A longer body is refused
// with a 422, so Fit trims the note to it before posting.
const MaxBody = 65536

// Client talks to one GitHub API host with one token (GITHUB_TOKEN with
// pull-requests: write, or a fine-grained token with the same).
type Client struct {
	BaseURL string // e.g. https://api.github.com (GITHUB_API_URL)
	Token   string
	HTTP    *http.Client
}

// Repo is "owner/name" (GITHUB_REPOSITORY).
type Repo string

func (r Repo) path() string {
	owner, name, _ := strings.Cut(string(r), "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// Error is a non-2xx response.
type Error struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("github: %s %s: %d %s", e.Method, e.Path, e.Status, e.Body)
}

// Comment is a pull request's conversation comment.
type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// do performs one request against u (absolute), retrying once on a 5xx,
// and decodes JSON into out when out is non-nil.
func (c *Client) do(ctx context.Context, method, u string, body any, out any) (*http.Response, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	path := u
	if p, err := url.Parse(u); err == nil {
		path = p.Path
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http().Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode >= 500 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			last = &Error{Status: resp.StatusCode, Method: method, Path: path, Body: strings.TrimSpace(string(snippet))}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, &Error{Status: resp.StatusCode, Method: method, Path: path, Body: strings.TrimSpace(string(snippet))}
		}
		defer resp.Body.Close()
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return nil, fmt.Errorf("github: decode %s %s: %w", method, path, err)
			}
		}
		return resp, nil
	}
	return nil, last
}

func (c *Client) url(format string, args ...any) string {
	return strings.TrimRight(c.BaseURL, "/") + fmt.Sprintf(format, args...)
}

// ListComments returns every conversation comment on a pull request, oldest
// first, following the Link header across pages.
func (c *Client) ListComments(ctx context.Context, r Repo, number int) ([]Comment, error) {
	var all []Comment
	next := c.url("/repos/%s/issues/%d/comments?per_page=100", r.path(), number)
	for next != "" {
		var page []Comment
		resp, err := c.do(ctx, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next = nextLink(resp.Header.Get("Link"))
	}
	return all, nil
}

// CreateComment posts a comment on a pull request and returns it.
func (c *Client) CreateComment(ctx context.Context, r Repo, number int, body string) (*Comment, error) {
	var out Comment
	if _, err := c.do(ctx, http.MethodPost, c.url("/repos/%s/issues/%d/comments", r.path(), number), map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateComment replaces a comment's body. Comment ids are repo-wide on
// GitHub, so the pull request number is not part of the path.
func (c *Client) UpdateComment(ctx context.Context, r Repo, id int64, body string) (*Comment, error) {
	var out Comment
	if _, err := c.do(ctx, http.MethodPatch, c.url("/repos/%s/issues/comments/%d", r.path(), id), map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindMarkedComment returns the first comment whose body starts with the
// marker, or nil when there is none. Starts with, not contains: a reply
// that quotes the note carries the marker too, behind "> ".
func (c *Client) FindMarkedComment(ctx context.Context, r Repo, number int, marker string) (*Comment, error) {
	comments, err := c.ListComments(ctx, r, number)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		if strings.HasPrefix(strings.TrimLeft(comments[i].Body, " \t\r\n"), marker) {
			return &comments[i], nil
		}
	}
	return nil, nil
}

// nextLink is the rel="next" URL of a Link header, or "".
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		seg := strings.Split(part, ";")
		if len(seg) < 2 {
			continue
		}
		for _, p := range seg[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(seg[0]), "<>")
			}
		}
	}
	return ""
}

// detailsOmitted replaces the note's closing Details section when the note
// is over MaxBody.
const detailsOmitted = "\n_The details table is left out: GitHub caps a comment at 65,536 characters. The full delta is the job's artifact, `shapediff/delta.json`._\n"

// Fit makes a note fit in one comment. The note's last section is the
// collapsed Details table, which grows with the size of the change and is
// the part a reader can most easily do without, so it goes first. A note
// still over the limit is cut at a line boundary. It reports whether it
// changed anything. Lengths are bytes, which is never less than GitHub's
// character count.
func Fit(body string) (string, bool) {
	if len(body) <= MaxBody {
		return body, false
	}
	if i := strings.LastIndex(body, "\n<details><summary>"); i >= 0 && strings.HasSuffix(strings.TrimRight(body, "\n"), "</details>") {
		body = body[:i] + detailsOmitted
		if len(body) <= MaxBody {
			return body, true
		}
	}
	const cut = "\n\n_This note is cut short: GitHub caps a comment at 65,536 characters. The full delta is the job's artifact, `shapediff/delta.json`._\n"
	limit := MaxBody - len(cut) - len("\n```") // room for closeFences
	if j := strings.LastIndex(body[:limit], "\n"); j > 0 {
		limit = j
	}
	return closeFences(body[:limit]) + cut, true
}

// closeFences closes a code fence the cut left open, so the rest of the
// comment is not swallowed into a diagram.
func closeFences(s string) string {
	open := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "```") {
			open = !open
		}
	}
	if open {
		return s + "\n```"
	}
	return s
}
