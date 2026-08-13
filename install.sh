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
# asset; stays "dev" when run from a checkout, which is the honest answer.
# Not exposed as `--version`: that flag already pins the CLI release to install,
# and overloading it would be ambiguous. It is printed in the run header
# instead, so it appears in any output a user pastes into a bug report.
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
# for a pipe to consume — the progress *is* the output, and rustup, Homebrew
# and Docker's installers all print it to stdout. Deliberate, not an oversight.
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

# Whether there is a human to prompt. stdin is the script itself under
# `curl | sh`, so the terminal has to be reached through /dev/tty. Testing
# `[ -r /dev/tty ]` is not enough: the device node is world-readable, so it
# passes even in CI where the process has no controlling terminal and opening
# it fails with ENXIO. Actually opening it is the only honest check.
have_tty() {
  { : < /dev/tty; } 2>/dev/null
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
  VERSION="$(curl -fsSL "$api_url" 2>/dev/null | awk -F '"' '/"tag_name":/ {print $4; exit}')"

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

  if ! curl -fSL --progress-bar -o "$tmp_file" "$download_url"; then
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
  checksum_status="$(curl -sSL -o "$tmp_sum" -w '%{http_code}' "$checksum_url" || true)"
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

  installed_version="$("$target" --version 2>/dev/null || echo '')"
  if [ -n "$installed_version" ]; then
    printf '%s installed      %s  (%s)\n' "$G_OK" "$target" "$installed_version"
  else
    printf '%s installed      %s\n' "$G_OK" "$target"
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
      printf '\n  Add %s to PATH in %s?\n' "$INSTALL_DIR" "$profile"
      printf '      %s\n' "$path_line"
      printf '  [Y/n] '
      # `read` returns non-zero at EOF, which would abort the script under -e.
      read -r reply < /dev/tty || reply=""
      printf '\n'
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

if [ "$RUN_SETUP" = "1" ] && have_tty; then
  printf '\n  Starting setup - press Ctrl-C to skip and run '\''orq setup'\'' later.\n'
  printf '\n%s\n' "$G_RULE"
  # The chained run inherits the cwd of the curl invocation, which is rarely a
  # project; setup detects that and defaults to a global install.
  ORQ_SETUP_FROM_INSTALLER=1 "$target" setup < /dev/tty || true
  exit 0
fi

printf '\n'
if [ "$path_already_set" != "1" ] && [ -z "$profile" ]; then
  printf '  Add to your shell profile:\n'
  printf '      export PATH="%s:$PATH"\n\n' "$INSTALL_DIR"
elif [ -n "$profile" ]; then
  printf '  Restart your shell, or run:\n'
  printf '      exec %s -l\n\n' "${SHELL:-sh}"
fi
if [ "${setup_missing:-0}" != "1" ]; then
  printf '  Next:\n'
  printf '      orq setup\n\n'
fi
