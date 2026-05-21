# Развёртывание

## Docker Compose (рекомендуется)

```bash
# Скопируйте docker-compose.yml на VPS и отредактируйте CONNECT_ADDR
docker compose up -d
docker compose logs -f
```

По умолчанию: WireGuard backend `127.0.0.1:51820`, host network, порт `56000/udp`.

Для Xray/sing-box: `MODE=tcp` и смените `CONNECT_ADDR` на `127.0.0.1:443`. Для SRTP-мимикрии: `OBF_MODE=true` и `OBF_KEY=<64-hex>`.

## Docker Run

**Host network** (backend на хосте):

```bash
docker run -d --name btp --network host --restart unless-stopped \
  -e CONNECT_ADDR=127.0.0.1:51820 \
  ghcr.io/samosvalishe/btp:latest
```

**Bridge mode:**

```bash
docker run -d --name btp -p 56000:56000/udp --restart unless-stopped \
  -e CONNECT_ADDR=<host-ip>:51820 \
  ghcr.io/samosvalishe/btp:latest
```

> В bridge mode `CONNECT_ADDR=127.0.0.1:...` указывает внутрь контейнера. Используйте host network или IP хоста.

**Переменные окружения:**

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `CONNECT_ADDR` | **обязательна** | backend сервера |
| `LISTEN_ADDR` | `0.0.0.0:56000` | адрес прослушивания |
| `MODE` | `udp` | режим туннеля: `udp` \| `tcp` |
| `OBF_MODE` | `false` | включает `-obf` |
| `OBF_KEY` | пусто | значение `-obf-key` |
| `DEBUG` | `false` | включает `-debug` |

Сборка образа:

```bash
docker build -t btp .
```

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
