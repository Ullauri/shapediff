package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/forge"
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
	// Forge is the host ProjectURL is on: it shapes the permalinks and names
	// the change ("MR" or "PR"). The zero value is GitLab.
	Forge forge.Forge
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
	// e.g. a link to the uploaded attachment; when empty a line says the
	// diagram is not drawn and where its content is instead. Nothing
	// uploads the SVG yet, so the generic line must not promise one.
	FallbackText string
}

// SummaryItem is one item of the "What changed" list: Text, then Items
// nested under it, one per line. IDs, when set, is the definition each item
// names, index for index ("" for none), and such an item opens to it.
type SummaryItem struct {
	Text  string
	Items []string
	IDs   []string
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
	// One line says what this is and what it compares, so a reader who
	// meets the comment cold is not left to infer it from a heading.
	fmt.Fprintf(&b, "**shapediff** · what this %s did to the shape of the code · base `%s` → head `%s`\n\n", in.Forge.Change(), short(in.BaseSHA), short(in.HeadSHA))
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
		key = keyFor(pkgMap, delta, in.Forge.Change())
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
		b.WriteString("\n### Dataflow\n")
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
		b.WriteString("\n### Call paths\n\n")
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
				b.WriteString("The call-path diagram is over this instance's Mermaid size limit, so it is not drawn. The list below names what it would show, and the Details table links each definition to its line.\n")
			}
		} else {
			fence(&b, Mermaid(&primary))
			if c := remainderCaption(&primary); c != "" {
				b.WriteString(c + "\n")
			}
		}
		b.WriteString("\n" + CallPathBlurb(in, &primary) + "\n")
		if rest := in.Filter.CallPaths[1:]; len(rest) > 0 {
			fmt.Fprintf(&b, "\n<details><summary>%d more call-path %s</summary>\n\n", len(rest), plural(len(rest), "diagram", "diagrams"))
			for i := range rest {
				dg := rest[i]
				fmt.Fprintf(&b, "**%s**\n\n", mdEsc(dg.Title))
				fence(&b, Mermaid(&dg))
				if c := remainderCaption(&dg); c != "" {
					b.WriteString(c + "\n")
				}
				b.WriteString("\n" + CallPathBlurb(in, &dg) + "\n\n")
			}
			b.WriteString("</details>\n")
		}
	}
	writeStacks(&b, in)
	if in.Filter.Wiring != nil {
		b.WriteString("\n### Module wiring\n\n")
		fence(&b, Mermaid(in.Filter.Wiring))
	}
	fmt.Fprintf(&b, "\n<details><summary>%s</summary>\n\n", detailsSummary(in))
	labels := itemLabels(in)
	writeNodeTable(&b, in, labels)
	writeLists(&b, in, labels)
	fmt.Fprintf(&b, "\nextractor %s %s (%s, %s), base %s, head %s, %s\n", in.Extractor.Name, in.Extractor.Version, in.Extractor.Precision, in.Extractor.Toolchain, short(in.BaseSHA), short(in.HeadSHA), in.Elapsed.Round(time.Millisecond))
	b.WriteString("</details>\n")
	if in.WeeklyURL != "" {
		fmt.Fprintf(&b, "\nBehind on this repo? The weekly structure brief covers what it has become: %s\n", in.WeeklyURL)
	}
	return b.String(), nil
}

// maxOpenRows is how many definitions a count in "What it did" shows as
// rows before the rest are folded under one "N more" row. Twenty
// disclosure triangles in a column were a wall; eight is a screen.
const maxOpenRows = 8

