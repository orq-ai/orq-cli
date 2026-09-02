# orq CLI: non-GET list table workaround

Date: 2026-09-02
Status: approved design; written spec awaiting review
Upstream: ENG-2942
Related: ENG-2855

## Problem

Bartolo v0.10.0 emits `FormatList` only for operations classified as lists, and its
classifier gates every signal behind an HTTP `GET` check. Read operations implemented as
`POST` therefore call the generic `Formatter.Format`, even when their successful response is
an ordinary collection envelope. At a terminal, `orq traces search` consequently prints TOON
such as `data[0]:` instead of entering Bartolo's table renderer. Passing `-o table` cannot
change that because the generated handler selected the wrong formatter method.

The generated tree cannot carry a downstream fix: `bartolo generate` replaces it. ENG-2942
tracks the generator change that will let explicit list metadata classify non-GET operations.
This design is the removable `cli/custom` workaround until an updated Bartolo is adopted.

## Scope

The workaround covers non-mutating POST operations whose response contains exactly one
top-level row collection that Bartolo's existing `FormatList` understands.

| Command | Row field | Default columns |
| --- | --- | --- |
| `documentation search` | `results` | `path`, `content` |
| `knowledge-bases list-chunks-paginated` | `data` | `_id`, `status`, `enabled`, `created` |
| `knowledge-bases search` | `matches` | `id`, `text` |
| `models azure-foundry-deployments` | `deployments` | `id`, `model`, `publisher`, `wire` |
| `reporting query` | `data` | `timestamp`, `dimensions`, `metrics` |
| `webhooks query` | `items` | `_id`, `display_name`, `enabled`, `failure_count` |
| `logs aggregate` | `buckets` | `timestamp`, `total_count`, `severity_counts` |
| `logs get-patterns` | `data` | `id`, `template`, `count`, `percentage` |
| `logs search` | `data` | `id`, `timestamp`, `severity_text`, `body` |
| `traces aggregate` | `data` | `group`, `metrics` |
| `traces search` | `data` | `trace_id`, `name`, `status`, `duration_ms` |
| `audit-logs query` (RC only) | `audit_logs` | `audit_log_id`, `created_at`, `action`, `actor_display` |
| `knowledge-bases preview-chunks` (RC only) | `chunks` | `page_number`, `text` |

The columns are schema fields rather than runtime guesses. They keep empty responses headed,
make nested aggregate maps visible instead of leaving a table with no inferable columns, and
pin a useful order until the same values move into `x-cli-list-fields` upstream.

The shared custom package runs against both generated trees. A configured command absent from
one tree is skipped, which is how the two RC-only entries coexist with the stable binary.

## Why OQL query commands are separate

`logs query` and `traces query-oql` are semantically list operations, but their wire shape is
nested:

```yaml
object: query
search:
  data: [...]
  has_more: false
```

Bartolo's row detector examines only the top-level object for an array. Redirecting these
commands to `FormatList` would still fall back to serialized output because `search` is an
object, not the row array. Extracting `search` in the CLI only for table output would require
knowing whether stdout is a real terminal; that state is private to Bartolo. Extracting it
unconditionally would break the stability contract by changing piped and explicit JSON output.

ENG-2942 therefore records these commands as needing a row-path facility such as
`x-cli-list-path: search.data`, or a flatter API response. They are not part of this workaround.
`logs get-context` is also excluded: it contains two peer collections, `before` and `after`,
and has no unambiguous single table.

Mutation and invocation responses are excluded even when their response has an array field.
The schemas contain 57 stable and 58 RC non-GET responses that satisfy Bartolo's broad
`isCollectionResponse` heuristic; most are create/update/run results containing fields such as
`skills`, `messages`, or `choices`, not list commands. The workaround is an explicit allowlist,
not response-shape inference.

## Design

Add `cli/custom/generated_list_workaround.go` with a declarative slice containing each command
path and its columns. `Register`, which already runs after generated registration, calls one
installer that resolves each path in the completed Cobra tree.

For every present command, the installer wraps its generated `RunE`. The wrapper snapshots the
current `bartolocli.Formatter`, replaces it for that command invocation with a small adapter,
defers restoration, and calls the original `RunE`. The swap happens after persistent pre-run
hooks, so it delegates to the final formatter selected by `--no-color` and terminal detection.
The CLI runs one command per process, so no concurrent command can observe the temporary value.

The adapter implements `ResponseFormatter.Format`, because that is the method the generated
non-list command calls. Its implementation asks the captured delegate for the same optional
`FormatList(data, columns...)` capability used by Bartolo's public `FormatList` helper. A
delegate with that capability receives the complete response envelope and the configured
columns. A custom delegate without it falls back to its ordinary `Format` method, preserving
Bartolo's compatibility behavior.

Passing the complete envelope is load-bearing. Bartolo's list formatter renders a table only
for a human at a terminal. For a pipe, `--json`, `--raw`, YAML, or explicit TOON, it serializes
the same complete API response it received. The workaround therefore changes presentation
only and leaves the machine contract untouched.

The wrapper is marked in the Cobra command's annotations so installing custom registration a
second time cannot stack adapters. Missing commands and commands without `RunE` are skipped;
tests prove every expected stable command resolves, while RC-only entries are exercised with a
minimal synthetic command tree and compiled again by the RC module checks.

Each function and configuration comment names ENG-2942 and the removal condition: delete the
workaround after both generated modules use a Bartolo release that emits `FormatList` for these
operations.

## Error handling

Formatter errors pass through the generated handler's existing `formatting failed` wrapping.
The adapter restoration is deferred before the generated handler runs, so request errors,
formatter errors, panics unwound by tests, and successful completion all restore the process
formatter. No warning is added: the user requested table output implicitly through the normal
default, and serialization fallbacks remain Bartolo's responsibility.

## Testing seams

The public seam is Cobra execution of the real registered `traces search` command against an
`httptest` server. This drives the generated HTTP client, the custom wrapper, and Bartolo's real
formatter together.

One vertical slice starts red with an empty trace response and expects table headers rather
than `data[0]:`. The implementation then makes it green. Follow-up slices cover a populated
row and `--json`, whose bytes must still decode to the full response envelope including paging
and metadata.

Focused wiring tests then cover:

- every stable allowlisted command resolves to a wrapped generated `RunE`;
- absent RC-only commands are harmless on the stable tree;
- synthetic RC-only command paths receive the same adapter;
- the original formatter is restored after success and after an error;
- a delegate without `FormatList` retains generic formatting.

No test edits generated files or contacts the live API.

## Documentation and removal

`CHANGELOG.md` gains a `**Fixed:**` entry under `## Unreleased`: POST-backed search, query,
aggregate, and preview commands now use terminal tables instead of TOON while machine formats
remain unchanged.

Removing the workaround after ENG-2942 ships consists of deleting its source and tests,
removing the `Register` call, and regenerating both modules with the Bartolo version carrying
explicit non-GET list classification and the corresponding `x-cli-list-fields` metadata.

## Out of scope

- Editing `cli/generated` or `packages/orq-rc/cli/generated` by hand.
- Automatically classifying every non-GET response containing an array.
- Flattening or changing JSON, YAML, TOON, raw, or piped output.
- Supporting the nested `logs query` and `traces query-oql` envelopes before Bartolo has a
  row-path contract.
- Rendering compound responses such as `logs get-context` as multiple tables.
- Turning bulk mutation results into list commands.
