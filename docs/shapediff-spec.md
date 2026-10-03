# shapediff: structural change legibility for GitLab merge requests

Status: proposal, v0.5, 2026-09-24. Working name only. v0.1 (2026-09-20) is
kept beside this file as `shapediff-spec.v0.1.md`; section 20 lists what
changed and why, and what v0.3, v0.4 and v0.5 added on top. v0.5 is written
on top of v0.4.1 and v0.4.2 (the catching-up reader of section 2, `weekly
--repo`); it cites those and does not restate them.

This document is written to be reviewed. Every section that makes a design
choice says what the alternative was and why it lost. Sections marked
**Open** are decisions deferred to the spike or to the reviewer. Section 19
collects everything still open.

## 1. Problem

As more MRs are produced with agentic help, and some end to end by agents,
MR volume rises past the point where reviewers can hold the lines of code in
their heads. The team can still control *what* work gets done but loses the
ability to see *how* the system's shape is changing. The unified diff is the
wrong artifact for that when the shape changes: the callers that did not
change are exactly what a reviewer needs for orientation, and they are not
in the diff at all.

**Open:** this paragraph has no number. Before phase 3 starts, record on the
pilot repo: MRs merged per week, median time-to-first-review, and the share
that are agent-authored. Phase 3's metric (section 15) is measured against
that baseline, and the tool is only worth building if the shape actually
changes in a meaningful fraction of those MRs.

## 2. Goal

Make structural change legible, fast, at two cadences.

- **Per MR.** Before reading a hunk, a reviewer sees what the MR did to the
  shape of the system: which entry points' reach changed, which signatures
  changed and who calls them, which packages appeared and where they were
  wired in.
- **Weekly.** One aggregate picture per repo, and a roll-up across repos,
  of where change concentrated and how the structure drifted over the week.
  Not a stack of per-MR diagrams.

The two cadences answer two different questions, and the second reader is
easy to lose sight of. The per-MR reader asks **"what did this MR change"**
and is oriented by the unchanged callers drawn in grey around the change.
The weekly reader asks **"what has this repo become"** — they did not watch
the last ten MRs and are catching up, not reviewing. That reader is served
by the weekly brief, not by making the per-MR note longer: the orientation
cost is paid once a week instead of once an MR, and the per-MR note stays
short enough to be read before the diff. A per-MR note whose repo has a
published brief ends with one line pointing at it (`weekly_url`, section
13), and the brief can be produced for a single local checkout as well as by
the scheduled job (section 14.2).

A short prose summary accompanies the diagrams. The prose is **rendered by
template from the same structured delta the diagrams are drawn from**, so
they cannot disagree. An LLM may later rewrite the templated sentences for
fluency (section 11), but only under validators that hold every sentence to
a claim present in the delta.

A third reader exists and is stated as a goal from v1: **an agent
reviewer**. The delta document (section 8) is published as a CI job
artifact on every MR, structural or not, so an automated reviewer can
consume the same facts the human sees drawn.

## 3. Non-goals

- Bug detection, breaking-change analysis, security scanning, lint, or any
  other review-quality judgement. The output is never a list of violations.
- Cross-service or user-journey dataflow. "Dataflow" here means, within one
  repo, what each entry point (HTTP handler, gRPC method, worker, cron, CLI
  command) reaches: which packages and which sinks (DB, queue, outbound
  client).
- Replacing the diff. This is an orientation aid read before the diff.
- Perfect accuracy. A small error should cost less than thirty seconds of
  reviewer time. But a *confidently wrong call edge* is worse than a missing
  one because the diagram looks authoritative. Precision is prioritised over
  recall for edges. The same rule applies to *removals*: a red edge for
  something that never went away is the worst output the tool can produce,
  and section 8.4 exists to prevent it.

## 4. Core architectural decision: graph diff, not text diff

Extract a code graph at the MR base SHA and at the head SHA. Subtract.
Render the delta. Narrate the delta.

Everything downstream depends on the graph being **deterministic** for a
given SHA and on node IDs being **stable across SHAs**. Any component that
is not deterministic (LLM output, unseeded community detection) is kept out
of the subtracted graph and applied only to the delta afterwards. In v1
there is no non-deterministic component at all: narration is templated
(section 11) and the filter breaks every tie by ID (section 9).

## 5. Scope and sequencing

Go first. JS/TS second. The normalized graph schema (section 6) is
language-neutral from day one so the second extractor does not force a
rewrite of the delta engine or renderer.

**Open:** Go-first is justified by precision, not by where the agent MRs
are. Record the target repos' language mix and the share of agent-authored
MRs per language before phase 1. If most agent-authored MRs land in TS
repos, phases 1 and 6 swap order and the spike (phase 0) runs on a TS repo
with scip-typescript. That reorder is a decision for the spec's owner, not
for whoever runs the spike.

The three "views" from the original framing are three projections of one
graph, not three extractions:

| View | Projection |
|---|---|
| Dataflow | Call graph rooted at entry points, collapsed to packages and sinks |
| Call paths | Call graph at symbol level, filtered to changed nodes and their neighbours |
| Module wiring | Package import graph |

## 6. Normalized graph schema

One JSON document per (repo, SHA). Produced by a language extractor. This is
the contract between extractors and everything else.

```jsonc
{
  "schema_version": 1,
  "repo": "group/service",
  "sha": "abc123",
  "extractor": {
    "name": "go-xtools", "version": "0.1.0", "precision": "typed",
    "toolchain": "go1.25.1",           // illustrative; part of the cache key (section 14.1)
    "config_hash": "sha256 of .shapediff.yml as loaded, defaults applied",
    "builder_mode": "InstantiateGenerics"
  },
  "packages": [
    { "id": "pkg:...", "load_status": "ok" }   // ok | partial | failed
  ],
  "nodes": [ /* Node */ ],
  "edges": [ /* Edge */ ],
  "warnings": []
}
```

`packages[].load_status` is `ok` when the package type-checked cleanly,
`partial` when it loaded with type errors, and `failed` when it did not load
at all. The delta engine reads it (section 8.4); nothing else does.

### 6.1 Node

```jsonc
{
  "id": "sym:github.com/acme/svc/internal/orders.(*Service).Create",
  "kind": "symbol",            // package | symbol | entrypoint | sink | file
  "name": "(*Service).Create", // short display name
  "package": "github.com/acme/svc/internal/orders",
  "file": "internal/orders/service.go",   // not part of id
  "line": 42,                             // not part of id
  "attrs": {
    "symbol_kind": "method",   // func | method | type | interface | var | const
    "exported": true,
    "generated": false,        // file carries "// Code generated"
    "test": false,             // _test.go
    "signature": "func (*Service) Create(ctx context.Context, req CreateReq) (*Order, error)",
    "entry_kind": null,        // http | grpc | worker | cron | cli | loop   (entrypoint nodes)
    "sink_kind": null,         // db | queue | http_client | cache | fs (sink nodes)
    "sink_name_source": null,  // table | package   (sink nodes; section 7.3)
    "doc": "Create validates req and stores the order."  // optional; see below
  },
  "fingerprint": "sha256 of normalized body tokens: identifiers kept, literals replaced by kind, whitespace and comments removed",
  "fingerprint_recv": "as fingerprint, with every occurrence of the receiver type name replaced by a placeholder token (methods only)",
  "body_hash": "sha256 of body tokens with literals kept; whitespace and comments removed",
  "body_tokens": 184,          // token count of the body; used by rename detection
  "body_sketch": [ /* 64 minhash values over the normalized token multiset */ ]
}
```

Two hashes, because one was doing two jobs. `fingerprint` normalizes
literals so a renamed function still matches itself; it is used **only for
rename detection** (section 8.1). `body_hash` keeps literals; it is what
decides `body_changed` (section 8). A timeout changed from `5s` to `500s`
must never read as "no structural change" while behaviour moved.

Two more fields exist because the delta engine sees graphs, never source.
`fingerprint_recv` lets a type rename be detected by hash equality (section
8.1, step 2) without re-tokenizing anything. `body_sketch` is a minhash of
the normalized token multiset, so step 4's similarity is estimated from the
graph alone; 64 values estimate Jaccard to within a few points, which is
enough for a threshold that only ever yields medium confidence.

ID conventions (stable across SHAs by construction):

- `pkg:<import path>`
- `sym:<import path>.<qualified name>`. The encoding is **shapediff's own**,
  not `types.Object.FullName()` (which yields `(*example.com/p.T).M`, a
  different shape). Methods carry the receiver: `(*Service).Create`,
  `Service.String`. Type parameters are normalized **positionally**
  (`(*Cache[T0]).Get`, whatever the parameter is called), so renaming `K`
  to `Key` does not rename every method.
- `entry:<entry_kind>:<route or name>` e.g. `entry:http:POST /orders`,
  `entry:grpc:orders.OrderService/Create`, `entry:worker:process_refund`.
- `sink:<sink_kind>:<name>` e.g. `sink:db:orders`, `sink:queue:refunds`,
  `sink:http_client:payments`.

`doc` is optional. It holds the first sentence of the definition's doc
comment (Go) or docstring (Python), cut to 160 characters, and the call
stacks of section 10.3 quote it. On a package node it is the package
comment (Go, from `doc.go` when there is one) or the module docstring
(Python), and the note's "What it touches" list (section 14.1) opens with
it. It is never part of a fingerprint or body
hash, since editing a comment is not a structural change, and no invariant
or conformance assertion reads it, so an extractor that leaves it out still
conforms.

Line numbers and file paths are attributes, never part of the ID, so a
function that moves files is the same node.

An entrypoint node's `package` is the package of its **registration
site** (where the route, job or command was wired in); it is not part of
the ID, and section 8.4's suppression reads it. Sink nodes have no
package.

Five node classes need a rule or they churn on unrelated edits:

- **`init` functions.** A package may have several; none is in the package
  scope and SSA numbers them in file order, so adding a file renumbers
  them. ID: `sym:<pkg>.init#<file>#<ordinal-within-file>`. This is the one
  deliberate exception to "file paths are never part of the ID": an `init`
  has no name, so the file is the only stable handle, and an `init` that
  moves files is a new node.
- **Closures and anonymous functions** are **not first-class nodes**. SSA
  names them `parent$N` by position, so inserting one churns every later
  closure. Their call edges are attributed to the enclosing declared
  function.
- **Synthetic functions** (bound-method wrappers, thunks, instantiation
  wrappers; anything with a non-empty `Synthetic`) are not emitted. Edges
  through them are re-pointed to the declared function they wrap.
- **Generic instantiations** collapse to the origin symbol; see positional
  normalization above.
- **Test packages.** Loading with `Tests: true` returns both `p` and
  `p [p.test]` at one import path. Nodes are deduplicated by ID; a node
  seen only in the test variant is `test: true`.

### 6.2 Edge

```jsonc
{
  "from": "sym:...",
  "to": "sym:...",
  "kind": "calls",            // calls | imports | implements | handles | writes | reads
  "confidence": "resolved",   // resolved | inferred
  "attrs": {
    "dynamic": true,          // interface or func-value dispatch
    "targets": 2,             // resolved targets at this site (dynamic edges)
    "sites": 3                // number of call sites
  }
}
```

- `handles`: entrypoint -> the symbol registered for it.
- `writes` / `reads`: symbol -> sink. Derived from sink patterns (section 7.3).
- `implements`: type -> interface. Used for rename and for explaining
  dynamic edges, not drawn by default.
- `confidence` is `resolved` for every edge the Go extractor emits. The TS
  extractor will emit `inferred` for name-matched edges. The renderer draws
  inferred edges dashed and the filter can drop them under budget pressure.
- `targets` is how many concrete callees the analysis resolved at a dynamic
  site. It is the quantity that separates "calls the one `Store`
  implementation" (targets 1, drawn like a static edge) from "calls any of
  eleven things" (targets 11, collapsed). Reachability (section 7.4) and the
  filter (section 9) both read it; whether the site *is* an invoke does not
  matter to either.

