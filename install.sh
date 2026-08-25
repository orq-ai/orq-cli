#!/usr/bin/env sh
#
# install.sh - curl | sh installer for the orq.ai CLI.
#
# Usage:
#   curl -fsSL https://cli.orq.ai/install.sh | sh
#   curl -fsSL https://cli.orq.ai/install.sh | sh -s -- --no-modify-path
#
# Options:
#   --version <v>        Pin a specific release (e.g. v0.1.0). Default: latest.
#   --channel <c>        stable (default) or rc, the pre-release line.
#   --install-dir <dir>  Install directory. Default: $HOME/.orq/bin.
#   --no-modify-path     Do not touch the shell profile.
#   --no-setup           Do not run 'orq setup' after installing.
#   --help               Show this help.
#
# Environment (flags win when both are given):
#   ORQ_CLI_VERSION       Same as --version.
#   ORQ_CLI_CHANNEL       Same as --channel.
#   ORQ_CLI_INSTALL_DIR   Same as --install-dir.
#
# This script downloads a single raw binary from the GitHub Releases page for
# this repository, verifies it against the published .sha256, and drops it at
# $ORQ_CLI_INSTALL_DIR/orq.
#
# For Windows, install via npm instead:
#   npm install -g @orq-ai/cli

set -eu

# Stamped by the release workflow; stays "dev" in any unstamped copy.
INSTALLER_VERSION="dev"

REPO="orq-ai/orq-cli"
INSTALL_DIR="${ORQ_CLI_INSTALL_DIR:-$HOME/.orq/bin}"
VERSION="${ORQ_CLI_VERSION:-}"
CHANNEL="${ORQ_CLI_CHANNEL:-stable}"
MODIFY_PATH=1
RUN_SETUP=1
unverified=0

PATH_MARKER_START="# >>> orq cli >>>"
PATH_MARKER_END="# <<< orq cli <<<"

err() {
  echo "orq-cli installer: $*" >&2
}

# Inline, not read from the file: under `curl | sh` the script is on stdin and "$0" is the shell.
usage() {
  printf 'install.sh %s\n\n' "$INSTALLER_VERSION"
  cat <<'USAGE'
install.sh - curl | sh installer for the orq.ai CLI.

Usage:
  curl -fsSL https://cli.orq.ai/install.sh | sh
  curl -fsSL https://cli.orq.ai/install.sh | sh -s -- --no-modify-path

Options:
  --version <v>        Pin a specific release (e.g. v0.1.0). Default: latest.
  --channel <c>        stable (default) or rc, the pre-release line.
  --install-dir <dir>  Install directory. Default: $HOME/.orq/bin.
  --no-modify-path     Do not touch the shell profile.
  --no-setup           Do not run 'orq setup' after installing.
  --help               Show this help.

Environment (flags win when both are given):
  ORQ_CLI_VERSION       Same as --version.
  ORQ_CLI_CHANNEL       Same as --channel.
  ORQ_CLI_INSTALL_DIR   Same as --install-dir.

For Windows, install via npm instead:
  npm install -g @orq-ai/cli
USAGE
}

# --retry-all-errors needs curl 7.71; RHEL 8 (7.61) and Ubuntu 20.04 (7.68) ship older, so probe.
if curl --help all 2>/dev/null | grep -q -- --retry-all-errors; then
  CURL_RETRY_ALL='--retry-all-errors'
else
  CURL_RETRY_ALL=''
fi

# No -f: the checksum call reads the status code out of a 404; callers needing it pass -f.
fetch() {
  # Unquoted: empty must expand to no argument at all.
  # shellcheck disable=SC2086
  curl -L --proto '=https' --retry 3 --retry-delay 1 $CURL_RETRY_ALL "$@"
}

# Not `[ -r /dev/tty ]`: the node is world-readable and passes with no
# controlling terminal, where opening it fails ENXIO. `true`, not `:` — a
# redirection error on a POSIX special built-in exits a non-interactive dash.
have_tty() {
  { true < /dev/tty; } 2>/dev/null
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "required command not found: $1"
    exit 1
  fi
}

