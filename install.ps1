<#
.SYNOPSIS
  irm | iex installer for the orq.ai CLI on Windows (PowerShell).

.DESCRIPTION
  The Windows counterpart to install.sh. Downloads the single native binary
  (orq-win32-x64.exe) from this repo's GitHub Releases, verifies it against the
  published .sha256, drops it at $HOME\.orq\bin\orq.exe (parity with the unix
  ~/.orq/bin layout), adds that directory to the user PATH, and runs 'orq setup'.

.EXAMPLE
  irm https://cli.orq.ai/install.ps1 | iex

.EXAMPLE
  # With options, download first (a piped 'iex' cannot take script arguments):
  #   irm https://cli.orq.ai/install.ps1 -OutFile install.ps1; .\install.ps1 -NoModifyPath
  # or set the ORQ_CLI_* environment variables before the piped form.

.NOTES
  Env vars (flags win when both are given), matching install.sh:
    ORQ_CLI_VERSION      Same as -Version.
    ORQ_CLI_CHANNEL      Same as -Channel (ignored when -Version pins a release).
    ORQ_CLI_INSTALL_DIR  Same as -InstallDir.
    ORQ_CLI_QUIET        Set to 1 to drop the banner and progress lines.
#>
[CmdletBinding()]
param(
  [string]$Version,
  [ValidateSet('stable', 'rc')]
  [string]$Channel,
  [string]$InstallDir,
  [switch]$NoModifyPath,
  [switch]$NoSetup,
  [switch]$Help
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Stamped by the release workflow; stays "dev" in any unstamped copy.
$InstallerVersion = 'dev'

$Repo = 'orq-ai/orq-cli'
$PathMarker = 'orq cli'

# Flags win over env; env wins over the default.
if (-not $InstallDir) { $InstallDir = if ($env:ORQ_CLI_INSTALL_DIR) { $env:ORQ_CLI_INSTALL_DIR } else { Join-Path $HOME '.orq\bin' } }
if (-not $Version)     { $Version = $env:ORQ_CLI_VERSION }
$channelExplicit = [bool]$Channel
if (-not $Channel)     { $Channel = if ($env:ORQ_CLI_CHANNEL) { $env:ORQ_CLI_CHANNEL } else { 'stable' } }
$Quiet = ($env:ORQ_CLI_QUIET -eq '1')

function Say  { param([string]$m) if (-not $Quiet) { Write-Host $m } }
function Warn { param([string]$m) Write-Host $m -ForegroundColor Yellow }
function Die  { param([string]$m) [Console]::Error.WriteLine("orq-cli installer: $m"); exit 1 }

if ($Help) {
  Get-Help $PSCommandPath -Detailed
  exit 0
}

# A pinned version names one exact release, leaving the channel nothing to resolve.
# Only an explicit -Channel flag conflicts; an ambient ORQ_CLI_CHANNEL is config.
if ($Version -and $channelExplicit) {
  Die '-Channel and -Version cannot be combined: -Version already names a release'
}

# PowerShell 5.1 defaults to TLS 1.0/1.1, which GitHub and npm reject.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# --- Detect architecture ---------------------------------------------------
# Only orq-win32-x64.exe is published. Windows on ARM runs x64 under emulation,
# so install the x64 binary there too rather than refuse.
$archRaw = $env:PROCESSOR_ARCHITECTURE
if ($archRaw -notin @('AMD64', 'ARM64', 'x86')) {
  Die "unsupported architecture: $archRaw (only x64, incl. ARM64 emulation, is published)"
}
if ($archRaw -eq 'ARM64') {
  Warn '! no native ARM64 build; installing the x64 binary (runs under emulation)'
}
$asset = 'orq-win32-x64.exe'

# --- Resolve version -------------------------------------------------------
$versionPinned = [bool]$Version
$versionLabel = $Version
if (-not $Version) {
  if ($Channel -eq 'rc') {
    # The rc line is a GitHub pre-release, which /releases/latest skips, so it is
    # resolved from the npm dist-tag - the same source 'orq update' uses.
    try {
      $tags = Invoke-RestMethod -Uri 'https://registry.npmjs.org/-/package/@orq-ai/cli/dist-tags'
    } catch {
      Die "failed to fetch rc release metadata: $($_.Exception.Message)"
    }
    if (-not $tags.rc) { Die 'no rc release is published; use -Version <version> for a known release' }
    $Version = "v$($tags.rc)"
  } else {
    try {
      $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ 'User-Agent' = 'orq-cli-installer' }
    } catch {
      Die "failed to determine the latest $Channel release: $($_.Exception.Message)"
    }
    $Version = $rel.tag_name
    if (-not $Version) { Die "could not read the latest $Channel release; pin one with -Version v0.1.0" }
  }
  $versionLabel = "$Version (latest $Channel)"
}

