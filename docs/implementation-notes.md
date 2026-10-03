# Implementation notes

How this implementation maps onto [`shapediff-spec.md`](shapediff-spec.md),
what it decided where the spec left a choice open, and what is a seam rather
than a finished feature. Any change to the code should start from the
section-to-package map below: every package implements a numbered spec
section.

## Spec section to package

| Spec | Package | Notes |
|---|---|---|
| 6 schema, 6.1 node, 6.2 edge | `internal/graph` | types, the four ID grammars, canonical order, minhash sketch |
| 6.3 invariants | `internal/graph` `Validate` | one violation per line, stable order |
| 6.3 fixtures, plug-in contract | `internal/conform`, `conformance/go/` | manifest, `expect.yml` assertion vocabulary, `Exe` runner |
| 7 Go extractor (7.2 mechanism, 7.3 patterns, 7.5 hazards) | `internal/goextract` | go/packages, SSA `InstantiateGenerics`, VTA seeded from CHA; `patterns.go` is the 7.3 table |
| 7.4 reachability | `internal/delta` `Reach` | minimax width: widest dynamic edge on the narrowest path |
| 8 delta, 8.1 renames, 8.2 reach delta, 8.4 suppression | `internal/delta` | `types.go` is the document, `engine.go` the computation |
| 8.3 weekly delta and attribution | `internal/weekly` | joins cached per-MR deltas; MR list from the merge requests API |
| 9 filtering | `internal/filter` | steps 1 to 6 as written, every tie by ID |
| 10 rendering | `internal/render` | Mermaid, note assembly, graphviz dot fallback |
| 10.1 baseline view | `internal/filter` `BaselineView`, `internal/render` `writeBaseline` | the package map and touched-package list; the kept reach on the dataflow diagram is in `internal/filter` `dataflows` |
| 10.3 call stacks | `internal/filter` `stacks`, `internal/render` `writeStacks` | the tree over head ∪ base; the prose beside each diagram is `internal/render` `prose.go` |
| 10 weekly pinned layout | `internal/weekly` `Layout` | `pin=true`, `notranslate`, reset policy |
| 11 narration | `internal/narrate` | template, claim validator, optional OpenAI-compatible rewrite |
| 13 configuration | `internal/config` | defaults, `config_hash` |
| 14.1 per MR | `internal/pipeline`, `internal/gitlab`, `internal/cache` | orchestration with injected deps; notes, MR list, wiki, package registry; dir and registry caches |
| 14.1 on GitHub | `internal/pipeline` `githubEnv`, `internal/github`, `internal/forge` | the Actions environment and merge base; PR comments; permalink shapes and "MR"/"PR" |
| 14.2 weekly | `internal/weekly`, `cmd_weekly.go` | page and roll-up markdown |
| 16 tool shape | `cmd/shapediff` | one binary, seven subcommands, `wiring.go` is the glue |

## Decisions the spec left to the implementation

- **Package-less nodes and suppression (8.4).** Entry points carry the
  package of their registration site in `package` (not in the ID), so a
  route registered in a package that stopped compiling is suppressed with
  it. A sink, which has no package, is suppressed when every edge touching
  it comes from a suppressed package.
- **Structural when the head does not compile.** `stats.structural` is true
  whenever changes were hidden by suppression: a broken head cannot be
  called non-structural, and the headline says why.
- **Structural over the program only.** A change whose every added,
  removed, renamed or re-signed definition is a test or generated, and
  whose every edge change starts in such code, is not structural: the
  note never draws test or generated code, so there would be nothing to
  show. The "What it did" list counts test and generated definitions
  apart ("and 2 in tests and 7 generated") and names neither.
- **Type signatures.** Interfaces carry their method set as the signature,
  so widening an interface is a signature change with callers; structs
  carry `struct`, so adding a field is not. Type parameters are positional
  (`T0`, `T1`) in IDs and signatures.
- **Test variants.** With `Tests: true` a package loads twice; nodes are
  deduplicated by ID and the non-test node wins. An external test package
  (`p_test`) is its own package node with every symbol `test: true`.
- **External modules.** Import edges to other modules collapse to one
  `pkg:<module path>` node per module; the standard library is not drawn.
- **gRPC entry points** come from `Register<X>Server(s, impl)` calls; a
  method promoted from an embedded `Unimplemented<X>Server` is not an entry
  point.
- **cron entry points** are named by the handler, since a schedule string
  is not an identity.
- **sqlc tables** are read from the generated file's query constant, taken
  only when the query names exactly one table; every other database call
  is a package-level sink. Connection and transaction plumbing (`Open`,
  `Begin`, `Close`...) is never a sink.
- **Closures** are attributed to the enclosing declared function; a call
  *to* a closure is dropped (its own outgoing edges are on the parent).
  Anonymous functions stored in package-level variables are therefore
  invisible as callees.
