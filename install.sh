#!/usr/bin/env sh
#
# install.sh - curl | sh installer for the orq.ai CLI.
#
# Usage:
#   curl -fsSL https://orq.ai/cli/install.sh | sh
#   curl -fsSL https://orq.ai/cli/install.sh | sh -s -- --no-modify-path
#
# Options:
#   --version <v>        Pin a specific release (e.g. v0.1.0). Default: latest.
#   --install-dir <dir>  Install directory. Default: $HOME/.orq/bin.
#   --no-modify-path     Do not touch the shell profile.
#   --no-setup           Do not run 'orq setup' after installing.
#   --help               Show this help.
#
# Environment (flags win when both are given):
#   ORQ_CLI_VERSION       Same as --version.
#   ORQ_CLI_INSTALL_DIR   Same as --install-dir.
#
# This script downloads a single raw binary from the GitHub Releases page for
# this repository, verifies it against the published .sha256, and drops it at
# $ORQ_CLI_INSTALL_DIR/orq.
#
# For Windows, install via npm instead:
#   npm install -g @orq-ai/cli

set -eu

# Stamped by the release workflow when this script is published as a release
# asset; stays "dev" when run from a checkout. Printed in the run header so it
# appears in any output pasted into a bug report. Not a `--version` flag —
# that one already pins the CLI release to install.
INSTALLER_VERSION="dev"

REPO="orq-ai/orq-cli"
INSTALL_DIR="${ORQ_CLI_INSTALL_DIR:-$HOME/.orq/bin}"
VERSION="${ORQ_CLI_VERSION:-}"
MODIFY_PATH=1
RUN_SETUP=1

PATH_MARKER_START="# >>> orq cli >>>"
PATH_MARKER_END="# <<< orq cli <<<"

# Errors go to stderr; progress and results stay on stdout. clig.dev prefers
# messaging on stderr so stdout stays pipeable, but an installer emits no data
# for a pipe to consume — the progress *is* the output. Deliberate, not an
# oversight. (Practice differs across installers: some route everything to
# stderr instead. Either is defensible; what matters is that ours is chosen.)
err() {
  echo "orq-cli installer: $*" >&2
}

# Printed inline rather than sed'd out of this file's header: the documented
# way to run this is `curl -fsSL ... | sh -s -- --help`, where the script
# arrives on stdin and "$0" is the shell, so reading "$0" printed nothing.
usage() {
  printf 'install.sh %s\n\n' "$INSTALLER_VERSION"
  cat <<'USAGE'
install.sh - curl | sh installer for the orq.ai CLI.

Usage:
  curl -fsSL https://orq.ai/cli/install.sh | sh
  curl -fsSL https://orq.ai/cli/install.sh | sh -s -- --no-modify-path

Options:
  --version <v>        Pin a specific release (e.g. v0.1.0). Default: latest.
  --install-dir <dir>  Install directory. Default: $HOME/.orq/bin.
  --no-modify-path     Do not touch the shell profile.
  --no-setup           Do not run 'orq setup' after installing.
  --help               Show this help.

Environment (flags win when both are given):
  ORQ_CLI_VERSION       Same as --version.
  ORQ_CLI_INSTALL_DIR   Same as --install-dir.

For Windows, install via npm instead:
  npm install -g @orq-ai/cli
USAGE
}

# Every network call goes through here.
#
#   --proto '=https'    refuse to be redirected onto plain http. The documented
#                       entry point is a vanity URL that redirects, so the
#                       redirect chain is part of our attack surface.
#   --retry             ride out a 5xx or a DNS blip.
#   --retry-all-errors  without it, curl only retries a specific list: an empty
#                       reply (52) and a transfer cut mid-stream (18) get zero
#                       retries, and those are the likeliest way a multi-MB
#                       binary download dies. Safe here — every caller is an
#                       idempotent GET and -o truncates before each attempt.
#
# -f is deliberately not set here: the checksum call needs to read the status
# code out of a 404 rather than have curl turn it into a generic failure.
# Callers that want "any non-2xx is fatal" pass -f themselves.
#
# Requires curl >= 7.71 (--retry-all-errors). Older builds exit 2 with
# "option ... is unknown" before issuing a request; that message is left on
# stderr rather than swallowed, so the cause is visible.
fetch() {
  curl -L --proto '=https' --retry 3 --retry-delay 1 --retry-all-errors "$@"
}

