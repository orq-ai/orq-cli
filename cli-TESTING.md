# Testing PR #27 — `review/cli-onboarding-integration`

The onboarding work from 11 PRs, now one branch and one merge candidate.
Everything below runs against a throwaway `$HOME`, so your real config is never
touched.

---

## Build

```bash
git fetch origin
git checkout review/cli-onboarding-integration
git reset --hard origin/review/cli-onboarding-integration
make build          # ./bin/orq — does not replace the orq on your PATH
```

Use `./bin/orq` throughout. A fresh sandbox for each run:

```bash
FRESH=$(mktemp -d) && chmod 700 "$FRESH"
run() { env -u ORQ_API_KEY HOME="$FRESH" ./bin/orq "$@"; }
```

`run setup` now behaves like a first-ever install. Delete `$FRESH` to reset.

`run` deliberately unsets `ORQ_API_KEY` so setup starts from nothing. The
`launch` checks below therefore need a credential first — either complete
`run setup` once (it writes one into `$FRESH`), or pass one explicitly:

```bash
launch() { env HOME="$FRESH" ORQ_API_KEY=sk-orq-anything ./bin/orq launch "$@"; }
```

A dry run never calls the API, so the key does not have to be valid.

> Only the interactive `run setup` writes anything, and only inside `$FRESH`.
> If you skip the sandbox and run `./bin/orq setup` directly, it writes to your
> real `~/.claude.json`, `~/.codex/`, `~/.config/opencode/`, `~/.config/kilo/`,
> `~/.kimi-code/` and `~/.pi/agent/`. Back those up first.

---

## Fast pass (~5 min)

| # | Command | Expect |
|---|---|---|
| 1 | `./bin/orq --help` | Seven topic groups. `setup` under **Get started** |
| 2 | `run setup` | Three steps, ends `✓ Setup complete` |
| 3 | `run doctor` | Auth, endpoint, per-agent wiring, gateway funding |
| 4 | `launch claude --dry-run` | Sonnet/Opus/Haiku tiers. **No** MCP |
| 5 | `launch claude --mcp --dry-run` | `--mcp-config` and `--plugin-url` appear |

Steps 4 and 5 are the breaking change: MCP used to be on by default.

---

## Setup

| Check | How | Expect |
|---|---|---|
| No project question | `run setup` | Never asks. `--project` is gone |
| Nothing in `./.env` | `run setup` then `cat .env` | Absent, or no `ORQ_API_KEY` line |
| Key not in agent configs | `grep -r sk-orq ~/.claude.json ~/.codex 2>/dev/null` | Nothing. Configs reference `ORQ_API_KEY` |
| kimi is the exception | `grep api_key "$FRESH/.kimi-code/config.toml"` | Holds the literal key, file mode 0600 |
| Re-run reuses the key | `run setup` twice | `reusing the key from an earlier setup`. One key in the dashboard, not two |
| Rewire only | `run setup coding-agents --gateway` | Writes providers, mints nothing, leaves your shell profile alone |
| pi | `run setup --coding-agent pi` then `cat "$FRESH/.pi/agent/models.json"` | `orq` provider added, any existing providers untouched |
| Unfunded workspace | run against a workspace with no credits | Says what is missing, links credits and provider pages, still exits 0 |

**Declining the key.** `run setup -i`, answer **no** to "Create a workspace API
key now?":

```bash
grep -c eyJ "$FRESH/.kimi-code/config.toml" 2>/dev/null   # -> 0 or no such file
```

A session token in there would work for under an hour and then 401 on every
prompt. Setup skips the provider write instead.

**Ctrl-C is not consent.** Run `run setup -i` and press Ctrl-C at any
confirmation. Nothing should be minted and no shell profile edited — a prompt
that could not be answered counts as "no", not as the default.

---

## Launch

| Check | How | Expect |
|---|---|---|
| Defaults | `launch <agent> --dry-run` | claude → sonnet-5/opus-5/haiku-4-5; codex, opencode, kilo, pi → `gpt-5.6-terra`; kimi → `kimi-k2.7-code` |
| MCP is opt-in | `launch opencode --dry-run \| grep -c mcp` | `0` |
| MCP on request | `launch opencode --mcp --dry-run \| grep -c mcp` | Non-zero |
| Old flag accepted | `launch claude --no-mcp --dry-run` | Exits 0, does nothing |
| `--local` skips the prompt | `launch claude --local --dry-run` | No filesystem-access confirmation |
| Flags conflict loudly | `launch claude --local --sandbox` | Errors rather than picking one |

---

## Doctor

```bash
run doctor
```

Should report every wired agent, and warn if the current shell has no
`ORQ_API_KEY` — the state an agent started from a terminal older than your last
setup is in, which from inside the agent looks like a broken install.

For kimi the warning should say **model calls keep working and only the MCP
tools fail**, because its provider config carries the key directly.

---

## Installer

```bash
dash -n install.sh                                    # Debian/Ubuntu sh is dash
LC_ALL=C sh install.sh --help                         # ASCII fallback, no mojibake
sh install.sh --help < /dev/null                      # no TTY, exits 0, no hang
```

A real install into a sandbox, twice — the second run exercises the upgrade
path, which is where the interesting failures live:

```bash
D=$(mktemp -d)
sh install.sh --no-setup --install-dir "$D/bin" < /dev/null
sh install.sh --no-setup --install-dir "$D/bin" < /dev/null
ls "$D/bin"        # orq, and no orq.previous left behind
```

Version pinning is exact now, not a substring — pinning `4.13.1` on a machine
running `4.13.10` must actually install, not report "already up to date".

---

## `--json`

For anyone with scripts or CI against this:

```bash
run setup --json | jq '{setup_complete, gateway_funded, agents}'
```

- `gateway_verified` and `api_key.env_file` are **gone**
- `gateway_funded` is a **string** — `funded`, `unfunded`, or `unknown`.
  Not a boolean: "nobody asked" is a normal outcome, and truthiness would read
  it as "cannot pay"
- `setup_complete` is false when any agent failed to wire, not just when the
  API is unreachable

---

## Reporting

Comment on PR #27 with the command you ran, what you expected, what happened,
plus `./bin/orq --version`, your OS and your shell.

Worth a deliberate opinion rather than a pass/fail: **MCP becoming opt-in on
`orq launch`** changes behaviour on a shipped command. v4.13.10 wired it into
every launch.
