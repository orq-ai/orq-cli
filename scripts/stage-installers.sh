#!/bin/sh
# Stage the per-tag installers and their checksums for a GitHub release.
set -eu

if [ "$#" -ne 2 ]; then
  echo 'usage: stage-installers.sh <tag> <output-dir>' >&2
  exit 2
fi

tag=$1
out=$2
mkdir -p "$out"

# Refuse a changed or missing stamp point instead of publishing a "dev" installer.
[ "$(grep -c '^INSTALLER_VERSION=' install.sh)" -eq 1 ]
[ "$(grep -c '^\$InstallerVersion = ' install.ps1)" -eq 1 ]

sed "s|^INSTALLER_VERSION=.*|INSTALLER_VERSION=\"${tag}\"|" install.sh > "$out/install.sh"
sed "s|^\$InstallerVersion = .*|\$InstallerVersion = '${tag}'|" install.ps1 > "$out/install.ps1"

grep -Fx "INSTALLER_VERSION=\"${tag}\"" "$out/install.sh" > /dev/null
grep -Fx "\$InstallerVersion = '${tag}'" "$out/install.ps1" > /dev/null

# Run inside the output dir so the .sha256 files name assets, not local paths.
( cd "$out" && shasum -a 256 install.sh > install.sh.sha256 && shasum -a 256 install.ps1 > install.ps1.sha256 )
