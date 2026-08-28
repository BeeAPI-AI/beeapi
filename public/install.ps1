[CmdletBinding()]
param(
  [string]$Version = "latest",
  [string]$InstallDir = $(if ($env:BEEAPI_INSTALL_DIR) { $env:BEEAPI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "GetBeeAPI\bin" }),
  [switch]$NoSetup
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
  "X64" { $Arch = "amd64" }
  "Arm64" { $Arch = "arm64" }
  default { throw "Unsupported CPU architecture: $($_)" }
}

$Asset = "beeapi_windows_$Arch.zip"
if ($env:BEEAPI_DOWNLOAD_BASE) {
  $Bases = @($env:BEEAPI_DOWNLOAD_BASE.TrimEnd("/"))
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

try {
  $Archive = Join-Path $TempDir $Asset
  $Checksum = "$Archive.sha256"
  $Downloaded = $false
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
  if (-not $NoSetup) {
    & $Target
  }
} finally {
  Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
}
