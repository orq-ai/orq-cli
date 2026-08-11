#!/usr/bin/env sh
#
# install.sh — curl | sh installer for the orq.ai CLI.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/orq-ai/orq-cli/main/install.sh | sh
#
# Environment:
#   ORQ_CLI_VERSION       Pin a specific release (e.g. v0.1.0). Default: latest.
#   ORQ_CLI_INSTALL_DIR   Install directory. Default: $HOME/.orq/bin.
#
# This script downloads a single raw binary from the GitHub Releases page for
# this repository and drops it at $ORQ_CLI_INSTALL_DIR/orq. It does not touch
# your shell profile; follow the PATH hint printed at the end if you need it.
#
# For Windows, install via npm instead:
#   npm install -g @orq-ai/cli

set -eu

REPO="orq-ai/orq-cli"
INSTALL_DIR="${ORQ_CLI_INSTALL_DIR:-$HOME/.orq/bin}"
VERSION="${ORQ_CLI_VERSION:-}"

err() {
  echo "orq-cli installer: $*" >&2
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "required command not found: $1"
    exit 1
  fi
}

require_cmd curl
require_cmd uname
require_cmd mktemp
require_cmd chmod
require_cmd mv

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

if [ -z "$VERSION" ]; then
  # GitHub's latest-release API returns JSON; awk out the tag_name field
  # without requiring jq. The server responds in a stable order so this
  # is safe across the board.
  api_url="https://api.github.com/repos/$REPO/releases/latest"
  VERSION="$(curl -fsSL "$api_url" 2>/dev/null | awk -F '"' '/"tag_name":/ {print $4; exit}')"

  if [ -z "$VERSION" ]; then
    err "failed to determine latest release from $api_url"
    err "You can pin one explicitly: ORQ_CLI_VERSION=v0.1.0 sh install.sh"
    exit 1
  fi
fi

asset="orq-${os}-${arch}"
download_url="https://github.com/$REPO/releases/download/${VERSION}/${asset}"

echo "Installing orq ${VERSION} (${os}-${arch}) → ${INSTALL_DIR}/orq"

# --- Download --------------------------------------------------------------

tmp_file="$(mktemp -t orq-cli.XXXXXX)"
cleanup() {
  rm -f "$tmp_file" "$tmp_file.sha256"
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

# --- Verify checksum -------------------------------------------------------

# Releases publish a .sha256 next to each binary. This is an integrity check
# against corruption or a truncated download - the checksum comes from the
# same host as the binary, so it is not a defense against a compromised
# release host. Only a genuine 404 (older releases predate the checksum
# assets) downgrades to a warning; any other fetch failure - timeout, proxy,
# 5xx, DNS - is fatal, because failing open would skip verification exactly
# when the network is least trustworthy.
checksum_url="${download_url}.sha256"
checksum_file="$tmp_file.sha256"
# curl's -w writes the status (000 on a connection failure) even when it exits
# non-zero, so `|| true` just stops that non-zero from aborting the script. A
# second `|| echo 000` would append and read as HTTP 000000.
checksum_status="$(curl -sSL -o "$checksum_file" -w '%{http_code}' "$checksum_url" || true)"
checksum_status="${checksum_status:-000}"
expected=""
case "$checksum_status" in
  200)
    expected="$(awk '{print $1}' <"$checksum_file")"
    # A 200 with an empty or truncated body is NOT "no checksum published" — the
    # asset exists but we could not read it, so verification would be skipped
    # exactly when the download is suspect. Treat it as fatal, not a downgrade.
    if [ -z "$expected" ]; then
      err "checksum fetch returned HTTP 200 but an empty body ($checksum_url); refusing to install unverified"
      exit 1
    fi
    ;;
  404) ;; # no checksum published for this release
  *)
    err "failed to fetch checksum ($checksum_url returned HTTP $checksum_status)"
    exit 1
    ;;
esac
if [ -n "$expected" ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$tmp_file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$tmp_file" | awk '{print $1}')"
  else
    err "cannot verify checksum: neither sha256sum nor shasum is available"
    exit 1
  fi
  if [ "$actual" != "$expected" ]; then
    err "checksum mismatch for $asset"
    err "expected: $expected"
    err "actual:   $actual"
    err "refusing to install a binary that does not match its published checksum"
    exit 1
  fi
  echo "Checksum verified (sha256)"
else
  err "warning: no checksum published for ${VERSION}; skipping verification"
fi

chmod +x "$tmp_file"

# --- Install ---------------------------------------------------------------

mkdir -p "$INSTALL_DIR"
target="$INSTALL_DIR/orq"

# Atomic replace so a partial install can't corrupt an existing binary.
if ! mv "$tmp_file" "$target"; then
  err "failed to move binary into $target"
  err "does the user have write permission to $INSTALL_DIR?"
  exit 1
fi

# --- Verify + PATH hint ----------------------------------------------------

installed_version="$("$target" --version 2>/dev/null || echo '')"
if [ -n "$installed_version" ]; then
  echo "Installed: $installed_version"
else
  echo "Installed: $target"
fi

case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    ;;
  *)
    echo
    echo "NOTE: $INSTALL_DIR is not on your PATH."
    echo
    echo "Add this line to your shell profile (~/.zshrc, ~/.bashrc, etc.):"
    echo
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    echo
    ;;
esac

echo
echo "Next: run 'orq auth login' to authenticate."
