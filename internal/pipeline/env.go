// Package pipeline is the per-MR CI job of spec section 14.1, written as an
// orchestration over injected dependencies so it can be tested without the
// extractor or a GitLab instance.
package pipeline

import (
	"strconv"
	"strings"
)

// Env is what the job reads from the CI environment.
type Env struct {
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
}

// EnvFrom parses the CI variables from a lookup function (os.Getenv or a
// test map).
func EnvFrom(get func(string) string) Env {
	iid, _ := strconv.Atoi(strings.TrimSpace(get("CI_MERGE_REQUEST_IID")))
	e := Env{
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
	}
	if e.ProjectDir == "" {
		e.ProjectDir = "."
	}
	return e
}

// IsMergeRequestEvent reports whether this pipeline is the only kind the job
// runs in: the one where CI_MERGE_REQUEST_DIFF_BASE_SHA exists.
func (e Env) IsMergeRequestEvent() bool { return e.PipelineSource == "merge_request_event" }

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
