# Trace Thread View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `orq traces thread <trace-id> [span-id]`, which normalizes Chat Completions and Responses conversations from hydrated spans and renders readable Markdown or a canonical structured representation.

**Architecture:** Keep format-specific parsing and Markdown rendering in focused files under `cli/custom/commands`. Inject the three generated trace operations from each binary's own generated module so stable and rc use their matching schemas, then attach the hand-written command to the generated `traces` parent. Normalize untyped and partly wrapped attributes into one canonical `Thread`, slice that model, and render from it.

**Tech Stack:** Go 1.25, Cobra, Viper, Bartolo formatter, generated Orq trace operations, table-driven Go tests and JSON fixtures.

**Spec:** [Linear RES-1507](https://linear.app/orqai/issue/RES-1507/add-orq-traces-thread-to-render-normalized-conversations-from-traces)

## Global Constraints

- V1 recognizes exactly two conversation dialects: Chat Completions and OpenAI Responses. OpenTelemetry GenAI `gen_ai.input.messages` / `gen_ai.output.messages` normalization is out of scope.
- Observed Chat Completions payloads stored directly in `gen_ai.input` and `gen_ai.output` remain in scope; `gen_ai.output` may be a direct assistant object rather than a `choices[].message` envelope.
- Parse fields independently because spans may be partial or hybrid. Prefer usable `openresponses.*` content, then Chat Completions shapes, then legacy `span.input` / `span.output` fallbacks.
- Flexible values may be structured maps/arrays, JSON strings, or wrappers containing `_value`, `string`, and `items.count`. A count without content becomes `[content unavailable: N items]`; never infer missing text, tools, or reasoning.
- Never print encrypted, signature, or redacted reasoning blobs. Render only `[encrypted]`, `[redacted]`, `[masked]`, or `[truncated]` state markers as appropriate.
- Default output is Markdown on stdout when no machine format was explicitly requested. `--json`, `-o yaml`, and `-o toon` serialize the canonical selected structure. Errors go to stderr.
- `--slice` uses zero-based Python list semantics with an exclusive stop, omitted bounds, negative indices, and a single integer; strides and arbitrary non-contiguous selectors are out of scope. Instructions are not sliced.
- Markdown has indexed `USER`, `ASSISTANT`, and `TOOL` headings. Nested indicators are `INSTRUCTIONS`, `SYSTEM`, `DEVELOPER`, `REASONING`, `REASONING SUMMARY`, `TOOL CALL`, `TOOL RESULT`, `ERROR`, and `EXCEPTION` only when actual content exists.
- Do not edit `cli/generated/`. Changes under `cli/custom/` must compile for both the root stable module and `packages/orq-rc`.
- The implementation calls generated Go operations directly through injected functions; it does not invoke the `orq` subprocess or round-trip through `cat`/`jq`.
- Preserve source order, pair tool requests/results by call ID, and remove only a duplicated final assistant message that is identical across input history and output. Distinct repeated messages remain distinct.
- Keep all live-trace fixtures anonymized and synthetic. Do not commit trace, span, response, item, or call identifiers from production.

---

### Task 1: Canonical thread normalization, slicing, and Markdown rendering

**Files:**
- Create: `cli/custom/commands/thread.go`
- Create: `cli/custom/commands/thread_normalize.go`
- Create: `cli/custom/commands/thread_markdown.go`
- Create: `cli/custom/commands/thread_test.go`
- Create: `cli/custom/commands/testdata/thread/chat.json`
- Create: `cli/custom/commands/testdata/thread/responses.json`
- Create: `cli/custom/commands/testdata/thread/responses-unavailable.json`
- Create: `cli/custom/commands/testdata/thread/malformed-fallback.json`

**Interfaces:**
- Consumes: an untyped hydrated span response as `map[string]any` and a caller-supplied `ThreadSource`.
- Produces: `NormalizeThread(span map[string]any, source ThreadSource) (Thread, error)`, `SliceThread(thread Thread, expression string) (Thread, error)`, and `RenderThreadMarkdown(w io.Writer, thread Thread) error`.
- Produces canonical exported JSON fields: `instructions`, `messages`, and `source`; each message has `index`, `role`, optional `name`, `content`, optional `reasoning`, optional `tool_calls`, and optional `tool_call_id`.

- [ ] **Step 1: Define canonical types and write failing normalization tests**

  Define `Thread`, `ThreadSource`, `ThreadInstruction`, `ThreadMessage`, `ThreadPart`, and `ThreadToolCall` in `thread.go` with snake-case JSON tags and `omitempty` only for optional fields. Table tests must load each JSON fixture and assert the complete normalized value, including original zero-based indices, roles, multipart text, attached reasoning, tool-call names/arguments/call IDs, tool results, instructions, detected representations, typed unsupported placeholders, and unavailable-content markers.

- [ ] **Step 2: Run the focused tests and confirm they fail**

  Run `go test ./cli/custom/commands -run 'TestNormalizeThread' -count=1`. The expected failure is missing canonical types or `NormalizeThread`.

- [ ] **Step 3: Implement flexible lookup and Chat Completions normalization**

  In `thread_normalize.go`, implement literal dotted-key and nested-map lookup plus recursive decoding of structured values, JSON strings, and `_value`/`string`/`items.count` wrappers. Normalize Chat input arrays or `{messages:[...]}`, direct output messages, and `choices[].message`; support `system`, `developer`, `user`, `assistant`, and `tool`, string/multipart content, assistant `tool_calls`, `tool_call_id`, and recorded reasoning fields (`reasoning_content`, `reasoning`, `thinking`, summaries, and redacted variants). Convert system/developer messages to instructions. Append output after input and remove only an identical duplicate at the boundary.

- [ ] **Step 4: Implement Responses normalization**

  Normalize `openresponses.instructions`, `openresponses.input`, and `openresponses.output` item arrays. Handle `message`, `reasoning`, `function_call`, and `function_call_output` items; attach standalone reasoning to the following assistant/tool-call message when possible and otherwise create an assistant reasoning-only message. Parse JSON tool arguments when valid while retaining raw text when invalid. If a wrapper exposes only `items.count`, add one canonical unavailable part carrying the count.

- [ ] **Step 5: Implement Python-style slicing tests and logic**

  Add cases for `-5:`, `:10`, `2:4`, `-4:-1`, `3`, out-of-range bounds, empty results, whitespace, malformed integers, more than one colon, and stride syntax. `SliceThread` must clamp like Python, retain source indices, leave instructions/source untouched, and return a descriptive error for invalid syntax or strides.

- [ ] **Step 6: Implement Markdown golden tests and renderer**

  Assert full Markdown output for Chat text/tool-call/tool-result and Responses reasoning/unavailable fixtures. Render indexed `## USER [n]`, `## ASSISTANT [n]`, and `## TOOL [n] — name`; render instructions before messages; pretty-print structured values in fenced JSON; preserve plain text; render state and unsupported markers without encrypted/redacted payloads; and end output with a newline.

- [ ] **Step 7: Run focused tests and commit**

  Run `go test ./cli/custom/commands -run 'Test(NormalizeThread|SliceThread|RenderThreadMarkdown)' -count=1`, then `gofmt -w cli/custom/commands/thread*.go`, rerun the focused tests, and commit with `feat(traces): normalize and render trace threads`.

### Task 2: Trace API integration and command registration

**Files:**
- Create: `cli/custom/commands/traces_thread.go`
- Create: `cli/custom/commands/traces_thread_test.go`
- Modify: `cli/custom/register.go`
- Modify: `cli/custom/run.go`
- Modify: `cmd/orq/main.go`
- Modify: `packages/orq-rc/cmd/orq/main.go`
- Modify: affected custom registration/run tests when required by the injected dependency.

**Interfaces:**
- Consumes Task 1's `NormalizeThread`, `SliceThread`, `RenderThreadMarkdown`, and canonical types.
- Produces `TraceAPI` with `GetTrace(traceID string, params *viper.Viper) (map[string]any, error)`, `GetSpan(traceID, spanID string, params *viper.Viper) (map[string]any, error)`, and `ListSpans(traceID string, params *viper.Viper) (map[string]any, error)` function fields.
- Produces `NewTracesThreadCommand(api TraceAPI) *cobra.Command` and attaches it beneath the generated `traces` command.
- Stable `cmd/orq/main.go` wraps `orq/cli/generated` operations; rc `packages/orq-rc/cmd/orq/main.go` wraps `orq-rc/cli/generated` operations. Each wrapper discards only the HTTP response and returns the decoded map/error.

- [ ] **Step 1: Write failing command and selection tests**

  Build a fake `TraceAPI` that records calls and returns fixture maps. Cover exact-span selection, trace `leading_span_id` selection, fallback after a missing/non-conversational leading span, sorting fallback candidates by descending `started_at`, pagination using `next_page_token`, excluding evaluator spans and every descendant of an evaluator span, skipping spans with `has_detail:false`, no-supported-payload errors, API error wrapping, default Markdown, explicit JSON/YAML/TOON through the canonical formatter, and `--slice` behavior.

- [ ] **Step 2: Run the focused command tests and confirm they fail**

  Run `go test ./cli/custom/commands -run 'TestTracesThread' -count=1`. The expected failure is missing `TraceAPI` or `NewTracesThreadCommand`.

- [ ] **Step 3: Implement span resolution**

  With an explicit span ID, hydrate only that span. Otherwise call get-trace, hydrate `trace.leading_span_id`, and use it if normalization yields content. If absent or unusable, page through list-spans, build evaluator-descendant exclusions from `span_id`/`parent_span_id`, sort remaining detailed spans by `started_at` descending with stable tie-breaking, hydrate candidates, and return the first normalizable conversation. Treat type/name/operation values containing `evaluator` or the standalone token `eval` case-insensitively as evaluator spans. Return a clear error if no supported conversation exists.

- [ ] **Step 4: Implement the Cobra command and output behavior**

  Define `Use: "thread trace-id [span-id]"`, `Args: cobra.RangeArgs(1, 2)`, examples for the three slice forms, and a string `--slice` flag. Default to Markdown whenever the user did not explicitly request a machine format, including when stdout is piped. Route explicit `--json`, `-o yaml`, and `-o toon` through `emit(thread)`. Keep API/parse/selection errors on the Cobra error path.

- [ ] **Step 5: Inject each module's generated operations and register under `traces`**

  Extend `custom.Run` and registration plumbing to receive `commands.TraceAPI`. Keep `Register` test-friendly with an optional/zero API while always exposing the command surface. In each main, create wrappers around that module's `generated.OpenapiTracesGet`, `generated.OpenapiTracesGetSpan`, and `generated.OpenapiTracesListSpans`; never import the root generated package from shared custom code. Find the existing generated `traces` parent and add `NewTracesThreadCommand` once.

- [ ] **Step 6: Run stable and rc tests and commit**

  Run `go test ./cli/custom/... -count=1`, `go build ./...` in the repository root, and `go build ./... && go vet ./...` in `packages/orq-rc`. Run `gofmt` on modified Go files, repeat those commands, and commit with `feat(traces): add thread command`.

### Task 3: Real fixtures, surface contract, changelog, and final validation

**Files:**
- Modify: `cli/custom/commands/testdata/thread/chat.json`
- Modify: `cli/custom/commands/testdata/thread/responses.json`
- Modify: `cli/custom/commands/testdata/thread/responses-unavailable.json`
- Modify: `cli/custom/commands/thread_test.go`
- Modify: `cli/custom/commands/traces_thread_test.go`
- Modify: `surface.json`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes the completed command and canonical structure from Tasks 1-2.
- Produces anonymized hydrated-span regression fixtures and the public command surface entry `orq traces thread` with string flag `slice`.

- [ ] **Step 1: Retrieve and sanitize real hydrated spans**

  Use the locally built `orq` CLI with the `orq-research` workspace to retrieve safe synthetic traces. Reuse the known synthetic Responses traces `5210c6be4da10f53c6ffab0c680d10d9` / span `b1c07b68ae43f800` and `f001508c572664360190c583f9b70806` / span `6a2b15abe02da6b2` where still accessible. Retrieve or make one minimal non-streaming Chat Completions call with synthetic text plus a synthetic tool call/result. Replace all trace/span/response/item/call IDs and remove unrelated metadata before updating fixtures.

- [ ] **Step 2: Pin real-shape canonical and Markdown behavior**

  Ensure fixtures demonstrate Chat JSON-string input and direct-message output, Responses `_value` content, and Responses `items.count` without `_value`. Add assertions that the command never invents unavailable response text, reasoning, tool names, arguments, or results; never outputs encrypted/redacted blobs; and preserves the known available content.

- [ ] **Step 3: Update command surface and changelog**

  Run `go run ./cmd/surface-dump -write` and verify the only intended surface addition is `orq traces thread` with `slice:string`. Add an `**Added:**` entry directly below `## Unreleased` describing the new thread view, its Chat/Responses normalization, Markdown output, structured formats, slicing, and explicit unavailable-content marker.

- [ ] **Step 4: Exercise the built command end to end**

  Run `make build`, then execute at least one explicit-span and one trace-only invocation, one `--slice=-5:` invocation, and one `--json` invocation against safe traces. Confirm stdout contains only the requested result, errors use stderr, a Responses count-only span shows `[content unavailable: 2 items]`, and an invalid slice exits with status 1.

- [ ] **Step 5: Run every repository gate and commit**

  Run `go test ./... && go vet ./...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go run ./cmd/surface-dump -check`, `dash -n install.sh`, `python3 scripts/stamp-changelog.py --self-test`, and `go build ./... && go vet ./...` inside `packages/orq-rc`. Commit with `feat(traces): validate thread view fixtures`.
