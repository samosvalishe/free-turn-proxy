#!/usr/bin/env bash
# Free Turn Proxy - установщик и контроллер сервера.

set -Eeuo pipefail
umask 077

PROTO_VERSION=3

PREFIX="${FT_PREFIX:-/opt/free-turn-proxy}"
CONF_FILE="$PREFIX/install.conf"
LOCKFILE="$PREFIX/control.lock"
BIN="$PREFIX/server"
VERFILE="$PREFIX/version"
ENVFILE="$PREFIX/run.env"
AUTH_DIR="$PREFIX/auth"
CLIENTSFILE="$AUTH_DIR/clients.json"
# Раздаётся по вебу - метаданные и allowlist держим вне его.
CLIENTS_DIR="$PREFIX/clients"
CLIENTS_META="$PREFIX/clients.list"
OWNER="owner"

# Веб-раздача: QR не влезает в терминал, картинку снимают телефоном. Сервер живёт TTL -
# порт с приватными ключами не должен стоять открытым между показами.
WEB_ROOT="$PREFIX/web"
WEB_TOKEN_FILE="$PREFIX/web.token"
WEB_LOG="$PREFIX/web.log"
WEB_UNIT="freeturn-web"
WEB_PROBE_EXT=".ok"

SERVICE="free-turn-proxy.service"
UNIT_FILE="/etc/systemd/system/$SERVICE"
CONTAINER="free-turn-proxy"
GUM_VERSION="0.17.0"

WG_DIR="${FT_WG_DIR:-/etc/wireguard}"
WG_IFACE="ft-wg0"
WG_CONF="$WG_DIR/$WG_IFACE.conf"
WG_UNIT="wg-quick@$WG_IFACE"
WG_MARKER="# managed-by: free-turn-proxy"
WG_MTU=1280                    # = tunnel.DefaultMTU клиента
CLIENT_LISTEN="127.0.0.1:9000" # = -listen клиента по умолчанию, Endpoint клиентского WG
SYSCTL_FILE="/etc/sysctl.d/99-free-turn-proxy.conf"

REPO="samosvalishe/free-turn-proxy"
IMAGE="ghcr.io/$REPO"
RELEASES_URL="https://github.com/$REPO/releases"

# Material Design 3 + ANSI fallback
MD_PRIMARY="#D0BCFF"
MD_SECONDARY="#CCC2DC"
MD_TERTIARY="#EFB8C8"
MD_SUCCESS="#81C784"
MD_ERROR="#F2B8B5"
BANNER_GRADIENT=("#EADDFF" "#D0BCFF" "#B69DF8" "#9A82DB" "#7F67BE" "#6750A4")
C_RED='\033[0;31m' C_GREEN='\033[0;32m' C_YELLOW='\033[1;33m' C_CYAN='\033[0;36m' C_NC='\033[0m'

# install.conf
INSTALL_METHOD="docker"        # docker | systemd
BACKEND="new"                  # new - свой WG ft-wg0 | external - «мой VPN» по CONNECT
CONNECT=""
PUBLIC_HOST=""                 # адрес VPS в ссылках клиентов
VERSION="latest"
PROXY_MODE="udp"               # udp | tcp (tcp только для external)
LISTEN_PORT="56000"
WG_PORT="51820"
WG_NET="10.13.13.0/24"
OBF_PROFILE="rtpopus3"         # rtpopus3 | rtpopus2 | rtpopus | none
OBF_KEY=""
KCP_ARGS=""                    # argv-токены -kcp-* сервера, только tcp
FT_WEB_PORT="${FT_WEB_PORT:-8080}"
FT_WEB_TTL="${FT_WEB_TTL:-900}"

HAS_GUM=0
NONINTERACTIVE=0
ACTION=""
FORCE_UPDATE=0
LOCAL_BIN=""
PURGE=0
TARGET="all"
TAIL=80
NAME=""
KCP_SET=0
PREV_LISTEN_PORT=""
PREV_SHARE=""
PEER_IP=""
PEER_PUB=""

PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"
export PATH
export COLORFGBG="15;0"

# ─────────────────────────────────────────────────────────────────────────────
# Валидаторы. Всё, что уходит в install.conf (он source-ится), heredoc-конфиги и argv.

valid_port()   { [[ "$1" =~ ^[1-9][0-9]{0,4}$ ]] && [ "$1" -le 65535 ]; }
valid_hex64()  { [[ "$1" =~ ^[0-9a-fA-F]{64}$ ]]; }
valid_host()   { [[ "$1" =~ ^[a-zA-Z0-9.-]{1,253}$ || "$1" =~ ^[0-9a-fA-F:]{2,39}$ ]]; }
valid_name()   { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$ ]]; }
valid_hostport() {
    [[ "$1" =~ ^(\[[0-9a-fA-F:]{2,39}\]|[a-zA-Z0-9.-]{1,253}):([0-9]{1,5})$ ]] && valid_port "${BASH_REMATCH[2]}"
}
valid_kcp()    { [[ "$1" =~ ^((-kcp-(nodelay|interval|resend|nc|sndwnd|rcvwnd|mtu)\ [0-9]{1,6}|-kcp-acknodelay=(true|false))( |$))*$ ]]; }
valid_version() { [[ "$1" =~ ^(latest|local|v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?)$ ]]; }

# Подсеть WG: сетевой адрес, префикс /16../29 (сервер .1, клиенты с .2).
valid_net() {
    [[ "$1" =~ ^((0|[1-9][0-9]{0,2})\.){3}(0|[1-9][0-9]{0,2})/(1[6-9]|2[0-9])$ ]] || return 1
    local ip=${1%/*} o
    for o in ${ip//./ }; do [ "$o" -le 255 ] || return 1; done
    [ $(( $(ip2int "$ip") & ~$(mask_int "${1#*/}") & 0xffffffff )) -eq 0 ]
}

ip2int() { local IFS=.; set -- $1; echo $(( ($1 << 24) + ($2 << 16) + ($3 << 8) + $4 )); }
int2ip() { printf '%d.%d.%d.%d' $(( $1 >> 24 & 255 )) $(( $1 >> 16 & 255 )) $(( $1 >> 8 & 255 )) $(( $1 & 255 )); }
mask_int() { echo $(( (0xffffffff << (32 - $1)) & 0xffffffff )); }

nets_overlap() {
    local b1=${1#*/} b2=${2#*/} m
    m=$(( b1 < b2 ? b1 : b2 ))
    [ $(( $(ip2int "${1%/*}") & $(mask_int "$m") )) -eq $(( $(ip2int "${2%/*}") & $(mask_int "$m") )) ]
}

join_hostport() { case "$1" in *:*) printf '[%s]:%s' "$1" "$2" ;; *) printf '%s:%s' "$1" "$2" ;; esac; }

# ─────────────────────────────────────────────────────────────────────────────
# JSON RPC: буферизация в один объект, trap EXIT ловит неожиданный выход.

_DATA=()
_LOGS=()
_EMITTED=0
_STAGE="init"
_IS_RPC=0

stage() { _STAGE="$1"; }

