#!/bin/sh
# Prepares the state directories and drops root privileges before starting the backup in one of its two modes, or the
# portal:
#   standalone  one source, one repository, one job per type, password file, no API
#   portal      the daemon of the web portal: management API, any number of jobs, stored secrets (default of the compose file)
#   web         the web portal itself
set -eu

DATA="${CONTROL_PATH:-/var/lib/control}"
CACHE="${CONTROL_CACHEDIR:-/var/cache/restic}"
PORTAL_DATA="${PORTAL_PATH:-/var/lib/portal}"

# The directory of a password file that is created on the first start has to be writable by the unprivileged user, a
# bind mount that did not exist is created as root. Only the directory is changed, best effort.
prepare_secret_dir() {
	[ -n "$1" ] || return 0
	dir="$(dirname "$1")"
	[ -d "$dir" ] || return 0
	chown "$PUID:$PGID" "$dir" 2>/dev/null || true
}

# Client commands (status, once, run, ...) are passed straight through.
MODE="${1:-${CONTROL_MODE:-standalone}}"
case "$MODE" in
	standalone | portal | web) ;;
	*) exec control "$@" ;;
esac
[ $# -gt 0 ] && shift

if [ "$MODE" = "web" ]; then
	# The portal keeps its users and sessions (portal.db) and its generated configuration here.
	if [ "$(id -u)" = "0" ]; then
		PUID="${PUID:-1000}"
		PGID="${PGID:-1000}"
		mkdir -p "$PORTAL_DATA"
		chown "$PUID:$PGID" "$PORTAL_DATA"
		chown -R "$PUID:$PGID" "$PORTAL_DATA" 2>/dev/null || true
		prepare_secret_dir "${PORTAL_PASSWORDFILE:-}"
		exec su-exec "$PUID:$PGID" control-portal --path "$PORTAL_DATA" "$@"
	fi
	mkdir -p "$PORTAL_DATA"
	exec control-portal --path "$PORTAL_DATA" "$@"
fi

export CONTROL_MODE="$MODE"

if [ "$(id -u)" = "0" ]; then
	PUID="${PUID:-1000}"
	PGID="${PGID:-1000}"
	mkdir -p "$DATA" "$CACHE"
	# chmod needs CAP_FOWNER once the directory belongs to PUID, which the container drops, so it is best effort.
	chmod 0750 "$DATA" 2>/dev/null || true
	chown "$PUID:$PGID" "$DATA" "$CACHE"
	# Only restores need it. On a read-only root file system without a volume at /restore (the sync example) it cannot be
	# created, which is not an error.
	mkdir -p /restore 2>/dev/null && chown "$PUID:$PGID" /restore 2>/dev/null || true
	prepare_secret_dir "${CONTROL_API_PASSWORDFILE:-}"
	# The tokens and history are owned by the unprivileged user, the healthcheck runs as root.
	chown -R "$PUID:$PGID" "$DATA" 2>/dev/null || true
	export HOME="$DATA"
	exec su-exec "$PUID:$PGID" control daemon --path "$DATA" "$@"
fi

exec control daemon --path "$DATA" "$@"
