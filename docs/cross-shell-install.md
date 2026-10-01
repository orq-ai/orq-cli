# Cross-shell installation support (RES-1595)

Research on installing orq-cli outside zsh: other POSIX shells, and PowerShell on Windows.

## Current state

`install.sh` is a POSIX `sh` script run as `curl -fsSL https://cli.orq.ai/install.sh | sh`.
The shell you run it from does not matter: CI runs it under `dash`. What varies by
shell is one step only, the PATH edit at the end (`profile_for_shell`):

| Shell | PATH auto-config | File |
|-------|------------------|------|
| zsh | yes | `~/.zshrc` |
| bash | yes | `~/.bash_profile` on macOS if it exists; otherwise `~/.bashrc` |
| fish | yes | `~/.config/fish/config.fish` |
| anything else | no, prints the manual `export PATH=...` line | - |
| Windows / PowerShell | not supported, installer exits and points to npm | - |

So the ticket's "supports only zsh" is not accurate for the unix side: bash and fish
already work. The two real gaps are:

1. Windows has no native installer. `install.sh` hard-exits on `MINGW*/MSYS*/CYGWIN*/Windows_NT`
   and tells the user to run `npm install -g @orq-ai/cli`. npm requires Node.
2. Niche shells (nushell, elvish, pwsh on macOS/Linux, tcsh, ksh) get no PATH edit,
   only the printed manual line.

## Windows: feasible now, nothing to build in the pipeline

The release already publishes everything a native Windows installer needs:

- `orq-win32-x64.exe` and `orq-win32-x64.exe.sha256` on every GitHub release
  (`release-pipeline.yml`), the same assets `install.sh` downloads for unix.
- The binary already targets PowerShell: `orq completion powershell` exists, and
  `orq doctor` has Windows-specific handling.

Only x64 is built. Windows on ARM runs the x64 binary under emulation, so the
installer targets x64 there too rather than refusing.

The missing piece is the installer script itself, plus hosting. See the prototype
at [`install.ps1`](../install.ps1) in this branch. It mirrors `install.sh`'s contract:

- resolve latest (GitHub API for stable, npm dist-tag for rc), or pin `-Version`
- download the exe, verify the published `.sha256` with `Get-FileHash`, refuse on
  mismatch, refuse a missing checksum on "latest" (allow only on a pinned old release)
- install to `$HOME\.orq\bin\orq.exe` (parity with the unix `~/.orq/bin`)
- add the dir to the user `Path` by editing its raw registry value, preserving
  `REG_EXPAND_SZ`, and notify running Windows applications of the change unless
  `-NoModifyPath`
- run `orq setup` unless `-NoSetup`
- same env vars as install.sh: `ORQ_CLI_VERSION`, `ORQ_CLI_CHANNEL`, `ORQ_CLI_INSTALL_DIR`, `ORQ_CLI_QUIET`

Early manual PowerShell 7 checks covered parsing, `-Help`, channel validation,
and release resolution. A Windows PowerShell 5.1 review found that GitHub's
`.sha256` response can arrive as bytes, and that `irm | iex` does not run a
script parameter block in an isolated scope. The installer handles the byte
response and uses a scriptblock invocation below. The `windows-latest` CI job
runs offline integration tests under Windows PowerShell 5.1 and PowerShell 7
for checksum acceptance and rejection, architecture and channel guards,
install, rollback, and PATH persistence. The Unix installer has a separate
Ubuntu job running under `dash`.

### Rollout steps to ship it

1. Publish `install.ps1` as a release asset, the same way `install.sh` is
   (`release-pipeline.yml` stamps `INSTALLER_VERSION` and uploads `install.sh` +
   `.sha256`; add `install.ps1` alongside).
2. Serve it at `https://cli.orq.ai/install.ps1`. The `cli.orq.ai` redirect that
   serves `install.sh` lives outside this repo; the same mechanism needs an
   `install.ps1` route before the one-liner can be advertised.
3. Keep the Windows PowerShell 5.1 and pwsh 7 integration tests green, and run
   one live download on Windows before advertising the endpoint. Evaluate
   Authenticode signing for the Windows executable: a consumer antivirus
   quarantined an unsigned build during review.
4. Only then update the README install section to add:
   `& ([scriptblock]::Create((irm https://cli.orq.ai/install.ps1)))`.

Until step 2 is done, do not advertise the one-liner: it would 404.

## Other POSIX shells: low value, document rather than build

The unix installer already prints the manual PATH line for any shell it does not
recognise, so the binary is usable after one copy-paste. There is no shell
usage data in this repo to rank the remaining shells. Recommendation: leave
`profile_for_shell` as is and add a shell-specific case when a user need is
identified.

## Recommendation

- Ship the PowerShell installer (prototype + rollout steps above). This closes the
  only real gap. Effort is small because the binary and checksum assets already exist.
- Leave the extra POSIX shells as manual-PATH; extend `profile_for_shell` on demand.
