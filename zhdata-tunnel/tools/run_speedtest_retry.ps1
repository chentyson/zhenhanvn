# Run the real speedtest smoke test until the upload phase actually runs.
# Usage: powershell -File tools/run_speedtest_retry.ps1 [socks] [attempts]
param(
  [string]$Socks = "127.0.0.1:49888",
  [int]$Attempts = 4
)
$ErrorActionPreference = "Continue"
$env:NODE_PATH = "C:\Users\tyson\AppData\Local\npm-cache\_npx\e41f203b7505f1fb\node_modules"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  for ($i = 1; $i -le $Attempts; $i++) {
    $out = "st_retry_$i.json"
    Write-Host "=== attempt $i -> $out"
    node tools\speedtest_cdp.js $Socks $out 2>&1 | ForEach-Object { Write-Host $_ }
    if (-not (Test-Path $out)) { continue }
    try { $j = Get-Content $out -Raw | ConvertFrom-Json } catch { continue }
    $upReq = 0
    foreach ($e in $j.events) { if ($e.kind -eq 'req' -and $e.url -match '/upload') { $upReq++ } }
    $leaf = $null
    if ($j.samples.Count -gt 0) { $leaf = $j.samples[-1].leaf }
    $down = $null; $up = $null
    # leaf layout: [0]=share id, [1]=download Mbps, [2]=upload Mbps, [3+]=ping/jitter/loss
    if ($leaf -and $leaf.Count -ge 3) { $down = $leaf[1].v; $up = $leaf[2].v }
    Write-Host "attempt ${i}: uploadReqs=$upReq displayed(down=$down up=$up)"
    $upNum = 0.0
    [void][double]::TryParse([string]$up, [ref]$upNum)
    if ($upReq -ge 1 -and $upNum -gt 2) {
      Write-Host "SUCCESS attempt ${i}: download=$down Mbps upload=$up Mbps (upload requests=$upReq)"
      exit 0
    }
  }
  Write-Host "NO VALID RUN in $Attempts attempts"
  exit 1
} finally { Pop-Location }

