package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Marker is the first line of every note the tool posts; the post command
// finds the note to update by it.
const Marker = "<!-- shapediff:v1 -->"

// NoteInput is everything the note needs.
type NoteInput struct {
	Delta      *delta.Delta
	Head, Base *graph.Index
	Filter     *filter.Result
	// Baseline is the "where this lands" view; nil leaves the section out.
	Baseline    *filter.Baseline
	Headline    string
	Suppression string
	Sentences   []string
	ProjectURL  string // e.g. https://gitlab.example.com/group/svc, "" disables permalinks
	HeadSHA     string
	BaseSHA     string
	MRIID       int
	Extractor   graph.Extractor
	Elapsed     time.Duration
	// FallbackText is shown in place of a diagram that fell back to SVG,
	// e.g. a link to the uploaded attachment; when empty a generic line is
	// printed.
	FallbackText string
}

// NoStructuralNote is the one-line body used when a later push removed all
// structural change from an MR that already has a note.
func NoStructuralNote(headSHA string) string {
	return Marker + "\nno structural change at " + short(headSHA) + "\n"
}

// Note assembles the MR note in section 14.1's order.
func Note(in NoteInput) (string, error) {
	if in.Delta == nil || in.Filter == nil {
		return "", fmt.Errorf("note needs a delta and a filter result")
	}
	var b strings.Builder
	b.WriteString(Marker + "\n")
	fmt.Fprintf(&b, "**%s**\n", in.Headline)
	if in.Suppression != "" {
		fmt.Fprintf(&b, "\n%s\n", in.Suppression)
	}
	if len(in.Sentences) > 0 {
		b.WriteString("\n" + strings.Join(in.Sentences, " ") + "\n")
	}
	if in.Baseline != nil && in.Baseline.Packages > 0 {
		writeBaseline(&b, in)
	}
	if in.Filter.Dataflow != nil {
		b.WriteString("\n#### Dataflow\n\n")
		fence(&b, Mermaid(in.Filter.Dataflow))
	}
	if len(in.Filter.CallPaths) > 0 && len(in.Filter.CallPaths[0].Nodes) > 0 {
		b.WriteString("\n#### Call paths\n\n")
		primary := in.Filter.CallPaths[0]
		if in.Filter.Fallback {
			if in.FallbackText != "" {
				b.WriteString(in.FallbackText + "\n")
			} else {
				b.WriteString("The call-path diagram exceeds the Mermaid size limit for this instance; see the attached SVG.\n")
			}
		} else {
			fence(&b, Mermaid(&primary))
			b.WriteString(Legend + "\n")
		}
		if rest := in.Filter.CallPaths[1:]; len(rest) > 0 {
			fmt.Fprintf(&b, "\n<details><summary>%d more call-path %s</summary>\n\n", len(rest), plural(len(rest), "diagram", "diagrams"))
			for i := range rest {
				dg := rest[i]
				fmt.Fprintf(&b, "**%s**\n\n", dg.Title)
				fence(&b, Mermaid(&dg))
			}
			b.WriteString("</details>\n")
		}
	}
	if in.Filter.Wiring != nil {
		b.WriteString("\n#### Module wiring\n\n")
		fence(&b, Mermaid(in.Filter.Wiring))
	}
	b.WriteString("\n<details><summary>Details</summary>\n\n")
	writeNodeTable(&b, in)
	writeLists(&b, in)
	fmt.Fprintf(&b, "\nextractor %s %s (%s, %s), base %s, head %s, %s\n", in.Extractor.Name, in.Extractor.Version, in.Extractor.Precision, in.Extractor.Toolchain, short(in.BaseSHA), short(in.HeadSHA), in.Elapsed.Round(time.Millisecond))
	b.WriteString("</details>\n")
	return b.String(), nil
}

// BaselineLegend is the line printed under the baseline map.
const BaselineLegend = "Legend: purple changed by this MR, green added, red dashed removed, grey untouched; rounded entry points, cylinder sinks; arrows are imports; boxes are directories."

