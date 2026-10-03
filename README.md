# shapediff

[![ci](https://github.com/Ullauri/shapediff/actions/workflows/ci.yml/badge.svg)](https://github.com/Ullauri/shapediff/actions/workflows/ci.yml)

Shows reviewers how a merge request or pull request changes the *shape* of a
codebase, before they read the diff.

A unified diff shows the lines that changed. It leaves out the callers that
did not change, and those callers are what a reviewer needs to see how the
change fits in. shapediff extracts a typed code graph (packages, functions,
types, entry points such as HTTP routes, and sinks such as database tables)
at the change's base and at its head. It subtracts the two graphs, trims the
difference to a size that fits in a comment, and posts one note on the merge
request or pull request. The note has Mermaid diagrams and a short summary.
The summary is filled in from templates, using the same delta the diagrams
are drawn from, so the two cannot disagree.

The note covers:

- **What changed**: the packages the change has code in and what each one is
  for, then what the change did to them. That includes which endpoints'
  reach changed, which signatures changed and how many callers each has,
  and which packages are new. Every definition expands to its signature
  (before and after), its doc, who calls it and a permalink.
- **Where this lands**: a map of the repo's packages, with the ones the
  change touches highlighted.
- **Dataflow, call paths and call stacks**: diagrams from each affected entry
  point down to the changed code, with the unchanged callers in grey for
  context.
- **Module wiring**: imports that were added or removed.
- **Details**: every drawn node with a permalink, plus renames, body-only
  changes and anything that was suppressed or hidden to fit the budget.

[`internal/render/testdata/note.golden.md`](internal/render/testdata/note.golden.md)
is a complete example note.

A second, weekly job turns a repo's changes over a week into a wiki page.
That page is for the reader who is catching up, not reviewing: where change
concentrated, and how the structure drifted.

> **Status: early.** shapediff runs end to end, and its conformance suite
> passes. It has not been tested against a real GitLab instance or across
> many repositories, and its graph format and note layout may still change.
> The design is written up in [`docs/shapediff-spec.md`](docs/shapediff-spec.md).

## Languages

| Language | Extractor | Precision |
|---|---|---|
| Go | built in (`go/packages`, SSA, VTA call graph) | typed |
| Python | [`extractors/python/shapediff-py`](extractors/python/shapediff-py), stdlib only, Python 3.10+ | inferred (syntactic, import-bound) |
| anything else | any executable under the plug-in contract below | yours to declare |

Go entry points recognised out of the box: `net/http` and chi routes, gRPC
servers, cobra commands, asynq and machinery workers, and `robfig/cron`
jobs. Sinks: sqlc tables, `database/sql`, and `net/http` clients. You can
add your own registration functions and client packages in the config. The Python
extractor maps FastAPI routes, MCP tools, SQLAlchemy tables and
httpx/requests/aiohttp calls.

**Plug-in contract (spec 6.3).** An extractor is any executable that takes
`<exe> --repo <dir> --config <path>`, writes one schema v1 graph document to
stdout and exits 0. Pass it with `--extractor` (or `SHAPEDIFF_EXTRACTOR` in
CI), and check it with `shapediff conform --fixtures conformance/<lang>
--extractor "<exe>"`. The conformance scenarios have language-neutral names,
so a new language only needs to provide its own `conformance/<lang>/`.

## Install

Requires Go 1.26 or later and `git`.

```
go install github.com/Ullauri/shapediff/cmd/shapediff@v0.2.0
```

Or from a clone:

```
make build      # bin/shapediff
```

Optional: graphviz (`dot`, `neato`) for the SVG fallback and the weekly
pinned layout. Without graphviz, shapediff writes the `.dot` source and a
warning. Python 3.10+ is needed only for the Python extractor.

## Try it locally

Compare any two commits of a Go repo. The base needs its own checkout, and
`git worktree` is the easiest way to get one:

```
git worktree add /tmp/base main          # or whatever the change branched from
shapediff extract --repo /tmp/base -o base.json
shapediff extract --repo .         -o head.json
shapediff delta  --base base.json --head head.json --repo . -o delta.json
shapediff render --delta delta.json --base base.json --head head.json -o note.md
```

`note.md` is the note shapediff would post. Paste it into any Markdown
viewer that renders Mermaid (GitHub and GitLab both do). Add `--project-url
https://github.com/<owner>/<repo>` to `render` to get permalinks.

For the weekly view of one checkout, with no GitLab and nothing published:

```
shapediff weekly --repo .               # last week; --week YYYY-Www for another
```

## Run it in CI

**GitLab.** Include `ci/shapediff.gitlab-ci.yml` from a repo's
`.gitlab-ci.yml`. `ci/weekly.gitlab-ci.yml` is the scheduled weekly job for a
central structure project. Each file lists the variables it needs. The
template expects two things on your GitLab instance: a copy of this repo it
can include from, and an image (`SHAPEDIFF_IMAGE`) that contains the
`shapediff` binary and the Go toolchain your repo builds with. No image is
published yet, so you build your own.

**GitHub.** Copy `ci/shapediff.github-workflow.yml` to
`.github/workflows/shapediff.yml`. When `post` sees `GITHUB_ACTIONS=true`,
it reads the pull request from `$GITHUB_EVENT_PATH` and posts one comment
with `GITHUB_TOKEN`. On every later push it edits that same comment instead
of adding a new one. Only the per-PR job runs on GitHub. The weekly job
needs GitLab, because it lists merged MRs and writes a wiki page.

Either way, the job never blocks a merge. `post` exits 0 on every runtime
failure, and it always publishes the delta as a job artifact, so a person or
an automated reviewer can tell "nothing structural" apart from "did not
run".

You can also run the GitHub job by hand against any pull request. Set the
variables Actions would set:

- `GITHUB_ACTIONS=true`
- `GITHUB_EVENT_NAME=pull_request`
- `GITHUB_EVENT_PATH`: a file holding `{"pull_request": <gh api repos/o/r/pulls/N>}`
- `GITHUB_REPOSITORY`
- `GITHUB_WORKSPACE`: a checkout of the head
- `SHAPEDIFF_TOKEN=$(gh auth token)`

Then run `shapediff post --dry-run` first. Without `--dry-run`, the comment
is posted as the user who owns the token.

## Configuration

Every field is optional. [`.shapediff.example.yml`](.shapediff.example.yml)
shows the defaults: module roots for monorepos, ignore globs, extra
entry-point and sink patterns, the diagram budget, and narration. Narration
is `template` by default. Setting it to `llm` sends the template sentences
through any OpenAI-compatible endpoint, and a validator rejects any sentence
that is not backed by the delta. Spec section 13 documents each field.

## Commands

```
shapediff extract  --repo . [--config .shapediff.yml] [--sha] [--repo-slug] [--extractor <exe>] -o graph.json
shapediff delta    --base base.json --head head.json [--repo .] [--mr-iid N] -o delta.json
shapediff render   --delta delta.json --head head.json --base base.json [--project-url URL] [--forge gitlab|github] [--diagram-only] -o note.md
shapediff post     [--dry-run] [--extractor <exe>]   # the whole per-MR job from CI variables (spec 14.1), GitLab or GitHub
shapediff weekly   --repos repos.yml | --repo <dir> [--dry-run] [--week YYYY-MM-DD|YYYY-Www]   # spec 14.2
shapediff query    --graph base.json --symbols Create,(*Service).Get [--project-url URL] [--forge gitlab|github]  # spec 18, decision 5
shapediff conform  --check graph.json | --fixtures conformance/go [--extractor "<exe>"] [--only a,b]
shapediff version
shapediff help     [command]              # a command's synopsis and flags; also `<command> -h`
```

## Development

```
make build      # bin/shapediff
make test       # unit tests
make conform    # the conformance suite over conformance/go, through the built binary
make check      # fmt, vet, test, conform (what CI runs)
```

`packages.Load` runs the `go` command, so anything that extracts needs the Go
toolchain on `PATH`. The conformance run extracts each fixture pair twice
and takes about a minute.

- [`docs/shapediff-spec.md`](docs/shapediff-spec.md) is the design. Every
  package implements a numbered section of it, and the plug-in contract and
  conformance suite in section 6.3 are what other extractors are written
  against. [`docs/shapediff-spec.v0.1.md`](docs/shapediff-spec.v0.1.md) is
  the first draft. Section 20 lists what changed since then and why.
- [`docs/implementation-notes.md`](docs/implementation-notes.md) maps each
  spec section to its package and records the decisions the spec left open.
  It also lists what is a seam rather than a payload (the JS/TS extractor,
  the live LLM call, an S3 cache) and the phase gates this build meets.
  Read it before changing the code.
- `spike/` and `docs/spike-*.md` are throwaway experiments. They sit behind
  a build tag and are not part of `./...`.

Releases follow `Version` in `cmd/shapediff/main.go`, the number that
`shapediff version` prints and that every graph and cache key records. Bump
it and tag in the same change.

## License

[MIT](LICENSE)
