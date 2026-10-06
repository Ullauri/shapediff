// Package weekly is the weekly view (spec 8.3, 10, 14.2): one delta per
// repo between main at the week's start and end, attribution from cached
// per-MR deltas, a pinned-layout package diagram, a wiki page per week and a
// cross-repo roll-up. It shares the extractor and delta engine with the
// per-MR tool and nothing else.
package weekly

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
)

// MergedMR is what attribution needs from the MR API.
type MergedMR struct {
	IID    int
	Title  string
	WebURL string
	SHA    string // head SHA at merge, the other half of the delta cache key
}

// ResolveWeek returns the SHAs of branch at start and end, via
// `git rev-list -1 --first-parent --before=<t> <branch>`. "main@week_start"
// is not re-derivable later, so callers print both on the page.
//
// --first-parent keeps the walk on the branch itself. Without it git
// returns the newest commit by date among all ancestors, and a commit made
// on a feature branch before the week but merged during it qualifies: the
// week then started inside that branch, and the change it merged was left
// out of the week it landed in.
func ResolveWeek(ctx context.Context, repoDir, branch string, start, end time.Time) (baseSHA, headSHA string, err error) {
	baseSHA, err = revBefore(ctx, repoDir, branch, start)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s at week start: %w", branch, err)
	}
	headSHA, err = revBefore(ctx, repoDir, branch, end)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s at week end: %w", branch, err)
	}
	return baseSHA, headSHA, nil
}

func revBefore(ctx context.Context, repoDir, branch string, t time.Time) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-list", "-1", "--first-parent", "--before="+t.UTC().Format(time.RFC3339), branch).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-list: %v: %s", err, strings.TrimSpace(string(out)))
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("no commit on %s before %s", branch, t.Format(time.RFC3339))
	}
	return sha, nil
}

// WeekWindow returns [Monday 00:00 UTC, next Monday 00:00 UTC) of the ISO
// week containing t.
func WeekWindow(t time.Time) (start, end time.Time) {
	t = t.UTC()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	start = time.Date(t.Year(), t.Month(), t.Day()-(wd-1), 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 0, 7)
}

// PageTitle is the wiki title for the week containing t: Structure/Week-YYYY-WW.
func PageTitle(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("Structure/Week-%d-%02d", y, w)
}

// Attribute joins each node changed in the weekly delta to the MRs whose
// cached per-MR delta lists it in added, removed, signature_changed or
// renamed. load fetches a cached delta; false when the cache has none for
// that MR (its job did not run, or ran at a different head SHA). The result
// maps node ID to MR iids, ascending; nodes nobody claims are absent.
func Attribute(d *delta.Delta, mrs []MergedMR, load func(iid int, sha string) (*delta.Delta, bool)) map[string][]int {
	changed := map[string]bool{}
	for _, id := range d.Nodes.Added {
		changed[id] = true
	}
	for _, id := range d.Nodes.Removed {
		changed[id] = true
	}
	for _, s := range d.Nodes.SignatureChanged {
		changed[s.ID] = true
	}
	for _, r := range d.Nodes.Renamed {
		changed[r.From] = true
		changed[r.To] = true
	}
	out := map[string][]int{}
	for _, mr := range mrs {
		md, ok := load(mr.IID, mr.SHA)
		if !ok {
			continue
		}
		claim := func(id string) {
			if changed[id] {
				out[id] = append(out[id], mr.IID)
			}
		}
		for _, id := range md.Nodes.Added {
			claim(id)
		}
		for _, id := range md.Nodes.Removed {
			claim(id)
		}
		for _, s := range md.Nodes.SignatureChanged {
			claim(s.ID)
		}
		for _, r := range md.Nodes.Renamed {
			claim(r.From)
			claim(r.To)
		}
	}
	for id := range out {
		sort.Ints(out[id])
		out[id] = dedupe(out[id])
	}
	return out
}

func dedupe(xs []int) []int {
	if len(xs) == 0 {
		return xs
	}
	o := xs[:1]
	for _, x := range xs[1:] {
		if x != o[len(o)-1] {
			o = append(o, x)
		}
	}
	return o
}
