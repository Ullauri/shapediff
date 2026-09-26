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
	Baseline *filter.Baseline
	// Summary is the "What changed" list at the top of the note (section
	// 14.1): one item per fact class, each a bold count with the names under
	// it. Unchanged is the line under it naming the standing counts that are
	// zero.
	Summary     []SummaryItem
	Unchanged   string
	Suppression string
	ProjectURL  string // e.g. https://gitlab.example.com/group/svc, "" disables permalinks
	// WeeklyURL is the weekly structure brief (section 14.2). When set, the
	// note ends with a pointer to it, for the reader who is behind on the
	// repo rather than only on this MR. "" prints nothing.
	WeeklyURL string
	HeadSHA   string
	BaseSHA   string
	MRIID     int
	Extractor graph.Extractor
	Elapsed   time.Duration
	// FallbackText is shown in place of a diagram that fell back to SVG,
	// e.g. a link to the uploaded attachment; when empty a generic line is
	// printed.
	FallbackText string
}

// SummaryItem is one item of the "What changed" list: Text, then Items
// nested under it, one per line.
type SummaryItem struct {
	Text  string
	Items []string
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
	writeSummary(&b, in)
	// One key, above the first diagram. Under the call paths it was invisible
	// to a reader of the dataflow diagram, and repeating it per diagram turns
	// it into the kind of chrome section 11 warns reviewers learn to skip.
	// The package map, when drawn, is the first diagram, so writeBaseline
	// prints the key; otherwise it goes above the first delta diagram.
	pkgMap := in.Baseline != nil && in.Baseline.Map != nil
	delta := len(in.Filter.Dataflows) > 0 || len(in.Filter.CallPaths) > 0 || in.Filter.Wiring != nil
	key := ""
	if pkgMap || delta {
		key = Key(pkgMap, delta)
	}
	if in.Baseline != nil && (in.Baseline.Map != nil || len(in.Baseline.Touched) > 0) {
		writeBaseline(&b, in, key)
	}
	if delta && !pkgMap {
		fmt.Fprintf(&b, "\n%s\n", key)
	}
	if dfs := in.Filter.Dataflows; len(dfs) > 0 {
		// One diagram per entry point, each with the sentence that reads it:
		// several entry points drawn into one diagram crossed their edges
		// over a shared column of packages.
		// The diagrams are read side by side, so they share one naming.
		all := make([]*filter.Diagram, len(dfs))
		for i := range dfs {
			all[i] = &dfs[i]
		}
		names := shortNamesOf(all...)
		b.WriteString("\n#### Dataflow\n")
		for i := range dfs {
			dg := dfs[i]
			fmt.Fprintf(&b, "\n**%s**\n\n", mdEsc(dg.Title))
			if s := dataflowBlurb(in, &dg, names); s != "" {
				b.WriteString(s + "\n\n")
			}
			fence(&b, mermaidNamed(&dg, names))
		}
	}
	if len(in.Filter.CallPaths) > 0 && len(in.Filter.CallPaths[0].Nodes) > 0 {
		b.WriteString("\n#### Call paths\n\n")
		primary := in.Filter.CallPaths[0]
		// A split diagram is titled by its entry point; an unsplit one is
		// titled "call paths", which the heading already says.
		if len(in.Filter.CallPaths) > 1 && primary.Title != "call paths" {
			fmt.Fprintf(&b, "**%s**\n\n", mdEsc(primary.Title))
		}
		if in.Filter.Fallback {
			if in.FallbackText != "" {
				b.WriteString(in.FallbackText + "\n")
			} else {
				b.WriteString("The call-path diagram exceeds the Mermaid size limit for this instance; see the attached SVG.\n")
			}
		} else {
			fence(&b, Mermaid(&primary))
		}
		b.WriteString("\n" + CallPathBlurb(in, &primary) + "\n")
		if rest := in.Filter.CallPaths[1:]; len(rest) > 0 {
			fmt.Fprintf(&b, "\n<details><summary>%d more call-path %s</summary>\n\n", len(rest), plural(len(rest), "diagram", "diagrams"))
			for i := range rest {
				dg := rest[i]
				fmt.Fprintf(&b, "**%s**\n\n", mdEsc(dg.Title))
				fence(&b, Mermaid(&dg))
				b.WriteString("\n" + CallPathBlurb(in, &dg) + "\n\n")
			}
			b.WriteString("</details>\n")
		}
	}
	writeStacks(&b, in)
	if in.Filter.Wiring != nil {
		b.WriteString("\n#### Module wiring\n\n")
		fence(&b, Mermaid(in.Filter.Wiring))
	}
	b.WriteString("\n<details><summary>Details</summary>\n\n")
	writeNodeTable(&b, in)
	writeLists(&b, in)
	fmt.Fprintf(&b, "\nextractor %s %s (%s, %s), base %s, head %s, %s\n", in.Extractor.Name, in.Extractor.Version, in.Extractor.Precision, in.Extractor.Toolchain, short(in.BaseSHA), short(in.HeadSHA), in.Elapsed.Round(time.Millisecond))
	b.WriteString("</details>\n")
	if in.WeeklyURL != "" {
		fmt.Fprintf(&b, "\nBehind on this repo? The weekly structure brief covers what it has become: %s\n", in.WeeklyURL)
	}
	return b.String(), nil
}