// writeSummary is the top of the note: what changed, as a list. It was a
// line of counts and a paragraph naming the changes, and both had to be
// read across to find anything; a list is scanned down. The counts come
// first and the packages they happened in second: a reviewer asks what
// the change did before asking what each package is for.
func writeSummary(b *strings.Builder, in NoteInput) {
	b.WriteString("### What changed\n")
	if in.Suppression != "" {
		fmt.Fprintf(b, "\n%s\n", in.Suppression)
	}
	if len(in.Summary) > 0 {
		if code, tests := touchGroups(in); len(code)+len(tests) > 0 {
			// "What it touches" follows the list, so the list is labelled too.
			b.WriteString("\n**What it did**\n")
		}
		b.WriteString("\n")
		labels := itemLabels(in)
		for _, s := range in.Summary {
			fmt.Fprintf(b, "- %s\n", s.Text)
			opens := false
			rows, folded := 0, false
			openable := 0
			for _, id := range s.IDs {
				if id != "" {
					openable++
				}
			}
			for i, it := range s.Items {
				id := ""
				if i < len(s.IDs) {
					id = s.IDs[i]
				}
				switch {
				case id != "" && rows >= maxOpenRows:
					if !folded {
						// The rest, behind one line, one per line with its
						// permalink: a reader who wants them opens one
						// thing; one who does not is spared a column of
						// triangles. Compact, because a note is bounded by
						// the forge's comment size, and the eight rows
						// above already carry the ones that matter most.
						fmt.Fprintf(b, "\n  <details><summary>%d more</summary>\n\n", openable-rows)
						folded = true
					}
					fmt.Fprintf(b, "  - %s\n", compactItem(in, it, id))
					rows++
				case id != "":
					opens = writeItem(b, in, it, id, labels, opens) || opens
					rows++
				case opens:
					// "and N more" under rows that open: a line, not a
					// bullet among disclosure triangles
					fmt.Fprintf(b, "\n  %s\n", it)
				default:
					fmt.Fprintf(b, "  - %s\n", it)
				}
			}
			if folded {
				b.WriteString("\n  </details>\n")
			}
		}
	}
	if in.Unchanged != "" {
		fmt.Fprintf(b, "\n%s\n", in.Unchanged)
	}
	writeTouches(b, in)
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
	b.WriteString("\n### Where this lands\n\n")
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
		fmt.Fprintf(b, ". This %s changes code in none of them.\n", in.Forge.Change())
	case changed == 0:
		fmt.Fprintf(b, ". This %s adds %d new %s.\n", in.Forge.Change(), added, plural(added, "package", "packages"))
	case added == 0:
		fmt.Fprintf(b, ". This %s changes code in %d of them.\n", in.Forge.Change(), changed)
	default:
		fmt.Fprintf(b, ". This %s changes code in %d of them and adds %d new %s.\n", in.Forge.Change(), changed, added, plural(added, "package", "packages"))
	}
	if len(bl.Touched) > 0 {
		// One list item per package, not a table row: six columns wrapped
		// to a word a line at note width, and a count ("3 added") did not
		// say what was added.
		b.WriteString("\n")
		folded := foldedTouches(in)
		for _, t := range bl.Touched {
			writeTouched(b, in, t, folded[t.ID])
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

// writeTouched is one package of "Where this lands": its name and size,
// then one line per kind of change counting what changed, then who depends
// on it and what it talks to. A line with nothing to say is left out. The
// names are not repeated here: "What it did" names every one of them, with
// the package beside each, and the Details table links them; a second
// copy grouped by package doubled the length of the note.
// folded says "What it touches" already opens to the package's place in
// the repo, so those last lines would repeat it.
func writeTouched(b *strings.Builder, in NoteInput, t filter.TouchedPackage, folded bool) {
	name := "`" + mdEsc(t.Label) + "`"
	if t.Dir != "" && in.ProjectURL != "" {
		sha := in.BaseSHA
		if t.Status == filter.StatusAdded {
			sha = in.HeadSHA
		}
		name = fmt.Sprintf("[%s](%s)", name, in.Forge.Tree(in.ProjectURL, sha, t.Dir))
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
		fmt.Fprintf(b, "  - %d %s\n", c.n, c.what)
	}
	if folded {
		return
	}
	for _, line := range placeFacts(in, t) {
		fmt.Fprintf(b, "  - %s\n", line)
	}
}

// placeFacts say where a package sits among the rest of the repo: who
// imports it, which entry points reach it, which sinks it talks to.
func placeFacts(in NoteInput, t filter.TouchedPackage) []string {
	var out []string
	for _, r := range []struct {
		label string
		ids   []string
		entry bool
	}{{"Imported by", t.ImportedBy, false}, {"Reached from", t.Entrypoints, true}, {"Talks to", t.Sinks, false}} {
		if len(r.ids) == 0 {
			continue
		}
		var status map[string]string
		if r.entry && in.Baseline != nil {
			status = in.Baseline.EntryStatus
		}
		out = append(out, r.label+" "+shortList(r.ids, 4, status))
	}
	return out
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
		part := "`" + mdEsc(graph.ShortName(id)) + "`"
		if graph.IDPrefix(id) == "sink" {
			part = sinkPhrase(id)
		}
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

// detailsSummary is the label of the collapsed Details section: what is
// inside and how much, so a reviewer can tell whether to open it. A bare
// "Details" hid a medium-confidence rename and the extractor's warnings
// behind the same word as a table of links.
func detailsSummary(in NoteInput) string {
	om := in.Filter.Omitted
	var parts []string
	for _, c := range []struct {
		n         int
		one, many string
	}{
		{len(nodeRows(in)), "drawn item", "drawn items"}, // definitions, entry points and sinks alike
		{len(in.Delta.Nodes.Renamed), "rename", "renames"},
		{len(om.BodyOnly), "body-only change", "body-only changes"},
		{len(om.WideReach), "wide-reach change", "wide-reach changes"},
		{len(in.Delta.Suppressed), "suppressed package", "suppressed packages"},
		{len(in.Delta.Warnings), "warning", "warnings"},
	} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, plural(c.n, c.one, c.many)))
		}
	}
	if len(parts) == 0 {
		return "Details"
	}
	return "Details: " + strings.Join(parts, ", ")
}

