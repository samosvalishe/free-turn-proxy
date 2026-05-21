# Мобильные

## Android (Termux)

1. Termux из F-Droid.
2. WireGuard: `Endpoint = 127.0.0.1:9000`, `MTU = 1280`.
3. Termux в исключения WireGuard.
4. В Termux:

```bash
termux-wake-lock
curl -L -o client https://github.com/samosvalishe/btp/releases/latest/download/client-android-arm64
chmod +x client
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>"
```

Снять wake lock: `termux-wake-unlock`.

## iOS (iSH)

Запасной вариант без нативного клиента.

```bash
apk update
apk add curl
curl -L -o client https://github.com/samosvalishe/btp/releases/latest/download/client-linux-386
chmod +x client
GOMAXPROCS=1 GODEBUG=asyncpreemptoff=1 ./client -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>"
```

Дольше в фоне:

```bash
cat /dev/location > /dev/null &
```