// writeSummary is the top of the note: what changed, as a list. It was a
// line of counts and a paragraph naming the changes, and both had to be
// read across to find anything; a list is scanned down.
func writeSummary(b *strings.Builder, in NoteInput) {
	b.WriteString("#### What changed\n")
	if in.Suppression != "" {
		fmt.Fprintf(b, "\n%s\n", in.Suppression)
	}
	if len(in.Summary) > 0 {
		b.WriteString("\n")
		for _, s := range in.Summary {
			fmt.Fprintf(b, "- %s\n", s.Text)
			for _, it := range s.Items {
				fmt.Fprintf(b, "  - %s\n", it)
			}
		}
	}
	if in.Unchanged != "" {
		fmt.Fprintf(b, "\n%s\n", in.Unchanged)
	}
}

// writeBaseline is the "Where this lands" section (spec 10.1): one line
// on the repo's size at the base, one row per package the MR changed
// inside, then the package map with those packages highlighted. It is
// written for a reader who does not know the repo: every name is a
// package, an entry point or a sink, and every package links to its
// directory at the base SHA. key is the note's one key, printed above
// the map when the map is drawn.
func writeBaseline(b *strings.Builder, in NoteInput, key string) {
	bl := in.Baseline
	b.WriteString("\n#### Where this lands\n\n")
	fmt.Fprintf(b, "At base %s the repo has %d %s, %d %s and %d %s",
		short(in.BaseSHA), bl.Packages, plural(bl.Packages, "package", "packages"),
		bl.Entrypoints, plural(bl.Entrypoints, "entry point", "entry points"),
		bl.Sinks, plural(bl.Sinks, "sink", "sinks"))
	if bl.External > 0 {
		fmt.Fprintf(b, " (%d external %s not drawn)", bl.External, plural(bl.External, "module", "modules"))
	}
	changed, added := 0, 0
	for _, t := range bl.Touched {
		if t.Status == filter.StatusAdded {
			added++
		} else {
			changed++
		}
	}
	switch {
	case changed == 0 && added == 0:
		b.WriteString(". This MR changes code in none of them.\n")
	case changed == 0:
		fmt.Fprintf(b, ". This MR adds %d new %s.\n", added, plural(added, "package", "packages"))
	case added == 0:
		fmt.Fprintf(b, ". This MR changes code in %d of them.\n", changed)
	default:
		fmt.Fprintf(b, ". This MR changes code in %d of them and adds %d new %s.\n", changed, added, plural(added, "package", "packages"))
	}
	if len(bl.Touched) > 0 {
		// One list item per package, not a table row: six columns wrapped
		// to a word a line at note width, and a count ("3 added") did not
		// say what was added.
		b.WriteString("\n")
		for _, t := range bl.Touched {
			writeTouched(b, in, t)
		}
	}
	if bl.Map == nil {
		if bl.MapOmitted {
			b.WriteString("\nThe package map exceeds the Mermaid size limit for this instance even at its smallest; not drawn.\n")
		}
		return
	}
	fmt.Fprintf(b, "\n%s\n\n", key)
	fence(b, Mermaid(bl.Map))
	var cut []string
	if bl.Collapsed > 0 {
		cut = append(cut, fmt.Sprintf("%d untouched %s folded into their directory", bl.Collapsed, plural(bl.Collapsed, "package", "packages")))
	}
	if bl.Hidden > 0 {
		cut = append(cut, fmt.Sprintf("%d %s hidden", bl.Hidden, plural(bl.Hidden, "package", "packages")))
	}
	if len(cut) > 0 {
		b.WriteString(strings.Join(cut, " and ") + " to fit.\n")
	}
}

// maxNamed is how many definitions a package's list item names per kind
// of change before "and N more".
const maxNamed = 5