**Alternative considered:** SCIP as the interchange format. It is precise
and has indexers for Go and TS, but it models occurrences and symbols, not
entry points, sinks, or fingerprints. It is a good *input* to an extractor,
not the interchange format. Revisit if maintaining two extractors becomes
the dominant cost.

### 6.3 Conformance suite

Sections 6.1 and 6.2 describe the contract; this subsection is what
enforces it. An extractor for any language is done when it passes this
suite, and the delta engine, filter and renderer are allowed to assume
nothing about a graph beyond what the suite checks. It has two layers.

**Document invariants.** Run on any single graph document, no fixtures
needed, by `shapediff conform --check <graph.json>`:

- Valid against schema v1 with every enum closed (`kind`, `symbol_kind`,
  `entry_kind`, `sink_kind`, `sink_name_source`, `load_status`,
  `confidence`, edge `kind`). The `extractor` block is complete;
  `builder_mode` may be null for a backend that has none.
- Every edge endpoint names a node in `nodes`; every node's `package`
  appears in `packages`; a package with `load_status: failed` has no nodes
  under it (a `partial` one may).
- IDs are unique and match one of the four grammars in section 6.1. No
  `sym:` ID contains a path separator or file extension except the
  `init#<file>#<ordinal>` form.
- `fingerprint`, `body_hash`, `body_tokens` and a 64-value `body_sketch`
  on every symbol node that has a body; `fingerprint_recv` present exactly
  when `symbol_kind` is `method`.
- `confidence` on every edge; `targets` present exactly when `dynamic` is
  true; `sites` at least 1 on every `calls` edge.
- Every entrypoint node is the source of one `handles` edge; every sink
  node is the target of at least one `writes` or `reads` edge.
- `packages`, `nodes` and `edges` are in canonical order in the file
  itself (packages and nodes by ID; edges by `from`, `to`, `kind`).
  Canonical order is part of the document, not a post-processing step, so
  determinism is checked as byte equality of two runs.

**Fixture scenarios.** Each is a base and head checkout pair under
`conformance/<language>/<scenario>/` with an `expect.yml` of assertions.
The scenarios are named once, language-neutrally; each language supplies
its own source for each. Tier A assertions are checked on the two graphs
alone and gate the extractor; Tier B assertions run `shapediff delta`
and are the phase 2 golden tests (section 15) expressed as fixtures, so
they gate the engine, not the extractor.

| Scenario | Tier A (graphs) | Tier B (delta) |
|---|---|---|
| Symbol rename | node count unchanged | one `renamed`, zero edge changes |
| Type rename | `fingerprint_recv` equal across the pair | one `renamed`, zero edge changes |
| Package move | | one `renamed`, zero edge changes |
| Route rename | handler node ID unchanged | route reported via shared handler |
| Function moved between files | node ID unchanged, `file` differs | no added or removed |
| Literal changed | `body_hash` differs, `fingerprint` equal | `body_changed`, not `renamed` |
| File added containing an init or closure | no unrelated ID changes | no added or removed beyond the new file's |
| Interface dispatch | dynamic `calls` edge with `targets` at least 1 | |
| Generated and test files | `generated` and `test` flags set | |
| Entry point registered | entrypoint node plus `handles` edge | |
| Entry point behind a wrapper (v0.5) | entrypoint node plus `inferred` `handles` edge to the wrapped function | |
| Background loop started from main (v0.5) | `loop` entrypoint node plus `inferred` `handles` edge | |
| Sink called | sink node plus `writes` or `reads` edge, `sink_name_source` set | |
| Package broken at head | `load_status` not `ok` on that package | zero removals, package in `suppressed` |

A scenario a language lacks (generics, `init`, a sink kind the ecosystem
does not have) is declared `n/a` with a reason in that language's
`manifest.yml`, never skipped silently. The manifest is the definition of
done for a new extractor: every scenario is either passing or explained.

**The plug-in contract.** An extractor is any executable invoked as
`<extractor> --repo <dir> --config <path>` that writes one schema v1
document to stdout and exits 0, or writes a message to stderr and exits
nonzero. `shapediff extract` runs the built-in Go extractor by default and
`--extractor <exe>` for any other. The conformance harness drives every
extractor, including the built-in one, through this interface, so what it
certifies is the seam a new language plugs into, not just the file
format. The built-in extractor is the reference implementation: a
question about what the schema means is answered by what it emits on the
Go fixtures.

**Alternative considered:** expected graph JSON per fixture (golden
files). Rejected as the cross-language layer because the bytes are
language-specific and would make every schema tweak a twelve-file edit
per language. Golden files remain fine inside one extractor's own tests.

## 7. Go extractor (`go-xtools`)

### 7.1 Why not Graphify or tree-sitter for Go

Verified against the Graphify repo on 2026-09-20: code edges come from
tree-sitter plus name matching (tagged EXTRACTED / INFERRED), no type
resolution, no documented hook for a custom extractor, and community labels
come from an LLM. For Go this discards the type checker, which is the one
thing that makes precise call edges cheap. Interface dispatch and same-named
methods across types would be wrong often enough to erode trust. Graphify
stays a candidate for the TS/JS extractor (section 12). It is **not** used
for the spike (section 15, phase 0): a spike run on name-matched Go edges
could fail because of the edges and be blamed on the filter.

### 7.2 Mechanism

- Load with `golang.org/x/tools/go/packages`, mode `LoadAllSyntax |
  NeedModule`, `Tests: true`, pattern `./...`, build tags and env from repo
  config (section 13). `NeedModule` is what "module-local packages only"
  needs. Note the cost: `LoadAllSyntax` type-checks the whole transitive
  closure including the standard library, and SSA builds all of it, so
  memory scales with the closure, not the repo. Phase 1 gates on it.
- Build SSA with `golang.org/x/tools/go/ssa/ssautil.AllPackages`, builder
  mode `InstantiateGenerics`. The mode is recorded in `extractor` and is
  part of the cache key.
- Call graph with `golang.org/x/tools/go/callgraph/vta` (variable type
  analysis). It resolves interface dispatch with good precision at
  acceptable cost. When given no initial graph it seeds itself from CHA, so
  CHA is a component VTA is built on, not a rejected alternative; CHA
  *alone* was rejected because it over-approximates (every implementer of
  every interface). RTA was rejected not because it needs a single root
  (it takes a slice) but because it needs roots at all, and section 7.3
  derives the entry points *from* the call graph, which would be circular.
  VTA's own package documentation carries a TODO about generic function
  bodies and instantiation wrappers, so precision on heavily generic code is
  a phase 1 fixture to test, not an assumption; section 7.5's earlier
  claim that "VTA handles instantiations" is withdrawn to that fixture.
- Mark edges `dynamic: true` where the SSA call site is an invoke (interface
  method) or a call through a func value, and record `targets` as the
  number of distinct declared functions VTA resolved at that site after
  wrapper re-pointing.
- Import graph from the loaded packages' `Imports` map, module-local
  packages only; external modules collapse to one node per module path.
- Fingerprint and body hash as defined in section 6.1.
- Files with a `// Code generated ... DO NOT EDIT.` header get
  `generated: true`. `_test.go` files get `test: true`. Both are extracted
  (they matter for rename detection and for counts) but hidden by default.
- Every package gets a `load_status` (section 6). A head that does not
  compile is the normal state of an early push on a WIP MR, so this is the
  expected case, not an error path.

### 7.3 Entry point and sink detection

A pattern table, evaluated against the call graph and AST. Built-in
patterns cover the common cases; repo config can add more.

Entry points (produce an `entrypoint` node and a `handles` edge):

| Kind | Pattern |
|---|---|
| http | `net/http` `HandleFunc`/`Handle`, `chi` `Get/Post/...` and `Method`, `gin` `GET/POST/...`, `echo`, `gorilla/mux` `HandleFunc().Methods()`. Route string is the literal first arg where it is a literal; otherwise `<dynamic>` plus the handler symbol. |
| grpc | Generated `Register<Service>Server(s, impl)` calls; each interface method on the server interface becomes an entrypoint, handler is the concrete method on `impl`. |
| worker | Configurable: a call to a registration function with (name literal, handler) shape, e.g. `mux.HandleFunc("task:name", h)` for asynq, `river.AddWorker`. Default table ships asynq, river, machinery, and a generic "any func whose name matches `^(Handle|Process|Consume)` on a type that implements a configured interface". |
| cron | `robfig/cron` `AddFunc`, `AddJob`; configurable. |
| cli | `cobra` `Command.Run`/`RunE`; `main.main` if no other entry points exist. |

Because the route string is part of the entrypoint ID, a route rename reads
as one entrypoint removed and one added. The delta engine reports it as a
rename when the two share a handler symbol (section 8.1, step 0).

**Added in v0.5, not implemented.** Two gaps found by the period-map spike
(`docs/spike-period-map-2026-09-24.md`) on a repo that registers routes on
the standard library's `ServeMux`. Both matter more on the period map
(section 10.2), whose purpose is to show where data goes, than on the
per-MR note:

- **Wrapped handlers.** When the handler argument of a registration is
  itself a call (`mux.Handle(p, withAuth(h.list))`, `http.HandlerFunc(f)`,
  `chain(a, b)(f)`), the handler is the innermost argument that is a
  function or method value declared in the module, and the `handles` edge
  is `inferred`. When the argument is an `http.Handler` value whose VTA
  target set is exactly one `ServeHTTP` declared in the module, that method
  is the handler; this is a mount such as `Handle("/fever/",
  fever.Serve(store))`. A mount whose target is a `ServeMux` is not an
  entry point: the routes registered on that mux are. A `handles` edge is
  exempt from section 9 step 4a whatever its confidence: that step drops
  uncertain call edges, and a route is not uncertain for being wrapped.
  On the spike repo, 14 of one package's 15 routes were missed for want of
  this rule.
- **Background loops (`loop`).** A `go` statement whose callee is a
  function or method declared in the module, in a function narrowly
  reachable (section 7.4) from `main.main` or from a `cli` entry point's
  handler, produces a `loop` entry point named by the callee (a goroutine
  has no other identity; cron is named by its handler for the same
  reason), with an `inferred` `handles` edge. `go func() { ... }()` is
  attributed to the enclosing function like any closure (section 6.1) and
  so is not found; a repo adds such loops, and loops started outside
  main's reach, with `entrypoints: loop: - func: "<import path>.<name>"`.
  A channel hand-off is still not an edge. The rule matters because the
  channel's *consumer* (a worker's `Run`, started with `go` by its pool) is
  then an entry point with its own reach, so a producer-and-workers pipeline
  shows as two entry points with the channel between them unstated. On the
  spike repo, that pipeline is the whole feed refresh path, and without the
  rule it is absent from the map.

Until the two rules are built, their section 6.3 scenarios
(`entrypoint-behind-wrapper`, `background-loop`) are declared `n/a` with
that reason in `conformance/go/manifest.yml`, so the suite is not silently
short.

Sinks (produce a `sink` node and a `reads`/`writes` edge from the calling
symbol):

| Kind | Pattern |
|---|---|
| db | Calls into `database/sql`, `pgx`, `gorm`, `sqlc`-generated `Queries` methods, `ent`. Sink name is the table **only when recoverable unambiguously from a sqlc-generated method** (the generated file carries the query); otherwise the package. `sink_name_source` says which. String literals are never parsed for table names in v1. |
| queue | Publish/enqueue methods on configured client types (asynq, river, kafka-go, sarama, amqp, pubsub). |
| http_client | `net/http` `Client.Do/Get/Post`, and any configured client package (e.g. an internal `paymentsclient`). Name is the package. |
| cache | redis clients. |

