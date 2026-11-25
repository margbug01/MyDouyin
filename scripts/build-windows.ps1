#Requires -Version 7.0
param(
    [string]$OutDir,
    [switch]$SkipInstaller,
    [ValidateRange(1, 20)]
    [int]$KeepReleases = 1
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot
$revision = git -C $repo rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw 'Run from a Git checkout.' }
if (git -C $repo status --porcelain) { throw 'Commit the source before building a snapshot.' }
$managedOut = -not $OutDir
if ($managedOut) { $OutDir = Join-Path $repo "build.noindex/release-$($revision.Substring(0,12))" }
$OutDir = [IO.Path]::GetFullPath($OutDir)
$outExisted = Test-Path $OutDir
$succeeded = $false
$tempRoot = Join-Path $env:LOCALAPPDATA "Alma Go/temp/$(Get-Date -Format yyyy-MM-dd)"
$work = Join-Path $tempRoot "mydouyin-build-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $work -Force | Out-Null
$oldProxy = @($env:HTTP_PROXY, $env:HTTPS_PROXY, $env:ALL_PROXY)
try {
    git -C $repo archive --format=zip -o (Join-Path $work 'source.zip') $revision
    if ($LASTEXITCODE -ne 0) { throw 'Cannot archive the source commit.' }
    $source = Join-Path $work 'source'
    Expand-Archive (Join-Path $work 'source.zip') $source
    $resources = Join-Path $source 'resources/windows-amd64'
    New-Item -ItemType Directory -Path $resources -Force | Out-Null
    $info = @("MyDouyin source commit: $revision", "Built: $([DateTimeOffset]::Now.ToString('o'))")
    $ffmpegRoot = $null
    foreach ($name in @('ffmpeg','ffprobe')) {
        $binary = Get-Item (Get-Command "$name.exe" -ErrorAction Stop).Source
        if ($binary.LinkType) { $binary = $binary.ResolveLinkTarget($true) }
        if ($binary.Length -lt 1MB) { throw "$name.exe is not a complete FFmpeg binary." }
        Copy-Item $binary.FullName (Join-Path $resources "$name.exe")
        $info += "$name.exe SHA256: $((Get-FileHash $binary.FullName -Algorithm SHA256).Hash)"
        if ($name -eq 'ffmpeg') { $ffmpegRoot = Split-Path (Split-Path $binary.FullName) }
    }
    # Keep FFmpeg's license and upstream source/build information with its tools.
    Copy-Item (Join-Path $ffmpegRoot 'LICENSE') (Join-Path $resources 'FFmpeg-LICENSE.txt')
    Copy-Item (Join-Path $ffmpegRoot 'README.txt') (Join-Path $resources 'FFmpeg-README.txt')
    $info | Set-Content (Join-Path $resources 'BUILD-INFO.txt') -Encoding utf8
    $env:HTTP_PROXY = 'http://127.0.0.1:10808'
    $env:HTTPS_PROXY = $env:HTTP_PROXY
    $env:ALL_PROXY = 'socks5://127.0.0.1:10808'
    Push-Location $source
    try {
        $buildArgs = @('tool','mygo','build','-platform','windows/amd64','-o',$OutDir)
        if ($SkipInstaller) { $buildArgs += '-skip-installer' }
        & go @buildArgs
        if ($LASTEXITCODE -ne 0) { throw 'Windows build failed.' }
    } finally { Pop-Location }
    $succeeded = $true
    if ($managedOut) {
        $buildRoot = Join-Path $repo 'build.noindex'
        $oldReleases = Get-ChildItem -Path $buildRoot -Directory -Filter 'release-*' -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -ne $OutDir } |
            Sort-Object LastWriteTimeUtc -Descending |
            Select-Object -Skip ([Math]::Max(0, $KeepReleases - 1))
        foreach ($old in $oldReleases) {
            Remove-Item $old.FullName -Recurse -Force
        }
    }
    Write-Host "Source commit: $revision"
    Write-Host "Artifacts: $OutDir/windows-amd64"
} finally {
    $env:HTTP_PROXY, $env:HTTPS_PROXY, $env:ALL_PROXY = $oldProxy
    Remove-Item $work -Recurse -Force
    if (-not $succeeded -and $managedOut -and -not $outExisted -and (Test-Path $OutDir)) {
        Remove-Item $OutDir -Recurse -Force
    }
}
