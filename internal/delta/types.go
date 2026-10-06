// Package delta subtracts two graphs (spec section 8): nodes, edges,
// packages, hierarchical renames, entrypoint reachability and load-status
// suppression. This file is the delta document; engine.go computes it.
package delta

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/Ullauri/shapediff/internal/graph"
)

// SchemaVersion of the delta document.
const SchemaVersion = 1

// Outcome values: how the run that wrote the document ended. A reader of
// the artifact (the agent reviewer of spec section 2) needs this to tell
// "nothing structural" from "the job did not get as far as a delta".
const (
	OutcomeOK     = "ok"     // the delta was computed
	OutcomeFailed = "failed" // it was not; Reason says why, and the delta is empty
)

// Delta is one delta document.
type Delta struct {
	SchemaVersion int    `json:"schema_version"`
	Repo          string `json:"repo"`
	BaseSHA       string `json:"base_sha"`
	HeadSHA       string `json:"head_sha"`
	MRIID         int    `json:"mr_iid,omitempty"`
	// Outcome and Reason are set by the CI job (section 14.1): "ok" when
	// the delta below was computed, "failed" with the reason when it was
	// not. A document written by the delta command alone has neither.
	Outcome     string       `json:"outcome,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	Nodes       NodeDelta    `json:"nodes"`
	Edges       EdgeDelta    `json:"edges"`
	Entrypoints []Entrypoint `json:"entrypoints"`
	Packages    PackageDelta `json:"packages"`
	Suppressed  []Suppressed `json:"suppressed"`
	Stats       Stats        `json:"stats"`
	Warnings    []string     `json:"warnings"`
}

// NodeDelta lists node changes by class. IDs in Added/Removed/BodyChanged
// are head IDs for added and body-changed nodes, base IDs for removed ones.
type NodeDelta struct {
	Added            []string          `json:"added"`
	Removed          []string          `json:"removed"`
	SignatureChanged []SignatureChange `json:"signature_changed"`
	BodyChanged      []string          `json:"body_changed"`
	Renamed          []Rename          `json:"renamed"`
}

// SignatureChange is a node whose signature attr differs.
type SignatureChange struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Rename is a base node matched to a head node by section 8.1.
type Rename struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Confidence string `json:"confidence"` // high | medium
	Via        string `json:"via"`        // entrypoint | package | type | fingerprint | similarity
}

// EdgeDelta lists edge changes. Renamed nodes are matched through the
// rename first, so a pure rename yields no edge changes.
type EdgeDelta struct {
	Added   []graph.Edge `json:"added"`
	Removed []graph.Edge `json:"removed"`
}

// Entrypoint status values.
const (
	EPAdded        = "added"
	EPRemoved      = "removed"
	EPReachChanged = "reach_changed"
	EPUnchanged    = "unchanged"
)

// Entrypoint is one entry point's reachability delta (section 8.2).
type Entrypoint struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	ReachAdded       []string   `json:"reach_added"`
	ReachRemoved     []string   `json:"reach_removed"`
	WideReachAdded   []string   `json:"wide_reach_added"`
	WideReachRemoved []string   `json:"wide_reach_removed"`
	ChangedPaths     [][]string `json:"changed_paths"`
}

// PackageDelta is the module wiring delta.
type PackageDelta struct {
	Added          []string     `json:"added"`
	Removed        []string     `json:"removed"`
	ImportsAdded   []ImportEdge `json:"imports_added"`
	ImportsRemoved []ImportEdge `json:"imports_removed"`
}

// ImportEdge is one package import.
type ImportEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Suppressed records a package whose changes were hidden (section 8.4).
type Suppressed struct {
	Package         string `json:"package"`
	Base            string `json:"base"`
	Head            string `json:"head"`
	HiddenRemovals  int    `json:"hidden_removals"`
	HiddenAdditions int    `json:"hidden_additions"`
}

// Stats is the headline's numbers. Structural is the one bit CI reads.
type Stats struct {
	EntrypointsAdded        int  `json:"entrypoints_added"`
	EntrypointsRemoved      int  `json:"entrypoints_removed"`
	EntrypointsReachChanged int  `json:"entrypoints_reach_changed"`
	SymbolsAdded            int  `json:"symbols_added"`
	SymbolsRemoved          int  `json:"symbols_removed"`
	SignaturesChanged       int  `json:"signatures_changed"`
	SignatureCallers        int  `json:"signature_callers"`
	BodyChanged             int  `json:"body_changed"`
	Renamed                 int  `json:"renamed"`
	PackagesAdded           int  `json:"packages_added"`
	PackagesRemoved         int  `json:"packages_removed"`
	ImportsChanged          int  `json:"imports_changed"`
	EdgesAdded              int  `json:"edges_added"`
	EdgesRemoved            int  `json:"edges_removed"`
	Suppressed              int  `json:"suppressed"`
	Structural              bool `json:"structural"`
}

// Canonicalize sorts every list so the encoding is stable.
func (d *Delta) Canonicalize() {
	nz := func(s *[]string) {
		if *s == nil {
			*s = []string{}
		}
		sort.Strings(*s)
	}
	nz(&d.Nodes.Added)
	nz(&d.Nodes.Removed)
	nz(&d.Nodes.BodyChanged)
	nz(&d.Packages.Added)
	nz(&d.Packages.Removed)
	nz(&d.Warnings)
	if d.Nodes.SignatureChanged == nil {
		d.Nodes.SignatureChanged = []SignatureChange{}
	}
	sort.Slice(d.Nodes.SignatureChanged, func(i, j int) bool { return d.Nodes.SignatureChanged[i].ID < d.Nodes.SignatureChanged[j].ID })
	if d.Nodes.Renamed == nil {
		d.Nodes.Renamed = []Rename{}
	}
	sort.Slice(d.Nodes.Renamed, func(i, j int) bool {
		a, b := d.Nodes.Renamed[i], d.Nodes.Renamed[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	sortEdges := func(es *[]graph.Edge) {
		if *es == nil {
			*es = []graph.Edge{}
		}
		sort.Slice(*es, func(i, j int) bool {
			a, b := (*es)[i], (*es)[j]
			if a.From != b.From {
				return a.From < b.From
			}
			if a.To != b.To {
				return a.To < b.To
			}
			return a.Kind < b.Kind
		})
	}
	sortEdges(&d.Edges.Added)
	sortEdges(&d.Edges.Removed)
	sortImports := func(es *[]ImportEdge) {
		if *es == nil {
			*es = []ImportEdge{}
		}
		sort.Slice(*es, func(i, j int) bool {
			a, b := (*es)[i], (*es)[j]
			if a.From != b.From {
				return a.From < b.From
			}
			return a.To < b.To
		})
	}
	sortImports(&d.Packages.ImportsAdded)
	sortImports(&d.Packages.ImportsRemoved)
	if d.Entrypoints == nil {
		d.Entrypoints = []Entrypoint{}
	}
	for i := range d.Entrypoints {
		e := &d.Entrypoints[i]
		nz(&e.ReachAdded)
		nz(&e.ReachRemoved)
		nz(&e.WideReachAdded)
		nz(&e.WideReachRemoved)
		if e.ChangedPaths == nil {
			e.ChangedPaths = [][]string{}
		}
	}
	sort.Slice(d.Entrypoints, func(i, j int) bool { return d.Entrypoints[i].ID < d.Entrypoints[j].ID })
	if d.Suppressed == nil {
		d.Suppressed = []Suppressed{}
	}
	sort.Slice(d.Suppressed, func(i, j int) bool { return d.Suppressed[i].Package < d.Suppressed[j].Package })
}

// Marshal encodes canonically with a trailing newline.
func (d *Delta) Marshal() ([]byte, error) {
	d.Canonicalize()
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Write writes the canonical encoding.
func (d *Delta) Write(w io.Writer) error {
	b, err := d.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// WriteFile writes the canonical encoding to path.
func (d *Delta) WriteFile(path string) error {
	b, err := d.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ReadFile decodes a delta document.
func ReadFile(path string) (*Delta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Delta
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%s: delta schema_version %d, want %d", path, d.SchemaVersion, SchemaVersion)
	}
	d.Canonicalize()
	return &d, nil
}

// SignatureChangedIDs returns the IDs of signature-changed nodes.
func (d *Delta) SignatureChangedIDs() []string {
	out := make([]string, 0, len(d.Nodes.SignatureChanged))
	for _, s := range d.Nodes.SignatureChanged {
		out = append(out, s.ID)
	}
	return out
}

// RenameMap returns base ID to head ID for every rename.
func (d *Delta) RenameMap() map[string]string {
	m := make(map[string]string, len(d.Nodes.Renamed))
	for _, r := range d.Nodes.Renamed {
		m[r.From] = r.To
	}
	return m
}

// Touched returns the set of pkg: IDs touched by the MR (spec section 9's
// definition), given lookups for the head and base graphs.
func (d *Delta) Touched(head, base *graph.Index) map[string]bool {
	t := map[string]bool{}
	mark := func(ix *graph.Index, id string) {
		if p := ix.PackageOf(id); p != "" {
			t[p] = true
		}
	}
	for _, id := range d.Nodes.Added {
		mark(head, id)
	}
	for _, id := range d.Nodes.BodyChanged {
		mark(head, id)
	}
	for _, s := range d.Nodes.SignatureChanged {
		mark(head, s.ID)
	}
	for _, id := range d.Nodes.Removed {
		mark(base, id)
	}
	for _, r := range d.Nodes.Renamed {
		mark(base, r.From)
		mark(head, r.To)
	}
	return t
}

// Read decodes a delta document from a reader.
func Read(r io.Reader) (*Delta, error) {
	var d Delta
	if err := json.NewDecoder(r).Decode(&d); err != nil {
		return nil, fmt.Errorf("decode delta: %w", err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("delta schema_version %d, want %d", d.SchemaVersion, SchemaVersion)
	}
	d.Canonicalize()
	return &d, nil
}
