#!/usr/bin/env bash
# Free Turn Proxy - установщик и контроллер сервера.
# Сгенерирован из scripts/control/*.sh.

set -Eeuo pipefail
umask 077

PROTO_VERSION=2

# Пути
PREFIX="${FT_PREFIX:-/opt/free-turn-proxy}"
STATE_FILE="$PREFIX/state"
PIDFILE="$PREFIX/proxy.pid"
LOCKFILE="$PREFIX/control.lock"
PEERS_LOCK="$PREFIX/peers.lock"
LOGFILE="$PREFIX/server.log"
VERFILE="$PREFIX/version"
ARGSFILE="$PREFIX/run.args"
ENVFILE="$PREFIX/run.env"
LAUNCHER="$PREFIX/launch.sh"
CLIENTSFILE="$PREFIX/clients.json"
OWNERCIDFILE="$PREFIX/owner.cid"
SHARE_DIR="$PREFIX/share"
APP_DIR="$PREFIX"
CONF_FILE="${PREFIX}/install.conf"

# Службы и контейнеры
SERVICE="free-turn-proxy.service"
UNIT_NAME="$SERVICE"
UNIT_FILE="/etc/systemd/system/${SERVICE}"
UNIT_PATH="$UNIT_FILE"
LEGACY_UNIT="vk-turn-proxy.service"
COMPOSE_FILE="${PREFIX}/docker-compose.yml"
CONTAINER="free-turn-proxy"
AWG_CONTAINER="freeturn-awg"
GUM_VERSION="0.17.0"

# AmneziaWG и WireGuard
AWG_DIR="${PREFIX}/awg"
AWG_IFACE="${FT_AWG_IFACE:-ftawg0}"
AWG_CONF="${AWG_DIR}/${AWG_IFACE}.conf"
AWG_NET="10.13.13"
WG_DIR="${FT_WG_DIR:-/etc/wireguard}"
WG_IFACE="${FT_WG_IFACE:-ft-wg0}"
WG_CONF="${WG_DIR}/${WG_IFACE}.conf"
WG_MARKER="# managed-by: free-turn-proxy"
WG_NET="${FT_WG_NET:-10.13.13}"
WG_CLIENT_CONF="${PREFIX}/wireguard-client.conf"
WG_MTU="${FT_WG_MTU:-1280}"
CLIENTS_DIR="${PREFIX}/clients"
CLIENTS_META="${CLIENTS_DIR}/clients.list"

# Репозитории и ссылки
REPO="samosvalishe/free-turn-proxy"
IMAGE="ghcr.io/${REPO}"
AWG_IMAGE="ghcr.io/samosvalishe/freeturn-awg:latest"
RELEASES_URL="https://github.com/${REPO}/releases"
BASE_URL="${RELEASES_URL}/latest/download"

# Цвета (Material Design 3 + ANSI fallback)
MD_PRIMARY="#D0BCFF"
MD_SECONDARY="#CCC2DC"
MD_TERTIARY="#EFB8C8"
MD_SUCCESS="#81C784"
MD_ERROR="#F2B8B5"
BANNER_GRADIENT=("#EADDFF" "#D0BCFF" "#B69DF8" "#9A82DB" "#7F67BE" "#6750A4")
C_RED='\033[0;31m' C_GREEN='\033[0;32m' C_YELLOW='\033[1;33m' C_CYAN='\033[0;36m' C_NC='\033[0m'

# Конфигурация сервера
INSTALL_METHOD="docker"        # docker | systemd
INSTALL_FREETURN=1             # 1 = ставить релей FreeTurn
INSTALL_AWG=1                  # 1 = ставить AmneziaWG 3.1
INSTALL_WG=0                   # 1 = ставить WireGuard
VERSION="latest"
PROVIDER="vk"
PROXY_MODE="udp"               # udp | tcp
BACKEND_TYPE="awg"             # awg | wg | custom
BACKEND_PORT="51820"
LISTEN_PORT="56000"
AWG_DIRECT_PORT=1              # 1 = открыть BACKEND_PORT/udp в файрволе
OBF_PROFILE="rtpopus3"         # rtpopus3 | rtpopus2 | rtpopus | none
OBF_KEY=""
CLIENTS_FILE_CONF="${CLIENTS_DIR}/clients.json"
WG_ENDPOINT="127.0.0.1:9000"

# AmneziaWG 3.1 параметры
AWG_JC=""
AWG_JMIN=""
AWG_JMAX=""
AWG_S1=""
AWG_S2=""
AWG_S3=""
AWG_S4=""
AWG_H1=""
AWG_H2=""
AWG_H3=""
AWG_H4=""
AWG_HPK=""

# Состояние рантайма
HAS_GUM=0
GOARCH=""
WG_PORT=""
NONINTERACTIVE=0
OPEN_FIREWALL=""
PURGE=0
ACTION=""
CLIENT_SUBCOMMAND=""
CLIENT_SUBARG=""
UNINSTALL_TARGET="all"         # freeturn | awg | all
OVERRIDES=()

PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"
export PATH
export COLORFGBG="15;0"

valid_port()     { [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }
valid_hex64()    { [[ "$1" =~ ^[0-9a-fA-F]{64}$ ]]; }
valid_endpoint() { [[ "$1" =~ ^(\[[0-9a-fA-F:]+\]|[a-zA-Z0-9._-]+):[0-9]{1,5}$ ]]; }

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 10-proto.sh
# ─────────────────────────────────────────────────────────────────────────────
# JSON RPC v2 для Android ServerControl.kt (буферизация в 1 JSON-объект, trap EXIT)

_DATA=()       # "key":<json-value>
_LOGS=()       # заковыченные JSON-строки
_EMITTED=0     # защита от двойной печати
_STAGE="init"
_IS_RPC=0      # 1 = режим JSON RPC для Android / API

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

log() {
    if [ "$_IS_RPC" = 1 ]; then
        _LOGS+=("\"$(esc "$*")\"")
    else
        log_info "$*"
    fi
}

_join() { local IFS=','; printf '%s' "${*-}"; }
_data_json() { if [ "${#_DATA[@]}" -gt 0 ]; then _join "${_DATA[@]}"; fi; }
_logs_json() { if [ "${#_LOGS[@]}" -gt 0 ]; then _join "${_LOGS[@]}"; fi; }

ok() {
    [ "$_EMITTED" -eq 1 ] && return 0
    _EMITTED=1
    trap - EXIT
    printf '{"proto":%d,"result":"ok","data":{%s},"logs":[%s]}\n' \
        "$PROTO_VERSION" "$(_data_json)" "$(_logs_json)"
}

fail() {
    local code=$1 msg=${2:-$1}
    [ "$_EMITTED" -eq 1 ] && exit 1
    _EMITTED=1
    trap - EXIT
    printf '{"proto":%d,"result":"err","code":"%s","msg":"%s","stage":"%s","logs":[%s]}\n' \
        "$PROTO_VERSION" "$code" "$(esc "$msg")" "$_STAGE" "$(_logs_json)"
    exit 1
}

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
# Модуль: 20-ui.sh
# ─────────────────────────────────────────────────────────────────────────────
# UI-слой: обёртки над gum с plain-fallback при отсутствии gum.
gum() {
    local sub="${1:-}"
    case "$sub" in
        style|join|format|log)
            if [ -t 0 ]; then
                command gum "$@" </dev/null
            else
                command gum "$@"
            fi
            ;;
        *)
            command gum "$@"
            ;;
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

die() {
    if [ "$_IS_RPC" = 1 ]; then fail internal "$1"; fi
    log_error "$1"
    exit 1
}

ui_drain_input() {
    [ -t 0 ] || [ -r /dev/tty ] || return 0
    stty -echo </dev/tty 2>/dev/null || true
    local discard
    while read -r -t 0.05 -n 1000 discard </dev/tty 2>/dev/null; do :; done
    stty echo </dev/tty 2>/dev/null || true
}

ui_abort() { ui_drain_input; log_info "Отменено."; exit 0; }

ui_banner() {
    [ "$_IS_RPC" = 1 ] && return 0
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
        gum style --foreground "$MD_SECONDARY" --italic --margin "0 0 1 1" \
            "FreeTurn & AmneziaWG 3.1  ·  установщик сервера"
    else
        echo -e "${C_CYAN}"; printf '%s\n' "${art[@]}"; echo -e "${C_NC}"
        echo "  FreeTurn & AmneziaWG 3.1 · установщик сервера"; echo
    fi
}

ui_note() {
    [ "$_IS_RPC" = 1 ] && return 0
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
    else
        if [ -n "$__def" ]; then read -r -p "$__prompt [$__def]: " __ans </dev/tty
        else read -r -p "$__prompt: " __ans </dev/tty; fi
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
    local hint; [ "$__def" = "Y" ] && hint="Y/n" || hint="y/N"
    read -r -p "$__prompt [$hint]: " __ans </dev/tty
    [[ "${__ans:-$__def}" =~ ^[Yy]$ ]]
}

ui_menu() {
    local __var="$1" __prompt="$2" __def_tag="$3"; shift 3
    local tags=() labels=()
    while [ $# -gt 0 ]; do tags+=("$1"); labels+=("$2"); shift 2; done

    if [ "$HAS_GUM" = 1 ]; then
        local i def_label="" sel
        for i in "${!tags[@]}"; do [ "${tags[$i]}" = "$__def_tag" ] && def_label="${labels[$i]}"; done
        sel=$(gum choose --header "$__prompt" --header.foreground "$MD_PRIMARY" \
            --cursor "❯ " --cursor.foreground "$MD_TERTIARY" \
            --selected.foreground "$MD_PRIMARY" --selected "$def_label" \
            "${labels[@]}" </dev/tty) || ui_abort
        for i in "${!labels[@]}"; do
            [ "${labels[$i]}" = "$sel" ] && { printf -v "$__var" '%s' "${tags[$i]}"; return; }
        done
        ui_abort
    else
        local i sel
        echo; log_info "$__prompt"
        for i in "${!tags[@]}"; do
            if [ "${tags[$i]}" = "$__def_tag" ]; then echo -e "  ${C_GREEN}${tags[$i]}${C_NC}) ${labels[$i]}"
            else echo "  ${tags[$i]}) ${labels[$i]}"; fi
        done
        while :; do
            read -r -p "Выбор [${__def_tag}]: " sel </dev/tty
            sel="${sel:-$__def_tag}"
            for i in "${!tags[@]}"; do
                [ "$sel" = "${tags[$i]}" ] && { printf -v "$__var" '%s' "$sel"; return; }
            done
            log_warn "Неверный выбор: $sel"
        done
    fi
}

ask_port() {
    local __var="$1" __prompt="$2" __def="$3"
    while :; do
        ui_input "$__var" "$__prompt" "$__def"
        valid_port "${!__var}" && break
        ui_note "Ошибка" "Порт - число 1-65535. Получено: '${!__var}'"
    done
}

ui_spin() {
    local title="$1"; shift
    local rc=0 log; log="$(mktemp)"
    if [ "$HAS_GUM" = 1 ]; then
        ( "$@" >"$log" 2>&1 ) &
        local pid=$!
        gum spin --spinner dot --spinner.foreground "$MD_PRIMARY" --title "$title" \
            -- bash -c 'while kill -0 "$1" 2>/dev/null; do sleep 0.1; done' _ "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null && rc=0 || rc=$?
        if [ "$rc" -eq 0 ]; then log_success "$title"
        else
            log_error "$title - ошибка (код $rc)"
            tail -n 40 "$log" | gum style --border rounded --border-foreground "$MD_ERROR" --padding "0 1"
        fi
    elif [ -t 1 ]; then
        ( "$@" >"$log" 2>&1 ) &
        local pid=$! i=0 ch='|/-'$'\\'
        while kill -0 "$pid" 2>/dev/null; do
            i=$(((i + 1) % 4)); printf "\r${C_CYAN}[*]${C_NC} %s %s" "$title" "${ch:$i:1}"; sleep 0.2
        done
        wait "$pid" 2>/dev/null && rc=0 || rc=$?
        if [ "$rc" -eq 0 ]; then printf "\r${C_GREEN}[+]${C_NC} %s\033[K\n" "$title"
        else printf "\r${C_RED}[x]${C_NC} %s\033[K\n" "$title"; tail -n 40 "$log" >&2; fi
    else
        log_info "$title..."
        "$@" >"$log" 2>&1 && rc=0 || rc=$?
        [ "$rc" -ne 0 ] && tail -n 40 "$log" >&2
    fi
    rm -f "$log"
    return "$rc"
}

# Генерация файла картинки QR-кода (PNG) с высоким разрешением
generate_qr_png_file() {
    local conf_file="$1" png_file="$2"
    [ -f "$conf_file" ] || return 0
    command -v qrencode >/dev/null 2>&1 || pkg_install qrencode || true
    if command -v qrencode >/dev/null 2>&1; then
        qrencode -s 8 -m 2 -o "$png_file" < "$conf_file" 2>/dev/null || true
        [ -f "$png_file" ] && chmod 0600 "$png_file" 2>/dev/null || true
    fi
}

generate_qr_png_text() {
    local text="$1" png_file="$2"
    [ -n "$text" ] || return 0
    command -v qrencode >/dev/null 2>&1 || pkg_install qrencode || true
    if command -v qrencode >/dev/null 2>&1; then
        printf '%s' "$text" | qrencode -s 8 -m 2 -o "$png_file" 2>/dev/null || true
        [ -f "$png_file" ] && chmod 0600 "$png_file" 2>/dev/null || true
    fi
}

render_qr_file() {
    local file="$1" title="$2" png_file="${3:-${file%.*}.png}"
    [ "$_IS_RPC" = 1 ] && return 0
    if [ ! -f "$file" ]; then
        log_warn "Файл конфигурации '$file' не найден."
        return 1
    fi
    [ -n "$png_file" ] && [ ! -f "$png_file" ] && generate_qr_png_file "$file" "$png_file"

    command -v qrencode >/dev/null 2>&1 || pkg_install qrencode || true
    if ! command -v qrencode >/dev/null 2>&1; then
        log_warn "qrencode не установлен - QR пропущен."
        return 0
    fi

    if [ -n "$title" ]; then
        echo
        if [ "$HAS_GUM" = 1 ]; then
            gum style --foreground "$MD_PRIMARY" --bold "$title"
        else
            echo -e "${C_CYAN}${title}${C_NC}"
        fi
    fi

    # 1. Проверка поддержки inline-графики (iTerm2, WezTerm, Ghostty)
    if [ -n "$png_file" ] && [ -f "$png_file" ] && [ -n "${TERM_PROGRAM:-}" ]; then
        case "$TERM_PROGRAM" in
            iTerm.app|WezTerm|ghostty)
                if command -v base64 >/dev/null 2>&1; then
                    local b64_img; b64_img=$(base64 -w0 < "$png_file" 2>/dev/null || base64 < "$png_file" | tr -d '\r\n')
                    if [ -n "$b64_img" ]; then
                        printf '\033]1337;File=inline=1;width=45%%;height=auto:%s\a\n' "$b64_img"
                        echo
                        log_info "Картинка QR-кода (PNG): $png_file"
                        return 0
                    fi
                fi
                ;;
        esac
    fi

    # 2. Очистка конфига от комментариев и пустых строк для максимальной компактности.
    # Флаг -l L (Low ECC) сжимает размер QR до ~51 символов ширины,
    # что гарантированно помещается в стандартные 80 колонок PowerShell/PuTTY/CMD без переносов строк.
    local clean_conf
    clean_conf=$(grep -v '^[[:space:]]*#' "$file" 2>/dev/null | grep -v '^[[:space:]]*$' || cat "$file")

    local cols=80
    command -v tput >/dev/null 2>&1 && cols=$(tput cols 2>/dev/null || echo 80)
    if [ "$cols" -lt 55 ]; then
        if [ "$HAS_GUM" = 1 ]; then
            gum style --border rounded --border-foreground "$MD_TERTIARY" --padding "0 1" \
                "$(gum style --foreground "$MD_TERTIARY" --bold "⚠ Окно терминала очень узкое (${cols} колонок, нужно ≥55).")" \
                "Рекомендуется использовать сохранённую картинку:" \
                "$(gum style --foreground "$MD_PRIMARY" --bold "${png_file}")"
        else
            log_warn "Окно терминала (${cols}) очень узкое. Используйте PNG файл: $png_file"
        fi
        echo
    fi

    printf '%s\n' "$clean_conf" | qrencode -t ansiutf8 -m 1 -l L
    echo
    [ -n "$png_file" ] && [ -f "$png_file" ] && log_info "Картинка QR-кода (PNG): $png_file"
}

download_file_osc1337() {
    local f="$1"
    [ -f "$f" ] || return 0
    if command -v base64 >/dev/null 2>&1; then
        local b64; b64=$(base64 -w0 < "$f" 2>/dev/null || base64 < "$f" | tr -d '\r\n')
        local fname; fname=$(basename "$f")
        local fsize; fsize=$(wc -c < "$f" 2>/dev/null || echo 0)
        if [ -n "${TERM_PROGRAM:-}" ] || [ -n "${WT_SESSION:-}" ]; then
            printf '\033]1337;File=name=%s;size=%s;inline=0:%s\a' "$fname" "$fsize" "$b64" 2>/dev/null || true
        fi
    fi
}

