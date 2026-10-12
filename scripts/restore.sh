#!/bin/sh
# Compatibility wrapper, use `control restore` directly.
# Usage: docker exec backup /restore.sh [SNAPSHOT] TARGET_DIR
set -eu

case $# in
	1) exec control restore -snapshot latest -target "$1" ;;
	2) exec control restore -snapshot "$1" -target "$2" ;;
	*)
		echo "usage: restore.sh [SNAPSHOT] TARGET_DIR  (TARGET_DIR must be below /restore)" >&2
		exit 2
		;;
esac
