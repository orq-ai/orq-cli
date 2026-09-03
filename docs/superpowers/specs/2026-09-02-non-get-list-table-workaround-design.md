# orq CLI: non-GET list table workaround

Date: 2026-09-02
Status: reviewed; ready for implementation planning
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

The workaround covers the explicit read/query/search/aggregate/preview POST operations below.
Each has one intended top-level row collection. It does not infer list semantics from response
shape: transformation, mutation, bulk-write, and invocation commands remain excluded even when
their responses happen to contain arrays.

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

The columns are schema fields rather than runtime guesses. Together with the configured row
field, they keep empty responses headed, make nested aggregate maps visible instead of leaving
a table with no inferable columns, and pin a useful order until the same values move into
`x-cli-list-fields` upstream.

The shared custom package runs against both generated trees. Every stable entry is required and
registration fails loudly if its path or generated `RunE` is missing; silent skipping would hide
schema or command-name drift. Only the two entries explicitly marked RC-only are optional in the
stable tree. The RC module build and tests prove those paths resolve in the staging tree.

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
object, not the row array. The CLI could build a terminal-only table view, as it does for the
nonconventional top-level keys below, but nested row selection is a separate contract with more
edge cases around JMESPath and envelope metadata. Extracting `search.data` unconditionally would
break the stability contract by changing piped and explicit JSON output.

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
path as Cobra path segments, its exact row field, its columns, and whether it is required in the
stable tree. `Register` calls the installer immediately after `registerCommands`, because those
custom registrations replace some generated commands. The installer resolves paths with
`root.Find(pathSegments)`; it does not derive paths from `Use` strings, which may contain argument
placeholders. It rejects a missing stable command or missing generated `RunE`, while an absent
RC-only command is allowed in the production-schema tree.

For every present command, the installer wraps its generated `RunE`. The wrapper snapshots the
current `bartolocli.Formatter`, rejects a nil snapshot as an initialization error, replaces it
for that command invocation with a small adapter, defers restoration, and calls the original
`RunE`. The swap happens after persistent pre-run hooks, so it delegates to the final formatter
selected by `--no-color` and terminal detection.

This package, Bartolo, Cobra, and Viper all use process-global command state and do not expose a
concurrent `Execute` contract. The workaround preserves that existing one-command-per-process
invariant; it does not make concurrent root execution safe. Tests that execute command roots or
change `bartolocli.Formatter` must not use `t.Parallel`. A mutex around only the allowlisted
commands would imply safety it cannot provide, because an unwrapped command could still read the
same global formatter concurrently.

The adapter implements `ResponseFormatter.Format`, because that is the method the generated
non-list command calls. Its implementation type-asserts the captured delegate directly for the
same optional `FormatList(data, columns...)` capability used by Bartolo's public helper. It must
never call the package-level `bartolocli.FormatList`: while the adapter is installed that helper
would dispatch through the global adapter and recurse. A custom delegate without `FormatList`
falls back to its ordinary `Format` method with the original envelope, preserving Bartolo's
compatibility behavior.

Bartolo recognizes empty envelopes only when their array is under one of six conventional keys.
Five allowlisted operations use `matches`, `deployments`, `buckets`, `audit_logs`, or `chunks`,
so merely selecting `FormatList` would still serialize an empty response. When, and only when,
the captured delegate is Bartolo's default formatter and the invocation is eligible for terminal
table output (`stdoutIsTerminal()`, no `--raw`, and resolved output format `table`), the adapter
builds a shallow table-view copy of the envelope. Bartolo probes conventional keys in the fixed
order `items`, `data`, `results`, `records`, `entries`, `servers`, so adding a `data` alias alone
does not make the configured field win over a peer `items` array. The copy therefore removes
other conventional object-row arrays that could win Bartolo's probe, retains the configured wire
row field, and then aliases that value under `data`. A configured field that is present with a
nil value is normalized to an empty object-row slice in the copy so the configured columns still
render; an absent configured field remains an error. The original envelope is never mutated.
This makes row selection deterministic without relying on map iteration or Bartolo's conventional
key precedence.

The shared terminal predicate matches Bartolo on Windows: stdout is interactive when either
`isatty.IsTerminal` or `isatty.IsCygwinTerminal` recognizes its file descriptor. Tests that
replace those process-global probes do not run in parallel.

Passing the untouched envelope outside that narrow table branch is load-bearing. For a pipe,
`--json`, `--raw`, YAML, explicit TOON, or a custom formatter, the delegate receives the exact
object returned by the API: no extracted rows and no added `data` alias. The workaround therefore
changes presentation only and leaves the machine contract untouched.

The wrapper is marked in the Cobra command's annotations so installing custom registration a
second time cannot stack adapters. Tests prove every expected stable command resolves; RC-only
absence is tested separately from RC-tree resolution so optional handling cannot mask drift.

The central source comment names ENG-2942 and the complete removal condition: both generated
modules must emit `FormatList` for all 13 operations, and generated `x-cli-list-fields` values
must replace every local column list in the same change.

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

The vertical slice explicitly installs a terminal-enabled Bartolo default formatter and captures
`bartolocli.Stdout`; an ordinary `go test` stdout is not a TTY and would otherwise exercise only
serialization. It starts red with an empty trace response and expects table headers rather than
`data[0]:`. Follow-up slices cover a populated row and `--json`, whose bytes must still decode to
the full response envelope including paging and metadata.

A second empty-response case uses a nonconventional key such as `matches` and proves both sides
of the table-view boundary: terminal table output has configured headers, while piped or explicit
JSON receives the original `matches` envelope with no synthetic `data` field. A multi-array
fixture drives Bartolo's real default formatter with a conflicting `items` peer and proves the
configured row field wins. A focused `deployments: null` fixture proves present nil rows normalize
to a headed empty table, while the existing missing-field test keeps absence as an error. The
shared terminal-predicate test covers native, Cygwin, and piped stdout without `t.Parallel`.

Focused wiring tests then cover:

- every stable allowlisted command resolves to a wrapped generated `RunE`;
- a missing stable path or `RunE` fails registration loudly;
- absent RC-only commands are harmless on the stable tree, while both resolve in the RC tree;
- the original formatter is restored after success and after an error;
- a delegate without `FormatList` retains generic formatting;
- a list-capable delegate is called directly without recursion;
- a nil formatter produces a clear initialization error rather than a nil dereference.

No test edits generated files or contacts the live API.

## Documentation and removal

`CHANGELOG.md` gains a `**Fixed:**` entry under `## Unreleased`: POST-backed search, query,
aggregate, and preview commands now use terminal tables instead of TOON while machine formats
remain unchanged.

Removing the workaround after ENG-2942 ships consists of deleting its source and tests,
removing the `Register` call, and regenerating both modules with the Bartolo version carrying
explicit non-GET list classification. Removal is not complete until all 13 operations also carry
the corresponding generated `x-cli-list-fields` values; only then are the local columns deleted
in the same change. The local lists are intentional compatibility duplicates and must not become
a second long-lived source of truth. Row-path metadata remains the separate removal boundary for
the nested OQL commands excluded from this workaround.

## Out of scope

- Editing `cli/generated` or `packages/orq-rc/cli/generated` by hand.
- Automatically classifying every non-GET response containing an array.
- Flattening or changing JSON, YAML, TOON, raw, or piped output.
- Supporting the nested `logs query` and `traces query-oql` envelopes before Bartolo has a
  row-path contract.
- Rendering compound responses such as `logs get-context` as multiple tables.
- Turning bulk mutation results into list commands.