- **Multiple modules** (`modules:` in config) are extracted separately and
  merged into one document; import paths keep the IDs disjoint.
- **Filter (section 9).** Nodes on a `changed_paths` route are always drawn
  and exempt from package collapse, so a route stays contiguous. Generated
  and test nodes are never taken as context, not only excluded from the
  focus set. The "never both a subgraph and a pkg node" rule is enforced
  as: a package with a collapsed node draws its remaining symbols bare.
  Mermaid identifiers are short hashes rather than sanitized names, so IDs
  do not eat the character budget; labels carry the names.
- **Baseline map (10.1).** A package is internal when a node lives under
  it; an external module collapses to a bare `pkg:` node and is counted,
  not drawn. Packages are boxed by parent directory relative to the common
  import-path root, so `internal/graph` and `cmd/graph` are `graph` in two
  boxes. An entry point with no recorded registration site is attached to
  its handler's package. "Reached from" in the table is narrow reach plus
  registration. The map uses a seventh colour (purple, `touched`) so it
  never reads as a signature change.
- **Orphan rule (section 9 step 4g).** A node with no drawn edge is not
  drawn; it keeps its row and permalink in the details table instead. The
  prune runs before the budget, after each collapse, and after the split and
  terminal rules, which strand nodes by keeping only the edges among the
  nodes they kept. A diagram left with no edges is not drawn at all.
- **One key per note (section 10).** The legend is printed once, above the
  first diagram, not under each; it names the node shapes as well as the
  colours. Removed edges are dashed as well as red. The baseline map has no
  legend of its own: the key covers it, and says only what the note draws
  (purple and imports when the map is drawn; amber, `*` and the call arrows
  when a delta diagram is). What the map folded or hid to fit is a caption
  under the map.
- **The top of the note is a list (sections 11, 14.1).** Each narration
  template is written twice from the same facts: a sentence, which the LLM
  rewrite is given and the claim validator checks, and a list item, a bold
  count with the names under it, which the note prints. The four standing
  counts that are zero share one line under the list. `narrate.Sentence`
  carries both forms; `cmd_render.go` picks one per sentence.
- **What it touches (sections 6.1, 14.1).** "What changed" opens with
  every package the change has code in, with what the package is for, and
  only then, under "What it did", says what the change did to them: a
  reader cannot weigh "`_stamp` added in `reading`" without knowing what
  `reading` is. The description is the package's own first sentence, which
  both extractors now record on the package node, cut at a word past 120
  characters. A package without one gets a line built from the graph,
  after an italic _no doc recorded; inferred:_ — the entry points
  registered or handled in it, the tables it reads or writes (else what it
  calls), who outside the tests imports it, what it defines; at most two.
  "No doc recorded", not "no docstring": a graph cached before package docs
  were extracted has none either. New and removed packages carry the key's
  🟩 and 🟥. Test packages are named on one closing line (the Python
  extractor flags them; a Go `_test` package is one whose every definition
  is a test), and more than eight code packages end in "also" and their
  names. Import-only changes and external modules are not listed. It is
  one list at the top, not a regrouping of the change list: "Where this
  lands" is the per-package view. Its labels and the change list's come
  from one `delta.PackageLabels` over every package with a changed
  definition; labelled a sentence at a time, `services/reading` read as
  `reading` wherever `routers/reading` was not in the same sentence. A
  doc is the author's text, not markup: it is HTML-escaped outside code
  spans, and `@name`, `#123` and `!123` are broken with a zero-width space
  so a docstring mentioning `@dataclass` pings nobody on every post.
- **Every definition in "What it did" opens (14.1, like 10.3).** An item
  that names a definition ("`_meta_title` in `reading`") is a collapsed
  row, the same disclosure the call stacks use. Opened, it shows the
  signature (a before/after diff when that changed), the definition's own
  first sentence, how it is reached, and a permalink. "How it is reached"
  is the route it handles, then who calls it by name, with the package
  when it is another one, and tests counted apart. For a new helper, that
  names the changed function that now depends on it. "Nothing calls it
  yet" is said only of new code. Existing code with no recorded caller is
  said to have none in the code, since a framework callback or a dynamic
  call is the usual reason, not dead code. The narration sentences carry
  the ID behind each item (`ItemIDs`) so the renderer can find it, and
  "and N more" under open-able rows is a plain line, not a bullet among
  disclosure triangles. Sentences about packages, imports and routes keep
  their bullets.
- **Counts name what they count.** `graph.DescribeKinds` turns counts per
  symbol kind into "3 functions and 1 type"; the note never says "symbols".
- **Colliding package labels are widened (section 10).** Each package drawn
  in a diagram takes the shortest trailing path unique within that diagram,
  so two packages called `pipelines` are not two boxes with one name. The
  weekly page's table still has the original form of this defect, below.
