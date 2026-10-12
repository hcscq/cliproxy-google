#!/bin/bash
# usage: start.sh HOMES PROXIES [mock args...]
H=${1:-2}; P=${2:-4}; shift 2
R=/workspace/cap/run; B=/workspace/cap/bin
[ -n "$FRESH_ID" ] && rm -rf $R/proxy*/id   # after a DB rebuild the cluster CA changes
pkill -f "$B/(home|proxy|mock)"; rm -f $R/ABORTED
for t in $(seq 1 30); do pgrep -f "$B/(home|proxy|mock)" >/dev/null || break; sleep 1; done
pkill -9 -f "$B/(home|proxy|mock)"; sleep 1
cd $R
bash -c "ulimit -n 500000; GOMEMLIMIT=200MiB exec $B/mock $* > $R/mock.log 2>&1" &
for i in $(seq 1 $H); do
  d=$R/home$i; mkdir -p $d; sed "s/10.201.0.11/10.201.0.1/; s/port: 8327/port: $((8317+10*i))/" $R/cluster.yaml > $d/cluster.yaml
  # SUPERVISE=1: restart a Home that exits (like the k8s kubelet does)
  LOOP_A=""; LOOP_B=""; [ -n "$SUPERVISE" ] && LOOP_A="while true; do" && LOOP_B="; sleep 2; done"
  bash -c "cd $d; ulimit -n 500000; $LOOP_A GOMAXPROCS=${HOME_PROCS:-2} GOMEMLIMIT=${HOME_MEM:-450MiB} HOME_PPROF_ADDR=127.0.0.1:$((6059+i)) HOME_REDIS_ENABLED=${REDIS:-0} HOME_REDIS_SENTINELS=127.0.0.1:56390,127.0.0.1:56391,127.0.0.1:56392 HOME_REDIS_MASTER=cpg HOME_USAGE_STREAM=${STREAM:-0} HOME_PG_POOLER_ADDR=${POOLER:-} MANAGEMENT_PASSWORD=capmk HOME_BOOTSTRAP_TOKEN=capboot HOME_EPHEMERAL_NODE_TTL=10m HOME_DRAIN_TIMEOUT=${DRAIN:-25s} $([ -n "$SUPERVISE" ] || echo exec) $B/home >> $d/home.log 2>&1 $LOOP_B" &
done
for t in $(seq 1 60); do (echo > /dev/tcp/127.0.0.1/8327) 2>/dev/null && break; sleep 1; done
for j in $(seq 1 $P); do
  n=$j; hi=$(( (j-1) % H + 1 )); d=$R/proxy$j; mkdir -p $d/tmp $d/id
  ip netns exec cap$n bash -c "cd $d; ulimit -n 500000; GOMAXPROCS=${PROXY_PROCS:-2} GOMEMLIMIT=${PROXY_MEM:-300MiB} HOME_ADDRESS=10.201.0.1:$((8317+10*hi)) HOME_BOOTSTRAP_TOKEN=capboot HOME_NODE_NAME=proxy$j HOME_CERT_DIR=$d/id WRITABLE_PATH=$d/tmp HOME=$d exec $B/proxy > $d/proxy.log 2>&1" &
done
for j in $(seq 1 $P); do
  for t in $(seq 1 60); do curl -fs -o /dev/null http://10.201.0.$((10+j)):8317/readyz && break; sleep 1; done
  curl -s -o /dev/null -w "proxy$j readyz=%{http_code}\n" http://10.201.0.$((10+j)):8317/readyz
done
