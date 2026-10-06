// Package pipeline is the per-MR CI job of spec section 14.1, written as an
// orchestration over injected dependencies so it can be tested without the
// extractor or a GitLab instance. It runs in a GitLab merge-request pipeline
// or a GitHub Actions pull_request workflow; Env hides which.
package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/Ullauri/shapediff/internal/forge"
)

// Env is what the job reads from the CI environment. The field comments
// name the GitLab variable; githubEnv says where each comes from on GitHub.
type Env struct {
	Forge           forge.Forge
	PipelineSource  string // CI_PIPELINE_SOURCE
	SourceBranchSHA string // CI_MERGE_REQUEST_SOURCE_BRANCH_SHA
	CommitSHA       string // CI_COMMIT_SHA
	DiffBaseSHA     string // CI_MERGE_REQUEST_DIFF_BASE_SHA
	MRIID           int    // CI_MERGE_REQUEST_IID
	ProjectID       string // CI_PROJECT_ID
	ProjectPath     string // CI_PROJECT_PATH
	ProjectURL      string // CI_PROJECT_URL
	APIURL          string // CI_API_V4_URL
	ProjectDir      string // CI_PROJECT_DIR
	Token           string // SHAPEDIFF_TOKEN
	CacheSpec       string // SHAPEDIFF_CACHE
	// CommentAuthor is the user whose comment or note is the job's
	// (SHAPEDIFF_COMMENT_AUTHOR); "" means the token's own, read from the
	// API. On both forges a note by anyone else is never taken for it.
	CommentAuthor string
}

// EnvFrom parses the CI variables from a lookup function (os.Getenv or a
// test map). On GitHub Actions it also reads the event payload file.
func EnvFrom(get func(string) string) Env {
	return EnvFromFiles(get, os.ReadFile)
}

// EnvFromFiles is EnvFrom with the file reader injected, for tests.
func EnvFromFiles(get func(string) string, read func(string) ([]byte, error)) Env {
	if get("GITHUB_ACTIONS") == "true" {
		return githubEnv(get, read)
	}
	iid, _ := strconv.Atoi(strings.TrimSpace(get("CI_MERGE_REQUEST_IID")))
	e := Env{
		Forge:           forge.GitLab,
		PipelineSource:  get("CI_PIPELINE_SOURCE"),
		SourceBranchSHA: get("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"),
		CommitSHA:       get("CI_COMMIT_SHA"),
		DiffBaseSHA:     get("CI_MERGE_REQUEST_DIFF_BASE_SHA"),
		MRIID:           iid,
		ProjectID:       get("CI_PROJECT_ID"),
		ProjectPath:     get("CI_PROJECT_PATH"),
		ProjectURL:      get("CI_PROJECT_URL"),
		APIURL:          get("CI_API_V4_URL"),
		ProjectDir:      get("CI_PROJECT_DIR"),
		Token:           get("SHAPEDIFF_TOKEN"),
		CacheSpec:       get("SHAPEDIFF_CACHE"),
		CommentAuthor:   get("SHAPEDIFF_COMMENT_AUTHOR"),
	}
	if e.ProjectDir == "" {
		e.ProjectDir = "."
	}
	return e
}