Sink attribution is deliberately coarse. "Reaches the orders DB" is the
sentence a reviewer needs; column-level is out of scope. Table-level from
sqlc is taken because a single `internal/db` package otherwise makes every
entrypoint reach one sink node and the dataflow view stops discriminating;
it is taken only from generated code because a guessed table name is a
confident wrong sink.

### 7.4 Reachability

Not stored in the graph; computed by the delta engine from the graph. For
each entrypoint, BFS over `calls` edges from its handler, collecting the set
of reached packages and sinks. The walk records, per reached item, the
**widest dynamic edge on the narrowest path to it** (`max targets`). An
item is **narrow-reached** when that number is at most `k` (default 2,
configurable) and **wide-reached** otherwise. Narrow reach is what the
reviewer is shown; wide reach is listed, never drawn (section 8.2).

Static-only reach was considered and rejected: an idiomatic handler calls a
service interface which calls a store interface which hits the DB, and a
static-only walk makes every such entrypoint reach nothing. VTA's whole job
is to make those sites resolve to one or two targets; `targets` is the
number that says whether it did.

### 7.5 Known Go hazards

- Build tags: extract with a fixed tag set from repo config so both SHAs
  are comparable. Default: none.
- Generics: instantiations collapse to the origin symbol with positional
  type-parameter names (section 6.1). VTA precision on generic bodies is a
  phase 1 fixture (section 7.2).
- cgo and packages that fail to load: recorded as `load_status: failed`
  and skipped; the delta engine suppresses removals under them (section
  8.4) and the MR note says so.
- Monorepos with many modules: config lists module roots; one graph per
  module, delta per module.
- Fan-out through very wide interfaces (e.g. a `Store` interface with 60
  methods and 3 implementations) can make every entrypoint reach
  everything. Mitigation: `targets` on the edge, narrow-versus-wide reach
  (section 7.4), and the filter (section 9): collapse to package, mark
  dynamic, cap.

## 8. Delta engine

Input: base graph, head graph. Output: one delta document.

```jsonc
{
  "schema_version": 1,
  "repo": "...", "base_sha": "...", "head_sha": "...",
  "mr_iid": 123,                       // when run in an MR pipeline
  "nodes": {
    "added": [ids], "removed": [ids],
    "signature_changed": [{ "id": "...", "before": "sig", "after": "sig" }],
    "body_changed": [ids],           // body_hash differs, signature same
    "renamed": [{ "from": "id", "to": "id", "confidence": "high|medium", "via": "entrypoint|package|type|fingerprint|similarity" }]
  },
  "edges": { "added": [edges], "removed": [edges] },
  "entrypoints": [
    {
      "id": "entry:http:POST /orders",
      "status": "added|removed|reach_changed|unchanged",
      "reach_added": ["sink:queue:refunds", "pkg:.../internal/refunds"],
      "reach_removed": [],
      "wide_reach_added": [],          // reached only through edges with targets > k; listed, not drawn
      "wide_reach_removed": [],
      "changed_paths": [["entry:...", "sym:...", "sym:...", "sink:..."]]
    }
  ],
  "packages": {
    "added": [], "removed": [],
    "imports_added": [{ "from": "pkg:", "to": "pkg:" }], "imports_removed": []
  },
  "suppressed": [ { "package": "pkg:...", "base": "ok", "head": "partial", "hidden_removals": 14 } ],
  "stats": { "entrypoints_added": 1, "entrypoints_reach_changed": 2, "symbols_added": 7, "structural": true, ... },
  "warnings": []
}
```

`stats.structural` is false when the only changes are `body_changed`. It is
the one bit the CI job reads to decide whether to post (section 14.1).

### 8.1 Rename detection

Refactor MRs are where structure matters most and where naive diffing
renders as delete-everything add-everything. v0.1's algorithm matched
individual symbols only, which missed the two refactors it was written
for: a **type rename** changes the receiver in every method ID and every
body that mentions the type, and a **package move** changes the import
path in every ID. Both rendered as N removed, N added. The order is now
hierarchical, coarse to fine:

0. **Entrypoints.** A removed and an added entrypoint with the same
   `handles` target: `renamed`, via `entrypoint`, confidence high.
1. **Packages.** A removed `pkg:P` and an added `pkg:Q` where the symbol
   short names under P and Q overlap by at least 0.6 (Jaccard on names
   with the package qualifier stripped): P renamed to Q, via `package`,
   confidence high. All P's symbols are re-keyed under Q before later steps.
2. **Types.** Within a package (after step 1), a removed type T and an added
   type U where the method short names overlap by at least 0.6 and the
   matching methods have equal `fingerprint_recv`: T renamed to U, via
   `type`, confidence high. Methods and bodies are re-keyed before later
   steps.
3. **Fingerprint.** A removed and an added symbol with equal `fingerprint`,
   where the fingerprint is **unique on both sides** (a 1:1 match; ambiguous
   fingerprints such as trivial accessors do not match) and `body_tokens`
   is at least 12: `renamed`, via `fingerprint`, confidence high.
4. **Similarity.** Same package (after re-keying) or git-renamed file, same
   symbol kind, same arity, `body_tokens` within 20 percent, signature
   equal after renaming the identifier, and Jaccard similarity estimated
   from `body_sketch` at least 0.67 (equivalent to Dice 0.8), taken as a
   **mutual best match** in both directions: `renamed`, via `similarity`,
   confidence medium. Shown as "probably", in the summary list and in
   the details list.
5. Everything else stays added/removed.

Git's own file rename detection (`git diff -M --name-status base..head`)
seeds step 4's "git-renamed file" and nothing else; because IDs do not
include the file, symbol moves between files already match without it.

A renamed node's edges are matched through the rename before edge deltas
are computed, so a pure rename yields zero added/removed edges. The phase 2
golden tests include a type rename, a package move, and a route rename,
each of which must yield zero edge changes.

### 8.2 Reachability delta

For each entrypoint present in either graph:

- Absent in base: `added`. Absent in head: `removed`.
- **Narrow** reach set differs (section 7.4): `reach_changed`, with
  `reach_added` and `reach_removed` at the package-and-sink granularity.
- Wide reach set differs and narrow does not: status stays `unchanged`;
  the difference is recorded in `wide_reach_added/removed` and appears in
  the note's collapsed section. This is what stops a 60-method `Store`
  interface from flagging every entrypoint on every MR.
- `changed_paths`: for each item in `reach_added`, one shortest path from
  the entrypoint that passes through at least one node in
  `nodes.added ∪ nodes.signature_changed ∪ endpoints of edges.added`. This
  is the path the diagram draws. Ties prefer paths with lower `max
  targets`, then lexicographically smaller node IDs, so the choice is
  deterministic.

Body-only changes (`body_changed` with no edge change) are listed in the
note's collapsed section but never drawn as nodes. They are what the diff
is for. A body-changed node that is *already drawn* because it lies on a
`changed_paths` route gets a small badge (section 10); see section 18,
question 2.

### 8.3 Weekly delta

The weekly delta is **not** the sum of per-MR deltas. Per-MR deltas do not
compose (an edge added in MR 1 and removed in MR 3 cancels; merge order
matters). Instead:

- Resolve `main` at the week's start and end to SHAs **and print them in
  the page**; "main@week_start" is not re-derivable later.
- Diff those two SHAs directly with the same engine.
- **Attribute** each changed node to MRs by joining on the **cached per-MR
  deltas** (section 14.1 caches the delta document per MR alongside the
  graphs): a node is "touched by !123" when !123's delta lists it in
  `added`, `removed`, `signature_changed` or `renamed`. The merged MRs of
  the week come from the GitLab merge requests API filtered by
  `merged_after` and `merged_before`, which works under every merge method
  (`git log --merges` returns nothing on a squash-merging project) and
  returns each MR's final head SHA, the other half of the delta cache key.
  This is exact at symbol granularity, costs one cache read per MR, and
  names MRs, never people.

v0.1 proposed `git log -L` and blame for attribution. Dropped: `-L` works on
line ranges or a heuristic function-name regex, is unreliable across moves,
and everything it would recover is already in the per-MR deltas.

**Any period (v0.5).** Nothing in the mechanism above is weekly: it
resolves the branch at two instants and diffs the two SHAs. v0.5 calls the
window a **period**: a week, a calendar month, a calendar quarter (all UTC),
or explicit dates. Two things change with longer windows. The MR list is
longer (the merge requests API is paged, and a quarter on a busy repo is
hundreds of MRs, still one cache read each). And a per-MR delta that left
the cache before the period ended is attributed as "not cached", never
recomputed; section 14.3 sets the retention that avoids it. Summing shorter
periods stays ruled out for the reason above: a quarter is not twelve weeks
added together.

### 8.4 Load-status asymmetry

`packages.Load` continues past type errors. A head with one broken package
has missing edges, and a naive subtraction renders them as **removals**: a
confident red edge for something that never happened, which is the exact
output section 3 forbids. So:

- For any package whose `load_status` is not `ok` in **either** graph
  (base or head), the engine suppresses every added/removed node and edge
  under it and records the package in `suppressed` with the count it hid.
  A node with no package (a sink) is suppressed when every edge touching
  it comes from a suppressed package; an entrypoint is under its
  registration site's package (section 6.1).
- The note says so in one line at its top: "**Head does not compile:**
  `internal/orders` (14 changes hidden)", or "Base does not compile" when
  it is the base that failed. Not a generic extraction failure,
  because reviewers will learn to ignore a generic one.
- Reachability is computed only over packages `ok` in both graphs; an
  entrypoint whose handler is under a suppressed package is reported
  `unchanged` with a warning.

Phase 2 golden test: break one package at head, assert zero removals.

## 9. Filtering (the per-MR legibility algorithm)

This is the part that decides whether anyone uses the tool. Stated as an
algorithm so it can be tested with golden files. Every step is
deterministic: every ordering is a total order ending in node ID.

Inputs: delta, head graph, base graph, and three budgets: **rendered
source characters `S`** (the unit GitLab's Mermaid limit is expressed in;
default set from the value measured in the spike, section 10), **node cap
`B`** (default 24) and **edge cap `E`** (default `1.5 × B`, so 36). `S`
is primary; `B` and `E` are derived guards that keep a diagram legible
before it hits the character wall.

Definitions:

- A package is **touched by the MR** when any node under it is in
  `nodes.added ∪ nodes.removed ∪ nodes.signature_changed ∪ nodes.renamed
  ∪ nodes.body_changed`.
- **Priority** of a node, highest first: on a `changed_paths` route;
  `signature_changed`; `added` or `removed`; context. Ties by ID.

Algorithm:

1. **Focus set F** = `nodes.added ∪ nodes.removed ∪ nodes.signature_changed
   ∪ endpoints(edges.added ∪ edges.removed)`, excluding `generated` and
   `test` nodes (test edges are counted and shown as a badge on the node,
   e.g. "3 tests"). An added edge whose endpoint was excluded is re-pointed
   to that endpoint's `pkg:` node.
2. **Context set C** = 1-hop callers and callees of F in the head graph
   (base graph for removed nodes). Per focus node, at most 8 callers and 8
   callees are taken, highest priority first, then by ID; the rest become a
   "+N callers" badge on the focus node. This is what stops a common helper
   from dragging in thousands of nodes before step 3 can run.
3. **Collapse** any node in C whose package is not touched by the MR into
   its `pkg:` node, **except** callers of a `signature_changed` node, up to
   8 per changed signature, which stay as symbols: those callers are the
   ones that kept compiling through a widened interface or an added return
   value, and their identity is the point. A collapse only happens for a
   group of two or more nodes; collapsing a lone node into a fresh package
   node does not reduce the count and is skipped. Merge edges accordingly
   with `sites` summed.
