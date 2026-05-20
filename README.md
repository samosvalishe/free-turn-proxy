# VK TURN Proxy

VK TURN Proxy - клиент и сервер для прокидывания локального UDP/TCP-трафика через TURN-реле, получаемые из ссылки на VK Calls. Типичный сценарий - поднять небольшой `server` на VPS рядом с WireGuard или Xray, а на клиентском устройстве запустить `client`, который слушает локальный адрес вроде `127.0.0.1:9000`.

> [!CAUTION]
> Проект предназначен для обучения, исследований и администрирования собственных стендов. Используйте его только там, где у вас есть право запускать такой трафик и менять сетевую конфигурацию.

## Содержание

- [Как это работает](#как-это-работает)
- [Возможности](#возможности)
- [Что нужно](#что-нужно)
- [Быстрый старт: WireGuard](#быстрый-старт-wireguard)
  - [Запуск сервера на VPS](#1-запустите-сервер-на-vps)
  - [Настройка WireGuard](#2-настройте-wireguard-на-клиенте)
  - [Запуск клиента](#3-запустите-клиент)
- [Android через Termux](#android-через-termux)
- [iOS через iSH](#ios-через-ish)
- [systemd-сервис](#сервер-как-systemd-сервис)
- [Docker](#docker)
- [VLESS / Xray](#vless--xray)
- [WRAP-режим](#wrap-режим)
- [Яндекс Телемост](#яндекс-телемост)
- [Флаги клиента](#флаги-клиента)
- [Флаги сервера](#флаги-сервера)
- [Captcha](#captcha)
- [Сборка из исходников](#сборка-из-исходников)
- [Решение проблем](#решение-проблем)
- [Похожие проекты](#похожие-проекты)
- [Лицензия](#лицензия)

## Как Это Работает

Схема для WireGuard:

```text
WireGuard client -> 127.0.0.1:9000 -> VK TURN Proxy client
  -> VK TURN relay -> VK TURN Proxy server на VPS
  -> 127.0.0.1:<порт WireGuard> -> WireGuard server
```

Клиент берет временные TURN-учетные данные из ссылки VK Calls, открывает одно или несколько соединений к TURN-реле и отправляет через них трафик к вашему `server`. Между `client` и `server` используется DTLS. Для WireGuard сервер пересылает данные в UDP backend, для VLESS/Xray - в TCP backend через KCP и smux.

## Возможности

- VK Calls как основной источник TURN-учетных данных.
- TCP или UDP подключение клиента к TURN-реле.
- Несколько параллельных TURN-потоков через `-n`.
- WireGuard/Hysteria-подобный UDP backend.
- VLESS/Xray TCP backend через `-vless`.
- Bonding для VLESS через `-vless-bond`.
- Дополнительная WRAP-обфускация DTLS-пакетов через `-wrap`.
- Автоматическое и ручное прохождение VK captcha.
- Docker-образ для серверной части.

## Что Нужно

- VPS с публичным IP.
- На VPS уже должен слушать backend:
  - WireGuard: обычно `127.0.0.1:51820/udp`;
  - Xray/VLESS: обычно `127.0.0.1:443/tcp`.
- Ссылка на активный VK Calls вида `https://vk.com/call/join/...`.
- На клиенте: WireGuard, Xray или другой локальный клиент, который будет ходить в `127.0.0.1:9000`.

Ссылку VK Calls лучше создать самостоятельно. Не завершайте звонок для всех, если хотите использовать эту ссылку дальше.

## Быстрый Старт: WireGuard

### 1. Запустите Сервер На VPS

Скачайте бинарник для Linux amd64:

```bash
curl -L -o server https://github.com/cacggghp/vk-turn-proxy/releases/latest/download/server-linux-amd64
chmod +x server
```

Запустите `server`, указав локальный адрес WireGuard:

```bash
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820
```

Порт `56000/udp` должен быть доступен снаружи. Если WireGuard слушает другой порт, замените `51820`.

### 2. Настройте WireGuard На Клиенте

В клиентском конфиге WireGuard замените endpoint сервера на локальный адрес VK TURN Proxy:

```ini
Endpoint = 127.0.0.1:9000
MTU = 1280
```

На Android добавьте Termux или приложение-клиент в исключения WireGuard. На Windows, Linux и macOS перед включением WireGuard нужно добавить маршрут до TURN-реле, иначе клиент может попытаться подключаться к TURN уже через сам VPN.

### 3. Запустите Клиент

Linux:

```bash
curl -L -o client https://github.com/cacggghp/vk-turn-proxy/releases/latest/download/client-linux-amd64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>" | ./routes.sh
```

Windows PowerShell от администратора:

```powershell
Invoke-WebRequest -Uri https://github.com/cacggghp/vk-turn-proxy/releases/latest/download/client-windows-amd64.exe -OutFile client.exe
.\client.exe -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>" | .\routes.ps1
```

macOS:

```bash
curl -L -o client https://github.com/cacggghp/vk-turn-proxy/releases/latest/download/client-darwin-arm64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>" | ./routes-macos.sh
```

После появления соединения включите WireGuard.

Если вы скачали только бинарник, но не клонировали репозиторий, возьмите нужный route-скрипт из этого репозитория: `routes.sh`, `routes.ps1` или `routes-macos.sh`.

## Android Через Termux

1. Установите Termux из F-Droid.
2. В WireGuard укажите `Endpoint = 127.0.0.1:9000` и `MTU = 1280`.
3. Добавьте Termux в исключения WireGuard.
4. Запустите в Termux:

```bash
termux-wake-lock
curl -L -o client https://github.com/cacggghp/vk-turn-proxy/releases/latest/download/client-android-arm64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>"
```

Чтобы снять wake lock:

```bash
termux-wake-unlock
```

## iOS Через iSH

Это запасной вариант, если нет нативного клиента.

```bash
apk update
apk add curl
curl -L -o client https://github.com/cacggghp/vk-turn-proxy/releases/latest/download/client-linux-386
chmod +x client
GOMAXPROCS=1 GODEBUG=asyncpreemptoff=1 ./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>"
```

Чтобы iSH дольше жил в фоне, можно в начале сессии выполнить:

```bash
cat /dev/location > /dev/null &
```

## Сервер Как systemd-Сервис

Пример `/etc/systemd/system/vk-turn-proxy.service`:

```ini
[Unit]
Description=VK TURN Proxy server
After=network.target

[Service]
Type=simple
ExecStart=/opt/vk-turn-proxy/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820
Restart=always
RestartSec=5
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
```

Применить:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now vk-turn-proxy.service
sudo systemctl status vk-turn-proxy.service
```

## Docker

Образ публикуется в GitHub Container Registry:

```bash
docker pull ghcr.io/cacggghp/vk-turn-proxy:latest
```

Если backend слушает на хосте, удобнее использовать host network:

```bash
docker run --rm --network host \
  -e CONNECT_ADDR=127.0.0.1:51820 \
  ghcr.io/cacggghp/vk-turn-proxy:latest
```

Bridge mode:

```bash
docker run --rm -p 56000:56000/udp \
  -e CONNECT_ADDR=<host-ip>:51820 \
  ghcr.io/cacggghp/vk-turn-proxy:latest
```

Переменные окружения:

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `CONNECT_ADDR` | обязательна | backend, куда сервер пересылает трафик |
| `LISTEN_ADDR` | `0.0.0.0:56000` | адрес прослушивания сервера |
| `VLESS_MODE` | `false` | включает `-vless` |
| `VLESS_BOND` | `false` | включает `-vless-bond` |
| `WRAP_MODE` | `false` | включает `-wrap` |
| `WRAP_KEY` | пусто | ключ для `-wrap-key` |
| `VK_TURN_KCP_PROFILE` | `balanced` | профиль KCP (`fast`, `balanced`, `slow`) |
| `VK_TURN_KCP_MTU` | `1200` | переопределить MTU для KCP |

Сборка образа вручную:

```bash
docker build -t vk-turn-proxy .
```

## VLESS / Xray

В режиме `-vless` VK TURN Proxy прокидывает TCP-соединения. На VPS `server` подключается к локальному TCP backend, например к Xray inbound на `127.0.0.1:443`. На клиенте `client` слушает локальный TCP адрес, на который должен смотреть ваш Xray/v2rayN/sing-box клиент.

Сервер:

```bash
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:443 -vless
```

Клиент:

```bash
./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>" -vless
```

С bonding:

```bash
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:443 -vless
./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>" -vless -vless-bond -n 4
```

## WRAP-Режим

`-wrap` маскирует TURN-payload под SRTP: добавляет RTP/opus-заголовок и шифрует тело ChaCha20-Poly1305 AEAD. Это не защита (DTLS уже шифрует внутренний канал), а обфускация под голосовой трафик, чтобы content-filter VK не дропал пакеты. Ключ должен совпадать на клиенте и сервере.

Сгенерировать ключ:

```bash
./server -gen-wrap-key
```

Запуск:

```bash
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820 -wrap -wrap-key <64-hex-key>
./client -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>" -wrap -wrap-key <64-hex-key>
```

> **Breaking change (V2-0):** удалены клиентский `-no-dtls` и **серверный** `-vless-bond`.
> Клиентский `-vless-bond` сохранён — это и есть переключатель bonding.
> `-no-dtls` помечался DO NOT USE и в проде не работал (VK дропает пакеты без DTLS-handshake).
> Серверный `-vless-bond` больше не нужен: сервер автоопределяет bond по magic-префиксу в каждом стриме.

## Настройка KCP (VLESS)

В режиме `-vless` для передачи данных поверх DTLS используется KCP. Его можно настроить через переменные окружения (работает и для клиента, и для сервера):

| Переменная | Профили / Значения | Описание |
| --- | --- | --- |
| `VK_TURN_KCP_PROFILE` | `fast`, `balanced`, `slow` | Предустановленные режимы работы KCP. |
| `VK_TURN_KCP_MTU` | например, `1200` | Максимальный размер пакета. |

**Профили:**
- `fast` (или `legacy`): Минимальные задержки, активная переотправка, MTU 1280.
- `balanced` (или `cc`): Оптимальный баланс для большинства сетей, MTU 1200.
- `slow` (или `conservative`): Для очень нестабильных каналов, MTU 1150.

Для более тонкой настройки доступны переменные: `VK_TURN_KCP_NODELAY`, `VK_TURN_KCP_INTERVAL`, `VK_TURN_KCP_RESEND`, `VK_TURN_KCP_NC`, `VK_TURN_KCP_SNDWND`, `VK_TURN_KCP_RCVWND`, `VK_TURN_KCP_ACK_NODELAY`.

**FEC (Reed-Solomon):** `VK_TURN_KCP_FEC=data:parity` (например, `10:3`) — упреждающая коррекция ошибок. На каждые `data` пакетов добавляется `parity` избыточных, потери до `parity` из `data+parity` восстанавливаются без ретрансмита. Overhead по полосе: `parity/data` (для `10:3` — 30%). Должно совпадать на клиенте и сервере. По умолчанию выключено (`0:0`). Включай только при случайных потерях; при шейпе по полосе FEC ухудшит goodput.


## Флаги Клиента

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9000` | локальный адрес `ip:port`, куда подключается WireGuard или Xray клиент |
| `-peer` | обязательный | адрес сервера VK TURN Proxy на VPS, `host:port` (например `<ip-vps>:56000`) |
| `-vk-link` | обязательный | ссылка VK Calls вида `https://vk.com/call/join/...` |
| `-n` | `10` | количество параллельных TURN-потоков (соединений к TURN-реле) |
| `-udp` | `false` | подключаться к TURN-реле по UDP (по умолчанию TCP/TLS) |
| `-turn` | из ссылки | переопределить IP TURN-сервера |
| `-port` | из ссылки | переопределить порт TURN-сервера |
| `-vless` | `false` | режим TCP-форвардера (VLESS/Xray) вместо UDP-релея для WireGuard |
| `-vless-bond` | `false` | распределять одно VLESS TCP-соединение по всем активным smux-сессиям (только с `-vless`) |
| `-wrap` | `false` | маскировать TURN-payload под SRTP (см. [WRAP-режим](#wrap-режим)); ключ должен совпадать с сервером |
| `-wrap-key` | пусто | общий ключ для `-wrap`, 32 байта в hex (64 символа) |
| `-gen-wrap-key` | `false` | напечатать новый ключ для `-wrap-key` и выйти |
| `-manual-captcha` | `false` | пропустить авто-решение captcha и сразу открыть ручной режим в локальном браузере |
| `-streams-per-cred` | `10` | сколько TURN-потоков делят один кеш VK-учёток |
| `-dns` | `auto` | режим DNS-резолвинга: `udp` \| `doh` \| `auto` (UDP/53, sticky-fallback на DoH) |
| `-dns-servers` | пусто | список UDP/53 DNS-серверов через запятую вместо встроенных, формат `ip[:port][,ip[:port]...]` |
| `-debug` | `false` | включить подробные debug-логи |

## Флаги Сервера

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `0.0.0.0:56000` | локальный адрес прослушивания `ip:port` |
| `-connect` | обязательный | адрес локального бэкенда `host:port` (WireGuard `127.0.0.1:51820` или Xray `127.0.0.1:443`) |
| `-vless` | `false` | режим TCP-форвардера (VLESS/Xray) вместо UDP-релея; bond определяется автоматически по magic-префиксу в стриме |
| `-wrap` | `false` | маскировать TURN-payload под SRTP (см. [WRAP-режим](#wrap-режим)); ключ должен совпадать с клиентом |
| `-wrap-key` | пусто | общий ключ для `-wrap`, 32 байта в hex (64 символа) |
| `-gen-wrap-key` | `false` | напечатать новый ключ для `-wrap-key` и выйти |
| `-debug` | `false` | включить подробные debug-логи |

## Captcha

Для VK Calls клиент умеет автоматически проходить captcha. Если автоматика не сработала, включается ручной сценарий через локальный браузер. Можно сразу запросить ручной режим:

```bash
./client -manual-captcha -listen 127.0.0.1:9000 -peer <ip-vps>:56000 -vk-link "<vk-call-link>"
```

Профиль браузера сохраняется в `vk_profile.json` рядом с бинарником и может помочь последующим запросам выглядеть последовательнее.

## Сборка Из Исходников

Нужен Go 1.25.x.

```bash
go build -o client ./cmd/vk-turn-client
go build -o server ./cmd/vk-turn-server
go test ./...
```

Кросс-сборка примера для Linux amd64:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o server-linux-amd64 ./cmd/vk-turn-server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o client-linux-amd64 ./cmd/vk-turn-client
```

Android arm64 клиент как `.so` для упаковки в приложение (PowerShell):

```powershell
$env:GOOS="android"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"; go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn.so .\cmd\vk-turn-client
```

То же из bash:

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn.so ./cmd/vk-turn-client
```

Linux arm64 клиент как `.so` (для упаковки в Android-приложение в обход NDK; ABI совпадает):

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn.so ./cmd/vk-turn-client
```

Linux amd64 клиент/сервер как `.so`:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn-client.so ./cmd/vk-turn-client
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn-server.so ./cmd/vk-turn-server
```

Все `.so` выше — обычные ELF-бинари под `.so`-именем (`CGO_ENABLED=0`, не настоящий shared library). Если нужен настоящий `c-shared` — `CGO_ENABLED=1` + NDK toolchain (см. `.github/workflows/release.yml`).

## Решение Проблем

- Сначала запускайте VK TURN Proxy client, потом включайте WireGuard.
- Если WireGuard забирает весь трафик, добавьте маршрут до IP TURN-реле через `routes.sh`, `routes.ps1` или `routes-macos.sh`.
- Если TCP до TURN не работает, попробуйте `-udp`.
- Если соединение нестабильное, попробуйте уменьшить `-n`, например `-n 1`.
- Если VK просит captcha слишком часто, попробуйте `-manual-captcha`, затем повторите обычный запуск.
- Если клиент зависает на получении TURN-данных, проверьте, что ссылка VK Calls живая и не была завершена для всех.
- Если сервер запущен в Docker bridge mode, `CONNECT_ADDR=127.0.0.1:51820` укажет внутрь контейнера, а не на хост. Используйте host network или IP хоста.
- Если включен `-wrap`, убедитесь, что и клиент, и сервер используют одинаковый `-wrap-key`.

## Лицензия

GPL-3.0. См. [LICENSE](LICENSE).
