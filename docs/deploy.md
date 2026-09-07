# Развёртывание сервера

Для работы сервера 24/7 его необходимо настроить как службу. Мы поддерживаем установку через скрипт (интерактивно или автоматически), а также ручную настройку через **Docker** или **systemd**.

---

## Автоматическая установка (Рекомендуется)

Проще всего использовать официальный скрипт. Он автоматически установит зависимости, настроит **AmneziaWG (AWG 3.1)** или WireGuard, поднимет контейнеры через Docker Compose, сгенерирует ключи обфускации, создаст первого клиента и покажет **QR-код** для мгновенного импорта в приложение AmneziaWG.

**Интерактивный мастер** (задаст вопросы в терминале):
```bash
curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/master/scripts/install.sh | sudo bash
```
> Скрипт идемпотентен: при повторном запуске он предлагает интерактивное меню: управление клиентами (добавить, список, QR-коды), обновление версии, переконфигурация или удаление.

**Управление клиентами и сервером:**
После установки скрипт доступен глобально через команду `freeturn`:
```bash
# Добавить нового клиента (сгенерирует ключи AWG, Client ID и выведет QR-код)
freeturn client add phone

# Показать список существующих клиентов
freeturn client list

# Показать QR-код конкретного клиента (direct / relay / freeturn)
freeturn client qr phone direct

# Удалить клиента
freeturn client remove phone

# Открыть интерактивное меню управления сервером
freeturn
```

**Неинтерактивный режим** (для автоматизации / CI):
```bash
# Установка через Docker с AmneziaWG бэкендом (AWG 3.1, порт 51820)
curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/master/scripts/install.sh | \
  sudo bash -s -- -y --backend awg --backend-port 51820 --obf rtpopus3

# Обновление до конкретной версии
freeturn -y --update --version v1.2.3

# Полное удаление
freeturn -y --uninstall --purge
```
*Все доступные флаги:* `freeturn --help`

---

## Ручная установка

Если вы предпочитаете контролировать каждый шаг, используйте инструкции ниже.
Сначала сгенерируйте 64-символьный hex-ключ обфускации (он должен совпадать на сервере и клиенте):
```bash
openssl rand -hex 32
```
*В примерах ниже он обозначен как `<ВАШ_КЛЮЧ>`.*

### Способ 1: Docker Compose (free-turn-proxy + freeturn-awg)

1. Установите Docker:
   ```bash
   curl -fsSL https://get.docker.com | sudo sh
   ```
2. Создайте директорию и `docker-compose.yml`:
   ```bash
   mkdir -p /opt/free-turn-proxy/awg /opt/free-turn-proxy/clients && cd /opt/free-turn-proxy
   nano docker-compose.yml
   ```
3. Вставьте конфигурацию (совместный запуск прокси и AmneziaWG бэкенда):
   ```yaml
   services:
     free-turn-proxy:
       image: ghcr.io/samosvalishe/free-turn-proxy:latest
       container_name: free-turn-proxy
       network_mode: "host" # Важно для прямого доступа к локальному бэкенду (127.0.0.1)
       restart: unless-stopped
       environment:
         - CONNECT_ADDR=127.0.0.1:51820  # Порт AmneziaWG
         - LISTEN_ADDR=0.0.0.0:56000     # Внешний порт приёма
         - MODE=udp                      # udp (WG/AmneziaWG) или tcp (Xray/VLESS)
         - OBF_PROFILE=rtpopus3          # Рекомендуемая маскировка (RTP/opus + RFC 8285 + ChaCha20)
         - OBF_KEY=<ВАШ_КЛЮЧ>            # 64-hex ключ (openssl rand -hex 32)
         # - CLIENTS_FILE=/opt/free-turn-proxy/clients/clients.json # Для авторизации
       # volumes:
       #   - /opt/free-turn-proxy/clients:/opt/free-turn-proxy/clients

     # Опционально: VPN-бэкенд AmneziaWG (AWG 3.1)
     freeturn-awg:
       image: ghcr.io/samosvalishe/freeturn-awg:latest
       container_name: freeturn-awg
       network_mode: "host"
       environment:
         - AWG_IFACE=ftawg0
         - AWG_LOG_LEVEL=verbose
       cap_add:
         - NET_ADMIN
       devices:
         - /dev/net/tun
       restart: unless-stopped
       volumes:
         - /opt/free-turn-proxy/awg/ftawg0.conf:/etc/awg/ftawg0.conf:ro
   ```
4. Включите IP forwarding на хосте:
   ```bash
   echo 'net.ipv4.ip_forward = 1' | sudo tee /etc/sysctl.d/99-free-turn-proxy.conf
   sudo sysctl -p /etc/sysctl.d/99-free-turn-proxy.conf
   ```
5. Запустите: `docker compose up -d`

### Способ 2: systemd (Без Docker)

1. Скачайте бинарник:
   ```bash
   sudo mkdir -p /opt/free-turn-proxy
   sudo curl -L -o /opt/free-turn-proxy/server https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/server-linux-amd64
   sudo chmod +x /opt/free-turn-proxy/server
   ```
   *(Для ARM замените `-amd64` на `-arm64`)*
