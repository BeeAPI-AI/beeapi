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
  $Base = $env:BEEAPI_DOWNLOAD_BASE.TrimEnd("/")
} elseif ($Version -eq "latest") {
  $Base = "https://github.com/BeeAPI-AI/beeapi/releases/latest/download"
} else {
  $Base = "https://github.com/BeeAPI-AI/beeapi/releases/download/$Version"
}

if (-not $Base.StartsWith("https://")) {
  throw "Download base must use HTTPS"
}

$TempDir = Join-Path ([IO.Path]::GetTempPath()) ("getbeeapi-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TempDir | Out-Null

try {
  $Archive = Join-Path $TempDir $Asset
  $Checksum = "$Archive.sha256"
  Write-Host "Downloading $Asset…"
  Invoke-WebRequest -UseBasicParsing -Uri "$Base/$Asset" -OutFile $Archive
  Invoke-WebRequest -UseBasicParsing -Uri "$Base/$Asset.sha256" -OutFile $Checksum

  $Expected = (Get-Content -Raw -LiteralPath $Checksum).Trim().ToLowerInvariant()
  $Actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash.ToLowerInvariant()
  if ($Expected -ne $Actual) {
    throw "SHA-256 verification failed"
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
    $env:Path = "$env:Path;$InstallDir"
    Write-Host "Added $InstallDir to your user PATH."
  }

  Write-Host "`nInstalled beeapi to $Target"
  if (-not $NoSetup) {
    & $Target
  }
} finally {
  Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
}