esc() {
    local s=${1-}
    s=${s//\\/\\\\}
    s=${s//\"/\\\"}
    s=${s//$'\t'/\\t}
    s=${s//$'\r'/\\r}
    s=${s//$'\n'/\\n}
    s=${s//[$'\001'-$'\037'$'\177']/}
    printf '%s' "$s"
}

d_str()  { _DATA+=("\"$1\":\"$(esc "${2-}")\""); }
d_num()  { _DATA+=("\"$1\":${2:-0}"); }
d_bool() { _DATA+=("\"$1\":$2"); }
d_raw()  { _DATA+=("\"$1\":$2"); }

_join() { local IFS=','; printf '%s' "${*-}"; }
_data_json() { if [ "${#_DATA[@]}" -gt 0 ]; then _join "${_DATA[@]}"; fi; }
_logs_json() { if [ "${#_LOGS[@]}" -gt 0 ]; then _join "${_LOGS[@]}"; fi; }

ok() {
    [ "$_EMITTED" -eq 1 ] && return 0
    _EMITTED=1
    trap - EXIT
    [ "$_IS_RPC" = 1 ] || return 0
    printf '{"proto":%d,"result":"ok","data":{%s},"logs":[%s]}\n' \
        "$PROTO_VERSION" "$(_data_json)" "$(_logs_json)"
}

fail() {
    local code=$1 msg=${2:-$1}
    if [ "$_IS_RPC" != 1 ]; then
        log_error "$msg"
        exit 1
    fi
    [ "$_EMITTED" -eq 1 ] && exit 1
    _EMITTED=1
    trap - EXIT
    printf '{"proto":%d,"result":"err","code":"%s","msg":"%s","stage":"%s","logs":[%s]}\n' \
        "$PROTO_VERSION" "$code" "$(esc "$msg")" "$_STAGE" "$(_logs_json)"
    exit 1
}

die() { fail internal "$1"; }

_on_exit() {
    local rc=$?
    [ "$_EMITTED" -eq 1 ] && return
    [ "$_IS_RPC" -ne 1 ] && return
    _EMITTED=1
    printf '{"proto":%d,"result":"err","code":"internal","msg":"unexpected exit %d","stage":"%s","logs":[%s]}\n' \
        "$PROTO_VERSION" "$rc" "$_STAGE" "$(_logs_json)"
}
trap _on_exit EXIT

# ─────────────────────────────────────────────────────────────────────────────
# UI: обёртки над gum с plain-fallback. В RPC сообщения копятся в logs[].

gum() {
    case "${1:-}" in
        style|join|format|log)
            if [ -t 0 ]; then command gum "$@" </dev/null; else command gum "$@"; fi ;;
        *) command gum "$@" ;;
    esac
}

log_info() {
    [ "$_IS_RPC" = 1 ] && { _LOGS+=("\"$(esc "$*")\""); return 0; }
    if [ "$HAS_GUM" = 1 ]; then gum log --level info -- "$1"; else echo -e "${C_CYAN}[*]${C_NC} $1"; fi
}

log_warn() {
    [ "$_IS_RPC" = 1 ] && { _LOGS+=("\"$(esc "WARN: $*")\""); return 0; }
    if [ "$HAS_GUM" = 1 ]; then gum log --level warn -- "$1"; else echo -e "${C_YELLOW}[!]${C_NC} $1" >&2; fi
}

log_error() {
    [ "$_IS_RPC" = 1 ] && { _LOGS+=("\"$(esc "ERROR: $*")\""); return 0; }
    if [ "$HAS_GUM" = 1 ]; then gum log --level error -- "$1"; else echo -e "${C_RED}[x]${C_NC} $1" >&2; fi
}

log_success() {
    [ "$_IS_RPC" = 1 ] && { _LOGS+=("\"$(esc "$*")\""); return 0; }
    if [ "$HAS_GUM" = 1 ]; then gum style --foreground "$MD_SUCCESS" "✔ $1"; else echo -e "${C_GREEN}[+]${C_NC} $1"; fi
}

log() { log_info "$*"; }

ui_drain_input() {
    [ -t 0 ] || [ -r /dev/tty ] || return 0
    stty -echo </dev/tty 2>/dev/null || true
    local discard
    while read -r -t 0.05 -n 1000 discard </dev/tty 2>/dev/null; do :; done
    stty echo </dev/tty 2>/dev/null || true
}

ui_abort() { ui_drain_input; log_info "Отменено."; exit 0; }

ui_banner() {
    local art=(
'███████╗██████╗ ███████╗███████╗████████╗██╗   ██╗██████╗ ███╗   ██╗'
'██╔════╝██╔══██╗██╔════╝██╔════╝╚══██╔══╝██║   ██║██╔══██╗████╗  ██║'
'█████╗  ██████╔╝█████╗  █████╗     ██║   ██║   ██║██████╔╝██╔██╗ ██║'
'██╔══╝  ██╔══██╗██╔══╝  ██╔══╝     ██║   ██║   ██║██╔══██╗██║╚██╗██║'
'██║     ██║  ██║███████╗███████╗   ██║   ╚██████╔╝██║  ██║██║ ╚████║'
'╚═╝     ╚═╝  ╚═╝╚══════╝╚══════╝   ╚═╝    ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝')
    printf '\n'
    if [ "$HAS_GUM" = 1 ]; then
        local i lines=()
        for i in "${!art[@]}"; do
            lines+=("$(gum style --foreground "${BANNER_GRADIENT[$i]}" "${art[$i]}")")
        done
        gum join --vertical "${lines[@]}"
        gum style --foreground "$MD_SECONDARY" --italic --margin "0 0 1 1" "FreeTurn  ·  установщик сервера"
    else
        echo -e "${C_CYAN}"; printf '%s\n' "${art[@]}"; echo -e "${C_NC}"
        echo "  FreeTurn · установщик сервера"; echo
    fi
}

ui_note() {
    if [ "$HAS_GUM" = 1 ]; then
        gum style --border rounded --border-foreground "$MD_PRIMARY" --padding "0 1" --margin "1 0" \
            "$(gum style --foreground "$MD_PRIMARY" --bold "$1")" "$2"
    else
        echo; log_warn "$1: $2"
    fi
}

ui_input() {
    local __var="$1" __prompt="$2" __def="${3:-}" __ans
    if [ "$HAS_GUM" = 1 ]; then
        __ans=$(gum input --prompt "$__prompt: " --prompt.foreground "$MD_PRIMARY" \
            --cursor.foreground "$MD_TERTIARY" --value "$__def" </dev/tty) || ui_abort
    elif [ -n "$__def" ]; then
        read -r -p "$__prompt [$__def]: " __ans </dev/tty
    else
        read -r -p "$__prompt: " __ans </dev/tty
    fi
    printf -v "$__var" '%s' "${__ans:-$__def}"
}

ui_yesno() {
    local __prompt="$1" __def="${2:-Y}" __ans
    if [ "$HAS_GUM" = 1 ]; then
        local flags=(--selected.background "$MD_PRIMARY" --selected.foreground "#1C1B1F")
        [ "$__def" = "N" ] && flags+=(--default=false)
        gum confirm "${flags[@]}" "$__prompt" </dev/tty
        return $?
    fi
    local hint="y/N"
    [ "$__def" = "Y" ] && hint="Y/n"
    read -r -p "$__prompt [$hint]: " __ans </dev/tty
    [[ "${__ans:-$__def}" =~ ^[Yy]$ ]]
}

ui_menu() {
    local __var="$1" __prompt="$2" __def_tag="$3"; shift 3
    local tags=() labels=() i sel
    while [ $# -gt 0 ]; do tags+=("$1"); labels+=("$2"); shift 2; done

    if [ "$HAS_GUM" = 1 ]; then
        local def_label=""
        for i in "${!tags[@]}"; do [ "${tags[$i]}" = "$__def_tag" ] && def_label="${labels[$i]}"; done
        sel=$(gum choose --header "$__prompt" --header.foreground "$MD_PRIMARY" \
            --cursor "❯ " --cursor.foreground "$MD_TERTIARY" \
            --selected.foreground "$MD_PRIMARY" --selected "$def_label" \
            "${labels[@]}" </dev/tty) || ui_abort
        for i in "${!labels[@]}"; do
            [ "${labels[$i]}" = "$sel" ] && { printf -v "$__var" '%s' "${tags[$i]}"; return 0; }
        done
        ui_abort
    fi
    echo; log_info "$__prompt"
    for i in "${!tags[@]}"; do
        if [ "${tags[$i]}" = "$__def_tag" ]; then echo -e "  ${C_GREEN}${tags[$i]}${C_NC}) ${labels[$i]}"
        else echo "  ${tags[$i]}) ${labels[$i]}"; fi
    done
    while :; do
        read -r -p "Выбор [${__def_tag}]: " sel </dev/tty
        sel="${sel:-$__def_tag}"
        for i in "${!tags[@]}"; do
            [ "$sel" = "${tags[$i]}" ] && { printf -v "$__var" '%s' "$sel"; return 0; }
        done
        log_warn "Неверный выбор: $sel"
    done
}

# ask VAR "Вопрос" default validator
ask() {
    local __var=$1 __prompt=$2 __def=$3 __check=$4
    while :; do
        ui_input "$__var" "$__prompt" "$__def"
        "$__check" "${!__var}" && return 0
        ui_note "Ошибка" "Недопустимое значение: '${!__var}'"
    done
}

ui_spin() {
    local title="$1"; shift
    local rc=0 out; out="$(mktemp)"
    if [ "$HAS_GUM" = 1 ]; then
        ( "$@" >"$out" 2>&1 ) &
        local pid=$!
        gum spin --spinner dot --spinner.foreground "$MD_PRIMARY" --title "$title" \
            -- bash -c 'while kill -0 "$1" 2>/dev/null; do sleep 0.1; done' _ "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || rc=$?
        if [ "$rc" -eq 0 ]; then
            log_success "$title"
        else
            log_error "$title - ошибка (код $rc)"
            tail -n 40 "$out" | gum style --border rounded --border-foreground "$MD_ERROR" --padding "0 1"
        fi
    else
        log_info "$title..."
        "$@" >"$out" 2>&1 || rc=$?
        [ "$rc" -eq 0 ] || tail -n 40 "$out" >&2
    fi
    rm -f "$out"
    return "$rc"
}

# step: долгий шаг; в RPC молча, хвост вывода - в logs[] при ошибке. Внутри не звать fail.
step() {
    local title=$1; shift
    if [ "$_IS_RPC" = 1 ]; then
        local out rc=0
        out=$("$@" 2>&1) || rc=$?
        [ "$rc" -eq 0 ] || log_error "$title: $(printf '%s' "$out" | tail -n 5)"
        return "$rc"
    fi
    ui_spin "$title" "$@"
}

# ─────────────────────────────────────────────────────────────────────────────
# Система: блокировка, пакеты, окружение.

with_lock() {
    command -v flock >/dev/null 2>&1 || return 0
    mkdir -p "$PREFIX" 2>/dev/null || return 0
    exec 8>"$LOCKFILE" 2>/dev/null || return 0
    flock -w 300 8 2>/dev/null || true
}

require_root() { [ "$(id -u 2>/dev/null || echo -1)" -eq 0 ] || fail needs_root "нужен root"; }
has_systemd()  { command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; }

pkg_mgr() {
    local m
    for m in apt-get dnf yum apk pacman zypper; do
        command -v "$m" >/dev/null 2>&1 && { echo "$m"; return 0; }
    done
    return 1
}

pkg_install() {
    local mgr; mgr=$(pkg_mgr) || return 1
    case "$mgr" in
        apt-get)
            DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 \
                apt-get -o DPkg::Lock::Timeout=300 update -qq >/dev/null 2>&1 || true
            DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 \
                apt-get -o DPkg::Lock::Timeout=300 install -y -qq "$@" >/dev/null 2>&1 ;;
        dnf)    dnf install -y -q "$@" >/dev/null 2>&1 ;;
        yum)    yum install -y -q "$@" >/dev/null 2>&1 ;;
        apk)    apk add --no-cache "$@" >/dev/null 2>&1 ;;
        pacman) pacman -Sy --noconfirm "$@" >/dev/null 2>&1 ;;
        zypper) zypper --non-interactive install "$@" >/dev/null 2>&1 ;;
    esac
}

ensure_base_deps() {
    local missing=() b
    for b in curl jq openssl; do command -v "$b" >/dev/null 2>&1 || missing+=("$b"); done
    [ "${#missing[@]}" -eq 0 ] && return 0
    step "Установка зависимостей: ${missing[*]}" pkg_install "${missing[@]}" || true
    for b in "${missing[@]}"; do
        command -v "$b" >/dev/null 2>&1 || fail internal "не удалось установить $b - поставьте вручную"
    done
}

