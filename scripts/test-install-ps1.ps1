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
    return [pscustomobject]@{ Content = [Text.Encoding]::ASCII.GetBytes("$script:digest  orq-win32-x64.exe") }
  }
  Copy-Item $script:downloadFile $OutFile
}

function Run-Installer([string]$dir, [switch]$fromText, [switch]$runSetup) {
  $options = @{ Version = 'v2.0.0'; InstallDir = $dir; NoModifyPath = $true }
  if (-not $runSetup) { $options.NoSetup = $true }
  if ($fromText) {
    & ([scriptblock]::Create((Get-Content $installer -Raw))) @options
  } else {
    & $installer @options
  }
}

try {
  $good = Build-Fake 'good' '2.0.0' '0' '0'
  $old = Build-Fake 'old' '1.0.0' '0' '0'
  $bad = Build-Fake 'bad' '2.0.0' '9' '0'
  $setupBad = Build-Fake 'setup-bad' '2.0.0' '0' '13'

  # A byte[] checksum response is what Windows PowerShell 5.1 receives from GitHub.
  $script:downloadFile = $good
  $script:digest = (Get-FileHash $good -Algorithm SHA256).Hash
  $freshDir = Join-Path $scratch 'fresh'
  Run-Installer $freshDir -fromText
  $freshTarget = Join-Path $freshDir 'orq.exe'
  Assert (Test-Path $freshTarget) 'scriptblock install did not create orq.exe'
  Assert ((Get-FileHash $freshTarget).Hash -eq $script:digest) 'fresh install has wrong binary'

  # An executable that prints a version and exits nonzero must not replace the old one.
  $upgradeDir = Join-Path $scratch 'upgrade'
  New-Item -ItemType Directory -Path $upgradeDir | Out-Null
  $upgradeTarget = Join-Path $upgradeDir 'orq.exe'
  Copy-Item $old $upgradeTarget
  $oldDigest = (Get-FileHash $upgradeTarget).Hash
  $script:downloadFile = $bad
  $script:digest = (Get-FileHash $bad -Algorithm SHA256).Hash
  $upgradeError = $null
  try { Run-Installer $upgradeDir } catch { $upgradeError = $_ }
  Assert ($null -ne $upgradeError) 'nonzero version probe was accepted'
  Assert ($upgradeError.Exception.Message -match 'previous one is being restored') "unexpected upgrade failure: $($upgradeError.Exception.Message)"
  Assert (Test-Path $upgradeTarget) 'failed upgrade removed orq.exe'
  Assert ((Get-FileHash $upgradeTarget).Hash -eq $oldDigest) 'failed upgrade did not restore the old binary'
  Assert (-not (Test-Path "$upgradeTarget.previous")) 'failed upgrade left a backup'

  # Setup errors must be visible to the caller after an otherwise valid install.
  $script:downloadFile = $setupBad
  $script:digest = (Get-FileHash $setupBad -Algorithm SHA256).Hash
  $setupDir = Join-Path $scratch 'setup'
  $setupError = $null
  try { Run-Installer $setupDir -runSetup } catch { $setupError = $_ }
  Assert ($null -ne $setupError) 'setup exit code was ignored'
  Assert ($setupError.Exception.Message -match 'setup exited 13') "unexpected setup failure: $($setupError.Exception.Message)"
  Assert (Test-Path (Join-Path $setupDir 'orq.exe')) 'setup failure removed the installed CLI'

  Write-Host 'PowerShell installer integration tests passed'
} finally {
  Remove-Item $scratch -Recurse -Force -ErrorAction SilentlyContinue
}