// writeBaseline is the "Where this lands" section (spec 10.1): one line
// on the repo's size at the base, one row per package the MR changed
// inside, then the package map with those packages highlighted. It is
// written for a reader who does not know the repo: every name is a
// package, an entry point or a sink, and every package links to its
// directory at the base SHA.
func writeBaseline(b *strings.Builder, in NoteInput) {
	bl := in.Baseline
	b.WriteString("\n#### Where this lands\n\n")
	fmt.Fprintf(b, "At base %s the repo has %d %s, %d %s and %d %s",
		short(in.BaseSHA), bl.Packages, plural(bl.Packages, "package", "packages"),
		bl.Entrypoints, plural(bl.Entrypoints, "entry point", "entry points"),
		bl.Sinks, plural(bl.Sinks, "sink", "sinks"))
	if bl.External > 0 {
		fmt.Fprintf(b, " (%d external %s not drawn)", bl.External, plural(bl.External, "module", "modules"))
	}
	switch n := len(bl.Touched); n {
	case 0:
		b.WriteString(". This MR changes code in none of them.\n")
	default:
		fmt.Fprintf(b, ". This MR changes code in %d of them.\n", n)
	}
	if len(bl.Touched) > 0 {
		b.WriteString("\n| package | symbols | this MR | imported by | reached from | talks to |\n|---|---|---|---|---|---|\n")
		for _, t := range bl.Touched {
			name := "`" + mdEsc(t.Label) + "`"
			if t.Dir != "" && in.ProjectURL != "" {
				sha := in.BaseSHA
				if t.Status == filter.StatusAdded {
					sha = in.HeadSHA
				}
				name = fmt.Sprintf("[%s](%s/-/tree/%s/%s)", name, strings.TrimRight(in.ProjectURL, "/"), sha, t.Dir)
			}
			var did []string
			switch t.Status {
			case filter.StatusAdded:
				did = append(did, "new package")
			case filter.StatusRemoved:
				did = append(did, "removed")
			}
			for _, c := range []struct {
				n    int
				what string
			}{{t.Added, "added"}, {t.Removed, "removed"}, {t.Changed, "changed"}, {t.Renamed, "renamed"}} {
				// "new package" and "removed" already say where every symbol went
				if c.n == 0 || (t.Status == filter.StatusAdded && c.what == "added") || (t.Status == filter.StatusRemoved && c.what == "removed") {
					continue
				}
				did = append(did, fmt.Sprintf("%d %s", c.n, c.what))
			}
			fmt.Fprintf(b, "| %s | %d | %s | %s | %s | %s |\n", name, t.Symbols, strings.Join(did, ", "), shortList(t.ImportedBy, 4), shortList(t.Entrypoints, 4), shortList(t.Sinks, 4))
		}
	}
	if bl.Map == nil {
		if bl.MapOmitted {
			b.WriteString("\nThe package map exceeds the Mermaid size limit for this instance even at its smallest; not drawn.\n")
		}
		return
	}
	b.WriteString("\n")
	fence(b, Mermaid(bl.Map))
	b.WriteString(BaselineLegend)
	var cut []string
	if bl.Collapsed > 0 {
		cut = append(cut, fmt.Sprintf("%d untouched %s folded into their directory", bl.Collapsed, plural(bl.Collapsed, "package", "packages")))
	}
	if bl.Hidden > 0 {
		cut = append(cut, fmt.Sprintf("%d %s hidden", bl.Hidden, plural(bl.Hidden, "package", "packages")))
	}
	if len(cut) > 0 {
		b.WriteString(" " + strings.Join(cut, " and ") + " to fit.")
	}
	b.WriteString("\n")
}

// shortList renders up to max names from IDs, then "+N"; "—" when empty.
// Sinks carry their kind so a table named like a package reads
// unambiguously.
func shortList(ids []string, max int) string {
	if len(ids) == 0 {
		return "—"
	}
	var parts []string
	for i, id := range ids {
		if i == max {
			parts = append(parts, fmt.Sprintf("+%d", len(ids)-max))
			break
		}
		name := graph.ShortName(id)
		if graph.IDPrefix(id) == "sink" {
			if p := strings.SplitN(id, ":", 3); len(p) == 3 {
				name = p[1] + " " + p[2]
			}
		}
		parts = append(parts, "`"+mdEsc(name)+"`")
	}
	return strings.Join(parts, ", ")
}