gum_download() {
    local ver="$1" arch tmp bin
    case "$(uname -m 2>/dev/null || true)" in
        x86_64|amd64)  arch="x86_64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) return 1 ;;
    esac
    tmp="$(mktemp -d)"
    if curl -fsSL --connect-timeout 10 --max-time 30 \
        "https://github.com/charmbracelet/gum/releases/download/v${ver}/gum_${ver}_Linux_${arch}.tar.gz" \
        | tar -xz -C "$tmp" 2>/dev/null; then
        bin="$(find "$tmp" -name gum -type f 2>/dev/null | head -n1 || true)"
        [ -n "$bin" ] && install -m 0755 "$bin" /usr/local/bin/gum 2>/dev/null
    fi
    rm -rf "$tmp"
    command -v gum >/dev/null 2>&1
}

ensure_gum() {
    command -v gum >/dev/null 2>&1 && { HAS_GUM=1; return 0; }
    log_info "Установка gum ${GUM_VERSION}..."
    gum_download "$GUM_VERSION" && { HAS_GUM=1; return 0; }
    log_warn "gum недоступен - классический текстовый режим."
}

_mips_is_le() { [ "$(printf '\1\0' | od -An -tx2 -N2 2>/dev/null | tr -d ' \n')" = "0001" ]; }

# Имя ассета релиза (.goreleaser.yaml, archives.raw).
server_asset() {
    case "$(uname -m 2>/dev/null || true)" in
        x86_64|amd64)        echo server-linux-amd64 ;;
        aarch64|arm64)       echo server-linux-arm64 ;;
        armv7*)              echo server-linux-armv7 ;;
        i386|i486|i586|i686) echo server-linux-386 ;;
        riscv64)             echo server-linux-riscv64 ;;
        mips64*)             _mips_is_le && echo server-linux-mips64le-softfloat ;;
        mips*)               if _mips_is_le; then echo server-linux-mipsle-softfloat; else echo server-linux-mips-softfloat; fi ;;
        *)                   return 1 ;;
    esac
}

get_public_ip() {
    local ip url
    for url in "https://api.ipify.org" "https://icanhazip.com" "https://ifconfig.me/ip" "https://ident.me"; do
        ip="$(curl -4 -fsSL --connect-timeout 3 --max-time 5 "$url" 2>/dev/null | tr -d ' \r\n' || true)"
        [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] && { echo "$ip"; return 0; }
    done
    return 1
}

# Занят ли UDP-порт. Без ss проверить нечем - считаем свободным.
port_busy_udp() {
    command -v ss >/dev/null 2>&1 || return 1
    [ -n "$(ss -Hlun "sport = :$1" 2>/dev/null || true)" ]
}

firewall_open_port() {
    local port=$1 proto=${2:-udp}
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "${port}/${proto}" >/dev/null 2>&1 || true
    elif command -v iptables >/dev/null 2>&1; then
        if ! iptables -C INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null; then
            iptables -I INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null || true
            command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1 || true
        fi
    fi
    return 0
}

firewall_close_port() {
    local port=$1 proto=${2:-udp}
    [ -n "$port" ] || return 0
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw delete allow "${port}/${proto}" >/dev/null 2>&1 || true
    elif command -v iptables >/dev/null 2>&1; then
        iptables -D INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null || true
        command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1 || true
    fi
    return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# install.conf

share_fingerprint() { printf '%s|' "$BACKEND" "$PUBLIC_HOST" "$LISTEN_PORT" "$PROXY_MODE" "$OBF_PROFILE" "$OBF_KEY"; }

# shellcheck disable=SC1090
load_config() {
    [ -f "$CONF_FILE" ] || return 0
    . "$CONF_FILE"
    PREV_LISTEN_PORT="$LISTEN_PORT"
    PREV_SHARE="$(share_fingerprint)"
}

save_config() {
    mkdir -p "$PREFIX"
    cat > "$CONF_FILE.tmp" <<EOF
INSTALL_METHOD="$INSTALL_METHOD"
BACKEND="$BACKEND"
CONNECT="$CONNECT"
PUBLIC_HOST="$PUBLIC_HOST"
VERSION="$VERSION"
PROXY_MODE="$PROXY_MODE"
LISTEN_PORT="$LISTEN_PORT"
WG_PORT="$WG_PORT"
WG_NET="$WG_NET"
OBF_PROFILE="$OBF_PROFILE"
OBF_KEY="$OBF_KEY"
KCP_ARGS="$KCP_ARGS"
FT_WEB_PORT="$FT_WEB_PORT"
FT_WEB_TTL="$FT_WEB_TTL"
EOF
    chmod 0600 "$CONF_FILE.tmp"
    mv -f "$CONF_FILE.tmp" "$CONF_FILE"
}

# Дозаполняет производные значения и проверяет согласованность; ошибка - bad_arg.
validate_config() {
    [[ "$INSTALL_METHOD" =~ ^(docker|systemd)$ ]] || fail bad_arg "method: docker | systemd"
    [[ "$BACKEND" =~ ^(new|external)$ ]]          || fail bad_arg "backend: new | external"
    [[ "$PROXY_MODE" =~ ^(udp|tcp)$ ]]            || fail bad_arg "mode: udp | tcp"
    valid_port "$LISTEN_PORT"                     || fail bad_arg "listen-port: 1-65535"
    valid_version "$VERSION"                      || fail bad_arg "version: latest | local | vX.Y.Z"
    # local держится в install.conf: обычный apply оставляет залитый бинарь, --update без --bin
    # возвращает на релиз.
    if [ "$VERSION" = local ] && [ -z "$LOCAL_BIN" ]; then
        if [ "$FORCE_UPDATE" = 1 ]; then VERSION=latest
        else [ -x "$BIN" ] || fail bad_arg "version=local: нет $BIN - залейте его через --bin"; fi
    fi
    [ -z "$LOCAL_BIN" ] || [ -f "$LOCAL_BIN" ] || fail bad_arg "bin: нет файла $LOCAL_BIN"
    valid_kcp "$KCP_ARGS"                         || fail bad_arg "kcp: неверные параметры"
    valid_port "$FT_WEB_PORT"                     || fail bad_arg "web-port: 1-65535"
    [[ "$FT_WEB_TTL" =~ ^[0-9]{2,6}$ ]] && [ "$FT_WEB_TTL" -ge 60 ] || fail bad_arg "web-ttl: от 60 секунд"

    if [ "$BACKEND" = new ]; then
        [ "$PROXY_MODE" = udp ] || fail bad_arg "свой WireGuard работает только в режиме udp"
        valid_port "$WG_PORT" || fail bad_arg "wg-port: 1-65535"
        valid_net "$WG_NET"   || fail bad_arg "wg-net: сетевой адрес a.b.c.d/16..29"
        [ "$WG_PORT" != "$LISTEN_PORT" ] || fail bad_arg "wg-port совпадает с listen-port"
        CONNECT=""
    else
        valid_hostport "$CONNECT" || fail bad_arg "connect: host:port | [ipv6]:port"
    fi

    case "$OBF_PROFILE" in
        rtpopus3|rtpopus2|rtpopus)
            [ -n "$OBF_KEY" ] || OBF_KEY="$(openssl rand -hex 32)"
            valid_hex64 "$OBF_KEY" || fail bad_arg "obf-key: 64 hex-символа" ;;
        none) OBF_KEY="" ;;
        *) fail bad_arg "obf-profile: rtpopus3 | rtpopus2 | rtpopus | none" ;;
    esac

    if [ -z "$PUBLIC_HOST" ]; then
        PUBLIC_HOST="$(get_public_ip || true)"
        [ -n "$PUBLIC_HOST" ] || fail host_unknown "не удалось определить внешний IP - задайте host"
    fi
    valid_host "$PUBLIC_HOST" || fail bad_arg "host: имя или IP"
}

# ─────────────────────────────────────────────────────────────────────────────
# FreeTurn: один argv на оба метода запуска.

backend_addr() { if [ "$BACKEND" = new ]; then echo "127.0.0.1:$WG_PORT"; else echo "$CONNECT"; fi; }

server_argv() {
    printf '%s\n' -listen "0.0.0.0:$LISTEN_PORT" -connect "$(backend_addr)" -mode "$PROXY_MODE" \
        -clients-file "$CLIENTSFILE"
    if [ "$OBF_PROFILE" != none ]; then printf '%s\n' -obf-profile "$OBF_PROFILE" -obf-key "$OBF_KEY"; fi
    # shellcheck disable=SC2086
    if [ "$PROXY_MODE" = tcp ] && [ -n "$KCP_ARGS" ]; then printf '%s\n' $KCP_ARGS; fi
}

# local - свой бинарь поверх образа latest (рантайм из образа, /app/server подменён).
image_tag() { case "$VERSION" in latest|local) echo latest ;; *) echo "${VERSION#v}" ;; esac; }

ft_installed() {
    [ -f "$UNIT_FILE" ] && return 0
    command -v docker >/dev/null 2>&1 && docker container inspect "$CONTAINER" >/dev/null 2>&1
}

ft_running() {
    if [ "$INSTALL_METHOD" = systemd ]; then
        systemctl is-active --quiet "$SERVICE" 2>/dev/null
    else
        [ "$(docker inspect -f '{{.State.Status}}' "$CONTAINER" 2>/dev/null || true)" = running ]
    fi
}

ft_version() {
    if [ "$INSTALL_METHOD" = systemd ] || [ "$VERSION" = local ]; then cat "$VERFILE" 2>/dev/null || true
    else image_tag; fi
}

ft_logs() {
    if [ "$INSTALL_METHOD" = systemd ]; then
        journalctl -u "$SERVICE" -n "$1" --no-pager -o short-iso 2>/dev/null || true
    else
        docker logs --tail "$1" "$CONTAINER" 2>&1 || true
    fi
}

# Рестарт-петля выглядит как running первые секунды - проверяем после паузы.
ft_check_started() {
    sleep 2
    ft_running && return 0
    local l
    while IFS= read -r l; do log_error "$l"; done < <(ft_logs 20)
    fail start_failed "FreeTurn не запустился"
}

