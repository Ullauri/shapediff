// Package conform is the section 6.3 conformance suite: document
// invariants on any graph, and fixture scenarios driven through the
// plug-in contract for any extractor.
package conform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Manifest is conformance/<language>/manifest.yml.
type Manifest struct {
	Language  string                      `yaml:"language"`
	Extractor string                      `yaml:"extractor"`
	Scenarios map[string]ScenarioManifest `yaml:"scenarios"`
}

// ScenarioManifest says whether a scenario is present or n/a with a reason.
type ScenarioManifest struct {
	Status string `yaml:"status"` // present | n/a
	Reason string `yaml:"reason"`
}

// Expect is <scenario>/expect.yml. Each assertion is a one-key map.
type Expect struct {
	Scenario string           `yaml:"scenario"`
	TierA    []map[string]any `yaml:"tier_a"`
	TierB    []map[string]any `yaml:"tier_b"`
}

// Extractor produces a graph for a checkout under the plug-in contract.
type Extractor interface {
	Extract(ctx context.Context, repoDir, configPath string) (*graph.Document, error)
	Name() string
}

// Exe runs an external extractor: <exe> --repo <dir> --config <path>,
// JSON on stdout, exit 0. Path may carry leading arguments ("shapediff
// extract"), split on whitespace.
type Exe struct{ Path string }

// Name returns the executable path.
func (e Exe) Name() string { return e.Path }

// Extract runs the executable and decodes its stdout.
func (e Exe) Extract(ctx context.Context, repoDir, configPath string) (*graph.Document, error) {
	argv := strings.Fields(e.Path)
	if len(argv) == 0 {
		return nil, fmt.Errorf("extractor: empty command")
	}
	argv = append(argv, "--repo", repoDir, "--config", configPath)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("extractor %s: %s", e.Path, msg)
	}
	doc, err := graph.Read(&stdout)
	if err != nil {
		return nil, fmt.Errorf("extractor %s: %w", e.Path, err)
	}
	return doc, nil
}

// Func adapts an in-process extractor.
type Func struct {
	Label string
	Fn    func(ctx context.Context, repoDir, configPath string) (*graph.Document, error)
}

// Name returns the label.
func (f Func) Name() string { return f.Label }

// Extract calls the function.
func (f Func) Extract(ctx context.Context, repoDir, configPath string) (*graph.Document, error) {
	return f.Fn(ctx, repoDir, configPath)
}

// ScenarioResult is one scenario's outcome.
type ScenarioResult struct {
	Name     string
	Status   string // pass | fail | n/a | error
	Reason   string // n/a reason or error text
	Failures []string
}

// Report is the whole run.
type Report struct {
	Language  string
	Extractor string
	Results   []ScenarioResult
}

// OK reports whether every scenario passed or was explained.
func (r Report) OK() bool {
	for _, s := range r.Results {
		if s.Status == "fail" || s.Status == "error" {
			return false
		}
	}
	return true
}

// Write prints the report in one line per scenario.
func (r Report) Write(w io.Writer) {
	fmt.Fprintf(w, "conformance: language=%s extractor=%s\n", r.Language, r.Extractor)
	for _, s := range r.Results {
		switch s.Status {
		case "pass":
			fmt.Fprintf(w, "  PASS  %s\n", s.Name)
		case "n/a":
			fmt.Fprintf(w, "  N/A   %s: %s\n", s.Name, s.Reason)
		case "error":
			fmt.Fprintf(w, "  ERROR %s: %s\n", s.Name, s.Reason)
		default:
			fmt.Fprintf(w, "  FAIL  %s\n", s.Name)
			for _, f := range s.Failures {
				fmt.Fprintf(w, "        - %s\n", f)
			}
		}
	}
}

// Runner drives an extractor over a fixture directory.
type Runner struct {
	Extractor     Extractor
	FixturesDir   string
	NarrowTargets int
	Only          []string // scenario names; empty means all
}

// Run executes every scenario in the manifest.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	mb, err := os.ReadFile(filepath.Join(r.FixturesDir, "manifest.yml"))
	if err != nil {
		return Report{}, fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(mb, &m); err != nil {
		return Report{}, fmt.Errorf("manifest: %w", err)
	}
	rep := Report{Language: m.Language, Extractor: r.Extractor.Name()}
	names := make([]string, 0, len(m.Scenarios))
	for n := range m.Scenarios {
		names = append(names, n)
	}
	sort.Strings(names)
	only := map[string]bool{}
	for _, o := range r.Only {
		only[o] = true
	}
	for _, name := range names {
		if len(only) > 0 && !only[name] {
			continue
		}
		sm := m.Scenarios[name]
		switch sm.Status {
		case "n/a":
			if strings.TrimSpace(sm.Reason) == "" {
				rep.Results = append(rep.Results, ScenarioResult{Name: name, Status: "error", Reason: "declared n/a without a reason"})
			} else {
				rep.Results = append(rep.Results, ScenarioResult{Name: name, Status: "n/a", Reason: sm.Reason})
			}
			continue
		case "present", "":
		default:
			rep.Results = append(rep.Results, ScenarioResult{Name: name, Status: "error", Reason: "status must be present or n/a"})
			continue
		}
		rep.Results = append(rep.Results, r.scenario(ctx, name))
	}
	return rep, nil
}

