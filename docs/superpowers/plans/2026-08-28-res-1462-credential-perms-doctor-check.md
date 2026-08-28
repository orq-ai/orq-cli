# RES-1462 — orq doctor reports world-readable credential files

## Decisions after review

Three calls made after the plan below was written. They override the Global
Constraints and Context sections that follow; the original text is left intact
as the record of what was planned first.

1. **`orq doctor --fix` exists.** Doctor still repairs nothing on its own, but
   an opt-in flag chmods the flagged paths (0600 files, 0700 directories) and
   reports what it changed. A failed chmod is reported as a failure naming the
   path and error. The rotation advice survives the repair: a chmod cannot
   un-expose a key that was already readable. This supersedes "Report only. No
   `os.Chmod`, no auto-repair, no `--fix` flag anywhere in this change" and the
   plan's "do not touch surface.json" — a new flag must appear in
   `surface.json` or the CI gate fails.
2. **Symlinks are followed, not skipped.** A symlinked `credentials.json`
   (chezmoi, stow) is judged on its target's mode, because that is the file the
   CLI reads. The symlink path is what gets reported and chmodded — chmod
   follows the link, so the user-facing path is the one that works when pasted.
   A broken symlink is `fs.ErrNotExist` and is skipped like a missing file.
3. **A clean run is silent.** The check returns `(doctorCheck{}, false)` when
   nothing is loose and nothing failed inspection, like `mcpCheck` does. It
   appears only for a loose path, a path that could not be inspected, or a
   `--fix` that repaired something.

## Context

Bartolo v0.6.0 made every credentials.json write 0600 from birth
(`CredentialsFile.Save`: `os.CreateTemp` + `viper.WriteConfigAs` + rename), and
`saveAuthProfile` now routes through it. That fixes future writes only. A file
created by an earlier `orq auth add-profile` stays 0644 — with the API key in
plaintext — until the next successful save, which for a user who authenticated
once may be never.

The decision made with the ticket's owner: **report, do not repair.** A silent
`os.Chmod` cannot un-expose a key that has been readable for weeks, and it
mutates a user-owned file on every command run. Doctor warns, names the exact
`chmod`, and tells the user to rotate.

## Global Constraints

- Report only. No `os.Chmod`, no auto-repair, no `--fix` flag anywhere in this
  change.
- Unix only. When `runtime.GOOS == "windows"` the check must not appear at all
  (Go's mode bits do not map to Windows ACLs). Do not report a `pass` there
  either — the check is absent.
- Never read, log, or include the contents of any credential file. Only
  `os.Stat` metadata.
- A missing file is not a finding: skip it silently.
- The check follows the existing `(doctorCheck, bool)` shape used by
  `mcpCheck`, `gatewayKeyShadowsSessionCheck` and `gatewayKeyExpiryCheck` in
  `cli/custom/commands/doctor.go`, and is wired into `RunE` alongside them so
  it appears in both the human table and `--json`.
- Do not change how any file is written. This change is read-only diagnostics.

## Task 1 — `credentialPermsCheck` in doctor

File: `cli/custom/commands/doctor.go` (plus `doctor_test.go`).

Add:

```go
func credentialPermsCheck() (doctorCheck, bool)
```

Behavior:

- Return `doctorCheck{}, false` immediately when `runtime.GOOS == "windows"`.
- Resolve the config directory with `viper.GetString("config-directory")`.
  Return `false` when it is empty.
- Stat these paths, skipping any that do not exist:
  - `<dir>` itself — the directory. Loose is `perm&0o077 != 0`; the fix is
    `chmod 700 <dir>`.
  - `<dir>/credentials.json`
  - `<dir>/env`
  - `<dir>/env.fish`
  - every `*.json` under the sessions directory. Derive that directory from
    `auth.SessionFilePath()` (`filepath.Dir`) rather than hardcoding a name, so
    it cannot drift from the auth package. Enumerate with `os.ReadDir`; if the
    directory is absent, skip it.
  For files, loose is `info.Mode().Perm()&0o077 != 0`; the fix is
  `chmod 600 <file>`.
- Only regular files are checked (`info.Mode().IsRegular()`), plus the config
  directory itself. Symlinks and anything else are skipped.
- With no loose paths: return a `pass` check, ID `credential_permissions`,
  message along the lines of `Credential files are not readable by other
  accounts`.
- With one or more loose paths: `warn`, ID `credential_permissions`. The message
  must name every loose path and give the exact command to run for each, and end
  with a rotation hint — a key that was world-readable should be treated as
  exposed, and `orq setup` mints a new one. Keep it one line, `; `-joined, like
  `mcpCheck` does.
- `Details` carries a machine-readable list for `--json`: for each loose path,
  its path, its octal mode as a string (e.g. `"0644"`), and the `chmod` command.
  Use a key such as `loose` holding a slice of maps, plus a `checked` count.

Wire the call into `RunE` next to the other `if _, ok := ...` checks (after
`gatewayKeyExpiryCheck`, before the `probeURL` calls) so it lands in `checks`.

Tests in `cli/custom/commands/doctor_test.go`, following the existing style
(`viper.Set("config-directory", t.TempDir())` with a `t.Cleanup` restoring it,
as `connect_test.go` does):

- Skip the whole test with `t.Skip` when `runtime.GOOS == "windows"`.
- A 0644 `credentials.json` produces a `warn` naming that path and
  `chmod 600`, and mentions rotation.
- A 0600 `credentials.json` (in a 0700 dir) produces `pass`.
- A loose sessions file (`<sessions dir>/<profile>.json` at 0644) is reported —
  this is the case that proves the sessions directory is actually scanned.
- A loose config directory (0755) is reported with `chmod 700`.
- An empty config dir with no credential files produces `pass`, not a warn.
- Two loose files are both named in the single message.

Verify with `go test ./cli/custom/commands/ -run Perm` and `go vet ./...`.

## Task 2 — CHANGELOG and README

Files: `CHANGELOG.md`, `README.md`.

- `CHANGELOG.md`, under `## Unreleased`: an **Added** entry saying `orq doctor`
  now warns when a credential file under `~/.orq` is readable by other accounts,
  naming the `chmod` to run and advising rotation; Unix only. It should point
  back to the existing "Fixed (security)" entry rather than restating it — that
  entry already tells people to check the file by hand; this is what makes the
  check automatic.
- `README.md`, in the `doctor` reports list under `## Diagnostics`: one bullet
  for the credential-permissions check.
- No other docs changes. Do not touch `surface.json` — no command or flag
  changed.
