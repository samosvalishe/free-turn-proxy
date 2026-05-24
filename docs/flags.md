# Флаги

## Клиент

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9000` | локальный адрес `ip:port`, куда подключается WireGuard или Xray клиент |
| `-peer` | **обязательный** | адрес сервера на VPS, `host:port` |
| `-provider` | `vk` | источник TURN-creds: `vk` (см. `docs/providers.md`) |
| `-link` | **обязательный для `-provider vk`** | ссылка VK Calls `https://vk.com/call/join/...` |
| `-n` | `10` | параллельных TURN-потоков |
| `-transport` | `tcp` | транспорт до TURN-реле: `tcp` (TCP/TLS) \| `udp` |
| `-mode` | `udp` | режим туннеля: `udp` (UDP-релей для WireGuard) \| `tcp` (TCP-форвардер для Xray/sing-box) |
| `-bond` | `false` | распределять одно TCP-соединение по всем активным smux-сессиям (только с `-mode tcp`) |
| `-turn` | из creds | переопределить IP TURN-сервера |
| `-port` | из creds | переопределить порт TURN-сервера |
| `-obf-profile` | `none` | wire-профиль обфускации payload: `none` \| `rtpopus` (RTP/opus + ChaCha20-Poly1305 AEAD) |
| `-obf-key` | пусто | общий ключ для `-obf-profile != none`, 32 байта hex (64 символа) |
| `-gen-obf-key` | `false` | напечатать новый ключ и выйти |
| `-manual-captcha` | `false` | сразу ручной режим captcha (только `-provider vk`) |
| `-streams-per-cred` | `10` | потоков на один кеш VK-учёток (только `-provider vk`) |
| `-dns-mode` | `auto` | `plain` (UDP/53) \| `doh` \| `auto` |
| `-dns-servers` | пусто | свои UDP/53 резолверы, `ip[:port][,ip[:port]...]` |
| `-client-id` | авто | уникальный ID клиента (автогенерация если не задан) |
| `-sub` | пусто | URL подписки (sub.md) для получения списка серверов |
| `-auth` | `false` | отправлять Client ID для авторизации на сервере |
| `-debug` | `false` | debug-логи |

## Сервер

| Флаг | По умолчанию | Описание |
| --- | --- | --- |
| `-listen` | `0.0.0.0:56000` | адрес прослушивания `ip:port` |
| `-connect` | **обязательный** | локальный backend `host:port` (WG `127.0.0.1:51820` / Xray `127.0.0.1:443`) |
| `-mode` | `udp` | режим туннеля: `udp` \| `tcp` (bond автоопределяется) |
| `-obf-profile` | `none` | wire-профиль обфускации payload: `none` \| `rtpopus` (RTP/opus + ChaCha20-Poly1305 AEAD) |
| `-obf-key` | пусто | общий ключ для `-obf-profile != none`, 32 байта hex |
| `-gen-obf-key` | `false` | напечатать новый ключ и выйти |
| `-clients-file` | пусто | путь к JSON-файлу (`clients.json`) для включения авторизации по Client ID |
| `-debug` | `false` | debug-логи |

## Управление Client ID (Команды Сервера)

> [!WARNING]
> **Важно про Авторизацию:** Флаги `-auth` (на клиенте) и `-clients-file` (на сервере) должны быть включены либо **на обоих концах одновременно**, либо ни на одном. Рассинхронизация приведёт к обрыву соединений без внятных ошибок, так как ломается порядок байтов в туннеле.

Сервер содержит встроенные команды для управления файлом `clients.json` (горячая перезагрузка поддерживается автоматически, перезапускать сервер после изменений не нужно).

```bash
# Добавить или обновить клиента
./server clients add <client_id> ["Комментарий"]

# Удалить клиента
./server clients remove <client_id>

# Вывести список всех клиентов
./server clients list
```

По умолчанию команды работают с файлом `clients.json` в текущей директории. Если вы используете другой путь, задайте его через переменную окружения `CLIENTS_FILE`:
```bash
CLIENTS_FILE=/etc/free-turn-proxy/clients.json ./server clients list
```

### Управление через Docker

Если сервер запущен в Docker-контейнере (например, с именем `free-turn-proxy`), вы можете использовать команду `docker exec` для управления клиентами без необходимости заходить внутрь контейнера или редактировать файл вручную:

```bash
# Добавить клиента
docker exec -it free-turn-proxy /app/server clients add "my-client" "Комментарий"

# Удалить клиента
docker exec -it free-turn-proxy /app/server clients remove "my-client"

# Посмотреть список
docker exec -it free-turn-proxy /app/server clients list
```

> **Важно:** команды `docker exec` берут путь к файлу из переменной окружения `CLIENTS_FILE` контейнера. Это работает, только если контейнер запущен с включённой авторизацией (т.е. `CLIENTS_FILE` задан в `docker-compose.yml` и файл проброшен через `volumes`). Если авторизация выключена, `clients` пишет в эфемерный `clients.json` внутри контейнера, который сервер не читает. Путь должен совпадать с тем, что смонтирован и передан в `-clients-file`.

