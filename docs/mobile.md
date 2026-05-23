# Мобильные устройства

## Android (Termux / Приложения)

При работе на мобильных устройствах возникают две основные проблемы: перехват DNS оператором и зацикливание маршрутов VPN.

1. Установите Termux или используйте приложение (например, Free Turn).
2. В клиенте WireGuard / AmneziaWG: `Endpoint = 127.0.0.1:9000`, `MTU = 1280` (если связь нестабильна, MTU можно опускать вплоть до 1120).
3. **Критично:** Добавьте приложение, в котором запущен `free-turn-proxy` клиент (Termux, Free Turn), в **Исключения WireGuard** (разрешенные приложения, не пускать через VPN). Если этого не сделать, туннель завернется сам в себя, и соединения не будет.
4. **Критично:** В большинстве случаев мобильные операторы связи перехватывают/блокируют сторонние DNS, включая DoH. Вам необходимо передавать IP-адрес DNS вашего оператора связи через флаг `-dns-servers`.

Пример для Termux:

```bash
termux-wake-lock
curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-android-arm64
chmod +x client
# Обязательно замените <ip_dns_оператора> на DNS вашего провайдера
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -link "<vk-link>" -dns-servers <ip_dns_оператора>
```

Снять wake lock: `termux-wake-unlock`.

## iOS (iSH)

Запасной вариант без нативного клиента.

```bash
apk update
apk add curl
curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-linux-386
chmod +x client
GOMAXPROCS=1 GODEBUG=asyncpreemptoff=1 ./client -listen 127.0.0.1:9000 -peer <vps>:56000 -link "<vk-link>"
```

Дольше в фоне:

```bash
cat /dev/location > /dev/null &
```