# --- Parse arguments -------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      [ $# -ge 2 ] || { err "--version needs a value"; exit 1; }
      VERSION="$2"; shift 2 ;;
    --version=*)
      VERSION="${1#*=}"; shift ;;
    --channel)
      [ $# -ge 2 ] || { err "--channel needs a value"; exit 1; }
      CHANNEL="$2"; shift 2 ;;
    --channel=*)
      CHANNEL="${1#*=}"; shift ;;
    --install-dir)
      [ $# -ge 2 ] || { err "--install-dir needs a value"; exit 1; }
      INSTALL_DIR="$2"; shift 2 ;;
    --install-dir=*)
      INSTALL_DIR="${1#*=}"; shift ;;
    --no-modify-path)
      MODIFY_PATH=0; shift ;;
    --no-setup)
      RUN_SETUP=0; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      err "unknown option: $1"
      err "run with --help to see the supported flags"
      exit 1 ;;
  esac
done

case "$CHANNEL" in
  stable|rc) ;;
  *) err "unknown channel: $CHANNEL (expected 'stable' or 'rc')"; exit 1 ;;
esac

require_cmd curl
require_cmd uname
require_cmd mktemp
require_cmd chmod
require_cmd mv

# --- Presentation ----------------------------------------------------------

# Only decorate a real terminal; a piped install stays plain.
supports_art() {
  [ -t 1 ] || return 1
  [ "${TERM:-dumb}" != "dumb" ] || return 1
  case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
    *UTF-8*|*utf-8*|*UTF8*|*utf8*) return 0 ;;
    *) return 1 ;;
  esac
}

if supports_art; then
  G_BULLET='•'
  G_OK='✓'
  G_RULE='────────────────────────────────────────────────────────────────'
else
  G_BULLET='*'
  G_OK='ok:'
  G_RULE='----------------------------------------------------------------'
fi

banner() {
  if supports_art; then
    printf '%s\n' \
      '' \
      '  ██████╗ ██████╗  ██████╗' \
      ' ██╔═══██╗██╔══██╗██╔═══██╗' \
      ' ██║   ██║██████╔╝██║   ██║' \
      ' ██║   ██║██╔══██╗██║▄▄ ██║' \
      ' ╚██████╔╝██║  ██║╚██████╔╝' \
      '  ╚═════╝ ╚═╝  ╚═╝ ╚══▀▀═╝   CLI installer' \
      ''
  else
    printf '\norq.ai CLI installer\n\n'
  fi
}

# --- Detect OS -------------------------------------------------------------

uname_s="$(uname -s)"
case "$uname_s" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT)
    err "Windows is not supported by this installer."
    err "Install via npm instead: npm install -g @orq-ai/cli"
    exit 1
    ;;
  *)
    err "unsupported operating system: $uname_s"
    exit 1
    ;;
esac

# --- Detect architecture ---------------------------------------------------

uname_m="$(uname -m)"
case "$uname_m" in
  x86_64|amd64)   arch="x64" ;;
  arm64|aarch64)  arch="arm64" ;;
  *)
    err "unsupported architecture: $uname_m"
    err "Supported: x86_64/amd64, arm64/aarch64"
    exit 1
    ;;
esac

# --- Resolve version -------------------------------------------------------

version_label="$VERSION"
# Whether the user named the version. A missing checksum is only forgivable for
# a pinned old release; on "latest" it means something is wrong with the fetch.
version_pinned=1
if [ -z "$VERSION" ]; then
  version_pinned=0
  if [ "$CHANNEL" = "rc" ]; then
    # The rc line is a GitHub pre-release, which /releases/latest deliberately
    # skips, so it is resolved from the npm dist-tag instead - the same source
    # `orq update` uses, so the two never disagree about what "rc" means.
    api_url="https://registry.npmjs.org/-/package/@orq-ai/cli/dist-tags"
    # One JSON object on one line: walk the quoted fields and take the value
    # after the "rc" key. No `exit` in the body - that closes the pipe
    # mid-stream and curl then reports a write failure on every retry.
    VERSION="$(fetch -fsS "$api_url" \
      | awk -F '"' '{for (i = 2; i < NF; i += 2) if ($i == "rc" && !v) v = $(i + 2)} END {if (v != "") print "v" v}')"
  else
    # GitHub's latest-release API returns JSON; awk out the tag_name field
    # without requiring jq. Pre-releases are excluded by the endpoint itself.
    # awk drains the stream: exiting on the match closes the pipe mid-body and
    # curl reports a write failure per retry.
    api_url="https://api.github.com/repos/$REPO/releases/latest"
    VERSION="$(fetch -fsS "$api_url" | awk -F '"' '/"tag_name":/ && !v {v=$4} END {print v}')"
  fi

  if [ -z "$VERSION" ]; then
    err "failed to determine the latest $CHANNEL release from $api_url"
    err "You can pin one explicitly: --version v0.1.0"
    exit 1
  fi
  version_label="$VERSION (latest $CHANNEL)"
