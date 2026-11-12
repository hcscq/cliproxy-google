# CPA Bright Data ISP relay

CPA's own "node secondary IP → Bright Data" data plane. It does **not** use
kiro's `:3129` at runtime. The feature is gated by
`routing.egress.bd.enabled` (**default false**). Applying the DaemonSet does
not enable the flag and does not move any credential.

## What to allowlist (operator step)

Add the **current secondary IPv4** of these two nodes to the Bright Data ISP
zone allowlist (the zone whose password is in Secret `cliproxy-bd-isp`):

- `rainyun-7fm6r3az`
- `rainyun-xnrgjetf`

These are the CPA egress-agent nodes that do not run kiro's 3129. The relay
binds every upstream connection to the node's secondary IPv4; Bright Data
rejects (`407 ip_forbidden`) any source IP that is not allowlisted.

How to read the addresses (they have been rotated and must be confirmed live):

- CPA panel → egress list: the **副** IP on each of those two nodes
- On the node: `ip -4 -o addr show dev ens19 scope global` (the address marked
  `secondary`)

Do **not** allowlist the node primary / control-plane IPs. A November 10
inventory showed `7fm6r3az` with `154.44.10.227`; treat that as historical
only and confirm before submitting.

When you later expand the relay to more CPA nodes, allowlist those nodes'
secondary IPs the same way.

## Other operator steps (not done by this PR)

1. Create Secret `cliproxy-bd-isp` from `../20-secrets.example.yaml` (real
   password, never committed). Mount it on Home and Proxy as
   `CLIPROXY_BD_ISP_PASSWORD` or `CLIPROXY_BD_ISP_PASSWORD_FILE`.
2. Apply the ConfigMap from this directory (see `../47-bd-relay.yaml`).
3. Host firewall: allow **tcp 3142** from `10.42.0.0/16` and `172.16.0.0/16`
   (same sources as 3141).
4. Set `routing.egress.bd.enabled: false` in Home config (already the default).
   Enabling it is a **separate approved change**.
5. Fill the static-IP inventory in the panel (Settings → Bright Data static
   IPs). Adding a row never auto-binds a credential.
6. Only after probes pass, explicitly assign a test credential with
   `POST /proxy/egress/assign` `{egress_id:"bd/<ip>"}`.

Import, auto-balance, and pickEgress never select a BD egress. If a BD
egress is down, the pinned credential is paused in place (`egress_down`);
it is never repinned or sent direct.
