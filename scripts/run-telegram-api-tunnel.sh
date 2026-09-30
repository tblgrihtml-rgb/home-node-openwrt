#!/bin/sh
# Expose the Mac-only Bot API to the router's loopback address over SSH.
set -eu

if [ "$#" -ne 3 ]; then
	printf '%s\n' 'Usage: run-telegram-api-tunnel.sh SSH_KEY KNOWN_HOSTS ROUTER_SSH_HOST' >&2
	exit 2
fi

exec ssh -N -T -i "$1" \
	-o IdentitiesOnly=yes \
	-o BatchMode=yes \
	-o StrictHostKeyChecking=yes \
	-o UserKnownHostsFile="$2" \
	-o ExitOnForwardFailure=yes \
	-o ServerAliveInterval=20 \
	-o ServerAliveCountMax=3 \
	-R 127.0.0.1:8081:127.0.0.1:8081 "$3"