$downloadUrl = "https://github.com/$Repo/releases/download/$Version/$asset"
$checksumUrl = "$downloadUrl.sha256"
$target = Join-Path $InstallDir 'orq.exe'

if (-not $Quiet) {
  Say ''
  Say '  orq.ai CLI installer'
  Say ''
  Say "  * installer     $InstallerVersion"
  Say "  * platform      win32-x64"
  Say "  * version       $versionLabel"
  Say "  * install dir   $InstallDir"
  Say ''
}

# --- Skip when already current ---------------------------------------------
$expectedVersion = $Version -replace '^v', ''
$alreadyCurrent = $false
if (Test-Path $target) {
  try {
    $current = (& $target --version 2>$null | Select-Object -First 1)
    $currentVersion = ($current -split '\s+')[-1]
    if ($currentVersion -eq $expectedVersion) {
      Say "ok: already up to date  ($current)"
      $alreadyCurrent = $true
      $NoSetup = $true
    }
  } catch { }
}

if (-not $alreadyCurrent) {
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  # Temp file lives inside the install dir, not %TEMP%: a cross-volume Move-Item
  # degrades to copy+delete (a crash mid-copy truncates orq.exe), so same-volume
  # keeps the final move a true atomic rename.
  $tmpFile = Join-Path $InstallDir (".orq-download-" + [Guid]::NewGuid().ToString('N') + '.exe')
  $previous = $null
  # finally is the PowerShell counterpart to install.sh's EXIT/INT/TERM trap: on any
  # error or Ctrl-C it drops the temp file and, if the swap left no orq.exe, restores
  # the previous binary so the user never ends up with less than they started with.
  try {
    # --- Download ----------------------------------------------------------
    try {
      Invoke-WebRequest -Uri $downloadUrl -OutFile $tmpFile -UseBasicParsing
    } catch {
      Die "failed to download $downloadUrl : $($_.Exception.Message)"
    }
    if ((Get-Item $tmpFile).Length -eq 0) {
      Die 'downloaded file is empty'
    }

    # --- Verify checksum ---------------------------------------------------
    # Same host as the binary: catches corruption and truncation. A genuine 404
    # (releases predating the .sha256 assets) is the only forgivable miss, and only
    # for a pinned old release; on "latest" a missing checksum means a broken fetch.
    $expected = $null
    $checksumMissing = $false
    try {
      $sumBody = (Invoke-WebRequest -Uri $checksumUrl -UseBasicParsing).Content
      $expected = ($sumBody -split '\s+')[0]
    } catch {
      $status = $null
      if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
      if ($status -eq 404) {
        if (-not $versionPinned) {
          Die 'no checksum published at the latest release; refusing to install unverified (pin an older release with -Version if intended)'
        }
        Warn "! installing UNVERIFIED: $Version publishes no .sha256"
        $checksumMissing = $true
      } else {
        $statusText = if ($status) { $status } else { '000' }
        Die "failed to fetch checksum ($checksumUrl returned HTTP $statusText)"
      }
    }

    # A 200 with an empty or whitespace body means the asset exists but was
    # unreadable (proxy, CDN, captive portal), NOT that no checksum is published.
    # install.sh refuses this (install.sh:408-412); do the same rather than fall
    # through the empty-string-is-falsy check below and install unverified.
    if (-not $checksumMissing -and [string]::IsNullOrWhiteSpace($expected)) {
      Die "checksum fetch returned an empty body ($checksumUrl); refusing to install unverified"
    }

    if ($expected) {
      if ($expected -notmatch '^[0-9a-fA-F]{64}$') {
        Die 'checksum response is not a sha256 digest; a proxy or captive portal may be intercepting the request'
      }
      $actual = (Get-FileHash -Path $tmpFile -Algorithm SHA256).Hash
      if ($actual -ne $expected.ToUpper()) {
        Die "checksum mismatch for $asset`n  expected $expected`n  actual   $actual`nRefusing to install. Report at https://github.com/$Repo/issues"
      }
      Say 'ok: checksum verified (sha256)'
    }

    # --- Install -----------------------------------------------------------
    # On an upgrade keep the previous binary until the new one proves it runs.
    if (Test-Path $target) {
      $previous = "$target.previous"
      Move-Item -Force $target $previous
    }
    try {
      Move-Item -Force $tmpFile $target
    } catch {
      Die "failed to move binary into $target : $($_.Exception.Message)"
    }

    # Probe the installed binary; restore the previous one if it does not run.
    $installedVersion = $null
    try { $installedVersion = (& $target --version 2>$null | Select-Object -First 1) } catch { }
    if ($installedVersion) {
      Say "ok: installed      $target  ($installedVersion)"
    } elseif ($previous) {
      Die 'the new binary did not run here; the previous one is being restored'
    } else {
      $previous = $null  # nothing to restore; leave it for inspection
      Die "the installed binary does not run on this machine; left at $target for inspection"
    }
  } finally {
    if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
    # The swap left no orq.exe (mid-upgrade error or Ctrl-C): put the old one back.
    if ($previous -and (Test-Path $previous)) {
      if (-not (Test-Path $target)) {
        Move-Item -Force $previous $target -ErrorAction SilentlyContinue
      } else {
        Remove-Item $previous -Force -ErrorAction SilentlyContinue
      }
    }
  }
}

