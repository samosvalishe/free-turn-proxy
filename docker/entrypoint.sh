#!/bin/sh
set -e

CONNECT="${CONNECT_ADDR:?CONNECT_ADDR is required}"
LISTEN="${LISTEN_ADDR:-0.0.0.0:56000}"

MODE_FLAG=""
if [ "${MODE}" = "tcp" ]; then
    MODE_FLAG="-mode tcp"
fi

OBF_FLAG=""
OBF_KEY_FLAG=""
if [ "${OBF_MODE}" = "true" ]; then
    OBF="${OBF_KEY:?OBF_KEY is required when OBF_MODE=true}"
    OBF_FLAG="-obf"
    OBF_KEY_FLAG="-obf-key $OBF"
fi

DEBUG_FLAG=""
if [ "${DEBUG}" = "true" ]; then
    DEBUG_FLAG="-debug"
fi

exec ./server -listen "$LISTEN" -connect "$CONNECT" $MODE_FLAG $OBF_FLAG $OBF_KEY_FLAG $DEBUG_FLAG