render_qr_text() {
    local text="$1" title="$2" png_file="${3:-}"
    [ "$_IS_RPC" = 1 ] && return 0
    [ -n "$text" ] || return 0
    [ -n "$png_file" ] && [ ! -f "$png_file" ] && generate_qr_png_text "$text" "$png_file"

    command -v qrencode >/dev/null 2>&1 || pkg_install qrencode || true
    if ! command -v qrencode >/dev/null 2>&1; then
        log_warn "qrencode не установлен - QR пропущен."
        return 0
    fi

    if [ -n "$title" ]; then
        echo
        if [ "$HAS_GUM" = 1 ]; then
            gum style --foreground "$MD_PRIMARY" --bold "$title"
        else
            echo -e "${C_CYAN}${title}${C_NC}"
        fi
    fi

    # 1. Проверка поддержки inline-графики
    if [ -n "$png_file" ] && [ -f "$png_file" ] && [ -n "${TERM_PROGRAM:-}" ]; then
        case "$TERM_PROGRAM" in
            iTerm.app|WezTerm|ghostty)
                if command -v base64 >/dev/null 2>&1; then
                    local b64_img; b64_img=$(base64 -w0 < "$png_file" 2>/dev/null || base64 < "$png_file" | tr -d '\r\n')
                    if [ -n "$b64_img" ]; then
                        printf '\033]1337;File=inline=1;width=45%%;height=auto:%s\a\n' "$b64_img"
                        echo
                        log_info "Картинка QR-кода (PNG): $png_file"
                        return 0
                    fi
                fi
                ;;
        esac
    fi

    # 2. Проверка ширины терминала (для длинных URI со вшитыми конфигами)
    local cols=80
    command -v tput >/dev/null 2>&1 && cols=$(tput cols 2>/dev/null || echo 80)
    if [ "${#text}" -gt 500 ] && [ "$cols" -lt 85 ]; then
        if [ "$HAS_GUM" = 1 ]; then
            gum style --border rounded --border-foreground "$MD_TERTIARY" --padding "0 1" \
                "$(gum style --foreground "$MD_TERTIARY" --bold "⚠ Длинная ссылка со вшитым VPN (${cols} колонок, нужно ≥85).")" \
                "Текстовый код может перенестись по строкам." \
                "Используйте сохранённую картинку:" \
                "$(gum style --foreground "$MD_PRIMARY" --bold "${png_file:-$CLIENTS_DIR}")"
        else
            log_warn "Ширина терминала (${cols}) меньше размера QR. Используйте PNG файл: $png_file"
        fi
        echo
    fi

    printf '%s' "$text" | qrencode -t ansiutf8 -m 1 -l L
    echo
    [ -n "$png_file" ] && [ -f "$png_file" ] && log_info "Картинка QR-кода (PNG): $png_file"
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 30-system.sh
# ─────────────────────────────────────────────────────────────────────────────
# Системные примитивы: блокировки flock, пакетный менеджер, детекция окружения.

with_lock() {
    command -v flock >/dev/null 2>&1 || return 0
    [ -d "$PREFIX" ] || mkdir -p "$PREFIX" 2>/dev/null || return 0
    [ -w "$PREFIX" ] || return 0
    exec 8>"$LOCKFILE" 2>/dev/null || return 0
    flock -w 300 8 2>/dev/null || true
}

with_peers_lock() {
    command -v flock >/dev/null 2>&1 || return 0
    [ -d "$PREFIX" ] || mkdir -p "$PREFIX" 2>/dev/null || return 0
    exec 9>"$PEERS_LOCK" 2>/dev/null || return 0
    flock -w 60 9 2>/dev/null || true
}

pkg_mgr() {
    local m
    for m in apt-get dnf yum apk pacman zypper; do
        if command -v "$m" >/dev/null 2>&1; then echo "$m"; return 0; fi
    done
    return 1
}

_apt_wait_lock() {
    command -v fuser >/dev/null 2>&1 || return 0
    local i=0
    while fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1 \
       || fuser /var/lib/dpkg/lock >/dev/null 2>&1; do
        [ "$i" -ge 60 ] && return 0
        log "ожидание dpkg lock ($((i * 2))s)..."
        sleep 2
        i=$((i + 1))
    done
}

pkg_install() {
    local mgr; mgr=$(pkg_mgr) || return 1
    case "$mgr" in
        apt-get)
            _apt_wait_lock
            DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 \
                apt-get -o DPkg::Lock::Timeout=300 update -qq >/dev/null 2>&1 || true
            DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 \
                apt-get -o DPkg::Lock::Timeout=300 install -y -qq "$@" >/dev/null 2>&1 ;;
        dnf)    dnf install -y -q "$@" >/dev/null 2>&1 ;;
        yum)    yum install -y -q "$@" >/dev/null 2>&1 ;;
        apk)    apk add --no-cache "$@" >/dev/null 2>&1 ;;
        pacman) pacman -Sy --noconfirm "$@" >/dev/null 2>&1 ;;
        zypper) zypper --non-interactive install "$@" >/dev/null 2>&1 ;;
        *)      return 1 ;;
    esac
}

pkg_remove() {
    local mgr; mgr=$(pkg_mgr) || return 1
    case "$mgr" in
        apt-get)
            _apt_wait_lock
            DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 \
                apt-get -o DPkg::Lock::Timeout=300 remove -y -qq "$@" >/dev/null 2>&1 ;;
        dnf)    dnf remove -y -q "$@" >/dev/null 2>&1 ;;
        yum)    yum remove -y -q "$@" >/dev/null 2>&1 ;;
        apk)    apk del "$@" >/dev/null 2>&1 ;;
        pacman) pacman -Rns --noconfirm "$@" >/dev/null 2>&1 ;;
        zypper) zypper --non-interactive remove "$@" >/dev/null 2>&1 ;;
        *)      return 1 ;;
    esac
}
export -f pkg_mgr _apt_wait_lock pkg_install pkg_remove 2>/dev/null || true

ensure_base_deps() {
    local missing=() b
    for b in curl jq openssl tar; do
        command -v "$b" >/dev/null 2>&1 || missing+=("$b")
    done
    [ "${#missing[@]}" -eq 0 ] && return 0
    if [ "$_IS_RPC" = 1 ]; then
        pkg_install "${missing[@]}" || true
    else
        ui_spin "Установка зависимостей: ${missing[*]}" pkg_install "${missing[@]}"
    fi
    missing=()
    for b in curl jq openssl tar; do
        command -v "$b" >/dev/null 2>&1 || missing+=("$b")
    done
    if [ "${#missing[@]}" -ne 0 ]; then
        die "Не удалось установить: ${missing[*]}. Установите их вручную."
    fi
}

gum_download() {
    local ver="$1" arch tmp bin
    case "$(uname -m 2>/dev/null || echo "")" in
        x86_64|amd64)   arch="x86_64" ;;
        aarch64|arm64)  arch="arm64" ;;
        *) return 1 ;;
    esac
    local url="https://github.com/charmbracelet/gum/releases/download/v${ver}/gum_${ver}_Linux_${arch}.tar.gz"
    tmp="$(mktemp -d)"
    if curl -fsSL --connect-timeout 10 --max-time 30 "$url" | tar -xz -C "$tmp" 2>/dev/null; then
        bin="$(find "$tmp" -name gum -type f 2>/dev/null | head -n1 || true)"
        [ -n "$bin" ] && install -m 0755 "$bin" /usr/local/bin/gum 2>/dev/null
    fi
    rm -rf "$tmp"
    command -v gum >/dev/null 2>&1
}

ensure_gum() {
    [ "$_IS_RPC" = 1 ] && { HAS_GUM=0; return 0; }
    command -v gum >/dev/null 2>&1 && { HAS_GUM=1; return 0; }
    log_info "Установка gum ${GUM_VERSION}..."
    if gum_download "$GUM_VERSION"; then HAS_GUM=1; return 0; fi
    local latest
    latest="$(curl -s --max-time 10 'https://api.github.com/repos/charmbracelet/gum/releases/latest' \
        | jq -r '.tag_name // empty' 2>/dev/null | sed 's/^v//' || true)"
    if [ -n "$latest" ] && gum_download "$latest"; then HAS_GUM=1; return 0; fi
    HAS_GUM=0
    log_warn "gum недоступен - классический текстовый режим."
}

compose_cmd() {
    if docker compose version >/dev/null 2>&1; then
        docker compose "$@"
    elif command -v docker-compose >/dev/null 2>&1; then
        docker-compose "$@"
    else
        die "Docker Compose не найден."
    fi
}
export -f compose_cmd 2>/dev/null || true

_install_compose_step() {
    local mgr; mgr=$(pkg_mgr 2>/dev/null || true)
    if [ -n "$mgr" ]; then
        case "$mgr" in
            apt-get)
                pkg_install docker-compose-v2 || pkg_install docker-compose-plugin || pkg_install docker-compose || true ;;
            dnf|yum)
                pkg_install docker-compose-plugin || pkg_install docker-compose || true ;;
            apk)
                pkg_install docker-cli-compose || pkg_install docker-compose || true ;;
            *)
                pkg_install docker-compose || true ;;
        esac
    fi

    if command -v docker-compose >/dev/null 2>&1 && ! docker compose version >/dev/null 2>&1; then
        mkdir -p /usr/local/lib/docker/cli-plugins
        ln -sf "$(command -v docker-compose)" /usr/local/lib/docker/cli-plugins/docker-compose 2>/dev/null || true
    fi

    if docker compose version >/dev/null 2>&1; then
        return 0
    fi

    local m; m=$(uname -m 2>/dev/null || echo "x86_64")
    case "$m" in
        x86_64|amd64)   m="x86_64" ;;
        aarch64|arm64)  m="aarch64" ;;
        armv7*)         m="armv7" ;;
        armv6*)         m="armv6" ;;
        riscv64)        m="riscv64" ;;
        s390x)          m="s390x" ;;
        ppc64le)        m="ppc64le" ;;
        *)              return 1 ;;
    esac

    local plugin_dir="/usr/local/lib/docker/cli-plugins"
    mkdir -p "$plugin_dir" /usr/local/bin
    if curl -fsSL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-$m" \
        -o "$plugin_dir/docker-compose" 2>/dev/null; then
        chmod +x "$plugin_dir/docker-compose"
        ln -sf "$plugin_dir/docker-compose" /usr/local/bin/docker-compose 2>/dev/null || true
        return 0
    fi
    return 1
}
export -f _install_compose_step 2>/dev/null || true

ensure_compose() {
    if docker compose version >/dev/null 2>&1; then
        return 0
    fi
    if command -v docker-compose >/dev/null 2>&1; then
        mkdir -p /usr/local/lib/docker/cli-plugins
        ln -sf "$(command -v docker-compose)" /usr/local/lib/docker/cli-plugins/docker-compose 2>/dev/null || true
        if docker compose version >/dev/null 2>&1; then
            return 0
        fi
    fi

    if [ "$_IS_RPC" = 1 ]; then
        _install_compose_step >/dev/null 2>&1 || fail compose_install_failed "docker compose install failed"
    else
        ui_spin "Установка Docker Compose" _install_compose_step || die "Установка Docker Compose не удалась."
    fi

    if ! docker compose version >/dev/null 2>&1 && ! command -v docker-compose >/dev/null 2>&1; then
        die "Docker Compose не появился в системе."
    fi
}

ensure_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        if [ "$NONINTERACTIVE" != 1 ] && [ "$_IS_RPC" != 1 ]; then
            if ! ui_yesno "Docker не найден. Установить автоматически?" "Y"; then
                die "Для выбранного метода требуется Docker."
            fi
        fi
        if [ "$_IS_RPC" = 1 ]; then
            curl -fsSL https://get.docker.com | sh >/dev/null 2>&1 || fail docker_install_failed "docker install failed"
        else
            ui_spin "Установка Docker" sh -c 'curl -fsSL https://get.docker.com | sh' || die "Установка Docker не удалась."
        fi
        command -v docker >/dev/null 2>&1 || die "Docker не появился в PATH."
    fi

    if ! docker info >/dev/null 2>&1; then
        systemctl start docker >/dev/null 2>&1 || service docker start >/dev/null 2>&1 || true
    fi

    ensure_compose
}

_mips_is_le() {
    local hex; hex=$(printf '\1\0' | od -An -tx2 -N2 2>/dev/null | tr -d ' \n')
    [ "$hex" = "0001" ]
}

detect_arch() {
    local m; m=$(uname -m 2>/dev/null || echo "")
    case "$m" in
        x86_64|amd64)              GOARCH="amd64"; echo "server-linux-amd64" ;;
        aarch64|arm64)             GOARCH="arm64"; echo "server-linux-arm64" ;;
        armv7l|armv6l|armv5*|arm)  GOARCH="arm";   echo "server-linux-arm" ;;
        i386|i486|i586|i686)       GOARCH="386";   echo "server-linux-386" ;;
        riscv64)                   GOARCH="riscv64"; echo "server-linux-riscv64" ;;
        mips64|mips64le)
            if _mips_is_le; then   GOARCH="mips64le"; echo "server-linux-mips64le"; else echo ""; return 1; fi ;;
        mips|mipsel|mipsle)
            if _mips_is_le; then   GOARCH="mipsle"; echo "server-linux-mipsle"; else GOARCH="mips"; echo "server-linux-mips"; fi ;;
        *) echo ""; return 1 ;;
    esac
}

ensure_arch() {
    local a; a=$(detect_arch) || {
        if [ "$_IS_RPC" = 1 ]; then fail unsupported_arch "unsupported arch: $(uname -m)"; else die "Неподдерживаемая архитектура: $(uname -m)"; fi
    }
    case "$a" in
        server-linux-amd64)    GOARCH="amd64" ;;
        server-linux-arm64)    GOARCH="arm64" ;;
        server-linux-arm)      GOARCH="arm" ;;
        server-linux-386)      GOARCH="386" ;;
        server-linux-riscv64)  GOARCH="riscv64" ;;
        server-linux-mips64le) GOARCH="mips64le" ;;
        server-linux-mipsle)   GOARCH="mipsle" ;;
        server-linux-mips)     GOARCH="mips" ;;
    esac
}

detect_virt() {
    local v
    if command -v systemd-detect-virt >/dev/null 2>&1; then
        v=$(systemd-detect-virt 2>/dev/null || true)
        [ -n "$v" ] && { echo "$v"; return 0; }
    fi
    [ -f /.dockerenv ] && { echo docker; return 0; }
    grep -qa 'container=lxc' /proc/1/environ 2>/dev/null && { echo lxc; return 0; }
    echo none
}

wg_kernel_ok() {
    [ -d /sys/module/wireguard ] && return 0
    modprobe wireguard >/dev/null 2>&1
}

has_systemd() {
    command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]
}

conflict_warp() {
    command -v warp-cli >/dev/null 2>&1 && return 0
    ip link show 2>/dev/null | grep -qi 'CloudflareWARP' && return 0
    ls "$WG_DIR"/wgcf*.conf >/dev/null 2>&1 && return 0
    return 1
}

conflict_x3ui() {
    [ -d /etc/x-ui ] || [ -d /usr/local/x-ui ] && return 0
    command -v x-ui >/dev/null 2>&1 && return 0
    has_systemd && systemctl list-unit-files 2>/dev/null | grep -qi '^x-ui' && return 0
    return 1
}

conflict_wgeasy() {
    if command -v docker >/dev/null 2>&1; then
        docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | grep -qi 'wg-easy' && return 0
    fi
    [ -n "${WG_HOST:-}" ] && return 0
    return 1
}

conflict_tailscale() {
    command -v tailscale >/dev/null 2>&1 && return 0
    ip link show 2>/dev/null | grep -qi 'tailscale' && return 0
    return 1
}

other_wg_ifaces_csv() {
    command -v wg >/dev/null 2>&1 || return 0
    local i out="" first=1
    for i in $(wg show interfaces 2>/dev/null || true); do
        [ "$i" = "$WG_IFACE" ] && continue
        [ "$i" = "$AWG_IFACE" ] && continue
        [ "$first" -eq 1 ] && first=0 || out="$out,"
        out="$out\"$(esc "$i")\""
    done
    printf '%s' "$out"
}

port_owner() {
    local proto=$1 port=$2 letter line name
    case "$proto" in tcp) letter=t ;; udp) letter=u ;; *) echo unknown; return 0 ;; esac
    if command -v ss >/dev/null 2>&1; then
        line=$(ss -H -ln"$letter"p 2>/dev/null | awk -v p=":${port}\$" '$4 ~ p {print; exit}' || true)
        [ -z "$line" ] && { echo free; return 0; }
        name=$(printf '%s' "$line" | sed -nE 's/.*users:\(\("([^"]+)".*/\1/p')
        echo "${name:-unknown}"; return 0
    fi
    echo unknown
}

port_pid() {
    local proto=$1 port=$2 letter line
    case "$proto" in tcp) letter=t ;; udp) letter=u ;; *) return 0 ;; esac
    command -v ss >/dev/null 2>&1 || return 0
    line=$(ss -H -ln"$letter"p 2>/dev/null | awk -v p=":${port}\$" '$4 ~ p {print; exit}' || true)
    printf '%s' "$line" | sed -nE 's/.*pid=([0-9]+).*/\1/p'
}

