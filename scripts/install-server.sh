#!/usr/bin/env bash
# Free Turn Proxy Server — интерактивный установщик.
#
# Идемпотентен: повторный запуск обнаруживает существующую установку и
# предлагает обновить версию (сохраняя конфиг), переконфигурировать или удалить.
# Конфигурация установки хранится в $CONF_FILE и переиспользуется при обновлении.

set -e

# --- Константы ---
REPO="samosvalishe/free-turn-proxy"
IMAGE="ghcr.io/${REPO}"
APP_DIR="/opt/free-turn-proxy"
CONF_FILE="${APP_DIR}/install.conf"
SERVICE="free-turn-proxy.service"
UNIT_FILE="/etc/systemd/system/${SERVICE}"
COMPOSE_FILE="${APP_DIR}/docker-compose.yml"

# --- Цвета ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# --- Логирование ---
info() { echo -e "${CYAN}[*]${NC} $1"; }
success() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[x]${NC} $1"; exit 1; }

# --- Переменные конфигурации (дефолты; перекрываются load_config / мастером) ---
INSTALL_METHOD="1"        # 1=docker, 2=systemd
VERSION="latest"
PROXY_MODE="udp"
BACKEND_PORT=""
LISTEN_PORT="56000"
OBF_PROFILE="rtpopus"
OBF_KEY=""
CLIENTS_FILE_CONF=""

# ============================================================================
# Хелперы
# ============================================================================

# ask VAR "Текст вопроса" "дефолт" — читает ответ в переменную VAR.
ask() {
    local __var="$1" __prompt="$2" __def="$3" __ans
    if [ -n "$__def" ]; then
        read -p "$__prompt [$__def]: " __ans
    else
        read -p "$__prompt: " __ans
    fi
    printf -v "$__var" '%s' "${__ans:-$__def}"
}

# yesno "Текст" "Y|N" — true если пользователь согласился.
yesno() {
    local __ans
    read -p "$1 [$([ "$2" = "Y" ] && echo "Y/n" || echo "y/N")]: " __ans
    __ans=${__ans:-$2}
    [[ "$__ans" =~ ^[Yy]$ ]]
}

detect_arch() {
    local arch
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) GOARCH="amd64" ;;
        aarch64|arm64) GOARCH="arm64" ;;
        *) error "Неподдерживаемая архитектура: $arch" ;;
    esac
}

ensure_base_deps() {
    info "Проверка базовых утилит (curl, jq, openssl)..."
    if command -v apt-get >/dev/null; then
        apt-get update -y -qq >/dev/null
        apt-get install -y -qq curl jq openssl >/dev/null
    elif command -v yum >/dev/null; then
        yum install -y -q curl jq openssl >/dev/null
    else
        warn "Пакетный менеджер не определён (apt/yum). Убедитесь, что curl, jq, openssl установлены."
    fi
}

# --- GitHub API ---
gh_latest_version() {
    curl -s "https://api.github.com/repos/${REPO}/releases/latest" | jq -r '.tag_name // empty'
}

gh_recent_versions() {
    curl -s "https://api.github.com/repos/${REPO}/releases?per_page=6" | jq -r '.[].tag_name'
}

# resolve_asset_url <version> <asset-name> — URL ассета релиза (latest или vX.Y.Z).
resolve_asset_url() {
    local ver="$1" asset="$2" api
    if [ "$ver" = "latest" ]; then
        api="https://api.github.com/repos/${REPO}/releases/latest"
    else
        api="https://api.github.com/repos/${REPO}/releases/tags/${ver}"
    fi
    curl -s "$api" | jq -r --arg n "$asset" '.assets[] | select(.name==$n) | .browser_download_url'
}

# Тег Docker-образа: latest -> latest, иначе сам тег.
image_tag() {
    [ "$VERSION" = "latest" ] && echo "latest" || echo "$VERSION"
}

