#!/bin/sh
set -e

CONNECT="${CONNECT_ADDR:?CONNECT_ADDR is required}"
LISTEN="${LISTEN_ADDR:-0.0.0.0:56000}"

case "$CONNECT" in
  *:*) ;;
  *) echo "CONNECT_ADDR must be in host:port format" >&2; exit 1 ;;
esac

set -- -listen "$LISTEN" -connect "$CONNECT"

if [ "${MODE}" = "tcp" ]; then
    set -- "$@" -mode tcp
fi

if [ "${OBF_MODE}" = "true" ]; then
    OBF="${OBF_KEY:?OBF_KEY is required when OBF_MODE=true}"
    set -- "$@" -obf -obf-key "$OBF"
fi

if [ "${DEBUG}" = "true" ]; then
    set -- "$@" -debug
fi

exec ./server "$@"
