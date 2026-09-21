# Развёртывание сервера

## Установка скриптом (рекомендуется)

```bash
curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/master/scripts/install.sh | sudo bash
```

Мастер спросит:

1. **VPN на сервере** - новый WireGuard (скрипт поднимет его сам) или уже установленный VPN (Amnezia, wg-easy, 3x-ui и т.п.: указываете его `host:port`).
2. **Запуск** - `docker` или `systemd`.
3. **Порт и обфускация.**

В конце - ссылка `freeturn://` для приложения (и WireGuard-конфиг, если VPN новый).

Управление:

```bash
freeturn                      # меню: клиенты, настройки, обновление, логи, удаление
freeturn client add phone     # новый клиент и его ссылка
freeturn client list
freeturn client qr phone      # показать ссылку и QR ещё раз
freeturn client remove phone
```

Без вопросов:

```bash
curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/master/scripts/install.sh | \
  sudo bash -s -- -y --backend=new --method=docker
```

Все флаги: `freeturn --help`.

## Ручная установка

Ключ обфускации (одинаковый на сервере и клиенте): `openssl rand -hex 32`.

### Docker Compose

```yaml
services:
  free-turn-proxy:
    image: ghcr.io/samosvalishe/free-turn-proxy:latest
    network_mode: host
    restart: unless-stopped
    environment:
      - CONNECT_ADDR=127.0.0.1:51820   # ваш VPN
      - LISTEN_ADDR=0.0.0.0:56000
      - OBF_PROFILE=rtpopus3
      - OBF_KEY=<ВАШ_КЛЮЧ>
```

| Переменная | По умолчанию | |
| --- | --- | --- |
| `CONNECT_ADDR` | обязательна | адрес вашего VPN |
| `LISTEN_ADDR` | `0.0.0.0:56000` | внешний адрес |
| `MODE` | `udp` | `udp` (WireGuard) \| `tcp` (Xray/VLESS), как на клиенте |
| `OBF_PROFILE` | `none` | `none` \| `rtpopus` \| `rtpopus2` \| `rtpopus3` |
| `OBF_KEY` | - | ключ обфускации |
| `CLIENTS_FILE` | - | список разрешённых Client ID |
| `KCP_*` | - | тюнинг `MODE=tcp`, см. `docs/flags.md` |
| `DEBUG` | `false` | подробные логи |

### systemd

```bash
sudo mkdir -p /opt/free-turn-proxy
sudo curl -L -o /opt/free-turn-proxy/server \
  https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/server-linux-amd64
sudo chmod +x /opt/free-turn-proxy/server
```

`/etc/systemd/system/free-turn-proxy.service`:

```ini
[Unit]
Description=Free Turn Proxy
After=network-online.target

[Service]
ExecStart=/opt/free-turn-proxy/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820 -obf-profile rtpopus3 -obf-key <ВАШ_КЛЮЧ>
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now free-turn-proxy
```

### Доступ по Client ID

Без списка подключиться может любой, у кого есть ключ обфускации. Список:

```bash
sudo CLIENTS_FILE=/opt/free-turn-proxy/clients.json \
  /opt/free-turn-proxy/server clients add $(openssl rand -hex 16) phone
```

Запуск с `-clients-file /opt/free-turn-proxy/clients.json` (Docker: `CLIENTS_FILE` + volume). На клиенте - `-client-id` или ссылка `freeturn://`.

## Порт

Откройте внешний порт по **UDP** (при `MODE=tcp` тоже): `sudo ufw allow 56000/udp`. Скрипт делает это сам.