ft_stop() {
    if [ "$INSTALL_METHOD" = systemd ]; then
        systemctl stop "$SERVICE" 2>/dev/null || true
    else
        docker stop "$CONTAINER" >/dev/null 2>&1 || true
    fi
}

ft_restart() {
    if [ "$INSTALL_METHOD" = systemd ]; then
        systemctl restart "$SERVICE" 2>/dev/null || fail start_failed "systemctl restart $SERVICE"
    else
        docker restart "$CONTAINER" >/dev/null 2>&1 || fail start_failed "docker restart $CONTAINER"
    fi
    ft_check_started
}

# Снимает оба метода: смена docker <-> systemd не должна оставлять второй экземпляр на порту.
ft_remove() {
    if command -v docker >/dev/null 2>&1; then docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; fi
    if [ -f "$UNIT_FILE" ]; then
        systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
        rm -f "$UNIT_FILE"
        systemctl daemon-reload 2>/dev/null || true
    fi
    rm -f "$ENVFILE"
}

ensure_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        step "Установка Docker" sh -c 'curl -fsSL https://get.docker.com | sh' \
            || fail docker_failed "установка Docker не удалась"
    fi
    docker info >/dev/null 2>&1 || systemctl start docker >/dev/null 2>&1 || service docker start >/dev/null 2>&1 || true
    docker info >/dev/null 2>&1 || fail docker_failed "Docker не запущен"
}

docker_apply() {
    ensure_docker
    local ref="$IMAGE:$(image_tag)" argv mount=()
    step "Загрузка образа $ref" docker pull "$ref" \
        || docker image inspect "$ref" >/dev/null 2>&1 || fail docker_failed "docker pull $ref"
    if [ "$VERSION" = local ]; then
        binary_ensure
        mount=(-v "$BIN:/app/server:ro")
    fi
    mapfile -t argv < <(server_argv)
    docker run -d --name "$CONTAINER" --network host --restart unless-stopped \
        -v "$AUTH_DIR:$AUTH_DIR:ro" ${mount[@]+"${mount[@]}"} --entrypoint /app/server "$ref" "${argv[@]}" >/dev/null \
        || fail docker_failed "docker run $ref"
}

_dl() { curl -fsSL --connect-timeout 15 --max-time 300 -o "$2" "$1"; }

# Свой бинарь (отладка ядра из приложения): без сети и checksums, ставится как есть.
binary_local() {
    [ "$(head -c4 "$LOCAL_BIN" | od -An -c | tr -d ' \n')" = '177ELF' ] || fail bad_arg "bin: не ELF"
    mkdir -p "$PREFIX"
    { install -m 0755 "$LOCAL_BIN" "$BIN.tmp" && mv -f "$BIN.tmp" "$BIN"; } \
        || fail internal "не удалось поставить $LOCAL_BIN"
    rm -f "$LOCAL_BIN"
    echo local > "$VERFILE"
}

binary_ensure() {
    if [ -n "$LOCAL_BIN" ]; then binary_local; return 0; fi
    if [ -x "$BIN" ] && [ "$FORCE_UPDATE" != 1 ] && [ "$(cat "$VERFILE" 2>/dev/null || true)" = "$VERSION" ]; then
        return 0
    fi
    local asset base tmp want got
    asset=$(server_asset) || fail unsupported_arch "архитектура $(uname -m) не поддерживается"
    base="$RELEASES_URL/download/$VERSION"
    [ "$VERSION" = latest ] && base="$RELEASES_URL/latest/download"
    tmp=$(mktemp -d)
    if ! step "Скачивание $asset ($VERSION)" _dl "$base/$asset" "$tmp/server" \
        || ! _dl "$base/checksums.txt" "$tmp/sums" 2>/dev/null; then
        rm -rf "$tmp"
        fail download_failed "не удалось скачать $base/$asset"
    fi
    want=$(awk -v n="$asset" '$2 == n { print $1 }' "$tmp/sums")
    got=$(sha256sum "$tmp/server" | awk '{ print $1 }')
    if [ -z "$want" ] || [ "$want" != "$got" ]; then
        rm -rf "$tmp"
        fail download_failed "sha256 $asset не совпал с checksums.txt"
    fi
    chmod 0755 "$tmp/server"
    mv -f "$tmp/server" "$BIN"
    rm -rf "$tmp"
    echo "$VERSION" > "$VERFILE"
}

systemd_apply() {
    has_systemd || fail no_systemd "systemd не найден - выберите метод docker"
    binary_ensure
    # Токены argv без пробелов (валидаторы), systemd режет $FT_ARGS по словам.
    printf 'FT_ARGS=%s\n' "$(server_argv | tr '\n' ' ')" > "$ENVFILE"
    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Free Turn Proxy server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENVFILE
ExecStart=$BIN \$FT_ARGS
Restart=on-failure
RestartSec=2
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$UNIT_FILE"
    systemctl daemon-reload
    systemctl enable "$SERVICE" >/dev/null 2>&1 || true
    systemctl restart "$SERVICE" || fail start_failed "systemctl restart $SERVICE"
}

ft_apply() {
    ft_remove
    port_busy_udp "$LISTEN_PORT" && fail port_busy "порт $LISTEN_PORT/udp занят"
    if [ "$INSTALL_METHOD" = docker ]; then docker_apply; else systemd_apply; fi
    ft_check_started
    if [ -n "$PREV_LISTEN_PORT" ] && [ "$PREV_LISTEN_PORT" != "$LISTEN_PORT" ]; then
        firewall_close_port "$PREV_LISTEN_PORT" udp
    fi
    firewall_open_port "$LISTEN_PORT" udp
}

# ─────────────────────────────────────────────────────────────────────────────
# WireGuard ft-wg0: ядерный, wg-quick@ft-wg0. Чужой WG не трогаем.