4. **Budget.** While `|nodes| > B` or `|edges| > E` or rendered source
   exceeds `S`, apply the first step that still changes something:
   a. Drop `inferred` edges and any context node only reachable through
      them.
   b. Drop edges with `targets > k` (section 7.4) and any context node
      only reachable through them; the dropped reach is already in the
      collapsed section.
   c. Collapse context nodes into their package nodes, lowest degree first,
      ties by ID, degree recomputed after each collapse, groups of two or
      more only.
   d. Collapse focus nodes in the same package with identical
      added/removed status into one "`pkg` +N more" node, **exempting** nodes
      on a `changed_paths` route and `signature_changed` nodes, which are
      the MR's subject.
   e. Split into one diagram per entrypoint drawn, changed ones first, plus
      one "not reachable from any entrypoint" diagram for what is left, each
      budgeted separately, and put all but the first in a collapsed section.
      An unchanged entry point is drawn when it calls a changed signature;
      splitting only by changed ones left it in the remainder, under a title
      that said no entry point reached it.
   f. **Terminal rule.** If a single diagram is still over budget, keep the
      top-`B` nodes by priority and the edges among them, and add one
      "+N more" node with a count. If the rendered source still exceeds
      `S` (long labels), lower `B` by 4 and repeat this step; below `B = 8`,
      fall back to the graphviz SVG attachment (section 10). This step
      always terminates the loop.
   g. **Orphan rule.** A node with no drawn edge is not drawn. It carries no
      path information — the diagram is called call *paths* — and the budget
      it spends is budget the surviving edges need. The prune runs before
      step 4, again after any collapse in 4c or 4d (merging edges can strand
      a node by turning its last edge into a self loop), and again after 4e
      and 4f, which keep nodes by priority but only the edges *among* them
      and so strand nodes of their own. The "+N more" remainder of 4f is
      edgeless by construction and is exempt. Every pruned node keeps its row,
      with its permalink, in the note's details table, and the count is
      reported there; nothing is dropped silently. If a diagram has no edges
      left at the end, it is not drawn at all: there were no paths to show.
5. **Dataflow diagrams** are drawn separately and independently of budget:
   **one diagram per changed entrypoint**, showing `reach_added` and
   `reach_removed` at package-and-sink granularity (and the kept reach of
   section 10.1). Each is small by construction. They were one diagram with
   a row per entry point, and two entry points reaching the same packages
   drew a knot of crossing edges in which a reader could not tell whose
   reach was whose.
6. **Module wiring diagram** is drawn only when `packages.added`,
   `packages.removed`, or `imports_added/removed` is non-empty. Shows
   touched packages and their imports, added edges green, removed red.
   **Test and generated packages are excluded**, as they are from the focus
   set in step 1, and the count left out is reported in the details section:
   a repo with a test module per source module otherwise draws a wiring
   diagram that is mostly test modules. Step 5 is exempt from the budget
   because it is small by construction; this diagram is **not**, so the node
   and character budgets apply, through the terminal rule of 4f only — steps
   4a to 4d are about call edges and symbol nodes, and this diagram has
   neither. Being over budget here is a legibility problem, not a reason to
   fall back to SVG. The orphan rule of 4g applies.

If the MR has no structural change at all (`stats.structural` is false),
**nothing is drawn and no note is posted** (section 14.1). This case must
be fast and quiet; it will be the majority of MRs.

## 10. Rendering

Per-MR: Mermaid `flowchart LR`, since GitLab renders it natively in MR
notes and it needs no image hosting.

- Node styling by status: added green fill, removed red dashed border,
  signature-changed amber fill, context grey. Dynamic edges dashed.
  Inferred edges dotted. One key per note, above its first diagram (the
  package map of 10.1 when it is drawn); it names the shapes as well as the
  colours, and writes a clause only when the note draws a diagram it
  describes. It is a short list, one line per concern (colours as emoji
  swatches, shapes, arrows, the package map, marks), not a paragraph: a
  reader looks a symbol up in it, and a paragraph has to be read through
  to find one.
- A node that is drawn because it lies on a `changed_paths` route and is
  also `body_changed` carries a `*` suffix on its label, meaning "body also
  changed here". It is a badge, not a colour; amber is taken.
