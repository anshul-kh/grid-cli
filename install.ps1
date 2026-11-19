#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Installs the latest (or pinned) grid-cli release on Windows.

.DESCRIPTION
    Downloads the release archive for the current architecture, verifies its
    checksum against the release's checksums.txt, and installs the "grid"
    binary. When the release has no windows/<arch> archive (or the download
    404s) and a Go toolchain is available, it clones the tag and builds from
    source instead.

    Never elevates (no UAC prompt): it installs into
    $env:ProgramFiles\grid-cli if that's writable without elevation, and
    otherwise falls back to a per-user directory it creates itself. If
    neither works it fails with instructions rather than prompting for admin.

.EXAMPLE
    irm https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.ps1 | iex

.EXAMPLE
    # Force a source build (needs git + go on PATH)
    $env:GRID_INSTALL_METHOD = "source"; irm https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.ps1 | iex

.PARAMETER Version
    Release tag to install (e.g. "v1.2.3"). Defaults to the latest release.
    Also settable via $env:GRID_INSTALL_VERSION.

.PARAMETER InstallDir
    Directory to install into. Also settable via $env:GRID_INSTALL_DIR.

.PARAMETER Method
    auto | archive | source. Also settable via $env:GRID_INSTALL_METHOD.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:GRID_INSTALL_VERSION,
    [string]$InstallDir = $env:GRID_INSTALL_DIR,
    [string]$Method = $(if ($env:GRID_INSTALL_METHOD) { $env:GRID_INSTALL_METHOD } else { "auto" })
)

$ErrorActionPreference = "Stop"

$Repo = "anshul-kh/grid-cli"
$Project = "grid-cli" # .goreleaser.yaml's project_name - the archive's name prefix
$Binary = "grid"
$Module = "github.com/anshul-kh/grid-cli"

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

function Resolve-InstallDir {
    if ($InstallDir) { return $InstallDir }
    $systemDir = Join-Path $env:ProgramFiles "grid-cli"
    if (Test-DirWritable $systemDir) { return $systemDir }
    return (Join-Path $env:LOCALAPPDATA "grid-cli")
}

function Install-Binary([string]$sourceBinary) {
    $dir = Resolve-InstallDir
    if (-not (Test-DirWritable $dir)) {
        Die @"
$dir is not writable and could not be created.
This script never elevates (no UAC prompt). Either create $dir
yourself with the permissions you need, or point elsewhere:
  `$env:GRID_INSTALL_DIR = "<a directory you own>"
  irm https://raw.githubusercontent.com/$Repo/main/install.ps1 | iex
"@
    }

    $destBinary = Join-Path $dir "$Binary.exe"
    Copy-Item -Path $sourceBinary -Destination $destBinary -Force
    Write-Info "Installed $destBinary"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $pathEntries = @($userPath -split ";" | Where-Object { $_ })
    if ($pathEntries -notcontains $dir) {
        [Environment]::SetEnvironmentVariable("Path", ($userPath.TrimEnd(";") + ";$dir"), "User")
        $env:Path = "$env:Path;$dir"
        Write-Info ""
        Write-Info "Added $dir to your user PATH. Restart your terminal for it to"
        Write-Info "take effect everywhere, or use it immediately in this session."
    }

    & $destBinary --version
}

function Install-FromRelease([string]$tag, [string]$arch, [string]$work) {
    $ver = $tag.TrimStart("v")
    $asset = "${Project}_${ver}_windows_${arch}.zip"
    $baseUrl = "https://github.com/$Repo/releases/download/$tag"
    $assetPath = Join-Path $work $asset
    $checksumsPath = Join-Path $work "checksums.txt"

    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$asset" -OutFile $assetPath
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/checksums.txt" -OutFile $checksumsPath

    # Verify the checksum before anything in the archive is ever executed. A
    # mismatch is fatal and never triggers a source fallback.
    $line = Select-String -Path $checksumsPath -Pattern " $([regex]::Escape($asset))$"
    if (-not $line) { Die "no checksum entry for $asset in checksums.txt" }
    $want = ($line.Line -split '\s+')[0].ToLowerInvariant()
    $got = (Get-FileHash -Path $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($want -ne $got) { Die "checksum mismatch for $asset: expected $want, got $got" }
    Write-Info "Checksum verified."

    Expand-Archive -Path $assetPath -DestinationPath $work -Force
    $extractedBinary = Join-Path $work "$Binary.exe"
    if (-not (Test-Path $extractedBinary)) { Die "$Binary.exe was not found in $asset" }

    Install-Binary $extractedBinary
}

function Build-FromSource([string]$tag, [string]$work) {
    foreach ($tool in @("git", "go")) {
        if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
            Die @"
building from source needs $tool on PATH.
Install Go 1.27+ (https://go.dev/dl/) and Git (https://git-scm.com/download/win),
or pin a release that ships a windows archive: `$env:GRID_INSTALL_VERSION = "<tag>"
"@
        }
    }

    $src = Join-Path $work "src"
    $ver = $tag.TrimStart("v")
    Write-Info "Building grid-cli $tag from source..."
    git clone --depth 1 --branch $tag "https://github.com/$Repo" $src
    if ($LASTEXITCODE -ne 0) { Die "could not clone $Repo at $tag" }
    $commit = (git -C $src rev-parse --short HEAD)

    Push-Location $src
    try {
        go build -trimpath `
            -ldflags "-s -w -X $Module/internal/buildinfo.Version=$ver -X $Module/internal/buildinfo.Commit=$commit" `
            -o (Join-Path $work "$Binary.exe") .
        if ($LASTEXITCODE -ne 0) { Die "go build failed (Go 1.27+ is required)" }
    } finally {
        Pop-Location
    }

    Install-Binary (Join-Path $work "$Binary.exe")
}

if (@("auto", "archive", "source") -notcontains $Method) {
    Die "unknown GRID_INSTALL_METHOD '$Method' (expected auto, archive or source)"
}

$arch = Resolve-Arch
$tag = Resolve-Tag
Write-Info "Installing grid-cli $tag (windows/$arch)..."

$work = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $work | Out-Null
try {
    if ($Method -eq "source") {
        Build-FromSource $tag $work
    }
    else {
        try {
            Install-FromRelease $tag $arch $work
        }
        catch {
            $reason = $_.Exception.Message
            if ($Method -eq "archive") { throw }
            Write-Info ""
            Write-Info "Release $tag has no usable windows/$arch archive: $reason"
            Write-Info "Falling back to a source build."
            Build-FromSource $tag $work
        }
    }
} finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
