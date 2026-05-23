# Режимы

## UDP-Релей (WireGuard / Hysteria)

Дефолт (`-mode udp`). Клиент слушает локальный UDP, сервер форвардит UDP в backend (WG `127.0.0.1:51820`).

## TCP-Форвардер (Xray / sing-box)

Флаг `-mode tcp` на обеих сторонах. Сервер коннектит локальный TCP backend (Xray inbound `127.0.0.1:443`). Клиент слушает локальный TCP, на него смотрит Xray/v2rayN/sing-box.

Поверх DTLS — KCP + smux.

```bash
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:443 -mode tcp
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -link "<vk-link>" -mode tcp
```

**Bonding** — распределение одного TCP-соединения по всем активным smux-сессиям, флаг только клиентский:

```bash
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -link "<vk-link>" -mode tcp -bond -n 4
```

Серверного `-bond` нет — сервер автоопределяет bond по magic-префиксу в стриме.

## OBF (wire-профили обфускации)

`-obf-profile` выбирает wire-профиль маскировки TURN-payload. Не защита (DTLS уже шифрует), а обфускация — иначе VK content-filter дропает payload, не похожий на голосовой трафик. Профиль и ключ должны совпадать на клиенте и сервере.

Доступные профили:
- **`none`** (default) — обфускация выключена.
- **`rtpopus`** — RTP/opus-заголовок + ChaCha20-Poly1305 AEAD на теле.

Сгенерировать ключ:

```bash
./server -gen-obf-key
```

Запуск:

```bash
./server ... -obf-profile rtpopus -obf-key <64-hex>
./client ... -obf-profile rtpopus -obf-key <64-hex>
```

## TURN Транспорт

По умолчанию клиент подключается к TURN-реле по TCP/TLS. Флаг `-transport udp` переключает на UDP:

```bash
./client ... -transport udp
```

## Captcha

Клиент сам проходит VK captcha. Если автоматика не справилась — открывается локальный браузер. Принудительно ручной:

```bash
./client -manual-captcha ...
```

Профиль браузера: `vk_profile.json` рядом с бинарником.

## DNS

Флаг `-dns-mode` управляет транспортом резолвера самого клиента (VK API, токены, captcha) — не DNS-маршрутизацией пользовательского трафика.

- `plain` — только UDP/53;
- `doh` — только DNS-over-HTTPS (Yandex DoH);
- `auto` (дефолт) — сначала UDP/53, sticky-fallback на DoH при полном отказе.

Свои UDP/53 резолверы: `-dns-servers ip[:port][,ip[:port]...]`.
