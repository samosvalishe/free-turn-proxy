# Быстрый Старт

Для стабильной работы мы сразу настроим **обфускацию трафика**.

## Что Нужно

- VPS с публичным IP, VPN-сервер (например, WireGuard или AmneziaWG) слушает локальный порт (например, `127.0.0.1:51820/udp`).
- Активная ссылка VK Calls: `https://vk.com/call/join/...` — создайте сами (звонок завершать нельзя).
- VPN-клиент на вашем устройстве: `Endpoint = 127.0.0.1:9000` (порт `9000` взят для примера), `MTU = 1280` (крайне важно для компенсации накладных расходов сети).

---

## Шаг 1: Генерация ключа обфускации

Сгенерируйте секретный ключ (64 символа), который замаскирует ваш VPN-трафик под обычный голосовой звонок.

Вам даже не нужно ничего скачивать для этого. В любом терминале Linux/macOS просто выполните:

```bash
openssl rand -hex 32
```

*(Если на сервере нет openssl, используйте: `head -c 32 /dev/urandom | xxd -p -c 32`)*

> Альтернативно, ключ можно сгенерировать через сам бинарник `free-turn-proxy` командой `./server -gen-obf-key`, либо через Docker: `docker run --rm ghcr.io/samosvalishe/free-turn-proxy:latest -gen-obf-key`.

*Скопируйте полученный 64-символьный ключ. Далее в примерах он будет обозначен как `<ВАШ_КЛЮЧ>`.*

---

## Шаг 2: Запуск Сервера на VPS

Мы настоятельно рекомендуем запускать сервер через **Docker**. Это гарантирует его работу в фоне 24/7 после закрытия терминала.

### Вариант А: Запуск через Docker Compose (Рекомендуется)

Если Docker еще не установлен, установите его (команды установки есть в [deploy.md](deploy.md)).
Создайте файл `docker-compose.yml` в папке `/opt/free-turn-proxy`:

```yaml
services:
  vk-turn-proxy:
    image: ghcr.io/samosvalishe/free-turn-proxy:latest
    container_name: free-turn-proxy
    network_mode: "host"
    restart: unless-stopped
    environment:
      - CONNECT_ADDR=127.0.0.1:51820
      - LISTEN_ADDR=0.0.0.0:56000
      - MODE=udp
      - OBF_PROFILE=rtpopus
      - OBF_KEY=<ВАШ_КЛЮЧ>
```
Запустите сервер в фоне командой: `docker compose up -d`

### Вариант Б: Ручной запуск бинарника (для быстрого теста)

Если вы просто хотите протестировать туннель без установки Docker, скачайте бинарник и запустите его напрямую:

```bash
sudo mkdir -p /opt/free-turn-proxy
sudo curl -L -o /opt/free-turn-proxy/server https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/server-linux-amd64
sudo chmod +x /opt/free-turn-proxy/server

/opt/free-turn-proxy/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820 -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ>
```

> **Внимание:** Команда выше запустит сервер только в текущем окне терминала. Для постоянной работы (если вы выбрали Вариант Б) настройте **systemd-службу** по инструкции из [Развёртывания (deploy.md)](deploy.md).

> **Важно про флаг `-connect`:** В параметре `-connect 127.0.0.1:51820` вы должны указать IP и порт, на котором **реально работает ваш VPN-сервер** (WireGuard, AmneziaWG, VLESS и т.д.). `51820` — это стандартный порт WireGuard, но если у вас настроен другой порт, обязательно укажите свой.

> **Примечание:** Внешний порт прослушивания (`56000` в примере) и порт клиента (`9000` далее по тексту) — это примеры. Вы можете использовать любые свободные порты. Убедитесь, что внешний порт (например, `56000/udp`) открыт в файрволе вашего VPS.

---

## Шаг 3: Запуск Клиента и Маршруты (ПК)

Туннель замыкается сам на себя, если VPN (WireGuard) перехватывает весь трафик. Чтобы прокси `free-turn-proxy` мог общаться с TURN-серверами напрямую, нужен скрипт добавления маршрутов-исключений.

**Linux:**
```bash
curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-linux-amd64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<vk-link>" -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ> -debug 2>&1 | ./scripts/routes.sh
```

**Windows (PowerShell от администратора):**
```powershell
Invoke-WebRequest -Uri https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-windows-amd64.exe -OutFile client.exe
.\client.exe -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<vk-link>" -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ> -debug 2>&1 | .\scripts\routes.ps1
```

**macOS:**
```bash
# Для Apple Silicon (M1/M2) скачайте client-darwin-arm64, для Intel — client-darwin-amd64
./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<vk-link>" -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ> -debug 2>&1 | ./scripts/routes-macos.sh
```

*Когда скрипт напечатает `Ensuring route to ...` — включайте ваш VPN-клиент.*

---

## Шаг 4: Мобильные Устройства

На мобильных сетях есть жесткая специфика:
1. **Маршруты не нужны**. Но приложение с клиентом (Termux) **обязательно** нужно добавить в исключения VPN-клиента.
2. **Перехват DNS**. Мобильные операторы блокируют сторонние DNS, поэтому обязательно указывайте DNS оператора связи через `-dns-servers` (узнать его можно в настройках APN телефона).

```bash
termux-wake-lock
# Скачивание: curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-android-arm64 && chmod +x client

# Обязательно укажите ваш ключ и DNS
./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<vk-link>" -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ> -dns-servers <ip_dns_мобильного_оператора>
```

> **Совет:** Транспорт до TURN-реле по умолчанию происходит по протоколу TCP/TLS (это транспорт самого туннеля, внутри него может идти любой UDP-трафик WireGuard). Мы рекомендуем всегда оставлять этот TCP-транспорт по умолчанию, так как многие мобильные провайдеры и системы DPI агрессивно режут голый UDP трафик. Не используйте флаг `-transport udp` без крайней необходимости.
