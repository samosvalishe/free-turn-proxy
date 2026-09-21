# Быстрый Старт

## Требования
- **VPS с публичным IP** (скрипт установки сам развернёт FreeTurn и, по желанию, WireGuard).
- **Активная ссылка на звонок** (создайте сами, звонок не завершайте).

---

## Шаг 1: Запуск Сервера (VPS)

Интерактивный скрипт поднимет FreeTurn (Docker или systemd) и WireGuard `ft-wg0` либо подключит FreeTurn к вашему VPN, сгенерирует ключи, создаст клиента `owner` и выдаст **ссылку `freeturn://` и ссылку на QR-код**:

```bash
curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/master/scripts/install.sh | sudo bash
```

> [!TIP]
> Отсканируйте QR ссылки `freeturn://` в приложении FreeTurn: в ней уже есть ключ, Client ID и WireGuard-конфиг.
> Ссылка равносильна ключам - делитесь осторожно. Показать снова: `freeturn client qr <имя>`.
>
> **Новый клиент:** `freeturn client add myphone`. Подробнее - [deploy.md](deploy.md).

---

## Шаг 2: Запуск Клиента Free Turn Proxy (ПК)

Если прямой UDP до вашего VPS блокируется провайдером, запустите клиент `free-turn-proxy`. Он перенаправит трафик через TURN-релей провайдера под видом аудиозвонка. Клиент автоматически создаёт маршруты-исключения для IP TURN-серверов (`-routes`).

**Linux:**
```bash
curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-linux-amd64
chmod +x client
sudo ./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<call-link>" -obf-profile rtpopus3 -obf-key <ВАШ_КЛЮЧ> -client-id <ВАШ_CLIENT_ID> -routes
```

**Windows (PowerShell от администратора):**
```powershell
Invoke-WebRequest -Uri https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-windows-amd64.exe -OutFile client.exe
.\client.exe -peer <vps_ip>:56000 -provider vk -link "<call-link>" -listen 127.0.0.1:9000 -n 12 -streams-per-cred 12 -obf-profile rtpopus3 -obf-key <ВАШ_КЛЮЧ> -dns-servers 192.168.31.1 -dns-mode doh -client-id <ВАШ_CLIENT_ID> -routes
```

**macOS:**
```bash
# Apple Silicon (M1/M2/M3): client-darwin-arm64 | Intel: client-darwin-amd64
sudo ./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<call-link>" -obf-profile rtpopus3 -obf-key <ВАШ_КЛЮЧ> -client-id <ВАШ_CLIENT_ID> -routes
```

> **Важно:** В настройках вашего VPN-клиента (AmneziaWG или WireGuard) при работе через прокси используйте конфиг клиента (`<имя>.conf` из `freeturn client qr`, `Endpoint = 127.0.0.1:9000`, `MTU = 1280`). Включайте VPN *только после того*, как клиент выведет `Ensuring route to ...`.

> [!TIP]
> **Упрощение:** Вместо длинных флагов можно передать ссылку `freeturn://` (генерируется установщиком) или подписку (`-sub`):
> ```bash
> sudo ./client "freeturn://eyJ2Ijox..." -link "<call-link>" -routes
> ```
> Подробнее в [uri.md](uri.md) и [sub.md](sub.md).

---

## Шаг 3: Мобильные Устройства (Termux)

На мобильных сетях маршруты не нужны, но есть своя специфика (блокировка DNS, добавление в исключения VPN).

```bash
termux-wake-lock
# Скачивание: curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-android-arm64 && chmod +x client

# Обязательно укажите ваш ключ и DNS оператора (можно узнать в настройках APN)
./client -listen 127.0.0.1:9000 -peer <vps_ip>:56000 -link "<call-link>" -obf-profile rtpopus3 -obf-key <ВАШ_КЛЮЧ> -dns-servers <ip_dns_оператора> -client-id <ВАШ_CLIENT_ID>
```

> Обязательно добавьте приложение Termux в исключения вашего VPN-клиента. Подробнее в [mobile.md](mobile.md).

