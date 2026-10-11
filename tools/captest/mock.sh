#!/bin/bash
pkill -f "/workspace/cap/bin/mock"; sleep 1
bash -c "ulimit -n 500000; exec /workspace/cap/bin/mock $* > /workspace/cap/run/mock.log 2>&1" &
sleep 1