# --- PATH ------------------------------------------------------------------
# Edit the registry directly, not [Environment]::SetEnvironmentVariable('Path','User'):
# the .NET helper reads the value with %VAR% tokens already expanded and writes it
# back as REG_SZ, permanently flattening a user PATH stored as REG_EXPAND_SZ (e.g.
# entries like %USERPROFILE%\bin). Reading raw and writing back with the original
# value kind preserves it.
$envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if (-not $envKey) { $envKey = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment') }
try {
  $rawPath = ''
  $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
  $existing = $envKey.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  if ($null -ne $existing) {
    $rawPath = [string]$existing
    try { $kind = $envKey.GetValueKind('Path') } catch { }
  }
  # Trailing-slash-normalized, case-insensitive membership so a re-run does not append a dupe.
  $want = $InstallDir.TrimEnd('\')
  $onPath = @($rawPath -split ';' | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('\') }) -contains $want
  if ($onPath) {
    # nothing to do
  } elseif ($NoModifyPath) {
    Say '! PATH not updated (-NoModifyPath)'
  } else {
    $newRaw = if ([string]::IsNullOrEmpty($rawPath)) { $InstallDir } else { ($rawPath.TrimEnd(';') + ';' + $InstallDir) }
    $envKey.SetValue('Path', $newRaw, $kind)
    # Also update the current session so 'orq' resolves without a restart.
    $env:Path = "$env:Path;$InstallDir"
    Say "ok: PATH updated   (user) $InstallDir"
    Say '      open a new terminal for the change to persist in other shells'
  }
} finally {
  $envKey.Close()
}

# --- Setup -----------------------------------------------------------------
if (-not $NoSetup) {
  # Only if this release ships 'orq setup'.
  $hasSetup = (& $target --help 2>$null | Select-String -Pattern '^\s+setup\s' -Quiet)
  if ($hasSetup) {
    Say ''
    Say '  Starting setup - press Ctrl-C to skip and run ''orq setup'' later.'
    $env:ORQ_SETUP_FROM_INSTALLER = '1'
    & $target setup
  } else {
    Warn '! this release has no ''orq setup'' yet - skipping setup'
  }
}

Say ''
if (-not $onPath) {
  Say '  To use orq in this shell now:  $env:Path += ";' + $InstallDir + '"'
}
Say '  Next:  orq setup'
