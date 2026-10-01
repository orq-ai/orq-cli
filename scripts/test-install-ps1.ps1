# Offline integration tests for the Windows installer. Run with powershell and pwsh.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$installer = Join-Path (Split-Path $PSScriptRoot -Parent) 'install.ps1'
$fixture = Join-Path $PSScriptRoot 'testdata/fake-orq'
$scratch = Join-Path ([IO.Path]::GetTempPath()) ("orq-installer-test-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $scratch | Out-Null

function Assert([bool]$condition, [string]$message) {
  if (-not $condition) { throw $message }
}

function Build-Fake([string]$name, [string]$version, [string]$versionExit, [string]$setupExit) {
  $output = Join-Path $scratch "$name.exe"
  $flags = "-X main.version=$version -X main.versionExit=$versionExit -X main.setupExit=$setupExit"
  & go build -ldflags $flags -o $output $fixture
  Assert ($LASTEXITCODE -eq 0) "failed to build fixture $name"
  return $output
}

function Invoke-WebRequest {
  param([string]$Uri, [string]$OutFile, [switch]$UseBasicParsing)
  if ($Uri.EndsWith('.sha256')) {
    $body = if ($null -ne $global:installerTestChecksumBody) { $global:installerTestChecksumBody } else { "$global:installerTestDigest  orq-win32-x64.exe" }
    return [pscustomobject]@{ Content = [Text.Encoding]::ASCII.GetBytes($body) }
  }
  Copy-Item $global:installerTestDownloadFile $OutFile
}

function Invoke-RestMethod {
  param([string]$Uri, [hashtable]$Headers)
  if ($Uri.EndsWith('/dist-tags')) {
    if ($global:installerTestNoRc) { return [pscustomobject]@{ latest = '2.0.0' } }
    return [pscustomobject]@{ latest = '2.0.0'; rc = '2.0.0' }
  }
  return [pscustomobject]@{ tag_name = 'v2.0.0' }
}

function Run-Installer([string]$dir, [switch]$fromText, [switch]$runSetup, [switch]$modifyPath) {
  $options = @{ Version = 'v2.0.0'; InstallDir = $dir }
  if (-not $modifyPath) { $options.NoModifyPath = $true }
  if (-not $runSetup) { $options.NoSetup = $true }
  if ($fromText) {
    & ([scriptblock]::Create((Get-Content $installer -Raw))) @options
  } else {
    & $installer @options
  }
}

try {
  $global:installerTestChecksumBody = $null
  $global:installerTestNoRc = $false
  $good = Build-Fake 'good' '2.0.0' '0' '0'
  $old = Build-Fake 'old' '1.0.0' '0' '0'
  $bad = Build-Fake 'bad' '2.0.0' '9' '0'
  $setupBad = Build-Fake 'setup-bad' '2.0.0' '0' '13'

  $argumentError = $null
  try { & $installer -Version v2.0.0 -Channel rc -NoSetup } catch { $argumentError = $_ }
  Assert ($null -ne $argumentError) 'version and channel flags were accepted together'
  Assert ($argumentError.Exception.Message -match 'cannot be combined') 'version and channel flags were accepted together'

  $priorChannel = $env:ORQ_CLI_CHANNEL
  try {
    $env:ORQ_CLI_CHANNEL = 'unknown'
    $channelError = $null
    try { & $installer -Version v2.0.0 -NoSetup } catch { $channelError = $_ }
    Assert ($null -ne $channelError) 'invalid channel environment variable was accepted'
    Assert ($channelError.Exception.Message -match 'unknown channel') "unexpected channel failure: $($channelError.Exception.Message)"
  } finally {
    if ($null -eq $priorChannel) { Remove-Item Env:ORQ_CLI_CHANNEL -ErrorAction SilentlyContinue }
    else { $env:ORQ_CLI_CHANNEL = $priorChannel }
  }

  $priorArchitecture = $env:PROCESSOR_ARCHITECTURE
  $priorArchitectureW6432 = $env:PROCESSOR_ARCHITEW6432
  try {
    $env:PROCESSOR_ARCHITECTURE = 'unsupported'
    Remove-Item Env:PROCESSOR_ARCHITEW6432 -ErrorAction SilentlyContinue
    $architectureError = $null
    try { & $installer -Version v2.0.0 -NoSetup } catch { $architectureError = $_ }
    Assert ($null -ne $architectureError) 'unsupported architecture was accepted'
    Assert ($architectureError.Exception.Message -match 'unsupported architecture') 'unsupported architecture was accepted'
  } finally {
    $env:PROCESSOR_ARCHITECTURE = $priorArchitecture
    if ($null -eq $priorArchitectureW6432) { Remove-Item Env:PROCESSOR_ARCHITEW6432 -ErrorAction SilentlyContinue }
    else { $env:PROCESSOR_ARCHITEW6432 = $priorArchitectureW6432 }
  }

  # A byte[] checksum response is what Windows PowerShell 5.1 receives from GitHub.
  $global:installerTestDownloadFile = $good
  $global:installerTestDigest = (Get-FileHash $good -Algorithm SHA256).Hash
  $freshDir = Join-Path $scratch 'fresh'
  Run-Installer $freshDir -fromText
  $freshTarget = Join-Path $freshDir 'orq.exe'
  Assert (Test-Path $freshTarget) 'scriptblock install did not create orq.exe'
  Assert ((Get-FileHash $freshTarget).Hash -eq $global:installerTestDigest) 'fresh install has wrong binary'

  # Stable and rc release discovery must lead to the same verified download.
  $stableDir = Join-Path $scratch 'stable-resolution'
  & $installer -InstallDir $stableDir -NoModifyPath -NoSetup
  Assert (Test-Path (Join-Path $stableDir 'orq.exe')) 'stable release resolution did not install'
  $rcDir = Join-Path $scratch 'rc-resolution'
  & $installer -Channel rc -InstallDir $rcDir -NoModifyPath -NoSetup
  Assert (Test-Path (Join-Path $rcDir 'orq.exe')) 'rc release resolution did not install'
  $global:installerTestNoRc = $true
  $noRcError = $null
  try { & $installer -Channel rc -NoSetup } catch { $noRcError = $_ }
  Assert ($null -ne $noRcError) 'missing rc dist-tag was accepted'
  Assert ($noRcError.Exception.Message -match 'no rc release is published') 'missing rc dist-tag produced the wrong error'
  $global:installerTestNoRc = $false

  # Corrupt and non-digest checksum bodies must not produce an executable.
  foreach ($body in @(('0' * 64), '<html>proxy</html>', '', '   ')) {
    $global:installerTestChecksumBody = $body
    $badSumDir = Join-Path $scratch ("bad-sum-" + [Guid]::NewGuid().ToString('N'))
    $sumError = $null
    try { Run-Installer $badSumDir } catch { $sumError = $_ }
    Assert ($null -ne $sumError) 'invalid checksum was accepted'
    Assert (-not (Test-Path (Join-Path $badSumDir 'orq.exe'))) 'invalid checksum installed a binary'
  }
  $global:installerTestChecksumBody = $null

  # An executable that prints a version and exits nonzero must not replace the old one.
  $upgradeDir = Join-Path $scratch 'upgrade'
  New-Item -ItemType Directory -Path $upgradeDir | Out-Null
  $upgradeTarget = Join-Path $upgradeDir 'orq.exe'
  Copy-Item $old $upgradeTarget
  $oldDigest = (Get-FileHash $upgradeTarget).Hash
  Set-Content "$upgradeTarget.previous" 'stale backup'
  $global:installerTestDownloadFile = $bad
  $global:installerTestDigest = (Get-FileHash $bad -Algorithm SHA256).Hash
  $upgradeError = $null
  try { Run-Installer $upgradeDir } catch { $upgradeError = $_ }
  Assert ($null -ne $upgradeError) 'nonzero version probe was accepted'
  Assert ($upgradeError.Exception.Message -match 'previous one is being restored') "unexpected upgrade failure: $($upgradeError.Exception.Message)"
  Assert (Test-Path $upgradeTarget) 'failed upgrade removed orq.exe'
  Assert ((Get-FileHash $upgradeTarget).Hash -eq $oldDigest) 'failed upgrade did not restore the old binary'
  Assert ((Get-Content "$upgradeTarget.previous" -Raw).Trim() -eq 'stale backup') 'failed upgrade touched a stale backup'

  # If the new executable is locked, report where the recoverable old one remains.
  $lockedDir = Join-Path $scratch 'locked-upgrade'
  New-Item -ItemType Directory -Path $lockedDir | Out-Null
  $global:installerTestLockedTarget = Join-Path $lockedDir 'orq.exe'
  Copy-Item $old $global:installerTestLockedTarget
  function Remove-Item {
    [CmdletBinding()]
    param([Parameter(Position = 0)][string]$Path, [switch]$Force, [switch]$Recurse)
    if ($Path -eq $global:installerTestLockedTarget) { throw 'simulated locked executable' }
    Microsoft.PowerShell.Management\Remove-Item @PSBoundParameters
  }
  try {
    $lockedError = $null
    try { Run-Installer $lockedDir } catch { $lockedError = $_ }
  } finally {
    Microsoft.PowerShell.Management\Remove-Item Function:Remove-Item
  }
  $backups = @(Get-ChildItem $lockedDir -Filter '.orq-previous-*.exe')
  Assert ($null -ne $lockedError) 'locked upgrade did not report a restore failure'
  Assert ($backups.Count -eq 1) 'locked upgrade did not retain the previous binary'
  Assert ($lockedError.Exception.Message.Contains($backups[0].FullName)) 'restore failure did not name the previous binary'
  Assert ((Get-FileHash $backups[0].FullName).Hash -eq $oldDigest) 'locked upgrade corrupted the previous binary'
  Remove-Variable installerTestLockedTarget -Scope Global

  # Setup errors must be visible to the caller after an otherwise valid install.
  $global:installerTestDownloadFile = $setupBad
  $global:installerTestDigest = (Get-FileHash $setupBad -Algorithm SHA256).Hash
  $setupDir = Join-Path $scratch 'setup'
  $setupError = $null
  try { Run-Installer $setupDir -runSetup } catch { $setupError = $_ }
  if ([Console]::IsInputRedirected) {
    Assert ($null -eq $setupError) "non-interactive setup was run: $setupError"
  } else {
    Assert ($null -ne $setupError) 'setup exit code was ignored'
    Assert ($setupError.Exception.Message -match 'setup exited 13') "unexpected setup failure: $($setupError.Exception.Message)"
  }
  Assert (Test-Path (Join-Path $setupDir 'orq.exe')) 'setup failure removed the installed CLI'

  # A registry write must preserve the existing value kind and update this process.
  $envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
  if (-not $envKey) { $envKey = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment') }
  $rawPathBefore = $envKey.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  $kindBefore = if ($null -ne $rawPathBefore) { $envKey.GetValueKind('Path') } else { $null }
  $processPathBefore = $env:Path
  try {
    $global:installerTestDownloadFile = $good
    $global:installerTestDigest = (Get-FileHash $good -Algorithm SHA256).Hash

    # A broadcast failure must leave a usable install after the registry write.
    function Add-Type { throw 'simulated Add-Type failure' }
    try {
      $broadcastDir = Join-Path $scratch 'broadcast-failure'
      Run-Installer $broadcastDir -modifyPath
      Assert (Test-Path (Join-Path $broadcastDir 'orq.exe')) 'broadcast failure aborted install'
    } finally {
      Remove-Item Function:Add-Type
      if ($null -eq $rawPathBefore) { $envKey.DeleteValue('Path', $false) }
      else { $envKey.SetValue('Path', $rawPathBefore, $kindBefore) }
      $env:Path = $processPathBefore
    }

    $pathDir = Join-Path $scratch 'path'
    Run-Installer $pathDir -modifyPath
    $rawPathAfter = [string]$envKey.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    Assert ($rawPathAfter.Split(';') -contains $pathDir) 'user PATH was not updated'
    if ($null -ne $kindBefore) { Assert ($envKey.GetValueKind('Path') -eq $kindBefore) 'user PATH value kind changed' }
    Assert ($env:Path.Split(';') -contains $pathDir) 'process PATH was not updated'

    # Expand tokens before comparing an existing user PATH entry.
    $expandedDir = Join-Path $scratch 'expanded-path'
    $rawExpanded = '%USERPROFILE%' + $expandedDir.Substring($env:USERPROFILE.Length)
    $envKey.SetValue('Path', $rawExpanded, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    Run-Installer $expandedDir -modifyPath
    $rawPathAfter = [string]$envKey.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    Assert ($rawPathAfter -eq $rawExpanded) 'expanded user PATH entry was duplicated'
    Assert ($envKey.GetValueKind('Path') -eq [Microsoft.Win32.RegistryValueKind]::ExpandString) 'expanded user PATH value kind changed'
  } finally {
    if ($null -eq $rawPathBefore) { $envKey.DeleteValue('Path', $false) }
    else { $envKey.SetValue('Path', $rawPathBefore, $kindBefore) }
    $env:Path = $processPathBefore
    $envKey.Close()
  }

  Write-Host 'PowerShell installer integration tests passed'
} finally {
  Remove-Variable installerTestDownloadFile, installerTestDigest, installerTestChecksumBody, installerTestNoRc, installerTestLockedTarget -Scope Global -ErrorAction SilentlyContinue
  Remove-Item $scratch -Recurse -Force -ErrorAction SilentlyContinue
}

# Native probes may leave LASTEXITCODE nonzero after an expected failure.
exit 0