# Интерактивный выбор версии в переменную VERSION.
choose_version() {
    info "Получение списка версий с GitHub..."
    local latest others def="${VERSION:-latest}"
    latest=$(gh_latest_version || true)
    echo ""
    if [ -n "$latest" ]; then
        echo -e "  ${GREEN}latest${NC} (= $latest)"
        others=$(gh_recent_versions 2>/dev/null | grep -vx "$latest" | head -4 || true)
        if [ -n "$others" ]; then
            while IFS= read -r t; do echo "  $t"; done <<< "$others"
        fi
    else
        warn "Не удалось получить список релизов. Введите тег вручную (или latest)."
    fi
    echo ""
    ask VERSION "Версия (latest или vX.Y.Z)" "$def"
}

# --- Конфигурация установки ---
is_installed() {
    [ -f "$CONF_FILE" ] || [ -f "$COMPOSE_FILE" ] || [ -f "$UNIT_FILE" ]
}

load_config() {
    [ -f "$CONF_FILE" ] && . "$CONF_FILE"
}

save_config() {
    mkdir -p "$APP_DIR"
    cat > "$CONF_FILE" <<EOF
# Сгенерировано install-server.sh — переиспользуется при обновлении. Не редактируйте вручную.
INSTALL_METHOD="$INSTALL_METHOD"
VERSION="$VERSION"
PROXY_MODE="$PROXY_MODE"
BACKEND_PORT="$BACKEND_PORT"
LISTEN_PORT="$LISTEN_PORT"
OBF_PROFILE="$OBF_PROFILE"
OBF_KEY="$OBF_KEY"
CLIENTS_FILE_CONF="$CLIENTS_FILE_CONF"
EOF
    chmod 600 "$CONF_FILE"   # содержит OBF_KEY
}

connect_addr() { echo "127.0.0.1:${BACKEND_PORT}"; }

