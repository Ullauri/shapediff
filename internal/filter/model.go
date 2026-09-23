// Package filter is spec section 9: the per-MR legibility algorithm. It
// turns a delta plus the two graphs into a small number of abstract diagrams
// under a budget. It knows nothing about Mermaid; the renderer supplies a
// measure function so the character budget can be applied here.
package filter

import "sort"

// Node kinds in a diagram.
const (
	KindPackage    = "package"
	KindSymbol     = "symbol"
	KindEntrypoint = "entrypoint"
	KindSink       = "sink"
	KindMore       = "more" // "N symbols" group or "+N more" remainder
)

// Node statuses.
const (
	StatusAdded            = "added"
	StatusRemoved          = "removed"
	StatusSignatureChanged = "signature_changed"
	StatusContext          = "context"
	StatusQuery            = "query"
	// StatusTouched marks a package the MR changed inside, on the baseline
	// map; the map draws the shape and this is the "you are here".
	StatusTouched = "touched"
)

// Edge statuses.
const (
	EdgeAdded     = "added"
	EdgeRemoved   = "removed"
	EdgeUnchanged = "unchanged"
)

// Diagram is one drawable graph.
type Diagram struct {
	Title string
	Nodes []Node // sorted by ID
	Edges []Edge // sorted by (From, To, Kind)
	// Subgraphs lists pkg: IDs drawn as package boxes: packages with symbol
	// nodes drawn and no collapsed pkg: node in this diagram.
	Subgraphs []string
}

// Node is one drawn node.
type Node struct {
	ID      string
	Label   string
	Kind    string
	Status  string
	Package string // import path, "" for entry points, sinks and the remainder node
	// Group boxes the node under a labelled subgraph that is not a package:
	// the baseline map groups packages by directory. Empty for every other
	// diagram.
	Group          string
	Badges         []string
	BodyChanged    bool
	CollapsedCount int
	File           string
	Line           int
	priority       int
}

// Edge is one drawn edge.
type Edge struct {
	From, To string
	Kind     string
	Dynamic  bool
	Inferred bool
	Status   string
	Targets  int
	Sites    int
}

// Budget is section 9's S, B, E and section 7.4's k.
type Budget struct {
	Chars, Nodes, Edges, NarrowTargets int
}

// Options controls Apply.
type Options struct {
	Budget Budget
	// Focus lists node IDs to treat as the subject (status query, priority
	// of a signature change). Used by the query command; empty for an MR.
	Focus []string
	// Measure returns the rendered source length of a diagram, or nil to
	// ignore the character budget.
	Measure func(*Diagram) int
}

// Result is what the renderer draws.
type Result struct {
	// CallPaths holds the call-path diagrams. The first is the primary; the
	// rest go in a collapsed section (step 4e).
	CallPaths []Diagram
	// Dataflow is step 5, nil when no entrypoint changed.
	Dataflow *Diagram
	// Wiring is step 6, nil when packages and imports are unchanged.
	Wiring *Diagram
	// Fallback is set when the terminal rule could not fit a diagram under
	// the character budget at B = 8; the caller renders SVG instead.
	Fallback bool
	Omitted  Omitted
}

// Omitted is what the diagrams left out, for the note's details section.
type Omitted struct {
	// HiddenNodes is the count folded into "+N more" nodes across diagrams.
	HiddenNodes int
	// CollapsedSymbols is the count of symbol nodes folded into package or
	// "N symbols" nodes.
	CollapsedSymbols int
	// ExcludedGenerated and ExcludedTest count focus candidates dropped by
	// step 1.
	ExcludedGenerated int
	ExcludedTest      int
	// BodyOnly lists body-changed nodes that were not drawn.
	BodyOnly []string
	// WideReach lists entrypoints whose wide reach changed.
	WideReach []WideReach
}

// WideReach is one entrypoint's wide-reach change.
type WideReach struct {
	Entrypoint string
	Added      []string
	Removed    []string
}

// NodeByID returns the node or nil.
func (dg *Diagram) NodeByID(id string) *Node {
	i := sort.Search(len(dg.Nodes), func(i int) bool { return dg.Nodes[i].ID >= id })
	if i < len(dg.Nodes) && dg.Nodes[i].ID == id {
		return &dg.Nodes[i]
	}
	return nil
}