pid_is_ours() {
    local exe; exe=$(readlink "/proc/$1/exe" 2>/dev/null || true)
    case "$exe" in "$PREFIX"/*) return 0 ;; esac
    return 1
}

get_public_ip() {
    local ip="" url
    for url in "https://api.ipify.org" "https://icanhazip.com" "https://ifconfig.me/ip" "https://ident.me"; do
        ip="$(curl -4 -fsSL --connect-timeout 3 --max-time 5 "$url" 2>/dev/null | tr -d ' \r\n' || true)"
        if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
            echo "$ip"; return 0
        fi
    done
    echo "IP_СЕРВЕРА"
}

detect_wg_port() {
    WG_PORT=""
    if [ -f "$AWG_CONF" ]; then
        WG_PORT="$(sed -n 's/^[[:space:]]*ListenPort[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
        [ -n "$WG_PORT" ] && return 0
    fi
    if [ -f "$WG_CONF" ]; then
        WG_PORT="$(sed -n 's/^[[:space:]]*ListenPort[[:space:]]*=[[:space:]]*//Ip' "$WG_CONF" | head -n1 | tr -d ' \r')"
        [ -n "$WG_PORT" ] && return 0
    fi
    command -v wg >/dev/null 2>&1 || return 0
    if wg show all listen-port >/dev/null 2>&1; then
        WG_PORT="$(wg show all listen-port 2>/dev/null | head -n1 | awk '{print $2}' || true)"
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 40-state.sh
# ─────────────────────────────────────────────────────────────────────────────
# Сохранение, загрузка и валидация конфигурации сервера (install.conf и state).

state_get() {
    [ -f "$STATE_FILE" ] || return 0
    sed -n "s/^$1=//p" "$STATE_FILE" 2>/dev/null | head -n1
}

state_set() {
    local k=$1 v=$2 tmp
    [ -d "$PREFIX" ] || mkdir -p "$PREFIX" 2>/dev/null || return 0
    [ -w "$PREFIX" ] || return 0
    tmp=$(mktemp "$STATE_FILE.XXXXXX" 2>/dev/null) || return 0
    if [ -f "$STATE_FILE" ]; then
        grep -v "^$k=" "$STATE_FILE" > "$tmp" 2>/dev/null || true
    fi
    printf '%s=%s\n' "$k" "$v" >> "$tmp"
    chmod 0600 "$tmp"
    mv -f "$tmp" "$STATE_FILE"
}

is_installed() {
    [ -f "$CONF_FILE" ] || [ -f "$COMPOSE_FILE" ] || [ -f "$UNIT_FILE" ] || [ -f "$AWG_CONF" ]
}

# shellcheck disable=SC1090
load_config() {
    [ -f "$CONF_FILE" ] && . "$CONF_FILE" || true
    if [ -n "${AWG_SETUP:-}" ]; then INSTALL_AWG="$AWG_SETUP"; fi
    if [ -n "${WG_SETUP:-}" ]; then INSTALL_WG="$WG_SETUP"; fi
    return 0
}

save_config() {
    mkdir -p "$APP_DIR" 2>/dev/null || true
    cat > "$CONF_FILE" <<EOF
INSTALL_METHOD="$INSTALL_METHOD"
INSTALL_FREETURN="$INSTALL_FREETURN"
INSTALL_AWG="$INSTALL_AWG"
INSTALL_WG="$INSTALL_WG"
VERSION="$VERSION"
PROVIDER="$PROVIDER"
PROXY_MODE="$PROXY_MODE"
BACKEND_TYPE="$BACKEND_TYPE"
BACKEND_PORT="$BACKEND_PORT"
LISTEN_PORT="$LISTEN_PORT"
AWG_DIRECT_PORT="$AWG_DIRECT_PORT"
OBF_PROFILE="$OBF_PROFILE"
OBF_KEY="$OBF_KEY"
CLIENTS_FILE_CONF="$CLIENTS_FILE_CONF"
WG_ENDPOINT="$WG_ENDPOINT"
AWG_JC="$AWG_JC"
AWG_JMIN="$AWG_JMIN"
AWG_JMAX="$AWG_JMAX"
AWG_S1="$AWG_S1"
AWG_S2="$AWG_S2"
AWG_S3="$AWG_S3"
AWG_S4="$AWG_S4"
AWG_H1="$AWG_H1"
AWG_H2="$AWG_H2"
AWG_H3="$AWG_H3"
AWG_H4="$AWG_H4"
AWG_HPK="$AWG_HPK"
EOF
    chmod 0600 "$CONF_FILE"
}

apply_overrides() {
    local kv k v
    for kv in "${OVERRIDES[@]+"${OVERRIDES[@]}"}"; do
        k="${kv%%=*}"; v="${kv#*=}"; printf -v "$k" '%s' "$v"
    done
}

connect_addr() { echo "127.0.0.1:${BACKEND_PORT}"; }

validate_config() {
    [ "$INSTALL_FREETURN" != "1" ] && [ "$INSTALL_AWG" != "1" ] && [ "$INSTALL_WG" != "1" ] \
        && die "Не выбран ни один компонент для установки (FreeTurn или AmneziaWG)."

    if [ "$INSTALL_FREETURN" = "1" ]; then
        case "$INSTALL_METHOD" in docker | systemd) ;; *) die "method: docker|systemd, а не '$INSTALL_METHOD'" ;; esac
        case "$PROVIDER"       in vk) ;; *) die "provider: vk, а не '$PROVIDER'" ;; esac
        case "$PROXY_MODE"     in udp | tcp) ;; *) die "mode: udp|tcp, а не '$PROXY_MODE'" ;; esac
        valid_port "$LISTEN_PORT" || die "listen-port невалиден: '$LISTEN_PORT'"
        case "$OBF_PROFILE" in
            rtpopus3 | rtpopus2 | rtpopus)
                [ -z "$OBF_KEY" ] && { OBF_KEY="$(openssl rand -hex 32)"; log_info "Сгенерирован ключ обфускации."; }
                valid_hex64 "$OBF_KEY" || die "obf-key должен состоять ровно из 64 hex-символов" ;;
            none) OBF_KEY="" ;;
            *) die "obf: rtpopus3|rtpopus2|rtpopus|none, а не '$OBF_PROFILE'" ;;
        esac
    fi

    if [ "$INSTALL_AWG" = "1" ] || [ "$INSTALL_WG" = "1" ]; then
        valid_port "$BACKEND_PORT" || die "backend-port невалиден: '$BACKEND_PORT'"
        valid_endpoint "$WG_ENDPOINT" || die "wg-endpoint невалиден (host:port): '$WG_ENDPOINT'"
    fi
    return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 50-download.sh
# ─────────────────────────────────────────────────────────────────────────────
# Загрузка бинарников и проверка целостности.

_dl() {
    local url=$1 out=$2
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --connect-timeout 15 --max-time 300 -o "$out" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -q --timeout=300 -O "$out" "$url"
    else
        fail download_failed "neither curl nor wget present"
    fi
}

_resolve_version() {
    local url=$1 loc=""
    if command -v curl >/dev/null 2>&1; then
        loc=$(curl -sI "$url" 2>/dev/null | awk -F': ' 'tolower($1)=="location"{print $2}' | tr -d '\r' | head -n1)
    elif command -v wget >/dev/null 2>&1; then
        loc=$(wget --spider --server-response "$url" 2>&1 | awk '/[Ll]ocation:/{print $2}' | tr -d '\r' | head -n1)
    fi
    printf '%s' "$loc" | sed -nE 's#.*/releases/download/([^/]+)/.*#\1#p'
}

gh_latest_version() {
    curl -s --max-time 10 "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | jq -r '.tag_name // empty' 2>/dev/null || true
}

gh_recent_versions() {
    curl -s --max-time 10 "https://api.github.com/repos/${REPO}/releases?per_page=6" 2>/dev/null | jq -r '.[].tag_name' 2>/dev/null || true
}

resolve_asset_url() {
    local ver=$1 asset=$2 api
    [ "$ver" = "latest" ] && api="https://api.github.com/repos/${REPO}/releases/latest" \
                          || api="https://api.github.com/repos/${REPO}/releases/tags/$ver"
    curl -s --max-time 15 "$api" 2>/dev/null | jq -r --arg n "$asset" '.assets[] | select(.name==$n) | .browser_download_url' 2>/dev/null || true
}

image_tag() {
    [ "$VERSION" = "latest" ] && echo "latest" || echo "$VERSION"
}

_verify_download() {
    local f=$1 want=${2:-} size magic got
    size=$(wc -c < "$f" 2>/dev/null || echo 0)
    [ "$size" -lt 100000 ] && { rm -f "$f"; fail too_small "Файл слишком мал ($size байт)"; }

    if command -v od >/dev/null 2>&1; then
        magic=$(od -An -tx1 -N4 "$f" 2>/dev/null | tr -d ' \n')
        if [ -n "$magic" ] && [ "$magic" != "7f454c46" ]; then
            rm -f "$f"
            fail not_elf "Файл не является бинарником ELF (magic: $magic)"
        fi
    fi

    if [ -n "$want" ] && command -v sha256sum >/dev/null 2>&1; then
        got=$(sha256sum "$f" | awk '{print $1}')
        if [ -n "$got" ] && [ "$got" != "$want" ]; then
            rm -f "$f"
            fail sha_mismatch "Не совпадает sha256: ожидался $want, получен $got"
        fi
    fi
}

download_binary() {
    ensure_arch
    local asset="server-linux-${GOARCH}"
    local url tmp
    url="$(resolve_asset_url "$VERSION" "$asset" || true)"
    [ -z "$url" ] && url="${BASE_URL}/${asset}"

    tmp=$(mktemp "${APP_DIR}/server.new.XXXXXX" 2>/dev/null) || tmp="${APP_DIR}/server.new.$$"
    if [ "$_IS_RPC" = 1 ]; then
        _dl "$url" "$tmp" || fail download_failed "Не удалось скачать $url"
    else
        ui_spin "Скачивание ${asset} (${VERSION})" _dl "$url" "$tmp" || die "Не удалось скачать $url"
    fi
    _verify_download "$tmp" "${ARG_SHA256:-}"
    chmod 0755 "$tmp"
    with_lock
    [ -f "${APP_DIR}/server" ] && cp -f "${APP_DIR}/server" "${APP_DIR}/server.bak" 2>/dev/null || true
    mv -f "$tmp" "${APP_DIR}/server"
    cp -f "${APP_DIR}/server" "${APP_DIR}/${asset}" 2>/dev/null || true
    echo "$VERSION" > "$VERFILE"
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 60-backend.sh
# ─────────────────────────────────────────────────────────────────────────────
# VPN-бэкенды: AmneziaWG (AWG 3.1) и классический WireGuard (ft-wg0).

init_awg_params() {
    if [ -f "$AWG_CONF" ]; then
        AWG_JC="$(sed -n 's/^[[:space:]]*Jc[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
        AWG_HPK="$(sed -n 's/^[[:space:]]*HeaderProtectionKey[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
        if [ -n "$AWG_JC" ] && [ -n "$AWG_HPK" ]; then
            AWG_JMIN="$(sed -n 's/^[[:space:]]*Jmin[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_JMAX="$(sed -n 's/^[[:space:]]*Jmax[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_S1="$(sed -n 's/^[[:space:]]*S1[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_S2="$(sed -n 's/^[[:space:]]*S2[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_S3="$(sed -n 's/^[[:space:]]*S3[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_S4="$(sed -n 's/^[[:space:]]*S4[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_H1="$(sed -n 's/^[[:space:]]*H1[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_H2="$(sed -n 's/^[[:space:]]*H2[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_H3="$(sed -n 's/^[[:space:]]*H3[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            AWG_H4="$(sed -n 's/^[[:space:]]*H4[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" | head -n1 | tr -d ' \r')"
            return 0
        fi
    fi

    AWG_JC=$((RANDOM % 3 + 4))
    AWG_JMIN=10
    AWG_JMAX=50

    local s1 s2 s3 s4=12
    s1=$((RANDOM % 136 + 15))
    while :; do
        s2=$((RANDOM % 136 + 15))
        [ "$s2" -ne "$s1" ] && [ "$s2" -ne "$s4" ] && [ $((s1 + 148)) -ne $((s2 + 92)) ] && break
    done
    while :; do
        s3=$((RANDOM % 50 + 15))
        [ "$s3" -ne "$s1" ] && [ "$s3" -ne "$s2" ] && [ "$s3" -ne "$s4" ] && \
        [ $((s1 + 148)) -ne $((s3 + 64)) ] && [ $((s2 + 92)) -ne $((s3 + 64)) ] && break
    done

    AWG_S1="$s1"
    AWG_S2="$s2"
    AWG_S3="$s3"
    AWG_S4="$s4"
    AWG_H1=1
    AWG_H2=2
    AWG_H3=3
    AWG_H4=4
    AWG_HPK="$(openssl rand 32 2>/dev/null | base64 | tr -d '\r\n')"
}

ensure_awg_image() {
    [ "$INSTALL_AWG" = "1" ] || return 0
    command -v docker >/dev/null 2>&1 || return 0
    if docker image inspect "${AWG_IMAGE}" >/dev/null 2>&1; then
        return 0
    fi
    if docker pull "${AWG_IMAGE}" >/dev/null 2>&1; then
        return 0
    fi

    local script_dir repo_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo "")"
    repo_dir="$(cd "$script_dir/.." 2>/dev/null && pwd || echo "")"
    if [ -f "$repo_dir/docker/awg/Dockerfile" ]; then
        if [ "$_IS_RPC" = 1 ]; then
            docker build -t "${AWG_IMAGE}" -f "$repo_dir/docker/awg/Dockerfile" "$repo_dir" >/dev/null 2>&1 || true
        else
            ui_spin "Сборка Docker-образа AmneziaWG" docker build -t "${AWG_IMAGE}" -f "$repo_dir/docker/awg/Dockerfile" "$repo_dir" || true
        fi
        if docker image inspect "${AWG_IMAGE}" >/dev/null 2>&1; then
            return 0
        fi
    fi

    local bdir raw_url
    bdir="$(mktemp -d)"
    raw_url="https://raw.githubusercontent.com/${REPO}/master"
    if curl -fsSL "$raw_url/docker/awg/Dockerfile" -o "$bdir/Dockerfile" 2>/dev/null && \
       mkdir -p "$bdir/docker/awg" && \
       curl -fsSL "$raw_url/docker/awg/start.sh" -o "$bdir/docker/awg/start.sh" 2>/dev/null; then
        if [ "$_IS_RPC" = 1 ]; then
            docker build -t "${AWG_IMAGE}" "$bdir" >/dev/null 2>&1 || true
        else
            ui_spin "Сборка Docker-образа AmneziaWG" docker build -t "${AWG_IMAGE}" "$bdir" || true
        fi
    fi
    rm -rf "$bdir"

    docker image inspect "${AWG_IMAGE}" >/dev/null 2>&1 || die "Не удалось получить или собрать образ ${AWG_IMAGE}."
}

generate_awg_keypair() {
    local priv="" pub=""
    if command -v awg >/dev/null 2>&1; then
        priv="$(awg genkey 2>/dev/null || true)"
        [ -n "$priv" ] && pub="$(awg pubkey <<< "$priv" 2>/dev/null || true)"
    elif [ "$(docker inspect -f '{{.State.Running}}' "$AWG_CONTAINER" 2>/dev/null || echo false)" = "true" ]; then
        priv="$(docker exec "$AWG_CONTAINER" awg genkey 2>/dev/null || true)"
        [ -n "$priv" ] && pub="$(docker exec -i "$AWG_CONTAINER" awg pubkey <<< "$priv" 2>/dev/null || true)"
    elif command -v docker >/dev/null 2>&1; then
        ensure_awg_image
        priv="$(docker run --rm "${AWG_IMAGE}" awg genkey 2>/dev/null || true)"
        [ -n "$priv" ] && pub="$(docker run --rm -i "${AWG_IMAGE}" awg pubkey <<< "$priv" 2>/dev/null || true)"
    elif command -v wg >/dev/null 2>&1; then
        priv="$(wg genkey 2>/dev/null || true)"
        [ -n "$priv" ] && pub="$(wg pubkey <<< "$priv" 2>/dev/null || true)"
    fi
    [ -z "$priv" ] || [ -z "$pub" ] && die "Не удалось сгенерировать ключи VPN."
    echo "$priv $pub"
}

awg_present() { [ -f "$AWG_CONF" ]; }

awg_port() {
    [ -f "$AWG_CONF" ] || return 0
    sed -n 's/^[[:space:]]*ListenPort[[:space:]]*=[[:space:]]*//Ip' "$AWG_CONF" 2>/dev/null \
        | head -n1 | sed 's/[#;].*//' | tr -d ' \r'
}

wg_present() { [ -f "$WG_CONF" ] || [ -f "$WG_DIR/wg0.conf" ]; }

wg_is_ours() {
    [ -f "$WG_CONF" ] || return 1
    grep -qF "$WG_MARKER" "$WG_CONF" 2>/dev/null
}

wg_port() {
    local p=""
    if command -v wg >/dev/null 2>&1; then
        p=$(wg show "$WG_IFACE" listen-port 2>/dev/null | tr -d ' \r' || true)
        case "$p" in ''|0) p="" ;; esac
        if [ -n "$p" ]; then echo "$p"; return 0; fi
    fi
    if [ -f "$WG_CONF" ]; then
        p=$(sed -n 's/^[[:space:]]*ListenPort[[:space:]]*=[[:space:]]*//Ip' "$WG_CONF" 2>/dev/null \
            | head -n1 | sed 's/[#;].*//' | tr -d ' \r')
        case "$p" in ''|*[!0-9]*) p="" ;; esac
        if [ -n "$p" ]; then echo "$p"; return 0; fi
    fi
}

alloc_client_ip() {
    local conf=$1 net_prefix=$2 max_ip=1 ips num
    if [ -f "$conf" ]; then
        ips=$(grep -oE "${net_prefix}\.[0-9]+" "$conf" 2>/dev/null | awk -F. '{print $4}' | sort -n || true)
        for num in $ips; do
            [ "$num" -gt "$max_ip" ] && max_ip="$num"
        done
    fi
    echo "${net_prefix}.$((max_ip + 1))"
}

enable_ip_forwarding() {
    [ -d /etc/sysctl.d ] || mkdir -p /etc/sysctl.d 2>/dev/null || true
    echo 'net.ipv4.ip_forward = 1' > /etc/sysctl.d/99-free-turn-proxy.conf 2>/dev/null || true
    sysctl -q -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
}

awg_bootstrap() {
    [ "$INSTALL_AWG" = "1" ] || return 0
    enable_ip_forwarding
    mkdir -p "$AWG_DIR" "$CLIENTS_DIR"
    init_awg_params
    [ -f "$AWG_CONF" ] && return 0

    log_info "Инициализация AmneziaWG (AWG 3.1)..."
    local kp priv pub
    kp="$(generate_awg_keypair)"
    priv="${kp%% *}"; pub="${kp##* }"

    echo "$priv" > "${AWG_DIR}/server.key"; chmod 0600 "${AWG_DIR}/server.key"
    echo "$pub"  > "${AWG_DIR}/server.pub"

    ( umask 077
      cat > "$AWG_CONF" <<EOF
[Interface]
Address = ${AWG_NET}.1/24
ListenPort = ${BACKEND_PORT}
PrivateKey = ${priv}
Jc = ${AWG_JC}
Jmin = ${AWG_JMIN}
Jmax = ${AWG_JMAX}
S1 = ${AWG_S1}
S2 = ${AWG_S2}
S3 = ${AWG_S3}
S4 = ${AWG_S4}
H1 = ${AWG_H1}
H2 = ${AWG_H2}
H3 = ${AWG_H3}
H4 = ${AWG_H4}
HeaderProtectionKey = ${AWG_HPK}
ContentPaddingAddition = 10
RekeyAfterTime = 110
RekeyTimeout = 5
RejectAfterTime = 160
KeepaliveTimeout = 10
MaxHandshakeAttempts = 15
RandomTrailers = on
DisableCookies = on
EOF
    )
    log_success "Конфигурация AmneziaWG создана (${AWG_CONF})."
}

awg_reconcile() {
    if [ "$(docker inspect -f '{{.State.Running}}' "$AWG_CONTAINER" 2>/dev/null || echo false)" = "true" ]; then
        docker exec "$AWG_CONTAINER" ft-awg-start sync >/dev/null 2>&1 || true
    elif command -v awg >/dev/null 2>&1; then
        awg syncconf "$AWG_IFACE" <(awg-quick strip "$AWG_IFACE" 2>/dev/null) 2>/dev/null || true
    fi
}

wireguard_bootstrap() {
    [ "$INSTALL_WG" = "1" ] || return 0
    enable_ip_forwarding
    mkdir -p "$WG_DIR" "$CLIENTS_DIR"
    [ -f "$WG_CONF" ] && return 0

    local kp priv pub
    kp="$(generate_awg_keypair)"
    priv="${kp%% *}"; pub="${kp##* }"

    echo "$priv" > "${WG_DIR}/server.key"; chmod 0600 "${WG_DIR}/server.key"
    echo "$pub"  > "${WG_DIR}/server.pub"

    ( umask 077
      cat > "$WG_CONF" <<EOF
[Interface]
$WG_MARKER
Address = ${WG_NET}.1/24
ListenPort = ${BACKEND_PORT}
MTU = ${WG_MTU}
PrivateKey = ${priv}
EOF
    )
}

wg_reconcile() {
    command -v wg >/dev/null 2>&1 || return 0
    ip link show "$WG_IFACE" >/dev/null 2>&1 || return 0
    wg syncconf "$WG_IFACE" <(wg-quick strip "$WG_IFACE" 2>/dev/null) 2>/dev/null || true
}

_pub_fs() { printf '%s' "$1" | tr '/+' '_-' | tr -d '='; }

generate_freeturn_uri() {
    local peer="$1" mode="$2" obf="$3" key="$4" cid="${5:-}" name="${6:-}" wg_conf="${7:-}"
    local json b64
    json="{\"v\":1,\"provider\":\"$(esc "${PROVIDER:-vk}")\",\"peer\":\"$(esc "$peer")\""
    if [ -n "$mode" ] && [ "$mode" != "udp" ]; then
        json="$json,\"mode\":\"$(esc "$mode")\""
    fi
    if [ -n "$obf" ] && [ "$obf" != "none" ]; then
        json="$json,\"obf\":\"$(esc "$obf")\",\"key\":\"$(esc "$key")\""
    fi
    local n="${STREAMS:-12}" spc="${STREAMS_PER_CRED:-12}"
    json="$json,\"n\":${n},\"spc\":${spc}"
    if [ -n "$cid" ]; then
        json="$json,\"cid\":\"$(esc "$cid")\""
    fi
    if [ -n "$name" ]; then
        json="$json,\"name\":\"$(esc "$name")\""
    fi
    if [ -n "$wg_conf" ]; then
        json="$json,\"wg\":\"$(esc "$wg_conf")\""
    fi
    json="$json}"
    b64=$(printf '%s' "$json" | openssl base64 -A 2>/dev/null || printf '%s' "$json" | base64 | tr -d '\r\n')
    b64=$(printf '%s' "$b64" | tr '+/' '-_' | tr -d '=')
    echo "freeturn://${b64}"
}

peers_json() {
    local target_conf="" iface=""
    if [ -f "$AWG_CONF" ]; then target_conf="$AWG_CONF"; iface="$AWG_IFACE"
    elif wg_present && wg_is_ours; then target_conf="$WG_CONF"; iface="$WG_IFACE"
    else printf '[]'; return 0; fi

    local hs="" out="" first=1 in_peer=0 pub="" name="" ip="" raw line val
    command -v wg >/dev/null 2>&1 && hs=$(wg show "$iface" latest-handshakes 2>/dev/null || true)

    flush_peer() {
        if [ "$in_peer" = 1 ] && [ -n "$pub" ]; then
            local h conf_yes el
            h=$(printf '%s\n' "$hs" | awk -v p="$pub" '$1==p{print $2}')
            [ -f "$SHARE_DIR/$(_pub_fs "$pub").conf" ] || [ -f "$CLIENTS_DIR/$(_pub_fs "$pub").conf" ] \
                && conf_yes=true || conf_yes=false
            el="{\"pub\":\"$(esc "$pub")\""
            [ -n "$name" ] && el="$el,\"name_b64\":\"$(esc "$name")\""
            [ -n "$ip" ] && el="$el,\"ip\":\"$(esc "$ip")\""
            [ -n "$h" ] && [ "$h" != 0 ] && el="$el,\"hs\":$h"
            el="$el,\"has_conf\":$conf_yes}"
            [ "$first" = 1 ] && first=0 || out="$out,"
            out="$out$el"
        fi
        pub=""; name=""; ip=""
    }

    while IFS= read -r raw || [ -n "$raw" ]; do
        line=${raw//$'\r'/}; line=${line#"${line%%[![:space:]]*}"}; line=${line%"${line##*[![:space:]]}"}
        case "$line" in
            "["*) flush_peer; case "$line" in \[[Pp]eer\]) in_peer=1 ;; *) in_peer=0 ;; esac ;;
            "# ft-user: "*) [ "$in_peer" = 1 ] && name="${line#\# ft-user: }" ;;
            "# client: "*)  [ "$in_peer" = 1 ] && name=$(printf '%s' "${line#\# client: }" | base64 | tr -d '\r\n') ;;
            PublicKey*=*)   [ "$in_peer" = 1 ] && { val=${line#*=}; pub=${val// /}; } ;;
            AllowedIPs*=*)  [ "$in_peer" = 1 ] && [ -z "$ip" ] && { val=${line#*=}; val=${val%%,*}; val=${val%%/*}; ip=${val// /}; } ;;
        esac
    done < "$target_conf"
    flush_peer
    printf '[%s]' "$out"
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 70-runtime.sh
# ─────────────────────────────────────────────────────────────────────────────
# Управление рантаймами (Docker Compose / Systemd), процессами и файрволом.

current_runtime() {
    local r; r=$(state_get runtime)
    [ -n "$r" ] && { echo "$r"; return 0; }
    [ -f "$COMPOSE_FILE" ] || [ "$INSTALL_METHOD" = "docker" ] && { echo "docker"; return 0; }
    has_systemd && echo "systemd" || echo "nohup"
}

_running_nohup() {
    if [ -f "$PIDFILE" ]; then
        local pid; pid=$(cat "$PIDFILE" 2>/dev/null || echo "")
        [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && return 0
    fi
    pgrep -f "^$PREFIX/(server|server-linux-)" >/dev/null 2>&1
}

rt_running() {
    case "$(current_runtime)" in
        docker)
            if [ "$INSTALL_FREETURN" = "1" ]; then
                [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)" = "true" ]
            elif [ "$INSTALL_AWG" = "1" ]; then
                [ "$(docker inspect -f '{{.State.Running}}' "$AWG_CONTAINER" 2>/dev/null || echo false)" = "true" ]
            else
                return 1
            fi ;;
        systemd)
            systemctl is-active --quiet "$UNIT_NAME" ;;
        nohup)
            _running_nohup ;;
        *)  return 1 ;;
    esac
}