wg_ours()     { [ -f "$WG_CONF" ] && grep -qxF "$WG_MARKER" "$WG_CONF"; }
wg_up()       { ip link show "$WG_IFACE" >/dev/null 2>&1; }
wg_conf_get() { sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$WG_CONF" 2>/dev/null | head -n1 | tr -d ' \r'; }
wg_server_pub() { wg pubkey <<< "$(wg_conf_get PrivateKey)"; }

wg_conf_net() {
    local addr; addr=$(wg_conf_get Address)
    [ -n "$addr" ] || return 0
    printf '%s/%s' "$(int2ip $(( $(ip2int "${addr%/*}") & $(mask_int "${addr#*/}") )))" "${addr#*/}"
}

wg_tools_ensure() {
    command -v wg >/dev/null 2>&1 && command -v wg-quick >/dev/null 2>&1 && command -v iptables >/dev/null 2>&1 \
        && return 0
    step "Установка wireguard-tools" pkg_install wireguard-tools iptables || true
    command -v wg >/dev/null 2>&1 && command -v wg-quick >/dev/null 2>&1 && command -v iptables >/dev/null 2>&1 \
        || fail wg_install_failed "не удалось установить wireguard-tools и iptables"
}

# Прямая проверка: ядро создаёт интерфейс wireguard (модуль подгрузится сам).
wg_kernel_check() {
    wg_up && return 0
    if ip link add dev ftwgprobe type wireguard 2>/dev/null; then
        ip link del dev ftwgprobe 2>/dev/null || true
        return 0
    fi
    fail wg_unsupported "ядро без WireGuard (OpenVZ/LXC?) - выберите «мой VPN»"
}

wg_subnet_free() {
    local cidr
    while read -r cidr; do
        [ -n "$cidr" ] && nets_overlap "$1" "$cidr" && return 1
    done < <(ip -o -4 addr show 2>/dev/null | awk '{ print $4 }'
             ip -4 route show 2>/dev/null | awk '$1 ~ /^[0-9.]+(\/[0-9]+)?$/ { print ($1 ~ /\//) ? $1 : $1 "/32" }')
    return 0
}

wg_write_conf() {
    local srv; srv=$(int2ip $(( $(ip2int "${WG_NET%/*}") + 1 )))
    mkdir -p "$WG_DIR"
    chmod 0700 "$WG_DIR"
    cat > "$WG_CONF" <<EOF
[Interface]
$WG_MARKER
Address = $srv/${WG_NET#*/}
ListenPort = $WG_PORT
MTU = $WG_MTU
PrivateKey = $(wg genkey)
PostUp = iptables -t nat -A POSTROUTING -s $WG_NET ! -d $WG_NET -j MASQUERADE
PostUp = iptables -I FORWARD -i %i -j ACCEPT
PostUp = iptables -I FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
PostUp = iptables -t mangle -A FORWARD -o %i -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
PostDown = iptables -t nat -D POSTROUTING -s $WG_NET ! -d $WG_NET -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT
PostDown = iptables -D FORWARD -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
PostDown = iptables -t mangle -D FORWARD -o %i -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
EOF
    chmod 0600 "$WG_CONF"
}

wg_apply() {
    has_systemd || fail no_systemd "свой WireGuard требует systemd (wg-quick@$WG_IFACE)"
    wg_tools_ensure
    local restart=0
    if [ -f "$WG_CONF" ]; then
        wg_ours || fail wg_conflict "$WG_CONF создан не установщиком"
        [ "$(wg_conf_net)" = "$WG_NET" ] \
            || fail backend_locked "подсеть $(wg_conf_net) не меняется - удалите WG: uninstall --target=wg"
        if [ "$(wg_conf_get ListenPort)" != "$WG_PORT" ]; then
            port_busy_udp "$WG_PORT" && fail port_busy "порт $WG_PORT/udp занят"
            sed -i "s/^ListenPort = .*/ListenPort = $WG_PORT/" "$WG_CONF"
            restart=1
        fi
    else
        wg_kernel_check
        port_busy_udp "$WG_PORT" && fail port_busy "порт $WG_PORT/udp занят"
        wg_subnet_free "$WG_NET" || fail subnet_conflict "подсеть $WG_NET пересекается с адресами хоста"
        wg_write_conf
        restart=1
    fi
    echo 'net.ipv4.ip_forward = 1' > "$SYSCTL_FILE"
    sysctl -q -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
    systemctl enable "$WG_UNIT" >/dev/null 2>&1 || true
    if [ "$restart" = 1 ]; then
        systemctl restart "$WG_UNIT" >/dev/null 2>&1 || true
    else
        systemctl start "$WG_UNIT" >/dev/null 2>&1 || true
    fi
    systemctl is-active --quiet "$WG_UNIT" || fail start_failed "$WG_UNIT не поднялся (journalctl -u $WG_UNIT)"
}

wg_remove() {
    wg_ours || return 0
    systemctl disable --now "$WG_UNIT" >/dev/null 2>&1 || true
    rm -f "$WG_CONF" "$SYSCTL_FILE"
}

wg_sync() {
    wg_up || return 0
    wg syncconf "$WG_IFACE" <(wg-quick strip "$WG_CONF") || fail start_failed "wg syncconf $WG_IFACE"
}

wg_next_ip() {
    local base last used i ip
    base=$(ip2int "${WG_NET%/*}")
    last=$(( base + (1 << (32 - ${WG_NET#*/})) - 2 ))
    used=" $(sed -n 's/^AllowedIPs[[:space:]]*=[[:space:]]*\([0-9.]*\)\/32.*/\1/p' "$WG_CONF" | tr '\n' ' ') "
    for (( i = base + 2; i <= last; i++ )); do
        ip=$(int2ip "$i")
        case "$used" in *" $ip "*) ;; *) echo "$ip"; return 0 ;; esac
    done
    return 1
}

# Пир клиента: блок в ft-wg0.conf + клиентский конфиг; результат - в PEER_IP/PEER_PUB.
wg_peer_add() {
    local name=$1 priv
    PEER_IP=$(wg_next_ip) || fail subnet_full "в подсети $WG_NET нет свободных адресов"
    priv=$(wg genkey)
    PEER_PUB=$(wg pubkey <<< "$priv")
    printf '\n[Peer]\n# ft-client: %s\nPublicKey = %s\nAllowedIPs = %s/32\n' "$name" "$PEER_PUB" "$PEER_IP" >> "$WG_CONF"
    wg_sync
    cat > "$(client_dir "$name")/$name.conf" <<EOF
[Interface]
PrivateKey = $priv
Address = $PEER_IP/32
DNS = 1.1.1.1, 1.0.0.1
MTU = $WG_MTU

[Peer]
PublicKey = $(wg_server_pub)
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = $CLIENT_LISTEN
PersistentKeepalive = 25
EOF
}

wg_cut_peer() { # conf name
    awk -v name="$2" '
        function flush() { for (j = 0; j < n; j++) print buf[j]; n = 0 }
        /^[ \t]*\[/ { if (insec) { if (drop) n = 0; flush() }; insec = 1; drop = 0; buf[n++] = $0; next }
        { if (!insec) { print; next }; buf[n++] = $0; l = $0; gsub(/[ \t\r]/, "", l); if (l == "#ft-client:" name) drop = 1 }
        END { if (insec) { if (drop) n = 0; flush() } }
    ' "$1"
}

wg_peer_remove() {
    wg_ours || return 0
    wg_cut_peer "$WG_CONF" "$1" > "$WG_CONF.tmp"
    chmod 0600 "$WG_CONF.tmp"
    mv -f "$WG_CONF.tmp" "$WG_CONF"
    wg_sync
}

# ─────────────────────────────────────────────────────────────────────────────
# Клиенты: строка clients.list = name|client_id|ip|pub|created (ip/pub - только при своём WG).

client_dir() { printf '%s/%s' "$CLIENTS_DIR" "$1"; }
meta_get()   { awk -F'|' -v n="$1" '$1 == n { print; exit }' "$CLIENTS_META" 2>/dev/null || true; }
meta_has()   { [ -n "$(meta_get "$1")" ]; }

meta_put() {
    local name=${1%%|*}
    awk -F'|' -v n="$name" -v l="$1" '$1 == n { print l; f = 1; next } { print } END { if (!f) print l }' \
        "$CLIENTS_META" 2>/dev/null > "$CLIENTS_META.tmp" || printf '%s\n' "$1" > "$CLIENTS_META.tmp"
    mv -f "$CLIENTS_META.tmp" "$CLIENTS_META"
}

meta_del() {
    [ -f "$CLIENTS_META" ] || return 0
    awk -F'|' -v n="$1" '$1 != n' "$CLIENTS_META" > "$CLIENTS_META.tmp"
    mv -f "$CLIENTS_META.tmp" "$CLIENTS_META"
}

auth_init() {
    mkdir -p "$AUTH_DIR" "$CLIENTS_DIR"
    chmod 0700 "$AUTH_DIR" "$CLIENTS_DIR"
    [ -f "$CLIENTSFILE" ] || echo '{"clients":{}}' > "$CLIENTSFILE"
}

# Правка напрямую: сервер перечитывает clients.json по mtime (hot reload).
allow_add() {
    jq --arg id "$1" --arg c "$2" '.clients[$id] = {comment: $c}' "$CLIENTSFILE" > "$CLIENTSFILE.tmp" \
        && mv -f "$CLIENTSFILE.tmp" "$CLIENTSFILE"
}

allow_del() {
    jq --arg id "$1" 'del(.clients[$id])' "$CLIENTSFILE" > "$CLIENTSFILE.tmp" \
        && mv -f "$CLIENTSFILE.tmp" "$CLIENTSFILE"
}

# freeturn://base64url(json), схема - internal/uri. n/spc не пишем: у клиента свои дефолты.
freeturn_uri() { # cid name wg_conf
    local json b64
    json="{\"v\":1,\"provider\":\"vk\",\"peer\":\"$(esc "$(join_hostport "$PUBLIC_HOST" "$LISTEN_PORT")")\""
    [ "$PROXY_MODE" = udp ] || json="$json,\"mode\":\"$PROXY_MODE\""
    [ "$OBF_PROFILE" = none ] || json="$json,\"obf\":\"$OBF_PROFILE\",\"key\":\"$OBF_KEY\""
    json="$json,\"cid\":\"$1\",\"name\":\"$(esc "$2")\""
    [ -z "$3" ] || json="$json,\"wg\":\"$(esc "$3")\""
    json="$json}"
    b64=$(printf '%s' "$json" | openssl base64 -A | tr '+/' '-_' | tr -d '=')
    printf 'freeturn://%s' "$b64"
}

client_conf_file() { printf '%s/%s.conf' "$(client_dir "$1")" "$1"; }

client_link() { # name cid
    local conf wg=""
    conf=$(client_conf_file "$1")
    [ -f "$conf" ] && wg=$(cat "$conf")
    freeturn_uri "$2" "$1" "$wg"
}

# Ссылки несут host/порт/ключ - после apply пересобираются все.
clients_links_write() {
    local name cid
    [ -f "$CLIENTS_META" ] || return 0
    while IFS='|' read -r name cid _; do
        [ -d "$(client_dir "$name")" ] || continue
        client_link "$name" "$cid" > "$(client_dir "$name")/$name.link"
    done < "$CLIENTS_META"
}

client_create() {
    local name=$1 cid ip="" pub=""
    meta_has "$name" && fail exists "клиент '$name' уже есть"
    mkdir -p "$(client_dir "$name")"
    cid=$(openssl rand -hex 16)
    allow_add "$cid" "$name" || fail internal "не удалось обновить $CLIENTSFILE"
    if [ "$BACKEND" = new ]; then
        wg_peer_add "$name"
        ip=$PEER_IP pub=$PEER_PUB
    fi
    meta_put "$name|$cid|$ip|$pub|$(date '+%Y-%m-%d %H:%M')"
    client_link "$name" "$cid" > "$(client_dir "$name")/$name.link"
}

client_delete() {
    local name=$1 cid
    [ "$name" = "$OWNER" ] && fail owner_protected "хозяина удалить нельзя"
    meta_has "$name" || fail not_found "клиента '$name' нет"
    cid=$(meta_get "$name" | cut -d'|' -f2)
    allow_del "$cid" || fail internal "не удалось обновить $CLIENTSFILE"
    wg_peer_remove "$name"
    rm -rf "$(client_dir "$name")"
    web_unlink "$name"
    meta_del "$name"
}

# Хозяин есть всегда; при своём WG у каждого клиента есть пир.
clients_sync() {
    meta_has "$OWNER" || client_create "$OWNER"
    if [ "$BACKEND" = new ]; then
        local name cid ip pub created
        while IFS='|' read -r name cid ip pub created; do
            [ -z "$pub" ] || continue
            mkdir -p "$(client_dir "$name")"
            wg_peer_add "$name"
            meta_put "$name|$cid|$PEER_IP|$PEER_PUB|$created"
        done < <(cat "$CLIENTS_META")
    fi
    clients_links_write
}

# После сноса WG пиров нет: у клиентов остаётся только FreeTurn-часть.
clients_drop_peers() {
    [ -f "$CLIENTS_META" ] || return 0
    local name cid ip pub created
    while IFS='|' read -r name cid ip pub created; do
        rm -f "$(client_conf_file "$name")"
        meta_put "$name|$cid|||$created"
    done < <(cat "$CLIENTS_META")
    clients_links_write
}

# ─────────────────────────────────────────────────────────────────────────────
# Применение конфигурации: общий путь TUI и RPC apply. Идемпотентно.

apply_config() {
    require_root
    ensure_base_deps
    validate_config
    with_lock
    if [ "$BACKEND" = external ] && wg_ours; then
        fail backend_locked "свой WireGuard ещё стоит - сначала uninstall --target=wg"
    fi
    auth_init
    [ "$BACKEND" = new ] && wg_apply
    save_config
    ft_apply
    clients_sync
    install_cli
}

install_cli() {
    local target="$PREFIX/install.sh" cur="${BASH_SOURCE[0]:-$0}"
    if [ -f "$cur" ]; then
        [ "$(readlink -f "$cur" 2>/dev/null || true)" = "$(readlink -f "$target" 2>/dev/null || true)" ] \
            || cp -f "$cur" "$target" 2>/dev/null || return 0
    else
        # curl | bash и RPC по ssh: файла скрипта нет.
        _dl "https://raw.githubusercontent.com/$REPO/master/scripts/install.sh" "$target" 2>/dev/null || return 0
    fi
    chmod 0755 "$target"
    ln -sf "$target" /usr/local/bin/freeturn 2>/dev/null || true
}

do_uninstall() {
    local target=$1
    with_lock
    case "$target" in
        freeturn|all)
            ft_remove
            firewall_close_port "$LISTEN_PORT" udp
            rm -f "$BIN" "$VERFILE"
            log_success "FreeTurn удалён." ;;
    esac
    case "$target" in
        wg|all)
            if wg_ours; then
                wg_remove
                clients_drop_peers
                log_success "WireGuard $WG_IFACE удалён."
            fi ;;
    esac
    if [ "$target" = all ]; then
        stop_web_server
        if [ "$PURGE" = 1 ]; then
            rm -rf "$PREFIX"
            rm -f /usr/local/bin/freeturn
            log_success "Каталог $PREFIX удалён."
        fi
    fi
    return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# RPC-команды

RPC_COMMANDS="probe apply stop restart logs client-list client-add client-conf client-remove uninstall"

is_rpc_command() {
    local c
    for c in $RPC_COMMANDS; do [ "$1" = "$c" ] && return 0; done
    return 1
}

require_conf() { [ -f "$CONF_FILE" ] || fail not_installed "сервер не установлен"; }

require_name() {
    [ -n "$NAME" ] || fail bad_arg "--name обязателен"
    meta_has "$NAME" || fail not_found "клиента '$NAME' нет"
}

conf_b64() { [ -f "$1" ] && base64 < "$1" | tr -d '\n'; }

config_json() {
    printf '{"method":"%s","backend":"%s","connect":"%s","host":"%s","listen_port":%s,"wg_port":%s,"wg_net":"%s","mode":"%s","obf_profile":"%s","obf_key":"%s","version":"%s"}' \
        "$INSTALL_METHOD" "$BACKEND" "$CONNECT" "$PUBLIC_HOST" "$LISTEN_PORT" "$WG_PORT" "$WG_NET" \
        "$PROXY_MODE" "$OBF_PROFILE" "$OBF_KEY" "$VERSION"
}

client_json() { # meta-строка, вывод wg show latest-handshakes
    local name cid ip pub created hs el
    IFS='|' read -r name cid ip pub created <<< "$1"
    el="{\"name\":\"$name\",\"client_id\":\"$cid\",\"self\":$([ "$name" = "$OWNER" ] && echo true || echo false)"
    if [ -n "$pub" ]; then
        hs=$(printf '%s\n' "$2" | awk -v p="$pub" '$1 == p { print $2 }')
        el="$el,\"ip\":\"$ip\",\"pub\":\"$pub\",\"hs\":${hs:-0}"
    fi
    printf '%s}' "$el"
}

cmd_probe() {
    stage probe
    local installed=false
    ft_installed && installed=true
    d_bool installed "$installed"
    d_bool running "$([ "$installed" = true ] && ft_running && echo true || echo false)"
    d_num euid "$(id -u 2>/dev/null || echo -1)"
    d_str arch "$(uname -m 2>/dev/null || true)"
    if [ -f "$CONF_FILE" ]; then
        d_str method "$INSTALL_METHOD"
        d_str version "$(ft_version)"
        d_raw config "$(config_json)"
    fi
    ok
}

cmd_apply() {
    stage apply
    apply_config
    local line cid pub conf
    line=$(meta_get "$OWNER")
    cid=$(cut -d'|' -f2 <<< "$line")
    pub=$(cut -d'|' -f4 <<< "$line")
    conf=$(client_conf_file "$OWNER")
    d_raw owner "{\"client_id\":\"$cid\",\"pub\":\"$pub\",\"conf_b64\":\"$(conf_b64 "$conf" || true)\",\"link\":\"$(client_link "$OWNER" "$cid")\"}"
    d_str obf_key "$OBF_KEY"
    d_bool needs_restart "$([ -n "$PREV_SHARE" ] && [ "$PREV_SHARE" != "$(share_fingerprint)" ] && echo true || echo false)"
    ok
}

cmd_stop() {
    stage stop
    require_root
    ft_installed || fail not_installed "FreeTurn не установлен"
    with_lock
    ft_stop
    d_bool stopped true
    ok
}

cmd_restart() {
    stage restart
    require_root
    ft_installed || fail not_installed "FreeTurn не установлен"
    with_lock
    ft_restart
    d_bool running true
    ok
}

cmd_logs() {
    stage logs
    ft_installed || fail not_installed "FreeTurn не установлен"
    [[ "$TAIL" =~ ^[0-9]{1,5}$ ]] || fail bad_arg "tail: число"
    local l out="" first=1
    while IFS= read -r l; do
        [ "$first" = 1 ] && first=0 || out="$out,"
        out="$out\"$(esc "$l")\""
    done < <(ft_logs "$TAIL")
    d_raw lines "[$out]"
    ok
}

cmd_client_list() {
    stage client_list
    require_conf
    local hs="" line out="" first=1
    wg_up && hs=$(wg show "$WG_IFACE" latest-handshakes 2>/dev/null || true)
    if [ -f "$CLIENTS_META" ]; then
        while IFS= read -r line; do
            [ -n "$line" ] || continue
            [ "$first" = 1 ] && first=0 || out="$out,"
            out="$out$(client_json "$line" "$hs")"
        done < "$CLIENTS_META"
    fi
    d_raw clients "[$out]"
    d_raw share "{\"backend\":\"$BACKEND\",\"host\":\"$PUBLIC_HOST\",\"port\":$LISTEN_PORT,\"mode\":\"$PROXY_MODE\",\"obf_profile\":\"$OBF_PROFILE\",\"obf_key\":\"$OBF_KEY\"}"
    ok
}

_emit_client() {
    local line cid
    line=$(meta_get "$1")
    cid=$(cut -d'|' -f2 <<< "$line")
    d_raw client "$(client_json "$line" "")"
    [ -f "$(client_conf_file "$1")" ] && d_str conf_b64 "$(conf_b64 "$(client_conf_file "$1")")"
    d_str link "$(client_link "$1" "$cid")"
}

cmd_client_add() {
    stage client_add
    require_root
    require_conf
    [ -n "$NAME" ] || fail bad_arg "--name обязателен"
    with_lock
    client_create "$NAME"
    _emit_client "$NAME"
    ok
}

cmd_client_conf() {
    stage client_conf
    require_conf
    require_name
    _emit_client "$NAME"
    ok
}

cmd_client_remove() {
    stage client_remove
    require_root
    require_conf
    [ -n "$NAME" ] || fail bad_arg "--name обязателен"
    with_lock
    client_delete "$NAME"
    d_bool removed true
    ok
}

cmd_uninstall() {
    stage uninstall
    require_root
    do_uninstall "$TARGET"
    d_str target "$TARGET"
    ok
}

# ─────────────────────────────────────────────────────────────────────────────
# Опции: один парсер для RPC (--k=v) и CLI установщика.

kcp_opt() {
    [ "$KCP_SET" = 1 ] || { KCP_ARGS=""; KCP_SET=1; }
    KCP_ARGS="${KCP_ARGS:+$KCP_ARGS }$1"
}

parse_opts() {
    local a v
    while [ $# -gt 0 ]; do
        a=$1 v=${1#*=}
        case "$a" in
            --method=*)      INSTALL_METHOD=$v ;;
            --backend=*)     BACKEND=$v ;;
            --connect=*)     CONNECT=$v; valid_hostport "$v" || fail bad_arg "connect: host:port | [ipv6]:port" ;;
            --host=*)        PUBLIC_HOST=$v; valid_host "$v" || fail bad_arg "host: имя или IP" ;;
            --listen-port=*) LISTEN_PORT=$v ;;
            --wg-port=*)     WG_PORT=$v ;;
            --wg-net=*)      WG_NET=$v ;;
            --mode=*)        PROXY_MODE=$v ;;
            --obf-profile=*) OBF_PROFILE=$v ;;
            --obf-key=*)     OBF_KEY=$v; valid_hex64 "$v" || fail bad_arg "obf-key: 64 hex-символа" ;;
            --version=*)     VERSION=$v ;;
            --bin=*)         LOCAL_BIN=$v; VERSION=local ;;
            --web-port=*)    FT_WEB_PORT=$v ;;
            --web-ttl=*)     FT_WEB_TTL=$v ;;
            --kcp-nodelay=*|--kcp-interval=*|--kcp-resend=*|--kcp-nc=*|--kcp-sndwnd=*|--kcp-rcvwnd=*|--kcp-mtu=*)
                a=${a%%=*}
                [[ "$v" =~ ^[0-9]{1,6}$ ]] || fail bad_arg "$a: число"
                kcp_opt "${a#-} $v" ;;
            --kcp-acknodelay=*)
                [[ "$v" =~ ^(true|false)$ ]] || fail bad_arg "--kcp-acknodelay: true | false"
                kcp_opt "-kcp-acknodelay=$v" ;;
            --tail=*)        TAIL=$v ;;
            --name=*)        NAME=$v; valid_name "$v" || fail bad_arg "name: латиница, цифры, . _ - (до 32)" ;;
            --target=*)      TARGET=$v; [[ "$v" =~ ^(freeturn|wg|all)$ ]] || fail bad_arg "target: freeturn | wg | all" ;;
            --purge)         PURGE=1 ;;
            -y|--yes)        NONINTERACTIVE=1 ;;
            --reconfigure)   ACTION=reconfigure ;;
            --update)        ACTION=update; NONINTERACTIVE=1; FORCE_UPDATE=1 ;;
            --uninstall)     ACTION=uninstall; NONINTERACTIVE=1 ;;
            *)               fail bad_arg "неизвестный аргумент: $a (см. --help)" ;;
        esac
        shift
    done
}

