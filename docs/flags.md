# Флаги

## Клиент

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9000` | локальный адрес `ip:port`, куда подключается WireGuard или Xray клиент |
| `-peer` | **обязательный** | адрес сервера на VPS, `host:port` |
| `-vk-link` | **обязательный** | ссылка VK Calls `https://vk.com/call/join/...` |
| `-n` | `10` | параллельных TURN-потоков |
| `-udp` | `false` | TURN по UDP вместо TCP/TLS |
| `-turn` | из ссылки | переопределить IP TURN-сервера |
| `-port` | из ссылки | переопределить порт TURN-сервера |
| `-vless` | `false` | TCP-форвардер (VLESS/Xray) вместо UDP-релея |
| `-vless-bond` | `false` | распределять TCP-соединение по smux-сессиям (только с `-vless`) |
| `-wrap` | `false` | SRTP-мимикрия payload |
| `-wrap-key` | пусто | общий ключ, 32 байта hex (64 символа) |
| `-gen-wrap-key` | `false` | напечатать новый ключ и выйти |
| `-manual-captcha` | `false` | сразу ручной режим captcha |
| `-streams-per-cred` | `10` | потоков на один кеш VK-учёток |
| `-dns` | `auto` | `udp` \| `doh` \| `auto` |
| `-dns-servers` | пусто | свои UDP/53 резолверы, `ip[:port][,ip[:port]...]` |
| `-debug` | `false` | debug-логи |

## Сервер

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `0.0.0.0:56000` | адрес прослушивания `ip:port` |
| `-connect` | **обязательный** | локальный backend `host:port` (WG `127.0.0.1:51820` / Xray `127.0.0.1:443`) |
| `-vless` | `false` | TCP-форвардер; bond автоопределяется |
| `-wrap` | `false` | SRTP-мимикрия payload |
| `-wrap-key` | пусто | общий ключ, 32 байта hex |
| `-gen-wrap-key` | `false` | напечатать новый ключ и выйти |
| `-debug` | `false` | debug-логи |

## Переменные Окружения (KCP)

См. [modes.md → KCP](modes.md#kcp-только-в--vless).
