#!/bin/sh

set -eu

DOMAINS_FILE="${1:-/mnt/ssd/homenode/data/vpn-routed-domains}"
VPN_DNS="${VPN_ROUTING_DNS:-1.1.1.1}"
VPN_SOURCE="${VPN_BIND_ADDRESS:?Set VPN_BIND_ADDRESS to the IPv4 address of the outgoing VPN interface}"

[ -r "$DOMAINS_FILE" ] || {
	echo "Domain list not found: $DOMAINS_FILE" >&2
	exit 1
}

apk add dnsmasq-full

uci -q delete dhcp.homenode_services_v4 || true
uci -q delete dhcp.homenode_services_v6 || true
uci set dhcp.homenode_services_v4='ipset'
uci set dhcp.homenode_services_v4.table='homenode_policy'
uci set dhcp.homenode_services_v4.table_family='inet'
uci set dhcp.homenode_services_v4.family='4'
uci add_list dhcp.homenode_services_v4.name='service_v4'
uci set dhcp.homenode_services_v6='ipset'
uci set dhcp.homenode_services_v6.table='homenode_policy'
uci set dhcp.homenode_services_v6.table_family='inet'
uci set dhcp.homenode_services_v6.family='6'
uci add_list dhcp.homenode_services_v6.name='service_v6'

# Remove entries created by an earlier run while preserving the user's normal resolvers.
for server in $(uci -q get dhcp.@dnsmasq[0].server || true); do
	case "$server" in
		/*/"$VPN_DNS"@"$VPN_SOURCE") uci -q del_list dhcp.@dnsmasq[0].server="$server" || true ;;
	esac
done

while IFS= read -r domain; do
	domain="${domain%%#*}"
	domain="$(printf '%s' "$domain" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
	[ -n "$domain" ] || continue
	printf '%s\n' "$domain" | grep -Eq '^([a-zA-Z0-9-]+\.)+[a-zA-Z0-9-]+$' || continue
	uci add_list dhcp.homenode_services_v4.domain="$domain"
	uci add_list dhcp.homenode_services_v6.domain="$domain"
	# Resolve selected services through the VPN as Russian upstream DNS may return NXDOMAIN.
	uci add_list dhcp.@dnsmasq[0].server="/$domain/$VPN_DNS@$VPN_SOURCE"
done < "$DOMAINS_FILE"

uci commit dhcp
/etc/init.d/dnsmasq restart
dnsmasq --test
