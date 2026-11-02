# CoreDNS hardening (k3s Addon)

App manifests do **not** own CoreDNS. On this cluster CoreDNS is the k3s Addon from
`/var/lib/rancher/k3s/server/manifests/coredns.yaml` (`objectset.rio.cattle.io/owner-name: coredns`).
`kubectl scale` / one-off patches are overwritten on Addon reconcile / k3s upgrade.

## What we do (preferred over NodeLocal DNSCache)

NodeLocal DNSCache would need a DaemonSet plus kubelet `--cluster-dns` / ConfigMap
plumbing — larger blast radius than the incident warranted. Instead:

1. Set `spec.replicas: 2` on the CoreDNS Deployment **inside** the k3s manifests file
   (topologySpreadConstraints for `kubernetes.io/hostname` with `DoNotSchedule` already
   exist, so the second replica lands on another node).
2. Keep the stock Corefile / Service IP.
3. Re-check after every k3s upgrade that replicas stayed at 2 (Addon may rewrite the file).

## Apply (master node, once)

```bash
# backup first
cp /var/lib/rancher/k3s/server/manifests/coredns.yaml /root/coredns.yaml.bak.$(date +%Y%m%d)
# ensure Deployment.spec.replicas: 2 (insert under spec: if missing)
# k3s Addon manager reapplies within seconds
kubectl -n kube-system get deploy coredns -o wide
kubectl -n kube-system get pods -l k8s-app=kube-dns -o wide
```

Rollback: restore the backed-up manifests file (replicas: omit / 1) and wait for Addon reconcile,
or `kubectl -n kube-system scale deploy/coredns --replicas=1` as a temporary step then fix the file.
