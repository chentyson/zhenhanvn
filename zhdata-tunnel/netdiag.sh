#!/usr/bin/env bash
echo "== established to 49888 (server side):"
ss -tn state established '( sport = :49888 )' | tail -n +2 | wc -l
echo "== full ss -i of those:"
ss -tinp '( sport = :49888 )' | grep -vE '^$' | head -30