// nodeRows are the details table's rows: every definition a call-path
// diagram draws, and those pruned from it, in ID order.
func nodeRows(in NoteInput) []filter.Node {
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
	return rows
}

func writeNodeTable(b *strings.Builder, in NoteInput, labels map[string]string) {
	rows := nodeRows(in)
	if len(rows) == 0 {
		return
	}
	// The node is its own link: a separate link column spent a third of
	// the width on file:line text and squeezed the names into a sliver.
	b.WriteString("| definition | status | package |\n|---|---|---|\n")
	for _, n := range rows {
		name := "`" + mdEsc(Label(n)) + "`"
		if u := permalinkURL(in, n.Status, n.File, n.Line); u != "" {
			name = "[" + name + "](" + u + ")"
		}
		b.WriteString("| " + name + " | " + tableStatus(n.Status) + " | " + tablePackage(n, labels) + " |\n")
	}
}

// tableStatus is a node's status in the words and swatches of the key.
// The table said "context" where the key and the call-path lists say
// "unchanged", so one grey box had two names.
func tableStatus(status string) string {
	switch status {
	case filter.StatusAdded:
		return "🟩 added"
	case filter.StatusRemoved:
		return "🟥 removed"
	case filter.StatusSignatureChanged:
		return "🟨 signature changed"
	case filter.StatusContext:
		return "⬜ unchanged"
	}
	return strings.ReplaceAll(status, "_", " ")
}

// tablePackage is the package cell, by the label the rest of the note
// uses for the package (a full import path in a narrow column was the one
// place the note still spelled it out). An entry point or a sink has no
// package, and an empty cell read as missing data; it says what the node
// is instead.
func tablePackage(n filter.Node, labels map[string]string) string {
	switch n.Kind {
	case filter.KindEntrypoint:
		return "_entry point_"
	case filter.KindSink:
		if p := strings.SplitN(n.ID, ":", 3); len(p) == 3 {
			return "_" + sinkWord(p[1], 1) + "_"
		}
		return "_sink_"
	}
	return mdEsc(pkgLabel(labels, n.Package))
}

// pkgLabel is the note's label for a package: the one the change list
// uses, else the last path segment.
func pkgLabel(labels map[string]string, pkg string) string {
	if l, ok := labels[pkg]; ok && l != "" {
		return l
	}
	return shortPkg(pkg)
}

// sinkWord is what a sink of a kind is called, in a reviewer's words.
func sinkWord(kind string, n int) string {
	one, many := strings.ReplaceAll(kind, "_", " "), strings.ReplaceAll(kind, "_", " ")+"s"
	switch kind {
	case "db":
		one, many = "table", "tables"
	case "http_client":
		one, many = "HTTP client", "HTTP clients"
	case "fs":
		one, many = "file store", "file stores"
	}
	if n == 1 {
		return one
	}
	return many
}

// sinkPhrase names a sink in prose: "`orders` table", "`http` (HTTP client)".
func sinkPhrase(id string) string {
	p := strings.SplitN(id, ":", 3)
	if len(p) != 3 {
		return "`" + mdEsc(graph.ShortName(id)) + "`"
	}
	if p[1] == "db" {
		return "`" + mdEsc(p[2]) + "` table"
	}
	return "`" + mdEsc(p[2]) + "` (" + sinkWord(p[1], 1) + ")"
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
	return in.Forge.Blob(in.ProjectURL, sha, file, line)
}