rt_pid() {
    case "$(current_runtime)" in
        docker)
            docker inspect -f '{{.State.Pid}}' "$CONTAINER" 2>/dev/null || echo 0 ;;
        systemd)
            local p; p=$(systemctl show -p MainPID --value "$UNIT_NAME" 2>/dev/null || echo 0)
            [ -n "$p" ] && [ "$p" != "0" ] && echo "$p" || true ;;
        nohup)
            [ -f "$PIDFILE" ] && cat "$PIDFILE" 2>/dev/null || true ;;
    esac
}

current_cmdline() {
    local p; p=$(rt_pid)
    [ -n "$p" ] && [ -r "/proc/$p/cmdline" ] && tr '\0' ' ' < "/proc/$p/cmdline" || true
}

_wait_running() {
    local i=0
    while [ "$i" -lt 5 ]; do
        rt_running >/dev/null 2>&1 && return 0
        sleep 1
        i=$((i + 1))
    done
    return 1
}

_install_systemd_unit() {
    local launcher_content unit_content need_reload=0
    launcher_content=$(cat <<'LAUNCH_EOF'
#!/bin/bash
set -e
PREFIX="/opt/free-turn-proxy"
[ -f "$PREFIX/run.args" ] || { echo "run.args missing" >&2; exit 1; }
a=()
while IFS= read -r line || [ -n "$line" ]; do a+=("$line"); done < "$PREFIX/run.args"
if [ -x "$PREFIX/server" ]; then exec "$PREFIX/server" "${a[@]}"; fi
arch=server-linux-amd64
case "$(uname -m 2>/dev/null || echo "")" in
    x86_64|amd64) arch=server-linux-amd64 ;;
    aarch64|arm64) arch=server-linux-arm64 ;;
    armv7l|armv6l|armv5*|arm) arch=server-linux-arm ;;
    i386|i486|i586|i686) arch=server-linux-386 ;;
    riscv64) arch=server-linux-riscv64 ;;
esac
exec "$PREFIX/$arch" "${a[@]}"
LAUNCH_EOF
)
    if [ ! -f "$LAUNCHER" ] || [ "$(cat "$LAUNCHER" 2>/dev/null)" != "$launcher_content" ]; then
        printf '%s\n' "$launcher_content" > "$LAUNCHER"
        chmod 0755 "$LAUNCHER"
    fi

    unit_content=$(cat <<UNIT_EOF
[Unit]
Description=free-turn-proxy server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-$ENVFILE
ExecStart=$LAUNCHER
Restart=on-failure
RestartSec=2
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT_EOF
)
    if [ ! -f "$UNIT_PATH" ] || [ "$(cat "$UNIT_PATH" 2>/dev/null)" != "$unit_content" ]; then
        printf '%s\n' "$unit_content" > "$UNIT_PATH"
        chmod 0644 "$UNIT_PATH"
        need_reload=1
    fi
    [ "$need_reload" = "1" ] && systemctl daemon-reload
    systemctl enable "$UNIT_NAME" >/dev/null 2>&1 || true
}

_write_args_file() {
    local tmp="$ARGSFILE.tmp"
    ( umask 077; : > "$tmp" )
    {
        echo "-listen";  echo "${ARG_LISTEN:-0.0.0.0:${LISTEN_PORT}}"
        echo "-connect"; echo "${ARG_CONNECT:-$(connect_addr)}"
        if [ "${ARG_MODE:-$PROXY_MODE}" = "tcp" ]; then
            echo "-mode"; echo "tcp"
            for tok in ${ARG_KCP[@]+"${ARG_KCP[@]}"}; do echo "$tok"; done
        else
            echo "-mode"; echo "udp"
        fi
        local prof="${ARG_OBF_PROFILE:-$OBF_PROFILE}"
        local key="${ARG_OBF_KEY:-$OBF_KEY}"
        if [ "$prof" != "none" ] && [ -z "$key" ]; then
            key="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | xxd -p -c 32 2>/dev/null || true)"
            OBF_KEY="$key"
        fi
        if [ "$prof" != "none" ] && [ -n "$key" ]; then
            echo "-obf-profile"; echo "$prof"
            echo "-obf-key";     echo "$key"
            [ -n "${ARG_OBF_TIMING:-}" ] && { echo "-obf-timing"; echo "$ARG_OBF_TIMING"; }
        fi
        if [ -n "${ARG_CLIENT_ID:-}" ] || [ -n "${CLIENTS_FILE_CONF:-}" ]; then
            echo "-clients-file"; echo "${CLIENTS_FILE_CONF:-$CLIENTSFILE}"
        fi
    } >> "$tmp"
    chmod 0600 "$tmp"
    mv -f "$tmp" "$ARGSFILE"
}

_write_env_file() {
    local tmp="$ENVFILE.tmp"
    ( umask 077; : > "$tmp" )
    chmod 0600 "$tmp"
    mv -f "$tmp" "$ENVFILE"
}

_stop_docker() {
    if [ -f "$COMPOSE_FILE" ] && command -v docker >/dev/null 2>&1; then
        ( cd "$APP_DIR" && compose_cmd stop ) >/dev/null 2>&1 || true
    fi
    docker stop "$CONTAINER" "$AWG_CONTAINER" >/dev/null 2>&1 || true
}

_stop_systemd() {
    systemctl stop "$UNIT_NAME" 2>/dev/null || true
    rm -f "$ENVFILE"
}

_stop_nohup() {
    if [ -f "$PIDFILE" ]; then
        local pid; pid=$(cat "$PIDFILE" 2>/dev/null || echo "")
        [ -n "$pid" ] && { kill "$pid" 2>/dev/null || true; sleep 1; kill -9 "$pid" 2>/dev/null || true; }
        rm -f "$PIDFILE"
    fi
    pkill -9 -f "^$PREFIX/(server|server-linux-)" 2>/dev/null || true
    rm -f "$ENVFILE"
}

rt_stop() {
    case "$(current_runtime)" in
        docker)  _stop_docker ;;
        systemd) _stop_systemd ;;
        nohup)   _stop_nohup ;;
    esac
}

init_clients_file() {
    local cfile="${CLIENTS_FILE_CONF:-$CLIENTSFILE}"
    [ -z "$cfile" ] && return 0
    mkdir -p "$(dirname "$cfile")" 2>/dev/null || true
    if [ ! -f "$cfile" ]; then
        echo '{"clients":{}}' > "$cfile"
        chmod 0600 "$cfile"
    fi
}

healthcheck_docker() {
    sleep 2
    if [ "$INSTALL_FREETURN" = "1" ]; then
        [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)" = "true" ] \
            && log_success "Контейнер FreeTurn работает." \
            || {
                log_warn "Логи ${CONTAINER}:"
                docker logs --tail 30 "$CONTAINER" 2>&1 || true
                die "Контейнер FreeTurn не запустился."
            }
    fi
    if [ "$INSTALL_AWG" = "1" ]; then
        [ "$(docker inspect -f '{{.State.Running}}' "$AWG_CONTAINER" 2>/dev/null || echo false)" = "true" ] \
            && log_success "Контейнер AmneziaWG работает." \
            || {
                log_warn "Логи ${AWG_CONTAINER}:"
                docker logs --tail 30 "$AWG_CONTAINER" 2>&1 || true
                die "Контейнер AmneziaWG не запустился."
            }
    fi
}

apply_docker() {
    state_set runtime docker
    ensure_docker
    init_clients_file
    mkdir -p "$APP_DIR"

    local mode="${ARG_MODE:-$PROXY_MODE}"
    local prof="${ARG_OBF_PROFILE:-$OBF_PROFILE}"
    local key="${ARG_OBF_KEY:-$OBF_KEY}"
    local listen_val="${ARG_LISTEN:-0.0.0.0:${LISTEN_PORT}}"
    local connect_val="${ARG_CONNECT:-$(connect_addr)}"
    if [ "$prof" != "none" ] && [ -z "$key" ]; then
        key="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | xxd -p -c 32 2>/dev/null || true)"
        OBF_KEY="$key"
    fi

    {
        echo "services:"
        if [ "$INSTALL_FREETURN" = "1" ]; then
            echo "  free-turn-proxy:"
            echo "    image: ${IMAGE}:$(image_tag)"
            echo "    container_name: ${CONTAINER}"
            echo "    network_mode: \"host\""
            echo "    restart: unless-stopped"
            echo "    environment:"
            echo "      - CONNECT_ADDR=${connect_val}"
            echo "      - LISTEN_ADDR=${listen_val}"
            echo "      - MODE=${mode}"
            echo "      - OBF_PROFILE=${prof}"
            [ "$prof" != "none" ] && [ -n "$key" ] && echo "      - OBF_KEY=${key}"
            [ -n "${ARG_OBF_TIMING:-}" ] && echo "      - OBF_TIMING=${ARG_OBF_TIMING}"
            if [ -n "$CLIENTS_FILE_CONF" ]; then
                local cdir; cdir="$(dirname "$CLIENTS_FILE_CONF")"
                echo "      - CLIENTS_FILE=${CLIENTS_FILE_CONF}"
                echo "    volumes:"
                echo "      - ${cdir}:${cdir}"
            fi
        fi
        if [ "$INSTALL_AWG" = "1" ]; then
            [ "$INSTALL_FREETURN" = "1" ] && echo ""
            echo "  freeturn-awg:"
            echo "    image: ${AWG_IMAGE}"
            echo "    container_name: ${AWG_CONTAINER}"
            echo "    network_mode: \"host\""
            echo "    environment:"
            echo "      - AWG_IFACE=${AWG_IFACE}"
            echo "      - AWG_CONF=/etc/awg/${AWG_IFACE}.conf"
            echo "      - AWG_LOG_LEVEL=verbose"
            echo "    cap_add:"
            echo "      - NET_ADMIN"
            echo "    devices:"
            echo "      - /dev/net/tun"
            echo "    restart: unless-stopped"
            echo "    volumes:"
            echo "      - ${AWG_CONF}:/etc/awg/${AWG_IFACE}.conf:ro"
        fi
    } > "$COMPOSE_FILE"
    chmod 0600 "$COMPOSE_FILE"

    if [ "$INSTALL_FREETURN" = "1" ]; then
        if [ "$_IS_RPC" = 1 ]; then
            docker pull "${IMAGE}:$(image_tag)" >/dev/null 2>&1 || true
        else
            ui_spin "Загрузка Docker-образа FreeTurn" docker pull "${IMAGE}:$(image_tag)" || true
        fi
    fi

    if [ "$INSTALL_AWG" = "1" ]; then
        ensure_awg_image
    fi

    if [ "$_IS_RPC" = 1 ]; then
        ( cd "$APP_DIR" && compose_cmd up -d >/dev/null 2>&1 ) \
            || fail compose_up_failed "docker compose up failed"
    else
        ( cd "$APP_DIR" && ui_spin "Запуск служб" compose_cmd up -d ) || die "docker compose up не удался."
    fi
    healthcheck_docker
}

healthcheck_systemd() {
    sleep 1
    systemctl is-active --quiet "$SERVICE" && { log_success "Служба ${SERVICE} активна."; return 0; }
    log_warn "Служба не активна. Логи:"
    journalctl -u "$SERVICE" --no-pager -n 40 2>&1 || true
    die "$SERVICE не запустилась."
}

apply_systemd() {
    state_set runtime systemd
    if [ "$INSTALL_FREETURN" = "1" ]; then
        download_binary
        init_clients_file
        _write_args_file
        _write_env_file
        _install_systemd_unit
        systemctl restart "$UNIT_NAME" || fail start_failed "systemctl restart failed"
        healthcheck_systemd
    else
        systemctl disable --now "$UNIT_NAME" 2>/dev/null || true
    fi
}

