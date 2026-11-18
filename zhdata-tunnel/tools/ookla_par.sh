#!/bin/bash
# Backend upload capacity test: N parallel POSTs from C498 to the real Ookla
# upload endpoint (same shape as the tunnel's backend writes).
# Usage: ookla_par.sh [host:port] [n] [size_bytes]
H="${1:-speedtest.la3eq.fdcservers.net:8080}"
N="${2:-8}"
SZ="${3:-10000000}"
head -c "$SZ" /dev/zero > /tmp/oka_body
t0=$(date +%s.%N)
pids=()
for i in $(seq 1 "$N"); do
  ( curl -s -m 60 -o /dev/null -w "s$i code=%{http_code} up=%{size_upload} speed=%{speed_upload} t=%{time_total}\n" \
      -X POST --data-binary @/tmp/oka_body "http://$H/upload.php?x=$RANDOM" ) &
  pids+=($!)
done
wait "${pids[@]}"
t1=$(date +%s.%N)
echo "elapsed=$(echo "$t1 - $t0" | bc)s  aggregate: $N x $SZ bytes"
