# Мобильные устройства

## Android-приложение - рекомендуемый способ использования

- Добавляйте подключения по ссылке, QR-коду или вручную.
- Устанавливайте и настраивайте сервер на VPS из приложения.
- Переключайте серверы, настраивайте раздельное туннелирование и делитесь доступом.
- Сохраняйте настройки в зашифрованную резервную копию.

[Скачать APK](https://github.com/samosvalishe/turn-proxy-android/releases/latest)

Требования: Android 7.0+ и устройство ARM (arm64-v8a или armeabi-v7a). Для собственного подключения нужен VPS; в режиме Relay также нужна ссылка на звонок.

> [!WARNING]
> При переходе Android-приложения на версию 5 потребуется настройка с нуля. Перед обновлением сохраните данные доступа к VPS и параметры подключения. Затем заново добавьте сервер или импортируйте новую ссылку и проверьте параметры VPN и раздельного туннелирования.

## Android (Termux, ручная настройка)

Альтернативный вариант для работы с отдельным VPN-клиентом.

При работе на мобильных устройствах возникают две основные проблемы: перехват DNS оператором и зацикливание маршрутов VPN.

1. Установите Termux.
2. В клиенте WireGuard / AmneziaWG: `Endpoint = 127.0.0.1:9000`, `MTU = 1280` (если связь нестабильна, MTU можно снижать вплоть до 1120).
3. **Критично:** Добавьте Termux в **Исключения WireGuard** (разрешенные приложения, не пускать через VPN). Если этого не сделать, туннель завернется сам в себя, и соединения не будет.
4. **Критично:** В большинстве случаев мобильные операторы блокируют сторонние DNS, включая DoH. Передавайте IP-адрес DNS вашего оператора связи через флаг `-dns-servers`.

Пример запуска в Termux:

```bash
termux-wake-lock
curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-android-arm64
chmod +x client
# Замените <ip_dns_оператора> на DNS вашего провайдера
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -link "<call-link>" -dns-servers <ip_dns_оператора>
```

Снять wake lock: `termux-wake-unlock`.

## iOS (iSH)

Запасной вариант без нативного клиента.

```bash
apk update
apk add curl
curl -L -o client https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/client-linux-386
chmod +x client
GOMAXPROCS=1 GODEBUG=asyncpreemptoff=1 ./client -listen 127.0.0.1:9000 -peer <vps>:56000 -link "<call-link>"
```

Дольше в фоне:

```bash
cat /dev/location > /dev/null &
```
