#!/bin/bash
# usage: redis.sh up|down — Redis primary 56380, replicas 56381/56382, sentinels 56390-56392 (master name "cpg")
D=/workspace/cap/run/redis
if [ "$1" = up ]; then
  mkdir -p $D
  for p in 56380 56381 56382; do
    extra=""; [ $p != 56380 ] && extra="--replicaof 127.0.0.1 56380"
    redis-server --port $p --bind 127.0.0.1 --save "" --appendonly no --maxmemory 256mb --maxmemory-policy volatile-lru \
      --dir $D --logfile $D/redis-$p.log --daemonize yes --pidfile $D/redis-$p.pid $extra --protected-mode no
  done
  for p in 56390 56391 56392; do
    cat > $D/sentinel-$p.conf <<CONF
port $p
bind 127.0.0.1
daemonize yes
pidfile $D/sentinel-$p.pid
logfile $D/sentinel-$p.log
dir $D
sentinel monitor cpg 127.0.0.1 56380 2
sentinel down-after-milliseconds cpg 3000
sentinel failover-timeout cpg 10000
sentinel parallel-syncs cpg 1
CONF
    redis-sentinel $D/sentinel-$p.conf
  done
  sleep 2; redis-cli -p 56390 sentinel get-master-addr-by-name cpg | paste -sd:
else
  for f in $D/*.pid; do [ -f "$f" ] && kill $(cat $f) 2>/dev/null; done; rm -f $D/*.pid
fi
