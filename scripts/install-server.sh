#!/usr/bin/env bash
# Free Turn Proxy Server Installation Script

set -e

# --- Цвета ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# --- Функции логирования ---
info() { echo -e "${CYAN}[*]${NC} $1"; }
success() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[x]${NC} $1"; exit 1; }

# --- Проверки ---
if [ "$EUID" -ne 0 ]; then
  error "Пожалуйста, запустите скрипт от имени root (sudo bash install-server.sh)"
fi

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
info "Вас приветствует интерактивный установщик Free Turn Proxy Server!"
echo ""

# Установка базовых утилит
info "Проверка базовых утилит..."
if command -v apt-get >/dev/null; then
    apt-get update -y -qq >/dev/null
    apt-get install -y -qq curl jq openssl >/dev/null
elif command -v yum >/dev/null; then
    yum install -y -q curl jq openssl >/dev/null
else
    warn "Не удалось определить пакетный менеджер (apt/yum). Убедитесь, что curl и jq установлены."
fi
success "Утилиты проверены."

# Определение архитектуры
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) GOARCH="amd64" ;;
    aarch64|arm64) GOARCH="arm64" ;;
    *) error "Неподдерживаемая архитектура: $ARCH" ;;
esac

# Шаг 1. Метод установки (Docker vs Systemd)
echo ""
info "Выберите метод установки сервера:"
echo -e "  ${GREEN}1) Docker Compose (Рекомендуется)${NC}"
echo -e "  2) Systemd (Прямой запуск бинарника)"
read -p "Ваш выбор [1/2] (по умолчанию 1): " INSTALL_METHOD
INSTALL_METHOD=${INSTALL_METHOD:-1}

if [ "$INSTALL_METHOD" == "1" ]; then
    if ! command -v docker >/dev/null; then
        warn "Docker не найден."
        read -p "Установить Docker сейчас? [Y/n]: " INSTALL_DOCKER
        INSTALL_DOCKER=${INSTALL_DOCKER:-Y}
        if [[ "$INSTALL_DOCKER" =~ ^[Yy]$ ]]; then
            info "Установка Docker..."
            curl -fsSL https://get.docker.com -o get-docker.sh
            sh get-docker.sh >/dev/null 2>&1
            rm -f get-docker.sh
            success "Docker успешно установлен."
        else
            error "Для этого метода требуется Docker. Отмена."
        fi
    fi
fi

