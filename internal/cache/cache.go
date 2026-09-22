// Package cache stores extracted graphs per (repo, sha, extractor version,
// toolchain, builder mode, config hash) and per-MR deltas per (repo, mr
// iid, head sha), as spec section 14.1 requires. Two backends: a local
// directory and the GitLab generic package registry. S3 is not built.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shapediff/internal/gitlab"
)

// Key identifies one cached graph. Every field is part of the key so an
// extractor upgrade, config edit or Go bump never diffs a new-format head
// against an old-format base.
type Key struct {
	Repo             string
	SHA              string
	ExtractorVersion string
	Toolchain        string
	BuilderMode      string // "" when the backend has none
	ConfigHash       string
}

// String renders the key as one filesystem-safe token: the readable parts
// joined by "_" plus a hash of the whole tuple, so two keys that differ only
// in a sanitized character can never collide.
func (k Key) String() string {
	tuple := strings.Join([]string{k.Repo, k.SHA, k.ExtractorVersion, k.Toolchain, k.BuilderMode, k.ConfigHash}, "\x00")
	sum := sha256.Sum256([]byte(tuple))
	short := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	return fmt.Sprintf("graph_%s_%s_%s_%s", safe(k.Repo), short(k.SHA, 12), safe(k.ExtractorVersion), hex.EncodeToString(sum[:8]))
}

// MRKey identifies one MR's cached delta at one head SHA.
func MRKey(repo string, iid int, headSHA string) string {
	if len(headSHA) > 12 {
		headSHA = headSHA[:12]
	}
	return fmt.Sprintf("delta_%s_mr%d_%s", safe(repo), iid, headSHA)
}

func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Store is the cache interface. Keys are the strings above.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, data []byte) error
}

// Dir is a directory of files named by key.
type Dir struct{ Path string }

// Get reads key; (nil, false, nil) when absent.
func (d Dir) Get(_ context.Context, key string) ([]byte, bool, error) {
	b, err := os.ReadFile(filepath.Join(d.Path, key+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Put writes key atomically.
func (d Dir) Put(_ context.Context, key string, data []byte) error {
	if err := os.MkdirAll(d.Path, 0o755); err != nil {
		return err
	}
	final := filepath.Join(d.Path, key+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Packages stores entries in the GitLab generic package registry: one
// package per repo (Name), the key as the version, one file per entry.
type Packages struct {
	Client  *gitlab.Client
	Project gitlab.Project
	Name    string // package name, e.g. "shapediff"
}

const packageFile = "entry.json"

// Get downloads key; (nil, false, nil) when absent.
func (p Packages) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return p.Client.DownloadPackageFile(ctx, p.Project, p.Name, key, packageFile)
}

// Put uploads key.
func (p Packages) Put(ctx context.Context, key string, data []byte) error {
	return p.Client.UploadPackageFile(ctx, p.Project, p.Name, key, packageFile, data)
}

// EnvSpec is what FromEnv reads: SHAPEDIFF_CACHE is "dir:<path>" or
// "gitlab"; when unset the default is <projectDir>/.shapediff-cache.
type EnvSpec struct {
	Value      string // SHAPEDIFF_CACHE
	ProjectDir string // CI_PROJECT_DIR or "."
	Client     *gitlab.Client
	Project    gitlab.Project
}

// FromEnv picks a backend. "gitlab" requires Client and Project.
func FromEnv(spec EnvSpec) (Store, error) {
	v := strings.TrimSpace(spec.Value)
	switch {
	case v == "":
		dir := spec.ProjectDir
		if dir == "" {
			dir = "."
		}
		return Dir{Path: filepath.Join(dir, ".shapediff-cache")}, nil
	case strings.HasPrefix(v, "dir:"):
		return Dir{Path: strings.TrimPrefix(v, "dir:")}, nil
	case v == "gitlab":
		if spec.Client == nil || spec.Project == "" {
			return nil, errors.New("cache: SHAPEDIFF_CACHE=gitlab needs an API client and project")
		}
		return Packages{Client: spec.Client, Project: spec.Project, Name: "shapediff"}, nil
	}
	return nil, fmt.Errorf("cache: SHAPEDIFF_CACHE=%q, want dir:<path> or gitlab", v)
}
