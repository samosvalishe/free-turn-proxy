#!/bin/sh
set -e

CONNECT="${CONNECT_ADDR:?CONNECT_ADDR is required}"
LISTEN="${LISTEN_ADDR:-0.0.0.0:56000}"

VLESS_FLAG=""
if [ "${VLESS_MODE}" = "true" ]; then
    VLESS_FLAG="-vless"
fi

WRAP_FLAG=""
WRAP_KEY_FLAG=""
if [ "${WRAP_MODE}" = "true" ]; then
    WRAP="${WRAP_KEY:?WRAP_KEY is required when WRAP_MODE=true}"
    WRAP_FLAG="-wrap"
    WRAP_KEY_FLAG="-wrap-key $WRAP"
fi

DEBUG_FLAG=""
if [ "${DEBUG}" = "true" ]; then
    DEBUG_FLAG="-debug"
fi

exec ./server -listen "$LISTEN" -connect "$CONNECT" $VLESS_FLAG $WRAP_FLAG $WRAP_KEY_FLAG $DEBUG_FLAG