- **`weekly --repo <dir>` (section 14.2)** runs the brief against one local
  checkout with no repos file, no GitLab and no wiki, and takes
  `--extractor` for a plug-in backend. It uses a fresh cache directory per
  run, because the cache key covers a toolchain that is not known until the
  first extraction when the extractor is a plug-in.
- **Package registry cache** uses one package name (`shapediff`) with the
  cache key as the version, since the key already embeds the repo.
- **GitHub (14.1 on another forge).** The head is
  `pull_request.head.sha`, never `GITHUB_SHA`, which on a `pull_request`
  event is an ephemeral merge commit (GitLab's merged-results trap). The
  base is the merge base of `pull_request.base.sha` and the head, computed
  in the checkout, because `base.sha` is the base branch's tip and diffing
  against it would count everything merged there since the branch forked;
  GitLab's `CI_MERGE_REQUEST_DIFF_BASE_SHA` is already the merge base. When
  the merge base cannot be computed (a shallow clone) the tip is used and
  the job says so. Only `pull_request` runs the job, not
  `pull_request_target`. The note is the same document on both forges;
  `internal/forge` holds what differs: permalink shapes (`/blob/` for
  `/-/blob/`) and the change's name ("This PR", "package this PR changes").
  The forge comes from the CI environment in `post`, and from `--forge`,
  else the `--project-url` host, in `render` and `query`. The note is found
  again by the marker at the start of a comment, not anywhere in one, since
  a reply quoting the note carries it too, and only in a comment by the
  token's own user (`github-actions[bot]` for `GITHUB_TOKEN`, which cannot
  read `/user`; `SHAPEDIFF_COMMENT_AUTHOR` overrides), since anyone who can
  comment can type the marker. The GitLab side applies the same two rules,
  with the token's username from `/user`; when that cannot be read it says
  so and falls back to the marker alone. Neither client retries a POST: a
  lost response would otherwise post the note twice. When no merge base is found the fallback is
  a line on the job's log, not in the note.
- **GitHub's comment limit** is 65,536 characters; GitLab's note limit is
  far above any note. A longer note loses its Details section first, with a
  line pointing at the artifact, keeping the weekly link after it; only if
  that is not enough is it cut at a line boundary, with the code fences and
  `<details>` the cut left open closed again.
- **Plug-in extractors in `post`** key the graph cache on a hash of the
  extractor command and of every file it names, the interpreter included,
  in place of the Go toolchain: the Python extractor's output differs
  between interpreters, and a key that ignored that would hand one
  interpreter's graph to another. Anything the command loads indirectly is
  not in the key; clear the cache when that changes.
- **Weekly page, known cosmetic defect:** the "Where change concentrated"
  table groups by short name, so a sink (`orders`) can appear in the
  Package column.

## What is a seam, not a payload

- **Phase 6 (JS/TS)** is not built. The plug-in contract and the fixture
  manifest are; `internal/conform/plugin_test.go` passes a shell script
  through the suite, and the twelve scenarios are named language-neutrally
  so a second language supplies its own `conformance/<lang>/`.
- **Phase 4 (LLM rewrite)** has the claim validator and an
  OpenAI-compatible client. The endpoint, model and key come from
  `SHAPEDIFF_LLM_ENDPOINT`, `SHAPEDIFF_LLM_MODEL` and `SHAPEDIFF_LLM_KEY`
  in the CI environment, never from the repo config, which the change
  under review controls; with `narration: llm` and no endpoint the
  template is used with a warning. Nothing makes a live call in tests.
- **Graphviz** is used for the per-MR SVG fallback and the weekly pinned
  layout. When `dot`/`neato` are absent the `.dot` source is written and a
  warning returned. Both paths are unexecuted on the machine this was
  built on, which has no graphviz.
- **S3 cache** is not built; `dir:<path>` and the GitLab generic package
  registry are. On GitHub the directory cache persists through
  `actions/cache`, which scopes a pull request's cache to that pull
  request: later pushes reuse it, other pull requests do not.
- **Weekly SVG upload**: the page links the SVG the layout step wrote; the
  wiki attachment upload is not in the client.
- **Pinned positions for the baseline map** (10.1) are not built: Mermaid
  cannot pin, the map is boxed by directory instead, and a pinned map
  would need the MR attachment upload, which is the seam above.

## Phase gates this build meets

- Phase 1: `conformance/go` passes the 6.3 invariants and Tier A on every
  scenario; two extractions of one checkout are byte-identical (checked in
  the suite). Wall time and RSS on a real repo are not measured here.
- Phase 2: the four rename golden tests (symbol, type, package, route)
  yield zero edge changes and zero signature changes; the broken-head
  scenario yields zero removals. Similarity renames, ambiguous
  fingerprints, narrow versus wide reach and suppression counts are unit
  tested in `internal/delta`.
- Phase 3 onward need a GitLab instance and reviewers.