func (r *Runner) scenario(ctx context.Context, name string) ScenarioResult {
	dir := filepath.Join(r.FixturesDir, name)
	res := ScenarioResult{Name: name, Status: "pass"}
	eb, err := os.ReadFile(filepath.Join(dir, "expect.yml"))
	if err != nil {
		return ScenarioResult{Name: name, Status: "error", Reason: err.Error()}
	}
	var ex Expect
	if err := yaml.Unmarshal(eb, &ex); err != nil {
		return ScenarioResult{Name: name, Status: "error", Reason: "expect.yml: " + err.Error()}
	}
	var docs [2]*graph.Document
	for i, side := range []string{"base", "head"} {
		sd := filepath.Join(dir, side)
		if _, err := os.Stat(sd); err != nil {
			return ScenarioResult{Name: name, Status: "error", Reason: fmt.Sprintf("%s missing", side)}
		}
		doc, err := r.Extractor.Extract(ctx, sd, filepath.Join(sd, ".shapediff.yml"))
		if err != nil {
			return ScenarioResult{Name: name, Status: "error", Reason: fmt.Sprintf("%s: %v", side, err)}
		}
		for _, p := range graph.Validate(doc) {
			res.Failures = append(res.Failures, fmt.Sprintf("%s invariant: %s", side, p))
		}
		// determinism: a second run must be byte-identical
		again, err := r.Extractor.Extract(ctx, sd, filepath.Join(sd, ".shapediff.yml"))
		if err == nil {
			a, _ := doc.Marshal()
			b, _ := again.Marshal()
			if !bytes.Equal(a, b) {
				res.Failures = append(res.Failures, side+": two extractions differ")
			}
		}
		docs[i] = doc
	}
	base, head := graph.NewIndex(docs[0]), graph.NewIndex(docs[1])
	for _, a := range ex.TierA {
		if msg := checkA(a, base, head); msg != "" {
			res.Failures = append(res.Failures, "tier A: "+msg)
		}
	}
	if len(ex.TierB) > 0 {
		d := delta.Compute(docs[0], docs[1], delta.Options{NarrowTargets: r.NarrowTargets})
		for _, b := range ex.TierB {
			if msg := checkB(b, d, base, head); msg != "" {
				res.Failures = append(res.Failures, "tier B: "+msg)
			}
		}
	}
	if len(res.Failures) > 0 {
		res.Status = "fail"
	}
	return res
}

// ---- assertions ----