rt_start() {
    local bin=${1:-}
    case "$(current_runtime)" in
        docker)  apply_docker ;;
        systemd)
            _install_systemd_unit
            _write_args_file
            _write_env_file
            systemctl restart "$UNIT_NAME" || fail start_failed "systemctl restart failed"
            _wait_running || fail start_failed "server failed to start; journalctl -u $UNIT_NAME" ;;
        nohup)
            [ -z "$bin" ] && bin="$PREFIX/server"
            [ -f "$PIDFILE" ] && _stop_nohup
            _write_env_file
            local args=(-listen "${ARG_LISTEN:-0.0.0.0:${LISTEN_PORT}}" -connect "${ARG_CONNECT:-$(connect_addr)}")
            [ "${ARG_MODE:-$PROXY_MODE}" = "tcp" ] && args+=(-mode tcp ${ARG_KCP[@]+"${ARG_KCP[@]}"}) || args+=(-mode udp)
            local prof="${ARG_OBF_PROFILE:-$OBF_PROFILE}" key="${ARG_OBF_KEY:-$OBF_KEY}"
            if [ "$prof" != "none" ] && [ -z "$key" ]; then
                key="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | xxd -p -c 32 2>/dev/null || true)"
                OBF_KEY="$key"
            fi
            [ "$prof" != "none" ] && [ -n "$key" ] && args+=(-obf-profile "$prof" -obf-key "$key")
            [ -n "${ARG_OBF_TIMING:-}" ] && args+=(-obf-timing "$ARG_OBF_TIMING")
            if [ -n "${ARG_CLIENT_ID:-}" ] || [ -n "${CLIENTS_FILE_CONF:-}" ]; then
                args+=(-clients-file "${CLIENTS_FILE_CONF:-$CLIENTSFILE}")
            fi
            ( cd "$PREFIX" && nohup "$bin" "${args[@]}" >"$LOGFILE" 2>&1 & echo $! > "$PIDFILE" )
            sleep 1; kill -0 "$(<"$PIDFILE")" 2>/dev/null || fail start_failed "nohup start failed" ;;
        *)  fail start_failed "unknown runtime" ;;
    esac
    local pid; pid=$(rt_pid)
    [ -n "$pid" ] && [ "$pid" != "0" ] && d_num pid "$pid"
}

firewall_open_port() {
    local port=${1:-} proto=${2:-udp}
    [[ "$port" =~ ^[0-9]+$ ]] || return 0
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "${port}/${proto}" >/dev/null 2>&1 || true
    elif command -v iptables >/dev/null 2>&1; then
        if ! iptables -C INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null; then
            iptables -I INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null || true
            command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1 || true
        fi
    fi
}

firewall_close_port() {
    local port=${1:-} proto=${2:-udp}
    [[ "$port" =~ ^[0-9]+$ ]] || return 0
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw delete allow "${port}/${proto}" >/dev/null 2>&1 || true
    elif command -v iptables >/dev/null 2>&1; then
        iptables -D INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null || true
        command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1 || true
    fi
}

firewall_open() {
    if [ "$INSTALL_FREETURN" = "1" ]; then
        firewall_open_port "$LISTEN_PORT" "udp"
        [ "$PROXY_MODE" = "tcp" ] && firewall_open_port "$LISTEN_PORT" "tcp"
    fi
    if [ "$INSTALL_AWG" = "1" ] && [ "$AWG_DIRECT_PORT" = "1" ] && [ -n "$BACKEND_PORT" ]; then
        firewall_open_port "$BACKEND_PORT" "udp"
    fi
}

_bin_path() {
    [ -x "$PREFIX/server" ] && { echo "$PREFIX/server"; return 0; }
    local arch; arch=$(detect_arch) || arch=""
    [ -n "$arch" ] && [ -x "$PREFIX/$arch" ] && { echo "$PREFIX/$arch"; return 0; }
}

_run_clients_cmd() {
    local cfile="${CLIENTS_FILE_CONF:-$CLIENTSFILE}" bin; bin=$(_bin_path)
    if [ -n "$bin" ] && [ -x "$bin" ]; then
        CLIENTS_FILE="$cfile" "$bin" clients "$@"
        return $?
    fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)" = "true" ]; then
        docker exec -i "$CONTAINER" env CLIENTS_FILE="$cfile" /app/server clients "$@"
        return $?
    fi
    return 1
}

clients_add() {
    _run_clients_cmd add "$1" "$2" >/dev/null 2>&1 || {
        [ "$_IS_RPC" = 1 ] && fail clients_cmd_failed "clients add failed" || log_warn "Не удалось добавить в clients.json"
    }
}

clients_remove_soft() {
    _run_clients_cmd remove "$1" >/dev/null 2>&1 || true
}

clients_json() {
    local cfile="${CLIENTS_FILE_CONF:-$CLIENTSFILE}" owner="" mapped="" f out="" first=1 line id comment el
    [ -f "$OWNERCIDFILE" ] && owner=$(tr -d ' \r\n' < "$OWNERCIDFILE")
    if [ -d "$SHARE_DIR" ]; then
        for f in "$SHARE_DIR"/*.cid; do
            [ -f "$f" ] && mapped="$mapped $(tr -d ' \r\n' < "$f")"
        done
    fi

    local raw; raw=$(_run_clients_cmd list 2>/dev/null) || { printf '[]'; return 0; }

    while IFS= read -r line; do
        case "$line" in " - "*) ;; *) continue ;; esac
        id=${line#" - "}; id=${id%% *}
        [ -n "$id" ] || continue
        [ "$id" = "$owner" ] && continue
        case " $mapped " in *" $id "*) continue ;; esac
        comment=$(printf '%s' "$line" | sed -nE 's/^ - [^ ]+ \(Comment: (.*)\)$/\1/p')
        el="{\"id\":\"$(esc "$id")\""
        [ -n "$comment" ] && el="$el,\"name_b64\":\"$(printf '%s' "$comment" | base64 | tr -d '\r\n')\""
        el="$el}"
        [ "$first" = 1 ] && first=0 || out="$out,"
        out="$out$el"
    done <<< "$raw"
    printf '[%s]' "$out"
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 80-commands.sh
# ─────────────────────────────────────────────────────────────────────────────
# Обработчики команд JSON RPC v2, CLI управления клиентами и интерактивных мастеров.

# ─────────────────────────────────────────────────────────────────────────────
# JSON RPC v2 парсер и команды
# ─────────────────────────────────────────────────────────────────────────────
ARG_LISTEN="" ARG_CONNECT="" ARG_MODE="" ARG_KCP=() ARG_OBF_PROFILE=""
ARG_OBF_KEY="" ARG_OBF_TIMING="" ARG_TAIL=80 ARG_WG_PORT="" ARG_WG_ENDPOINT=""
ARG_NAME_B64="" ARG_PUBKEY="" ARG_CLIENT_ID="" ARG_SHA256="" ARG_DNS="1.1.1.1"
ARG_WITH_WG_PKG=0 ARG_DRY_RUN=0 ARG_TARGET="all"

parse_rpc_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --listen=*)       ARG_LISTEN="${1#*=}" ;;
            --connect=*)      ARG_CONNECT="${1#*=}" ;;
            --mode=*)         ARG_MODE="${1#*=}" ;;
            --obf-profile=*)  ARG_OBF_PROFILE="${1#*=}" ;;
            --obf-timing=*)   ARG_OBF_TIMING="${1#*=}" ;;
            --obf-key=*)      ARG_OBF_KEY="${1#*=}" ;;
            --tail=*)         ARG_TAIL="${1#*=}" ;;
            --port=*)         ARG_WG_PORT="${1#*=}" ;;
            --endpoint=*)     ARG_WG_ENDPOINT="${1#*=}" ;;
            --name-b64=*)     ARG_NAME_B64="${1#*=}" ;;
            --pubkey=*)       ARG_PUBKEY="${1#*=}" ;;
            --client-id=*)    ARG_CLIENT_ID="${1#*=}" ;;
            --sha256=*)       ARG_SHA256="${1#*=}" ;;
            --dns=*)          ARG_DNS="${1#*=}" ;;
            --target=*)       ARG_TARGET="${1#*=}" ;;
            --with-wg-pkg)    ARG_WITH_WG_PKG=1 ;;
            --dry-run)        ARG_DRY_RUN=1 ;;
            --kcp-acknodelay=*) ARG_KCP+=("-kcp-acknodelay=${1#*=}") ;;
            --kcp-*)          ARG_KCP+=("-${1%%=*}" "${1#*=}") ;;
            *) fail bad_arg "unknown arg: $1" ;;
        esac
        shift
    done
}

cmd_probe() {
    stage probe
    local arch bin="" installed=false running=false version="" sha="" obf="none" mode="udp"
    arch=$(detect_arch) || arch=""
    [ -n "$arch" ] && [ -x "$PREFIX/$arch" ] && bin="$PREFIX/$arch"
    [ -z "$bin" ] && [ -x "$PREFIX/server" ] && bin="$PREFIX/server"

    if [ -n "$bin" ]; then
        installed=true
        sha=$(sha256sum "$bin" 2>/dev/null | awk '{print $1}' || true)
        [ -f "$VERFILE" ] && version=$(cat "$VERFILE" 2>/dev/null || true)
    elif [ -f "$COMPOSE_FILE" ]; then
        installed=true; version="docker"
    fi

    local runtime; runtime=$(current_runtime)
    if rt_running; then
        running=true
        local cmdline; cmdline=$(current_cmdline)
        if [ -n "$cmdline" ]; then
            obf=$(printf '%s' "$cmdline" | sed -nE 's/.*-obf-profile[= ]+([a-z0-9]+).*/\1/p'); obf=${obf:-none}
            mode=$(printf '%s' "$cmdline" | sed -nE 's/.*-mode[= ]+(udp|tcp).*/\1/p'); mode=${mode:-udp}
        elif [ "$runtime" = "docker" ]; then
            load_config; obf="${OBF_PROFILE:-none}"; mode="${PROXY_MODE:-udp}"
        fi
    fi

    local wgpresent=false wgp=""
    if awg_present; then wgpresent=true; wgp=$(awg_port)
    elif wg_present; then wgpresent=true; wgp=$(wg_port); fi

    local virt wgkernel=false
    virt=$(detect_virt)
    if wg_kernel_ok; then wgkernel=true; fi

    local cw=false cx=false cwe=false cts=false
    if conflict_warp; then cw=true; fi
    if conflict_x3ui; then cx=true; fi
    if conflict_wgeasy; then cwe=true; fi
    if conflict_tailscale; then cts=true; fi

    d_bool installed "$installed"
    [ -n "$version" ] && d_str version "$version"
    [ -n "$sha" ] && d_str bin_sha256 "$sha"
    d_bool running "$running"
    [ "$running" = true ] && { d_str mode "$mode"; d_str obf "$obf"; }
    d_str runtime "$runtime"
    d_num euid "$(id -u 2>/dev/null || echo -1)"
    [ -n "$wgp" ] && d_raw wg "{\"present\":$wgpresent,\"port\":$wgp}" || d_raw wg "{\"present\":$wgpresent,\"port\":null}"
    d_str virt "$virt"
    d_bool wg_kernel "$wgkernel"
    d_raw conflicts "{\"warp\":$cw,\"x3ui\":$cx,\"wgeasy\":$cwe,\"tailscale\":$cts,\"other_ifaces\":[$(other_wg_ifaces_csv)]}"
    ok
}

cmd_wg_setup() {
    stage wg_setup
    [ -n "$ARG_WG_PORT" ]     || fail bad_arg "--port required"
    [ -n "$ARG_WG_ENDPOINT" ] || fail bad_arg "--endpoint required"
    [ "$(id -u 2>/dev/null || echo -1)" -eq 0 ] || fail needs_root "root required"
    with_lock

    BACKEND_PORT="$ARG_WG_PORT"
    WG_ENDPOINT="$ARG_WG_ENDPOINT"
    awg_bootstrap
    local port; port=$(awg_port); [ -n "$port" ] || port="$ARG_WG_PORT"
    local existed=false; awg_present && existed=true
    d_raw wg "{\"port\":$port,\"existed\":$existed}"
    ok
}

cmd_install() {
    stage install
    [ "$(id -u 2>/dev/null || echo -1)" -eq 0 ] || fail needs_root "root required"
    mkdir -p "$PREFIX" || fail not_writable "cannot create $PREFIX"

    local name bin latest_url asset_url tmp ver curver="" cached=0
    name=$(detect_arch) || fail unsupported_arch "unsupported arch: $(uname -m)"
    bin="$PREFIX/$name"
    latest_url="$BASE_URL/$name"
    [ -f "$VERFILE" ] && curver=$(cat "$VERFILE" 2>/dev/null || true)

    ver=$(_resolve_version "$latest_url")
    if [ -z "$ver" ]; then
        if [ -x "$bin" ] || [ -x "$PREFIX/server" ]; then ver="${curver:-installed}"; cached=1
        else fail version_resolve_failed "cannot resolve latest version"; fi
    elif [ -x "$bin" ] && [ "$ver" = "$curver" ]; then cached=1; fi

    asset_url="$RELEASES_URL/download/$ver/$name"
    if [ "$cached" = 0 ]; then
        tmp=$(mktemp "$bin.XXXXXX" 2>/dev/null) || tmp="$bin.new.$$"
        _dl "$asset_url" "$tmp" || _dl "$latest_url" "$tmp" || { rm -f "$tmp"; fail download_failed "download failed"; }
        _verify_download "$tmp" "$ARG_SHA256"
        chmod 0755 "$tmp"
        with_lock
        [ -f "$bin" ] && cp -f "$bin" "$bin.bak" 2>/dev/null || true
        mv -f "$tmp" "$bin"
        cp -f "$bin" "$PREFIX/server" 2>/dev/null || true
        echo "$ver" > "$VERFILE"
    fi

    local was_running=false; rt_running && was_running=true
    if has_systemd; then _install_systemd_unit; state_set runtime systemd
    else state_set runtime nohup; fi

    d_str stage "$([ "$cached" = 1 ] && echo cached || echo downloaded)"
    d_str bin "$name"
    d_str version "$ver"
    d_str runtime "$(current_runtime)"
    d_bool needs_restart "$([ "$was_running" = true ] && [ "$cached" = 0 ] && echo true || echo false)"
    ok
}

cmd_start() {
    stage start
    [ -n "$ARG_LISTEN" ]  || fail bad_arg "--listen required"
    [ -n "$ARG_CONNECT" ] || fail bad_arg "--connect required"
    load_config
    with_lock

    if [ -n "$ARG_CLIENT_ID" ]; then
        init_clients_file
        clients_add "$ARG_CLIENT_ID" "owner"
        printf '%s\n' "$ARG_CLIENT_ID" > "$OWNERCIDFILE"; chmod 0600 "$OWNERCIDFILE"
    fi

    local port proto=udp owner opid; port=${ARG_LISTEN##*:}
    [ "${ARG_MODE:-$PROXY_MODE}" = "tcp" ] && proto=tcp
    if [[ "$port" =~ ^[0-9]+$ ]]; then
        owner=$(port_owner "$proto" "$port")
        case "$owner" in
            free|unknown) : ;;
            *)  opid=$(port_pid "$proto" "$port")
                [ -n "$opid" ] && ! pid_is_ours "$opid" && fail listen_port_busy "$proto port $port busy" ;;
        esac
        firewall_open_port "$port" "$proto"
    fi

    rt_stop
    rt_start
    ok
}

cmd_stop() { stage stop; with_lock; rt_stop; d_bool stopped true; ok; }

cmd_logs() {
    stage logs
    case "$(current_runtime)" in
        docker)
            command -v docker >/dev/null 2>&1 && while IFS= read -r l; do log "$l"; done < <(docker logs --tail "$ARG_TAIL" "$CONTAINER" 2>&1 || true) ;;
        systemd)
            command -v journalctl >/dev/null 2>&1 && while IFS= read -r l; do log "$l"; done < <(journalctl -u "$UNIT_NAME" -n "$ARG_TAIL" --no-pager 2>/dev/null || true) ;;
        nohup)
            [ -f "$LOGFILE" ] && while IFS= read -r l; do log "$l"; done < <(tail -n "$ARG_TAIL" "$LOGFILE") ;;
    esac
    ok
}

cmd_share_info() {
    stage share_info
    load_config
    local backend=false
    if awg_present || { wg_present && wg_is_ours; }; then
        backend=true
    fi
    d_bool wg_backend "$backend"
    d_str mode "${PROXY_MODE:-udp}"
    d_str obf_profile "${OBF_PROFILE:-none}"
    if [ -n "${OBF_KEY:-}" ]; then d_str obf_key "$OBF_KEY"; fi
    if awg_present; then d_str backend_type "awg"; fi
    ok
}

cmd_share_list() {
    stage share_list
    d_raw peers "$(peers_json)"
    local cj; cj=$(clients_json) || fail clients_cmd_failed "clients list failed"
    d_raw clients "$cj"
    ok
}

cmd_peer_add() {
    stage peer_add
    [ -n "$ARG_NAME_B64" ]    || fail bad_arg "--name-b64 required"
    [ -n "$ARG_WG_ENDPOINT" ] || fail bad_arg "--endpoint required"
    with_lock
    mkdir -p "$SHARE_DIR" "$CLIENTS_DIR"

    [ -n "$ARG_CLIENT_ID" ] && clients_add "$ARG_CLIENT_ID" "$(printf '%s' "$ARG_NAME_B64" | base64 -d 2>/dev/null || echo "client")"
    with_peers_lock

    local cname; cname="$(printf '%s' "$ARG_NAME_B64" | base64 -d 2>/dev/null || echo "client")"
    init_awg_params
    local cli_ip; cli_ip=$(alloc_client_ip "$AWG_CONF" "$AWG_NET")

    local kp cli_priv cli_pub srv_pub
    kp="$(generate_awg_keypair)"; cli_priv="${kp%% *}"; cli_pub="${kp##* }"
    srv_pub=$(cat "${AWG_DIR}/server.pub" 2>/dev/null || true)

    cat >> "$AWG_CONF" <<EOF

[Peer]
# ft-user: ${ARG_NAME_B64}
# client: ${cname}
PublicKey = ${cli_pub}
AllowedIPs = ${cli_ip}/32
EOF
    awg_reconcile

    local stored="$SHARE_DIR/$(_pub_fs "$cli_pub").conf"
    ( umask 077
      cat > "$stored" <<EOF
[Interface]
Address = ${cli_ip}/32
DNS = ${ARG_DNS:-1.1.1.1}
PrivateKey = ${cli_priv}
Jc = ${AWG_JC}
Jmin = ${AWG_JMIN}
Jmax = ${AWG_JMAX}
S1 = ${AWG_S1}
S2 = ${AWG_S2}
S3 = ${AWG_S3}
S4 = ${AWG_S4}
H1 = ${AWG_H1}
H2 = ${AWG_H2}
H3 = ${AWG_H3}
H4 = ${AWG_H4}
HeaderProtectionKey = ${AWG_HPK}
ContentPaddingAddition = 10
RekeyAfterTime = 110
RekeyTimeout = 5
RejectAfterTime = 160
KeepaliveTimeout = 10
MaxHandshakeAttempts = 15
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = ${srv_pub}
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = ${ARG_WG_ENDPOINT}
PersistentKeepalive = 25
EOF
    )

    [ -n "$ARG_CLIENT_ID" ] && { printf '%s\n' "$ARG_CLIENT_ID" > "$SHARE_DIR/$(_pub_fs "$cli_pub").cid"; d_str client_id "$ARG_CLIENT_ID"; }
    d_raw peer "{\"pub\":\"$(esc "$cli_pub")\",\"ip\":\"$(esc "$cli_ip")\"}"
    d_str client_conf_b64 "$(base64 < "$stored" | tr -d '\n')"
    ok
}

