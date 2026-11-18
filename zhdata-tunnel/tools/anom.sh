#!/bin/bash
# Count protocol anomalies since a UTC timestamp. Usage: anom.sh "2026-11-15 12:05:00"
SINCE="${1:-2026-11-15 12:05:00}"
echo "since: $SINCE (UTC)"
for k in "reorder overflow" "reorder park" "backend stalled" "gap truncate" "write err" "unknown handshake" "max clients"; do
  n=$(journalctl -u zhdata --since "$SINCE" --no-pager | grep -c "$k")
  printf '%-20s %s\n' "$k" "$n"
done
echo -n "sessions ended:     "
journalctl -u zhdata --since "$SINCE" --no-pager | grep -c "session ended"
echo -n "sessions established: "
journalctl -u zhdata --since "$SINCE" --no-pager | grep -c "session established"
echo "--- last write err ---"
journalctl -u zhdata --since "$SINCE" --no-pager | grep "write err" | tail -3
