#!/bin/sh
# ensure fsrv_go instances for 8000..8007 (speedtest backend targets)
for p in 8000 8001 8002 8003 8004 8005 8006 8007; do
  if ! pgrep -f "fsrv_go $p" >/dev/null 2>&1; then
    nohup /root/fsrv_go $p >/dev/null 2>&1 &
  fi
done
sleep 1
echo "fsrv_go running: $(pgrep -c fsrv_go)"
