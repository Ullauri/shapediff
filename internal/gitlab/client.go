// Package gitlab is the small REST client the per-MR job (spec 14.1) and the
// weekly job (14.2) need: MR notes, merged MR listing, award emoji, wiki
// pages and the generic package registry. Nothing else.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to one GitLab instance with one token (a project access
// token, Reporter role, stored as a masked CI variable).
type Client struct {
	BaseURL string // e.g. https://gitlab.example.com/api/v4 (CI_API_V4_URL)
	Token   string
	HTTP    *http.Client
}

// Project identifies a project by numeric ID or by path ("group/name");
// both are accepted everywhere the API takes :id.
type Project string

// ProjectID builds a Project from a numeric ID.
func ProjectID(id int) Project { return Project(strconv.Itoa(id)) }

// ProjectPath builds a Project from a path such as "group/svc".
func ProjectPath(p string) Project { return Project(p) }

func (p Project) encoded() string { return url.PathEscape(string(p)) }

// Error is a non-2xx response.
type Error struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("gitlab: %s %s: %d %s", e.Method, e.Path, e.Status, e.Body)
}

// Note is an MR note (comment).
type Note struct {
	ID     int    `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// MergeRequest is the subset of the MR API the weekly job reads.
type MergeRequest struct {
	IID      int        `json:"iid"`
	Title    string     `json:"title"`
	WebURL   string     `json:"web_url"`
	MergedAt *time.Time `json:"merged_at"`
	SHA      string     `json:"sha"` // head SHA at merge time
}

// AwardEmoji is one award on a note.
type AwardEmoji struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
}

// WikiPage is a wiki page.
type WikiPage struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Format  string `json:"format"`
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// do performs one request, retrying once on a 5xx or a transport error,
// and decodes JSON into out when out is non-nil; with a nil out the body
// is left open for the caller to read and close. Body may be nil, a
// JSON-encodable value or an io.Reader (sent raw with contentType). A POST
// is never retried: when the first one was created and only its response
// was lost, a retry would post a second note.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, contentType string, out any) (*http.Response, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var raw []byte
	switch b := body.(type) {
	case nil:
	case io.Reader:
		var err error
		raw, err = io.ReadAll(b)
		if err != nil {
			return nil, err
		}
	default:
		var err error
		raw, err = json.Marshal(b)
		if err != nil {
			return nil, err
		}
		contentType = "application/json"
	}
	attempts := 2
	if method == http.MethodPost {
		attempts = 1
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", c.Token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
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
		if out != nil {
			defer resp.Body.Close()
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return nil, fmt.Errorf("gitlab: decode %s %s: %w", method, path, err)
			}
		}
		return resp, nil
	}
	return nil, last
}

// ---- notes ----

// ListMRNotes returns every note on an MR, oldest first, across pages.
func (c *Client) ListMRNotes(ctx context.Context, p Project, iid int) ([]Note, error) {
	var all []Note
	for page := 1; ; page++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "sort": {"asc"}, "order_by": {"created_at"}}
		var notes []Note
		resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/merge_requests/%d/notes", p.encoded(), iid), q, nil, "", &notes)
		if err != nil {
			return nil, err
		}
		all = append(all, notes...)
		if !hasNextPage(resp, len(notes)) {
			return all, nil
		}
	}
}

// CreateMRNote posts a note and returns it.
func (c *Client) CreateMRNote(ctx context.Context, p Project, iid int, body string) (*Note, error) {
	var n Note
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/merge_requests/%d/notes", p.encoded(), iid), nil, map[string]string{"body": body}, "", &n)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// UpdateMRNote replaces a note's body.
func (c *Client) UpdateMRNote(ctx context.Context, p Project, iid, noteID int, body string) (*Note, error) {
	var n Note
	_, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/projects/%s/merge_requests/%d/notes/%d", p.encoded(), iid, noteID), nil, map[string]string{"body": body}, "", &n)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// FindMarkedNote returns the first non-system note by author whose body
// starts with the marker, or nil when there is none. By author, because
// anyone who can comment can write the marker: a stranger's note taken for
// the job's would be edited under their name, or refuse the edit and keep
// the real note from ever being posted. Starts with, not contains: a reply
// that quotes the note carries the marker too, behind "> ". An empty
// author matches by the marker alone, which is only for when the token's
// own user cannot be resolved; callers should say so when they fall back.
func (c *Client) FindMarkedNote(ctx context.Context, p Project, iid int, marker, author string) (*Note, error) {
	notes, err := c.ListMRNotes(ctx, p, iid)
	if err != nil {
		return nil, err
	}
	for i := range notes {
		n := &notes[i]
		if n.System || !strings.HasPrefix(strings.TrimLeft(n.Body, " \t\r\n"), marker) {
			continue
		}
		if author == "" || strings.EqualFold(n.Author.Username, author) {
			return n, nil
		}
	}
	return nil, nil
}

// Login is the username the token acts as: for a project access token,
// its bot user. It is whose note FindMarkedNote looks for.
func (c *Client) Login(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/user", nil, nil, "", &u); err != nil {
		return "", err
	}
	if u.Username == "" {
		return "", fmt.Errorf("gitlab: /user returned no username")
	}
	return u.Username, nil
}

// ListNoteAwards returns the award emoji on an MR note.
func (c *Client) ListNoteAwards(ctx context.Context, p Project, iid, noteID int) ([]AwardEmoji, error) {
	var out []AwardEmoji
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/merge_requests/%d/notes/%d/award_emoji", p.encoded(), iid, noteID), url.Values{"per_page": {"100"}}, nil, "", &out)
	return out, err
}

// ---- merge requests ----

// ListMergedMRs returns MRs merged in [after, before], across pages. This
// works under every merge method, unlike `git log --merges` (spec 8.3).
func (c *Client) ListMergedMRs(ctx context.Context, p Project, after, before time.Time) ([]MergeRequest, error) {
	var all []MergeRequest
	for page := 1; ; page++ {
		q := url.Values{
			"state": {"merged"}, "per_page": {"100"}, "page": {strconv.Itoa(page)},
			"merged_after": {after.UTC().Format(time.RFC3339)}, "merged_before": {before.UTC().Format(time.RFC3339)},
			"order_by": {"updated_at"}, "sort": {"asc"},
		}
		var mrs []MergeRequest
		resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/merge_requests", p.encoded()), q, nil, "", &mrs)
		if err != nil {
			return nil, err
		}
		all = append(all, mrs...)
		if !hasNextPage(resp, len(mrs)) {
			return all, nil
		}
	}
}

// ---- wiki ----

// GetWikiPage fetches a page by slug; (nil, nil) when it does not exist.
func (c *Client) GetWikiPage(ctx context.Context, p Project, slug string) (*WikiPage, error) {
	var w WikiPage
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/wikis/%s", p.encoded(), url.PathEscape(slug)), nil, nil, "", &w)
	if e, ok := err.(*Error); ok && e.Status == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// CreateWikiPage creates a markdown page.
func (c *Client) CreateWikiPage(ctx context.Context, p Project, title, content string) (*WikiPage, error) {
	var w WikiPage
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/wikis", p.encoded()), nil, map[string]string{"title": title, "content": content, "format": "markdown"}, "", &w)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// UpdateWikiPage replaces a page's content.
func (c *Client) UpdateWikiPage(ctx context.Context, p Project, slug, title, content string) (*WikiPage, error) {
	var w WikiPage
	_, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/projects/%s/wikis/%s", p.encoded(), url.PathEscape(slug)), nil, map[string]string{"title": title, "content": content, "format": "markdown"}, "", &w)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// UpsertWikiPage creates the page or updates it when it exists. The slug
// GitLab derives from a title replaces spaces with dashes and keeps "/".
func (c *Client) UpsertWikiPage(ctx context.Context, p Project, title, content string) (*WikiPage, error) {
	slug := strings.ReplaceAll(title, " ", "-")
	existing, err := c.GetWikiPage(ctx, p, slug)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return c.CreateWikiPage(ctx, p, title, content)
	}
	return c.UpdateWikiPage(ctx, p, existing.Slug, title, content)
}

// ---- generic package registry ----

// UploadPackageFile puts one file in the generic package registry.
func (c *Client) UploadPackageFile(ctx context.Context, p Project, name, version, file string, data []byte) error {
	resp, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/projects/%s/packages/generic/%s/%s/%s", p.encoded(), url.PathEscape(name), url.PathEscape(version), url.PathEscape(file)), nil, bytes.NewReader(data), "application/octet-stream", nil)
	if err != nil {
		return err
	}
	resp.Body.Close() // nothing to read; a nil out leaves the body to the caller
	return nil
}

// DownloadPackageFile fetches one file; (nil, false, nil) when absent.
func (c *Client) DownloadPackageFile(ctx context.Context, p Project, name, version, file string) ([]byte, bool, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/packages/generic/%s/%s/%s", p.encoded(), url.PathEscape(name), url.PathEscape(version), url.PathEscape(file)), nil, nil, "", nil)
	if e, ok := err.(*Error); ok && e.Status == 404 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func hasNextPage(resp *http.Response, got int) bool {
	if got == 0 {
		return false
	}
	if np := resp.Header.Get("X-Next-Page"); np != "" && np != "0" {
		return true
	}
	return false
}