# Шаг 2. WireGuard
echo ""
info "Проверка WireGuard..."
WG_PORT=""
if ! command -v wg >/dev/null; then
    warn "WireGuard не найден."
    read -p "Установить пакет WireGuard? (Скрипт установит только пакет, настраивать нужно будет вручную) [Y/n]: " INSTALL_WG
    INSTALL_WG=${INSTALL_WG:-Y}
    if [[ "$INSTALL_WG" =~ ^[Yy]$ ]]; then
        info "Установка WireGuard..."
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
    # Пытаемся найти порт
    if wg show all listen-port >/dev/null 2>&1; then
        WG_PORT=$(wg show all listen-port | head -n 1 | awk '{print $2}')
    fi
    if [ -z "$WG_PORT" ]; then
        if [ -n "$(ls -A /etc/wireguard/*.conf 2>/dev/null)" ]; then
            WG_PORT=$(grep -i ListenPort /etc/wireguard/*.conf | head -n 1 | awk -F '=' '{print $2}' | tr -d ' ')
        fi
    fi
fi

# Шаг 3. Конфигурация прокси
echo ""
info "Конфигурация Free Turn Proxy"

# Режим
read -p "Режим работы туннеля (udp - для WireGuard/AmneziaWG, tcp - для Xray) [udp]: " PROXY_MODE
PROXY_MODE=${PROXY_MODE:-udp}

# Порт бэкенда
if [ -n "$WG_PORT" ]; then
    read -p "Порт вашего VPN / бэкенда (найден WireGuard на порту $WG_PORT) [$WG_PORT]: " BACKEND_PORT
    BACKEND_PORT=${BACKEND_PORT:-$WG_PORT}
else
    if [ "$PROXY_MODE" == "udp" ]; then
        DEF_B="51820"
    else
        DEF_B="443"
    fi
    read -p "Порт вашего VPN / бэкенда (например, $DEF_B) [$DEF_B]: " BACKEND_PORT
    BACKEND_PORT=${BACKEND_PORT:-$DEF_B}
fi
CONNECT_ADDR="127.0.0.1:$BACKEND_PORT"

# Внешний порт
read -p "Внешний порт (на котором Free Turn Proxy будет принимать соединения) [56000]: " LISTEN_PORT
LISTEN_PORT=${LISTEN_PORT:-56000}

# Обфускация
read -p "Включить маскировку (rtpopus)? [Y/n]: " USE_OBF
USE_OBF=${USE_OBF:-Y}
OBF_PROFILE="none"
OBF_KEY=""

if [[ "$USE_OBF" =~ ^[Yy]$ ]]; then
    OBF_PROFILE="rtpopus"
    read -p "Сгенерировать случайный ключ обфускации? [Y/n]: " GEN_KEY
    GEN_KEY=${GEN_KEY:-Y}
    if [[ "$GEN_KEY" =~ ^[Yy]$ ]]; then
        OBF_KEY=$(openssl rand -hex 32)
        success "Ключ сгенерирован: $OBF_KEY"
    else
        read -p "Введите ваш 64-символьный hex ключ: " OBF_KEY
        if [ -z "$OBF_KEY" ]; then
            error "Ключ не может быть пустым."
        fi
    fi
fi

# Авторизация по Client ID
read -p "Включить авторизацию по Client ID? (Потребуется создать клиентов) [y/N]: " USE_AUTH
USE_AUTH=${USE_AUTH:-N}
CLIENTS_FILE_CONF=""
if [[ "$USE_AUTH" =~ ^[Yy]$ ]]; then
    CLIENTS_FILE_CONF="/opt/free-turn-proxy/clients.json"
fi

# Шаг 4. Настройка Firewall (UFW / iptables)
echo ""
info "Настройка брандмауэра..."
read -p "Открыть порт $LISTEN_PORT/udp в UFW/iptables? [Y/n]: " CONF_FW
CONF_FW=${CONF_FW:-Y}
if [[ "$CONF_FW" =~ ^[Yy]$ ]]; then
    if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
        ufw allow $LISTEN_PORT/udp >/dev/null
        success "Порт открыт через UFW."
    elif command -v iptables >/dev/null; then
        iptables -I INPUT -p udp --dport $LISTEN_PORT -j ACCEPT
        if command -v netfilter-persistent >/dev/null; then
            netfilter-persistent save >/dev/null 2>&1
        fi
        success "Порт открыт через iptables."
    else
        warn "Брандмауэр (ufw/iptables) не найден, пожалуйста, откройте порт $LISTEN_PORT/udp вручную."
    fi
fi

# Шаг 5. Установка
echo ""
info "Начинаем установку..."

mkdir -p /opt/free-turn-proxy

# Инициализация clients.json если нужно
if [ -n "$CLIENTS_FILE_CONF" ]; then
    if [ ! -f "$CLIENTS_FILE_CONF" ]; then
        echo "[]" > "$CLIENTS_FILE_CONF"
        success "Создан пустой список клиентов $CLIENTS_FILE_CONF"
    fi
fi

if [ "$INSTALL_METHOD" == "1" ]; then
    # DOCKER INSTALL
    info "Генерация docker-compose.yml..."
    cat > /opt/free-turn-proxy/docker-compose.yml <<EOF
services:
  free-turn-proxy:
    image: ghcr.io/samosvalishe/free-turn-proxy:latest
    container_name: free-turn-proxy
    network_mode: "host"
    restart: unless-stopped
    environment:
      - CONNECT_ADDR=$CONNECT_ADDR
      - LISTEN_ADDR=0.0.0.0:$LISTEN_PORT
      - MODE=$PROXY_MODE
      - OBF_PROFILE=$OBF_PROFILE
      - OBF_KEY=$OBF_KEY
EOF
    if [ -n "$CLIENTS_FILE_CONF" ]; then
        echo "      - CLIENTS_FILE=$CLIENTS_FILE_CONF" >> /opt/free-turn-proxy/docker-compose.yml
        echo "    volumes:" >> /opt/free-turn-proxy/docker-compose.yml
        echo "      - $CLIENTS_FILE_CONF:$CLIENTS_FILE_CONF" >> /opt/free-turn-proxy/docker-compose.yml
    fi

    cd /opt/free-turn-proxy
    info "Запуск контейнера..."
    docker compose up -d
else
    # SYSTEMD INSTALL
    info "Скачивание последнего релиза (server-linux-$GOARCH)..."
    LATEST_URL=$(curl -s https://api.github.com/repos/samosvalishe/free-turn-proxy/releases/latest | jq -r ".assets[] | select(.name == \"server-linux-$GOARCH\") | .browser_download_url")
    if [ -z "$LATEST_URL" ]; then
        error "Не удалось найти бинарник server-linux-$GOARCH в последнем релизе."
    fi
    if ! curl -fL -o /opt/free-turn-proxy/server "$LATEST_URL" >/dev/null 2>&1; then
        error "Не удалось скачать бинарник по адресу $LATEST_URL"
    fi
    chmod +x /opt/free-turn-proxy/server

    info "Генерация systemd сервиса..."
    CMD_ARGS="-listen 0.0.0.0:$LISTEN_PORT -connect $CONNECT_ADDR -mode $PROXY_MODE"
    if [ "$OBF_PROFILE" != "none" ]; then
        CMD_ARGS="$CMD_ARGS -obf-profile $OBF_PROFILE -obf-key $OBF_KEY"
    fi
    if [ -n "$CLIENTS_FILE_CONF" ]; then
        CMD_ARGS="$CMD_ARGS -clients-file $CLIENTS_FILE_CONF"
    fi

    cat > /etc/systemd/system/free-turn-proxy.service <<EOF
[Unit]
Description=Free TURN Proxy Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/free-turn-proxy/server $CMD_ARGS
Restart=always
RestartSec=5
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now free-turn-proxy.service >/dev/null 2>&1
fi

echo ""
success "Установка успешно завершена!"
echo "--------------------------------------------------------"
echo -e "${CYAN}Конфигурация для подключения (Client):${NC}"

EXT_IP=$(curl -s ifconfig.me || echo "IP_ВАШЕГО_СЕРВЕРА")
echo "  Сервер (peer): $EXT_IP:$LISTEN_PORT"
if [ "$OBF_PROFILE" != "none" ]; then
    echo "  Профиль обфускации: $OBF_PROFILE"
    echo "  Ключ обфускации: $OBF_KEY"
fi
echo "  Режим: $PROXY_MODE"

if [ -n "$CLIENTS_FILE_CONF" ]; then
    echo ""
    echo -e "${YELLOW}Внимание: Включена авторизация по Client ID.${NC}"
    echo "Чтобы клиент мог подключиться, вам нужно добавить его на сервере."
    if [ "$INSTALL_METHOD" == "1" ]; then
        echo "Управление клиентами (через Docker):"
        echo "  Добавить: docker exec -it free-turn-proxy /app/server clients add <ваш_id_клиента>"
        echo "  Список:   docker exec -it free-turn-proxy /app/server clients list"
    else
        echo "Управление клиентами (systemd):"
        echo "  Добавить: /opt/free-turn-proxy/server -clients-file $CLIENTS_FILE_CONF clients add <ваш_id_клиента>"
        echo "  Список:   /opt/free-turn-proxy/server -clients-file $CLIENTS_FILE_CONF clients list"
    fi
fi
echo "--------------------------------------------------------"
echo -e "Подробная документация: https://github.com/samosvalishe/free-turn-proxy"
