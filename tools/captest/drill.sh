#!/bin/bash
# usage: drill.sh LABEL RPS DUR [AT_SECONDS "action command"]
# Runs step.py, fires the action mid-run, waits for the usage stream to drain,
# then compares request-log rows written with requests the proxies served.
L=$1; RPS=$2; DUR=$3; AT=$4; ACT=$5
Q() { psql -h 127.0.0.1 -p 56432 -U postgres -d home -At -c "$1"; }
c0=$(Q "select count(*) from usage"); m0=$(Q "select coalesce(max(id),0) from usage")
HOMES=3 python3 $(dirname $0)/step.py $RPS $DUR $L > /workspace/cap/run/drill-$L.json 2>&1 &
SP=$!
if [ -n "$ACT" ]; then sleep $AT; echo "[$(date +%T)] action: $ACT"; bash -c "$ACT"; fi
wait $SP
cl=$(Q "select count(*) from usage"); echo "[$(date +%T)] load done; rows so far $((cl-c0)); draining"
for t in $(seq 1 90); do
  b=0
  for p in 8327 8337 8347; do
    v=$(curl -s -m 3 -H 'Authorization: Bearer capmk' http://127.0.0.1:$p/v0/management/infra | jq '(.usage_stream.length // 0) + (.usage_stream.pending // 0)' 2>/dev/null)
    b=$((b + ${v:-0}))
  done
  [ $b -eq 0 ] && break; sleep 2
done
sleep 5
c1=$(Q "select count(*) from usage")
dup=$(Q "select count(*) from (select stream_id from usage where id > $m0 and stream_id <> '' group by stream_id having count(*)>1) d")
jq -c --arg rows $((c1-c0)) --arg dup "$dup" --arg backlog "$b" '{label, codes, sent, rps_achieved, p50_ms, p99_ms, rows: ($rows|tonumber), dup_stream_ids: ($dup|tonumber), final_backlog: ($backlog|tonumber), min_mem_available_mb, pg_peak_conns, aborted}' /workspace/cap/run/drill-$L.json 2>/dev/null || tail -c 600 /workspace/cap/run/drill-$L.json
