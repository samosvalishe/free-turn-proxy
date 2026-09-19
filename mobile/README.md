# Интеграция free-turn-proxy через gomobile

Пакет `mobile` предоставляет API для встраивания ядра прокси в нативные мобильные приложения (iOS/Android) через `gomobile bind`.

## Сборка

### iOS
Сборка универсального XCFramework (`dist/Mobile.xcframework`) с поддержкой iOS и симулятора:
```bash
task build:ios
```

### Android
Сборка Android Archive (`dist/freeturn.aar` и `freeturn-sources.jar`) с JNI-обертками для Java/Kotlin:
```bash
task build:android
```

## API

Хост-приложение передает конфигурацию в формате JSON, а события получает через интерфейс обратного вызова.

```go
func Start(configJSON string) error                 // Запуск в режиме прокси
func StartTunnel(configJSON string, tunFD int) error // Запуск прокси + WireGuard/AmneziaWG
func StartDirectTunnel(wgText string, mtu, tunFD int) error // WireGuard/AmneziaWG напрямую, без релея
func Restart(configJSON string, tunFD int) error     // Перезапуск с ожиданием остановки (tunFD 0 - без туннеля)
func Stop()                                         // Остановка сессии
func GetState() *Snapshot                           // Метрики сессии (State, Rates, Streams)
func TunnelStats() *TunnelSnapshot                  // Статистика туннеля
func ParseTunnelConfig(wgText string, mtu int) (*TunnelParams, error) // Параметры tun для платформы
func SetEventSink(s EventSink)                      // Установка коллбека событий
func SetProtect(p Protector)                        // Защита сокетов (VpnService.protect)
func DefaultConfigJSON() string                     // Конфиг с дефолтами
func ValidateConfig(configJSON string) string       // Валидация JSON
func ConfigToArgs(configJSON string) (string, error) // Вывод эквивалентной CLI-команды
func DumpLogs() string
func ClearLogs()
func SetLogBuffer(enabled bool)
func Version() string
```

В Java класс экспортируется как `com.freeturn.core.mobile.Mobile`. `int` в Go транслируется в `long` на стороне Kotlin/Java.

### Конфигурация JSON

Пример схемы JSON:
```json
{
  "peer": "1.2.3.4:56000",
  "clientId": "0123456789abcdef0123456789abcdef",
  "provider": "vk",
  "turn":  {"n": 12, "transport": "tcp", "host": "", "port": ""},
  "proxy": {"mode": "udp", "listen": "127.0.0.1:9000"},
  "vk":    {"links": ["https://vk.ru/call/join/..."], "streamsPerCred": 12,
            "manualCaptcha": false, "platform": "mobile"},
  "obf":   {"profile": "rtpopus3", "key": "<64 hex>", "timingMs": 0},
  "dns":   {"mode": "auto", "servers": ["8.8.8.8"]},
  "log":   {"debug": false},
  "tunnel": {"mode": "none", "config": "", "mtu": 1280},
  "subUrl": ""
}
```

*   `clientId` - обязателен. Ядро на мобиле не пишет файлы, ID должен храниться в приложении.

### Режим туннеля

`proxy.mode`: `udp` (по умолчанию, UDP-релей для WireGuard) или `tcp` (TCP-форвардер для Xray/sing-box).

В `tcp` ядро слушает `proxy.listen` как TCP-порт, и VLESS-клиент ходит туда своим outbound. Своего tun при этом нет:

*   запускать через `Start(configJSON)`, не через `StartTunnel`; `VpnService.prepare`/`establish` звать не нужно - иначе tun отбирается у чужого VPN;
*   `protect()` не применяется, поэтому трафик до TURN пойдёт через активный системный VPN, если он включён;
*   `TunnelStats()` не наполняется, счётчики отдаёт `GetState()`, `connected`/`total` считаются по сессиям пула так же, как в udp-режиме;
*   `proxy.mode: "tcp"` вместе с `tunnel.mode` `wg`/`awg` отклоняется валидацией: встроенный WG гонит датаграммы;
*   `StartTunnel` при `proxy.mode: "tcp"` возвращает `ErrTCPModeRequiresStart` - ядро tun не читает, и принятый fd остался бы установленным вхолостую.

Опциональная секция `kcp` (`noDelay`, `interval`, `resend`, `nc`, `sndWnd`, `rcvWnd`, `mtu`, `ackNoDelay`) тюнит ARQ tcp-режима; в `udp` любое отличие от дефолта - ошибка. Дефолт агрессивный (`interval: 20`, окна 512, `ackNoDelay: true`), для мобильной сети щадящий вариант - `interval: 40`, окна 256, `ackNoDelay: false`.

JSON парсится с `DisallowUnknownFields`, поэтому конфиг с `proxy.mode`/`kcp` требует aar с этим изменением; старый JSON без них новое ядро читает как `udp`.

### Туннелирование

`tunnel.mode` принимает значения `none`, `wg` (WireGuard) или `awg` (AmneziaWG).

Текст WireGuard-конфигурации (`wg-quick`) передается в `tunnel.config`. В режиме `wg` параметры обфускации AmneziaWG игнорируются. Поле `Endpoint` из конфига не используется, так как трафик идет через TURN-релей.