# ============================================================================
# Мастер настройки (install / reconfigure)
# ============================================================================
wizard() {
    echo ""
    info "Выберите метод установки сервера:"
    echo -e "  ${GREEN}1) Docker Compose (Рекомендуется)${NC}"
    echo -e "  2) Systemd (Прямой запуск бинарника)"
    ask INSTALL_METHOD "Ваш выбор [1/2]" "${INSTALL_METHOD:-1}"

    # WireGuard (только подсказка дефолтного порта бэкенда)
    echo ""
    info "Проверка WireGuard..."
    local wg_port=""
    if ! command -v wg >/dev/null; then
        warn "WireGuard не найден."
        if yesno "Установить пакет WireGuard? (только пакет, настройка — вручную)" "Y"; then
            if command -v apt-get >/dev/null; then
                apt-get install -y -qq wireguard >/dev/null
            elif command -v yum >/dev/null; then
                yum install -y -q epel-release >/dev/null
                yum install -y -q wireguard-tools >/dev/null
            fi
            success "WireGuard установлен."
        fi
    else
        success "WireGuard уже установлен."
        if wg show all listen-port >/dev/null 2>&1; then
            wg_port=$(wg show all listen-port | head -n 1 | awk '{print $2}')
        fi
        if [ -z "$wg_port" ] && [ -n "$(ls -A /etc/wireguard/*.conf 2>/dev/null)" ]; then
            wg_port=$(grep -i ListenPort /etc/wireguard/*.conf | head -n 1 | awk -F '=' '{print $2}' | tr -d ' ')
        fi
    fi

    echo ""
    info "Конфигурация Free Turn Proxy"

    ask PROXY_MODE "Режим туннеля (udp — WireGuard/AmneziaWG, tcp — Xray)" "${PROXY_MODE:-udp}"

    # Порт бэкенда: приоритет — текущий конфиг, затем найденный WG, затем дефолт по режиму.
    local def_backend="$BACKEND_PORT"
    if [ -z "$def_backend" ]; then
        if [ -n "$wg_port" ]; then
            def_backend="$wg_port"
        elif [ "$PROXY_MODE" = "udp" ]; then
            def_backend="51820"
        else
            def_backend="443"
        fi
    fi
    ask BACKEND_PORT "Порт вашего VPN / бэкенда" "$def_backend"

    ask LISTEN_PORT "Внешний порт (приём соединений Free Turn Proxy)" "${LISTEN_PORT:-56000}"

    # Обфускация (дефолт ответа = текущее состояние)
    local obf_def="Y"
    [ "$OBF_PROFILE" = "none" ] && obf_def="N"
    if yesno "Включить маскировку (rtpopus)?" "$obf_def"; then
        OBF_PROFILE="rtpopus"
        if [ -n "$OBF_KEY" ]; then
            info "Текущий ключ обфускации сохранён (оставьте пустым, чтобы не менять)."
            ask OBF_KEY "Новый 64-hex ключ (Enter — оставить текущий)" "$OBF_KEY"
        elif yesno "Сгенерировать случайный ключ обфускации?" "Y"; then
            OBF_KEY=$(openssl rand -hex 32)
            success "Ключ сгенерирован: $OBF_KEY"
        else
            ask OBF_KEY "Введите ваш 64-символьный hex ключ" ""
            [ -z "$OBF_KEY" ] && error "Ключ не может быть пустым."
        fi
    else
        OBF_PROFILE="none"
        OBF_KEY=""
    fi

    # Авторизация по Client ID
    local auth_def="N"
    [ -n "$CLIENTS_FILE_CONF" ] && auth_def="Y"
    if yesno "Включить авторизацию по Client ID?" "$auth_def"; then
        CLIENTS_FILE_CONF="${APP_DIR}/clients.json"
    else
        CLIENTS_FILE_CONF=""
    fi

    # Версия
    echo ""
    choose_version

    # Firewall
    echo ""
    info "Настройка брандмауэра..."
    if yesno "Открыть порт ${LISTEN_PORT}/udp в UFW/iptables?" "Y"; then
        firewall_open
    fi
}

firewall_open() {
    if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
        ufw allow "${LISTEN_PORT}/udp" >/dev/null   # ufw сам дедуплицирует правила
        success "Порт открыт через UFW."
    elif command -v iptables >/dev/null; then
        # -C проверяет наличие правила → не плодим дубли при повторном запуске.
        if ! iptables -C INPUT -p udp --dport "$LISTEN_PORT" -j ACCEPT 2>/dev/null; then
            iptables -I INPUT -p udp --dport "$LISTEN_PORT" -j ACCEPT
            command -v netfilter-persistent >/dev/null && netfilter-persistent save >/dev/null 2>&1 || true
        fi
        success "Порт открыт через iptables."
    else
        warn "Брандмауэр (ufw/iptables) не найден — откройте порт ${LISTEN_PORT}/udp вручную."
    fi
}

# ============================================================================
# Применение конфигурации
# ============================================================================
init_clients_file() {
    if [ -n "$CLIENTS_FILE_CONF" ] && [ ! -f "$CLIENTS_FILE_CONF" ]; then
        echo "[]" > "$CLIENTS_FILE_CONF"
        success "Создан пустой список клиентов $CLIENTS_FILE_CONF"
    fi
}

ensure_docker() {
    if command -v docker >/dev/null; then
        return
    fi
    warn "Docker не найден."
    if yesno "Установить Docker сейчас?" "Y"; then
        info "Установка Docker..."
        curl -fsSL https://get.docker.com -o get-docker.sh
        sh get-docker.sh >/dev/null 2>&1
        rm -f get-docker.sh
        success "Docker успешно установлен."
    else
        error "Для этого метода требуется Docker. Отмена."
    fi
}

apply_docker() {
    ensure_docker
    info "Генерация docker-compose.yml..."
    {
        echo "services:"
        echo "  free-turn-proxy:"
        echo "    image: ${IMAGE}:$(image_tag)"
        echo "    container_name: free-turn-proxy"
        echo "    network_mode: \"host\""
        echo "    restart: unless-stopped"
        echo "    environment:"
        echo "      - CONNECT_ADDR=$(connect_addr)"
        echo "      - LISTEN_ADDR=0.0.0.0:${LISTEN_PORT}"
        echo "      - MODE=${PROXY_MODE}"
        echo "      - OBF_PROFILE=${OBF_PROFILE}"
        echo "      - OBF_KEY=${OBF_KEY}"
        if [ -n "$CLIENTS_FILE_CONF" ]; then
            echo "      - CLIENTS_FILE=${CLIENTS_FILE_CONF}"
            echo "    volumes:"
            echo "      - ${CLIENTS_FILE_CONF}:${CLIENTS_FILE_CONF}"
        fi
    } > "$COMPOSE_FILE"

    cd "$APP_DIR"
    info "Загрузка образа ${IMAGE}:$(image_tag)..."
    docker compose pull
    info "Запуск контейнера..."
    docker compose up -d
}

download_binary() {
    info "Поиск бинарника server-linux-${GOARCH} (версия: ${VERSION})..."
    local url
    url=$(resolve_asset_url "$VERSION" "server-linux-${GOARCH}")
    [ -z "$url" ] && error "Не удалось найти server-linux-${GOARCH} в релизе ${VERSION}."
    info "Скачивание..."
    # Скачиваем во временный файл и атомарно подменяем — рабочий процесс не падает.
    if ! curl -fL -o "${APP_DIR}/server.new" "$url" >/dev/null 2>&1; then
        error "Не удалось скачать бинарник по адресу $url"
    fi
    chmod +x "${APP_DIR}/server.new"
    mv -f "${APP_DIR}/server.new" "${APP_DIR}/server"
}

apply_systemd() {
    download_binary

    info "Генерация systemd-сервиса..."
    local args="-listen 0.0.0.0:${LISTEN_PORT} -connect $(connect_addr) -mode ${PROXY_MODE}"
    [ "$OBF_PROFILE" != "none" ] && args="$args -obf-profile ${OBF_PROFILE} -obf-key ${OBF_KEY}"
    [ -n "$CLIENTS_FILE_CONF" ] && args="$args -clients-file ${CLIENTS_FILE_CONF}"

    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Free TURN Proxy Server
After=network.target

[Service]
Type=simple
ExecStart=${APP_DIR}/server ${args}
Restart=always
RestartSec=5
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE" >/dev/null 2>&1 || true
    systemctl restart "$SERVICE"   # restart идемпотентен: стартует если стоит, перезапускает если работает
}

apply() {
    mkdir -p "$APP_DIR"
    init_clients_file
    if [ "$INSTALL_METHOD" = "1" ]; then
        apply_docker
    else
        apply_systemd
    fi
    save_config
}

# ============================================================================
# Итоговая сводка
# ============================================================================
print_summary() {
    echo ""
    success "Готово!"
    echo "--------------------------------------------------------"
    echo -e "${CYAN}Конфигурация для подключения (Client):${NC}"
    local ext_ip
    ext_ip=$(curl -s ifconfig.me || echo "IP_ВАШЕГО_СЕРВЕРА")
    echo "  Сервер (peer): ${ext_ip}:${LISTEN_PORT}"
    if [ "$OBF_PROFILE" != "none" ]; then
        echo "  Профиль обфускации: $OBF_PROFILE"
        echo "  Ключ обфускации: $OBF_KEY"
    fi
    echo "  Режим: $PROXY_MODE"
    echo "  Версия: $VERSION"

    if [ -n "$CLIENTS_FILE_CONF" ]; then
        echo ""
        echo -e "${YELLOW}Внимание: включена авторизация по Client ID.${NC}"
        echo "Добавьте клиента на сервере, иначе он не подключится:"
        if [ "$INSTALL_METHOD" = "1" ]; then
            echo "  Добавить: docker exec -it free-turn-proxy /app/server clients add <id_клиента>"
            echo "  Список:   docker exec -it free-turn-proxy /app/server clients list"
        else
            echo "  Добавить: ${APP_DIR}/server -clients-file ${CLIENTS_FILE_CONF} clients add <id_клиента>"
            echo "  Список:   ${APP_DIR}/server -clients-file ${CLIENTS_FILE_CONF} clients list"
        fi
    fi
    echo "--------------------------------------------------------"
    echo "Повторный запуск скрипта: обновление версии / переконфигурация / удаление."
    echo -e "Документация: https://github.com/${REPO}"
}

# ============================================================================
# Сценарии
# ============================================================================
flow_install() {
    info "Новая установка Free Turn Proxy Server."
    wizard
    echo ""
    info "Начинаем установку..."
    apply
    print_summary
}

flow_update() {
    if [ ! -f "$CONF_FILE" ]; then
        warn "Файл конфигурации не найден ($CONF_FILE) — нужна переконфигурация."
        flow_reconfigure
        return
    fi
    info "Обновление. Текущая версия: ${VERSION}, метод: $([ "$INSTALL_METHOD" = "1" ] && echo docker || echo systemd)."
    choose_version
    echo ""
    info "Применяем версию ${VERSION}..."
    apply
    print_summary
}

flow_reconfigure() {
    info "Переконфигурация (текущие значения подставлены как дефолты)."
    wizard
    echo ""
    info "Применяем изменения..."
    apply
    print_summary
}

flow_uninstall() {
    echo ""
    warn "Удаление Free Turn Proxy Server."
    if ! yesno "Продолжить удаление?" "N"; then
        info "Отменено."
        return
    fi
    if [ "$INSTALL_METHOD" = "1" ]; then
        if [ -f "$COMPOSE_FILE" ]; then
            ( cd "$APP_DIR" && docker compose down ) || warn "docker compose down завершился с ошибкой."
        fi
    else
        systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
        rm -f "$UNIT_FILE"
        systemctl daemon-reload
    fi
    success "Служба остановлена и удалена."
    if yesno "Удалить каталог ${APP_DIR} (ключи, clients.json, конфиг)?" "N"; then
        rm -rf "$APP_DIR"
        success "Каталог удалён."
    else
        info "Каталог сохранён: $APP_DIR"
    fi
}

menu_existing() {
    local method_label
    method_label=$([ "$INSTALL_METHOD" = "1" ] && echo docker || echo systemd)
    echo ""
    info "Обнаружена установка (метод: ${method_label}, версия: ${VERSION:-?})."
    echo "  1) Обновить версию (конфиг сохранён)"
    echo "  2) Переконфигурировать"
    echo "  3) Удалить"
    echo "  4) Выход"
    local choice
    ask choice "Ваш выбор [1-4]" "1"
    case "$choice" in
        1) flow_update ;;
        2) flow_reconfigure ;;
        3) flow_uninstall ;;
        4) info "Выход."; exit 0 ;;
        *) error "Неизвестный выбор: $choice" ;;
    esac
}

# ============================================================================
# main
# ============================================================================
main() {
    [ "$EUID" -ne 0 ] && error "Запустите скрипт от root (sudo bash install-server.sh)"

    echo -e "${CYAN}"
    cat << "EOF"
    ______               _____                 ____
   / ____/_______  ___  /_  __/_  ___________ / __ \_________  _  ____  __
  / /_  / ___/ _ \/ _ \  / / / / / / ___/ __ \/ /_/ / ___/ __ \| |/_/ / / /
 / __/ / /  /  __/  __/ / / / /_/ / /  / / / / ____/ /  / /_/ />  </ /_/ /
/_/   /_/   \___/\___/ /_/  \__,_/_/  /_/ /_/_/   /_/   \____/_/|_|\__, /
                                                                  /____/
EOF
    echo -e "${NC}"

    ensure_base_deps
    detect_arch

    if is_installed; then
        load_config
        menu_existing
    else
        flow_install
    fi
}

main "$@"
