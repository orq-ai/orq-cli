<#
.SYNOPSIS
  PowerShell installer for the orq.ai CLI on Windows.

.DESCRIPTION
  The Windows counterpart to install.sh. Downloads the single native binary
  (orq-win32-x64.exe) from this repo's GitHub Releases, verifies it against the
  published .sha256, drops it at $HOME\.orq\bin\orq.exe (parity with the unix
  ~/.orq/bin layout), adds that directory to the user PATH, and runs 'orq setup'.

.EXAMPLE
  & ([scriptblock]::Create((irm https://cli.orq.ai/install.ps1)))

.EXAMPLE
  # With options, download first:
  #   irm https://cli.orq.ai/install.ps1 -OutFile install.ps1
  #   powershell -ExecutionPolicy Bypass -File .\install.ps1 -NoModifyPath

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

# Flags win over env; env wins over the default.
if (-not $InstallDir) { $InstallDir = if ($env:ORQ_CLI_INSTALL_DIR) { $env:ORQ_CLI_INSTALL_DIR } else { Join-Path $HOME '.orq\bin' } }
if (-not $Version)     { $Version = $env:ORQ_CLI_VERSION }
$channelExplicit = [bool]$Channel
if (-not $Channel)     { $Channel = if ($env:ORQ_CLI_CHANNEL) { $env:ORQ_CLI_CHANNEL } else { 'stable' } }
$Quiet = ($env:ORQ_CLI_QUIET -eq '1')

function Say  { param([string]$m) if (-not $Quiet) { Write-Host $m } }
function Warn { param([string]$m) Write-Host $m -ForegroundColor Yellow }
function Die  { param([string]$m) throw "orq-cli installer: $m" }

if ($Help) {
  if ($PSCommandPath) {
    Get-Help $PSCommandPath -Detailed
  } else {
    Write-Host 'Usage: install.ps1 [-Version <tag>] [-Channel stable|rc] [-InstallDir <path>] [-NoModifyPath] [-NoSetup]'
  }
  return
}

if ($Channel -notin @('stable', 'rc')) {
  Die "unknown channel: $Channel (expected 'stable' or 'rc')"
}

# A pinned version names one exact release, leaving the channel nothing to resolve.
# Only an explicit -Channel flag conflicts; an ambient ORQ_CLI_CHANNEL is config.
if ($Version -and $channelExplicit) {
  Die '-Channel and -Version cannot be combined: -Version already names a release'
}

# PowerShell 5.1 defaults to TLS 1.0/1.1, which GitHub and npm reject.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# --- Detect architecture ---------------------------------------------------
# Only orq-win32-x64.exe is published. Windows on ARM runs x64 under emulation,
# so install the x64 binary there too rather than refuse.
$archRaw = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
if (-not [Environment]::Is64BitOperatingSystem -or $archRaw -notin @('AMD64', 'ARM64')) {
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
    $rcTag = $tags.PSObject.Properties['rc']
    if (-not $rcTag -or -not $rcTag.Value) { Die 'no rc release is published; use -Version <version> for a known release' }
    $Version = "v$($rcTag.Value)"
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
    $currentOutput = & $target --version 2>$null
    $probeExit = $LASTEXITCODE
    $current = @($currentOutput)[0]
    $currentVersion = if ($current) { ($current -split '\s+')[-1] } else { $null }
    if ($probeExit -eq 0 -and $currentVersion -eq $expectedVersion) {
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
  $installHealthy = $false
  # finally removes the temp file and restores the previous binary unless the
  # new one has passed its version probe.
  try {
    # --- Download ----------------------------------------------------------
    $savedProgressPreference = $ProgressPreference
    try {
      $ProgressPreference = 'SilentlyContinue'
      Invoke-WebRequest -Uri $downloadUrl -OutFile $tmpFile -UseBasicParsing
    } catch {
      Die "failed to download $downloadUrl : $($_.Exception.Message)"
    } finally {
      $ProgressPreference = $savedProgressPreference
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
      $sumContent = (Invoke-WebRequest -Uri $checksumUrl -UseBasicParsing).Content
      $sumBody = if ($sumContent -is [byte[]]) { [Text.Encoding]::ASCII.GetString($sumContent) } else { [string]$sumContent }
      $expected = ($sumBody -split '\s+')[0]
    } catch {
      $status = $null
      $responseProperty = $_.Exception.PSObject.Properties['Response']
      if ($responseProperty -and $responseProperty.Value) { $status = [int]$responseProperty.Value.StatusCode }
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
    # install.sh refuses this (install.sh:427-432); do the same rather than fall
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
      # A unique name cannot collide with a backup left by an older run.
      $previous = Join-Path $InstallDir ('.orq-previous-' + [Guid]::NewGuid().ToString('N') + '.exe')
      Move-Item -Force $target $previous
    }
    try {
      Move-Item -Force $tmpFile $target
    } catch {
      Die "failed to move binary into $target : $($_.Exception.Message)"
    }

    # Probe the installed binary; restore the previous one if it does not run.
    $installedVersion = $null
    $probeExit = 1
    try {
      $versionOutput = & $target --version 2>$null
      $probeExit = $LASTEXITCODE
      $installedVersion = @($versionOutput)[0]
    } catch { }
    if ($installedVersion -and $probeExit -eq 0) {
      $installHealthy = $true
      Say "ok: installed      $target  ($installedVersion)"
      if ($checksumMissing) { Warn '! this binary was NOT checksum-verified' }
    } elseif ($previous) {
      Die 'the new binary did not run here; the previous one is being restored'
    } else {
      Die "the installed binary does not run on this machine; left at $target for inspection"
    }
  } finally {
    if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
    # Keep the old binary until the new one has passed its probe.
    if ($previous -and (Test-Path $previous)) {
      if ($installHealthy) {
        Remove-Item $previous -Force -ErrorAction SilentlyContinue
      } else {
        if (Test-Path $target) { Remove-Item $target -Force }
        Move-Item -Force $previous $target
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
  # Compare expanded entries so a raw %USERPROFILE% path is recognized.
  $want = $InstallDir.TrimEnd('\')
  $onPath = @($rawPath -split ';' | Where-Object { $_ } | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') }) -contains $want
  if ($onPath) {
    # nothing to do
  } elseif ($NoModifyPath) {
    Say '! PATH not updated (-NoModifyPath)'
  } else {
    $newRaw = if ([string]::IsNullOrEmpty($rawPath)) { $InstallDir } else { ($rawPath.TrimEnd(';') + ';' + $InstallDir) }
    $envKey.SetValue('Path', $newRaw, $kind)
    # Explorer and other running shells refresh their environment on this message.
    try {
      if (-not ('OrqInstallerEnvironment' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class OrqInstallerEnvironment {
    [DllImport("user32.dll", CharSet = CharSet.Auto, SetLastError = true)]
    public static extern IntPtr SendMessageTimeout(IntPtr window, uint message, IntPtr wParam,
        string lParam, uint flags, uint timeout, out IntPtr result);
}
'@
      }
      $messageResult = [IntPtr]::Zero
      $sent = [OrqInstallerEnvironment]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [IntPtr]::Zero, 'Environment', 0x2, 5000, [ref]$messageResult)
      if ($sent -eq [IntPtr]::Zero) { Warn '! PATH was saved, but Windows did not acknowledge the environment change; restart Explorer or sign in again' }
    } catch {
      Warn "! PATH was saved, but Windows could not broadcast the change: $($_.Exception.Message); restart Explorer or sign in again"
    }
    Say "ok: PATH updated   (user) $InstallDir"
  }
} finally {
  $envKey.Close()
}

# Make the installed command available in this PowerShell process as well.
$processHasPath = @($env:Path -split ';' | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('\') }) -contains $want
if (-not $NoModifyPath -and -not $processHasPath) {
  $env:Path = "$env:Path;$InstallDir"
  $processHasPath = $true
}

# --- Setup -----------------------------------------------------------------
if (-not $NoSetup) {
  # Only if this release ships 'orq setup'.
  $hasSetup = (& $target --help 2>$null | Select-String -Pattern '^\s+setup\s' -Quiet)
  if ($hasSetup) {
    if ([Console]::IsInputRedirected) {
      Warn '! setup needs an interactive terminal; run orq setup later'
      $NoSetup = $true
    } else {
      Say ''
      Say '  Starting setup - press Ctrl-C to skip and run ''orq setup'' later.'
      $priorSetupMarker = $env:ORQ_SETUP_FROM_INSTALLER
      try {
        $env:ORQ_SETUP_FROM_INSTALLER = '1'
        & $target setup
        $setupStatus = $LASTEXITCODE
      } finally {
        if ($null -eq $priorSetupMarker) { Remove-Item Env:ORQ_SETUP_FROM_INSTALLER -ErrorAction SilentlyContinue }
        else { $env:ORQ_SETUP_FROM_INSTALLER = $priorSetupMarker }
      }
      if ($setupStatus -ne 0) {
        Die "setup exited $setupStatus; the CLI is installed, rerun 'orq setup'"
      }
    }
  } else {
    Warn '! this release has no ''orq setup'' yet - skipping setup'
  }
}

Say ''
if (-not $processHasPath) {
  Say ('  To use orq in this shell now:  $env:Path += ";' + $InstallDir + '"')
}
if ($NoSetup -or -not $hasSetup) { Say '  Next:  orq setup' }