cmd_peer_conf() {
    stage peer_conf
    [ -n "$ARG_PUBKEY" ] || fail bad_arg "--pubkey required"
    local stored="$SHARE_DIR/$(_pub_fs "$ARG_PUBKEY").conf"
    [ -f "$stored" ] || fail no_stored_conf "conf not found"
    local cidfile="$SHARE_DIR/$(_pub_fs "$ARG_PUBKEY").cid"
    [ -f "$cidfile" ] && d_str client_id "$(cat "$cidfile" | tr -d ' \r\n')"
    d_str client_conf_b64 "$(base64 < "$stored" | tr -d '\n')"
    ok
}

cmd_peer_remove() {
    stage peer_remove
    [ -n "$ARG_PUBKEY" ] || fail bad_arg "--pubkey required"
    with_lock

    local target_conf=""
    [ -f "$AWG_CONF" ] && target_conf="$AWG_CONF"
    [ -z "$target_conf" ] && wg_present && target_conf="$WG_CONF"
    [ -z "$target_conf" ] && fail no_wg_backend "no managed vpn backend"

    local cidfile="$SHARE_DIR/$(_pub_fs "$ARG_PUBKEY").cid"
    [ -f "$cidfile" ] && { clients_remove_soft "$(cat "$cidfile" | tr -d ' \r\n')"; rm -f "$cidfile"; }

    with_peers_lock
    local tmp="$target_conf.tmp"
    awk -v key="$ARG_PUBKEY" '
        function flushbuf() { for (j = 0; j < n; j++) print buf[j]; n = 0 }
        /^[ \t]*\[/ { if (insec) { if (drop) n = 0; flushbuf() }; insec = 1; drop = 0; buf[n++] = $0; next }
        { if (!insec) { print; next }; buf[n++] = $0; line = $0; gsub(/[ \t\r]/, "", line); if (line == "PublicKey=" key) drop = 1 }
        END { if (insec) { if (drop) n = 0; flushbuf() } }
    ' "$target_conf" > "$tmp" 2>/dev/null || rm -f "$tmp"
    if [ -s "$tmp" ]; then
        chmod 0600 "$tmp"; mv -f "$tmp" "$target_conf"
        awg_reconcile; wg_reconcile
    fi
    rm -f "$tmp" "$SHARE_DIR/$(_pub_fs "$ARG_PUBKEY").conf"
    d_bool removed true
    ok
}

cmd_client_add() {
    stage client_add
    [ -n "$ARG_CLIENT_ID" ] || fail bad_arg "--client-id required"
    [ -n "$ARG_NAME_B64" ]  || fail bad_arg "--name-b64 required"
    with_lock
    clients_add "$ARG_CLIENT_ID" "$(printf '%s' "$ARG_NAME_B64" | base64 -d 2>/dev/null || echo "$ARG_CLIENT_ID")"
    ok
}

cmd_client_remove() {
    stage client_remove
    [ -n "$ARG_CLIENT_ID" ] || fail bad_arg "--client-id required"
    with_lock
    clients_remove_soft "$ARG_CLIENT_ID"
    ok
}

do_uninstall() {
    local target=${1:-all} purge=${2:-0}
    with_lock

    case "$target" in
        freeturn)
            log_info "Удаление FreeTurn..."
            if [ -f "$COMPOSE_FILE" ] && command -v docker >/dev/null 2>&1; then
                docker stop "$CONTAINER" >/dev/null 2>&1 || true
                docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
            fi
            if has_systemd; then
                systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
                rm -f "$UNIT_FILE"; systemctl daemon-reload || true
            fi
            firewall_close_port "$LISTEN_PORT" "udp"
            firewall_close_port "$LISTEN_PORT" "tcp"
            log_success "FreeTurn удалён. AmneziaWG остался активен."
            ;;
        awg)
            log_info "Удаление AmneziaWG / WireGuard..."
            if [ -f "$COMPOSE_FILE" ] && command -v docker >/dev/null 2>&1; then
                docker stop "$AWG_CONTAINER" >/dev/null 2>&1 || true
                docker rm -f "$AWG_CONTAINER" >/dev/null 2>&1 || true
            fi
            if has_systemd; then
                systemctl disable --now "wg-quick@${WG_IFACE}" >/dev/null 2>&1 || true
            fi
            firewall_close_port "$BACKEND_PORT"
            rm -rf "$AWG_DIR"
            log_success "AmneziaWG удалён. FreeTurn остался активен."
            ;;
        all)
            log_info "Полное удаление..."
            rt_stop
            if [ -f "$COMPOSE_FILE" ] && command -v docker >/dev/null 2>&1; then
                ( cd "$APP_DIR" && compose_cmd down -v ) >/dev/null 2>&1 || true
            fi
            if has_systemd; then
                systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
                systemctl disable --now "wg-quick@${WG_IFACE}" >/dev/null 2>&1 || true
                rm -f "$UNIT_FILE" "$LAUNCHER"
                systemctl daemon-reload || true
            fi
            firewall_close_port "$LISTEN_PORT" "udp"
            firewall_close_port "$LISTEN_PORT" "tcp"
            [ -n "$BACKEND_PORT" ] && firewall_close_port "$BACKEND_PORT" "udp"
            rm -f /etc/sysctl.d/99-free-turn-proxy.conf
            if [ "$purge" = "1" ] || [ "$PURGE" = "1" ]; then
                rm -rf "$APP_DIR"
                rm -f /usr/local/bin/freeturn /usr/local/bin/free-turn-proxy 2>/dev/null || true
                log_success "Каталог $APP_DIR полностью удалён."
            fi
            log_success "Все компоненты удалены."
            ;;
    esac
}

cmd_uninstall() {
    stage uninstall
    [ "$(id -u 2>/dev/null || echo -1)" -eq 0 ] || fail needs_root "root required"
    do_uninstall "${ARG_TARGET:-all}" "$ARG_WITH_WG_PKG"
    d_str target "${ARG_TARGET:-all}"
    d_bool uninstalled true
    ok
}

# ─────────────────────────────────────────────────────────────────────────────
# CLI управление клиентами
# ─────────────────────────────────────────────────────────────────────────────
client_add() {
    local cname="${1:-}" silent="${2:-0}"
    mkdir -p "$CLIENTS_DIR" "$SHARE_DIR"
    local ext_ip; ext_ip="$(get_public_ip)"

    if [ -z "$cname" ]; then
        local next_num=1
        [ -f "$CLIENTS_META" ] && next_num="$(( $(wc -l < "$CLIENTS_META") + 1 ))"
        ui_input cname "Имя нового клиента" "client-${next_num}"
    fi

    local cid=""
    if [ "$INSTALL_FREETURN" = "1" ] && [ -n "$CLIENTS_FILE_CONF" ]; then
        cid="$(openssl rand -hex 16)"
        clients_add "$cid" "$cname"
    fi

    local direct_conf="" relay_conf="" ft_file="" client_ip=""
    if [ "$INSTALL_AWG" = "1" ] && [ -f "$AWG_CONF" ]; then
        init_awg_params
        client_ip=$(alloc_client_ip "$AWG_CONF" "$AWG_NET")

        local kp cli_priv cli_pub srv_pub
        kp="$(generate_awg_keypair)"; cli_priv="${kp%% *}"; cli_pub="${kp##* }"
        srv_pub="$(cat "${AWG_DIR}/server.pub" 2>/dev/null || true)"

        cat >> "$AWG_CONF" <<EOF

[Peer]
# ft-user: $(printf '%s' "$cname" | base64 | tr -d '\r\n')
# client: ${cname}
PublicKey = ${cli_pub}
AllowedIPs = ${client_ip}/32
EOF
        awg_reconcile

        direct_conf="${CLIENTS_DIR}/${cname}-direct.conf"
        relay_conf="${CLIENTS_DIR}/${cname}-relay.conf"

        ( umask 077
          cat > "$direct_conf" <<EOF
[Interface]
Address = ${client_ip}/32
DNS = 1.1.1.1, 1.0.0.1
PrivateKey = ${cli_priv}
Jc = ${AWG_JC}
Jmin = ${AWG_JMIN}
Jmax = ${AWG_JMAX}
S1 = ${AWG_S1}
S2 = ${AWG_S2}
S3 = ${AWG_S3}
S4 = ${AWG_S4}
H1 = ${AWG_H1}
H2 = ${AWG_H2}
H3 = ${AWG_H3}
H4 = ${AWG_H4}
HeaderProtectionKey = ${AWG_HPK}
ContentPaddingAddition = 10
RekeyAfterTime = 110
RekeyTimeout = 5
RejectAfterTime = 160
KeepaliveTimeout = 10
MaxHandshakeAttempts = 15
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = ${srv_pub}
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = ${ext_ip}:${BACKEND_PORT}
PersistentKeepalive = 25
EOF
        )

        ( umask 077
          cat > "$relay_conf" <<EOF
[Interface]
Address = ${client_ip}/32
DNS = 1.1.1.1, 1.0.0.1
PrivateKey = ${cli_priv}
Jc = ${AWG_JC}
Jmin = ${AWG_JMIN}
Jmax = ${AWG_JMAX}
S1 = ${AWG_S1}
S2 = ${AWG_S2}
S3 = ${AWG_S3}
S4 = ${AWG_S4}
H1 = ${AWG_H1}
H2 = ${AWG_H2}
H3 = ${AWG_H3}
H4 = ${AWG_H4}
HeaderProtectionKey = ${AWG_HPK}
ContentPaddingAddition = 10
RekeyAfterTime = 110
RekeyTimeout = 5
RejectAfterTime = 160
KeepaliveTimeout = 10
MaxHandshakeAttempts = 15
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = ${srv_pub}
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = ${WG_ENDPOINT}
PersistentKeepalive = 25
EOF
        )
        cp -f "$direct_conf" "$SHARE_DIR/$(_pub_fs "$cli_pub").conf" 2>/dev/null || true
        [ -n "$cid" ] && printf '%s\n' "$cid" > "$SHARE_DIR/$(_pub_fs "$cli_pub").cid" 2>/dev/null || true
        direct_png="${CLIENTS_DIR}/${cname}-direct.png"
        relay_png="${CLIENTS_DIR}/${cname}-relay.png"
        generate_qr_png_file "$direct_conf" "$direct_png"
        [ -f "$relay_conf" ] && generate_qr_png_file "$relay_conf" "$relay_png"
    fi

    local ft_uri="" ft_vpn_uri="" ft_png="" ft_vpn_png=""
    if [ "$INSTALL_FREETURN" = "1" ]; then
        ft_uri="$(generate_freeturn_uri "${ext_ip}:${LISTEN_PORT}" "${PROXY_MODE}" "${OBF_PROFILE}" "${OBF_KEY}" "${cid}" "${cname}")"
        ft_file="${CLIENTS_DIR}/${cname}-freeturn.txt"
        ft_png="${CLIENTS_DIR}/${cname}-freeturn.png"
        echo "$ft_uri" > "$ft_file"; chmod 0600 "$ft_file"
        generate_qr_png_text "$ft_uri" "$ft_png"

        if [ -f "$relay_conf" ]; then
            ft_vpn_uri="$(generate_freeturn_uri "${ext_ip}:${LISTEN_PORT}" "${PROXY_MODE}" "${OBF_PROFILE}" "${OBF_KEY}" "${cid}" "${cname}" "$(<"$relay_conf")")"
            ft_vpn_file="${CLIENTS_DIR}/${cname}-freeturn-vpn.txt"
            ft_vpn_png="${CLIENTS_DIR}/${cname}-freeturn-vpn.png"
            echo "$ft_vpn_uri" > "$ft_vpn_file"; chmod 0600 "$ft_vpn_file"
            generate_qr_png_text "$ft_vpn_uri" "$ft_vpn_png"
        fi
    fi

    echo "${cname}|${client_ip}|${cid}|$(date '+%Y-%m-%d %H:%M')" >> "$CLIENTS_META"

    # При silent=1 (первый клиент в ходе установки) не спамим в консоль —
    # полная информация будет показана в финальной M3-рамке print_summary.
    if [ "$silent" = "1" ]; then
        return 0
    fi

    log_success "Клиент '${cname}' добавлен!"
    echo
    if [ "$HAS_GUM" = 1 ]; then
        local card=()
        card+=("$(gum style --foreground "$MD_SUCCESS" --bold "✔ Клиент '${cname}' готов к работе!")")
        card+=("")
        if [ -f "$direct_conf" ]; then
            card+=("$(gum style --foreground "$MD_PRIMARY" --bold "• [1/3] AmneziaWG Direct (AWG 3.1):")")
            card+=("  Конфиг:   $direct_conf")
            card+=("  QR (PNG): $direct_png")
        fi
        if [ -n "$ft_vpn_uri" ]; then
            [ -f "$direct_conf" ] && card+=("")
            card+=("$(gum style --foreground "$MD_PRIMARY" --bold "• [2/3] FreeTurn App (релей + вшитый VPN):")")
            card+=("  QR (PNG): $ft_vpn_png")
            card+=("  Ссылка:   $ft_vpn_uri")
        elif [ -n "$ft_uri" ]; then
            [ -f "$direct_conf" ] && card+=("")
            card+=("$(gum style --foreground "$MD_PRIMARY" --bold "• [2/3] FreeTurn App (прокси-режим):")")
            card+=("  QR (PNG): $ft_png")
            card+=("  Ссылка:   $ft_uri")
        fi
        if [ -f "$relay_conf" ]; then
            card+=("")
            card+=("$(gum style --foreground "$MD_PRIMARY" --bold "• [3/3] AmneziaWG Relay (127.0.0.1:9000):")")
            card+=("  Конфиг:   $relay_conf")
            card+=("  QR (PNG): $relay_png")
        fi
        card+=("")
        card+=("$(gum style --foreground "$MD_SECONDARY" --bold "Скачать все 3 QR-картинки на телефон/ПК:")")
        card+=("  scp root@${ext_ip}:${CLIENTS_DIR}/${cname}* .")
        card+=("")
        card+=("$(gum style --foreground "$MD_SECONDARY" --italic "Показать все QR в консоли: freeturn client qr ${cname}")")

        local card_body; card_body=$(printf '%s\n' "${card[@]}")
        gum style --border rounded --border-foreground "$MD_PRIMARY" --padding "1 2" "$card_body"
    else
        echo "========================================================"
        echo "  Клиент '${cname}' успешно добавлен!"
        echo "========================================================"
        [ -f "$direct_conf" ] && echo "  [1/3] AmneziaWG Direct:   $direct_conf (QR: $direct_png)"
        [ -n "$ft_vpn_uri" ]  && echo "  [2/3] FreeTurn App (VPN): $ft_vpn_uri (QR: $ft_vpn_png)"
        [ -f "$relay_conf" ]  && echo "  [3/3] AmneziaWG Relay:    $relay_conf (QR: $relay_png)"
        echo "  Скачать на ПК: scp root@${ext_ip}:${CLIENTS_DIR}/${cname}* ."
        echo "  Показать все QR в консоли: freeturn client qr ${cname}"
        echo "========================================================"
    fi
}

client_list() {
    mkdir -p "$CLIENTS_DIR"
    [ ! -s "$CLIENTS_META" ] && { log_warn "Список клиентов пуст."; return 0; }
    echo
    if [ "$HAS_GUM" = 1 ]; then
        { echo "ИМЯ,IP,CLIENT_ID,ДАТА"; while IFS='|' read -r name ip cid dt; do echo "${name},${ip:--},${cid:--},${dt}"; done < "$CLIENTS_META"; } \
            | gum table --border rounded
    else
        printf "%-15s %-16s %-34s %s\n" "Имя" "IP" "Client ID" "Дата"
        echo "-----------------------------------------------------------------------------"
        while IFS='|' read -r name ip cid dt; do printf "%-15s %-16s %-34s %s\n" "$name" "${ip:--}" "${cid:--}" "$dt"; done < "$CLIENTS_META"
    fi
}

client_resolve_name() {
    local input="${1:-}"
    [ -z "$input" ] && return 1
    [ ! -s "$CLIENTS_META" ] && return 1
    # 1. Точное совпадение
    if grep -q "^${input}|" "$CLIENTS_META" 2>/dev/null; then
        echo "$input"; return 0
    fi
    # 2. Совпадение без дефисов и подчеркиваний (например client1 -> client-1)
    local clean_in; clean_in=$(printf '%s' "$input" | tr -d '-_')
    local name
    while IFS='|' read -r name _ _ _; do
        local clean_name; clean_name=$(printf '%s' "$name" | tr -d '-_')
        if [ "${clean_in,,}" = "${clean_name,,}" ]; then
            echo "$name"; return 0
        fi
    done < "$CLIENTS_META"
    # 3. Подстрока без учета регистра
    while IFS='|' read -r name _ _ _; do
        if [[ "${name,,}" == *"${input,,}"* ]]; then
            echo "$name"; return 0
        fi
    done < "$CLIENTS_META"
    return 1
}

_show_qr_direct() {
    local cname="$1" num="${2:-}"
    local conf="${CLIENTS_DIR}/${cname}-direct.conf"
    local png="${CLIENTS_DIR}/${cname}-direct.png"
    [ ! -f "$conf" ] && return 0
    [ ! -f "$png" ] && generate_qr_png_file "$conf" "$png"

    local prefix=""
    [ -n "$num" ] && prefix="[${num}] "

    echo
    if [ "$HAS_GUM" = 1 ]; then
        gum style --border normal --border-foreground "$MD_PRIMARY" --padding "0 1" \
            "$(gum style --foreground "$MD_PRIMARY" --bold "${prefix}AmneziaWG Direct (AWG 3.1) — прямое подключение")" \
            "Для официального приложения AmneziaWG (напрямую к серверу, минуя релей)."
    else
        echo -e "${C_CYAN}--- ${prefix}AmneziaWG Direct (AWG 3.1) — прямое подключение ---${C_NC}"
        echo "Для официального приложения AmneziaWG (напрямую к серверу, минуя релей)."
    fi

    render_qr_file "$conf" "" "$png"
    download_file_osc1337 "$png"
}

