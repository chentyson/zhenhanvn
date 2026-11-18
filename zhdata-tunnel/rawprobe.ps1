$c = New-Object System.Net.Sockets.TcpClient("127.0.0.1", 49888)
$s = $c.GetStream()
$s.WriteTimeout = 5000; $s.ReadTimeout = 15000
$b = New-Object byte[] 4096
# greeting
$s.Write([byte[]](5,1,0), 0, 3)
$n = $s.Read($b, 0, 2)
if ($n -ne 2 -or $b[0] -ne 5) { Write-Host "socks greeting fail"; exit 1 }
# CONNECT to 127.0.0.1:8000 (IPv4)
$req = [byte[]](5,1,0,1, 127,0,0,1, [byte]((8000 -shr 8) -band 255), [byte](8000 -band 255))
$s.Write($req, 0, $req.Length)
$n = $s.Read($b, 0, 10)
if ($n -ne 10 -or $b[1] -ne 0) { Write-Host "socks connect fail rep=$($b[1])"; exit 1 }
Write-Host "socks OK, sending GET"
$get = [System.Text.Encoding]::ASCII.GetBytes("GET /big HTTP/1.0`r`n`r`n")
$s.Write($get, 0, $get.Length)
$total = 0; $first = New-Object byte[] 240; $gotFirst = $false
$sw = [System.Diagnostics.Stopwatch]::StartNew()
while ($sw.Elapsed.TotalSeconds -lt 12) {
    try { $n = $s.Read($b, 0, 4096) } catch { Write-Host "read err/timeout at $total"; break }
    if ($n -le 0) { Write-Host "EOF at total=$total"; break }
    if (-not $gotFirst) { [Array]::Copy($b, $first, [Math]::Min($n,240)); $gotFirst = $true; Write-Host "FIRST CHUNK n=$n"; $hex = ($first[0..([Math]::Min($n,120)-1)] | ForEach-Object { $_.ToString("x2") }) -join ' '; $ascii = -join ($first[0..([Math]::Min($n,120)-1)] | ForEach-Object { if ($_ -ge 32 -and $_ -le 126) { [char]$_ } else { '.' } }); Write-Host "HEX: $hex"; Write-Host "ASC: $ascii" }
    $total += $n
}
Write-Host "TOTAL=$total in $($sw.Elapsed.TotalSeconds)s"
$c.Close()
