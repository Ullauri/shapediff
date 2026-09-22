# shapediff: structural change legibility for GitLab merge requests

Status: proposal, v0.1, 2026-09-20. Working name only.

This document is written to be reviewed. Every section that makes a design
choice says what the alternative was and why it lost. Sections marked
**Open** are decisions deferred to the spike or to the reviewer.

## 1. Problem

As more MRs are produced with agentic help, and some end to end by agents,
MR volume rises past the point where reviewers can hold the lines of code in
their heads. The team can still control *what* work gets done but loses the
ability to see *how* the system's shape is changing. The unified diff is the
wrong artifact for that: the callers that did not change are exactly what a
reviewer needs for orientation, and they are not in the diff at all.

## 2. Goal

Make structural change legible, fast, at two cadences.

- **Per MR.** Before reading a hunk, a reviewer sees what the MR did to the
  shape of the system: which entry points' reach changed, which signatures
  changed and who calls them, which packages appeared and where they were
  wired in.
- **Weekly.** One aggregate picture per repo, and a roll-up across repos,
  of where change concentrated and how the structure drifted over the week.
  Not a stack of per-MR diagrams.

A short prose summary accompanies the diagrams. The prose is generated from
the same structured delta the diagrams are drawn from, so they cannot
disagree.

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
  recall for edges.

## 4. Core architectural decision: graph diff, not text diff

Extract a code graph at the MR base SHA and at the head SHA. Subtract.
Render the delta. Narrate the delta.

Everything downstream depends on the graph being **deterministic** for a
given SHA and on node IDs being **stable across SHAs**. Any component that
is not deterministic (LLM output, unseeded community detection) is kept out
of the subtracted graph and applied only to the delta afterwards.

## 5. Scope and sequencing

Go first. JS/TS second. The normalized graph schema (section 6) is
language-neutral from day one so the second extractor does not force a
rewrite of the delta engine or renderer.

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
  "extractor": { "name": "go-xtools", "version": "0.1.0", "precision": "typed" },
  "nodes": [ /* Node */ ],
  "edges": [ /* Edge */ ]
}
```

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
    "sink_kind": null          // db | queue | http_client | cache | fs (sink nodes)
  },
  "fingerprint": "sha256 of normalized body tokens (identifiers kept, literals and whitespace normalized)"
}
```

ID conventions (stable across SHAs by construction):

- `pkg:<import path>`
- `sym:<import path>.<qualified name>` using Go's `types.Object` full name
  (receiver included for methods).
- `entry:<entry_kind>:<route or name>` e.g. `entry:http:POST /orders`,
  `entry:grpc:orders.OrderService/Create`, `entry:worker:process_refund`.
- `sink:<sink_kind>:<name>` e.g. `sink:db:orders`, `sink:queue:refunds`,
  `sink:http_client:payments`.

Line numbers and file paths are attributes, never part of the ID, so a
function that moves files is the same node.

### 6.2 Edge

```jsonc
{
  "from": "sym:...",
  "to": "sym:...",
  "kind": "calls",            // calls | imports | implements | handles | writes | reads
  "confidence": "resolved",   // resolved | inferred
  "attrs": {
    "dynamic": true,          // interface or func-value dispatch
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

**Alternative considered:** SCIP as the interchange format. It is precise
and has indexers for Go and TS, but it models occurrences and symbols, not
entry points, sinks, or fingerprints. It is a good *input* to an extractor,
not the interchange format. Revisit if maintaining two extractors becomes
the dominant cost.

## 7. Go extractor (`go-xtools`)

### 7.1 Why not Graphify or tree-sitter for Go

Verified against the Graphify repo on 2026-09-20: code edges come from
tree-sitter plus name matching (tagged EXTRACTED / INFERRED), no type
resolution, no documented hook for a custom extractor, and community labels
come from an LLM. For Go this discards the type checker, which is the one
thing that makes precise call edges cheap. Interface dispatch and same-named
methods across types would be wrong often enough to erode trust. Graphify
stays a candidate for the TS/JS extractor (section 12) and for the spike
(section 15, phase 0).

### 7.2 Mechanism

- Load with `golang.org/x/tools/go/packages` in `LoadAllSyntax` mode,
  pattern `./...`, build tags and env from repo config (section 13).
- Build SSA with `golang.org/x/tools/go/ssa/ssautil.AllPackages`.
- Call graph with `golang.org/x/tools/go/callgraph/vta` (variable type
  analysis). It resolves interface dispatch with good precision at
  acceptable cost. CHA over-approximates (every implementer of every
  interface), RTA needs a single `main` root which a multi-entry-point
  service does not have.
- Mark edges `dynamic: true` where the SSA call site is an invoke (interface
  method) or a call through a func value.
- Import graph from the loaded packages' `Imports` map, module-local
  packages only; external modules collapse to one node per module path.
- Fingerprint: hash of the function body's token stream with literals
  replaced by their kind and whitespace/comments removed.
- Files with a `// Code generated ... DO NOT EDIT.` header get
  `generated: true`. `_test.go` files get `test: true`. Both are extracted
  (they matter for rename detection and for counts) but hidden by default.

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