# ─────────────────────────────────────────────────────────────────────────────
# Веб-раздача файлов клиента и QR.

# Секрет живёт столько же, сколько установка: выданные ссылки не протухают. Лежит на
# диске, иначе каждый $(...) выдумывал бы новый.
web_token() {
    if [ -s "$WEB_TOKEN_FILE" ]; then
        tr -d ' \r\n' < "$WEB_TOKEN_FILE"
        return 0
    fi
    local tok; tok=$(openssl rand -hex 16 2>/dev/null) || return 1
    printf '%s\n' "$tok" > "$WEB_TOKEN_FILE" 2>/dev/null || return 1
    printf '%s' "$tok"
}

# Токен на клиента: ссылка 'phone' не открывает файлы 'laptop'. Выводится из мастер-секрета.
client_token() {
    local master h; master=$(web_token) || return 1
    h=$(printf '%s:%s' "$master" "$1" | sha256sum 2>/dev/null | awk '{ print $1 }')
    [[ "$h" =~ ^[0-9a-f]{64}$ ]] || return 1
    printf '%s' "${h:0:32}"
}

web_unlink() {
    [ -s "$WEB_TOKEN_FILE" ] || return 0
    local tok; tok=$(client_token "$1" 2>/dev/null || true)
    [ -z "$tok" ] || rm -f "$WEB_ROOT/$tok"
}

