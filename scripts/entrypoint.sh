#!/bin/sh
# Prepares the state directories and drops root privileges before starting the daemon or the portal.
set -eu

DATA="${CONTROL_PATH:-/var/lib/control}"
CACHE="${RESTIC_CACHE_DIR:-/var/cache/restic}"
PORTAL_DATA="${PORTAL_PATH:-/tmp/control-portal}"

# Client commands (status, run, ...) are passed straight through.
MODE="${1:-daemon}"
case "$MODE" in
	daemon | portal) ;;
	*) exec control "$@" ;;
esac
[ $# -gt 0 ] && shift

if [ "$MODE" = "portal" ]; then
	# The portal keeps no state besides its generated configuration file.
	if [ "$(id -u)" = "0" ]; then
		PUID="${PUID:-1000}"
		PGID="${PGID:-1000}"
		mkdir -p "$PORTAL_DATA"
		chown "$PUID:$PGID" "$PORTAL_DATA"
		exec su-exec "$PUID:$PGID" control-portal --path "$PORTAL_DATA" "$@"
	fi
	mkdir -p "$PORTAL_DATA"
	exec control-portal --path "$PORTAL_DATA" "$@"
fi

if [ "$(id -u)" = "0" ]; then
	PUID="${PUID:-1000}"
	PGID="${PGID:-1000}"
	mkdir -p "$DATA" "$CACHE" /restore
	# chmod needs CAP_FOWNER once the directory belongs to PUID, which the container drops, so it is best effort.
	chmod 0750 "$DATA" 2>/dev/null || true
	chown "$PUID:$PGID" "$DATA" "$CACHE" /restore
	# The token and history are owned by the unprivileged user, the healthcheck runs as root.
	chown -R "$PUID:$PGID" "$DATA" 2>/dev/null || true
	export HOME="$DATA"
	exec su-exec "$PUID:$PGID" control daemon --path "$DATA" "$@"
fi

exec control daemon --path "$DATA" "$@"