Sinks (produce a `sink` node and a `reads`/`writes` edge from the calling
symbol):

| Kind | Pattern |
|---|---|
| db | Calls into `database/sql`, `pgx`, `gorm`, `sqlc`-generated `Queries` methods, `ent`. Sink name is the table where recoverable from a sqlc method name or a string literal; otherwise the package. |
| queue | Publish/enqueue methods on configured client types (asynq, river, kafka-go, sarama, amqp, pubsub). |
| http_client | `net/http` `Client.Do/Get/Post`, and any configured client package (e.g. an internal `paymentsclient`). Name is the package. |
| cache | redis clients. |

Sink attribution is deliberately coarse. "Reaches the orders DB" is the
sentence a reviewer needs; column-level is out of scope.

### 7.4 Reachability

Not stored in the graph; computed by the delta engine from the graph. For
each entrypoint, BFS over `calls` edges from its handler, collecting the set
of reached packages and sinks. Dynamic edges are followed but the reached
set records whether each item was reached only via dynamic edges.

### 7.5 Known Go hazards

- Build tags: extract with a fixed tag set from repo config so both SHAs
  are comparable. Default: none.
- Generics: VTA handles instantiations; node ID uses the generic origin so
  instantiations do not multiply nodes.
- cgo: packages that fail to load are recorded in `extractor.warnings` and
  skipped; the MR note says so.
- Monorepos with many modules: config lists module roots; one graph per
  module, delta per module.
- Fan-out through very wide interfaces (e.g. a `Store` interface with 60
  methods and 3 implementations) can make every entrypoint reach
  everything. Mitigation is in the filter (section 9): collapse to package,
  mark dynamic, cap.

## 8. Delta engine

Input: base graph, head graph. Output: one delta document.

```jsonc
{
  "schema_version": 1,
  "repo": "...", "base_sha": "...", "head_sha": "...",
  "nodes": {
    "added": [ids], "removed": [ids],
    "signature_changed": [{ "id": "...", "before": "sig", "after": "sig" }],
    "body_changed": [ids],           // fingerprint differs, signature same
    "renamed": [{ "from": "id", "to": "id", "confidence": "high|medium" }]
  },
  "edges": { "added": [edges], "removed": [edges] },
  "entrypoints": [
    {
      "id": "entry:http:POST /orders",
      "status": "added|removed|reach_changed|unchanged",
      "reach_added": ["sink:queue:refunds", "pkg:.../internal/refunds"],
      "reach_removed": [],
      "changed_paths": [["entry:...", "sym:...", "sym:...", "sink:..."]]
    }
  ],
  "packages": {
    "added": [], "removed": [],
    "imports_added": [{ "from": "pkg:", "to": "pkg:" }], "imports_removed": []
  },
  "stats": { "entrypoints_added": 1, "entrypoints_reach_changed": 2, "symbols_added": 7, ... },
  "warnings": []
}
```

### 8.1 Rename detection

Refactor MRs are where structure matters most and where naive diffing
renders as delete-everything add-everything. Order of application:

1. Git's own file rename detection (`git diff -M --name-status base..head`)
   maps files. Because IDs do not include the file, symbol moves between
   files already match. This step exists only to seed step 3.
2. Exact fingerprint match between a removed and an added symbol:
   `renamed`, confidence high.
3. Same package (or git-renamed file), same symbol kind, signature equal
   after renaming the identifier, body token similarity >= 0.8: `renamed`,
   confidence medium.
4. Everything else stays added/removed.

A renamed node's edges are matched through the rename before edge deltas
are computed, so a pure rename yields zero added/removed edges.

### 8.2 Reachability delta

For each entrypoint present in either graph:

- Absent in base: `added`. Absent in head: `removed`.
- Reach set differs: `reach_changed`, with `reach_added` and
  `reach_removed` at the package-and-sink granularity.
- `changed_paths`: for each item in `reach_added`, one shortest path from
  the entrypoint that passes through at least one node in
  `nodes.added ∪ nodes.signature_changed ∪ endpoints of edges.added`. This
  is the path the diagram draws. Ties prefer paths with fewer dynamic
  edges.

Body-only changes (`body_changed` with no edge change) are listed in the
note's collapsed section but never drawn. They are what the diff is for.

### 8.3 Weekly delta

The weekly delta is **not** the sum of per-MR deltas. Per-MR deltas do not
compose (an edge added in MR 1 and removed in MR 3 cancels; merge order
matters). Instead:

