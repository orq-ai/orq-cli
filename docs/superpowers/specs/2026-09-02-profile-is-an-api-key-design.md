# orq CLI: `--profile` names an API key; a login belongs to a server

Date: 2026-09-02
Status: approved design; plan at `docs/superpowers/plans/2026-09-02-profile-is-an-api-key.md`
Stacked on: PR #63 (bartolo 0.6.0 → 0.9.0), same release
Related: orq-ai/bartolo#37

## Problem

The CLI has two things called a profile, keyed by the same name.

- A **bartolo profile** is an entry in `~/.orq/credentials.json` under `profiles.<name>`: an
  API key plus the server it belongs to. An orq API key is scoped to one workspace.
- A **session** is a browser login: `~/.orq/sessions/<name>.json` holds a refresh token, the
  server it was issued by, every workspace the account can see, the active one, and a cache
  of short-lived per-workspace tokens.

`--profile <name>` selected both. Under bartolo ≤0.7 that was survivable because bartolo let
the environment win and the CLI injected the session's workspace token into `ORQ_API_KEY`.
bartolo 0.8 made a named profile authoritative: `--profile work` with no `profiles.work` is
`profile "work" is not configured`, and the injected token is never consulted. So a named
browser login cannot be used at all after the upgrade, and the only fix that keeps both
meanings is a second credential resolver competing with bartolo's (#63's review, decision 1).

The two meanings also differ in scope. A profile is one workspace; a session spans every
workspace of one account on one server, and `orq workspace use` moves inside it. `--profile`
looked like a destination and was not one.

## Decision

A profile is a bartolo API-key profile and nothing else. A session is identified by the
server it was issued by. Three knobs, none overloaded:

| knob | picks | set by |
|---|---|---|
| server | request host | `--server` > active profile's server > `ORQ_SERVER` > `orq server set` > `https://my.orq.ai` |
| workspace | where entities land | `orq workspace use`, `--workspace` for one call |
| profile | a saved API key instead of the login | `--profile`, `ORQ_PROFILE`, `auth profile use` |

## Design

### `--profile`

bartolo resolves it. Unknown name → bartolo's own `profile "x" is not configured`. A profile
in force means the session is not read for that call: no token minted, `--workspace` warns
"no effect" exactly as it does today for an exported `ORQ_API_KEY`, `orq workspace use` still
edits the session but does not affect the call. `--profile ""` is bartolo's one-call opt-out
of a persisted `auth profile use` selection and is kept as is.

`orq auth login --profile x` is an error: "profiles are API keys; log in to another server
with `--server`". `orq auth login --api-key K --profile x` writes bartolo profile `x`; without
`--profile` it writes `default`, as today.

### Sessions are per server

`~/.orq/sessions/<host>.json`. The host comes from the session's own `apiBaseUrl`: lowercased,
dots kept, `_<port>` appended only when a port is present, any other character outside
`[a-z0-9.-]` replaced with `_`. No scheme: http and https to one host are one login.

```
https://my.orq.ai           → my.orq.ai.json
https://api.orq.ai          → my.orq.ai.json   (legacy hosted alias, same login)
https://my.staging.orq.ai   → my.staging.orq.ai.json
http://localhost:8080       → localhost_8080.json
```

The session a command uses is the one for the resolved server. The existing bridge that fell
back to the session's host when nothing else set a server (`register.go`, the
`session.APIBaseURL → auth.SetServer(…, "session")` step) becomes circular and is deleted.
`mirrorServerToViper` stays: it is how the generated commands see the resolved server.

`auth login` logs into the resolved server and writes that host's file. `auth logout` revokes
and removes only the resolved server's session.

### Gateway-key state lives in the session

`orq setup` mints a gateway key for coding agents and remembers the key, its id, its expiry
and the workspace it was minted for. #63 parked those under `state.<profile>` in
`credentials.json`. They belong to the login they were minted from, so they become fields on
`Session` (`gatewayKey`, `gatewayKeyId`, `gatewayKeyExpiresAt`, `gatewayWorkspace`).
`cli/custom/auth/state.go` is deleted. `credentials.json` is written only through bartolo's
own `auth profile add` path and holds only bartolo's fields.

When a profile is in force `setup` mints nothing and uses the profile's key as the agent key —
the existing `apiKeyConfigured()` branch, no new behaviour.

### Commands

bartolo's `auth profile add | list | current | use | clear` is canonical, names unchanged.
`auth list-profiles` and `auth add-profile` are already deprecated in bartolo 0.9 (hidden from help, one stderr
line pointing at the replacement) and are removed in the next minor. The custom
`list-profiles` union with session state goes away with it: the listing is bartolo's table,
because that is what a profile is now.