# docroot отдаёт только index.html; каталог клиента - за симлинком с именем-секретом.
_web_layout() {
    local master; master=$(web_token) || { log_warn "Не удалось сохранить $WEB_TOKEN_FILE - веб-раздача выключена."; return 1; }
    mkdir -p "$WEB_ROOT" "$CLIENTS_DIR" 2>/dev/null || return 1
    chmod 0755 "$WEB_ROOT" 2>/dev/null || true
    printf '<!doctype html><title>freeturn</title>\n' > "$WEB_ROOT/index.html"
    chmod 0644 "$WEB_ROOT/index.html" 2>/dev/null || true
    # Пустой маяк на неугадываемом имени: по нему _web_listening узнаёт свой сервер.
    : > "$WEB_ROOT/${master}${WEB_PROBE_EXT}"
    chmod 0644 "$WEB_ROOT/${master}${WEB_PROBE_EXT}" 2>/dev/null || true

    local keep=" " name tok link
    while IFS='|' read -r name _; do
        [ -n "$name" ] && [ -d "$(client_dir "$name")" ] || continue
        tok=$(client_token "$name") || continue
        ln -sfn "$(client_dir "$name")" "$WEB_ROOT/$tok" 2>/dev/null || return 1
        keep="${keep}${tok} "
    done < <(cat "$CLIENTS_META" 2>/dev/null)

    for link in "$WEB_ROOT"/*; do
        [ -L "$link" ] || continue
        case "$keep" in *" ${link##*/} "*) ;; *) rm -f "$link" ;; esac
    done
    return 0
}

_web_systemd_active() { has_systemd && systemctl is-active --quiet "${WEB_UNIT}.service" 2>/dev/null; }

# Пробой по маяку, а не по ss: 200 на нём отдаём только мы, чужой слушатель вернёт 404.
_web_listening() {
    local i=${1:-1}
    while :; do
        curl -fsS --max-time 2 -o /dev/null "http://127.0.0.1:${FT_WEB_PORT}/$(web_token)${WEB_PROBE_EXT}" 2>/dev/null \
            && return 0
        i=$((i - 1))
        [ "$i" -le 0 ] && return 1
        sleep 1
    done
}

# Строкой, а не функцией: исполняется вне скрипта (ExecStopPost, детач-обёртка). Зеркалит
# firewall_open_port - ufw ИЛИ iptables, иначе снесём постоянное правило админа.
_web_close_cmd() {
    local port=$1 ufw_bin ipt_bin
    ufw_bin=$(command -v ufw 2>/dev/null || true)
    ipt_bin=$(command -v iptables 2>/dev/null || true)
    if [ -n "$ufw_bin" ] && ufw status 2>/dev/null | grep -q "Status: active"; then
        printf "%s delete allow %s/tcp >/dev/null 2>&1; exit 0" "$ufw_bin" "$port"
    elif [ -n "$ipt_bin" ]; then
        printf "%s -D INPUT -p tcp --dport %s -j ACCEPT >/dev/null 2>&1; exit 0" "$ipt_bin" "$port"
    else
        printf "exit 0"
    fi
}

# busybox вторым: не умеет листинг каталога.
_web_server_argv() {
    if command -v python3 >/dev/null 2>&1; then
        printf '%s\n' python3 -m http.server "$1" --directory "$WEB_ROOT"
    elif command -v busybox >/dev/null 2>&1; then
        printf '%s\n' busybox httpd -f -p "$1" -h "$WEB_ROOT"
    else
        pkg_install python3 >/dev/null 2>&1 || true
        command -v python3 >/dev/null 2>&1 || return 1
        printf '%s\n' python3 -m http.server "$1" --directory "$WEB_ROOT"
    fi
}

# Транзиентный юнит: RuntimeMaxSec гасит сервер, ExecStopPost закрывает порт при любом исходе.
_web_start_systemd() {
    local port=$1 ttl=$2; shift 2
    has_systemd && command -v systemd-run >/dev/null 2>&1 || return 1
    systemd-run --collect --quiet --unit="$WEB_UNIT" \
        --property=RuntimeMaxSec="$ttl" \
        --property=WorkingDirectory="$WEB_ROOT" \
        --property=ExecStopPost="/bin/sh -c \"$(_web_close_cmd "$port")\"" \
        "$@" >/dev/null 2>&1
}

# Без systemd: одна отвязанная обёртка держит и таймер, и закрытие порта.
_web_start_nohup() {
    local port=$1 ttl=$2; shift 2
    command -v timeout >/dev/null 2>&1 || return 1
    local cmd; cmd=$(printf '%q ' "$@")
    setsid nohup bash -c "timeout $ttl $cmd >>'$WEB_LOG' 2>&1; $(_web_close_cmd "$port")" >/dev/null 2>&1 &
    _web_listening 8
}

ensure_web_server() {
    local port="$FT_WEB_PORT" ttl="$FT_WEB_TTL" a argv=()
    _web_layout || return 1
    # Сервер жив с прошлого вызова - правило файрвола могли снести извне, вернуть.
    if _web_systemd_active || _web_listening; then
        firewall_open_port "$port" tcp
        return 0
    fi
    while IFS= read -r a; do argv+=("$a"); done < <(_web_server_argv "$port")
    [ "${#argv[@]}" -gt 0 ] || { log_warn "Нет ни python3, ни busybox - веб-раздача выключена."; return 1; }
    if command -v ss >/dev/null 2>&1 && [ -n "$(ss -Hltn "sport = :$port" 2>/dev/null || true)" ]; then
        log_warn "Порт $port/tcp занят - веб-раздача выключена, задайте --web-port."
        return 1
    fi
    firewall_open_port "$port" tcp
    if _web_start_systemd "$port" "$ttl" "${argv[@]}"; then
        _web_listening 8 && return 0
        systemctl stop "${WEB_UNIT}.service" >/dev/null 2>&1 || true
    fi
    _web_start_nohup "$port" "$ttl" "${argv[@]}" && return 0
    firewall_close_port "$port" tcp
    log_warn "Не удалось поднять веб-раздачу."
    return 1
}

stop_web_server() {
    has_systemd && systemctl stop "${WEB_UNIT}.service" >/dev/null 2>&1 || true
    pkill -f "http\.server ${FT_WEB_PORT} --directory ${WEB_ROOT}" >/dev/null 2>&1 || true
    pkill -f "httpd -f -p ${FT_WEB_PORT} -h ${WEB_ROOT}" >/dev/null 2>&1 || true
    firewall_close_port "$FT_WEB_PORT" tcp
    rm -rf "$WEB_ROOT" "$WEB_LOG"
}

qr_png() { # src dst
    command -v qrencode >/dev/null 2>&1 || pkg_install qrencode || return 0
    qrencode -s 8 -m 2 -o "$2" < "$1" 2>/dev/null || true
}

