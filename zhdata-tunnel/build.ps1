# build.ps1 - cross-compile zhdata-tunnel for all target platforms
$ErrorActionPreference = "Stop"
$env:CGO_ENABLED = "0"
$targets = @(
    @{GOOS="windows"; GOARCH="amd64"},
    @{GOOS="windows"; GOARCH="arm64"},
    @{GOOS="darwin";  GOARCH="amd64"},
    @{GOOS="darwin";  GOARCH="arm64"},
    @{GOOS="linux";   GOARCH="amd64"},
    @{GOOS="linux";   GOARCH="arm64"}
)
New-Item -ItemType Directory -Force -Path dist | Out-Null
foreach ($t in $targets) {
    $ext = if ($t.GOOS -eq "windows") { ".exe" } else { "" }
    $out = "dist/zhdata-tunnel-$($t.GOOS)-$($t.GOARCH)$ext"
    $env:GOOS = $t.GOOS
    $env:GOARCH = $t.GOARCH
    go build -trimpath -ldflags "-s -w" -o $out .
    Write-Host "built $out"
}
$env:GOOS = ""; $env:GOARCH = ""
