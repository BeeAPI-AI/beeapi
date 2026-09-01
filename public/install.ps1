[CmdletBinding()]
param(
  [string]$Version = "latest",
  [string]$InstallDir = $(if ($env:BEEAPI_INSTALL_DIR) { $env:BEEAPI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "GetBeeAPI\bin" }),
  [switch]$NoSetup
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# Windows PowerShell 5.1 can run on .NET Framework versions where
# RuntimeInformation.OSArchitecture is unavailable. PROCESSOR_ARCHITEW6432
# reports the native OS architecture when a 32-bit host runs on 64-bit Windows.
$Architecture = [string]$env:PROCESSOR_ARCHITEW6432
if ([string]::IsNullOrWhiteSpace($Architecture)) {
  $Architecture = [string]$env:PROCESSOR_ARCHITECTURE
}

switch ($Architecture.ToUpperInvariant()) {
  "AMD64" { $Arch = "amd64" }
  "X64" { $Arch = "amd64" }
  "ARM64" { $Arch = "arm64" }
  default { throw "Unsupported CPU architecture: $Architecture" }
}

# Older Windows PowerShell sessions can still default to TLS 1.0. Keep any
# protocols already enabled and add TLS 1.2 for Cloudflare and GitHub downloads.
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
  # PowerShell 7 uses HttpClient and does not require this compatibility path.
}

$Asset = "beeapi_windows_$Arch.zip"
if ($env:BEEAPI_DOWNLOAD_BASE) {
  $Bases = @($env:BEEAPI_DOWNLOAD_BASE.TrimEnd([char]"/"))
} elseif ($Version -eq "latest") {
  $Bases = @(
    "https://getbeeapi.com/releases/latest/download",
    "https://github.com/BeeAPI-AI/beeapi/releases/latest/download"
  )
} else {
  $Bases = @(
    "https://getbeeapi.com/releases/$Version/download",
    "https://github.com/BeeAPI-AI/beeapi/releases/download/$Version"
  )
}

foreach ($CandidateBase in $Bases) {
  if (-not $CandidateBase.StartsWith("https://")) {
    throw "Download base must use HTTPS"
  }
}

$TempDir = Join-Path ([IO.Path]::GetTempPath()) ("getbeeapi-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TempDir | Out-Null

function Get-InstallSource([string]$BaseUrl) {
  if ($BaseUrl.StartsWith("https://getbeeapi.com/")) { return "getbeeapi" }
  if ($BaseUrl.StartsWith("https://github.com/")) { return "github" }
  return "custom"
}

function Send-VerifiedInstall([string]$InstalledVersion, [string]$InstallSource) {
  if ($env:BEEAPI_DISABLE_INSTALL_STATS -eq "1" -or
      $env:DO_NOT_TRACK -eq "1" -or
      $env:BEEAPI_TEST_ASSET_DIR) {
    return
  }

  $StatsUrl = if ($env:BEEAPI_INSTALL_STATS_URL) {
    [string]$env:BEEAPI_INSTALL_STATS_URL
  } else {
    "https://getbeeapi.com/api/install-events"
  }
  if (-not $StatsUrl.StartsWith("https://")) { return }

  $Payload = @{
    event_id = [Guid]::NewGuid().ToString("N").ToLowerInvariant()
    version = $InstalledVersion
    os = "windows"
    arch = $Arch
    source = $InstallSource
    installer = "powershell"
  } | ConvertTo-Json -Compress

  try {
    Invoke-RestMethod -Method Post -Uri $StatsUrl -ContentType "application/json" `
      -Body $Payload -TimeoutSec 3 | Out-Null
  } catch {
    # Statistics are best-effort and never prevent a verified installation.
  }
}

try {
  $Archive = Join-Path $TempDir $Asset
  $Checksum = "$Archive.sha256"
  $Downloaded = $false
  $InstallSource = "custom"
  foreach ($CandidateBase in $Bases) {
    try {
      Remove-Item -LiteralPath $Archive, $Checksum -Force -ErrorAction SilentlyContinue
      Write-Host "Downloading $Asset from $CandidateBase…"
      Invoke-WebRequest -UseBasicParsing -Uri "$CandidateBase/$Asset" -OutFile $Archive
      Invoke-WebRequest -UseBasicParsing -Uri "$CandidateBase/$Asset.sha256" -OutFile $Checksum

      $Expected = (Get-Content -Raw -LiteralPath $Checksum).Trim().ToLowerInvariant()
      $Actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash.ToLowerInvariant()
      if ($Expected -notmatch "^[0-9a-f]{64}$" -or $Expected -ne $Actual) {
        throw "SHA-256 verification failed for this source"
      }
      $Downloaded = $true
      $InstallSource = Get-InstallSource $CandidateBase
      break
    } catch {
      Write-Warning "This source is unavailable; trying the next verified source."
    }
  }
  if (-not $Downloaded) {
    throw "Unable to download the BeeAPI release from any source."
  }

  $Extracted = Join-Path $TempDir "extracted"
  Expand-Archive -LiteralPath $Archive -DestinationPath $Extracted -Force
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  $Target = Join-Path $InstallDir "beeapi.exe"
  Copy-Item -Force -LiteralPath (Join-Path $Extracted "beeapi.exe") -Destination $Target

  $VersionLines = @(& $Target --version 2>$null)
  $VersionExitCode = $LASTEXITCODE
  if ($VersionExitCode -ne 0 -or $VersionLines.Count -eq 0) {
    throw "Installed binary could not complete its version check."
  }
  $InstalledVersion = ([string]$VersionLines[0]).Trim()
  if ($InstalledVersion -notmatch "^[0-9A-Za-z._+-]+$") {
    throw "Installed binary returned an invalid version."
  }

  $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $Parts = @($UserPath -split ";" | Where-Object { $_ })
  if ($Parts -notcontains $InstallDir) {
    $NewPath = (@($Parts) + $InstallDir) -join ";"
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    Write-Host "Added $InstallDir to your user PATH."
  }
  $CurrentParts = @($env:Path -split ";" | Where-Object { $_ })
  if ($CurrentParts -notcontains $InstallDir) {
    $env:Path = "$env:Path;$InstallDir"
  }

  Write-Host "`nInstalled beeapi to $Target"
  Write-Host "The command is ready: beeapi"
  Send-VerifiedInstall $InstalledVersion $InstallSource
  if (-not $NoSetup) {
    & $Target
  }
} finally {
  Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
}