func writeLists(b *strings.Builder, in NoteInput, labels map[string]string) {
	d := in.Delta
	if len(d.Nodes.Renamed) > 0 {
		b.WriteString("\n**Renames**\n\n")
		for _, r := range d.Nodes.Renamed {
			why := renameEvidence[r.Via]
			if why == "" {
				why = "matched by " + r.Via
			}
			if r.Confidence == "medium" {
				// The summary list says "(probably)"; a "?" here was a
				// code the key does not explain.
				why = "probably: " + why
			}
			fmt.Fprintf(b, "- `%s` → `%s` (%s)\n", mdEsc(graph.ShortName(r.From)), mdEsc(graph.ShortName(r.To)), why)
		}
	}
	if len(in.Filter.Omitted.BodyOnly) > 0 {
		b.WriteString("\n**Body-only changes**\n\n")
		for _, id := range in.Filter.Omitted.BodyOnly {
			// Named as the change list names things: a short name and the
			// package's label, not the raw ID.
			fmt.Fprintf(b, "- `%s` in `%s`\n", mdEsc(graph.ShortName(id)), mdEsc(pkgLabel(labels, graph.SymPackage(id))))
		}
	}
	if len(in.Filter.Omitted.WideReach) > 0 {
		b.WriteString("\n**Wide-reach changes** (reached only through a call with many possible targets, such as an interface with many implementations; listed, not drawn)\n\n")
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
	// Only the counts that are not zero, as the top of the note does: a
	// line of zeros is read once and skipped ever after.
	var cut []string
	add := func(n int, s string) {
		if n > 0 {
			cut = append(cut, fmt.Sprintf(s, n))
		}
	}
	add(om.HiddenNodes, "%d hidden by the budget")
	add(len(om.Orphans), "%d on no drawn path")
	add(om.CollapsedSymbols, "%d collapsed into packages")
	add(om.ExcludedGenerated, "%d generated not drawn")
	add(om.ExcludedTest, "%d in tests not drawn")
	if n := om.ExcludedTestPackages; n > 0 {
		cut = append(cut, fmt.Sprintf("%d test %s left out of the wiring", n, plural(n, "package", "packages")))
	}
	if len(cut) > 0 {
		b.WriteString("\nDefinitions not shown: " + strings.Join(cut, ", ") + ".\n")
	}
	if len(d.Warnings) > 0 {
		b.WriteString("\n**Warnings**\n\n")
		for _, w := range d.Warnings {
			fmt.Fprintf(b, "- %s\n", mdEsc(w))
		}
	}
}

// renameEvidence says in a reviewer's words why section 8.1 matched a
// rename; the delta records it as a step name ("fingerprint").
var renameEvidence = map[string]string{
	"entrypoint":  "same handler",
	"package":     "moved with its package",
	"type":        "moved with its type",
	"fingerprint": "identical body",
	"similarity":  "similar body",
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

// remainderCaption says, under a diagram, how much the budget left out of
// it, in place of the edgeless "+N more" box the diagram used to draw: a
// box with no arrows was the one thing in the picture a reader could not
// place, and it reused a label the key gave another meaning.
func remainderCaption(dg *filter.Diagram) string {
	for _, n := range dg.Nodes {
		if n.ID == filter.HiddenMoreID {
			more := strings.TrimSuffix(strings.TrimPrefix(n.Label, "+"), " more")
			return fmt.Sprintf("%s more %s left out to fit; the Details table lists them.", more, plural(atoi(more), "definition was", "definitions were"))
		}
	}
	return ""
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// compactItem is a folded definition on one line: the item's text and a
// permalink to its line, when the graphs know where it is.
func compactItem(in NoteInput, text, id string) string {
	ix := in.Head
	status := ""
	if isRemoved(in.Delta, id) {
		ix, status = in.Base, filter.StatusRemoved
	}
	if ix == nil {
		return text
	}
	n := ix.Node(id)
	if n == nil || n.File == "" {
		return text
	}
	if u := permalinkURL(in, status, n.File, n.Line); u != "" {
		return fmt.Sprintf("%s · [%s:%d](%s)", text, mdEsc(n.File), n.Line, u)
	}
	return fmt.Sprintf("%s · %s:%d", text, mdEsc(n.File), n.Line)
}

func isRemoved(d *delta.Delta, id string) bool {
	if d == nil {
		return false
	}
	for _, r := range d.Nodes.Removed {
		if r == id {
			return true
		}
	}
	return false
}
