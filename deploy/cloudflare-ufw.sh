#!/usr/bin/env bash
# Restricts the published 80/443 to Cloudflare's edge ranges on a host that
# runs ufw, by way of Docker's DOCKER-USER chain -- ufw's own INPUT rules
# never see a published port (Docker routes it through FORWARD), so a plain
# `ufw allow from` would not close the origin. Rules are written as a managed
# block at the end of /etc/ufw/after.rules and after6.rules, which is where
# they survive `ufw reload` and a reboot. Re-run to refresh the ranges; the
# block is replaced, not appended; `--remove` takes it out again for a node
# that stops using the CDN. deploy/README.md, "Behind Cloudflare".
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
begin='# BEGIN moansubs-cloudflare (managed by cloudflare-ufw.sh, do not edit)'
end='# END moansubs-cloudflare'

if [[ $EUID -ne 0 ]]; then
	echo "cloudflare-ufw.sh: run as root (sudo)" >&2
	exit 1
fi
for f in /etc/ufw/after.rules /etc/ufw/after6.rules; do
	[[ -f $f ]] || { echo "cloudflare-ufw.sh: $f missing -- is ufw installed?" >&2; exit 1; }
done
ufw status | grep -q '^Status: active' || { echo "cloudflare-ufw.sh: ufw is not active; the rules would never load" >&2; exit 1; }

strip_block() {
	local file=$1 tmp
	tmp=$(mktemp)
	awk -v b="$begin" -v e="$end" '$0==b{skip=1} !skip{print} $0==e{skip=0}' "$file" >"$tmp"
	install -m 0640 -o root -g root "$tmp" "$file"
	rm -f "$tmp"
}

if [[ ${1:-} == --remove ]]; then
	strip_block /etc/ufw/after.rules
	strip_block /etc/ufw/after6.rules
	ufw reload >/dev/null
	echo "cloudflare-ufw.sh: managed rules removed; 80/443 are open to everyone again"
	exit 0
fi

# The interface the internet arrives on: the DROP must not touch traffic
# between containers on the compose bridge, or the host's own loopback.
wan=${WAN_IF:-$(ip -4 route show default | awk '{print $5; exit}')}
[[ -n $wan ]] || { echo "cloudflare-ufw.sh: cannot determine the default interface; set WAN_IF" >&2; exit 1; }

IFS=, read -r -a cidrs <<<"$("$here/cloudflare-cidrs.sh")"
v4=() v6=()
for c in "${cidrs[@]}"; do
	if [[ $c == *:* ]]; then v6+=("$c"); else v4+=("$c"); fi
done
[[ ${#v4[@]} -gt 0 && ${#v6[@]} -gt 0 ]] || { echo "cloudflare-ufw.sh: refusing to install an empty allow-list" >&2; exit 1; }

# Match on the port the client asked for (ctorigdstport), not the
# post-DNAT one, so a published "8443:443" is still covered.
block() {
	local -n list=$1
	echo "$begin"
	echo '*filter'
	echo ':DOCKER-USER - [0:0]'
	for c in "${list[@]}"; do
		for p in 80 443; do
			echo "-A DOCKER-USER -i $wan -s $c -p tcp -m conntrack --ctorigdstport $p -j RETURN"
		done
	done
	for p in 80 443; do
		echo "-A DOCKER-USER -i $wan -p tcp -m conntrack --ctorigdstport $p -j DROP"
	done
	echo '-A DOCKER-USER -j RETURN'
	echo 'COMMIT'
	echo "$end"
}

install_block() {
	local file=$1 restore=$2 tmp
	tmp=$(mktemp)
	# A previous managed block is dropped, and the new one goes after the
	# file's own COMMIT.
	awk -v b="$begin" -v e="$end" '$0==b{skip=1} !skip{print} $0==e{skip=0}' "$file" >"$tmp"
	block "$3" >>"$tmp"
	# The block is self-contained (it declares DOCKER-USER), so it can be
	# parsed on its own before it is allowed anywhere near ufw.
	sed -n "/^$begin\$/,/^$end\$/p" "$tmp" | grep -v '^#' | "$restore" --test
	install -m 0640 -o root -g root "$tmp" "$file"
	rm -f "$tmp"
}

install_block /etc/ufw/after.rules iptables-restore v4
install_block /etc/ufw/after6.rules ip6tables-restore v6
ufw reload >/dev/null
echo "cloudflare-ufw.sh: 80/443 on $wan restricted to ${#v4[@]} IPv4 and ${#v6[@]} IPv6 Cloudflare ranges"
