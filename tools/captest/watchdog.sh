#!/bin/bash
# Kills all local capacity-test processes if MemAvailable drops below 2 GiB.
LOG=/workspace/cap/run/watchdog.log
while true; do
  a=$(awk '/MemAvailable/{print int($2/1024)}' /proc/meminfo)
  if [ "$a" -lt 2048 ]; then
    echo "$(date '+%F %T') MemAvailable=${a}MB < 2048MB: aborting test" >> $LOG
    pkill -f /workspace/cap/bin/ ; pkill -f "redis-server.*cap" ; pkill -f "redis-sentinel.*cap"
    sudo -u postgres /usr/lib/postgresql/17/bin/pg_ctl -D /workspace/cap/pg/data stop -m immediate >/dev/null 2>&1
    touch /workspace/cap/run/ABORTED
  fi
  sleep 1
done
