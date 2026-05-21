# Развёртывание

## systemd

`/etc/systemd/system/btp.service`:

```ini
[Unit]
Description=VK TURN Proxy server
After=network.target

[Service]
Type=simple
ExecStart=/opt/btp/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820
Restart=always
RestartSec=5
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now btp.service
sudo systemctl status btp.service
```

## Docker

Образ:

```bash
docker pull ghcr.io/samosvalishe/btp:latest
```

**Host network** (backend на хосте):

```bash
docker run --rm --network host \
  -e CONNECT_ADDR=127.0.0.1:51820 \
  ghcr.io/samosvalishe/btp:latest
```

**Bridge mode:**

```bash
docker run --rm -p 56000:56000/udp \
  -e CONNECT_ADDR=<host-ip>:51820 \
  ghcr.io/samosvalishe/btp:latest
```

> В bridge `CONNECT_ADDR=127.0.0.1:...` указывает внутрь контейнера. Используйте host network или IP хоста.

**Переменные:**

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `CONNECT_ADDR` | **обязательна** | backend сервера |
| `LISTEN_ADDR` | `0.0.0.0:56000` | адрес прослушивания |
| `VLESS_MODE` | `false` | включает `-vless` |
| `WRAP_MODE` | `false` | включает `-wrap` |
| `WRAP_KEY` | пусто | значение `-wrap-key` |
| `VK_TURN_KCP_PROFILE` | `balanced` | профиль KCP |
| `VK_TURN_KCP_MTU` | `1200` | MTU для KCP |

> `VLESS_BOND` в `docker-entrypoint.sh` ещё передаёт `-vless-bond` серверу, но серверный флаг удалён в V2-0 (bond автоопределяется по magic-префиксу). Не устанавливайте `VLESS_BOND=true` — сервер не запустится.

Сборка образа:

```bash
docker build -t btp .
```
