#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Installs the latest (or pinned) grid-cli release on Windows.

.DESCRIPTION
    Downloads the release archive for the current architecture, verifies its
    checksum against the release's checksums.txt, and installs the "grid"
    binary. Never elevates (no UAC prompt): it installs into
    $env:ProgramFiles\grid-cli if that's writable without elevation, and
    otherwise falls back to a per-user directory it creates itself. If
    neither works it fails with instructions rather than prompting for admin.

.EXAMPLE
    irm https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.ps1 | iex

.PARAMETER Version
    Release tag to install (e.g. "v1.2.3"). Defaults to the latest release.
    Also settable via $env:GRID_INSTALL_VERSION.

.PARAMETER InstallDir
    Directory to install into. Also settable via $env:GRID_INSTALL_DIR.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:GRID_INSTALL_VERSION,
    [string]$InstallDir = $env:GRID_INSTALL_DIR
)

$ErrorActionPreference = "Stop"

$Repo = "anshul-kh/grid-cli"
$Project = "grid-cli" # .goreleaser.yaml's project_name - the archive's name prefix
$Binary = "grid"

function Write-Info($msg) { Write-Host $msg }
function Die($msg) {
    # `throw`, not `exit`: this script is typically run via `irm | iex`, and
    # `exit` there would close the user's entire interactive shell session
    # rather than just failing the install. A terminating error exits with a
    # non-zero code when run directly (e.g. `pwsh -File install.ps1`) without
    # that side effect.
    throw "error: $msg"
}

function Resolve-Arch {
    # PROCESSOR_ARCHITEW6432 reflects the real host architecture when this
    # script runs under a 32-bit PowerShell on a 64-bit Windows (WOW64);
    # PROCESSOR_ARCHITECTURE alone would misreport "x86" there.
    $native = $env:PROCESSOR_ARCHITEW6432
    if (-not $native) { $native = $env:PROCESSOR_ARCHITECTURE }
    switch ($native) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default { Die "unsupported architecture: $native" }
    }
}

function Resolve-Tag {
    if ($Version) { return $Version }
    try {
        $release = Invoke-RestMethod -UseBasicParsing `
            -Uri "https://api.github.com/repos/$Repo/releases/latest" `
            -Headers @{ "User-Agent" = "grid-cli-install.ps1" }
    } catch {
        Die "could not resolve the latest release (network issue?): $_"
    }
    if (-not $release.tag_name) { Die "no releases found for $Repo" }
    return $release.tag_name
}

function Test-DirWritable([string]$dir) {
    try {
        if (-not (Test-Path $dir)) {
            New-Item -ItemType Directory -Path $dir -Force -ErrorAction Stop | Out-Null
        }
        $probe = Join-Path $dir ".grid-install-write-test"
        [IO.File]::WriteAllText($probe, "")
        Remove-Item $probe -Force
        return $true
    } catch {
        return $false
    }
}

$arch = Resolve-Arch
$tag = Resolve-Tag
$ver = $tag.TrimStart("v")
$asset = "${Project}_${ver}_windows_${arch}.zip"
$baseUrl = "https://github.com/$Repo/releases/download/$tag"

Write-Info "Installing grid-cli $tag (windows/$arch)..."

$work = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $assetPath = Join-Path $work $asset
    $checksumsPath = Join-Path $work "checksums.txt"

    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$asset" -OutFile $assetPath
    } catch {
        Die "could not download $baseUrl/$asset (does release $tag have a windows/$arch build?): $_"
    }
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/checksums.txt" -OutFile $checksumsPath
    } catch {
        Die "could not download checksums.txt for ${tag}: $_"
    }

    # Verify the checksum before anything in the archive is ever executed.
    $line = Select-String -Path $checksumsPath -Pattern " $([regex]::Escape($asset))$"
    if (-not $line) { Die "no checksum entry for $asset in checksums.txt" }
    $want = ($line.Line -split '\s+')[0].ToLowerInvariant()
    $got = (Get-FileHash -Path $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($want -ne $got) { Die "checksum mismatch for $asset: expected $want, got $got" }
    Write-Info "Checksum verified."

    Expand-Archive -Path $assetPath -DestinationPath $work -Force
    $extractedBinary = Join-Path $work "$Binary.exe"
    if (-not (Test-Path $extractedBinary)) { Die "$Binary.exe was not found in $asset" }

    # Pick an install directory without ever elevating. Prefer the
    # conventional per-machine Program Files path if we can already write to
    # it without UAC; otherwise fall back to a per-user directory we can
    # create ourselves.
    if (-not $InstallDir) {
        $systemDir = Join-Path $env:ProgramFiles "grid-cli"
        if (Test-DirWritable $systemDir) {
            $InstallDir = $systemDir
        } else {
            $InstallDir = Join-Path $env:LOCALAPPDATA "grid-cli"
        }
    }

    if (-not (Test-DirWritable $InstallDir)) {
        Die @"
$InstallDir is not writable and could not be created.
This script never elevates (no UAC prompt). Either create $InstallDir
yourself with the permissions you need, or point elsewhere:
  `$env:GRID_INSTALL_DIR = "<a directory you own>"
  irm https://raw.githubusercontent.com/$Repo/main/install.ps1 | iex
"@
    }

    $destBinary = Join-Path $InstallDir "$Binary.exe"
    Copy-Item -Path $extractedBinary -Destination $destBinary -Force
    Write-Info "Installed $destBinary"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $pathEntries = @($userPath -split ";" | Where-Object { $_ })
    if ($pathEntries -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", ($userPath.TrimEnd(";") + ";$InstallDir"), "User")
        $env:Path = "$env:Path;$InstallDir"
        Write-Info ""
        Write-Info "Added $InstallDir to your user PATH. Restart your terminal for it to"
        Write-Info "take effect everywhere, or use it immediately in this session."
    }

    & $destBinary --version
} finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