- **Every diagram has text that reads it**, printed beside it and
  generated from the diagram and the graph behind it, never from the
  delta, so it cannot claim what the picture does not show. It says what
  the picture cannot; restating the arrows in words ("it reaches `a`, `b`
  and `c`") tells the reader nothing the boxes did not.
  - A **dataflow** diagram gets what the entry point is for and why each
    arrow is there, read off the entry point's call stack (10.3): whether
    it is new, removed or its reach changed, and the handler that serves
    it; the docstring of the handler, or of the first function below it
    that has one (a type's docstring describes the type, not the call),
    quoted and attributed, never written by the tool; then one list item
    per box, the tables first, then the packages where the stack meets
    changed code, then context. A table says whether it is read, written or
    both and by which definition; a package names the definitions the stack
    calls in it, with the docstring when it names one. An entry point whose
    reach changed gets the items under "now also reaches", "no longer
    reaches" and "still reaches".
  - A **call-path** diagram gets where it starts and then a list, one item
    per package and status, swatched as in the key: what is new, removed or
    has a changed signature, then what is drawn as context, each definition
    named and linked to its line; a "`pkg` +N more" box is opened up by name
    there, since the box alone does not say what it holds.
- Node labels are short names (`Service.Create`). Packages appear as
  either a `subgraph` box (when the package has symbol nodes drawn) or a
  collapsed `pkg:` node (when it was collapsed in section 9), **never both
  in one diagram**, because Mermaid forbids a node and a subgraph sharing
  an ID. Rendered IDs are sanitized and prefixed by kind (`n_`, `sg_`).
- Entry points are stadium shapes, sinks are cylinders.
- **Links.** Every drawn node has a permalink in the `<details>` table to
  `/-/blob/<head_sha>/<file>#L<line>` (base SHA for removed nodes), and to
  the MR diff anchor when the file is in the MR. Mermaid `click` handlers
  are added only if the target instance's renderer permits them; the table
  is the guaranteed path.
- **Open:** GitLab enforces a size limit on rendered Mermaid, expressed as a
  frontend constant on **source characters**, not nodes. Measure it on the
  target instance during the spike, set `S` from it, and confirm that a
  `classDef` preamble plus `B` subgraphed nodes fits. Fallback: render SVG
  with graphviz `dot` and upload as an MR attachment. The upload is not
  built; until it is, the note says the diagram is not drawn (section 20,
  UX review).

Weekly: Mermaid cannot keep layout stable across renders, which makes
week-over-week drift unreadable. Weekly package-level diagrams use
graphviz `neato` with **pinned positions**: positions for existing packages
are stored (in the tool's cache, keyed by repo) and emitted as `pos` with
`pin=true` on those nodes, `notranslate=true` on the graph so output
coordinates match input; new packages carry no `pos` and neato places them,
which puts them near their importers by construction. New positions are
stored after each run. `neato -n` was considered and rejected: it assumes
every node already has a position, which is exactly what a new package
lacks.

Layout reset policy: when more than 25 percent of packages are new since
the stored layout, or any stored package was deleted, re-layout from
scratch and bump the layout file's version so the reset is visible in the
page ("layout reset this week").

### 10.1 The baseline view ("Where this lands")

Section 9 cuts the picture to the change and one hop around it, which is
right for a reviewer who carries the repo in their head and wrong for
anyone who does not: the delta floats with nothing to anchor it. The
**third reader** this section serves is a person with no context on the
repo, who needs to see the whole shape once and the change placed on it.
The base graph already is that shape, extracted whole and cached per SHA
(section 14.1); this section is the rendering that was missing, and adds
no extraction, no state and no cache entry.

The section sits in the note between the narration and the dataflow
diagram (section 14.1), and has three parts, all read from the base graph
(the head graph only for a package the MR added):

1. **One sentence of size**: packages, entry points and sinks at the base
   SHA, and how many external modules are not drawn, then how many
   packages the MR changes code in. The base SHA is printed: it is the
   identity of the baseline.
2. **One list item per touched package** (section 9's definition), not a
   table row: its size at the base counted by kind ("96 functions, 40
   methods, 17 types and 3 constants", every kind, since "others" would
   say no more than "symbols" did); then one
   line per kind of change, the count with the definitions it names ("19
   added: `QuizHandler`, `GetQuiz`, ... and 13 more", five names, program
   code before tests), a changed definition split into "changed signature"
   and "edited, same signature", or "new package" / "removed package" in
   place of the count; then which internal packages import it (the blast
   radius), which entry points reach it narrowly (section 7.4) or are
   registered in it, and which sinks its symbols read or write, each cut
   at four names plus "+N". A line with nothing to say is left out. Each
   package links to its directory tree at the base SHA.
3. **The package map**: every internal package as a node, boxed by
   directory (the parent path relative to the packages' common root),
   import edges among them, one node per (package, entry kind) labelled
   with the count ("12 http routes", or the one name), one node per sink
   kind. Touched packages are highlighted (purple, a colour the other
   diagrams do not use), added packages green, removed red dashed, added
   and removed entry points drawn singly in their colour, added and
   removed imports coloured likewise. External modules are not drawn.

The map is Mermaid under the same `S`, `B`, `E` budget as every other
diagram, and is cut deterministically, ties by ID, in this order, each
step only while still over budget: (a) drop import edges between two
untouched packages; (b) drop entry and sink edges on untouched packages
and any entry or sink node left without an edge; (c) fold the untouched
packages of one directory into a "N more packages" node in that box,
largest directory first, repeated; (d) keep the touched packages and their
direct neighbours, the rest as one "+N packages" node; (e) keep the
touched packages only; (f) keep the top-`B` touched packages by count of
changed symbols plus the "+N packages" node (`B` plus one, as step 4f's
"+N more" is), lowering `B` while the source is still over `S`. Step f
always terminates; if the source is over `S` even at the floor, the table
stands and the map is omitted with one line saying so. What was folded or
hidden is said in a caption under the map, each package counted once. The
map has no legend of its own: the note's one key (section 10) covers it. The
reasoning for the order: a package as a node with no edges is still a
place on the map; an edge between two untouched packages is the first
thing a reader placing a change can do without.

Two things this deliberately is not. It is not the weekly pinned layout:
Mermaid cannot pin positions, so the map is not stable across MRs the way
the weekly diagram is; boxing by directory gives the reader a stable
frame instead, and pinned positions would need an SVG attachment (section
10's fallback path), which is not built. And it is not a second call-path
diagram: symbols are never drawn here.

**Baseline on the dataflow diagram.** The same reader is served by section
9 step 5 drawing, beside each changed entry point's `reach_added` and
`reach_removed`, what that entry point still reaches: its narrow reach at
head (base for a removed entry point) minus what the delta already
reports, minus suppressed packages, in grey, up to six items with sinks
first and the rest as a "+N more" badge. For an added entry point the
whole reach is drawn green and for a removed one red, because to the
reader all of it is new or gone. The delta is never cut to make room: if
the kept reach puts the diagram over budget it is taken back out, entry
point by entry point, most items first, until the diagram fits. "Now also
reaches `queue`" reads differently next to "and still reaches `db`", and
an added endpoint with no reach drawn was the least legible row in the
old note.

`baseline: off` in the repo configuration (section 13) removes the section.

### 10.2 The period map

The reader is section 2's catching-up reader at a longer range: someone
back from a month away, new to the team, or reviewing a quarter. Their
question is **"how does this system move data, and where did the period's
change land on that?"** The weekly page answers "which packages changed and
who changed them"; the package import diagram answers "what depends on
what". Neither says where a request goes. This section is the rendering for
that, and it adds no extraction: everything below is computed from the
period's base graph, head graph and delta (section 8.3). The spike that
tested it on one quarter of an 81k-line repo is
`docs/spike-period-map-2026-09-24.md`: 19 nodes and 34 flows, where only one
flow changed in the quarter. That is the same lesson 10.1 learned per MR,
at the period scale: **draw the map with the change marked on it, not a
diff of two maps**, because the diff alone is nearly empty.

**Components.** Packages are grouped into components by the `components:`
key (section 13). Each internal package belongs to exactly one: the glob
with the longest literal prefix wins, ties by component ID. A package no
glob matches falls into a fallback component named by its first two path
segments under the module root (`internal/billing`), and the page lists
every fallback by name, because a map made of fallbacks is a directory
listing: on the spike repo it would have had 27 groups where the written
map had 14. A component marked `support` (models, config, helpers) is
counted and never drawn: calls into it would join every component to it.
Test and generated symbols are never counted.

Why the grouping is written down rather than inferred: a grouping is a
claim about the system that the delta does not contain, so section 3 and
section 11 rule out letting a model make it. It is written once per repo
and reviewed like code. An agent may propose a mapping as an MR against
`.shapediff.yml`; a person merges it.

**Nodes.**

- One node per **entry group**: the entry points of one kind whose handler
  sits in one component, labelled with the count ("97 http, Web UI").
- One node per component with at least one flow, labelled with its name
  and definition count.
- One node per sink (section 7.3), shaped as in section 10.

**Flows.** Narrow reach (section 7.4) is computed from every non-test
handler at once, so a symbol is on the map when some entry point reaches it
narrowly.

- An entry group flows to the component of its handlers.
- A component flows to another when a narrowly reached symbol in one calls
  a narrowly reached symbol in the other.
- A component flows to a sink when a narrowly reached symbol in it reads or
  writes that sink.

Wide reach draws nothing; it is counted in the cards. The same flows are
computed on the base graph, with package renames (section 8.1) applied to
base IDs first, so each flow is **kept**, **new** or **gone**, and a package
moved within one component is not a flow change.

**What changed, on the map.**

- **Tags on components**, from the period delta: added, removed and
  signature-changed definitions, and new or removed packages. A component
  with any of these is drawn as *touched* (10.1's purple), a component new
  in the period green, a component gone red dashed.
- **Tags on entry groups**: how many entry points were added, removed, or
  changed reach.
- **New flows** green and thicker, **gone flows** red and dashed, kept
  flows grey. These are section 10's colours.
- **Cards** beside the canvas: the section 11 templates over the period
  delta, the entry points whose reach changed with what they now reach
  (10.1's "which entry points are new" rule applies), and the flows that
  appeared or went away. No new claim class: every card line names IDs
  present in the delta or in the two flow sets.
- **Guided views**, at most five, in a fixed order: what changed; who
  touches each sink kind; then one per entry group with a changed entry
  point, most changes first, ties by ID.

**Coverage line, printed above the map.** It lists the entry kinds found,
with counts. It names every component whose definitions no entry point
reaches, with its definition count. And it lists the fallback components.
The spike found this necessary, not decorative. Its map drew "Refresh
workers" with no outgoing flow, because the feed refresh loop was started
by a bare goroutine that no pattern detected (section 7.3's `loop` rule
closes that). A map of a system reads as complete even when it is not, and
the coverage line is what says where it is not. A confidently wrong map is
the period-scale version of section 3's confidently wrong edge.

**Size.** The map is a page, not a note, so `S` does not apply. The node
and edge caps are `2B` and `2E` (48 and 72 by default): a page can hold
twice a note, and the spike repo needed 19 and 34 with no cutting. Over the
caps, cut deterministically, ties by ID, each step only while still over:

1. Drop kept component-to-component flows, fewest calls first. New and
   gone flows are never cut.
2. Fold untouched fallback components into one "N more packages" node,
   smallest first.
3. Omit the map and print the tables, with one line saying so.

**Open**: this order is untested, because the spike repo needed none of it.

**Layout and rendering.** Layers are fixed in this order:

- entry groups;
- the components that host handlers;
- the other components, by breadth-first distance from the hosts over the
  union of both periods' flows, with components that touch a sink last;
- sinks.

Graphviz `dot` lays it out: `rankdir=LR`, one `rank=same` subgraph per
layer, HTML-like labels for the name, count and tag lines, and nodes and
edges emitted in ID order. `dot` routes backward and same-layer flows,
which a grid cannot. For one Graphviz version, identical input gives
identical output. Graphviz is already used by the weekly job for section
10's pinned `neato` layout.

The SVG is wrapped in **one self-contained HTML file**: the cards beside
the canvas, one key, guided views as buttons that dim what is out of
focus, a click on a node that highlights its flows and lists its
definitions with permalinks at the head SHA, and a theme that follows
`prefers-color-scheme`. The file makes no external requests and uses no
framework. The script is presentation only, and the static SVG and cards
carry everything the page claims. Where `dot` is absent, the same model is
written as a Mermaid `flowchart LR` with one subgraph per layer: no
interaction, and GitLab's `S` limit applies if it is pasted into a note
(the spike's map was 2814 characters).

**Open**: how stable the layout is across consecutive periods. `dot` does
not take pinned positions the way `neato` does (section 10). Layer
membership and in-layer ID order are the stable frame; how far nodes move
week to week has not been measured. The pinned weekly package diagram stays
for the reader who needs fixed positions.

Considered and not taken:

- **archify** (an agent skill that renders typed JSON to interactive
  HTML). The spike fed it this model and it refused every attempt: its
  dataflow type is a grid of at most 5 layers by 5 rows with no edge
  router, and its validator wants per-edge geometry that an author supplies
  by hand. Making generated input pass means writing a router for its
  linter, and it adds a Node runtime to CI. Its ideas are taken: guided
  views, cards, one key, focus on click, a self-contained file.
- **Mermaid as the primary renderer.** No interaction, no stable layout,
  and the character limit.
- **Layout in the browser** (ELK.js inline). The picture would depend on a
  script version, could not be diffed as SVG, and costs a large inline
  bundle for a layout `dot` already does.

### 10.3 Call stacks

The diagrams answer "which packages does this entry point now touch". A
reviewer's next question is "in what order, through what, and what else runs
next to the change", which a graph drawn left to right answers badly and an
indented list answers well. The note carries a **call stack** per entry
point, after the call-path diagrams.

**Which entry points.** Every entry point whose status is not `unchanged`,
and every unchanged entry point from whose handler changed code is reachable:
a route whose handler calls a changed signature is the MR's blast radius even
though its reach did not move. Changed entry points first, then by ID. At
most eight are listed; the rest are named in one line.

**The tree.** It is built over the union of the head and base graphs, so a
call the MR removed can be shown, on `calls`, `reads` and `writes` edges,
dropping dynamic edges wider than `k` (section 7.4) and test and generated
definitions. It starts at the entry point's handler and walks depth first.
Under each line are listed, in this order: every call that leads to changed
code (a node from which a `nodes.added`, `removed`, `signature_changed` or
`body_changed` symbol is reachable, the changed nodes included) or is itself
an added or removed edge; then the sinks it reads or writes; then up to four
other calls as context, with the rest folded into one "+N more" line that
names them. Only the calls that lead to changed code are expanded; under a
context line its own calls are counted, not listed. A definition already
expanded in the stack is listed again where it is called but not expanded
again. Depth is capped at twelve and a stack at forty lines, and a cut stack
says so.

**Each line** has a marker column, `+` added, `-` removed, `~` signature
changed, blank for unchanged, taken from the node, or from the edge into it
when the node itself did not change (a new call to old code is a `+` line
labelled "newly called"). `*` marks a body change as it does in the
diagrams. The line opens to the definition's signature (a two-line diff for
a changed signature), its `doc` (section 6.1) and a permalink. The doc is
quoted from the source, never written by the tool: section 3's line against
explaining the code holds.

**Markup.** Each line is its own `<details>` element, the tree drawn with box
characters inside the summary's `<code>`, spaces as `&nbsp;` so the columns
survive HTML whitespace rules. Nested lists or nested `<details>` would draw
the tree with less markup, but markdown inside HTML inside list items is the
part of GitLab's renderer least safe to rely on, and a hover tooltip is not
available at all in a note. The note already relies on `<details>`,
`<summary>`, `<code>` and markdown links, and the stacks use nothing else.
Stacks after the third go in one collapsed section.

## 11. Narration

Narration in v1 is a **deterministic template** over the delta. One
sentence template per fact class, in this order: entry points added,
removed and whose reach changed; signature changes and their caller
counts; renames; definitions added and removed; packages and module
wiring; body-only changes; suppressed packages. Each template takes IDs
from the delta, so every sentence is a claim the diagram also makes.

Each template is written twice from the same facts. As a **sentence**, it
is what the rewrite below is given and what its claims are checked
against. As a **list item**, it is what the note prints (section 14.1): a
bold count, by kind where it counts definitions ("3 functions and 1 type
added"), with the names nested under it one per line, up to eight and then
"and N more". Test code is counted apart and not named. A rewritten
sentence replaces its list item. The list form of a fact already on the
page is empty: an added endpoint's reach is nested under the endpoint, and
the suppression line stands at the top of the note.
This ships in phase 3, so the note is tested complete.

An **optional LLM rewrite** (phase 4) may replace the templated paragraph
with more fluent prose. Input: the templated sentences and the delta.
**Never the raw diff.** Output: at most eight sentences. It is accepted
only under validators enforced after generation, not by prompting:

- The model does not emit prose directly. It emits JSON claims, each
  `(subject_id, predicate, object_id, sentence)`, and the validator checks
  two things deterministically: the triple matches an edge, reach item,
  rename or signature change present in the delta, and the sentence
  contains both nodes' short names. Parsing free prose into triples would
  itself be an LLM task and would reintroduce the problem. A sentence that
  names two real nodes with the relation inverted fails the triple check;
  v0.1's "mentions a node" validator did not catch that.
- No evaluative language. A blocklist (`should`, `bug`, `risk`,
  `violates`, `incorrect`, `dangerous`) drops the sentence, with the
  knowledge that a blocklist is defeated by synonyms; the triple check
  above is the real guard, because an evaluation is not a delta fact.
- Any dropped sentence falls back to its templated original, so the
  paragraph is never shorter than the template.

Backend for the rewrite: any OpenAI-compatible endpoint, configured per
instance through the CI environment (`SHAPEDIFF_LLM_ENDPOINT`,
`SHAPEDIFF_LLM_MODEL`, `SHAPEDIFF_LLM_KEY`) and never through the repo
config: `.shapediff.yml` is part of the change under review, and a change
must not be able to name the server a CI secret is sent to, nor which
variable holds that secret. The endpoint must be https, or http on the CI
host itself. A local model on the CI host is acceptable; the input is
structured and small. The rewrite is offered the first eight template
sentences; any past the eighth stay as the template wrote them. Phase 4's gate is an A/B against the template: if
reviewers do not prefer the rewrite, the LLM, its endpoint and its
configuration never ship.

## 12. JS/TS extractor (phase 6)

Two candidates, chosen after Go is in production (or before, if section
5's open item reorders the phases):

- **scip-typescript** (Apache 2). Uses the tsc checker; resolved edges for
  TS and for JS with JSDoc types. Needs an adapter from SCIP occurrences to
  section 6 nodes and edges. Preferred if the target repos are mostly TS.
- **Graphify** `extract --code-only`. Tree-sitter plus name matching, so
  every call edge is `inferred`. Zero adapter effort beyond schema
  mapping. Acceptable for JS-heavy repos where tsc cannot resolve much
  anyway.

Entry-point patterns for express, fastify, NestJS decorators, Next
file-convention routes, and bullmq workers. File-convention routes make
the file path part of the entry point name, not the symbol.

The delta engine, filter, and renderer must not change for this phase. If
they do, the schema was wrong; fix the schema and the section 6.3 suite
with it. Either candidate ships as an external extractor under the
section 6.3 plug-in contract; the second language never enters the Go
binary.

## 13. Repo configuration

`.shapediff.yml` at the repo root, all fields optional:

```yaml
modules: ["."]                  # module roots for monorepos
build_tags: []
ignore: ["**/mocks/**", "**/testdata/**"]
entrypoints:
  worker:
    - call: "github.com/acme/jobs.Register"   # (name, handler) shape
sinks:
  http_client:
    - package: "github.com/acme/svc/internal/paymentsclient"
  db:
    - package: "github.com/acme/svc/internal/db"
components:                     # the period map's grouping, section 10.2; render-only
  - id: storage
    label: Storage layer
    packages: ["internal/storage", "internal/database/**"]
  - id: support
    label: shared libraries
    support: true               # counted, never drawn
    packages: ["internal/model", "internal/config"]
budget:
  chars: 1800                   # S; default is the instance limit measured in the spike
  nodes: 24                     # B
  edges: 36                     # E
narrow_targets: 2               # k, section 7.4
narration: template             # template | llm | off
baseline: on                    # on | off; the "Where this lands" section, 10.1
```

The loaded config, with defaults applied, is hashed into
`extractor.config_hash` and so into the cache key. A key the config does
not have is a load error that names the key and the closest one it does
have, so a misspelled key cannot pass for an omitted one; `components` is
not implemented yet and is rejected like any other unknown key until it
is. An `llm` section is accepted for compatibility and ignored with a
warning: the rewrite backend is configured by the environment (section 11).

`components` is the one key left out of `extractor.config_hash`. It changes
nothing in a graph, it will be edited often while a team settles its map,
and every edit would otherwise invalidate every cached graph for the repo
(one extraction on the spike repo took 110 s and 3.2 GB). The v0.4 keys
(`baseline`, `weekly_url`) stay in the hash: they are set once, so the cost
is one re-extraction, and one hashing rule was simpler. That trade does not
hold for a key that is edited repeatedly.

`weekly_url` is the address of the weekly structure brief (section 14.2). It
is optional and purely a pointer: when set, the per-MR note ends with one
line offering it to a reader who is behind on the repo. It takes part in
`extractor.config_hash` like every other key, so setting or changing it
invalidates that repo's cached graphs once. It is marshalled `omitempty`, so
a repo that does not set it keeps the hash it had.

## 14. GitLab integration

### 14.1 Per MR

One CI job in the MR pipeline (a shared CI template, included by each
repo), run only when `CI_PIPELINE_SOURCE == "merge_request_event"`, which
is the only pipeline source where `CI_MERGE_REQUEST_DIFF_BASE_SHA` exists.
Prerequisite: the project must have merge request pipelines enabled;
branch-only pipelines never fire that event. Steps:

1. Resolve the head SHA as `CI_MERGE_REQUEST_SOURCE_BRANCH_SHA` when
   non-empty, else `CI_COMMIT_SHA`. In merged-results pipelines
   `CI_COMMIT_SHA` is an ephemeral merge-result commit, never the branch
   head, and a graph must never be cached under one.
2. Fetch the head graph from the cache; extract if absent. **Cache key**:
   `(repo, sha, extractor.version, toolchain, builder_mode,
   config_hash)`. v0.1 keyed on repo and SHA alone, which would have made
   every extractor upgrade, config edit or Go bump diff a new-format head
   against an old-format base. Cache lives in the GitLab generic package
   registry or an S3 bucket.
3. Fetch or extract the base graph (`CI_MERGE_REQUEST_DIFF_BASE_SHA`). The
   job needs `GIT_DEPTH: 0`, because the default shallow clone usually
   lacks the base SHA and breaks both extraction and `git diff -M`. The
   base is frequently cached because several MRs branch from one `main`
   tip; v0.1's reason ("the base is the head of a previous MR") is only
   true under fast-forward merges and is not relied on. **Open:** record
   the target instance's merge method and measure the hit rate in phase 3.
4. Delta, filter, render, narrate. **Always** publish the delta document
   as a job artifact (`shapediff/delta.json`) and write it to the cache
   under `(repo, mr_iid, head_sha)` for the weekly job (section 8.3). The
   artifact is what lets a reader, or an agent, tell "nothing structural"
   from "the job did not run".
5. **Post a note only when `stats.structural` is true.** One MR note
   carrying a marker (`<!-- shapediff:v1 -->`), updated on every push,
   never duplicated. If a note already exists and a later push removed all
   structural change, update it to the one-line "no structural change at
   <sha>" rather than deleting it. Never post on first push for a
   non-structural MR: a marker that is usually empty teaches reviewers to
   stop opening it, and then depresses phase 3's own metric.
6. The token is a project access token stored as a masked CI variable. No
   API scope is narrower than `api` for the Notes API, so the token is
   bounded by **role** instead: Reporter can post notes and cannot push or
   edit CI configuration.

Note layout, top to bottom:

- **What changed**: the suppression line from section 8.4 when there is
  one; then the narration of section 11 in its list form, one item per
  fact class, each a bold count with what it counts named under it, one
  per line; then one line naming which of the four standing counts
  (endpoints added, endpoints' reach changed, signatures changed, packages
  added) are zero: `No endpoint's reach changed and no package was
  added.` A zero is news to a reviewer, so it is said, but in one line and
  not as four list items. With `narration: off` the counts stay and the
  names under them go.
- "Where this lands" (section 10.1): the size line, the touched-package
  table and the package map. Before the diagrams, because it is the frame
  they are read in; after the narration, because the reviewer who knows
  the repo reads the summary first and skips the frame.
- Dataflow diagrams (if any entrypoint changed), one per entry point, each
  with its sentence, with the kept reach in grey (section 10.1).
- Call-path diagram, with its paragraph (section 10).
- Call stacks (section 10.3), when an entry point reaches changed code.
- Module wiring diagram (if any).
- `<details>`: full node table, each node linked to its line, renames, body-only changes,
  wide-reach changes, suppressed packages, warnings, extractor version,
  timing.

Failure mode: if extraction fails outright on either SHA, post the warning
in one line (only if a note already exists; otherwise the artifact carries
it) and exit 0. The job must never block a pipeline.

### 14.2 Weekly

The brief also runs against one local checkout, with no repos file, no
GitLab and no wiki: `shapediff weekly --repo <dir> [--extractor <exe>]
[--branch <name>] [--week YYYY-MM-DD|YYYY-Www]` prints the same markdown. That is the
form the catching-up reader of section 2 can run for themselves, and it
takes the plug-in extractor of section 6.3, so it is not limited to Go. It
uses a fresh cache directory per run: the cache key of section 14.1 covers
the extractor version and toolchain, and neither is known before the first
extraction when the extractor is a plug-in.

A scheduled pipeline in a central "structure" project, Monday 06:00,
**enabled only after phase 3 has passed on at least one repo**:

- For each configured repo: weekly delta (section 8.3), attribution from
  cached per-MR deltas, pinned-layout diagram with the resolved SHAs
  printed.
- Cross-repo roll-up: a table of repos by structural churn (entrypoints
  changed, packages added, import edges changed), each row linking to the
  repo's section and to the week's merged MR list.
- Published as a wiki page `Structure/Week-YYYY-WW` in that project and
  linked from an issue for discussion. **Alternative:** GitLab Pages site;
  chosen against for v1 because the wiki needs no build step.

The weekly view shares the extractor and the delta engine with the per-MR
tool and nothing else: different reader, cadence, renderer and surface, and
it is the only stateful component (section 16). It stays in this spec so
the schema is built for both cadences, but it is built last and it is the
first thing to cut.

### 14.3 The period brief (v0.5)

The weekly job generalises to any period (section 8.3) and gains the map of
section 10.2:

- `weekly` takes `--period week|month|quarter`, or `--since` and `--until`,
  alongside `--week`, which stays as `--period week` for that date. Titles
  follow the period: `Structure/Week-2026-39`, `Structure/2026-09`,
  `Structure/2026-Q3`. The scheduled pipeline runs each configured period
  when it closes: Monday for the week, the 1st for the month, and the 1st
  of January, April, July and October for the quarter.
- `--format md|html`. `md` is the wiki page of 14.2, unchanged. `html` is
  one self-contained file: the map, then the page's tables.
- **Publishing.** Each period's HTML goes to the generic package registry
  that already holds the cache (section 14.1), under
  `shapediff-pages/<repo>/<period>.html`. A `pages` job assembles `public/`
  from the registry with an index per repo, and the wiki page links to its
  period's file. 14.2 chose the wiki over Pages because the wiki needs no
  build step. A static file needs none either, and GitLab does not render an
  uploaded HTML file as a page, so the wiki cannot host the map itself.
  Assembling from the registry keeps the `pages` job stateless: a Pages
  deploy replaces the whole site, and the registry is where the history
  lives.
- **Retention.** Per-MR delta cache entries must outlive the longest
  configured period, a quarter plus the job's lag. shapediff sets no
  expiry on registry entries; a `dir:` cache must not be pruned sooner.
- Local: `weekly --repo <dir> --period quarter --format html -o q3.html`,
  building on v0.4.2's local mode. This is the form a catching-up reader
  runs for themselves.

## 15. Phases and gates

| Phase | Deliverable | Gate to proceed |
|---|---|---|
| 0. Spike (1 to 2 days) | Run `golang.org/x/tools/cmd/callgraph -algo=vta` at the base and head of five real, recently merged MRs on the pilot repo (typed edges, no adapter, and it measures VTA wall time on a real module). Hand-write the delta and the Mermaid render. Post each as a comment on the merged MR, or hand it to the MR's reviewer. | **Judged by the reviewers of those MRs, not by the author.** Do they say it answered "what changed structurally" faster than the diff? Also: measure the instance's Mermaid character limit and record VTA wall time and peak memory. If the diagrams fail, rethink the filter; the edges are typed, so the extractor is not the suspect. |
| 1. Go extractor | `shapediff extract` emitting schema v1 with entrypoints, sinks, `load_status`, `targets`, both hashes. Golden tests on a fixture repo covering interfaces, generics (including VTA precision on generic bodies), generated code, `init` functions, closures, wrappers, test packages, chi and grpc routes. Go fixture pairs for every section 6.3 scenario. | Passes the section 6.3 document invariants and Tier A on every Go fixture. Deterministic across two runs on the same SHA (byte-identical; canonical order is an invariant). Wall time and peak RSS on the pilot repo under a stated ceiling (set from phase 0's measurement). |
| 2. Delta engine | `shapediff delta` with hierarchical renames, narrow/wide reachability and load-status suppression. Golden tests including a pure symbol rename, a type rename, a package move and a route rename (each zero edge changes), and a broken-at-head package (zero removals), expressed as section 6.3 Tier B assertions over the phase 1 fixtures. **Addendum:** render the actual delta of ten already-merged agent-authored MRs next to what their plans claimed, and show the pair to the team. | Golden tests pass. The addendum is not a gate; it is the free test of whether phase 7 has a buyer. |
| 3. Render and post | Filter, Mermaid, templated narration, permalinks, MR note, delta artifact, CI template. Deployed to **one** repo, chosen by agent-MR volume. | Runs until at least 15 structural MRs have received a note (extend the window as needed, expect more than two weeks). Denominator is **structural MRs only**. Signals, pre-registered before the phase starts with a pass mark: (a) fraction of those MRs where a reviewer comment references the note or a node named only in it; (b) an award emoji on the note, requested in the note's footer, counted via the API; (c) a five-question reviewer survey with the questions and pass mark written down before deployment. |
| 4. Narration rewrite | Optional LLM pass with the triple and blocklist validators, behind the template. | A/B on twenty notes: reviewers prefer the rewrite, and zero evaluative sentences survive. Otherwise the template stays and the LLM never ships. |
| 5. Weekly | Weekly pipeline, pinned layout, wiki page, roll-up, attribution from cached deltas. Starts only after phase 3 passes. | Two consecutive weeks each end in a **traceable decision**: a task filed, a refactor scheduled, or an explicit recorded "nothing to do". Attendance at a meeting is not the gate. |
| 5b. Period map (v0.5) | Section 10.2 map, section 14.3 periods and HTML, the two section 7.3 rules (wrapped handlers, `loop`), `components:` for the pilot repo. Starts after phase 5's first passing week. | On the pilot repo, a developer who did not work in it during the period answers three questions about where data goes, written down before they see the map, and someone who knows the code marks the answers. A maintainer reads the coverage line and names no main path it leaves out. |
| 6. JS/TS | Second extractor behind the same schema, driven through the section 6.3 plug-in contract, with its own fixture pair or an explained `n/a` for every scenario. | Passes the section 6.3 suite. Delta engine and renderer unchanged. |
| 7. Plan query | Given a plan's named symbols, query the **base graph**: who calls them, what they reach, which entrypoints they sit under, rendered with the same filter and renderer. | Used on ten agent-authored plans that actually name symbols. If the plans do not name symbols, this phase is a plan-format project, not this tool's. |

Phase 3's gate is the real one. If reviewers do not reference the note,
nothing after it is worth building.

Phase 7 in v0.1 was "synthesize a hypothetical head graph and render the
delta". Replaced: a synthesized head cannot type-check, and the type checker
is the whole precision story. A query over the real base graph answers the
same question ("what would this plan touch?") exactly.

## 16. Tool shape

- A single Go module producing one static binary `shapediff` with
  subcommands `extract`, `delta`, `render`, `post`, `weekly`, `query`,
  `conform`. Go is chosen because the Go extractor needs `x/tools`
  in-process and a static binary is the least friction in GitLab CI.
- Only the Go extractor is built in. `extract --extractor <exe>` runs any
  executable that meets the section 6.3 plug-in contract; `conform` checks
  a graph document or drives an extractor over the fixture scenarios.
- Licence: Apache 2.0. All dependencies are Apache/MIT/BSD.
- No server component in v1. State is the graph cache, the per-MR delta
  cache and the weekly layout positions, all files in object storage.

- The period map (section 10.2) adds flags to `weekly`, not a subcommand:
  `--period`, `--since`, `--until`, `--format`. `dot` becomes required for
  `--format html`, with Mermaid as the fallback where it is missing.

## 17. Risks

| Risk | Mitigation |
|---|---|
| Reviewers ignore the note | Phase 3 gate with a pre-registered pass mark; no note at all on non-structural MRs, so the marker never habituates to "empty" |
| Wide-interface fan-out makes every entrypoint reach everything | VTA; `targets` on every dynamic edge; narrow-versus-wide reach; package collapse; per-entrypoint split |
| A broken head renders as removals | `load_status` per package; suppression rule in section 8.4; golden test |
| Extraction time and memory on large modules | Per-SHA cache; `go build` cache in CI; phase 1 gates on wall time and RSS measured in phase 0 |
| False renames | Hierarchical passes; 1:1 fingerprint uniqueness and token floor; medium confidence shown with a "?" badge |
| GitLab Mermaid size limit | Measured in spike; character budget is primary; graphviz SVG fallback |
| Narration says something wrong | Template in v1; LLM rewrite only under the triple validator, falling back to the template sentence |
| Cache mixes extractor versions | Extractor version, toolchain, builder mode and config hash in the key |
| Schema too Go-shaped for TS | `confidence` field and file-convention entry names exist from v1; section 6.3 suite names every scenario language-neutrally and forces an explained `n/a`; phase 6 gate is "engine unchanged" |
| Wrong pilot language | Section 5 open item: record the language mix before phase 1 |
| The period map looks complete when entry detection is not | Coverage line above every map (section 10.2); the two section 7.3 rules; phase 5b gate has a maintainer read the coverage line |

## 18. Decisions on v0.1's open questions

1. **Budget.** Characters first, then nodes and edges. GitLab's limit is on
   source characters, so `S` is the budget that actually bites; `B = 24`
   and `E = 36` are derived guards, because legibility dies from edge
   crossings well before node count and a 30-node diagram with 80 edges is
   unreadable. All three are tuned from the real counts phase 0 produces.
2. **Body-only changes on a changed path.** Yes, as a `*` badge on nodes
   already drawn, never as an added node and never in amber. One bit per
   node tells the reviewer where on the path behaviour moved without
   expanding the focus set or re-importing the diff. Requires `body_hash`.
3. **Sink granularity.** Package-level by default; table names only from
   sqlc-generated method names where unambiguous, never from string
   literals, with `sink_name_source` saying which. One `internal/db`
   package would otherwise make every entrypoint reach one sink node.
4. **Weekly attribution.** Keep it, sourced from cached per-MR deltas
   keyed by node ID, and drop blame entirely. It is exact, it costs one
   cache read per MR, and it names MRs rather than people. Dropping
   attribution was considered (GitLab already answers "who touched it");
   rejected because "which MR moved this edge" is the question the weekly
   reader asks first, and the deltas answer it for free.
5. **Plan preview.** Not pulled forward, and not built as specified. A
   synthesized head graph cannot type-check. Phase 7 becomes a query over
   the base graph, and the phase 2 addendum (real deltas of merged
   agent-authored MRs beside their plans) tests the demand before any new
   code is written.

## 19. Still open

1. The problem's number (section 1): MRs per week, review latency, share
   agent-authored, on the pilot repo.
2. The language mix of the target repos and where agent-authored MRs land
   (section 5). Decides whether Go-first is right.
3. The target instance's Mermaid character limit (section 10). Sets `S`.
4. The target instance's merge method and the base-graph cache hit rate
   (section 14.1).
5. Whether the target repos use sqlc at all (section 7.3). If not, sink
   naming is package-level and question 3 closes free.
6. The period map's cut order (section 10.2), untested beyond one
   81k-line repo that needed no cutting.
7. How stable the period map's layout is across consecutive periods
   (section 10.2).
8. How precise the `loop` rule is (section 7.3): on the pilot repo, how
   many `go` statements reachable from main are long-running loops rather
   than fire-and-forget helpers.
9. Whether Pages is enabled on the target instance, and who can see a
   project's Pages site (section 14.3).

## 20. Changes from v0.1

Reviewed 2026-09-21 by three readers: a general reviewer, a Principal
Product Manager and a Principal Software Engineer, the latter two reading
independently. Each change names its source.

Taken:

- **Narration templated in v1, LLM optional behind a triple validator**
  (general, PM). v0.1's section 2 promised prose that could not disagree
  with the diagrams; section 11's "mentions a node" validator could not
  deliver that.
- **Hierarchical rename detection** (general), with **1:1 fingerprint
  uniqueness, a token floor, and a named similarity metric** (engineer).
- **Two body hashes** (engineer): `fingerprint` for renames, `body_hash`
  for `body_changed`; plus `fingerprint_recv` and `body_sketch` so the
  rename passes run on the graph alone (added in the consistency pass).
- **Node ID rules for `init`, closures, synthetic wrappers, generics and
  test packages** (engineer), and the correction that the ID encoding is
  shapediff's own, not `FullName()`.
- **`load_status` and the suppression rule** (engineer); the specific
  "head does not compile" message (general).
- **`targets` on dynamic edges and narrow-versus-wide reach** (general's
  observation that the dynamic bit was recorded but never used; the
  mechanism replaces the engineer's proposed static-only reach, see below).
- **Filter made total and terminating** (engineer): defined "touched",
  capped context in-degree, ID tiebreaks, groups-of-two collapse rule,
  exemptions in 4d, terminal top-`B` rule. **Callers of signature changes
  exempt from package collapse** (PM).
- **Character budget primary; subgraph/node ID collision rule** (engineer).
- **Cache key includes extractor version, toolchain, builder mode and
  config hash; fast-forward caveat on the base-cache claim** (general).
- **Spike uses `cmd/callgraph -algo=vta`** (general), **judged by the MRs'
  reviewers, not the author** (PM).
- **Post nothing on non-structural MRs; delta artifact always published**
  (PM, general).
- **Phase 3 metric: structural-MR denominator, minimum sample, pre-registered
  survey and pass mark, emoji signal** (PM).
- **Permalinks on every node** (PM).
- **Weekly attribution from cached per-MR deltas** (general), which
  required adding the per-MR delta cache to section 14.1.
- **`pin=true` instead of `neato -n`; `notranslate`; layout reset policy**
  (general, engineer). Verified against graphviz documentation: `-n`
  assumes every node has a `pos`.
- **CI facts** (engineer): `GIT_DEPTH: 0`, source-branch SHA in
  merged-results pipelines, `merge_request_event` gate, Reporter role.
- **Weekly SHAs printed; phase 5 conditional on phase 3; gate is a
  traceable decision** (engineer, PM).
- **Phase 7 replaced by a base-graph query; phase 2 addendum** (engineer,
  PM).
- **RTA rejection reason corrected** (engineer): it takes a slice of
  roots; the real objection is circularity with section 7.3.
- **VTA seeding stated correctly** (engineer's finding, adjusted): VTA
  seeds itself from CHA when given no initial graph, per the current
  `cmd/callgraph` source, so CHA is a component rather than a requirement.
- **Section 1 narrowed to "when the shape changes"; problem number and
  language mix recorded as open** (PM).
- **Route renames reported via shared handler** (general).

Considered and not taken:

- **Drop weekly attribution from v1** (PM). The per-MR deltas make it exact
  and free, and it names MRs, not people. Kept.
- **Split the weekly view into a separate document** (PM). It is a second
  product, and section 14.2 now says so, but the schema has to be built for
  both cadences and the owner's scope includes it. Kept, built last,
  conditional on phase 3.
- **Attribute at file granularity via `git log`** (engineer). Superseded by
  the cached-delta join, which is exact at symbol granularity.
- **Drive `reach_changed` from static edges only** (engineer). Rejected
  because idiomatic Go reaches its sinks through interfaces and a
  static-only walk would empty the dataflow view. Replaced by `targets`
  and the narrow/wide split, which addresses the same fan-out problem.
- **Amber outline for body-changed nodes on a path** (general, engineer).
  Taken as a badge instead (PM), since amber already means signature
  changed.

### Added in v0.5 (owner's request, 2026-09-24)

The ask: a weekly, monthly or quarterly view that explains how a system
moves data, the way archify's diagrams do, instead of only code-level
deltas. A spike (`docs/spike-period-map-2026-09-24.md`) built that view
for one quarter of miniflux from the existing graphs and delta, and fed it
to archify.

- **Section 10.2, the period map.** Components from a written mapping,
  entry groups, flows from narrow reach on both ends of the period, the
  period's change as tags, colours and cards, and a coverage line that
  says what the map cannot see. Rendered with `dot` into one
  self-contained HTML file; Mermaid is the fallback. archify was considered
  and rejected on the spike's evidence; its ideas are taken.
- **Section 8.3: any period**, not only a week. **Section 14.3**: periods,
  HTML output, publishing through the package registry to Pages, and
  cache retention.
- **Section 7.3: two detection rules**, for wrapped handlers and for
  background loops (`loop`, a new entry kind in section 6.1). They are
  specified, not implemented, and they come with two new section 6.3
  scenarios. The spike found both gaps by looking at the map. The rules
  gate phase 5b because a map missing its main pipeline is worse than no
  map.
- **Section 13: `components:`**, the first key outside
  `extractor.config_hash`, and why.
- Sections 15 (phase 5b), 16, 17 and 19 updated to match.

### Added in v0.4 (owner's request, 2026-09-22)

- **Section 10.1, the baseline view.** The ask: snapshot the whole repo
  and show the last baseline under the diff, so a reader who is not an
  expert on the repo can place the change. The snapshot already existed
  (the base graph, whole and cached per SHA); what did not was a rendering
  of it. Added: a "Where this lands" section in the note (size line,
  touched-package table with blast radius, entry points and sinks, and a
  directory-boxed package map with the MR's footprint marked), and the
  kept reach drawn beside the delta on the dataflow diagram. Both under
  the existing budget, both deterministic, both `baseline: off`-able.
  Section 2 gains this third reader implicitly; the phase 3 metric is
  unchanged and still measures the reviewer. Not done, and said so in
  10.1: pinned positions for the map (needs the SVG attachment path, not
  built) and any periodic repo-wide snapshot beyond the per-MR base SHA
  (that is the weekly cadence, section 14.2).
- **Mermaid preamble trimmed** (section 10): the `classDef` lines are
  emitted only for the classes a diagram uses. Six lines cost about a
  quarter of the default `S` on every diagram, whether or not a class
  appeared.

### Added in v0.3.1 (implementation, 2026-09-21)

- **Entrypoint `package` is the registration site; package-less nodes
  suppressed by adjacency** (sections 6.1 and 8.4). Found by the
  broken-head conformance scenario: a route registered in a package that
  stopped compiling leaked as a removal because the entry node had no
  package to be suppressed under. Recorded here because the schema is the
  contract a second language's extractor is written against.

### Added in v0.3 (owner's request, 2026-09-21)

- **Section 6.3, conformance suite and plug-in contract.** The question
  was whether the tool is tied to one backend. It was not, but the
  schema was descriptive rather than enforced: only the Go extractor was
  tested against it, and there was no stated way for a second backend to
  plug in beyond "emit the JSON". Now any extractor is an executable under
  one invocation contract, the document invariants are checkable on any
  graph, the fixture scenarios are named once and supplied per language
  with `n/a` requiring a reason, and phases 1 and 6 gate on the suite.
  Sections 12, 15, 16 and 17 updated to match.

### Added in v0.4.1 (legibility review, 2026-09-24)

Four readings of the rendered note on a real pilot repo, by the owner, found
that the diagrams were being judged unreadable for reasons the algorithm
could fix. The MRs were re-rendered before and after each change.

- **Orphan rule, section 9 step 4g.** The worst case measured drew 21 boxes
  and one arrow: the budget counted nodes and dropped edges as collateral,
  and nothing reconsidered a node once its last edge had gone. The same MR
  now draws 3 nodes and 1 edge, and the 20 definitions that left the diagram
  gained rows, with permalinks, in the details table — which is where they
  were legible in the first place.
- **Test and generated packages out of the module wiring, section 9 step 6.**
  Nine of twenty nodes in the measured wiring diagram were test modules,
  pulled in by a rule that step 1 already applies to the call paths. Same MR:
  20 nodes and 46 edges to 11 and 14.
- **The wiring diagram is budgeted.** It had been exempted by analogy with
  the dataflow diagram of step 5, whose exemption rests on "almost always
  small". That is true of dataflow and false of wiring: the measured diagram
  ran 46 edges against a cap of 36.

- **One key, above the first diagram, and it names the shapes** (section 10).
  The legend line was printed under the call-path diagram and nowhere else,
  so a reader of the dataflow diagram — the first one in the note, and often
  the only one they need — got four red arrows and nothing to say what red
  meant. The shapes were never written down at all, though a stadium, a
  double border and a cylinder carry as much as the colours do. It is printed
  once rather than per diagram: section 11 already warns that a marker
  reviewers meet on every MR is one they learn to skip.
- **Removed edges are dashed as well as red** (section 10). Hue alone does
  not separate them in a diagram with dozens of arrows, and removed *nodes*
  were already dashed.
- **Collapsed groups are named after their package** (section 9 step 4d).
  "17 symbols" takes the same space as "..." and says as much; it now reads
  "`reading` +17 more". "Symbol" is the schema's word, not a reviewer's, and
  is gone from everything the note prints.
- **Package labels that collide are widened** (section 10). Two packages
  called `pipelines` under different parents drew as two boxes with one name.
  Each drawn package now takes the shortest trailing path unique within its
  diagram. This is the same defect `implementation-notes.md` records for the weekly page's
  "Where change concentrated" table.

The theme of the four above is that both defects came from a rule stated for
one diagram and silently not inherited by another. The filter's steps are now
written as rules over diagrams rather than steps in one pipeline.

### Added in v0.4.2 (the catching-up reader, 2026-09-24)

The same review asked for a note that helps a developer who has not watched
the last ten MRs, not only one reviewing this MR. Section 2 now names that
reader and says where they are served, which is the weekly brief and not a
longer per-MR note — the orientation cost is paid once a week rather than
once an MR. Two things follow:

- **`weekly_url`** (section 13): when a repo has a published brief, the
  per-MR note ends with one line offering it.
- **`weekly --repo`** (section 14.2): the brief runs against a single local
  checkout with no repos file, no GitLab and no wiki, and takes the plug-in
  extractor of section 6.3, so the catching-up reader can produce it for
  themselves in any language shapediff can extract.

What was *not* done, deliberately: a note that explains the code or teaches
the reader something the delta does not contain. Section 3 rules that out and
section 11's validators enforce it, and that boundary is what keeps the note
something a reviewer can trust without checking.

### Added after v0.5 (owner's request, 2026-09-25)

- **The baseline map's legend is folded into the note's one key** (sections
  10 and 10.1). With both branches merged, the map's own legend line sat
  directly above the key, two keys in a row that disagreed on grey
  ("untouched" against "unchanged context") and on what a box is: the chrome
  v0.4.1 removed, back again. The key is now printed above the first diagram,
  which is the map when it is drawn, and adds purple, "an arrow is an import"
  and "an outer box a directory" only when the map is drawn, and amber, `*`
  and the call arrows only when a delta diagram is. What the map folded or
  hid to fit stays under the map as a caption, since it is about that
  diagram and not a key to it.

### Added after v0.5 (owner's review of a real note, 2026-09-26)

Read against the note for devlogplus !27, which adds two dismiss routes and
widens a list endpoint's filters:

- **One dataflow diagram per entry point** (section 9, step 5). Two new
  routes reaching the same four packages drew as one diagram of crossing
  edges. Each entry point now has its own small diagram and the sentence that
  reads it.
- **A paragraph under every diagram** (section 10), generated from the
  diagram, naming and linking what changed package by package and opening up
  "+N more" boxes, so the definitions a diagram draws are one click from the
  code.
- **Call stacks** (section 10.3, new): per entry point that reaches changed
  code, the calls from its handler down to the change, with the calls made
  alongside it as context and every line opening to a signature, doc line and
  permalink. The owner's example was an indented list with `+`/`-` lines;
  hover was asked for and is not possible in a GitLab note, so a line opens
  instead.
- **An optional `doc` attr** (section 6.1) on symbol nodes, from Go doc
  comments and Python docstrings. Optional so every existing extractor still
  conforms.
- **Step 4e splits by every entry point drawn** (section 9). `GET
  /pipelines/runs`, unchanged but calling two changed signatures, sat in the
  diagram titled "not reachable from any entrypoint".
- **The details table links the node name** (section 14.1) instead of
  spending a column on `file:line`; the separate column squeezed the names
  into a sliver that broke mid-word.

Second pass, same note, 2026-09-26. The owner kept the call stacks and the
details table and sent back the rest as a wall of text:

- **The key is a list** (section 10): one line per concern, colours as
  swatches. As a paragraph it read as a spec, not something to glance at.
- **Call-path text is a list** (section 10): one item per package and
  status, swatched like the boxes, where it had been a run of clauses.
- **Dataflow text says what the diagram cannot** (section 10). It had
  repeated the arrows in words. It now quotes what the entry point is for
  from a docstring, names its handler, and for each box says why it is
  reached: which definition reads or writes a table, which definitions the
  stack calls in a package.
- **A read and a write of one table are one stack line** (section 10.3),
  "reads and writes", not a read line and then the table "listed above".
- **The Python extractor sees unit-of-work writes.** Setting an attribute
  on a row fetched from the session and flushing wrote the table with no
  `add` or `update` call to see, so both dismiss functions were drawn as
  reads. A row is a variable bound to a session read, a result method chain
  on one, `list()`/`next()` of those, an index into them or a loop over
  them; a value merely built from rows, such as a response model, is not
  one.

Third pass, 2026-09-26, the owner's review of the notes for homechan,
devlogplus !27 and curriculumcompass !4. Everything that had to be read
across went down the page:

- **The headline and narration are one list** (sections 11 and 14.1). The
  headline was a line of four counts and the narration a paragraph naming
  what they counted, so a reader went across both to learn what "5
  endpoints added" were. Each fact class is now a bold count with the names
  under it, one per line; the zero counts are one line under the list.
- **Definitions added and removed are a fact class** (section 11). An MR
  that added a job handler, and nothing a route reaches, had a headline of
  zeros and one line about two edited bodies; the thirteen new definitions
  were not mentioned before the diagrams.
- **"symbols" is gone from the note** (sections 10.1 and 11). The reader
  did not know what it meant. Counts say what they count, by kind: "96
  functions, 40 methods and 17 types", "2 functions edited with the same
  signature".
- **The touched-package table is a list** (section 10.1), one item per
  package, naming what was added, removed, renamed or changed there rather
  than counting it. The six columns wrapped to a word a line at note width,
  and "3 added, 1 changed" did not say what was added.

### Added after v0.5 (UX review, 2026-09-26)

A review of every surface a person meets (the CLI, the MR note, the weekly
page) for places where a principle this spec already states for one surface
had not reached another. Fixes only; no new command, section or chrome.

- **The call-path fallback does not promise an attachment** (section 10).
  The note said "see the attached SVG", and nothing uploads one: the
  per-MR job has no attachment path yet. Until it does, the note says the
  diagram is not drawn and points at the list under it and the Details
  table, which name and link everything the diagram would have shown.
- **The suppression line names the side that failed** (section 8.4). It
  said "head does not compile" for every suppressed package, though a
  package is suppressed when either graph fails to load it, and a base
  that does not compile is not the author's to fix. Its lead is bold, like
  the counts under it, so it is not read as the first sentence of prose.
- **The Details summary says what it holds** (section 14.1): "Details: 7
  nodes, 1 rename, 1 warning". A bare "Details" put a medium-confidence
  rename and the extractor's warnings behind the same word as a table of
  links, so nothing told a reviewer there was a reason to open it. Only
  non-zero counts are named, in the order the section prints them.
- **The details table speaks the key's words** (sections 10 and 14.1). Its
  status column said `context` where the key and the call-path lists say
  "unchanged", so one grey box had two names; it now carries the key's
  words and swatches. An entry point's or a sink's package cell was empty,
  which read as missing data; it now says "entry point" or "db sink".
- **A rename says why it was matched, in words** (section 8.1). The
  details list printed `` `Old` → `New` ? (similarity) ``: a "?" the key
  does not explain, beside the name of an algorithm step. It now reads
  "(probably: similar body)", and "identical body", "same handler",
  "moved with its package" or "moved with its type" for the others,
  matching the summary list's "(probably)".
- **"Narrowly" and "entrypoint" are gone from the note** (sections 7.4 and
  9). "Changed code that no entry point reaches narrowly" and "dispatch
  sites with more targets than the narrow limit" used this spec's words,
  as "symbol" once did; they now say "except through a call with many
  possible targets". The remainder diagram of step 4e is titled "not
  reached from any entry point", two words as the rest of the note writes
  it.
- **The weekly page reads down, as the note does** (section 14.2). Each
  repo's headline was a one-row table of seven columns, mostly zeros,
  that wrapped at page width: the defect the third pass above removed
  from the note. It is now a list of the counts that are not zero. The
  week's SHAs were printed in full and unlinked; they are short and link
  to their commits. "Symbols changed", `reach_changed` and "(no cached
  MR delta)" are "Definitions changed", "reach changed" and "unknown: no
  MR recorded it", and an empty reach cell is "—". The cross-repo roll-up
  stays a table, because it is read down a column to compare repos.
