# shapediff: structural change legibility for GitLab merge requests

Status: proposal, v0.3.1, 2026-09-21. Working name only. v0.1 (2026-09-20) is
kept beside this file as `shapediff-spec.v0.1.md`; section 20 lists what
changed and why, and what v0.3 added on top.

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
    "entry_kind": null,        // http | grpc | worker | cron | cli   (entrypoint nodes)
    "sink_kind": null,         // db | queue | http_client | cache | fs (sink nodes)
    "sink_name_source": null   // table | package   (sink nodes; section 7.3)
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
   confidence medium. Shown with a "?" badge.
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
- The headline says so in one line: "head does not compile:
  `internal/orders` (14 changes hidden)". Not a generic extraction failure,
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
      added/removed status into one "N symbols" node, **exempting** nodes
      on a `changed_paths` route and `signature_changed` nodes, which are
      the MR's subject.
   e. Split into one diagram per changed entrypoint plus one "not reachable
      from any entrypoint" diagram, each budgeted separately, and put all
      but the first in a collapsed section.
   f. **Terminal rule.** If a single diagram is still over budget, keep the
      top-`B` nodes by priority and the edges among them, and add one
      "+N more" node with a count. If the rendered source still exceeds
      `S` (long labels), lower `B` by 4 and repeat this step; below `B = 8`,
      fall back to the graphviz SVG attachment (section 10). This step
      always terminates the loop.
5. **Dataflow diagram** is drawn separately and independently of budget:
   one row per changed entrypoint, showing `reach_added` and
   `reach_removed` only, at package-and-sink granularity. This diagram is
   almost always small.
6. **Module wiring diagram** is drawn only when `packages.added`,
   `packages.removed`, or `imports_added/removed` is non-empty. Shows
   touched packages and their imports, added edges green, removed red.

If the MR has no structural change at all (`stats.structural` is false),
**nothing is drawn and no note is posted** (section 14.1). This case must
be fast and quiet; it will be the majority of MRs.

## 10. Rendering

Per-MR: Mermaid `flowchart LR`, since GitLab renders it natively in MR
notes and it needs no image hosting.

- Node styling by status: added green fill, removed red dashed border,
  signature-changed amber fill, context grey. Dynamic edges dashed.
  Inferred edges dotted. Legend line under the diagram.
- A node that is drawn because it lies on a `changed_paths` route and is
  also `body_changed` carries a `*` suffix on its label, meaning "body also
  changed here". It is a badge, not a colour; amber is taken.
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
  with graphviz `dot` and upload as an MR attachment.

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

## 11. Narration

Narration in v1 is a **deterministic template** over the delta. Eight
sentence templates, one per fact class, in this order: entrypoint reach
changes; signature changes and their caller counts; renames; module
wiring; count of body-only changes; suppressed packages. Each template
takes IDs from the delta, so every sentence is a claim the diagram also
makes. The headline line in section 14.1 is the first of these templates.
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
instance. A local model on the CI host is acceptable; the input is
structured and small. Phase 4's gate is an A/B against the template: if
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
budget:
  chars: 1800                   # S; default is the instance limit measured in the spike
  nodes: 24                     # B
  edges: 36                     # E
narrow_targets: 2               # k, section 7.4
narration: template             # template | llm | off
```

The loaded config, with defaults applied, is hashed into
`extractor.config_hash` and so into the cache key.

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

- One headline line: `1 endpoint added, 2 endpoints' reach changed, 1
  signature changed (4 callers), 0 packages added.` Followed, when
  applicable, by the suppression line from section 8.4.
- Narration, up to eight sentences.
- Dataflow diagram (if any entrypoint changed).
- Call-path diagram.
- Module wiring diagram (if any).
- `<details>`: full node table with permalinks, renames, body-only changes,
  wide-reach changes, suppressed packages, warnings, extractor version,
  timing.

Failure mode: if extraction fails outright on either SHA, post the warning
in one line (only if a note already exists; otherwise the artifact carries
it) and exit 0. The job must never block a pipeline.

### 14.2 Weekly

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

## 15. Phases and gates

| Phase | Deliverable | Gate to proceed |
|---|---|---|
| 0. Spike (1 to 2 days) | Run `golang.org/x/tools/cmd/callgraph -algo=vta` at the base and head of five real, recently merged MRs on the pilot repo (typed edges, no adapter, and it measures VTA wall time on a real module). Hand-write the delta and the Mermaid render. Post each as a comment on the merged MR, or hand it to the MR's reviewer. | **Judged by the reviewers of those MRs, not by the author.** Do they say it answered "what changed structurally" faster than the diff? Also: measure the instance's Mermaid character limit and record VTA wall time and peak memory. If the diagrams fail, rethink the filter; the edges are typed, so the extractor is not the suspect. |
| 1. Go extractor | `shapediff extract` emitting schema v1 with entrypoints, sinks, `load_status`, `targets`, both hashes. Golden tests on a fixture repo covering interfaces, generics (including VTA precision on generic bodies), generated code, `init` functions, closures, wrappers, test packages, chi and grpc routes. Go fixture pairs for every section 6.3 scenario. | Passes the section 6.3 document invariants and Tier A on every Go fixture. Deterministic across two runs on the same SHA (byte-identical; canonical order is an invariant). Wall time and peak RSS on the pilot repo under a stated ceiling (set from phase 0's measurement). |
| 2. Delta engine | `shapediff delta` with hierarchical renames, narrow/wide reachability and load-status suppression. Golden tests including a pure symbol rename, a type rename, a package move and a route rename (each zero edge changes), and a broken-at-head package (zero removals), expressed as section 6.3 Tier B assertions over the phase 1 fixtures. **Addendum:** render the actual delta of ten already-merged agent-authored MRs next to what their plans claimed, and show the pair to the team. | Golden tests pass. The addendum is not a gate; it is the free test of whether phase 7 has a buyer. |
| 3. Render and post | Filter, Mermaid, templated narration, permalinks, MR note, delta artifact, CI template. Deployed to **one** repo, chosen by agent-MR volume. | Runs until at least 15 structural MRs have received a note (extend the window as needed, expect more than two weeks). Denominator is **structural MRs only**. Signals, pre-registered before the phase starts with a pass mark: (a) fraction of those MRs where a reviewer comment references the note or a node named only in it; (b) an award emoji on the note, requested in the note's footer, counted via the API; (c) a five-question reviewer survey with the questions and pass mark written down before deployment. |
| 4. Narration rewrite | Optional LLM pass with the triple and blocklist validators, behind the template. | A/B on twenty notes: reviewers prefer the rewrite, and zero evaluative sentences survive. Otherwise the template stays and the LLM never ships. |
| 5. Weekly | Weekly pipeline, pinned layout, wiki page, roll-up, attribution from cached deltas. Starts only after phase 3 passes. | Two consecutive weeks each end in a **traceable decision**: a task filed, a refactor scheduled, or an explicit recorded "nothing to do". Attendance at a meeting is not the gate. |
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
