# Флаги

## Клиент

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9000` | локальный адрес `ip:port`, куда подключается WireGuard или Xray клиент |
| `-peer` | **обязательный** | адрес сервера на VPS, `host:port` |
| `-link` | **обязательный** | ссылка VK Calls `https://vk.com/call/join/...` |
| `-n` | `10` | параллельных TURN-потоков |
| `-transport` | `tcp` | транспорт до TURN-реле: `tcp` (TCP/TLS) \| `udp` |
| `-mode` | `udp` | режим туннеля: `udp` (UDP-релей для WireGuard) \| `tcp` (TCP-форвардер для Xray/sing-box) |
| `-bond` | `false` | распределять одно TCP-соединение по всем активным smux-сессиям (только с `-mode tcp`) |
| `-turn` | из ссылки | переопределить IP TURN-сервера |
| `-port` | из ссылки | переопределить порт TURN-сервера |
| `-obf` | `false` | SRTP-мимикрия payload |
| `-obf-key` | пусто | общий ключ, 32 байта hex (64 символа) |
| `-gen-obf-key` | `false` | напечатать новый ключ и выйти |
| `-manual-captcha` | `false` | сразу ручной режим captcha |
| `-streams-per-cred` | `10` | потоков на один кеш VK-учёток |
| `-dns-mode` | `auto` | `plain` (UDP/53) \| `doh` \| `auto` |
| `-dns-servers` | пусто | свои UDP/53 резолверы, `ip[:port][,ip[:port]...]` |
| `-debug` | `false` | debug-логи |

## Сервер

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `0.0.0.0:56000` | адрес прослушивания `ip:port` |
| `-connect` | **обязательный** | локальный backend `host:port` (WG `127.0.0.1:51820` / Xray `127.0.0.1:443`) |
| `-mode` | `udp` | режим туннеля: `udp` \| `tcp` (bond автоопределяется) |
| `-obf` | `false` | SRTP-мимикрия payload |
| `-obf-key` | пусто | общий ключ, 32 байта hex |
| `-gen-obf-key` | `false` | напечатать новый ключ и выйти |
| `-debug` | `false` | debug-логи |