// githubEvent is the part of $GITHUB_EVENT_PATH the job reads.
type githubEvent struct {
	PullRequest *struct {
		Number int `json:"number"`
		Base   struct {
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

// githubEnv maps a GitHub Actions pull_request run onto Env.
//
// The head is pull_request.head.sha, never GITHUB_SHA: on a pull_request
// event GITHUB_SHA is the ephemeral merge commit, the same trap as
// CI_COMMIT_SHA in a merged-results pipeline, and must not be cached under.
// The base is pull_request.base.sha, the base branch's tip when the event
// fired; that is not GitLab's diff base, so the job replaces it with the
// merge base once it has a checkout (ResolveDiffBase).
func githubEnv(get func(string) string, read func(string) ([]byte, error)) Env {
	repo := get("GITHUB_REPOSITORY")
	server := strings.TrimRight(get("GITHUB_SERVER_URL"), "/")
	if server == "" {
		server = "https://github.com"
	}
	api := get("GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	token := get("SHAPEDIFF_TOKEN")
	if token == "" {
		token = get("GITHUB_TOKEN")
	}
	e := Env{
		Forge:          forge.GitHub,
		PipelineSource: get("GITHUB_EVENT_NAME"),
		CommitSHA:      get("GITHUB_SHA"),
		ProjectPath:    repo,
		ProjectURL:     server + "/" + repo,
		APIURL:         api,
		ProjectDir:     get("GITHUB_WORKSPACE"),
		Token:          token,
		CacheSpec:      get("SHAPEDIFF_CACHE"),
		CommentAuthor:  get("SHAPEDIFF_COMMENT_AUTHOR"),
	}
	if p := get("GITHUB_EVENT_PATH"); p != "" {
		if b, err := read(p); err == nil {
			var ev githubEvent
			if json.Unmarshal(b, &ev) == nil && ev.PullRequest != nil {
				e.MRIID = ev.PullRequest.Number
				e.SourceBranchSHA = ev.PullRequest.Head.SHA
				e.DiffBaseSHA = ev.PullRequest.Base.SHA
			}
		}
	}
	if e.ProjectDir == "" {
		e.ProjectDir = "."
	}
	return e
}

// IsMergeRequestEvent reports whether this pipeline is the only kind the job
// runs in: on GitLab the one where CI_MERGE_REQUEST_DIFF_BASE_SHA exists, on
// GitHub a pull_request event. Not pull_request_target: it runs with the
// base repository's token and secrets, and this job checks out and reads the
// pull request's code.
func (e Env) IsMergeRequestEvent() bool {
	if e.Forge == forge.GitHub {
		return e.PipelineSource == "pull_request"
	}
	return e.PipelineSource == "merge_request_event"
}

// EventName is the event IsMergeRequestEvent wants, for messages.
func (e Env) EventName() string {
	if e.Forge == forge.GitHub {
		return "pull_request"
	}
	return "merge_request_event"
}

// ResolveDiffBase replaces a GitHub run's base with the merge base of base
// and head, which is what GitLab's CI_MERGE_REQUEST_DIFF_BASE_SHA already
// is: diffing against the base branch's tip would count every change merged
// to it since the branch forked as this pull request's. It needs both
// commits in the checkout (fetch-depth: 0). On failure it keeps the base
// branch's tip and returns a line saying so; on GitLab it does nothing.
func ResolveDiffBase(ctx context.Context, e Env) (Env, string) {
	if e.Forge != forge.GitHub || e.DiffBaseSHA == "" || e.HeadSHA() == "" {
		return e, ""
	}
	mb, err := MergeBase(ctx, e.ProjectDir, e.DiffBaseSHA, e.HeadSHA())
	if err != nil {
		return e, "shapediff: no merge base of " + shortSHA(e.DiffBaseSHA) + " and " + shortSHA(e.HeadSHA()) + " in the checkout (is fetch-depth 0?); diffing against the base branch's tip: " + err.Error()
	}
	e.DiffBaseSHA = mb
	return e, ""
}

// HeadSHA is step 1: the source branch SHA when set, else CI_COMMIT_SHA. In
// merged-results pipelines CI_COMMIT_SHA is an ephemeral merge-result
// commit and must never be cached under, which is why the branch SHA wins.
func (e Env) HeadSHA() string {
	if s := strings.TrimSpace(e.SourceBranchSHA); s != "" {
		return s
	}
	return strings.TrimSpace(e.CommitSHA)
}

// Repo is the repo identifier used in cache keys and documents.
func (e Env) Repo() string {
	if e.ProjectPath != "" {
		return e.ProjectPath
	}
	return e.ProjectID
}
