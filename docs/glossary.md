# Глоссарий btp

Один абзац на термин. Если читаешь код и не понял слово — сюда.

---

## Слои протокола (снизу вверх)

```
backend (WireGuard UDP | TCP service)
  ↑
[app proxy mode]                  ← UDP-relay   |  TCP-forward (vless)  |  TCP-forward+bond
  ↑
[wrap / SRTP-mimicry]   (optional, AEAD obfuscation поверх payload)
  ↑
[DTLS]                  ← обфускация (не security): VK content-filter ожидает DTLS handshake
  ↑
[TURN ChannelData]      ← пакеты идут через VK TURN-relay
  ↑
[TURN transport]        ← UDP (-udp) | TCP/TLS (default), туннель до VK TURN
  ↑
client ←→ VK TURN-relay
```

---

## Термины

### TURN
Standard relay protocol (RFC 5766/8656). VK выдаёт креды на свои TURN-реле. Мы используем эти реле как транзит к собственному серверу. Транспорт до TURN выбирается флагом `-udp` (UDP/TCP).

### DTLS — это **обфускация, не security**
VK content-filter дропает payload, который не похож на legit DTLS-handshake. Мы используем `pion/dtls` чтобы пройти фильтр. Шифрование DTLS — побочный эффект, не цель. Флаг `-no-dtls` был помечен DO NOT USE и удалён в V2-0 (в проде не работал).

### wrap / SRTP-mimicry
Внутреннее имя пакета — `wrap` (планируется переименование в `srtpmimicry` на V2-4). CLI флаг `-wrap`. Это **ChaCha20-Poly1305 AEAD** с RTP-like wire-header (mimicry). Включается опционально поверх DTLS — даёт дополнительный слой обфускации с RTP-видимостью на проводе. CLI help говорит «ChaCha20-XOR» — **врёт**, реально AEAD. Не путать с Go-термином wrap (`errors.Wrap`).

### app proxy mode (3 значения)
Что копируется поверх DTLS:
- **UDP-relay** (default): UDP-пакеты 1-в-1. Для WireGuard.
- **TCP-forward** (`-vless`): TCP-байты в smux-стрим, на сервере `net.Dial("tcp", backend)`. Generic, имя историческое (первый потребитель — Xray VLESS). Внутренний кандидат имени: `tcpfwd`.
- **TCP-forward + bond** (`-vless-bond` на клиенте): один TCP-коннект страйпится по N smux-сессиям. Сервер автоопределяет по magic первых байтов (`bond-autodetect=true`). Серверный флаг `-vless-bond` удалён в V2-0 как избыточный.

### bond
Опция TCP-forward режима. Не отдельная подсистема — feature `vless`. Wire-формат: magic + frame с seq/type/data. Кодек в `internal/bond`, клиент-handler в `internal/bond/client`, сервер-registry в `internal/bond/server` (планируется переезд в `internal/proxy/bond{client,server}` на V2-4).

### TURN stream
Одна из N TURN-connection'ов клиента. Флаг `-n` (default 10). Каждый stream = отдельный TURN-allocate + DTLS-session. Не путать с smux stream.

### smux stream
Внутри **одной** DTLS-сессии запускается KCP+smux, и поверх него — N smux-стримов. Используется только в TCP-forward режиме. Тип `*smux.Stream` из `xtaci/smux`.

### VK credential cache / stream-per-cred
Один набор VK creds (полученный через VK API) обслуживает группу TURN streams. Флаг `-streams-per-cred` (default 10). В коде иногда называется `streamsPerCache`. То есть «cred» (одна credential) = «cache» (одна запись в `StreamCredentialsCache`).

### Client (4 разных)
1. `cmd/client/` — наш клиентский бинарь.
2. `vkauth.Client` — HTTP-клиент к VK API.
3. `bondclient.Handler` — наша клиент-сторона bond.
4. `turn.NewClient` — TURN-protocol-клиент (`pion/turn`).

При чтении кода держи контекст.

### VLESS
Имя проксирующего протокола Xray. **Внутри этого репо** означает только «TCP-forwarder режим». Никакой VLESS-логики в коде нет — мы просто гоним TCP-байты. Имя историческое; внутреннее переименование в `tcpfwd` запланировано на V2-4 (CLI флаг `-vless` остаётся).

### iSH
Linux user-mode environment для iOS (через usermode x86 emulation). Пакет `internal/ish` — listener shim вокруг iOS sandbox-ограничений. Только клиент-only; переедет в `internal/client/ish` на V2-4.

### tcputil
Top-level пакет с **KCP/smux**-профилями. Имя врёт (никакого TCP внутри). Запланировано переименование в `internal/transport/kcptun` на V2-4.

### turnpipe
Пакет с TURN dial + allocate. Имя `pipe` создаёт коллизию с `pipeConn` (bidi data-copy loop в server). Переедет в `internal/transport/turndial` на V2-4.

### netadapt
Conn wrappers (DirectNet, ConnectedUDPConn, SplitFirstWriteConn). Имя не отражает суть — переедет в `internal/netconn` на V2-4.

### Mode (5 разных в CLI)
Все ортогональны:
- `-udp` — TURN transport (UDP|TCP к TURN).
- `-vless` — app proxy mode (UDP-relay|TCP-forward).
- `-vless-bond` — внутри TCP-forward (только client).
- `-wrap` — wrap-обфускация (off|SRTP-mimicry).
- `-dns` — DNS resolver (udp|doh|auto).

### captcha (auto / manual)
Один домен, два пути:
- **auto** (`internal/client/captcha`): VK slider-puzzle решается программно через `client/internal/captcha`.
- **manual** (пока в `cmd/client/manual_captcha.go`, package main): HTTP-сервер на 127.0.0.1:8765, запуск браузера, gzip-rewrite VK-страниц. Запланирован вынос в `internal/client/captcha/manual` на V2-2.

Переключение: `vkauth.Client` пробует auto, при провале → manual fallback. Флаг `-manual-captcha` сразу идёт в manual.

### libvkturn.so
Мобильная (Android) сборка клиента в виде shared library. Собирается **вручную** разработчиком и переносится в Android-приложение. CI не строит. Внешних `//export`-функций нет — `package main` ← buildmode определяется на стороне разработчика.

### globalBondRegistry
Server-side глобал, держит активные bond-сессии. Инициализируется на package init — известная фрагильность (`isDebug` ещё false), работает только потому что bondserver `Debug`-поле не читается. Переедет в локальную переменную `cmd/server/main.go` на V2-3.

### `-no-dtls` (УДАЛЕНО)
Был помечен DO NOT USE — в проде не работал, VK content-filter дропает payload без DTLS-handshake. Удалён в V2-0.

### `-vless-bond` на сервере (УДАЛЕНО)
Сервер автоопределяет bond по magic первых байтов. Флаг был избыточен, удалён в V2-0. На клиенте — остаётся.