- Diff `main@week_start` against `main@week_end` directly with the same
  engine.
- Attribute each changed node to MRs by walking `git log --merges
  week_start..week_end`, mapping merge commits to MR IIDs via the GitLab
  API, and joining on `git log -L` / blame for changed symbols. Attribution
  is best effort and shown as "touched by !123, !140".

## 9. Filtering (the per-MR legibility algorithm)

This is the part that decides whether anyone uses the tool. Stated as an
algorithm so it can be tested with golden files.

Inputs: delta, head graph, base graph, node budget `B` (default 30).

1. **Focus set F** = `nodes.added ∪ nodes.removed ∪ nodes.signature_changed
   ∪ endpoints(edges.added ∪ edges.removed)`, excluding `generated` and
   `test` nodes (test edges are counted and shown as a badge on the node,
   e.g. "3 tests").
2. **Context set C** = 1-hop callers and callees of F in the head graph
   (base graph for removed nodes). Context nodes are drawn grey.
3. **Collapse** any node in C whose package is not touched by the MR into
   its `pkg:` node. Merge edges accordingly with `sites` summed.
4. **Budget.** If `|F ∪ C| > B`:
   a. Drop `inferred` edges and any context node only reachable through
      them.
   b. Collapse context nodes into their package nodes, lowest degree first.
   c. Collapse focus nodes in the same package with identical
      added/removed status into one "N symbols" node.
   d. If still over budget, split into one diagram per changed entrypoint
      plus one "not reachable from any entrypoint" diagram, each budgeted
      separately, and put all but the first in a collapsed section.
5. **Dataflow diagram** is drawn separately and independently of budget:
   one row per changed entrypoint, showing `reach_added` and
   `reach_removed` only, at package-and-sink granularity. This diagram is
   almost always small.
6. **Module wiring diagram** is drawn only when `packages.added`,
   `packages.removed`, or `imports_added/removed` is non-empty. Shows
   touched packages and their imports, added edges green, removed red.

If the MR has no structural change at all (only `body_changed`), the note
says exactly that in one line and draws nothing. This case must be fast
and quiet; it will be the majority of MRs.

## 10. Rendering

Per-MR: Mermaid `flowchart LR`, since GitLab renders it natively in MR
notes and it needs no image hosting.

- Node styling by status: added green fill, removed red dashed border,
  signature-changed amber fill, context grey. Dynamic edges dashed.
  Inferred edges dotted. Legend line under the diagram.
- Node labels are short names (`Service.Create`), package shown as a
  `subgraph` box. Full IDs in a `<details>` table.
- Entry points are stadium shapes, sinks are cylinders.
- **Open:** GitLab enforces a size limit on rendered Mermaid. Confirm the
  current limit on the target instance during the spike. Fallback: render
  SVG with graphviz `dot` and upload as an MR attachment.