fi

asset="orq-${os}-${arch}"
download_url="https://github.com/$REPO/releases/download/${VERSION}/${asset}"
# Releases publish a .sha256 next to each asset (see release-pipeline.yml).
checksum_url="${download_url}.sha256"
target="$INSTALL_DIR/orq"

banner
printf '  %s installer     %s\n' "$G_BULLET" "$INSTALLER_VERSION"
printf '  %s platform      %s-%s\n' "$G_BULLET" "$os" "$arch"
printf '  %s version       %s\n' "$G_BULLET" "$version_label"
printf '  %s install dir   %s\n' "$G_BULLET" "$INSTALL_DIR"
printf '\n'

# --- Skip when already current ---------------------------------------------

# The binary reports a bare semver; release tags carry a leading v.
expected_version="${VERSION#v}"
if [ -x "$target" ]; then
  # Exit status required, and the version token compared whole: a substring
  # match reports 4.13.10 as satisfying a pinned 4.13.1 and installs nothing.
  # First line only: `orq --version` prints the semver line and then the orq
  # API line under it, and flattening both would compare against the API
  # version instead. Older binaries print the one line; head is happy either way.
  if current="$("$target" --version 2>/dev/null | head -n 1)"; then
    current="$(printf '%s' "$current" | tr -d '\n')"
    current_version="$(printf '%s' "$current" | awk '{print $NF}')"
  else
    current=""
    current_version=""
  fi
  case "$current_version" in
    "$expected_version")
      printf '%s already up to date  (%s)\n' "$G_OK" "$current"
      # Skip the download, but still let the PATH check run: an existing binary
      # does not mean an existing PATH entry.
      RUN_SETUP=0
      already_current=1
      ;;
  esac
fi