_show_qr_freeturn() {
    local cname="$1" num="${2:-}"
    local txt="${CLIENTS_DIR}/${cname}-freeturn-vpn.txt"
    local png="${CLIENTS_DIR}/${cname}-freeturn-vpn.png"
    local is_vpn=1

    if [ ! -f "$txt" ]; then
        txt="${CLIENTS_DIR}/${cname}-freeturn.txt"
        png="${CLIENTS_DIR}/${cname}-freeturn.png"
        is_vpn=0
    fi
    [ ! -f "$txt" ] && return 0
    [ ! -f "$png" ] && generate_qr_png_text "$(<"$txt")" "$png"

    local prefix=""
    [ -n "$num" ] && prefix="[${num}] "

    echo
    if [ "$HAS_GUM" = 1 ]; then
        if [ "$is_vpn" = 1 ]; then
            gum style --border normal --border-foreground "$MD_SUCCESS" --padding "0 1" \
                "$(gum style --foreground "$MD_SUCCESS" --bold "${prefix}FreeTurn App — релей + вшитый VPN (всё в одном)")" \
                "Сканируйте встроенным сканером ВНУТРИ приложения FreeTurn (Android / iOS)!" \
                "Ссылка freeturn:// автоматически настраивает и релей, и VPN-туннель."
        else
            gum style --border normal --border-foreground "$MD_SUCCESS" --padding "0 1" \
                "$(gum style --foreground "$MD_SUCCESS" --bold "${prefix}FreeTurn App — ссылка freeturn:// (прокси-режим)")" \
                "Сканируйте встроенным сканером ВНУТРИ приложения FreeTurn (Android / iOS)."
        fi
    else
        echo -e "${C_GREEN}--- ${prefix}FreeTurn App — релей + вшитый VPN (всё в одном) ---${C_NC}"
        echo "Сканируйте встроенным сканером ВНУТРИ приложения FreeTurn (Android / iOS)!"
        echo "Ссылка freeturn:// автоматически настраивает и релей, и VPN-туннель."
    fi

    render_qr_text "$(<"$txt")" "" "$png"
    download_file_osc1337 "$png"
}

_show_qr_relay() {
    local cname="$1" num="${2:-}"
    local conf="${CLIENTS_DIR}/${cname}-relay.conf"
    local png="${CLIENTS_DIR}/${cname}-relay.png"
    [ ! -f "$conf" ] && return 0
    [ ! -f "$png" ] && generate_qr_png_file "$conf" "$png"

    local prefix=""
    [ -n "$num" ] && prefix="[${num}] "

    echo
    if [ "$HAS_GUM" = 1 ]; then
        gum style --border normal --border-foreground "$MD_SECONDARY" --padding "0 1" \
            "$(gum style --foreground "$MD_SECONDARY" --bold "${prefix}AmneziaWG Relay (127.0.0.1:9000) — раздельный запуск")" \
            "Конфиг для связки с отдельным CLI/Termux клиентом на ПК/телефоне." \
            "⚠ НЕ сканируйте в приложении FreeTurn — для него предназначен QR [2/3]!"
    else
        echo -e "${C_YELLOW}--- ${prefix}AmneziaWG Relay (127.0.0.1:9000) — раздельный запуск ---${C_NC}"
        echo "Конфиг для связки с отдельным CLI/Termux клиентом на ПК/телефоне."
        echo "⚠ НЕ сканируйте в приложении FreeTurn — для него предназначен QR [2/3]!"
    fi

    render_qr_file "$conf" "" "$png"
    download_file_osc1337 "$png"
}

_show_download_card() {
    local cname="$1" ext_ip="$2"
    echo
    if [ "$HAS_GUM" = 1 ]; then
        local card=()
        card+=("$(gum style --foreground "$MD_PRIMARY" --bold "📥 Как скачать все 3 картинки QR-кода на свой компьютер (ПК):")")
        card+=("")
        card+=("В терминале вашего ПК (PowerShell / macOS / Linux) выполните команду:")
        card+=("  $(gum style --foreground "$MD_SUCCESS" --bold "scp root@${ext_ip}:${CLIENTS_DIR}/${cname}*.png .")")
        card+=("")
        card+=("Будут сохранены все 3 файла:")
        card+=("  • ${cname}-direct.png       (AmneziaWG Direct - прямой доступ)")
        card+=("  • ${cname}-freeturn-vpn.png (FreeTurn App - со вшитым VPN)")
        card+=("  • ${cname}-relay.png        (AmneziaWG Relay - 127.0.0.1:9000)")
        if command -v python3 >/dev/null 2>&1; then
            card+=("")
            card+=("Или откройте веб-доступ для скачивания в браузере: freeturn client web ${cname}")
        fi
        gum style --border rounded --border-foreground "$MD_PRIMARY" --padding "1 2" "$(printf '%s\n' "${card[@]}")"
    else
        echo "===================================================================="
        echo "  📥 Как скачать все 3 картинки QR-кода на свой компьютер (ПК):"
        echo "  В терминале на вашем ПК выполните команду:"
        echo "    scp root@${ext_ip}:${CLIENTS_DIR}/${cname}*.png ."
        echo "  Будут скачаны:"
        echo "    • ${cname}-direct.png       (AmneziaWG Direct)"
        echo "    • ${cname}-freeturn-vpn.png (FreeTurn App со вшитым VPN)"
        echo "    • ${cname}-relay.png        (AmneziaWG Relay 127.0.0.1:9000)"
        command -v python3 >/dev/null 2>&1 && echo "  Или скачайте в браузере: freeturn client web ${cname}"
        echo "===================================================================="
    fi

    if [ "$NONINTERACTIVE" != "1" ] && command -v python3 >/dev/null 2>&1 && [ -t 0 ]; then
        echo
        if ui_yesno "Запустить временный веб-доступ для скачивания картинок через браузер?" "N"; then
            client_web "$cname" 8080
        fi
    fi
}

client_web() {
    local cname="${1:-}" port="${2:-8080}"
    if [ -z "$cname" ]; then
        [ ! -s "$CLIENTS_META" ] && die "Нет клиентов."
        local names=()
        while IFS='|' read -r n _ _ _; do names+=("$n"); done < "$CLIENTS_META"
        [ "$HAS_GUM" = 1 ] && cname=$(gum choose --header "Клиент для скачивания:" "${names[@]}" </dev/tty) || ui_input cname "Имя" "${names[0]}"
    else
        local resolved; resolved=$(client_resolve_name "$cname" || true)
        if [ -n "$resolved" ]; then
            cname="$resolved"
        else
            log_error "Клиент '$cname' не найден."
            echo
            client_list
            return 1
        fi
    fi

    local ext_ip; ext_ip="$(get_public_ip)"

    if ! command -v python3 >/dev/null 2>&1; then
        log_warn "python3 не установлен. Скачайте файлы через SCP:"
        echo "  scp root@${ext_ip}:${CLIENTS_DIR}/${cname}*.png ."
        return 0
    fi

    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "${port}/tcp" >/dev/null 2>&1 || true
    fi
    if command -v iptables >/dev/null 2>&1; then
        iptables -I INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null || true
    fi

    echo
    if [ "$HAS_GUM" = 1 ]; then
        local wcard=()
        wcard+=("$(gum style --foreground "$MD_SUCCESS" --bold "🌐 Временный веб-доступ открыт (порт ${port})")")
        wcard+=("")
        wcard+=("Ссылки для скачивания в браузере (телефон / ПК):")
        [ -f "${CLIENTS_DIR}/${cname}-direct.png" ]       && wcard+=("  • 1. AmneziaWG Direct:   http://${ext_ip}:${port}/${cname}-direct.png")
        [ -f "${CLIENTS_DIR}/${cname}-freeturn-vpn.png" ] && wcard+=("  • 2. FreeTurn App QR:    http://${ext_ip}:${port}/${cname}-freeturn-vpn.png")
        [ -f "${CLIENTS_DIR}/${cname}-relay.png" ]        && wcard+=("  • 3. AmneziaWG Relay:    http://${ext_ip}:${port}/${cname}-relay.png")
        wcard+=("")
        wcard+=("Каталог со всеми файлами: http://${ext_ip}:${port}/")
        wcard+=("")
        wcard+=("$(gum style --foreground "$MD_TERTIARY" --italic "Нажмите Enter для остановки веб-сервера и закрытия порта...")")
        gum style --border rounded --border-foreground "$MD_PRIMARY" --padding "1 2" "$(printf '%s\n' "${wcard[@]}")"
    else
        echo "===================================================================="
        echo "  🌐 Временный веб-сервер открыт: http://${ext_ip}:${port}/"
        [ -f "${CLIENTS_DIR}/${cname}-direct.png" ]       && echo "  • AmneziaWG Direct: http://${ext_ip}:${port}/${cname}-direct.png"
        [ -f "${CLIENTS_DIR}/${cname}-freeturn-vpn.png" ] && echo "  • FreeTurn App:     http://${ext_ip}:${port}/${cname}-freeturn-vpn.png"
        [ -f "${CLIENTS_DIR}/${cname}-relay.png" ]        && echo "  • AmneziaWG Relay:  http://${ext_ip}:${port}/${cname}-relay.png"
        echo "  Нажмите Enter для остановки веб-сервера и закрытия порта..."
        echo "===================================================================="
    fi

    ( cd "$CLIENTS_DIR" && python3 -m http.server "$port" >/dev/null 2>&1 ) &
    local srv_pid=$!

    read -r _ </dev/tty 2>/dev/null || read -r _ 2>/dev/null || sleep 60
    kill "$srv_pid" 2>/dev/null || true
    wait "$srv_pid" 2>/dev/null || true

    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw delete allow "${port}/tcp" >/dev/null 2>&1 || true
    fi
    if command -v iptables >/dev/null 2>&1; then
        iptables -D INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null || true
    fi
    echo
    log_info "Веб-сервер остановлен, порт ${port} закрыт."
}

client_qr() {
    local cname="${1:-}" mode="${2:-all}"
    if [ -z "$cname" ]; then
        [ ! -s "$CLIENTS_META" ] && die "Нет клиентов."
        local names=()
        while IFS='|' read -r n _ _ _; do names+=("$n"); done < "$CLIENTS_META"
        [ "$HAS_GUM" = 1 ] && cname=$(gum choose --header "Клиент:" "${names[@]}" </dev/tty) || ui_input cname "Имя" "${names[0]}"
    else
        local resolved; resolved=$(client_resolve_name "$cname" || true)
        if [ -n "$resolved" ]; then
            cname="$resolved"
        else
            log_error "Клиент '$cname' не найден."
            echo
            client_list
            return 1
        fi
    fi

    local ext_ip; ext_ip="$(get_public_ip)"

    case "$mode" in
        direct)
            _show_qr_direct "$cname" "1/1"
            ;;
        freeturn|freeturn_vpn|ft|vpn)
            _show_qr_freeturn "$cname" "1/1"
            ;;
        relay)
            _show_qr_relay "$cname" "1/1"
            ;;
        all|"")
            local total=0
            [ -f "${CLIENTS_DIR}/${cname}-direct.conf" ] && ((total++)) || true
            if [ -f "${CLIENTS_DIR}/${cname}-freeturn-vpn.txt" ] || [ -f "${CLIENTS_DIR}/${cname}-freeturn.txt" ]; then
                ((total++)) || true
            fi
            [ -f "${CLIENTS_DIR}/${cname}-relay.conf" ] && ((total++)) || true

            local idx=1
            if [ -f "${CLIENTS_DIR}/${cname}-direct.conf" ]; then
                _show_qr_direct "$cname" "${idx}/${total}"
                ((idx++))
                echo
            fi
            if [ -f "${CLIENTS_DIR}/${cname}-freeturn-vpn.txt" ] || [ -f "${CLIENTS_DIR}/${cname}-freeturn.txt" ]; then
                _show_qr_freeturn "$cname" "${idx}/${total}"
                ((idx++))
                echo
            fi
            if [ -f "${CLIENTS_DIR}/${cname}-relay.conf" ]; then
                _show_qr_relay "$cname" "${idx}/${total}"
                echo
            fi
            ;;
        *)
            die "Неизвестный формат QR: $mode (доступно: all, direct, freeturn, relay)"
            ;;
    esac

    _show_download_card "$cname" "$ext_ip"
}

client_remove() {
    local cname="${1:-}"
    if [ -z "$cname" ]; then
        [ ! -s "$CLIENTS_META" ] && die "Нет клиентов."
        local names=()
        while IFS='|' read -r n _ _ _; do names+=("$n"); done < "$CLIENTS_META"
        [ "$HAS_GUM" = 1 ] && cname=$(gum choose --header "Удалить клиента:" "${names[@]}" </dev/tty) || ui_input cname "Имя" "${names[0]}"
    else
        local resolved; resolved=$(client_resolve_name "$cname" || true)
        [ -n "$resolved" ] && cname="$resolved"
    fi

    local cid=""; [ -f "$CLIENTS_META" ] && cid=$(grep "^${cname}|" "$CLIENTS_META" | awk -F'|' '{print $3}' || true)
    [ -n "$cid" ] && clients_remove_soft "$cid"

    if [ -f "$AWG_CONF" ]; then
        local tmp="$AWG_CONF.tmp"
        awk -v name="$cname" '
            function flushbuf() { for (j = 0; j < n; j++) print buf[j]; n = 0 }
            /^[ \t]*\[/ { if (insec) { if (drop) n = 0; flushbuf() }; insec = 1; drop = 0; buf[n++] = $0; next }
            { if (!insec) { print; next }; buf[n++] = $0; line = $0; gsub(/[ \t\r]/, "", line); if (line == "#client:" name) drop = 1 }
            END { if (insec) { if (drop) n = 0; flushbuf() } }
        ' "$AWG_CONF" > "$tmp" 2>/dev/null || rm -f "$tmp"
        [ -s "$tmp" ] && { chmod 0600 "$tmp"; mv -f "$tmp" "$AWG_CONF"; awg_reconcile; }
        rm -f "$tmp"
    fi

    rm -f "${CLIENTS_DIR}/${cname}-direct.conf" "${CLIENTS_DIR}/${cname}-relay.conf" \
          "${CLIENTS_DIR}/${cname}-freeturn.txt" "${CLIENTS_DIR}/${cname}-freeturn-vpn.txt" \
          "${CLIENTS_DIR}/${cname}-direct.png" "${CLIENTS_DIR}/${cname}-relay.png" \
          "${CLIENTS_DIR}/${cname}-freeturn.png" "${CLIENTS_DIR}/${cname}-freeturn-vpn.png"
    [ -f "$CLIENTS_META" ] && sed -i "/^${cname}|/d" "$CLIENTS_META"
    log_success "Клиент '${cname}' удалён."
}

# ─────────────────────────────────────────────────────────────────────────────
# TUI Мастер
# ─────────────────────────────────────────────────────────────────────────────
wizard() {
    local comp_choice="full"
    if [ "$INSTALL_FREETURN" = "1" ] && [ "$INSTALL_AWG" = "1" ]; then comp_choice="full"
    elif [ "$INSTALL_FREETURN" = "0" ] && [ "$INSTALL_AWG" = "1" ]; then comp_choice="awg_only"
    elif [ "$INSTALL_FREETURN" = "1" ] && [ "$INSTALL_AWG" = "0" ]; then comp_choice="freeturn_only"
    fi

    ui_menu comp_choice "Состав установки:" "$comp_choice" \
        full          "FreeTurn + AmneziaWG 3.1  (рекомендуется - полный комплект)" \
        awg_only      "Только AmneziaWG 3.1      (чистый VPN-сервер без релея)" \
        freeturn_only "Только FreeTurn          (релей для стороннего бэкенда)"

    case "$comp_choice" in
        full)          INSTALL_FREETURN=1; INSTALL_AWG=1 ;;
        awg_only)      INSTALL_FREETURN=0; INSTALL_AWG=1 ;;
        freeturn_only) INSTALL_FREETURN=1; INSTALL_AWG=0 ;;
    esac

    ui_menu INSTALL_METHOD "Метод запуска:" "$INSTALL_METHOD" \
        docker  "Docker Compose (удобно, изолированно)" \
        systemd "Systemd (прямо на хосте)"

    if [ "$INSTALL_FREETURN" = "1" ]; then
        ui_menu PROXY_MODE "Режим релея:" "$PROXY_MODE" \
            udp "UDP-relay (AmneziaWG / WireGuard)" \
            tcp "TCP-forward (Xray / sing-box)"
        ask_port LISTEN_PORT "Внешний порт FreeTurn" "${LISTEN_PORT:-56000}"
        ui_menu OBF_PROFILE "Обфускация FreeTurn:" "$OBF_PROFILE" \
            rtpopus3 "rtpopus3 (RTP/opus + RFC 8285 + ChaCha20)" \
            rtpopus2 "rtpopus2 (RTP/opus + RFC 8285)" \
            rtpopus  "rtpopus (базовый RTP/opus)" \
            none     "none (без обфускации)"
        if [ "$OBF_PROFILE" != "none" ]; then
            [ -z "$OBF_KEY" ] && OBF_KEY="$(openssl rand -hex 32)"
        else OBF_KEY=""; fi
        ui_yesno "Включить авторизацию по Client ID (allowlist)?" "Y" \
            && CLIENTS_FILE_CONF="${CLIENTS_DIR}/clients.json" || CLIENTS_FILE_CONF=""
    fi

    if [ "$INSTALL_AWG" = "1" ]; then
        ask_port BACKEND_PORT "Порт AmneziaWG" "${BACKEND_PORT:-51820}"
        ui_yesno "Открыть порт AmneziaWG (${BACKEND_PORT}/udp) для прямого подключения?" "Y" \
            && AWG_DIRECT_PORT=1 || AWG_DIRECT_PORT=0
        ui_input WG_ENDPOINT "Локальный Endpoint клиента (-listen)" "${WG_ENDPOINT:-127.0.0.1:9000}"
    elif [ "$INSTALL_FREETURN" = "1" ]; then
        ask_port BACKEND_PORT "Порт бэкенда (куда пересылать трафик)" "${BACKEND_PORT:-51820}"
    fi

    ui_yesno "Открыть необходимые порты в файрволе сервера?" "Y" \
        && OPEN_FIREWALL=1 || OPEN_FIREWALL=0
}

