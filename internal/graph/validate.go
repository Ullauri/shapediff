package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Validate checks the section 6.3 document invariants and returns every
// violation found, in a stable order. An empty result means the document
// conforms. It never mutates the document.
func Validate(d *Document) []string {
	var probs []string
	add := func(format string, args ...any) { probs = append(probs, fmt.Sprintf(format, args...)) }

	if d.SchemaVersion != SchemaVersion {
		add("schema_version is %d, want %d", d.SchemaVersion, SchemaVersion)
	}
	if d.Repo == "" {
		add("repo is empty")
	}
	if d.SHA == "" {
		add("sha is empty")
	}
	ex := d.Extractor
	for name, v := range map[string]string{"name": ex.Name, "version": ex.Version, "precision": ex.Precision, "toolchain": ex.Toolchain, "config_hash": ex.ConfigHash} {
		if v == "" {
			add("extractor.%s is empty", name)
		}
	}
	if ex.Precision != "" && ex.Precision != "typed" && ex.Precision != "inferred" {
		add("extractor.precision %q not in [typed inferred]", ex.Precision)
	}
	if !d.IsCanonical() {
		add("document is not in canonical order (packages and nodes by id, edges by from,to,kind, no duplicates)")
	}
	if len(d.Packages) == 0 {
		add("document has no packages: nothing was extracted")
	}

	// packages
	pkgs := make(map[string]string, len(d.Packages))
	for _, p := range d.Packages {
		if !strings.HasPrefix(p.ID, "pkg:") {
			add("package %q: id must start with pkg:", p.ID)
		}
		if !in(LoadStatuses, p.LoadStatus) {
			add("package %s: load_status %q not in %v", p.ID, p.LoadStatus, LoadStatuses)
		}
		if _, dup := pkgs[p.ID]; dup {
			add("package %s listed twice", p.ID)
		}
		pkgs[p.ID] = p.LoadStatus
	}

	// nodes
	nodes := make(map[string]*Node, len(d.Nodes))
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if _, dup := nodes[n.ID]; dup {
			add("node %s listed twice", n.ID)
			continue
		}
		nodes[n.ID] = n
		if !in(NodeKinds, n.Kind) {
			add("node %s: kind %q not in %v", n.ID, n.Kind, NodeKinds)
		}
		if n.Name == "" {
			add("node %s: name is empty", n.ID)
		}
		pfx := IDPrefix(n.ID)
		switch n.Kind {
		case KindPackage:
			if pfx != "pkg" {
				add("node %s: kind package but id is not pkg:", n.ID)
			} else if n.Package == "" || n.ID != PkgID(n.Package) {
				add("node %s: package field %q does not match id", n.ID, n.Package)
			}
		case KindSymbol:
			if pfx != "sym" {
				add("node %s: kind symbol but id is not sym:", n.ID)
			} else {
				checkSymbolNode(n, add)
			}
		case KindEntrypoint:
			if pfx != "entry" {
				add("node %s: kind entrypoint but id is not entry:", n.ID)
			}
			if !in(EntryKinds, n.Attrs.EntryKind) {
				add("node %s: entry_kind %q not in %v", n.ID, n.Attrs.EntryKind, EntryKinds)
			} else if !strings.HasPrefix(n.ID, "entry:"+n.Attrs.EntryKind+":") {
				add("node %s: id kind does not match entry_kind %q", n.ID, n.Attrs.EntryKind)
			}
		case KindSink:
			if pfx != "sink" {
				add("node %s: kind sink but id is not sink:", n.ID)
			}
			if !in(SinkKinds, n.Attrs.SinkKind) {
				add("node %s: sink_kind %q not in %v", n.ID, n.Attrs.SinkKind, SinkKinds)
			} else if !strings.HasPrefix(n.ID, "sink:"+n.Attrs.SinkKind+":") {
				add("node %s: id kind does not match sink_kind %q", n.ID, n.Attrs.SinkKind)
			}
			if !in(SinkNameSources, n.Attrs.SinkNameSource) {
				add("node %s: sink_name_source %q not in %v", n.ID, n.Attrs.SinkNameSource, SinkNameSources)
			}
		case KindFile:
			if pfx != "file" {
				add("node %s: kind file but id is not file:", n.ID)
			}
		}
		if n.Package != "" {
			st, ok := pkgs[PkgID(n.Package)]
			if !ok {
				add("node %s: package %q not in packages", n.ID, n.Package)
			} else if st == LoadFailed {
				add("node %s: under package %q with load_status failed", n.ID, n.Package)
			}
		} else if n.Kind == KindSymbol || n.Kind == KindPackage {
			add("node %s: package is empty", n.ID)
		}
		if n.Kind != KindSymbol {
			if n.Attrs.SymbolKind != "" {
				add("node %s: symbol_kind set on a %s node", n.ID, n.Kind)
			}
			if n.Fingerprint != "" || n.BodyHash != "" || n.BodyTokens != 0 || len(n.BodySketch) != 0 || n.FingerprintRecv != "" {
				add("node %s: body fields set on a %s node", n.ID, n.Kind)
			}
		}
	}

	// edges
	seen := make(map[EdgeKey]bool, len(d.Edges))
	handles := map[string]int{}
	sunk := map[string]bool{}
	for _, e := range d.Edges {
		k := e.Key()
		if seen[k] {
			add("edge %s listed twice", k)
		}
		seen[k] = true
		if !in(EdgeKinds, e.Kind) {
			add("edge %s: kind not in %v", k, EdgeKinds)
		}
		if !in(Confidences, e.Confidence) {
			add("edge %s: confidence %q not in %v", k, e.Confidence, Confidences)
		}
		from, to := nodes[e.From], nodes[e.To]
		if from == nil {
			add("edge %s: from names no node", k)
		}
		if to == nil {
			add("edge %s: to names no node", k)
		}
		if e.Attrs.Dynamic && e.Attrs.Targets == nil {
			add("edge %s: dynamic without targets", k)
		}
		if !e.Attrs.Dynamic && e.Attrs.Targets != nil {
			add("edge %s: targets on a non-dynamic edge", k)
		}
		if e.Attrs.Targets != nil && *e.Attrs.Targets < 0 {
			add("edge %s: targets negative", k)
		}
		switch e.Kind {
		case EdgeCalls:
			if e.Attrs.Sites < 1 {
				add("edge %s: calls edge with sites < 1", k)
			}
		case EdgeHandles:
			if from != nil && from.Kind != KindEntrypoint {
				add("edge %s: handles from a non-entrypoint", k)
			}
			handles[e.From]++
		case EdgeWrites, EdgeReads:
			if to != nil && to.Kind != KindSink {
				add("edge %s: %s into a non-sink", k, e.Kind)
			}
			sunk[e.To] = true
		case EdgeImports:
			if (from != nil && from.Kind != KindPackage) || (to != nil && to.Kind != KindPackage) {
				add("edge %s: imports between non-package nodes", k)
			}
		}
	}
	for _, n := range d.Nodes {
		switch n.Kind {
		case KindEntrypoint:
			if handles[n.ID] != 1 {
				add("entrypoint %s: %d handles edges, want exactly 1", n.ID, handles[n.ID])
			}
		case KindSink:
			if !sunk[n.ID] {
				add("sink %s: no writes or reads edge targets it", n.ID)
			}
		}
	}
	sort.Strings(probs)
	return probs
}

