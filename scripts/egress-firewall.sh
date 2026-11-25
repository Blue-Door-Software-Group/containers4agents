#!/usr/bin/env bash
# Installs the deny rules for c4a's egress network (images registered with `network: egress`).
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
    --label c4a.network=egress "$NETWORK" >/dev/null
fi
SUBNET="$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "$NETWORK" | awk '{print $1}')"
if [[ -z "$SUBNET" ]]; then
  echo "could not read the subnet of $NETWORK" >&2
  exit 1
fi

# Drop the rules a previous run installed.
while iptables -S DOCKER-USER | grep -q -- "--comment $MARK"; do
  rule="$(iptables -S DOCKER-USER | grep -- "--comment $MARK" | head -n1 | sed 's/^-A /-D /')"
  eval "iptables $rule"
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

# IPv6: the AWS metadata address, when ip6tables is there.
if command -v ip6tables >/dev/null 2>&1 && ip6tables -S DOCKER-USER >/dev/null 2>&1; then
  while ip6tables -S DOCKER-USER | grep -q -- "--comment $MARK"; do
    rule="$(ip6tables -S DOCKER-USER | grep -- "--comment $MARK" | head -n1 | sed 's/^-A /-D /')"
    eval "ip6tables $rule"
  done
  ip6tables -I DOCKER-USER -d fd00:ec2::254 -m comment --comment "$MARK" -j DROP
fi

echo "c4a egress rules installed for $NETWORK ($SUBNET): ${#deny[@]} denied ranges"
