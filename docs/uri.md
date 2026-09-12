# Формат URI Share-ссылок

`freeturn://` - компактный способ передать клиенту все параметры подключения. Ссылка
генерируется на экране «поделиться» (или владельцем сервера) и переопределяет
одноимённые флаги клиента, чтобы не вводить IP, режим, ключ и т.д. вручную.

> [!NOTE]
> Ссылка на звонок VK (`-link`) **не входит** в URI - она уникальна для каждого клиента.

## Формат

```text
freeturn://<base64url(json)>
```

Payload - JSON-объект, закодированный `base64url` (без padding, как Go
`base64.RawURLEncoding`). Версионирован полем `v`: парсер старой версии отвергнет
незнакомую, новые поля не ломают разбор. Пустые и дефолтные поля опускаются.

### Поля JSON

| Ключ | Флаг | Описание |
|------|------|----------|
| `v` | - | версия формата (сейчас `1`). |
| `provider` | `-provider` | источник TURN-creds (например `vk`). |
| `peer` | `-peer` | адрес сервера на VPS (`ip:port`). |
| `transport` | `-transport` | транспорт до TURN-реле: `tcp` \| `udp`. |
| `mode` | `-mode` | режим туннеля: `udp` \| `tcp`; отсутствует - `udp`. |
| `obf` | `-obf-profile` | профиль обфускации (`rtpopus` \| `rtpopus2`); `none` опускается. |
| `key` | `-obf-key` | ключ обфускации (hex), только при заданном `obf`. |
| `n` | `-n` | число TURN-потоков; отсутствует - дефолт клиента. |
| `spc` | `-streams-per-cred` | потоков на один кеш VK-учёток; отсутствует - дефолт клиента. |
| `cid` | `-client-id` | Client ID гостя; owner добавляет его в allowlist (`clients.json`). |
| `listen` | `-listen` | локальный `ip:port` для WireGuard/Xray. |
| `dns` | `-dns-mode` | резолвер клиента: `plain` \| `doh` \| `auto`. |
| `dnss` | `-dns-servers` | свои DNS через запятую. |
| `mcap` | `-manual-captcha` | ручная VK captcha (`true`). |
| `kcp` | `-kcp-*` | профиль ARQ для `mode: tcp` (`nodelay`, `interval`, `resend`, `nc`, `sndwnd`, `rcvwnd`, `mtu`, `acknodelay`); отсутствует - дефолт. |
| `name` | - | имя клиента / комментарий. |

> [!NOTE]
> **Про `cid`:** на каждую ссылку генерируется **свежий** Client ID, который owner
> добавляет в `clients.json` (комментарий = `name`). Без `cid` в allowlist клиент не
> авторизуется. В ссылку входят все параметры подключения - не входит только `-link`
> (звонок VK, уникален для каждого клиента).

### Пример (декодированный payload)

```json
{"v":1,"provider":"vk","peer":"1.2.3.4:56000","transport":"tcp","mode":"udp",
 "obf":"rtpopus","key":"d823fa...","n":10,"cid":"a1b2c3...","name":"RU-Server"}
```

## Пример использования

```bash
./client "freeturn://eyJ2IjoxLCJwcm92aWRlciI6InZrIiwicGVlciI6..." -link "https://vk.ru/call/join/..."
```

Параметры из URI переопределяют базовые флаги (`-peer`, `-transport`, `-mode`, `-obf-key`, `-n` ...).

Версия ссылки не меняется: `mode` и `kcp` - опциональные поля, старый клиент их игнорирует и остаётся на `udp` с дефолтным ARQ.