func checkSymbolNode(n *Node, add func(string, ...any)) {
	if n.Package == "" {
		return // reported by caller
	}
	prefix := "sym:" + n.Package + "."
	if !strings.HasPrefix(n.ID, prefix) {
		add("node %s: id does not start with sym:<package>.", n.ID)
		return
	}
	qual := strings.TrimPrefix(n.ID, prefix)
	if qual == "" {
		add("node %s: empty qualified name", n.ID)
	}
	isInit := strings.HasPrefix(qual, "init#")
	if !isInit && (strings.ContainsAny(qual, "/\\") || strings.HasSuffix(qual, ".go")) {
		add("node %s: qualified name contains a path", n.ID)
	}
	if isInit {
		parts := strings.Split(qual, "#")
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			add("node %s: init id must be init#<file>#<ordinal>", n.ID)
		}
	}
	if !in(SymbolKinds, n.Attrs.SymbolKind) {
		add("node %s: symbol_kind %q not in %v", n.ID, n.Attrs.SymbolKind, SymbolKinds)
	}
	hasRecv := n.FingerprintRecv != ""
	isMethod := n.Attrs.SymbolKind == "method"
	if n.HasBody() {
		if n.Fingerprint == "" || n.BodyHash == "" {
			add("node %s: func/method without fingerprint and body_hash", n.ID)
		}
		if len(n.BodySketch) != SketchSize {
			add("node %s: body_sketch has %d values, want %d", n.ID, len(n.BodySketch), SketchSize)
		}
		if n.BodyTokens < 0 {
			add("node %s: body_tokens negative", n.ID)
		}
		if isMethod && !hasRecv {
			add("node %s: method without fingerprint_recv", n.ID)
		}
	} else if n.Fingerprint != "" || n.BodyHash != "" || len(n.BodySketch) != 0 {
		add("node %s: body fields on a %s", n.ID, n.Attrs.SymbolKind)
	}
	if hasRecv && !isMethod {
		add("node %s: fingerprint_recv on a non-method", n.ID)
	}
}

func in(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
