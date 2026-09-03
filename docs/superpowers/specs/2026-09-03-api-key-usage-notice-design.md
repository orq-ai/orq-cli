# API key usage notice

## Goal

Make interactive CLI users aware when an API key exported by their shell is
authenticating a command instead of their active user profile. The notice must
not expose the key or alter structured stdout.

## Behavior

- Before an API-backed command runs, inspect the credential sources before the
  session bridge injects its own token into `ORQ_API_KEY`.
- When `ORQ_API_KEY`, `ORQ_TOKEN`, or `ORQ_AUTHORIZATION` wins over an active
  credentials profile or login session, print one informational line to stderr
  naming the environment variable and the active profile.
- Show the line only when stdout is a terminal. Piped output and explicit
  machine formats remain quiet.
- Suppress the line when `ORQ_NO_API_KEY_NOTICE` is non-empty.
- Suppress the line when an explicit `--profile` wins, when no profile or
  session exists to be displaced, and for local/profile-management commands
  that do not authenticate an ordinary API request.

## Placement

The existing custom pre-run authentication hook owns the notice. It already
captures user-provided keys before session-token injection and resolves the
`--profile` precedence rule, so placing the decision there covers both generated
and hand-written API commands without duplicating behavior in each command.

The notice uses stderr directly through the CLI writer. It is informational,
not a warning, and never includes any portion of the credential.

## Tests

Focused tests will pin that the notice:

- appears for a TTY command when an environment key displaces a login session
  or stored profile;
- identifies each supported environment variable without exposing its value;
- is absent outside a TTY, when `ORQ_NO_API_KEY_NOTICE` is set, when no user
  profile is displaced, for local commands, and when explicit `--profile`
  selects the winning credential;
- writes only to stderr, preserving stdout and the JSON contract.

The change will also add an `Unreleased` changelog entry because users will see
new interactive output.
