# Режимы

## UDP-Релей (WireGuard / Hysteria)

Дефолт. Клиент слушает локальный UDP, сервер форвардит UDP в backend (WG `127.0.0.1:51820`).

## VLESS / Xray (TCP)

Флаг `-vless` на обеих сторонах. Сервер коннектит локальный TCP backend (Xray inbound `127.0.0.1:443`). Клиент слушает локальный TCP, на него смотрит Xray/v2rayN/sing-box.

Поверх DTLS — KCP + smux.

```bash
./server -listen 0.0.0.0:56000 -connect 127.0.0.1:443 -vless
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>" -vless
```

**Bonding** — распределение одного TCP-соединения по всем активным smux-сессиям, флаг только клиентский:

```bash
./client -listen 127.0.0.1:9000 -peer <vps>:56000 -vk-link "<vk-link>" -vless -vless-bond -n 4
```

> **Breaking (V2-0):** серверный `-vless-bond` удалён, сервер автоопределяет bond по magic-префиксу в стриме. Клиентский `-no-dtls` тоже удалён (VK дропает не-DTLS).

## WRAP (SRTP-Мимикрия)

`-wrap` маскирует TURN-payload под SRTP: RTP/opus-заголовок + ChaCha20-Poly1305 AEAD на теле. Не защита (DTLS уже шифрует), а обфускация под голос — иначе VK content-filter дропает. Ключ должен совпадать.

Сгенерировать ключ:

```bash
./server -gen-wrap-key
```

Запуск:

```bash
./server ... -wrap -wrap-key <64-hex>
./client ... -wrap -wrap-key <64-hex>
```

## KCP (Только В `-vless`)

Переменные окружения (клиент и сервер):

| Переменная | Значения | Описание |
| --- | --- | --- |
| `VK_TURN_KCP_PROFILE` | `fast` \| `balanced` \| `slow` | Предустановка. |
| `VK_TURN_KCP_MTU` | напр. `1200` | Размер пакета. |
| `VK_TURN_KCP_FEC` | `data:parity`, напр. `10:3` | Reed-Solomon FEC. |

**Профили:**

- `fast` (`legacy`) — минимальные задержки, активная переотправка, MTU 1280.
- `balanced` (`cc`) — баланс для большинства сетей, MTU 1200.
- `slow` (`conservative`) — нестабильные каналы, MTU 1150.

Тонкая настройка: `VK_TURN_KCP_NODELAY`, `_INTERVAL`, `_RESEND`, `_NC`, `_SNDWND`, `_RCVWND`, `_ACK_NODELAY`.

**FEC:** на каждые `data` пакетов — `parity` избыточных. Потери до `parity` из `data+parity` восстанавливаются без ретрансмита. Overhead полосы: `parity/data` (для `10:3` — 30%). Включайте при случайных потерях; при шейпе по полосе FEC ухудшит goodput. По умолчанию `0:0`.

## Captcha

Клиент сам проходит VK captcha. Если автоматика не справилась — открывается локальный браузер. Принудительно ручной:

```bash
./client -manual-captcha ...
```

Профиль браузера: `vk_profile.json` рядом с бинарником.

## DNS

Флаг `-dns`:

- `udp` — только UDP/53;
- `doh` — только DNS-over-HTTPS;
- `auto` (дефолт) — сначала UDP/53, sticky-fallback на DoH при полном отказе.

Свои резолверы: `-dns-servers ip[:port][,ip[:port]...]`.
