# Глоссарий btp

Один абзац на термин. Если читаешь код и не понял слово — сюда.

---

## Слои протокола (снизу вверх)

```
backend (WireGuard UDP | TCP service)
  ↑
[app proxy mode]                  ← UDP-relay (-mode udp)  |  TCP-forward (-mode tcp)  |  TCP-forward+bond (-mode tcp -bond)
  ↑
[srtpmimicry]           (optional, AEAD обфускация поверх payload)
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
Standard relay protocol (RFC 5766/8656). Источник реле выбирается флагом `-provider` (см. ниже). Мы используем реле как транзит к собственному серверу. Транспорт до TURN выбирается флагом `-transport` (UDP/TCP).

### Provider
Источник TURN-реквизитов (user/pass/server-addr). Интерфейс `internal/provider.Provider`. Текущие реализации:
- **`vk`** (default) — `internal/provider/vk`, обёртка над VK Calls API (требует `-link`).
- **`static`** — `internal/provider/static`, фиксированные `-static-user/-static-pass/-static-addr` (для coturn, metered, любого совместимого TURN).

Pipeline (proxy/udprelay, proxy/tcpfwd) работает только через интерфейс — без VK-specific импортов. См. `docs/providers.md`.

### DTLS — это **обфускация, не security**
VK content-filter дропает payload, который не похож на legit DTLS-handshake. Мы используем `pion/dtls` чтобы пройти фильтр. Шифрование DTLS — побочный эффект, не цель.

### srtpmimicry
Пакет `internal/wire/srtpmimicry`. CLI флаг `-obf`. Это **ChaCha20-Poly1305 AEAD** с RTP-like wire-header (mimicry). Включается опционально поверх DTLS — даёт дополнительный слой обфускации с RTP-видимостью на проводе. Не путать с Go-термином wrap (`errors.Wrap`).

### app proxy mode (3 значения)
Что копируется поверх DTLS:
- **UDP-relay** (`-mode udp`, default): UDP-пакеты 1-в-1. Для WireGuard.
- **TCP-forward** (`-mode tcp`): TCP-байты в smux-стрим, на сервере `net.Dial("tcp", backend)`. Для Xray/sing-box. Пакет `internal/proxy/tcpfwd`.
- **TCP-forward + bond** (`-mode tcp -bond` на клиенте): один TCP-коннект страйпится по N smux-сессиям. Сервер автоопределяет по magic первых байтов (`bond-autodetect=true`).

### bond
Опция TCP-forward режима (`-mode tcp -bond`). Wire-формат: magic + frame с seq/type/data. Кодек в `internal/wire/bondframe`, клиент-handler в `internal/proxy/bondclient`, сервер-registry в `internal/proxy/bondserver`.

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
Имя проксирующего протокола Xray. В коде нет VLESS-логики — просто TCP-форвардер. CLI флаг: `-mode tcp`. Внутренний пакет: `tcpfwd`.

### iSH
Linux user-mode environment для iOS (через usermode x86 emulation). Пакет `internal/client/ish` — listener shim вокруг iOS sandbox-ограничений. Только клиент-only.

### kcptun
Пакет `internal/transport/kcptun` с KCP/smux-профилями. Используется только в TCP-forward режиме.

### turndial
Пакет `internal/transport/turndial` — TURN dial + allocate.

### netconn
Пакет `internal/netconn` — Conn wrappers (DirectNet, ConnectedUDPConn, SplitFirstWriteConn).

### Mode (5 разных в CLI)
Все ортогональны:
- `-transport tcp|udp` — транспорт до TURN (TCP/TLS или UDP).
- `-mode udp|tcp` — app proxy mode (UDP-relay|TCP-forward).
- `-bond` — bonding внутри TCP-forward (только client).
- `-obf` — srtpmimicry-обфускация.
- `-dns-mode` — транспорт резолвера клиента (plain|doh|auto).

### captcha (auto / manual)
Один домен, два пути:
- **auto** (`internal/provider/vk/internal/captcha`): VK slider-puzzle решается программно.
- **manual** (`internal/provider/vk/internal/captcha/manual`): HTTP-сервер на 127.0.0.1:8765, запуск браузера, gzip-rewrite VK-страниц.

Переключение: `vkauth.Client` пробует auto, при провале → manual fallback. Флаг `-manual-captcha` сразу идёт в manual.

### libvkturn.so
Мобильная (Android) сборка клиента в виде shared library. Собирается **вручную** разработчиком и переносится в Android-приложение. CI не строит. Внешних `//export`-функций нет — `package main` ← buildmode определяется на стороне разработчика.

### `-bond` на сервере
Серверного `-bond` нет — сервер автоопределяет bond по magic первых байтов в стриме. Только клиентский флаг.