if [ "${already_current:-0}" != "1" ]; then

  # --- Download ------------------------------------------------------------

  tmp_file="$(mktemp -t orq-cli.XXXXXX)"
  tmp_sum="$(mktemp -t orq-sum.XXXXXX)"
  # previous holds the outgoing binary between the two renames below. A signal
  # in that window would otherwise leave no orq at $target and an unexplained
  # orq.previous beside it. HUP is in the list because `curl | sh` over ssh or
  # tmux is this installer's main invocation.
  previous=""
  cleanup() {
    rm -f "$tmp_file" "$tmp_sum"
    if [ -n "$previous" ] && [ -f "$previous" ] && [ ! -e "$target" ]; then
      mv "$previous" "$target" || true
    fi
  }
  trap cleanup EXIT INT TERM HUP

  if ! fetch -f --progress-bar -o "$tmp_file" "$download_url"; then
    err "failed to download $download_url"
    err "verify the release exists: https://github.com/$REPO/releases"
    exit 1
  fi

  # Sanity-check that we actually downloaded a binary, not an HTML error page
  if [ ! -s "$tmp_file" ]; then
    err "downloaded file is empty"
    exit 1
  fi

  # --- Verify checksum -----------------------------------------------------

  # Same host as the binary: catches corruption and truncation, not a compromised release host.
  # Anything but a genuine 404 is fatal; failing open would skip verification exactly when the network is least trustworthy.
  sha_cmd=""
  if command -v sha256sum >/dev/null 2>&1; then
    sha_cmd="sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    sha_cmd="shasum -a 256"
  fi

  if [ -z "$sha_cmd" ]; then
    err "cannot verify checksum: neither sha256sum nor shasum is available"
    exit 1
  fi

  # -w writes the status (000 on a connection failure) even when curl exits non-zero, so `|| true` only stops that aborting the script.
  # 404 is real - releases predating the .sha256 assets have none - and has to be told apart from a transport failure.
  checksum_status="$(fetch -sS -o "$tmp_sum" -w '%{http_code}' "$checksum_url" || true)"
  expected=""
  case "${checksum_status:-000}" in
    200)
      expected="$(awk '{print $1}' <"$tmp_sum")"
      # A 200 with an empty body means the asset exists and was unreadable, not that no checksum is published.
      if [ -z "$expected" ]; then
        err "checksum fetch returned HTTP 200 but an empty body ($checksum_url)"
        err "Refusing to install unverified."
        exit 1
      fi
      # A captive portal answers 200 with HTML, which would otherwise be compared as a hash and reported as a mismatch.
      # Written out 64 brackets because POSIX case patterns have no repetition operator.
      hex16='[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]'
      case "$expected" in
        $hex16$hex16$hex16$hex16) ;;
        *)
          err "checksum response from $checksum_url is not a sha256 digest"
          err "a proxy or captive portal may be intercepting the request"
          exit 1
          ;;
      esac
      ;;
    404)
      # Every release since the checksum assets landed publishes one, so a 404
      # on "latest" is a broken fetch, not an old release.
      if [ "$version_pinned" = "0" ]; then
        err "no checksum published for $asset at the latest release"
        err "refusing to install unverified; pin an older release with --version if that is what you want"
        exit 1
      fi
      printf '! installing UNVERIFIED: %s publishes no .sha256\n' "$VERSION"
      unverified=1
      ;;
    *)
      err "failed to fetch checksum ($checksum_url returned HTTP ${checksum_status:-000})"
      exit 1
      ;;
  esac

  if [ -n "$expected" ]; then
    actual="$($sha_cmd < "$tmp_file" | awk '{print $1}')"
    if [ "$expected" != "$actual" ]; then
      err "checksum mismatch for $asset"
      err "  expected $expected"
      err "  actual   $actual"
      err "Refusing to install. Report this at https://github.com/$REPO/issues"
      exit 1
    fi
    printf '%s checksum verified (sha256)\n' "$G_OK"
  fi

  chmod +x "$tmp_file"

  # --- Install -------------------------------------------------------------

  mkdir -p "$INSTALL_DIR"

  # On an upgrade, keep the previous binary until the new one proves it runs;
  # a failed probe must not leave the user with less than they started with.
  # `previous` is declared with the trap above, which restores it on a signal.
  if [ -f "$target" ]; then
    previous="$target.previous"
    if ! mv "$target" "$previous"; then
      # A signal can land after rename(2) returned, so mv reports failure on a
      # move that happened; say which of the two states the user is in.
      if [ -e "$target" ]; then
        previous=""
        err "failed to move the existing binary aside ($target)"
        err "does the user have write permission to $INSTALL_DIR?"
      else
        err "interrupted while moving $target aside; it is at $previous"
      fi
      exit 1
    fi
  fi

  # Atomic replace so a partial install can't corrupt an existing binary.
  # Restores are `if ! mv`, never `&& mv`: under set -e a failing last command
  # of an AND-list exits before the diagnostics below it print.
  if ! mv "$tmp_file" "$target"; then
    if [ -n "$previous" ] && ! mv "$previous" "$target"; then
      err "restoring the previous binary also failed; it is at $previous"
    fi
    err "failed to move binary into $target"
    err "does the user have write permission to $INSTALL_DIR?"
    exit 1
  fi

  # Exit status, not just output: a binary that prints something and exits
  # non-zero is broken, and `|| echo ''` accepted it. Stderr is kept so the
  # failure branches can quote the real reason instead of guessing at it.
  probe_err="$(mktemp -t orq-probe.XXXXXX)"
  if installed_version="$("$target" --version 2>"$probe_err")"; then
    installed_version="$(printf '%s' "$installed_version" | tr -d '\n')"
  else
    installed_version=""
  fi
  probe_reason="$(tr -d '\r' < "$probe_err" | head -3)"
  rm -f "$probe_err"
  if [ -n "$installed_version" ]; then
    if [ -n "$previous" ]; then
      rm -f "$previous" || true
    fi
    printf '%s installed      %s  (%s)\n' "$G_OK" "$target" "$installed_version"
    if [ "$unverified" = "1" ]; then
      printf '! this binary was NOT checksum-verified\n'
    fi
  elif [ -n "$previous" ]; then
    if ! mv "$previous" "$target"; then
      err "restoring the previous binary also failed; it is at $previous"
      exit 1
    fi
    err "the new binary did not run here, so the previous one was restored"
    [ -n "$probe_reason" ] && err "  $probe_reason"
    exit 1
  else
    err "the installed binary does not run on this machine (--version failed)"
    [ -n "$probe_reason" ] && err "  $probe_reason"
    err "it passed the checksum, so this may be a platform mismatch or an exec policy"
    err "it was left at $target for inspection"
    exit 1
  fi
fi

# --- PATH ------------------------------------------------------------------

# Returns the profile file for the user's login shell.
profile_for_shell() {
  case "$(basename "${SHELL:-/bin/sh}")" in
    zsh)  echo "$HOME/.zshrc" ;;
    bash)
      # macOS login shells read .bash_profile; Linux ones read .bashrc.
      if [ "$os" = "darwin" ] && [ -f "$HOME/.bash_profile" ]; then
        echo "$HOME/.bash_profile"
      else
        echo "$HOME/.bashrc"
      fi
      ;;
    fish) echo "$HOME/.config/fish/config.fish" ;;
    *)    echo "" ;;
  esac
}

