#!/bin/bash
# usage: roll.sh N [GAP] — SIGTERM each supervised Home 1..N in turn (like a
# rolling update), waiting until it serves again plus GAP seconds.
N=${1:-2}; GAP=${2:-20}; R=/workspace/cap/run
for i in $(seq 1 $N); do
  pid=$(for p in $(pgrep -f '^/workspace/cap/bin/home$'); do [ "$(sudo -n readlink /proc/$p/cwd)" = "$R/home$i" ] && echo $p; done)
  [ -z "$pid" ] && { echo "home$i not running"; continue; }
  t0=$(date +%s.%N); echo "[$(date +%T)] SIGTERM home$i ($pid)"; sudo -n kill -TERM $pid
  while sudo -n kill -0 $pid 2>/dev/null; do sleep 0.2; done
  echo "[$(date +%T)] home$i exited after $(echo "$(date +%s.%N) - $t0" | bc | cut -c1-5)s"
  port=$((8317+10*i))
  for t in $(seq 1 120); do (echo > /dev/tcp/127.0.0.1/$port) 2>/dev/null && break; sleep 0.5; done
  echo "[$(date +%T)] home$i serving again"; sleep $GAP
done