2. Создайте службу: `sudo nano /etc/systemd/system/free-turn-proxy.service`
3. Вставьте конфигурацию:
   ```ini
   [Unit]
   Description=Free TURN Proxy Server
   After=network.target

   [Service]
   Type=simple
   ExecStart=/opt/free-turn-proxy/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820 -obf-profile rtpopus3 -obf-key <ВАШ_КЛЮЧ>
   Restart=always
   RestartSec=5
   User=nobody
   Group=nogroup

   [Install]
   WantedBy=multi-user.target
   ```
4. Запустите: `sudo systemctl daemon-reload && sudo systemctl enable --now free-turn-proxy.service`

---

## AWG-бэкенд в Docker (AmneziaWG 3.1)

Образ `freeturn-awg` - VPN-бэкенд на базе `amneziawg-go v3.1` и `amneziawg-tools`: поднимает интерфейс AmneziaWG из конфига и раздаёт IPv4 NAT наружу.

### Параметры обфускации AmneziaWG 3.1

В `awg0.conf` задаются расширенные параметры маскировки:
```ini
[Interface]
Address = 10.13.13.1/24
ListenPort = 51820
PrivateKey = <приватный_ключ_сервера>
# AmneziaWG 3.1 обфускация
Jc = 5
Jmin = 10
Jmax = 50
S1 = 68
S2 = 114
S3 = 24
S4 = 12
H1 = 1
H2 = 2
H3 = 3
H4 = 4
HeaderProtectionKey = <32_байта_base64>
ContentPaddingAddition = 10
RekeyAfterTime = 110
RekeyTimeout = 5
RejectAfterTime = 160
KeepaliveTimeout = 10
MaxHandshakeAttempts = 15
RandomTrailers = on
DisableCookies = on
```

- **Прямой туннель (Direct AWG):** откройте порт `51820/udp` в файрволе, чтобы клиенты AmneziaWG могли подключаться напрямую к серверу без задержек релея.
- **Горячая синхронизация пиров:** добавьте `[Peer]` в `ftawg0.conf` и выполните `docker exec freeturn-awg ft-awg-start sync`. Существующие соединения не прерываются!

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `AWG_CONF` | `/etc/awg/ftawg0.conf` | Путь к конфигу в контейнере |
| `AWG_IFACE` | `ftawg0` | Имя интерфейса |
| `AWG_LOG_LEVEL` | `error` | Логи демона: `error` \| `verbose` \| `silent` |

NAT настраивается только для IPv4 (контейнер использует network_mode: host и добавляет правила iptables в стек хоста).

---

## Настройка Файрвола

Откройте внешний порт (сервер слушает по **UDP**, даже если `MODE=tcp`):
```bash
sudo ufw allow 56000/udp
# Или iptables: sudo iptables -I INPUT -p udp --dport 56000 -j ACCEPT
```

---

## Авторизация по Client ID (Опционально)

По умолчанию доступ открыт всем, кто знает `-obf-key`. Чтобы ограничить доступ:

1. Создайте пустой список: `echo '{"clients":{}}' | sudo tee /opt/free-turn-proxy/clients/clients.json`
2. Включите авторизацию:
   - **Docker:** раскомментируйте `CLIENTS_FILE` и `volumes` в `docker-compose.yml`, затем `docker compose up -d`.
   - **systemd:** добавьте флаг `-clients-file /opt/free-turn-proxy/clients/clients.json` в `ExecStart` и перезапустите службу.
3. Добавляйте клиентов:
   - Через установщик:
     ```bash
     sudo bash install.sh client add <имя>
     ```
   - Напрямую через бинарник:
     - **Docker:** `docker exec -i free-turn-proxy env CLIENTS_FILE=/opt/free-turn-proxy/clients/clients.json /app/server clients add <id> [комментарий]`
     - **systemd:** `CLIENTS_FILE=/opt/free-turn-proxy/clients/clients.json /opt/free-turn-proxy/server clients add <id> [комментарий]`
   
   На клиенте используйте флаг `-client-id <id>` или готовую ссылку `freeturn://`.

---

## Переменные окружения Docker

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `CONNECT_ADDR` | **обязательна** | IP и порт вашего VPN (бэкенда) |
| `LISTEN_ADDR` | `0.0.0.0:56000` | Внешний адрес прослушивания |
| `MODE` | `udp` | Режим туннеля: `udp` \| `tcp`; должен совпадать с клиентом |
| `KCP_*` | из дефолта | Тюнинг ARQ при `MODE=tcp`: `KCP_NODELAY`, `KCP_INTERVAL`, `KCP_RESEND`, `KCP_NC`, `KCP_SNDWND`, `KCP_RCVWND`, `KCP_MTU`, `KCP_ACKNODELAY` |
| `OBF_PROFILE` | `rtpopus3` | Маскировка: `none` \| `rtpopus` \| `rtpopus2` \| `rtpopus3` |
| `OBF_KEY` | пусто | Ключ маскировки (64 hex-символа) |
| `CLIENTS_FILE`| пусто | Путь к JSON-файлу авторизации |
| `DEBUG` | `false` | Включить debug-логи |

> **Внимание при Bridge Mode:** Если вы уберете `network_mode: "host"` и пробросите порты (`-p 56000:56000/udp`), `CONNECT_ADDR=127.0.0.1:51820` будет указывать внутрь контейнера. В этом случае прокси не найдет VPN. Используйте IP хоста или оставляйте `network_mode: "host"`.

