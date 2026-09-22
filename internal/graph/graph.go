// Package graph is the normalized graph schema (spec section 6): the contract
// between language extractors and everything downstream. Nothing in this
// package knows about Go, GitLab or Mermaid.
package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// SchemaVersion is the only schema version this build reads or writes.
const SchemaVersion = 1

// Node kinds.
const (
	KindPackage    = "package"
	KindSymbol     = "symbol"
	KindEntrypoint = "entrypoint"
	KindSink       = "sink"
	KindFile       = "file"
)

// Edge kinds.
const (
	EdgeCalls      = "calls"
	EdgeImports    = "imports"
	EdgeImplements = "implements"
	EdgeHandles    = "handles"
	EdgeWrites     = "writes"
	EdgeReads      = "reads"
)

// Load statuses.
const (
	LoadOK      = "ok"
	LoadPartial = "partial"
	LoadFailed  = "failed"
)

// Edge confidences.
const (
	ConfResolved = "resolved"
	ConfInferred = "inferred"
)

// SketchSize is the number of minhash values in Node.BodySketch.
const SketchSize = 64

var (
	SymbolKinds     = []string{"func", "method", "type", "interface", "var", "const"}
	EntryKinds      = []string{"http", "grpc", "worker", "cron", "cli"}
	SinkKinds       = []string{"db", "queue", "http_client", "cache", "fs"}
	SinkNameSources = []string{"table", "package"}
	NodeKinds       = []string{KindPackage, KindSymbol, KindEntrypoint, KindSink, KindFile}
	EdgeKinds       = []string{EdgeCalls, EdgeImports, EdgeImplements, EdgeHandles, EdgeWrites, EdgeReads}
	LoadStatuses    = []string{LoadOK, LoadPartial, LoadFailed}
	Confidences     = []string{ConfResolved, ConfInferred}
)

// Document is one graph: one (repo, sha) pair.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Repo          string    `json:"repo"`
	SHA           string    `json:"sha"`
	Extractor     Extractor `json:"extractor"`
	Packages      []Package `json:"packages"`
	Nodes         []Node    `json:"nodes"`
	Edges         []Edge    `json:"edges"`
	Warnings      []string  `json:"warnings"`
}

// Extractor identifies the backend that produced the document. Every field
// but BuilderMode is part of the cache key (section 14.1).
type Extractor struct {
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	Precision   string  `json:"precision"` // typed | inferred
	Toolchain   string  `json:"toolchain"`
	ConfigHash  string  `json:"config_hash"`
	BuilderMode *string `json:"builder_mode"` // null for a backend that has none
}

// Package carries the per-package load status the delta engine reads (8.4).
type Package struct {
	ID         string `json:"id"`
	LoadStatus string `json:"load_status"`
}

// Node is section 6.1.
type Node struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Package string    `json:"package"`
	File    string    `json:"file,omitempty"`
	Line    int       `json:"line,omitempty"`
	Attrs   NodeAttrs `json:"attrs"`

	Fingerprint     string   `json:"fingerprint,omitempty"`
	FingerprintRecv string   `json:"fingerprint_recv,omitempty"`
	BodyHash        string   `json:"body_hash,omitempty"`
	BodyTokens      int      `json:"body_tokens,omitempty"`
	BodySketch      []uint32 `json:"body_sketch,omitempty"`
}

// NodeAttrs is the attrs block of a node.
type NodeAttrs struct {
	SymbolKind     string `json:"symbol_kind,omitempty"`
	Exported       bool   `json:"exported"`
	Generated      bool   `json:"generated"`
	Test           bool   `json:"test"`
	Signature      string `json:"signature,omitempty"`
	EntryKind      string `json:"entry_kind,omitempty"`
	SinkKind       string `json:"sink_kind,omitempty"`
	SinkNameSource string `json:"sink_name_source,omitempty"`
}

// Edge is section 6.2.
type Edge struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	Kind       string    `json:"kind"`
	Confidence string    `json:"confidence"`
	Attrs      EdgeAttrs `json:"attrs"`
}

