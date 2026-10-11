#!/bin/bash
# usage: net.sh up N  -> creates netns cap1..capN with IPs 10.201.0.(10+i) on bridge brcap (host 10.201.0.1)
set -e
if [ "$1" = up ]; then
  ip link show brcap >/dev/null 2>&1 || { ip link add brcap type bridge; ip addr add 10.201.0.1/16 dev brcap; ip link set brcap up; }
  for i in $(seq 1 $2); do
    ns=cap$i; ip netns list | grep -qw $ns && continue
    ip netns add $ns; ip link add vh$i type veth peer name vn$i
    ip link set vh$i master brcap; ip link set vh$i up
    ip link set vn$i netns $ns
    ip netns exec $ns ip addr add 10.201.0.$((10+i))/16 dev vn$i
    ip netns exec $ns ip link set vn$i up; ip netns exec $ns ip link set lo up
    ip netns exec $ns ip route add default via 10.201.0.1
  done
else
  for ns in $(ip netns list | awk '/^cap/{print $1}'); do ip netns del $ns; done
  ip link del brcap 2>/dev/null || true
fi
