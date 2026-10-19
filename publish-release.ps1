# Build and publish a zhenhanvn GitHub release (Windows x64 + macOS x64/arm64).
#
# What it does:
#   1. Reads the version from v2rayN\Directory.Build.props (bump it manually first).
#   2. Builds and packages:
#        - zhenhanvn-windows-64.zip       (single-file WPF exe)
#        - zhenhanvn-macos-64.zip         (.app bundle, Intel)
#        - zhenhanvn-macos-arm64.zip      (.app bundle, Apple Silicon)
#      macOS binaries are cross-built on Windows with `dotnet publish -r osx-*`.
#   3. Creates (or reuses) GitHub release v<version> and uploads the three assets.
#   4. Unless -NoPush: commits the working tree, tags v<version> and pushes to origin.
#
# Token resolution order: -Token param > $env:GITHUB_TOKEN > .\.github-token file.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File .\publish-release.ps1
#   powershell -ExecutionPolicy Bypass -File .\publish-release.ps1 -SkipBuild   # reuse artifacts
#   powershell -ExecutionPolicy Bypass -File .\publish-release.ps1 -NoPush
#   powershell -ExecutionPolicy Bypass -File .\publish-release.ps1 -SkipBuild -NoPush

param(
    [string]$Token = "",
    [string]$Repo = "chentyson/zhenhanvn",
    [switch]$SkipBuild,
    [switch]$NoPush
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$PropsFile   = Join-Path $ProjectRoot "v2rayN\Directory.Build.props"
$PublishExe  = Join-Path $ProjectRoot "publish\zhenhanvn.exe"
$BuildRoot   = Join-Path $env:TEMP "zh-build"
$Python      = (Get-Command python -ErrorAction Stop).Source

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

# --- Version / tag ---
$propsContent = [System.IO.File]::ReadAllText($PropsFile)
$version = ([regex]::Match($propsContent, '<Version>([\d.]+)</Version>')).Groups[1].Value
if ([string]::IsNullOrWhiteSpace($version)) {
    Write-Error "Could not read <Version> from $PropsFile"
    exit 1
}
$tag = "v$version"
Write-Host "Releasing $tag" -ForegroundColor Cyan

# --- Sanity-check the token ---
try {
    $me = Invoke-RestMethod -Uri "https://api.github.com/user" -Headers $headers -Method Get
    Write-Host "Authenticated as: $($me.login)" -ForegroundColor Cyan
}
catch {
    Write-Error "GitHub token is invalid or lacks access: $($_.Exception.Message)"
    exit 1
}

$assets = @(
    (Join-Path $ProjectRoot "zhenhanvn-windows-64.zip"),
    (Join-Path $ProjectRoot "zhenhanvn-macos-64.zip"),
    (Join-Path $ProjectRoot "zhenhanvn-macos-arm64.zip")
)

# --- Build everything ---
if (-not $SkipBuild) {
    New-Item -ItemType Directory -Force -Path $BuildRoot | Out-Null

    Write-Host "Building Windows single-file exe ..." -ForegroundColor Cyan
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $ProjectRoot "publish.ps1")
    if ($LASTEXITCODE -ne 0) { Write-Error "publish.ps1 failed."; exit 1 }

    Write-Host "Zipping Windows exe ..." -ForegroundColor Cyan
    $winZip = $assets[0]
    if (Test-Path $winZip) { Remove-Item $winZip -Force }
    Compress-Archive -Path $PublishExe -DestinationPath $winZip -CompressionLevel Optimal

    foreach ($arch in @(
            @{ Name = "macos-64";    Rid = "osx-x64";   Src = "v2rayN-macos-64.zip" },
            @{ Name = "macos-arm64"; Rid = "osx-arm64"; Src = "v2rayN-macos-arm64.zip" })) {
        $publishDir = Join-Path $BuildRoot "publish-$($arch.Rid)"
        Write-Host "Publishing $($arch.Rid) ..." -ForegroundColor Cyan
        & dotnet publish (Join-Path $ProjectRoot "v2rayN\v2rayN.Desktop\v2rayN.Desktop.csproj") `
            -c Release -r $arch.Rid -p:SelfContained=true -o $publishDir
        if ($LASTEXITCODE -ne 0) { Write-Error "publish for $($arch.Rid) failed."; exit 1 }

        Write-Host "Packaging $($arch.Name) .app zip ..." -ForegroundColor Cyan
        $zip = Join-Path $ProjectRoot ("zhenhanvn-{0}.zip" -f $arch.Name)
        $coreZip = Join-Path $BuildRoot $arch.Src
        & $Python (Join-Path $ProjectRoot "package-osx-zhenhanvn.py") `
            --arch $arch.Name --publish $publishDir --version $version `
            --output $zip --cache-dir $BuildRoot
        if ($LASTEXITCODE -ne 0) { Write-Error "macOS packaging for $($arch.Name) failed."; exit 1 }
    }
}

foreach ($asset in $assets) {
    if (-not (Test-Path $asset)) {
        Write-Error "Missing release asset: $asset (run without -SkipBuild)"
        exit 1
    }
}

# --- Commit, tag and push (before creating the release so the tag exists) ---
if (-not $NoPush) {
    Push-Location $ProjectRoot
    try {
        $pushUrl = "https://x-access-token:$Token@github.com/$Repo.git"
        git add -u
        git add package-osx-zhenhanvn.py package-osx-zhenhanvn.sh publish.ps1 publish-github.ps1 publish-release.ps1 v2rayN/v2rayN/EmbeddedCoreExtractor.cs
        git diff --cached --quiet
        if ($LASTEXITCODE -ne 0) {
            git commit -m "Release ${tag}: macOS fixes (bundle permissions, in-app .app update, restart, Info.plist)" | Out-Null
            Write-Host "Committed release snapshot" -ForegroundColor Green
        }
        else {
            Write-Host "No source changes to commit (artifacts only)" -ForegroundColor Cyan
        }
        git tag -f $tag | Out-Null
        git push "$pushUrl" main 2>&1 | Out-Null
        git push "$pushUrl" $tag --force 2>&1 | Out-Null
        Write-Host "Pushed main + tag $tag" -ForegroundColor Green
    }
    catch {
        Write-Host "Git push step failed (release will still be created): $($_.Exception.Message)" -ForegroundColor Yellow
    }
    finally {
        Pop-Location
    }
}

# --- Create or reuse the release ---
$sha = @{}
foreach ($asset in $assets) {
    $sha[(Split-Path $asset -Leaf)] = (Get-FileHash $asset -Algorithm SHA256).Hash.ToLowerInvariant()
}
$body = @"
zhenhanvn $version

### macOS 修复 / macOS fixes
- macOS 安装包现在带正确的 Unix 可执行权限（主程序与 xray/mihomo/sing-box），此前的 macOS 压缩包解压后无法启动。
- 应用内更新在 macOS 上改为由独立脚本替换整个 .app（等待退出 → 替换 → 修复权限/隔离属性 → ad-hoc 重新签名 → 自动重启）；此前使用 Windows 专用 tar.exe，macOS 更新必然失败。
- 恢复配置/更新后的“重启”在 macOS/Linux 上不再走 Windows UAC 路径，改为通过 .app 重新启动并等待单实例锁释放。
- Info.plist 增加 CFBundleVersion，CFBundleShortVersionString 归一化为三段，最低系统版本修正为 13.6（libe_sqlite3 要求 13.5）。
- 最低系统要求：macOS 13.6+（Apple Silicon / Intel 分别对应 arm64 / 64 包）。

### Assets
- ``zhenhanvn-windows-64.zip``  — Windows x64 单文件
- ``zhenhanvn-macos-arm64.zip`` — macOS Apple Silicon (.app)
- ``zhenhanvn-macos-64.zip``    — macOS Intel (.app)

### SHA256
$(($sha.GetEnumerator() | Sort-Object Name | ForEach-Object { "- ``$($_.Key)``: $($_.Value)" }) -join "`n")
"@

$releaseBody = @{
    tag_name               = $tag
    name                   = $tag
    target_commitish       = "main"
    draft                  = $false
    prerelease             = $false
    generate_release_notes = $false
    body                   = $body
} | ConvertTo-Json

Write-Host "Creating GitHub release $tag ..." -ForegroundColor Cyan
try {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases" `
        -Headers $headers -Method Post -Body $releaseBody -ContentType "application/json"
}
catch {
    Write-Host "Create failed ($($_.Exception.Message)); trying to fetch existing release $tag" -ForegroundColor Yellow
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/tags/$tag" -Headers $headers -Method Get
}
Write-Host "Release: $($release.html_url)" -ForegroundColor Green

# --- Upload assets (replace an existing asset with the same name) ---
foreach ($asset in $assets) {
    $name = Split-Path $asset -Leaf
    $existing = $release.assets | Where-Object { $_.name -eq $name }
    if ($existing) {
        Write-Host "Deleting existing asset $name ..." -ForegroundColor Yellow
        Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/assets/$($existing.id)" `
            -Headers $headers -Method Delete | Out-Null
    }

    Write-Host "Uploading $name ($([math]::Round((Get-Item $asset).Length / 1MB, 1)) MB) ..." -ForegroundColor Cyan
    $uploadUri = "https://uploads.github.com/repos/$Repo/releases/$($release.id)/assets?name=$name"
    $resp = & curl.exe -sS --fail-with-body --retry 3 --retry-delay 5 `
        -X POST `
        -H "Authorization: token $Token" `
        -H "User-Agent: zhenhanvn-publish" `
        -H "Content-Type: application/octet-stream" `
        --data-binary "@$asset" `
        $uploadUri
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Upload failed for $name (curl exit $LASTEXITCODE): $resp"
        exit 1
    }
    $uploaded = $resp | ConvertFrom-Json
    Write-Host "Uploaded: $($uploaded.name) ($([math]::Round($uploaded.size / 1MB, 1)) MB)" -ForegroundColor Green
    Write-Host "  $($uploaded.browser_download_url)" -ForegroundColor Cyan
}

Write-Host ""
Write-Host "Done. Release: $($release.html_url)" -ForegroundColor Green