// EdgeAttrs is the attrs block of an edge. Targets is present exactly when
// Dynamic is true (6.3 invariant).
type EdgeAttrs struct {
	Dynamic bool `json:"dynamic"`
	Targets *int `json:"targets,omitempty"`
	Sites   int  `json:"sites,omitempty"`
}

// HasBody reports whether the node is a symbol kind that carries a body and
// therefore the fingerprint fields.
func (n *Node) HasBody() bool {
	return n.Kind == KindSymbol && (n.Attrs.SymbolKind == "func" || n.Attrs.SymbolKind == "method")
}

// EdgeKey is the identity of an edge: (from, to, kind).
type EdgeKey struct{ From, To, Kind string }

// Key returns the edge's identity.
func (e Edge) Key() EdgeKey { return EdgeKey{e.From, e.To, e.Kind} }

// String renders an edge key for messages.
func (k EdgeKey) String() string { return k.From + " -" + k.Kind + "-> " + k.To }

// ---- ID grammars (section 6.1) ----

// PkgID returns "pkg:<import path>".
func PkgID(importPath string) string { return "pkg:" + importPath }

// SymID returns "sym:<import path>.<qualified name>".
func SymID(importPath, qualified string) string { return "sym:" + importPath + "." + qualified }

// EntryID returns "entry:<kind>:<name>".
func EntryID(kind, name string) string { return "entry:" + kind + ":" + name }

// SinkID returns "sink:<kind>:<name>".
func SinkID(kind, name string) string { return "sink:" + kind + ":" + name }

// IDPrefix returns the grammar prefix ("pkg", "sym", "entry", "sink", "file")
// or "" when the ID matches none.
func IDPrefix(id string) string {
	i := strings.IndexByte(id, ':')
	if i <= 0 {
		return ""
	}
	switch p := id[:i]; p {
	case "pkg", "sym", "entry", "sink", "file":
		return p
	}
	return ""
}

// ShortName returns the display name for an ID when the node is not at hand:
// the qualified name for symbols, the last path segment for packages, the
// name for entry points and sinks.
func ShortName(id string) string {
	switch IDPrefix(id) {
	case "pkg":
		p := strings.TrimPrefix(id, "pkg:")
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			return p[i+1:]
		}
		return p
	case "sym":
		return SymQualified(id, "")
	case "entry", "sink":
		parts := strings.SplitN(id, ":", 3)
		if len(parts) == 3 {
			return parts[2]
		}
	}
	return id
}

// SymQualified returns the qualified name part of a sym ID. When pkg is
// known it is stripped exactly; otherwise the split is at the last '/' and
// the following '.', which is right for every ID the Go extractor emits.
func SymQualified(id, pkg string) string {
	s := strings.TrimPrefix(id, "sym:")
	if pkg != "" {
		return strings.TrimPrefix(s, pkg+".")
	}
	i := strings.LastIndexByte(s, '/')
	rest := s[i+1:]
	j := strings.IndexByte(rest, '.')
	if j < 0 {
		return rest
	}
	return rest[j+1:]
}

// SymPackage returns the import path part of a sym ID by the same split.
func SymPackage(id string) string {
	s := strings.TrimPrefix(id, "sym:")
	i := strings.LastIndexByte(s, '/')
	rest := s[i+1:]
	j := strings.IndexByte(rest, '.')
	if j < 0 {
		return s
	}
	return s[:i+1] + rest[:j]
}

// ---- canonical order and I/O ----

// Canonicalize sorts packages and nodes by ID and edges by (from, to, kind),
// and normalizes nil slices to empty ones so the encoding is stable.
func (d *Document) Canonicalize() {
	if d.Packages == nil {
		d.Packages = []Package{}
	}
	if d.Nodes == nil {
		d.Nodes = []Node{}
	}
	if d.Edges == nil {
		d.Edges = []Edge{}
	}
	if d.Warnings == nil {
		d.Warnings = []string{}
	}
	sort.Slice(d.Packages, func(i, j int) bool { return d.Packages[i].ID < d.Packages[j].ID })
	sort.Slice(d.Nodes, func(i, j int) bool { return d.Nodes[i].ID < d.Nodes[j].ID })
	sort.Slice(d.Edges, func(i, j int) bool { return edgeLess(d.Edges[i], d.Edges[j]) })
	sort.Strings(d.Warnings)
}