// writeTouched is one package of "Where this lands": its name and size,
// then one line per kind of change naming what changed, then who depends
// on it and what it talks to. A line with nothing to say is left out.
func writeTouched(b *strings.Builder, in NoteInput, t filter.TouchedPackage) {
	name := "`" + mdEsc(t.Label) + "`"
	if t.Dir != "" && in.ProjectURL != "" {
		sha := in.BaseSHA
		if t.Status == filter.StatusAdded {
			sha = in.HeadSHA
		}
		name = fmt.Sprintf("[%s](%s/-/tree/%s/%s)", name, strings.TrimRight(in.ProjectURL, "/"), sha, t.Dir)
	}
	size := graph.DescribeKinds(t.Kinds, 0)
	if size == "" {
		size = fmt.Sprintf("%d %s", t.Symbols, plural(t.Symbols, "definition", "definitions"))
	}
	switch t.Status {
	case filter.StatusAdded:
		size = "new package, " + size
	case filter.StatusRemoved:
		size = "removed package, " + size + " at base"
	}
	fmt.Fprintf(b, "- **%s**: %s\n", name, size)
	type change struct {
		n    int
		ids  []string
		what string
	}
	var changes []change
	// "new package" and "removed package" already say where every
	// definition went.
	if t.Status != filter.StatusAdded {
		changes = append(changes, change{t.Added, t.AddedIDs, "added"})
	}
	if t.Status != filter.StatusRemoved {
		changes = append(changes, change{t.Removed, t.RemovedIDs, "removed"})
	}
	changes = append(changes, change{t.Renamed, t.RenamedIDs, "renamed"})
	if len(t.SignatureIDs)+len(t.BodyIDs) > 0 {
		changes = append(changes, change{len(t.SignatureIDs), t.SignatureIDs, "changed signature"},
			change{len(t.BodyIDs), t.BodyIDs, "edited, same signature"})
	} else {
		changes = append(changes, change{t.Changed, nil, "changed"})
	}
	for _, c := range changes {
		if c.n == 0 {
			continue
		}
		line := fmt.Sprintf("%d %s", c.n, c.what)
		if names := namedList(c.ids, t.RenamedFrom); names != "" {
			line += ": " + names
		}
		fmt.Fprintf(b, "  - %s\n", line)
	}
	for _, r := range []struct {
		label string
		ids   []string
		entry bool
	}{{"Imported by", t.ImportedBy, false}, {"Reached from", t.Entrypoints, true}, {"Talks to", t.Sinks, false}} {
		if len(r.ids) == 0 {
			continue
		}
		var status map[string]string
		if r.entry {
			status = in.Baseline.EntryStatus
		}
		fmt.Fprintf(b, "  - %s %s\n", r.label, shortList(r.ids, 4, status))
	}
}

// namedList names up to maxNamed definitions, "`Old` → `New`" for a
// rename, then "and N more".
func namedList(ids []string, from map[string]string) string {
	var parts []string
	for i, id := range ids {
		if i == maxNamed {
			parts = append(parts, fmt.Sprintf("and %d more", len(ids)-maxNamed))
			break
		}
		part := "`" + mdEsc(graph.ShortName(id)) + "`"
		if f, ok := from[id]; ok {
			part = "`" + mdEsc(graph.ShortName(f)) + "` → " + part
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// shortList renders up to max names from IDs, then "+N"; "—" when empty.
// Sinks carry their kind so a table named like a package reads
// unambiguously; an entry point the MR added or removed says so, since
// the row is otherwise a base fact.
func shortList(ids []string, max int, status map[string]string) string {
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
		part := "`" + mdEsc(name) + "`"
		switch status[id] {
		case "added":
			part += " (new)"
		case "removed":
			part += " (removed)"
		}
		parts = append(parts, part)
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
	// Nodes pruned for having no drawn edge are not in any diagram but still
	// belong in the table: the row is where their permalink lives.
	for _, n := range in.Filter.Omitted.Orphans {
		if seen[n.ID] || n.Kind == filter.KindMore || n.Kind == filter.KindPackage {
			continue
		}
		seen[n.ID] = true
		rows = append(rows, n)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) == 0 {
		return
	}
	// The node is its own link: a separate link column spent a third of
	// the width on file:line text and squeezed the names into a sliver.
	b.WriteString("| node | status | package |\n|---|---|---|\n")
	for _, n := range rows {
		name := "`" + mdEsc(Label(n)) + "`"
		if u := permalinkURL(in, n.Status, n.File, n.Line); u != "" {
			name = "[" + name + "](" + u + ")"
		}
		b.WriteString("| " + name + " | " + strings.ReplaceAll(n.Status, "_", " ") + " | " + mdEsc(n.Package) + " |\n")
	}
}

// Permalink returns a markdown link to the node's file at the head SHA (base
// SHA for removed nodes), or "" when the project URL or file is unknown.
func Permalink(in NoteInput, n filter.Node) string {
	url := permalinkURL(in, n.Status, n.File, n.Line)
	if url == "" {
		return ""
	}
	return fmt.Sprintf("[%s:%d](%s)", mdEsc(n.File), n.Line, url)
}

// permalinkURL is the URL Permalink links to.
func permalinkURL(in NoteInput, status, file string, line int) string {
	if in.ProjectURL == "" || file == "" {
		return ""
	}
	sha := in.HeadSHA
	if status == filter.StatusRemoved {
		sha = in.BaseSHA
	}
	return fmt.Sprintf("%s/-/blob/%s/%s#L%d", strings.TrimRight(in.ProjectURL, "/"), sha, file, line)
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
	if om.HiddenNodes+om.CollapsedSymbols+om.ExcludedGenerated+om.ExcludedTest+om.ExcludedTestPackages+len(om.Orphans) > 0 {
		fmt.Fprintf(b, "\n%d nodes hidden by the budget, %d on no drawn path, %d definitions collapsed into packages, %d generated and %d test definitions not drawn, %d test packages left out of the wiring.\n", om.HiddenNodes, len(om.Orphans), om.CollapsedSymbols, om.ExcludedGenerated, om.ExcludedTest, om.ExcludedTestPackages)
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
