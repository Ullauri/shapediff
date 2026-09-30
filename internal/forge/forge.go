// Package forge is what differs between the code hosts shapediff posts to:
// the shape of a permalink and what a change request is called. The graph,
// the delta and the note are the same on every forge; only these strings
// are not.
package forge

import (
	"fmt"
	"net/url"
	"strings"
)

// Forge names a code host. The zero value is GitLab, the forge the spec was
// written for, so every caller that predates this package keeps its links.
type Forge string

const (
	GitLab Forge = "gitlab"
	GitHub Forge = "github"
)

// Parse reads a --forge flag value. "" is GitLab.
func Parse(s string) (Forge, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "gitlab":
		return GitLab, nil
	case "github":
		return GitHub, nil
	}
	return "", fmt.Errorf("forge %q: want gitlab or github", s)
}

// FromURL guesses the forge from a project web URL: github.com is GitHub,
// anything else GitLab, which is what a self-hosted instance usually is. A
// GitHub Enterprise host needs --forge github.
func FromURL(projectURL string) Forge {
	u, err := url.Parse(strings.TrimSpace(projectURL))
	if err != nil {
		return GitLab
	}
	h := strings.ToLower(u.Hostname())
	if h == "github.com" || h == "www.github.com" {
		return GitHub
	}
	return GitLab
}

func (f Forge) isGitHub() bool { return f == GitHub }

// Blob is a permalink to one line of a file at a commit.
func (f Forge) Blob(projectURL, sha, file string, line int) string {
	if f.isGitHub() {
		return fmt.Sprintf("%s/blob/%s/%s#L%d", strings.TrimRight(projectURL, "/"), sha, file, line)
	}
	return fmt.Sprintf("%s/-/blob/%s/%s#L%d", strings.TrimRight(projectURL, "/"), sha, file, line)
}

// Tree is a link to a directory at a commit.
func (f Forge) Tree(projectURL, sha, dir string) string {
	if f.isGitHub() {
		return fmt.Sprintf("%s/tree/%s/%s", strings.TrimRight(projectURL, "/"), sha, dir)
	}
	return fmt.Sprintf("%s/-/tree/%s/%s", strings.TrimRight(projectURL, "/"), sha, dir)
}

// Change is what the forge calls a change request, as the note says it:
// "This MR changes code in ..." or "This PR changes code in ...".
func (f Forge) Change() string {
	if f.isGitHub() {
		return "PR"
	}
	return "MR"
}
