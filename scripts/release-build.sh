#!/usr/bin/env bash
#
# release-build.sh — cross-compile orq for every platform that @orq-ai/cli
# publishes to npm, drop each binary into the matching npm/cli-<os>-<arch>/bin
# directory, ad-hoc sign the macOS binaries, and stamp the given version into
# all six package.json files.
#
# Usage:
#   scripts/release-build.sh <semver> [module-dir]
#
# <module-dir> is a repo-relative path to the Go module to build from (its
# cmd/orq is the entrypoint). Defaults to the repo root (the prod `orq`
# module). Pass `packages/orq-rc` to build the rc module. Either way the
# binaries land in the shared npm/cli-* packages — prod and rc publish the
# same @orq-ai/cli package, differing only by npm dist-tag.
#
# Example:
#   scripts/release-build.sh 0.1.0
#   scripts/release-build.sh 0.1.0-rc.1 packages/orq-rc
#
# Intended to run inside the GitHub Actions release workflow on macos-latest
# (so `codesign` is available for ad-hoc signing) but safe to run locally too.

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "usage: $0 <semver> [module-dir]" >&2
  exit 1
fi

VERSION="$1"
MODULE_DIR="${2:-}"
ROOT_DIR="$(cd -- "$(dirname "$0")/.." && pwd)"
NPM_DIR="$ROOT_DIR/npm"
# Absolute path to the Go module we build from. Empty MODULE_DIR = repo root.
BUILD_DIR="$ROOT_DIR${MODULE_DIR:+/$MODULE_DIR}"

if [ ! -d "$BUILD_DIR/cmd/orq" ]; then
  echo "error: no cmd/orq in build dir: $BUILD_DIR" >&2
  exit 1
fi

# Platforms we ship: "goos goarch npm-package-suffix exe-name"
PLATFORMS=(
  "darwin arm64 cli-darwin-arm64 orq"
  "darwin amd64 cli-darwin-x64   orq"
  "linux  amd64 cli-linux-x64    orq"
  "linux  arm64 cli-linux-arm64  orq"
  "windows amd64 cli-win32-x64  orq.exe"
)

echo "Building orq $VERSION for ${#PLATFORMS[@]} platforms (module: ${MODULE_DIR:-.})..."

for row in "${PLATFORMS[@]}"; do
  # shellcheck disable=SC2086
  set -- $row
  goos="$1"
  goarch="$2"
  pkg="$3"
  exe="$4"

  target_dir="$NPM_DIR/$pkg/bin"
  mkdir -p "$target_dir"

  echo "  $goos/$goarch → $target_dir/$exe"

  (
    # Build from inside the module dir: rc is a separate Go module
    # (packages/orq-rc) with its own go.mod, so it must be built there.
    cd "$BUILD_DIR"
    CGO_ENABLED=0 \
    GOOS="$goos" \
    GOARCH="$goarch" \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o "$target_dir/$exe" \
      ./cmd/orq
  )

  # Ad-hoc sign macOS binaries so Gatekeeper doesn't quarantine them when
  # installed via npm. Requires the `codesign` binary, which is only present
  # on macOS. Skip (with a warning) on other hosts.
  if [ "$goos" = "darwin" ]; then
    if command -v codesign >/dev/null 2>&1; then
      echo "  codesign --sign - $target_dir/$exe"
      codesign --sign - --force --timestamp=none "$target_dir/$exe"
    else
      echo "  warning: codesign not available, skipping ad-hoc sign of darwin/$goarch" >&2
    fi
  fi
done

# Stamp version into all package.json files (wrapper + 5 platform packages).
# The wrapper's optionalDependencies map also gets rewritten so every
# @orq-ai/cli-* pin lines up with the wrapper's version.
echo "Stamping version $VERSION into package.json files..."

VERSION="$VERSION" NPM_DIR="$NPM_DIR" node -e '
  const fs = require("node:fs");
  const version = process.env.VERSION;
  const npmDir = process.env.NPM_DIR;
  const dirs = [
    npmDir + "/cli",
    npmDir + "/cli-darwin-arm64",
    npmDir + "/cli-darwin-x64",
    npmDir + "/cli-linux-x64",
    npmDir + "/cli-linux-arm64",
    npmDir + "/cli-win32-x64",
  ];
  for (const dir of dirs) {
    const path = dir + "/package.json";
    const pkg = JSON.parse(fs.readFileSync(path, "utf8"));
    pkg.version = version;
    if (pkg.optionalDependencies) {
      for (const key of Object.keys(pkg.optionalDependencies)) {
        pkg.optionalDependencies[key] = version;
      }
    }
    fs.writeFileSync(path, JSON.stringify(pkg, null, 2) + "\n");
    console.log("  " + path);
  }
'

echo ""
echo "Done. Binaries and package.json files ready at version $VERSION."