func one(a map[string]any) (string, any) {
	for k, v := range a {
		return k, v
	}
	return "", nil
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func side(base, head *graph.Index, in string) []*graph.Index {
	switch in {
	case "base":
		return []*graph.Index{base}
	case "both":
		return []*graph.Index{base, head}
	default:
		return []*graph.Index{head}
	}
}

func field(n *graph.Node, f string) any {
	switch f {
	case "file":
		return n.File
	case "line":
		return n.Line
	case "generated":
		return n.Attrs.Generated
	case "test":
		return n.Attrs.Test
	case "exported":
		return n.Attrs.Exported
	case "symbol_kind":
		return n.Attrs.SymbolKind
	case "signature":
		return n.Attrs.Signature
	case "entry_kind":
		return n.Attrs.EntryKind
	case "sink_kind":
		return n.Attrs.SinkKind
	case "sink_name_source":
		return n.Attrs.SinkNameSource
	case "fingerprint":
		return n.Fingerprint
	case "fingerprint_recv":
		return n.FingerprintRecv
	case "body_hash":
		return n.BodyHash
	case "body_tokens":
		return n.BodyTokens
	case "kind":
		return n.Kind
	case "name":
		return n.Name
	case "package":
		return n.Package
	}
	return nil
}

func checkA(a map[string]any, base, head *graph.Index) string {
	key, v := one(a)
	m := asMap(v)
	switch key {
	case "node_count_equal":
		if len(base.Doc.Nodes) != len(head.Doc.Nodes) {
			return fmt.Sprintf("node counts differ: base %d head %d", len(base.Doc.Nodes), len(head.Doc.Nodes))
		}
	case "present":
		for _, ix := range side(base, head, str(m, "in")) {
			if ix.Node(str(m, "id")) == nil {
				return fmt.Sprintf("%s missing in %s", str(m, "id"), name(ix, base))
			}
		}
	case "absent":
		for _, ix := range side(base, head, str(m, "in")) {
			if ix.Node(str(m, "id")) != nil {
				return fmt.Sprintf("%s present in %s", str(m, "id"), name(ix, base))
			}
		}
	case "same_ids_except":
		except := map[string]bool{}
		if list, ok := v.([]any); ok {
			for _, x := range list {
				except[fmt.Sprint(x)] = true
			}
		}
		for id, n := range base.Nodes {
			if n.Kind == graph.KindSymbol && !except[id] && head.Nodes[id] == nil {
				return "base symbol missing from head: " + id
			}
		}
		for id, n := range head.Nodes {
			if n.Kind == graph.KindSymbol && !except[id] && base.Nodes[id] == nil {
				return "head symbol not in base: " + id
			}
		}
	case "attr_equal":
		for _, ix := range side(base, head, str(m, "in")) {
			n := ix.Node(str(m, "id"))
			if n == nil {
				return str(m, "id") + " missing"
			}
			if got := fmt.Sprint(field(n, str(m, "field"))); got != str(m, "value") {
				return fmt.Sprintf("%s.%s = %q, want %q", n.ID, str(m, "field"), got, str(m, "value"))
			}
		}
	case "field_differs", "field_equal":
		id := str(m, "id")
		b, h := base.Node(id), head.Node(id)
		if b == nil || h == nil {
			return id + " must be in both graphs"
		}
		bv, hv := fmt.Sprint(field(b, str(m, "field"))), fmt.Sprint(field(h, str(m, "field")))
		if key == "field_differs" && bv == hv {
			return fmt.Sprintf("%s.%s should differ, both %q", id, str(m, "field"), bv)
		}
		if key == "field_equal" && bv != hv {
			return fmt.Sprintf("%s.%s should be equal: %q vs %q", id, str(m, "field"), bv, hv)
		}
	case "field_equal_across":
		b, h := base.Node(str(m, "base")), head.Node(str(m, "head"))
		if b == nil || h == nil {
			return "node missing for field_equal_across"
		}
		bv, hv := fmt.Sprint(field(b, str(m, "field"))), fmt.Sprint(field(h, str(m, "field")))
		if bv != hv || bv == "" {
			return fmt.Sprintf("%s differs across %s and %s", str(m, "field"), b.ID, h.ID)
		}
	case "edge":
		for _, ix := range side(base, head, str(m, "in")) {
			e := ix.EdgeSet[graph.EdgeKey{From: str(m, "from"), To: str(m, "to"), Kind: str(m, "kind")}]
			if e == nil {
				return fmt.Sprintf("edge %s -%s-> %s missing in %s", str(m, "from"), str(m, "kind"), str(m, "to"), name(ix, base))
			}
			if d, ok := m["dynamic"]; ok && fmt.Sprint(d) != fmt.Sprint(e.Attrs.Dynamic) {
				return fmt.Sprintf("edge %s dynamic=%v", e.Key(), e.Attrs.Dynamic)
			}
			if mt, ok := m["min_targets"]; ok {
				want := 0
				fmt.Sscan(fmt.Sprint(mt), &want)
				if e.Attrs.Targets == nil || *e.Attrs.Targets < want {
					return fmt.Sprintf("edge %s targets below %d", e.Key(), want)
				}
			}
		}
	case "load_status":
		for _, ix := range side(base, head, str(m, "in")) {
			p := ix.Packages[str(m, "package")]
			if p == nil {
				return "package missing: " + str(m, "package")
			}
			if p.LoadStatus != str(m, "is") {
				return fmt.Sprintf("%s load_status %q, want %q", p.ID, p.LoadStatus, str(m, "is"))
			}
		}
	default:
		return "unknown tier A assertion " + key
	}
	return ""
}

func name(ix, base *graph.Index) string {
	if ix == base {
		return "base"
	}
	return "head"
}

func contains(list []string, id string) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

func checkB(b map[string]any, d *delta.Delta, base, head *graph.Index) string {
	key, v := one(b)
	m := asMap(v)
	switch key {
	case "renamed":
		for _, r := range d.Nodes.Renamed {
			if r.From == str(m, "from") && r.To == str(m, "to") {
				if via := str(m, "via"); via != "" && via != r.Via {
					return fmt.Sprintf("rename %s -> %s via %s, want %s", r.From, r.To, r.Via, via)
				}
				if c := str(m, "confidence"); c != "" && c != r.Confidence {
					return fmt.Sprintf("rename %s -> %s confidence %s, want %s", r.From, r.To, r.Confidence, c)
				}
				return ""
			}
		}
		return fmt.Sprintf("rename %s -> %s not detected (renames: %v)", str(m, "from"), str(m, "to"), d.Nodes.Renamed)
	case "counts":
		got := map[string]int{
			"added": len(d.Nodes.Added), "removed": len(d.Nodes.Removed), "renamed": len(d.Nodes.Renamed),
			"body_changed": len(d.Nodes.BodyChanged), "signature_changed": len(d.Nodes.SignatureChanged),
			"edges_added": len(d.Edges.Added), "edges_removed": len(d.Edges.Removed),
			"packages_added": len(d.Packages.Added), "packages_removed": len(d.Packages.Removed),
			"suppressed": len(d.Suppressed),
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			want := 0
			fmt.Sscan(fmt.Sprint(m[k]), &want)
			g, ok := got[k]
			if !ok {
				return "unknown count " + k
			}
			if g != want {
				return fmt.Sprintf("%s = %d, want %d (%s)", k, g, want, detail(d, k))
			}
		}
	case "body_changed_contains":
		if !contains(d.Nodes.BodyChanged, fmt.Sprint(v)) {
			return fmt.Sprintf("%v not in body_changed %v", v, d.Nodes.BodyChanged)
		}
	case "added_contains":
		if !contains(d.Nodes.Added, fmt.Sprint(v)) {
			return fmt.Sprintf("%v not in added %v", v, d.Nodes.Added)
		}
	case "removed_contains":
		if !contains(d.Nodes.Removed, fmt.Sprint(v)) {
			return fmt.Sprintf("%v not in removed %v", v, d.Nodes.Removed)
		}
	case "not_in":
		id := str(m, "id")
		var list []string
		switch str(m, "list") {
		case "added":
			list = d.Nodes.Added
		case "removed":
			list = d.Nodes.Removed
		case "body_changed":
			list = d.Nodes.BodyChanged
		case "renamed":
			for _, r := range d.Nodes.Renamed {
				list = append(list, r.From, r.To)
			}
		case "signature_changed":
			list = d.SignatureChangedIDs()
		}
		if contains(list, id) {
			return fmt.Sprintf("%s is in %s", id, str(m, "list"))
		}
	case "added_all_in_file":
		for _, id := range d.Nodes.Added {
			n := head.Node(id)
			if n == nil || n.File != fmt.Sprint(v) {
				return fmt.Sprintf("added node %s is not in %v", id, v)
			}
		}
	case "entrypoint":
		for _, ep := range d.Entrypoints {
			if ep.ID == str(m, "id") {
				if ep.Status != str(m, "status") {
					return fmt.Sprintf("entrypoint %s status %s, want %s", ep.ID, ep.Status, str(m, "status"))
				}
				return ""
			}
		}
		return "entrypoint missing from delta: " + str(m, "id")
	case "reach_added":
		for _, ep := range d.Entrypoints {
			if ep.ID == str(m, "id") {
				if !contains(ep.ReachAdded, str(m, "contains")) {
					return fmt.Sprintf("entrypoint %s reach_added %v lacks %s", ep.ID, ep.ReachAdded, str(m, "contains"))
				}
				return ""
			}
		}
		return "entrypoint missing from delta: " + str(m, "id")
	case "suppressed":
		for _, s := range d.Suppressed {
			if s.Package == str(m, "package") {
				return ""
			}
		}
		return "package not suppressed: " + str(m, "package")
	case "structural":
		if fmt.Sprint(v) != fmt.Sprint(d.Stats.Structural) {
			return fmt.Sprintf("structural = %v, want %v", d.Stats.Structural, v)
		}
	default:
		return "unknown tier B assertion " + key
	}
	return ""
}

func detail(d *delta.Delta, k string) string {
	switch k {
	case "added":
		return strings.Join(d.Nodes.Added, ", ")
	case "removed":
		return strings.Join(d.Nodes.Removed, ", ")
	case "renamed":
		return fmt.Sprint(d.Nodes.Renamed)
	case "signature_changed":
		return fmt.Sprint(d.Nodes.SignatureChanged)
	case "edges_added":
		return edgeList(d.Edges.Added)
	case "edges_removed":
		return edgeList(d.Edges.Removed)
	}
	return ""
}

func edgeList(es []graph.Edge) string {
	var parts []string
	for _, e := range es {
		parts = append(parts, e.Key().String())
	}
	return strings.Join(parts, "; ")
}