`doctor` and `whoami` report the profile (name, server, masked key) when one is in force and
the session otherwise. `doctor` names the session file (host) in play.

`orq launch` and `orq orqi` pass `ORQ_SERVER` as well as `ORQ_PROFILE` to the child, so the
child picks the same session the parent authenticated against. The environment variable is
the contract, not a `--server` argument: every child here is an `orq`-aware program that
already resolves the server from the environment, and a flag would have to be spelled
differently for each of them and accepted by all of them before it could be relied on.
`orq --server <url> orqi …` therefore sets `ORQ_SERVER=<url>` on the subprocess and leaves
the passthrough arguments untouched.

### Migration, on the first command of the new binary

1. **Session files.** Each `sessions/<name>.json` is renamed to its host. When two resolve to
   the same host, the file with the newest mtime wins; refresh tokens are opaque and carry no
   comparable freshness metadata. The other is renamed `<name>.json.deprecated` and left in
   place, one stderr line naming it.
2. **Our fields out of bartolo's table.** For each `profiles.<name>` carrying any of
   `gateway_key`, `gateway_key_id`, `gateway_key_expires_at`, `gateway_key_project`, `workspace` (the fields only this
   CLI writes), and for each `state.<name>` written by #63: move them into the session file of
   the profile's host (the profile's `server`, else the session named `<name>` before step 1,
   else `my.orq.ai`), delete them from `credentials.json`, and delete the profile if it is
   left with no `api_key`. A keyless profile with none of our fields belongs to someone else and
   is not touched. `state` is removed once empty. `profile-selected` in `config.json` is
   cleared if it names a profile that was deleted, `profile-decided` is kept.
3. Writes go through the atomic writer (`auth.WriteSecretFile`, from #63) and the session
   store's own save. The migration returns its error and the command stops; a warning here is
   only a preface to a worse error.

Idempotent: a migrated tree has nothing matching either step.

### Deleted

- `auth.ActiveProfile()` as a session selector, and `SessionFilePath()` keyed by it
- `rejectUnknownProfile` — bartolo reports an unknown profile itself
- `applyProfileAPIKey` — bartolo ranks a profile above the environment itself
- the session-host bridge in `installSessionPreRun` (`mirrorServerToViper` stays)
- `cli/custom/auth/state.go` and its tests; `StateProfiles`, `StateOf`, `StateValueOf`
- `BindProfileServer`'s keyless branch; a server is bound only by `auth profile add`
- the custom `listAuthProfiles` union

Kept: `repairAuthProfileType` (fixes real API-key profiles written before bartolo learned
`type`), `resolveServer`, `activeWorkspaceToken`.

### Cross-repo dependency

orqi (orq-ai/orqi) reads `~/.orq/sessions/<profile>.json` directly. After this change the
file is `sessions/<host>.json` and the child receives `ORQ_SERVER`. orqi must adopt the same
host rule, or shell out to `orq` for the session, before this release ships. Track as a
blocking companion change. Settled with the orqi side (RES-1500 / orq-ai/orqi#4): orqi reads
the session path from `orq auth whoami --json` (`session_file`, present on the profile payload
too), and takes the server from `ORQ_SERVER` rather than from a forwarded flag.

## User-visible changes (CHANGELOG)

**Breaking:** `--profile` names a saved API-key profile and nothing else. Browser logins are
per server (`~/.orq/sessions/<host>.json`), chosen by `--server`, `ORQ_SERVER` or
`orq server set`; `orq auth login --profile x` is an error. Existing session files are renamed
to their host on the next command; a second file for the same host is left as
`<name>.json.deprecated`. The gateway key `orq setup` minted moves from `credentials.json`
into the session file.

**Deprecated:** `auth list-profiles` and `auth add-profile` print a notice; use
`auth profile list` and `auth profile add`. Removed in the next minor.

## Verification

- `go test ./...`, `go vet ./...`, `gofmt -l`, `go run ./cmd/surface-dump -check`, rc module
  build.
- Migration tests: two sessions on one host; a legacy keyless profile with our fields; #63's
  `state` layout; a foreign keyless profile; idempotence; `profile-selected` clearing.
- Live: fresh HOME with the 5.2.1 layout, `ORQ_API_KEY` unset → `orq agents list` authenticates
  through the session; `--profile acme` with a saved key ignores the session; `--profile nope`
  gives bartolo's error; `--server https://my.staging.orq.ai auth login` writes the staging
  file and `orq --server … whoami` reads it.