func edgeLess(a, b Edge) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Kind < b.Kind
}

// IsCanonical reports whether the document is already in canonical order.
func (d *Document) IsCanonical() bool {
	for i := 1; i < len(d.Packages); i++ {
		if d.Packages[i-1].ID >= d.Packages[i].ID {
			return false
		}
	}
	for i := 1; i < len(d.Nodes); i++ {
		if d.Nodes[i-1].ID >= d.Nodes[i].ID {
			return false
		}
	}
	for i := 1; i < len(d.Edges); i++ {
		if !edgeLess(d.Edges[i-1], d.Edges[i]) {
			return false
		}
	}
	return true
}

// Marshal encodes the document canonically, with a trailing newline.
func (d *Document) Marshal() ([]byte, error) {
	d.Canonicalize()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Write writes the canonical encoding to w.
func (d *Document) Write(w io.Writer) error {
	b, err := d.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// WriteFile writes the canonical encoding to path.
func (d *Document) WriteFile(path string) error {
	b, err := d.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Read decodes a document. It does not canonicalize; the invariant checker
// reports a non-canonical file.
func Read(r io.Reader) (*Document, error) {
	var d Document
	dec := json.NewDecoder(r)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("decode graph: %w", err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("graph schema_version %d, this build reads %d", d.SchemaVersion, SchemaVersion)
	}
	return &d, nil
}

// ReadFile decodes a document from path.
func ReadFile(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// ---- index ----

// Index is a document with lookup tables. Build one per document; the
// delta engine, filter and renderer all read through it.
type Index struct {
	Doc      *Document
	Nodes    map[string]*Node
	Packages map[string]*Package
	Out      map[string][]*Edge // by from
	In       map[string][]*Edge // by to
	EdgeSet  map[EdgeKey]*Edge
}

// NewIndex builds lookup tables. The document is canonicalized first.
func NewIndex(d *Document) *Index {
	d.Canonicalize()
	ix := &Index{
		Doc:      d,
		Nodes:    make(map[string]*Node, len(d.Nodes)),
		Packages: make(map[string]*Package, len(d.Packages)),
		Out:      make(map[string][]*Edge),
		In:       make(map[string][]*Edge),
		EdgeSet:  make(map[EdgeKey]*Edge, len(d.Edges)),
	}
	for i := range d.Nodes {
		ix.Nodes[d.Nodes[i].ID] = &d.Nodes[i]
	}
	for i := range d.Packages {
		ix.Packages[d.Packages[i].ID] = &d.Packages[i]
	}
	for i := range d.Edges {
		e := &d.Edges[i]
		ix.Out[e.From] = append(ix.Out[e.From], e)
		ix.In[e.To] = append(ix.In[e.To], e)
		ix.EdgeSet[e.Key()] = e
	}
	return ix
}

// Node returns the node or nil.
func (ix *Index) Node(id string) *Node { return ix.Nodes[id] }

// PackageOf returns the pkg: ID of a node's package, or "" for a node with
// no package (entry points and sinks).
func (ix *Index) PackageOf(id string) string {
	n := ix.Nodes[id]
	if n == nil || n.Package == "" {
		return ""
	}
	return PkgID(n.Package)
}

// LoadStatus returns the load status of the node's package, "ok" for nodes
// without a package.
func (ix *Index) LoadStatus(nodeID string) string {
	p := ix.PackageOf(nodeID)
	if p == "" {
		return LoadOK
	}
	if pk := ix.Packages[p]; pk != nil {
		return pk.LoadStatus
	}
	return LoadOK
}

// Callees returns the calls-edge targets of a node in ID order.
func (ix *Index) Callees(id string) []*Edge {
	var out []*Edge
	for _, e := range ix.Out[id] {
		if e.Kind == EdgeCalls {
			out = append(out, e)
		}
	}
	return out
}

// Callers returns the calls-edge sources of a node in ID order.
func (ix *Index) Callers(id string) []*Edge {
	var out []*Edge
	for _, e := range ix.In[id] {
		if e.Kind == EdgeCalls {
			out = append(out, e)
		}
	}
	return out
}
