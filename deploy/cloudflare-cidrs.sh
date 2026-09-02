#!/usr/bin/env bash
# Prints Cloudflare's published edge ranges as one comma-joined line, for
# UPSTREAM_PROXY_CIDRS in .env (deploy/README.md, "Behind Cloudflare (or
# another CDN)"). Not tracked as a static list here because Cloudflare's
# ranges change; run this and refresh instead of pasting numbers into a
# tracked file.
set -euo pipefail

fetch() {
	curl -fsSL "$1"
}

is_cidr() {
	# IPv4 or IPv6 CIDR, loosely -- good enough to catch an HTML error page
	# or an empty line without re-implementing address validation.
	[[ "$1" =~ ^[0-9a-fA-F:.]+/[0-9]+$ ]]
}

cidrs=()
for url in https://www.cloudflare.com/ips-v4 https://www.cloudflare.com/ips-v6; do
	# Fetched into a variable first: a curl failure inside a process
	# substitution would be invisible to set -e and print a short list.
	body=$(fetch "$url")
	if [[ -z "$body" ]]; then
		echo "cloudflare-cidrs.sh: empty response from $url" >&2
		exit 1
	fi
	while IFS= read -r line; do
		[[ -z "$line" ]] && continue
		if ! is_cidr "$line"; then
			echo "cloudflare-cidrs.sh: unexpected line from $url: $line" >&2
			exit 1
		fi
		cidrs+=("$line")
	done <<<"$body"
done

IFS=,
echo "${cidrs[*]}"
