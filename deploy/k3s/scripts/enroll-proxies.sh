#!/usr/bin/env bash
# Create one Home client identity per proxy replica and store them in Secret proxy-home-identities.
#
# Each identity = Home enrollment JWT + mTLS client cert/key + Home CA, produced by running
# `CLIProxyAPI -home-enroll` inside the cluster (the JWT targets the Home pod DNS name,
# which only resolves in-cluster). Enrollment secrets are single-use; re-running for an
# existing pod creates a NEW identity (revoke the old one in Home management if needed).
#
# Usage: MANAGEMENT_KEY=... ./enroll-proxies.sh [replicas=3]
# Env:   NAMESPACE (cliproxy), PROXY_IMAGE (ghcr.io/hcscq/cliproxy-google-proxy:latest),
#        HOME_SVC (svc/cliproxy-home), LOCAL_PORT (18327)
set -euo pipefail

REPLICAS="${1:-3}"
NS="${NAMESPACE:-cliproxy}"
IMAGE="${PROXY_IMAGE:-ghcr.io/hcscq/cliproxy-google-proxy:latest}"
HOME_SVC="${HOME_SVC:-svc/cliproxy-home}"
LOCAL_PORT="${LOCAL_PORT:-18327}"
: "${MANAGEMENT_KEY:?set MANAGEMENT_KEY to the Home management key}"

for bin in kubectl curl python3; do command -v "$bin" >/dev/null || { echo "$bin is required" >&2; exit 1; }; done

WORK="$(mktemp -d)"
chmod 700 "$WORK"
cleanup() { [[ -n "${PF_PID:-}" ]] && kill "$PF_PID" 2>/dev/null || true; rm -rf "$WORK"; }
trap cleanup EXIT

kubectl -n "$NS" port-forward "$HOME_SVC" "${LOCAL_PORT}:8327" >/dev/null 2>&1 &
PF_PID=$!
for _ in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:${LOCAL_PORT}/" && break; sleep 1; done

for i in $(seq 0 $((REPLICAS - 1))); do
  pod="cliproxy-${i}"
  echo ">> creating Home client certificate for ${pod}"
  resp="$(curl -fsS -X POST "http://127.0.0.1:${LOCAL_PORT}/v8/management/certificates/clients" \
    -H "Authorization: Bearer ${MANAGEMENT_KEY}" -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${pod}\"}")"
  jwt="$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin)["home_jwt"])')"
  printf '%s' "$jwt" > "$WORK/${pod}.jwt"

  echo ">> enrolling ${pod} in-cluster"
  runpod="enroll-${pod}-$RANDOM"
  kubectl -n "$NS" run "$runpod" --image="$IMAGE" --restart=Never --quiet \
    --env="HOME_JWT=${jwt}" --env="HOME_CERT_DIR=/tmp/c" --env="HOME=/tmp" \
    --command -- sh -ec '/app/CLIProxyAPI -home-enroll >/dev/null; for f in client-crt.pem client-key.pem home-ca-crt.pem; do echo "@@${f}"; base64 -w0 "/tmp/c/${f}"; echo; done'
  kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/${runpod}" --timeout=120s
  kubectl -n "$NS" logs "$runpod" > "$WORK/${pod}.out"
  kubectl -n "$NS" delete pod "$runpod" --wait=false >/dev/null
  python3 - "$WORK" "$pod" <<'PY'
import base64, sys, os
work, pod = sys.argv[1], sys.argv[2]
cur = None
for line in open(os.path.join(work, pod + ".out")):
    line = line.strip()
    if line.startswith("@@"):
        cur = line[2:]
    elif cur and line:
        with open(os.path.join(work, f"{pod}.{cur}"), "wb") as fh:
            fh.write(base64.b64decode(line))
        cur = None
PY
  rm -f "$WORK/${pod}.out"
  for f in client-crt.pem client-key.pem home-ca-crt.pem; do test -s "$WORK/${pod}.${f}" || { echo "enrollment output missing ${f}" >&2; exit 1; }; done
done

echo ">> writing Secret proxy-home-identities"
kubectl -n "$NS" create secret generic proxy-home-identities --from-file="$WORK" --dry-run=client -o yaml | kubectl apply -f -
echo "done. Restart proxies: kubectl -n ${NS} rollout restart statefulset/cliproxy"