Weekly: Mermaid cannot keep layout stable across renders, which makes
week-over-week drift unreadable. Weekly package-level diagrams use
graphviz with **pinned positions**: positions for existing packages are
stored (in the tool's cache, keyed by repo) and passed with `neato -n`;
new packages are placed near their importers and their positions stored.
Only edge weight, colour, and node size change week to week.

## 11. Narration (LLM pass)

Input: the delta document and the filtered diagram node list. **Never the
raw diff.** Output: at most eight sentences.

Constraints enforced after generation, not by prompting alone:

- Every sentence must reference at least one node or edge ID present in
  the delta (matched by short name or ID). Sentences that reference
  nothing in the delta are dropped. This keeps the prose and the pictures
  two views of one thing.
- No evaluative language. A blocklist (`should`, `bug`, `risk`,
  `violates`, `incorrect`, `dangerous`) drops the sentence.
- Ordering instruction: entrypoint reach changes first, then signature
  changes and their callers, then module wiring, then a count of internal
  body changes.

Backend: any OpenAI-compatible endpoint, configured per instance. A local
model on the CI host is acceptable; the input is structured and small.
Narration is the only non-deterministic component and it is applied to
the delta, never to the graph.

## 12. JS/TS extractor (phase 6)

Two candidates, chosen after Go is in production:

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
they do, the schema was wrong; fix the schema.

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
node_budget: 30
narration: true
```

## 14. GitLab integration

### 14.1 Per MR

One CI job in the MR pipeline (a shared CI template, included by each
repo). Steps:

1. Fetch head graph from cache keyed by `(repo, sha)`; extract if absent.
   Cache lives in the GitLab generic package registry or an S3 bucket.
   Because the base of this MR is usually the head of a previous one, the
   base is almost always cached and each MR costs one extraction.
2. Fetch or extract base graph (`CI_MERGE_REQUEST_DIFF_BASE_SHA`).
3. Delta, filter, render, narrate.
4. Post or update **one** MR note carrying a marker
   (`<!-- shapediff:v1 -->`). Updated on every push, never duplicated.
   Uses a project access token with `api` scope stored as a masked CI
   variable.

Note layout, top to bottom:

- One headline line: `1 endpoint added, 2 endpoints' reach changed, 1
  signature changed (4 callers), 0 packages added.`
- Narration, up to eight sentences.
- Dataflow diagram (if any entrypoint changed).
- Call-path diagram.
- Module wiring diagram (if any).
- `<details>`: full node table, renames, body-only changes, warnings,
  extractor version, timing.

Failure mode: if extraction fails on either SHA, post the warning in one
line and exit 0. The job must never block a pipeline.

### 14.2 Weekly

A scheduled pipeline in a central "structure" project, Monday 06:00:

- For each configured repo: weekly delta (section 8.3), attribution,
  pinned-layout diagram.
- Cross-repo roll-up: a table of repos by structural churn (entrypoints
  changed, packages added, import edges changed), each row linking to the
  repo's section.
- Published as a wiki page `Structure/Week-YYYY-WW` in that project and
  linked from an issue for discussion. **Alternative:** GitLab Pages site;
  chosen against for v1 because the wiki needs no build step.

## 15. Phases and gates

| Phase | Deliverable | Gate to proceed |
|---|---|---|
| 0. Spike (1 to 2 days) | Graphify `--code-only` on one Go repo at the base and head of five real merged MRs. Hand-written delta and Mermaid render. | Looking at the five diagrams next to the five diffs, do they answer "what changed structurally" faster than reading the diff? If not, stop and rethink the filter, not the extractor. |
| 1. Go extractor | `shapediff extract` emitting schema v1 with entrypoints and sinks. Golden tests on a fixture repo covering interfaces, generics, generated code, chi and grpc routes. | Deterministic across two runs on the same SHA (byte-identical after sorting). |
| 2. Delta engine | `shapediff delta` with renames and reachability. Golden tests including a pure-rename MR that must yield zero edge changes. | Golden tests pass. |
| 3. Render and post | Filter, Mermaid, MR note, CI template. Deployed to **one** repo. | Two weeks of live MRs. Measure: fraction of MRs where a reviewer comment references the note or a node named only in it; a five-question survey of the reviewers. |
| 4. Narration | LLM pass with the reference and blocklist validators. | Manual read of twenty notes; zero evaluative sentences survive. |
| 5. Weekly | Weekly pipeline, pinned layout, wiki page, roll-up. | Two consecutive weeks reviewed in a team meeting. |
| 6. JS/TS | Second extractor behind the same schema. | Delta engine and renderer unchanged. |
| 7. Plan preview | Given a plan's named symbols and intended signature changes, synthesize a hypothetical head graph and render the same delta. | Used on ten agent-authored plans. |

Phase 3's gate is the real one. If reviewers do not reference the note,
nothing after it is worth building.

## 16. Tool shape

- A single Go module producing one static binary `shapediff` with
  subcommands `extract`, `delta`, `render`, `post`, `weekly`. Go is chosen
  because the extractor needs `x/tools` in-process and a static binary is
  the least friction in GitLab CI.
- Licence: Apache 2.0. All dependencies are Apache/MIT/BSD.
- No server component in v1. State is the graph cache and the weekly
  layout positions, both files in object storage.

## 17. Risks

| Risk | Mitigation |
|---|---|
| Reviewers ignore the note | Phase 3 gate; the note is one comment and quiet on non-structural MRs, so ignoring it is cheap |
| Wide-interface fan-out makes every entrypoint reach everything | VTA over CHA; dynamic edges marked; package collapse; per-entrypoint split |
| Extraction time on large modules | Per-SHA cache; `go build` cache in CI; parallel package loading |
| False renames | Two confidence levels, medium shown with a "?" badge |
| GitLab Mermaid size limit | Measured in spike; graphviz SVG fallback |
| Narration says something wrong | Reference validator drops unsupported sentences; narration can be switched off per repo |
| Schema too Go-shaped for TS | `confidence` field and file-convention entry names exist from v1; phase 6 gate is "engine unchanged" |

## 18. Open questions for review

1. Is `B = 30` the right default budget, and should the budget count
   edges as well as nodes?
2. Should body-only changes to a function that is on a changed
   entrypoint's path be drawn (amber outline) even though its signature and
   edges did not change? Argument for: the reviewer sees where on the path
   the behaviour moved. Argument against: it re-imports the diff.
3. Sink granularity: package-level only, or attempt table names from sqlc
   method names and string literals?
4. Weekly attribution via blame is approximate. Is "touched by !123,
   !140" useful enough, or should attribution be dropped from v1 of the
   weekly view?
5. Should the plan-preview mode (phase 7) be pulled forward, given that
   agent-authored plans are the input the team most wants to evaluate
   structurally?
