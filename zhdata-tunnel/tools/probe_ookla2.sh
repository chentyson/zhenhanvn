#!/bin/bash
# Probe Ookla upload endpoint the way speedtest-cli does:
#   POST http://host:8080/upload.php?x=<ts>.<bump>  Content-Length: N  body = zeros
H="${1:-la02.speedtest.windstream.net:8080}"
echo "== target $H"
head -c 100000 /dev/zero > /tmp/z100k
head -c 10000000 /dev/zero > /tmp/z10m
for url in "http://$H/upload.php?x=1699999999.0" "http://$H/upload.php" "http://$H/upload.php?size=100000"; do
  echo "== POST $url"
  curl -s -m 15 -o /tmp/resp.txt -w 'code=%{http_code} up=%{size_upload} speed=%{speed_upload} t=%{time_total}\n' \
    -X POST -H 'Content-Type: application/octet-stream' -H 'User-Agent: Mozilla/5.0' --data-binary @/tmp/z10m "$url"
  head -c 120 /tmp/resp.txt; echo
done