В секции `[Interface]` понимаются параметры AmneziaWG: `Jc`/`Jmin`/`Jmax`, `S1`-`S4`, `H1`-`H4`, `I1`-`I5`, а также AWG 3+:

| Ключ | Значение | Должен совпадать с сервером |
|---|---|---|
| `HeaderProtectionKey` | base64-ключ 32 байта | да |
| `ContentPaddingAddition` | `N` или `LO-HI` | нет |
| `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout` | `N` или `LO-HI`, секунды | нет |
| `MaxHandshakeAttempts` | `N` или `LO-HI`, попытки | нет |
| `RandomTrailers` | bool | да |
| `DisableCookies` | bool | нет |

`HeaderProtectionKey` берёт nonce из crypto-паддинга, поэтому требует `S1`-`S4` не меньше 12 - иначе конфиг отклоняется.

Адреса, DNS, маршруты и MTU tun-интерфейса ядро не применяет - это работа платформы. Их отдаёт `ParseTunnelConfig` из того же текста, что уходит в `tunnel.config` (аргумент `mtu` - то же значение, что в `tunnel.mtu`; `0` - взять из конфига):

```kotlin
val p = Mobile.parseTunnelConfig(wgText, 1280)
val builder = VpnService.Builder().setMtu(p.mtu.toInt())
p.addresses.split(",").forEach { builder.addAddress(it.substringBefore("/"), it.substringAfter("/").toInt()) }
p.dns.split(",").filter { it.isNotEmpty() }.forEach(builder::addDnsServer)
p.allowedIPs.split(",").forEach { builder.addRoute(it.substringBefore("/"), it.substringAfter("/").toInt()) }
```

Для `wg`/`awg` нужен запуск через `StartTunnel`:
```kotlin
pfd = builder.establish()!!                     // хранится в сервисе, живёт дольше сессии
Mobile.startTunnel(configJson, pfd.dup().detachFd().toLong())
```
Ядро закрывает переданный дескриптор при остановке, поэтому отдаётся копия (`dup`), а оригинал остаётся у приложения: tun переживает `Restart` при смене сети, интерфейс не пересоздаётся и VPN не мигает. На каждый `StartTunnel`/`Restart` с туннелем нужна новая копия; закрывать отданный fd самостоятельно нельзя - ядро закрывает его и когда старт не удался.

`Restart(configJSON, 0)` - перезапуск в режиме прокси: ядро дескриптор не берёт и ничего не закрывает. Конфиг с `tunnel.mode` `wg`/`awg` в этом виде отклоняется (`ErrTunnelRequiresStartTunnel`) - для туннеля передаётся `tunFD > 0`.

WireGuard общается с релеем через in-memory `netconn.PacketPipe` напрямую. Петля `127.0.0.1:9000` не используется, поэтому исключать приложение из VPN-маршрутов не требуется. Защите через `SetProtect` подлежат только сокеты релея.

### Прямой туннель (без релея)

`StartDirectTunnel(wgText, mtu, tunFD)` поднимает тот же WireGuard/AmneziaWG, но пакеты идут прямо на сервер, минуя TURN. Конфиг - текст wg-quick, а не JSON: полей релея тут нет.

```kotlin
pfd = builder.establish()!!
Mobile.startDirectTunnel(wgConf, 1376L, pfd.dup().detachFd().toLong())
```

Отличия от `StartTunnel`:

*   каждый `[Peer]` обязан нести `Endpoint` в виде `ip:port` - bind устройства не резолвит имена (если в конфиге указан домен, хост должен отрезолвить его до вызова);
*   `SetProtect` обязателен: сокеты открывает bind устройства, без защиты трафик заворачивается обратно в туннель;
*   состояние и метрики отслеживаются через `GetState()` (polling); push-события `OnState` в `EventSink` генерируются только для релейных сессий;
*   `GetState()` держит `connected` с первого handshake и уходит в `connecting`, только если handshake старше 180 с при идущем tx; `Streams`/`Total` всегда 1;
*   `Wake()` в прямом режиме - no-op; при смене сети или выходе устройства из сна вызывается `Reconnect()`;
*   `Reconnect()` пересоздаёт и заново защищает сокеты - этого хватает при смене сети, перезапуск не нужен;
*   `Restart` работает только с JSON-конфигурацией: для смены параметров прямого туннеля нужен `Stop` и новый `StartDirectTunnel`;
*   `mtu` идёт только в валидацию и логи: tun создаёт платформа, MTU для неё даёт `ParseTunnelConfig`.

### Обработка событий

Хост-приложение должно реализовать интерфейс `EventSink`:

```go
type EventSink interface {
    OnState(state string, streams, total int, errMsg string)
    OnLog(level, msg string, unixMillis int64)
    OnCaptcha(url string)
}
```

`OnState` вызывается строго последовательно из одной горутины при изменении статуса. `OnLog` может вызываться параллельно.
Хост обязан удерживать ссылку на объект `EventSink` в памяти (JNI-ссылка со стороны Go не защищает его от GC).
