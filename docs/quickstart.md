# Быстрый Старт

Для стабильной работы мы сразу настроим **обфускацию трафика**.

## Что Нужно

- VPS с публичным IP, VPN-сервер (например, WireGuard или AmneziaWG) слушает локальный порт (например, `127.0.0.1:51820/udp`).
- Активная ссылка VK Calls: `https://vk.com/call/join/...` — создайте сами (звонок завершать нельзя).
- VPN-клиент на вашем устройстве: `Endpoint = 127.0.0.1:9000` (порт `9000` взят для примера), `MTU = 1280` (крайне важно для компенсации накладных расходов сети).

---

## Шаг 1: Запуск Сервера на VPS (Автоматически)

Интерактивный скрипт сам поставит Docker (или настроит systemd), сгенерирует ключ обфускации, откроет порт в файрволе и запустит сервер. Запускать от root:

```bash
curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/main/scripts/install-server.sh -o install-server.sh
sudo bash install-server.sh
```

> Скрипт **интерактивный** — задаёт вопросы в терминале, поэтому скачивайте файл и запускайте через `sudo bash`, а не `curl | bash` (пайп ломает чтение ответов). Скопируйте параметры клиента, которые скрипт выдаст в конце.

*Предпочитаете ручную установку шаг за шагом? См. [Развёртывание (deploy.md)](deploy.md).*

---

## Шаг 2: Запуск Клиента и Маршруты (ПК)

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

> [!TIP]
> **Упрощение с помощью Share-ссылок и Подписок**
> Вместо длинного перечисления флагов вы можете передать клиенту одну компактную ссылку `freeturn://` первым аргументом, или использовать флаг `-sub` для скачивания настроек. Подробнее читайте в [uri.md](uri.md) и [sub.md](sub.md).

---

## Шаг 3: Мобильные Устройства

На мобильных сетях есть жесткая специфика:
1. **Маршруты не нужны**. Но приложение с клиентом (Termux) **обязательно** нужно добавить в исключения VPN-клиента.
2. **Перехват DNS**. Мобильные операторы блокируют сторонние DNS, поэтому обязательно указывайте DNS оператора связи через `-dns-servers` (узнать его можно в настройках APN телефона).

```bash
termux-wake-lock
# Скачивание: curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-android-arm64 && chmod +x client

# Обязательно укажите ваш ключ и DNS
./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<vk-link>" -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ> -dns-servers <ip_dns_мобильного_оператора>
```

> См. подробности в разделе [Мобильные устройства (mobile.md)](mobile.md).