# Whether there is a human to prompt. stdin is the script itself under
# `curl | sh`, so the terminal has to be reached through /dev/tty. Testing
# `[ -r /dev/tty ]` is not enough: the device node is world-readable, so it
# passes even in CI where the process has no controlling terminal and opening
# it fails with ENXIO. Actually opening it is the only honest check.
#
# `true`, not `:` — `:` is a POSIX *special* built-in, and a redirection error
# on one of those makes a non-interactive shell exit immediately. dash (Debian
# and Ubuntu's /bin/sh) and busybox ash implement that literally, so `:` here
# killed the installer with a silent exit 2 on every Linux box without a
# controlling terminal. `true` is a regular built-in and merely returns false.
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

# Status glyphs, resolved once against the same capability check as the banner.
# Gating only the art and then printing UTF-8 status markers regardless is worse
# than not gating at all: the header degrades cleanly and every line after it
# turns to mojibake, which reads as deliberate.
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
if [ -z "$VERSION" ]; then
  # GitHub's latest-release API returns JSON; awk out the tag_name field
  # without requiring jq. Pre-releases are excluded by the endpoint itself.
  api_url="https://api.github.com/repos/$REPO/releases/latest"
  VERSION="$(fetch -fsS "$api_url" | awk -F '"' '/"tag_name":/ {print $4; exit}')"

  if [ -z "$VERSION" ]; then
    err "failed to determine latest release from $api_url"
    err "You can pin one explicitly: --version v0.1.0"
    exit 1
  fi
  version_label="$VERSION (latest)"
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
  current="$("$target" --version 2>/dev/null | tr -d '\n' || echo '')"
  case "$current" in
    *"$expected_version"*)
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
  cleanup() {
    rm -f "$tmp_file" "$tmp_sum"
  }
  trap cleanup EXIT INT TERM

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

  # The checksum comes from the same host as the binary, so this guards against
  # corruption and truncated downloads, not a compromised release host. Only a
  # genuine 404 (releases predating the checksum assets) downgrades to a
  # warning; any other failure — timeout, proxy, 5xx, DNS — is fatal, because
  # failing open would skip verification exactly when the network is least
  # trustworthy.
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

  # curl's -w writes the status (000 on a connection failure) even when it exits
  # non-zero, so `|| true` only stops that non-zero from aborting the script.
  # No -f: a 404 here is a real case (releases predating the checksum
  # assets) and has to be told apart from a transport failure, which -f
  # would flatten into the same generic error.
  checksum_status="$(fetch -sS -o "$tmp_sum" -w '%{http_code}' "$checksum_url" || true)"
  expected=""
  case "${checksum_status:-000}" in
    200)
      expected="$(awk '{print $1}' <"$tmp_sum")"
      # A 200 with an empty body is not "no checksum published": the asset
      # exists and we failed to read it, so skipping would drop verification
      # exactly when the download is suspect.
      if [ -z "$expected" ]; then
        err "checksum fetch returned HTTP 200 but an empty body ($checksum_url)"
        err "Refusing to install unverified."
        exit 1
      fi
      # Shape check before comparing. A captive portal or proxy interstitial
      # answers 200 with HTML, which is non-empty and would otherwise be
      # compared as a hash — reporting a checksum mismatch and pointing the
      # user at our issue tracker for what is a network problem.
      case "$expected" in
        *[!0-9a-fA-F]* | "")
          err "checksum response from $checksum_url is not a sha256 digest"
          err "a proxy or captive portal may be intercepting the request"
          exit 1
          ;;
      esac
      if [ "${#expected}" -ne 64 ]; then
        err "checksum response from $checksum_url is not a sha256 digest"
        err "a proxy or captive portal may be intercepting the request"
        exit 1
      fi
      ;;
    404)
      printf '! checksum not verified (this release publishes no .sha256)\n'
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

  # Atomic replace so a partial install can't corrupt an existing binary.
  if ! mv "$tmp_file" "$target"; then
    err "failed to move binary into $target"
    err "does the user have write permission to $INSTALL_DIR?"
    exit 1
  fi

  # Reported by running the binary we just wrote rather than echoing the tag we
  # asked for. Integrity is already covered by the checksum above; what this
  # adds is that the binary actually executes here — the failure it catches is
  # a platform mismatch, or an exec policy that blocks it.
  #
  # Failing is fatal, where this used to print a success line regardless. An
  # unrunnable binary left on PATH is worse than no install, so remove it: the
  # EXIT trap only covers $tmp_file, which has already been moved away.
  installed_version="$("$target" --version 2>/dev/null || echo '')"
  if [ -n "$installed_version" ]; then
    printf '%s installed      %s  (%s)\n' "$G_OK" "$target" "$installed_version"
  else
    rm -f "$target"
    err "the downloaded binary does not run on this machine (--version failed)"
    err "it passed the checksum, so this is a platform mismatch or an exec policy"
    err "nothing was left at $target"
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

    # clig.dev: don't modify things outside the project without asking. The
    # shell profile is the user's, not ours, so ask for it when there is
    # someone to ask. stdin is the script itself under `curl | sh`, hence
    # /dev/tty — the same reason the setup handoff below reads from it.
    # Without a terminal (CI, piped scripts) we keep the historical behaviour
    # rather than silently leaving an installed binary off PATH.
    if have_tty; then
      # The question goes to /dev/tty, not stdout: under `curl … | sh | tee`
      # stdout is the log file, and asking there would block on a read with
      # nothing on screen to explain why.
      {
        printf '\n  Add %s to PATH in %s?\n' "$INSTALL_DIR" "$profile"
        printf '      %s\n' "$path_line"
        printf '  [Y/n] '
      } > /dev/tty
      # `read` returns non-zero at EOF — Ctrl-D. That is someone backing out of
      # the question, so it has to fail closed; treating it as consent would
      # edit the rcfile this prompt exists to protect.
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
        # The tail block prints the manual export line when no profile was
        # written; clearing it is what selects that branch.
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
  # The chained run inherits the cwd of the curl invocation, which is rarely a
  # project; setup detects that and defaults to a global install.
  #
  # A non-zero exit is reported, not swallowed: setup failing on auth, or the
  # user pressing Ctrl-C as invited above, must not read as a clean install to
  # whoever is watching. Execution continues either way so the PATH guidance
  # below still prints — setup just ran in a shell where the new entry is not
  # live yet, which is exactly when that advice matters most.
  setup_ran=1
  if ! ORQ_SETUP_FROM_INSTALLER=1 "$target" setup < /dev/tty; then
    setup_status=$?
    printf '\n'
    if [ "$setup_status" = "130" ]; then
      err "setup was interrupted; run 'orq setup' when you are ready"
    else
      err "setup exited $setup_status; the CLI is installed — rerun 'orq setup'"
    fi
  fi
fi

printf '\n'

# What the user has to do next depends only on whether `orq` resolves in the
# shell they are sitting in. Telling someone to restart a shell that already
# works is noise they have to think about before ignoring.
if [ "$path_already_set" != "1" ]; then
  if [ -n "$profile" ]; then
    # The profile carries the PATH line — either we just appended it, or it was
    # already there from an earlier run. Future shells are fine; this one isn't.
    printf '  To use orq in this shell, run:\n'
    printf '      exec %s -l\n\n' "${SHELL:-sh}"
  else
    # Nothing written: unrecognised shell, --no-modify-path, or declined.
    printf '  Add to your shell profile:\n'
    printf '      export PATH="%s:$PATH"\n\n' "$INSTALL_DIR"
  fi
fi

# Setup already ran above; pointing at it again would be telling the user to
# redo what they just did.
if [ "${setup_missing:-0}" != "1" ] && [ "$setup_ran" = "0" ]; then
  printf '  Next:\n'
  if [ "$path_already_set" = "1" ]; then
    printf '      orq setup\n\n'
  else
    # `orq` will not resolve yet, so give the command they can actually paste.
    printf '      %s setup\n\n' "$target"
  fi
fi
