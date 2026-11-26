#!/usr/bin/env bash
# Installs the deny rules for c4a's egress network (images registered with `network: egress`).
#
# Two chains: DOCKER-USER (traffic the host forwards, i.e. to other hosts) and INPUT (traffic
# addressed to the Docker host itself: its bridge gateway and its own IPs, where c4a, the app,
# databases and model servers listen). From the egress bridge, INPUT accepts only replies.
#
# Docker cannot block destinations by itself, so egress sandboxes would otherwise reach the cloud
# metadata service, private networks and the control planes. This script adds DOCKER-USER rules
# for the c4a-egress bridge subnet: established replies pass; the metadata IPs, RFC1918,
# carrier-grade NAT and link-local ranges, and any hosts in C4A_EGRESS_DENY_HOSTS are dropped;
# everything else (the public internet) is allowed. Each rule carries the comment
# "c4a-egress", which c4a looks for at startup, and re-running replaces them (idempotent).
#
# Run as root on the Docker host, after Docker starts (e.g. a systemd unit ordered after
# docker.service). Usage:
#   C4A_EGRESS_DENY_HOSTS="c4a.internal swarm.internal 10.0.0.5" scripts/egress-firewall.sh
set -euo pipefail

NETWORK="${C4A_EGRESS_NETWORK:-c4a-egress}"
MARK="c4a-egress"

if ! docker network inspect "$NETWORK" >/dev/null 2>&1; then
  docker network create --driver bridge \
    --opt com.docker.network.bridge.enable_icc=false \
    --opt com.docker.network.bridge.enable_ip_masquerade=true \
    --opt com.docker.network.bridge.name=c4a-egress0 \
    --label c4a.network=egress "$NETWORK" >/dev/null
fi
# The bridge interface: the name c4a gives it, or Docker's br-<id> for a network made before.
BRIDGE="$(docker network inspect -f '{{index .Options "com.docker.network.bridge.name"}}' "$NETWORK")"
if [[ -z "$BRIDGE" || "$BRIDGE" == "<no value>" ]]; then
  BRIDGE="br-$(docker network inspect -f '{{.Id}}' "$NETWORK" | cut -c1-12)"
fi
SUBNET="$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "$NETWORK" | awk '{print $1}')"
if [[ -z "$SUBNET" ]]; then
  echo "could not read the subnet of $NETWORK" >&2
  exit 1
fi

# Drop the rules a previous run installed.
for chain in DOCKER-USER INPUT; do
  while iptables -S "$chain" | grep -q -- "--comment $MARK"; do
    rule="$(iptables -S "$chain" | grep -- "--comment $MARK" | head -n1 | sed 's/^-A /-D /')"
    eval "iptables $rule"
  done
done

deny=(169.254.0.0/16 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10)
for host in ${C4A_EGRESS_DENY_HOSTS:-}; do
  if [[ "$host" =~ ^[0-9./]+$ ]]; then
    deny+=("$host")
  else
    while read -r ip; do [[ -n "$ip" ]] && deny+=("$ip"); done < <(getent ahostsv4 "$host" | awk '{print $1}' | sort -u)
  fi
done

# Inserted in reverse so the final order is: replies first, then the drops.
for (( i=${#deny[@]}-1; i>=0; i-- )); do
  iptables -I DOCKER-USER -s "$SUBNET" -d "${deny[$i]}" -m comment --comment "$MARK" -j DROP
done
iptables -I DOCKER-USER -s "$SUBNET" -m conntrack --ctstate ESTABLISHED,RELATED -m comment --comment "$MARK" -j RETURN

# Nothing on the host itself: replies to connections the host opened pass, every new connection
# from the bridge (to c4a, the app, databases, model servers on any host address) is dropped.
# Docker's embedded DNS answers inside each container, so name resolution does not need INPUT.
iptables -I INPUT -i "$BRIDGE" -m comment --comment "$MARK" -j DROP
iptables -I INPUT -i "$BRIDGE" -m conntrack --ctstate ESTABLISHED,RELATED -m comment --comment "$MARK" -j ACCEPT

# IPv6: the AWS metadata address, when ip6tables is there.
if command -v ip6tables >/dev/null 2>&1 && ip6tables -S DOCKER-USER >/dev/null 2>&1; then
  while ip6tables -S DOCKER-USER | grep -q -- "--comment $MARK"; do
    rule="$(ip6tables -S DOCKER-USER | grep -- "--comment $MARK" | head -n1 | sed 's/^-A /-D /')"
    eval "ip6tables $rule"
  done
  ip6tables -I DOCKER-USER -d fd00:ec2::254 -m comment --comment "$MARK" -j DROP
fi

echo "c4a egress rules installed for $NETWORK ($SUBNET on $BRIDGE): ${#deny[@]} denied ranges, host input closed"
