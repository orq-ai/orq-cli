# API key usage notice

## Goal

Make interactive CLI users aware when an API key exported by their shell is
authenticating a command instead of their active user profile. The notice must
not expose the key or alter structured stdout.

## Behavior

- Before an API-backed command runs, snapshot user-provided credentials before
  the session bridge injects its own token into `ORQ_API_KEY`.
- When `ORQ_API_KEY`, `ORQ_TOKEN`, or `ORQ_AUTHORIZATION` is the credential a
  request authenticates with, print `Using <VARIABLE> from environment` once to
  stderr, naming the variable that supplied the key so the user knows which one
  to unset. Whether a login session exists does not matter: the user who never
  logged in is told the same thing as the one whose session the key displaces. A
  selected stored API-key profile remains authoritative and does not produce
  the notice.
- Show the line only when stderr is a terminal — that is the stream it goes
  to, and `orq x | jq` is a person reading stderr with stdout handed to a pipe.
  Explicit machine formats remain quiet.
- Suppress the line when `ORQ_NO_API_KEY_NOTICE` is non-empty.
- Local and session-authenticated commands never produce it because their
  requests do not carry the exported credential through Bartolo's API-key
  client.

## Placement

The existing custom pre-run authentication hook records the candidate exported
credential before session-token injection. A Bartolo `before dial` middleware
owns the final decision: it emits only when the outgoing Authorization header
actually contains that credential. This boundary covers generated API commands
without maintaining a command allowlist or claiming that session-only and local
commands used the key.

The notice is written with the commands package's dimmed secondary-context
style (`commands.Notice`, the ungated half of `info`). It is informational, not
a warning, and never includes any portion of the credential.

## Tests

Focused tests will pin that the notice:

- appears for a request when an environment key authenticates it and stderr
  is a terminal, whether or not a login session exists;
- names the supported environment variable that won, without exposing its value;
- is absent when stderr is not a terminal, when `ORQ_NO_API_KEY_NOTICE` is
  set, when a stored profile wins, and when the actual request uses a different
  credential;
- writes only to stderr, preserving stdout and the JSON contract.

The change will also add an `Unreleased` changelog entry because users will see
new interactive output.