path_already_set=0
case ":$PATH:" in
  *":$INSTALL_DIR:"*) path_already_set=1 ;;
esac

profile=""
if [ "$path_already_set" = "1" ]; then
  : # nothing to do
elif [ "$MODIFY_PATH" = "0" ]; then
  printf '! PATH not updated (--no-modify-path)\n'
else
  profile="$(profile_for_shell)"
  if [ -z "$profile" ]; then
    printf '! PATH not updated (unrecognised shell: %s)\n' "${SHELL:-unknown}"
  elif [ -f "$profile" ] && grep -qF "$PATH_MARKER_START" "$profile" 2>/dev/null; then
    printf '%s PATH already configured in %s\n' "$G_OK" "$profile"
  else
    case "$profile" in
      *config.fish) path_line="fish_add_path \"$INSTALL_DIR\"" ;;
      *)            path_line="export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
    esac

    # With no terminal to ask at, write it anyway rather than leave the binary off PATH.
    if have_tty; then
      # To /dev/tty, not stdout: under `curl | sh | tee` stdout is the log file and the prompt would never be seen.
      {
        printf '\n  Add %s to PATH in %s?\n' "$INSTALL_DIR" "$profile"
        printf '      %s\n' "$path_line"
        printf '  [Y/n] '
      } > /dev/tty
      # `read` returns non-zero at EOF (Ctrl-D): fail closed rather than edit the rcfile this prompt protects.
      if ! read -r reply < /dev/tty; then
        reply="n"
        printf '\n'
        err "no answer read from the terminal; leaving $profile unchanged"
      else
        printf '\n'
      fi
    else
      reply=""
    fi

    case "$reply" in
      [nN]*)
        printf '! PATH not updated (declined)\n'
        profile=""
        ;;
      *)
        mkdir -p "$(dirname "$profile")"
        {
          echo ""
          echo "$PATH_MARKER_START"
          echo "$path_line"
          echo "$PATH_MARKER_END"
        } >> "$profile"
        printf '%s PATH updated   %s\n' "$G_OK" "$profile"
        printf '      %s\n' "$path_line"
        ;;
    esac
  fi
fi

# --- Next step -------------------------------------------------------------

# stdin is the script itself under `curl | sh`, so prompts must read the
# terminal directly.
if [ "$RUN_SETUP" = "1" ] && ! "$target" --help 2>/dev/null | grep -q '^  setup '; then
  # Installed release predates 'orq setup'; don't invoke a command that errors.
  printf '\n! this release has no '\''orq setup'\'' yet - skipping setup\n'
  RUN_SETUP=0
  setup_missing=1
fi

setup_ran=0
if [ "$RUN_SETUP" = "1" ] && have_tty; then
  printf '\n  Starting setup - press Ctrl-C to skip and run '\''orq setup'\'' later.\n'
  printf '\n%s\n' "$G_RULE"
  # The chained run inherits curl's cwd, rarely a project; setup then defaults to a global install.
  setup_ran=1
  # `|| setup_status=$?`, not `if ! ...; then`: there $? is the negated compound's status, so every failure read as 0.
  setup_status=0
  ORQ_SETUP_FROM_INSTALLER=1 "$target" setup < /dev/tty || setup_status=$?
  if [ "$setup_status" != "0" ]; then
    printf '\n'
    if [ "$setup_status" = "130" ]; then
      err "setup was interrupted; run 'orq setup' when you are ready"
    else
      err "setup exited $setup_status; the CLI is installed, rerun 'orq setup'"
    fi
  fi
fi

printf '\n'

if [ "$path_already_set" != "1" ]; then
  if [ -n "$profile" ]; then
    printf '  To use orq in this shell, run:\n'
    printf '      exec %s -l\n\n' "${SHELL:-sh}"
  else
    printf '  Add to your shell profile:\n'
    printf '      export PATH="%s:$PATH"\n\n' "$INSTALL_DIR"
  fi
fi

if [ "${setup_missing:-0}" != "1" ] && [ "$setup_ran" = "0" ]; then
  printf '  Next:\n'
  if [ "$path_already_set" = "1" ]; then
    printf '      orq setup\n\n'
  else
    printf '      %s setup\n\n' "$target"
  fi
fi
