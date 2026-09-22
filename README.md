# shapediff

Structural change legibility for GitLab merge requests. Extracts a typed
code graph at an MR's base and head, subtracts them, filters the delta to a
budget, renders Mermaid into one MR note, and (weekly) rolls a repo's drift
up into a wiki page. This is the implementation of `shapediff-spec.md`
v0.3, which sits beside this directory.

## Build and test

```
make build      # bin/shapediff (needs Go 1.26; go.mod's toolchain line fetches it)
make test       # unit tests plus the conformance suite over conformance/go
make conform    # the suite alone, through the built binary
make check      # fmt, vet, test, conform
```

`packages.Load` shells out to `go`, so `/usr/local/go/bin` (or wherever the
toolchain lives) must be on `PATH` for anything that extracts. The Makefile
adds it. The conformance run extracts twelve fixture pairs twice each and
takes about a minute.

## Commands

```
shapediff extract  --repo . [--config .shapediff.yml] [--sha] [--repo-slug] [--extractor <exe>] -o graph.json
shapediff delta    --base base.json --head head.json [--repo .] [--mr-iid N] -o delta.json
shapediff render   --delta delta.json --head head.json --base base.json [--project-url URL] [--diagram-only] -o note.md
shapediff post     [--dry-run]            # the whole per-MR job from CI variables (spec 14.1)
shapediff weekly   --repos repos.yml [--dry-run] [--week YYYY-Www]   # spec 14.2
shapediff query    --graph base.json --symbols Create,(*Service).Get  # spec 18, decision 5
shapediff conform  --check graph.json | --fixtures conformance/go [--extractor "<exe>"] [--only a,b]
```

`extract --extractor <exe>` and `conform --extractor <exe>` run any
executable under the plug-in contract of spec 6.3: `<exe> --repo <dir>
--config <path>`, one schema v1 document on stdout, exit 0. The built-in Go
extractor satisfies it as `shapediff extract`, and the suite proves the seam
by driving the binary that way (`conform --extractor "bin/shapediff extract"`).

CI: include `ci/shapediff.gitlab-ci.yml` from a repo's `.gitlab-ci.yml`;
`ci/weekly.gitlab-ci.yml` is the scheduled job for the central structure
project. Both files document their variables.

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
| 10 weekly pinned layout | `internal/weekly` `Layout` | `pin=true`, `notranslate`, reset policy |
| 11 narration | `internal/narrate` | template, claim validator, optional OpenAI-compatible rewrite |
| 13 configuration | `internal/config` | defaults, `config_hash` |
| 14.1 per MR | `internal/pipeline`, `internal/gitlab`, `internal/cache` | orchestration with injected deps; notes, MR list, wiki, package registry; dir and registry caches |
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

## What is a seam, not a payload

- **Phase 6 (JS/TS)** is not built. The plug-in contract and the fixture
  manifest are; `internal/conform/plugin_test.go` passes a shell script
  through the suite, and the twelve scenarios are named language-neutrally
  so a second language supplies its own `conformance/<lang>/`.
- **Phase 4 (LLM rewrite)** has the claim validator and an
  OpenAI-compatible client; with `narration: llm` and no endpoint the
  template is used with a warning. Nothing makes a live call in tests.
- **Graphviz** is used for the per-MR SVG fallback and the weekly pinned
  layout. When `dot`/`neato` are absent the `.dot` source is written and a
  warning returned. Both paths are unexecuted on the machine this was
  built on, which has no graphviz.
- **S3 cache** is not built; `dir:<path>` and the GitLab generic package
  registry are.
- **Weekly SVG upload**: the page links the SVG the layout step wrote; the
  wiki attachment upload is not in the client.

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
