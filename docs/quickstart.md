# Быстрый Старт (WireGuard)

## Что Нужно

- VPS с публичным IP, WireGuard слушает `127.0.0.1:51820/udp`.
- Активная ссылка VK Calls: `https://vk.com/call/join/...` — создайте сами, не завершайте звонок для всех.
- WireGuard на клиенте: `Endpoint = 127.0.0.1:9000`, `MTU = 1280`.

## 1. Сервер На VPS

```bash
curl -L -o server https://github.com/samosvalishe/btp/releases/latest/download/server-linux-amd64
chmod +x server
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820
```

Порт `56000/udp` открыт снаружи.

## 2. Маршруты (Split Tunneling)

WireGuard забирает весь трафик, включая трафик к TURN-реле — туннель замыкается сам на себя. Скрипт маршрутов добавляет исключение: трафик к TURN идёт напрямую через шлюз ISP.

Флаг `-debug` включает debug-логи, из которых скрипт читает IP TURN-сервера. `2>&1` перенаправляет их в пайп.

> **Android:** маршруты не нужны — добавьте Termux в исключения WireGuard.

## 3. Клиент

**Linux:**

```bash
curl -L -o client https://github.com/samosvalishe/btp/releases/latest/download/client-linux-amd64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>" -debug 2>&1 | ./scripts/routes.sh
```

**Windows (PowerShell от администратора):**

```powershell
Invoke-WebRequest -Uri https://github.com/samosvalishe/btp/releases/latest/download/client-windows-amd64.exe -OutFile client.exe
.\client.exe -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>" -debug 2>&1 | .\scripts\routes.ps1
```

**macOS:**

```bash
curl -L -o client https://github.com/samosvalishe/btp/releases/latest/download/client-darwin-arm64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>" -debug 2>&1 | ./scripts/routes-macos.sh
```

Когда скрипт напечатает `Ensuring route to ...` — включайте WireGuard.

> Скачали только бинарник без репо? Возьмите скрипты отдельно:
> [`scripts/routes.sh`](../scripts/routes.sh) · [`scripts/routes.ps1`](../scripts/routes.ps1) · [`scripts/routes-macos.sh`](../scripts/routes-macos.sh)