func fence(b *strings.Builder, src string) {
	b.WriteString("```mermaid\n")
	b.WriteString(src)
	if !strings.HasSuffix(src, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
}

func writeNodeTable(b *strings.Builder, in NoteInput) {
	seen := map[string]bool{}
	var rows []filter.Node
	for _, dg := range in.Filter.CallPaths {
		for _, n := range dg.Nodes {
			if seen[n.ID] || n.Kind == filter.KindMore || n.Kind == filter.KindPackage {
				continue
			}
			seen[n.ID] = true
			rows = append(rows, n)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) == 0 {
		return
	}
	b.WriteString("| node | status | package | link |\n|---|---|---|---|\n")
	for _, n := range rows {
		b.WriteString("| `" + mdEsc(Label(n)) + "` | " + n.Status + " | " + mdEsc(n.Package) + " | " + Permalink(in, n) + " |\n")
	}
}

// Permalink returns a markdown link to the node's file at the head SHA (base
// SHA for removed nodes), or "" when the project URL or file is unknown.
func Permalink(in NoteInput, n filter.Node) string {
	if in.ProjectURL == "" || n.File == "" {
		return ""
	}
	sha := in.HeadSHA
	if n.Status == filter.StatusRemoved {
		sha = in.BaseSHA
	}
	url := fmt.Sprintf("%s/-/blob/%s/%s#L%d", strings.TrimRight(in.ProjectURL, "/"), sha, n.File, n.Line)
	return fmt.Sprintf("[%s:%d](%s)", mdEsc(n.File), n.Line, url)
}

func writeLists(b *strings.Builder, in NoteInput) {
	d := in.Delta
	if len(d.Nodes.Renamed) > 0 {
		b.WriteString("\n**Renames**\n\n")
		for _, r := range d.Nodes.Renamed {
			q := ""
			if r.Confidence == "medium" {
				q = " ?"
			}
			fmt.Fprintf(b, "- `%s` → `%s`%s (%s)\n", mdEsc(graph.ShortName(r.From)), mdEsc(graph.ShortName(r.To)), q, r.Via)
		}
	}
	if len(in.Filter.Omitted.BodyOnly) > 0 {
		b.WriteString("\n**Body-only changes**\n\n")
		for _, id := range in.Filter.Omitted.BodyOnly {
			fmt.Fprintf(b, "- `%s`\n", mdEsc(strings.TrimPrefix(id, "sym:")))
		}
	}
	if len(in.Filter.Omitted.WideReach) > 0 {
		b.WriteString("\n**Wide-reach changes** (through dispatch sites with more targets than the narrow limit; listed, not drawn)\n\n")
		for _, w := range in.Filter.Omitted.WideReach {
			if len(w.Added) > 0 {
				fmt.Fprintf(b, "- `%s` now reaches %s\n", mdEsc(graph.ShortName(w.Entrypoint)), codes(w.Added))
			}
			if len(w.Removed) > 0 {
				fmt.Fprintf(b, "- `%s` no longer reaches %s\n", mdEsc(graph.ShortName(w.Entrypoint)), codes(w.Removed))
			}
		}
	}
	if len(d.Suppressed) > 0 {
		b.WriteString("\n**Suppressed packages**\n\n")
		for _, s := range d.Suppressed {
			fmt.Fprintf(b, "- `%s`: base %s, head %s, %d removals and %d additions hidden\n", mdEsc(strings.TrimPrefix(s.Package, "pkg:")), s.Base, s.Head, s.HiddenRemovals, s.HiddenAdditions)
		}
	}
	om := in.Filter.Omitted
	if om.HiddenNodes+om.CollapsedSymbols+om.ExcludedGenerated+om.ExcludedTest > 0 {
		fmt.Fprintf(b, "\n%d nodes hidden by the budget, %d symbols collapsed into packages, %d generated and %d test symbols not drawn.\n", om.HiddenNodes, om.CollapsedSymbols, om.ExcludedGenerated, om.ExcludedTest)
	}
	if len(d.Warnings) > 0 {
		b.WriteString("\n**Warnings**\n\n")
		for _, w := range d.Warnings {
			fmt.Fprintf(b, "- %s\n", mdEsc(w))
		}
	}
}

func codes(ids []string) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, "`"+mdEsc(graph.ShortName(id))+"`")
	}
	return strings.Join(parts, ", ")
}

func mdEsc(s string) string {
	return strings.NewReplacer("|", "\\|", "`", "'").Replace(s)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
