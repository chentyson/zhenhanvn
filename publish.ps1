# Publish v2rayN as a single-file executable
# Output: <project root>\publish\zhenhanvn.exe
# Also copies to a local path for direct execution (avoids SynologyDrive temp extraction issue).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File .\publish.ps1
#   powershell -ExecutionPolicy Bypass -File .\publish.ps1 -Run   # also launches the app

param(
    [switch]$Run
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectFile = Join-Path $ProjectRoot "v2rayN\v2rayN\v2rayN.csproj"
$OutputDir   = Join-Path $ProjectRoot "publish"
$LocalRunDir = Join-Path $env:USERPROFILE "zhenhanvn"
$LocalExe    = Join-Path $LocalRunDir "zhenhanvn.exe"

# Stop any running instance
Stop-Process -Name "zhenhanvn" -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1

# Clean previous output
if (Test-Path $OutputDir) {
    Remove-Item $OutputDir -Recurse -Force
}

# Publish as compressed single-file, self-contained
dotnet publish $ProjectFile `
    -c Release `
    -r win-x64 `
    --self-contained true `
    -p:PublishSingleFile=true `
    -p:IncludeNativeLibrariesForSelfExtract=true `
    -p:EnableCompressionInSingleFile=true `
    -o $OutputDir

if ($LASTEXITCODE -ne 0) {
    Write-Error "Publish failed."
    exit 1
}

$exe = Get-Item (Join-Path $OutputDir "zhenhanvn.exe")
Write-Host ""
Write-Host "Published: $($exe.FullName)" -ForegroundColor Green
Write-Host "Size: $([math]::Round($exe.Length / 1MB, 2)) MB"

# Copy to local path for execution (single-file .NET cannot extract temp on SynologyDrive)
if (-not (Test-Path $LocalRunDir)) {
    New-Item -ItemType Directory -Path $LocalRunDir -Force | Out-Null
}
Copy-Item $exe.FullName $LocalExe -Force
Write-Host "Local copy: $LocalExe" -ForegroundColor Cyan

# Optionally launch
if ($Run) {
    Start-Process $LocalExe
    Write-Host "Launched: $LocalExe" -ForegroundColor Green
}
