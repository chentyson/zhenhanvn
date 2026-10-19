# Publish v2rayN as a new GitHub release (single-file Windows x64 exe).
#
# What it does:
#   1. Bumps the 4th version segment in v2rayN\Directory.Build.props by +1
#      (e.g. 7.24.9.5 -> 7.24.9.6). Use -NoBump to keep the current version.
#   2. Builds the single-file exe by calling publish.ps1.
#   3. Zips the exe into zhenhanvn-windows-64.zip.
#   4. Creates a GitHub release (tag v<version>) and uploads the zip as an
#      asset, using a fixed token so no interactive login is required.
#   5. (Optional, -Push) Commits the version bump, tags it, and pushes to origin.
#
# Token resolution order: -Token param > $env:GITHUB_TOKEN > .\.github-token file.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File .\publish-github.ps1
#   powershell -ExecutionPolicy Bypass -File .\publish-github.ps1 -NoPush
#   powershell -ExecutionPolicy Bypass -File .\publish-github.ps1 -Token ghp_xxx

param(
    [string]$Token = "",
    [string]$Repo = "chentyson/zhenhanvn",
    [string]$AssetName = "zhenhanvn-windows-64.zip",
    [switch]$NoBump,
    [switch]$NoPush
)

$ErrorActionPreference = "Stop"
$ProjectRoot   = Split-Path -Parent $MyInvocation.MyCommand.Path
$PropsFile     = Join-Path $ProjectRoot "v2rayN\Directory.Build.props"
$PublishScript = Join-Path $ProjectRoot "publish.ps1"
$OutputDir     = Join-Path $ProjectRoot "publish"
$ExePath       = Join-Path $OutputDir "zhenhanvn.exe"
$ZipPath       = Join-Path $ProjectRoot $AssetName

# --- Resolve the GitHub token (fixed, non-interactive) ---
if ([string]::IsNullOrWhiteSpace($Token)) {
    if ($env:GITHUB_TOKEN) {
        $Token = $env:GITHUB_TOKEN
    }
    else {
        $tokenFile = Join-Path $ProjectRoot ".github-token"
        if (Test-Path $tokenFile) {
            $Token = (Get-Content $tokenFile -Raw).Trim()
        }
    }
}
if ([string]::IsNullOrWhiteSpace($Token)) {
    Write-Error "No GitHub token found. Pass -Token, set GITHUB_TOKEN, or create .\.github-token."
    exit 1
}
$headers = @{
    "Authorization" = "token $Token"
    "Accept"        = "application/vnd.github+json"
    "User-Agent"    = "zhenhanvn-publish"
}

# Sanity-check the token before doing any work.
try {
    $me = Invoke-RestMethod -Uri "https://api.github.com/user" -Headers $headers -Method Get
    Write-Host "Authenticated as: $($me.login)" -ForegroundColor Cyan
}
catch {
    Write-Error "GitHub token is invalid or lacks access: $($_.Exception.Message)"
    exit 1
}

# --- Bump the version tail (+1) ---
$propsContent = [System.IO.File]::ReadAllText($PropsFile)
if (-not $NoBump) {
    if ($propsContent -match '<Version>(\d+)\.(\d+)\.(\d+)\.(\d+)</Version>') {
        $major = $Matches[1]; $minor = $Matches[2]; $patch = $Matches[3]; $tail = [int]$Matches[4]
        $tail++
        $newVersion = "$major.$minor.$patch.$tail"
        $propsContent = [regex]::Replace($propsContent, '<Version>[\d.]+</Version>', "<Version>$newVersion</Version>")
        [System.IO.File]::WriteAllText($PropsFile, $propsContent)
        Write-Host "Bumped version to $newVersion" -ForegroundColor Green
    }
    else {
        Write-Error "Could not find a 4-part <Version> in $PropsFile"
        exit 1
    }
}
else {
    $newVersion = ([regex]::Match($propsContent, '<Version>([\d.]+)</Version>').Groups[1].Value)
    Write-Host "Using current version $newVersion (no bump)" -ForegroundColor Cyan
}

# --- Build the single-file exe ---
Write-Host "Building single-file exe via publish.ps1 ..." -ForegroundColor Cyan
& powershell -NoProfile -ExecutionPolicy Bypass -File $PublishScript
if ($LASTEXITCODE -ne 0) {
    Write-Error "publish.ps1 failed (exit $LASTEXITCODE)."
    exit 1
}
if (-not (Test-Path $ExePath)) {
    Write-Error "Expected exe not found at $ExePath"
    exit 1
}

# --- Zip the exe ---
if (Test-Path $ZipPath) { Remove-Item $ZipPath -Force }
Compress-Archive -Path $ExePath -DestinationPath $ZipPath
$zipSize = (Get-Item $ZipPath).Length
Write-Host "Created $AssetName ($([math]::Round($zipSize / 1MB, 2)) MB)" -ForegroundColor Green

# --- Create the release ---
$tag = "v$newVersion"
Write-Host "Creating GitHub release $tag ..." -ForegroundColor Cyan
$releaseBody = @{
    tag_name              = $tag
    name                  = $tag
    target_commitish      = "main"
    draft                 = $false
    prerelease            = $false
    generate_release_notes = $false
    body                  = "zhenhanvn $newVersion`n`nSingle-file Windows x64 build."
} | ConvertTo-Json
try {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases" `
        -Headers $headers -Method Post -Body $releaseBody -ContentType "application/json"
}
catch {
    Write-Host "Create failed ($($_.Exception.Message)); trying to fetch existing release $tag" -ForegroundColor Yellow
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/tags/$tag" -Headers $headers -Method Get
}
Write-Host "Release: $($release.html_url)" -ForegroundColor Green

# --- Upload the asset ---
$uploadUri = "https://uploads.github.com/repos/$Repo/releases/$($release.id)/assets?name=$AssetName"
$uploadHeaders = @{
    "Authorization" = "token $Token"
    "User-Agent"    = "zhenhanvn-publish"
}
Write-Host "Uploading asset (this may take a minute) ..." -ForegroundColor Cyan
$asset = Invoke-RestMethod -Uri $uploadUri -Headers $uploadHeaders -Method Post -InFile $ZipPath -ContentType "application/octet-stream"
Write-Host "Uploaded: $($asset.name) ($([math]::Round($asset.size / 1MB, 2)) MB)" -ForegroundColor Green
Write-Host "Download: $($asset.browser_download_url)" -ForegroundColor Cyan

# --- Optional: commit the version bump, tag, and push ---
if (-not $NoPush) {
    Push-Location $ProjectRoot
    try {
        $pushUrl = "https://x-access-token:$Token@github.com/$Repo.git"
        git add v2rayN/Directory.Build.props
        git commit -m "Bump version to $newVersion" | Out-Null
        git tag -f $tag | Out-Null
        git push "$pushUrl" main 2>&1 | Out-Null
        git push "$pushUrl" $tag 2>&1 | Out-Null
        Write-Host "Committed version bump and pushed main + tag $tag" -ForegroundColor Green
    }
    catch {
        Write-Host "Git push step failed (release was still created): $($_.Exception.Message)" -ForegroundColor Yellow
    }
    finally {
        Pop-Location
    }
}

Write-Host ""
Write-Host "Done. Release: $($release.html_url)" -ForegroundColor Green
