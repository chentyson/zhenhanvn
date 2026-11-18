#!/bin/bash
# Probe the Ookla HTTP test protocol on the speedtest backend used by a run.
# Usage: probe_ookla.sh [host:port]
H="${1:-la02.speedtest.windstream.net:8080}"
echo "== target $H"
for u in "download?size=1000" "upload?size=1000" "download?size=1000000"; do
  echo "== GET /$u"
  curl -s -m 6 -o /dev/null -w 'code=%{http_code} bytes=%{size_download} speed=%{speed_download}\n' "http://$H/$u"
done
head -c 100000 /dev/zero > /tmp/z100k
head -c 25000000 /dev/zero > /tmp/z25m
echo "== POST /upload?size=100000"
curl -s -m 10 -o /dev/null -w 'code=%{http_code} up=%{size_upload} speed=%{speed_upload} t=%{time_total}\n' -X POST --data-binary @/tmp/z100k "http://$H/upload?size=100000"
echo "== POST /upload (25MB, no size param)"
curl -s -m 30 -o /dev/null -w 'code=%{http_code} up=%{size_upload} speed=%{speed_upload} t=%{time_total}\n' -X POST --data-binary @/tmp/z25m "http://$H/upload"