review_config() {
    local comp_name="FreeTurn + AmneziaWG 3.1"
    [ "$INSTALL_FREETURN" = "0" ] && comp_name="Только AmneziaWG 3.1"
    [ "$INSTALL_AWG" = "0" ] && comp_name="Только FreeTurn"

    if [ "$HAS_GUM" = 1 ]; then
        local lines=()
        lines+=("$(gum style --foreground "$MD_PRIMARY" --bold "Настройки установки")")
        lines+=("")
        lines+=("$(printf '%-20s %s' "Компоненты:" "$comp_name")")
        lines+=("$(printf '%-20s %s' "Метод запуска:" "$INSTALL_METHOD")")
        [ "$INSTALL_FREETURN" = "1" ] && lines+=("$(printf '%-20s %s' "Порт FreeTurn:" "0.0.0.0:$LISTEN_PORT")")
        [ "$INSTALL_FREETURN" = "1" ] && lines+=("$(printf '%-20s %s' "Режим релея:" "$PROXY_MODE")")
        [ "$INSTALL_FREETURN" = "1" ] && lines+=("$(printf '%-20s %s' "Обфускация:" "$OBF_PROFILE")")
        [ "$INSTALL_AWG" = "1" ]      && lines+=("$(printf '%-20s %s' "Порт AmneziaWG:" "$BACKEND_PORT")")
        [ "$INSTALL_AWG" = "1" ]      && lines+=("$(printf '%-20s %s' "Прямой AWG:" "$([ "$AWG_DIRECT_PORT" = "1" ] && echo "да" || echo "нет")")")
        lines+=("$(printf '%-20s %s' "Файрвол:" "$([ "$OPEN_FIREWALL" = "1" ] && echo "открыть" || echo "не трогать")")")

        local body; body=$(printf '%s\n' "${lines[@]}")
        gum style --border double --border-foreground "$MD_PRIMARY" --padding "1 2" "$body"
    else
        echo; log_info "Настройки: components=$comp_name method=$INSTALL_METHOD"
    fi
    ui_drain_input
    ui_yesno "Применить конфигурацию?" "Y" || ui_abort
}

install_cli_symlink() {
    mkdir -p "$PREFIX" 2>/dev/null || true
    mkdir -p /usr/local/bin 2>/dev/null || true

    local script_target="$PREFIX/install.sh"
    local cur_script="${BASH_SOURCE[0]:-$0}"

    if [ -f "$cur_script" ]; then
        if [ "$(readlink -f "$cur_script" 2>/dev/null || true)" != "$(readlink -f "$script_target" 2>/dev/null || true)" ]; then
            cp -f "$cur_script" "$script_target" 2>/dev/null || true
        fi
    elif [ ! -f "$script_target" ]; then
        local repo_raw="https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/master/scripts/install.sh"
        if command -v curl >/dev/null 2>&1; then
            curl -sSL "$repo_raw" -o "$script_target" 2>/dev/null || true
        elif command -v wget >/dev/null 2>&1; then
            wget -qO "$script_target" "$repo_raw" 2>/dev/null || true
        fi
    fi

    if [ -f "$script_target" ]; then
        chmod 0755 "$script_target" 2>/dev/null || true
        ln -sf "$script_target" /usr/local/bin/freeturn 2>/dev/null || true
        ln -sf "$script_target" /usr/local/bin/free-turn-proxy 2>/dev/null || true
    fi
}

apply() {
    save_config
    [ "$INSTALL_AWG" = "1" ] && awg_bootstrap
    [ "$INSTALL_WG" = "1" ] && wireguard_bootstrap
    if [ "$INSTALL_METHOD" = "docker" ]; then
        apply_docker
    else
        apply_systemd
    fi
    [ "$OPEN_FIREWALL" = 1 ] && firewall_open
    if [ ! -s "$CLIENTS_META" ] && [ "$INSTALL_AWG" = "1" ]; then
        client_add "client-1" 1
    fi
    install_cli_symlink
}

print_summary() {
    ui_drain_input
    local ext_ip; ext_ip="$(get_public_ip)"
    echo
    if [ "$HAS_GUM" = 1 ]; then
        local lines=()
        lines+=("$(gum style --foreground "$MD_SUCCESS" --bold "✔ Установка успешно завершена!")")
        lines+=("")
        lines+=("$(gum style --foreground "$MD_PRIMARY" --bold "Параметры сервера:")")
        if [ "$INSTALL_FREETURN" = "1" ]; then
            lines+=("  • Сервер FreeTurn: $(gum style --bold "${ext_ip}:${LISTEN_PORT}") $(gum style --foreground "$MD_TERTIARY" "(${OBF_PROFILE})")")
        fi
        if [ "$INSTALL_AWG" = "1" ]; then
            local awg_info=""
            [ "$AWG_DIRECT_PORT" = "1" ] && awg_info=" (прямой доступ открыт)"
            lines+=("  • AmneziaWG 3.1:   $(gum style --bold "порт ${BACKEND_PORT}")${awg_info}")
        fi

        if [ -s "$CLIENTS_META" ]; then
            local first_cname; first_cname=$(head -n1 "$CLIENTS_META" | cut -d'|' -f1)
            if [ -n "$first_cname" ]; then
                lines+=("")
                lines+=("$(gum style --foreground "$MD_PRIMARY" --bold "Первый клиент ('${first_cname}'):")")
                [ -f "${CLIENTS_DIR}/${first_cname}-direct.png" ]       && lines+=("  • [1/3] AmneziaWG Direct: ${CLIENTS_DIR}/${first_cname}-direct.png")
                [ -f "${CLIENTS_DIR}/${first_cname}-freeturn-vpn.png" ] && lines+=("  • [2/3] FreeTurn App:     ${CLIENTS_DIR}/${first_cname}-freeturn-vpn.png")
                [ -f "${CLIENTS_DIR}/${first_cname}-relay.png" ]        && lines+=("  • [3/3] AmneziaWG Relay:  ${CLIENTS_DIR}/${first_cname}-relay.png")
                lines+=("  • Скачать на ПК (SCP):    scp root@${ext_ip}:${CLIENTS_DIR}/${first_cname}*.png .")
            fi
        fi

        lines+=("")
        lines+=("$(gum style --foreground "$MD_PRIMARY" --bold "Управление клиентами:")")
        lines+=("  freeturn client add [name]        - добавить клиента")
        lines+=("  freeturn client list              - список клиентов")
        lines+=("  freeturn client qr [name]         - показать все 3 QR-кода")
        lines+=("  freeturn client web [name] [port] - скачать картинки через браузер")

        local body; body=$(printf '%s\n' "${lines[@]}")
        gum style --border rounded --border-foreground "$MD_SUCCESS" --padding "1 2" "$body"
    else
        echo "========================================================"
        echo "  Установка успешно завершена!"
        echo "========================================================"
        [ "$INSTALL_FREETURN" = "1" ] && echo "FreeTurn: ${ext_ip}:${LISTEN_PORT} (${OBF_PROFILE})"
        [ "$INSTALL_AWG" = "1" ] && echo "AmneziaWG: порт ${BACKEND_PORT}"
        if [ -s "$CLIENTS_META" ]; then
            local first_cname; first_cname=$(head -n1 "$CLIENTS_META" | cut -d'|' -f1)
            if [ -n "$first_cname" ]; then
                echo "Первый клиент: ${first_cname}"
                [ -f "${CLIENTS_DIR}/${first_cname}-direct.png" ]       && echo "  [1/3] Direct QR:   ${CLIENTS_DIR}/${first_cname}-direct.png"
                [ -f "${CLIENTS_DIR}/${first_cname}-freeturn-vpn.png" ] && echo "  [2/3] FreeTurn QR: ${CLIENTS_DIR}/${first_cname}-freeturn-vpn.png"
                [ -f "${CLIENTS_DIR}/${first_cname}-relay.png" ]        && echo "  [3/3] Relay QR:    ${CLIENTS_DIR}/${first_cname}-relay.png"
                echo "  Скачать: scp root@${ext_ip}:${CLIENTS_DIR}/${first_cname}*.png ."
            fi
        fi
        echo "Управление клиентами: freeturn client <add|list|qr|web>"
    fi
    ui_drain_input

    if [ "$NONINTERACTIVE" != "1" ] && [ -s "$CLIENTS_META" ]; then
        local first_cname; first_cname=$(head -n1 "$CLIENTS_META" | cut -d'|' -f1)
        if [ -n "$first_cname" ]; then
            echo
            if ui_yesno "Вывести QR-коды клиента '${first_cname}' (все 3 формата) прямо сейчас?" "Y"; then
                echo
                client_qr "$first_cname" "all"
            fi
        fi
    fi
}

flow_install()     { wizard; validate_config; review_config; apply; print_summary; }
flow_reconfigure() { load_config; wizard; validate_config; review_config; apply; print_summary; }
flow_update()      { load_config; validate_config; apply; print_summary; }

flow_uninstall() {
    local choice
    ui_menu choice "Что удалить?" "all" \
        freeturn "Только FreeTurn (сохранить AmneziaWG для прямого доступа)" \
        awg      "Только AmneziaWG (сохранить FreeTurn)" \
        all      "Всё полностью (FreeTurn + AmneziaWG + конфиги и ключи)" \
        back     "Отмена"
    [ "$choice" = "back" ] && return 0
    ui_yesno "Вы уверены?" "N" || ui_abort
    do_uninstall "$choice" 1
}

menu_existing() {
    local choice
    ui_menu choice "Сервер настроен. Действие:" "clients" \
        clients     "Управление клиентами (добавить, список, QR, скачивание)" \
        reconfigure "Изменить настройки (переконфигурировать)" \
        update      "Обновить версию" \
        logs        "Просмотреть последние логи" \
        uninstall   "Удалить (полностью или раздельно)" \
        exit        "Выход"
    case "$choice" in
        clients)
            while :; do
                local c; ui_menu c "Клиенты:" "add" add "Добавить" list "Список" qr "QR-коды (все 3)" web "Скачать через браузер" remove "Удалить" back "Назад"
                case "$c" in add) client_add "" 0 ;; list) client_list ;; qr) client_qr "" "all" ;; web) client_web "" 8080 ;; remove) client_remove "" ;; back) break ;; esac
            done; menu_existing ;;
        reconfigure) flow_reconfigure ;;
        update)      flow_update ;;
        logs)
            case "$(current_runtime)" in
                docker)  docker logs --tail 40 "$CONTAINER" 2>&1 | ${PAGER:-cat} ;;
                systemd) journalctl -u "$UNIT_NAME" -n 40 --no-pager ;;
                nohup)   tail -n 40 "$LOGFILE" 2>/dev/null || echo "Лог пуст" ;;
            esac; menu_existing ;;
        uninstall)   flow_uninstall ;;
        exit)        ui_abort ;;
    esac
}

# ─────────────────────────────────────────────────────────────────────────────
# Модуль: 90-main.sh
# ─────────────────────────────────────────────────────────────────────────────
# Точка входа: маршрутизация RPC, CLI управления пирами, non-interactive и TUI мастера.

usage() {
    cat <<EOF
Free Turn Proxy & AmneziaWG - установщик и контроллер сервера.

Использование:
  freeturn                                интерактивный мастер (gum TUI)
  freeturn client add [name]              добавить клиента и показать QR
  freeturn client list                    список клиентов
  freeturn client qr [name] [mode]        показать QR-коды (все 3 или конкретный: direct/freeturn/relay)
  freeturn client web [name] [port]       раздать картинки QR для скачивания в браузере
  freeturn client remove [name]           удалить клиента
  freeturn -y [опции]                     неинтерактивная установка (скрипты/CI)

Опции компонентов:
  --only-awg                     установить только AmneziaWG 3.1 (без FreeTurn)
  --only-freeturn                установить только FreeTurn (без AmneziaWG)
  --with-freeturn                доустановить FreeTurn
  --with-awg                     доустановить AmneziaWG

Опции конфигурации:
  -y, --yes, --non-interactive   без интерактивных вопросов
  --method docker|systemd        метод запуска (default: docker)
  --mode   udp|tcp               режим туннеля (default: udp)
  --backend-port N               порт AmneziaWG (default: 51820)
  --listen-port N                внешний порт FreeTurn (default: 56000)
  --awg-direct | --no-awg-direct прямой доступ к порту AWG (default: да)
  --obf rtpopus3|rtpopus2|rtpopus|none  обфускация (default: rtpopus3)
  --obf-key HEX64                ключ обфускации (нет -> сгенерируется)
  --clients-auth | --no-clients-auth   авторизация по Client ID
  --firewall | --no-firewall     открывать порты в файрволе

Действия:
  --reconfigure                  переконфигурировать сервер
  --update                       обновить версию
  --uninstall                    удалить сервер
  --only freeturn|awg|all        цель удаления (default: all)
  --purge                        удалить каталог /opt/free-turn-proxy
  -h, --help                     справка

Машиночитаемый JSON RPC v2 (мобильное приложение):
  freeturn <probe|install|wg-setup|start|stop|logs|share-info|share-list|peer-add|peer-conf|peer-remove|client-add|client-remove|uninstall> [flags]
EOF
}

parse_cli_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            -y | --yes | --non-interactive) NONINTERACTIVE=1 ;;
            --only-awg)        OVERRIDES+=("INSTALL_FREETURN=0" "INSTALL_AWG=1" "AWG_DIRECT_PORT=1") ;;
            --only-freeturn)   OVERRIDES+=("INSTALL_FREETURN=1" "INSTALL_AWG=0") ;;
            --with-freeturn)   OVERRIDES+=("INSTALL_FREETURN=1") ;;
            --with-awg)        OVERRIDES+=("INSTALL_AWG=1") ;;
            --method)          OVERRIDES+=("INSTALL_METHOD=${2:-docker}"); shift ;;
            --mode)            OVERRIDES+=("PROXY_MODE=${2:-udp}"); shift ;;
            --backend-port)    OVERRIDES+=("BACKEND_PORT=${2:-51820}"); shift ;;
            --listen-port)     OVERRIDES+=("LISTEN_PORT=${2:-56000}"); shift ;;
            --awg-direct)      OVERRIDES+=("AWG_DIRECT_PORT=1") ;;
            --no-awg-direct)   OVERRIDES+=("AWG_DIRECT_PORT=0") ;;
            --obf)             OVERRIDES+=("OBF_PROFILE=${2:-rtpopus3}"); shift ;;
            --obf-key)         OVERRIDES+=("OBF_KEY=${2:-}"); shift ;;
            --clients-auth)    OVERRIDES+=("CLIENTS_FILE_CONF=${CLIENTS_DIR}/clients.json") ;;
            --no-clients-auth) OVERRIDES+=("CLIENTS_FILE_CONF=") ;;
            --wg-endpoint)     OVERRIDES+=("WG_ENDPOINT=${2:-}"); shift ;;
            --version)         OVERRIDES+=("VERSION=${2:-latest}"); shift ;;
            --firewall)        OPEN_FIREWALL=1 ;;
            --no-firewall)     OPEN_FIREWALL=0 ;;
            --reconfigure)     ACTION="reconfigure" ;;
            --update)          ACTION="update"; NONINTERACTIVE=1 ;;
            --uninstall)       ACTION="uninstall"; NONINTERACTIVE=1 ;;
            --only)            UNINSTALL_TARGET="${2:-all}"; shift ;;
            --purge)           PURGE=1 ;;
            -h | --help)       usage; exit 0 ;;
            *) die "Неизвестный аргумент: $1 (см. --help)" ;;
        esac
        shift
    done
}

main() {
    for arg in "$@"; do
        if [ "$arg" = "-h" ] || [ "$arg" = "--help" ]; then
            usage; return 0
        fi
    done

    # JSON RPC v2 протокол (вызовы без дефисов, кроме 'client')
    if [ $# -ge 1 ] && [[ "$1" =~ ^[a-z-]+$ ]] && [ "$1" != "client" ]; then
        case "$1" in
            probe)         _IS_RPC=1; HAS_GUM=0; cmd_probe; return 0 ;;
            install)       _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_install; return 0 ;;
            wg-setup)      _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_wg_setup; return 0 ;;
            start)         _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_start; return 0 ;;
            stop)          _IS_RPC=1; HAS_GUM=0; cmd_stop; return 0 ;;
            logs)          _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_logs; return 0 ;;
            share-info)    _IS_RPC=1; HAS_GUM=0; cmd_share_info; return 0 ;;
            share-list)    _IS_RPC=1; HAS_GUM=0; cmd_share_list; return 0 ;;
            peer-add)      _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_peer_add; return 0 ;;
            peer-conf)     _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_peer_conf; return 0 ;;
            peer-remove)   _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_peer_remove; return 0 ;;
            client-add)    _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_client_add; return 0 ;;
            client-remove) _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_client_remove; return 0 ;;
            uninstall)     _IS_RPC=1; HAS_GUM=0; shift; parse_rpc_args "$@"; cmd_uninstall; return 0 ;;
            *)             _IS_RPC=1; HAS_GUM=0; fail bad_arg "unknown subcommand: $1" ;;
        esac
    fi

    # Проверка прав root
    [ "$(id -u 2>/dev/null || echo -1)" -ne 0 ] && die "Запустите скрипт от root (sudo)."
    install_cli_symlink
    ensure_base_deps
    detect_arch >/dev/null 2>&1 || true

    # CLI управление клиентами
    if [ $# -ge 1 ] && [ "$1" = "client" ]; then
        shift
        local sub="${1:-}"
        [ -n "$sub" ] && shift || true
        load_config
        case "$sub" in
            add)    client_add "${1:-}" 0 ;;
            list)   client_list ;;
            qr)     client_qr "${1:-}" "${2:-all}" ;;
            web)    client_web "${1:-}" "${2:-8080}" ;;
            remove) client_remove "${1:-}" ;;
            *)      die "Использование: freeturn client <add|list|qr|web|remove>" ;;
        esac
        return 0
    fi

    parse_cli_args "$@"

    if [ "$NONINTERACTIVE" = 1 ]; then
        is_installed && load_config || true
        apply_overrides
        if [ "$ACTION" = "uninstall" ]; then
            do_uninstall "$UNINSTALL_TARGET" "$PURGE"
            return 0
        fi
        validate_config
        [ -z "$OPEN_FIREWALL" ] && OPEN_FIREWALL=1
        apply
        print_summary
        return 0
    fi

    ensure_gum
    ui_banner
    if [ "$ACTION" = "reconfigure" ]; then
        flow_reconfigure
    elif [ "$ACTION" = "uninstall" ]; then
        flow_uninstall
    elif is_installed; then
        load_config
        menu_existing
    else
        flow_install
    fi
    ui_drain_input
}

if [ "${BASH_SOURCE[0]:-$0}" = "$0" ]; then
    main "$@"
fi