show_client_links() {
    local name=$1 dir link url ttl_min=$(( FT_WEB_TTL / 60 ))
    dir=$(client_dir "$name")
    link=$(cat "$dir/$name.link" 2>/dev/null || true)
    qr_png "$dir/$name.link" "$dir/$name-link.png"
    [ -f "$dir/$name.conf" ] && qr_png "$dir/$name.conf" "$dir/$name-wg.png"

    local lines=("Клиент '$name'" "" "Ссылка для приложения FreeTurn:" "$link" "")
    if ensure_web_server; then
        url="http://$(join_hostport "$PUBLIC_HOST" "$FT_WEB_PORT")/$(client_token "$name")"
        lines+=("QR ссылки:        $url/$name-link.png")
        [ -f "$dir/$name.conf" ] && lines+=("WireGuard-конфиг: $url/$name.conf" "QR WireGuard:     $url/$name-wg.png")
        lines+=("" "Раздача живёт $ttl_min мин (снова: freeturn client qr $name)." \
            "Ссылка равносильна ключам клиента - не публикуйте её.")
    fi
    lines+=("Файлы на сервере: $dir")
    echo
    if [ "$HAS_GUM" = 1 ]; then
        gum style --border rounded --border-foreground "$MD_PRIMARY" --padding "1 2" "$(printf '%s\n' "${lines[@]}")"
    else
        printf '  %s\n' "${lines[@]}"
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# CLI: клиенты

client_pick() { # VAR prompt
    [ -s "$CLIENTS_META" ] || fail not_found "клиентов нет"
    local names=() n sel
    while IFS='|' read -r n _; do names+=("$n"); done < "$CLIENTS_META"
    if [ "$HAS_GUM" = 1 ]; then
        sel=$(gum choose --header "$2" "${names[@]}" </dev/tty) || ui_abort
        printf -v "$1" '%s' "$sel"
    else
        ui_input "$1" "$2" "${names[0]}"
    fi
}

client_list() {
    [ -s "$CLIENTS_META" ] || { log_warn "Клиентов нет."; return 0; }
    local name cid ip pub created
    echo
    if [ "$HAS_GUM" = 1 ]; then
        { echo "ИМЯ,IP,CLIENT_ID,СОЗДАН"
          while IFS='|' read -r name cid ip pub created; do echo "$name,${ip:--},$cid,$created"; done < "$CLIENTS_META"
        } | gum table --border rounded --print
    else
        printf '%-16s %-16s %-34s %s\n' "Имя" "IP" "Client ID" "Создан"
        while IFS='|' read -r name cid ip pub created; do
            printf '%-16s %-16s %-34s %s\n' "$name" "${ip:--}" "$cid" "$created"
        done < "$CLIENTS_META"
    fi
}

client_cli() {
    [ -f "$CONF_FILE" ] || fail not_installed "Сервер не установлен: запустите freeturn без аргументов."
    local sub=${1:-} name=${2:-}
    case "$sub" in
        add)
            [ -n "$name" ] || ask name "Имя клиента" "client-$(( $(wc -l 2>/dev/null < "$CLIENTS_META" || echo 0) + 1 ))" valid_name
            valid_name "$name" || fail bad_arg "Имя: латиница, цифры, . _ - (до 32 символов)"
            with_lock
            client_create "$name"
            log_success "Клиент '$name' добавлен."
            show_client_links "$name" ;;
        list)
            client_list ;;
        qr)
            [ -n "$name" ] || client_pick name "Клиент:"
            meta_has "$name" || fail not_found "Клиента '$name' нет."
            show_client_links "$name" ;;
        remove)
            [ -n "$name" ] || client_pick name "Удалить клиента:"
            with_lock
            client_delete "$name"
            log_success "Клиент '$name' удалён." ;;
        *)
            fail bad_arg "Использование: freeturn client <add|list|qr|remove> [имя]" ;;
    esac
}

# ─────────────────────────────────────────────────────────────────────────────
# TUI-мастер

wizard() {
    ui_menu BACKEND "VPN за FreeTurn:" "$BACKEND" \
        new      "Создать WireGuard на этом сервере" \
        external "Мой VPN (уже поднят: Amnezia, wg-easy, 3x-ui, Xray...)"
    if [ "$BACKEND" = new ]; then
        PROXY_MODE=udp
        ask WG_NET  "Подсеть WireGuard" "$WG_NET" valid_net
        ask WG_PORT "Порт WireGuard (локальный, наружу не открывается)" "$WG_PORT" valid_port
    else
        ask CONNECT "Адрес VPN host:port (для VPN на этом сервере - 127.0.0.1:порт)" \
            "${CONNECT:-127.0.0.1:51820}" valid_hostport
        ui_menu PROXY_MODE "Протокол VPN:" "$PROXY_MODE" \
            udp "UDP (WireGuard / AmneziaWG / Hysteria)" \
            tcp "TCP (Xray / sing-box)"
    fi
    ui_menu INSTALL_METHOD "Запуск FreeTurn:" "$INSTALL_METHOD" \
        docker  "Docker" \
        systemd "systemd (бинарь на хосте)"
    ask LISTEN_PORT "Внешний порт FreeTurn (UDP)" "$LISTEN_PORT" valid_port
    ui_menu OBF_PROFILE "Обфускация:" "$OBF_PROFILE" \
        rtpopus3 "rtpopus3 (рекомендуется)" \
        rtpopus2 "rtpopus2" \
        rtpopus  "rtpopus" \
        none     "none (без обфускации - VK режет скорость)"
    [ -n "$PUBLIC_HOST" ] || PUBLIC_HOST=$(get_public_ip || true)
    ask PUBLIC_HOST "Адрес сервера для ссылок клиентов" "$PUBLIC_HOST" valid_host
}

review_config() {
    local lines=("Настройки" "")
    if [ "$BACKEND" = new ]; then
        lines+=("$(printf '%-18s %s' "VPN:" "WireGuard $WG_IFACE, $WG_NET, порт $WG_PORT")")
    else
        lines+=("$(printf '%-18s %s' "VPN:" "$CONNECT ($PROXY_MODE)")")
    fi
    lines+=("$(printf '%-18s %s' "Запуск:" "$INSTALL_METHOD")"
            "$(printf '%-18s %s' "FreeTurn:" "$(join_hostport "$PUBLIC_HOST" "$LISTEN_PORT")/udp")"
            "$(printf '%-18s %s' "Обфускация:" "$OBF_PROFILE")")
    echo
    if [ "$HAS_GUM" = 1 ]; then
        gum style --border double --border-foreground "$MD_PRIMARY" --padding "1 2" "$(printf '%s\n' "${lines[@]}")"
    else
        printf '  %s\n' "${lines[@]}"
    fi
    ui_drain_input
    ui_yesno "Применить?" "Y" || ui_abort
}

run_apply() {
    apply_config
    log_success "Сервер готов."
    show_client_links "$OWNER"
    log_info "Клиенты: freeturn client <add|list|qr|remove>"
}

flow_install() { wizard; review_config; run_apply; }

flow_uninstall() {
    local choice
    ui_menu choice "Что удалить?" "all" \
        freeturn "Только FreeTurn" \
        wg       "Только WireGuard $WG_IFACE" \
        all      "Всё" \
        back     "Отмена"
    [ "$choice" = back ] && return 0
    ui_yesno "Точно удалить ($choice)?" "N" || ui_abort
    [ "$choice" = all ] && ui_yesno "Удалить и данные (клиенты, ключи, $PREFIX)?" "N" && PURGE=1
    do_uninstall "$choice"
}

menu_clients() {
    local c
    while :; do
        ui_menu c "Клиенты:" add add "Добавить" list "Список" qr "Ссылки и QR" remove "Удалить" back "Назад"
        case "$c" in
            back) return 0 ;;
            *) ( client_cli "$c" ) || true ;;
        esac
    done
}

menu_existing() {
    local choice
    while :; do
        ui_menu choice "Сервер настроен. Действие:" clients \
            clients     "Клиенты" \
            reconfigure "Изменить настройки" \
            update      "Обновить FreeTurn" \
            restart     "Перезапустить FreeTurn" \
            logs        "Логи" \
            uninstall   "Удалить" \
            exit        "Выход"
        case "$choice" in
            clients)     menu_clients ;;
            reconfigure) flow_install; return 0 ;;
            update)      FORCE_UPDATE=1; run_apply; return 0 ;;
            restart)     ft_restart; log_success "FreeTurn перезапущен." ;;
            logs)        ft_logs 40 ;;
            uninstall)   flow_uninstall; return 0 ;;
            exit)        return 0 ;;
        esac
    done
}

usage() {
    cat <<EOF
Free Turn Proxy - установщик и контроллер сервера.

  freeturn                           интерактивный мастер
  freeturn client add|list|qr|remove [имя]
  freeturn -y [опции]                установка без вопросов
  freeturn --update                  обновить FreeTurn с текущими настройками
  freeturn --uninstall [--target=freeturn|wg|all] [--purge]

Опции (--ключ=значение):
  --backend=new|external   new - создать WireGuard $WG_IFACE; external - свой VPN по --connect
  --connect=HOST:PORT      адрес VPN для external
  --mode=udp|tcp           протокол VPN (tcp - Xray/sing-box, только external)
  --wg-net=CIDR            подсеть WireGuard (default $WG_NET)
  --wg-port=N              порт WireGuard (default $WG_PORT, наружу не открывается)
  --method=docker|systemd  запуск FreeTurn (default docker)
  --listen-port=N          внешний UDP-порт FreeTurn (default $LISTEN_PORT)
  --obf-profile=P          rtpopus3 | rtpopus2 | rtpopus | none (default rtpopus3)
  --obf-key=HEX64          ключ обфускации (нет - сгенерируется)
  --host=HOST              адрес сервера в ссылках (нет - внешний IP)
  --version=TAG            версия FreeTurn (default latest)
  --bin=PATH               свой бинарь сервера вместо релиза (version=local)
  --kcp-*=N                параметры KCP сервера для --mode=tcp
  --web-port=N --web-ttl=S веб-раздача файлов клиентов (default 8080, 900 c)

JSON RPC (приложение, proto $PROTO_VERSION):
  freeturn <${RPC_COMMANDS// /|}> [--ключ=значение ...]
EOF
}

main() {
    case "${1:-}" in -h|--help) usage; return 0 ;; esac

    if is_rpc_command "${1:-}"; then
        _IS_RPC=1
        local cmd=$1; shift
        load_config
        parse_opts "$@"
        "cmd_${cmd//-/_}"
        return 0
    fi
    case "${1:-}" in
        ""|-*|client) ;;
        *) _IS_RPC=1; fail bad_arg "unknown command: $1" ;;
    esac

    require_root
    ensure_base_deps
    load_config
    if [ "${1:-}" = client ]; then
        shift
        ensure_gum
        client_cli "$@"
        return 0
    fi
    parse_opts "$@"

    if [ "$ACTION" = uninstall ]; then do_uninstall "$TARGET"; return 0; fi
    if [ "$ACTION" = update ] && ! [ -f "$CONF_FILE" ]; then fail not_installed "Сервер не установлен."; fi
    if [ "$NONINTERACTIVE" = 1 ]; then run_apply; return 0; fi

    ensure_gum
    ui_banner
    if [ "$ACTION" = reconfigure ] || ! [ -f "$CONF_FILE" ]; then
        flow_install
    else
        menu_existing
    fi
    ui_drain_input
}

if [ "${BASH_SOURCE[0]:-$0}" = "$0" ]; then
    main "$@"
fi
