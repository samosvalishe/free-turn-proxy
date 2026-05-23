# Развёртывание сервера

Для того чтобы сервер работал круглосуточно, не падал при закрытии SSH-сессии и автоматически стартовал после перезагрузки VPS, вам необходимо настроить его работу как службы.

У вас есть два пути: **Docker** (максимально просто и чисто) или классический **systemd**.

---

## Способ 1: Docker Compose (Рекомендуется)

Это самый простой и надежный способ. Если у вас чистый сервер Ubuntu/Debian и Docker еще не установлен, вы можете поставить его официальным скриптом в одну команду:

```bash
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh
```

Создайте директорию для проекта и файл `docker-compose.yml`:

```bash
mkdir -p /opt/btp && cd /opt/btp
nano docker-compose.yml
```

Вставьте в него следующий конфиг, заменив `<ВАШ_КЛЮЧ>` на ключ, сгенерированный на Шаге 1 быстрого старта (и обязательно проверьте `CONNECT_ADDR`):

```yaml
services:
  vk-turn-proxy:
    image: ghcr.io/samosvalishe/btp:latest
    container_name: btp
    network_mode: "host" # Позволяет напрямую стучаться к локальному WireGuard (127.0.0.1:51820)
    restart: unless-stopped
    environment:
      - CONNECT_ADDR=127.0.0.1:51820  # Порт ВАШЕГО VPN (WG/AmneziaWG)
      - LISTEN_ADDR=0.0.0.0:56000     # Внешний порт, к которому будет подключаться клиент
      - MODE=udp                      # udp для WG/Amnezia, tcp для Xray/VLESS
      - OBF_PROFILE=rtpopus           # Обязательная маскировка
      - OBF_KEY=<ВАШ_КЛЮЧ>            # Ваш сгенерированный 64-hex ключ
```

Запустите контейнер в фоне:

```bash
docker compose up -d
```

Посмотреть логи: `docker compose logs -f`

---

## Способ 2: systemd (Классический Linux)

Если вы не хотите использовать Docker, вы можете зарегистрировать бинарный файл как системную службу.

**1. Скачайте бинарник в /opt/btp (если еще не сделали это):**
```bash
sudo mkdir -p /opt/btp
sudo curl -L -o /opt/btp/server https://github.com/samosvalishe/btp/releases/latest/download/server-linux-amd64
sudo chmod +x /opt/btp/server
```

**2. Создайте файл службы:**
```bash
sudo nano /etc/systemd/system/btp.service
```

**3. Вставьте конфиг (замените порты и `<ВАШ_КЛЮЧ>` на свои):**
```ini
[Unit]
Description=VK TURN Proxy Server
After=network.target

[Service]
Type=simple
# Укажите ваши порты и вставьте ваш ключ обфускации
ExecStart=/opt/btp/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820 -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ>
Restart=always
RestartSec=5
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
```

**4. Запустите службу и добавьте в автозагрузку:**
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now btp.service
```

Посмотреть статус: `sudo systemctl status btp.service`
Посмотреть логи: `sudo journalctl -u btp.service -f`

---

## Переменные окружения для Docker (Справка)

Если вы запускаете через голый `docker run`, вам пригодятся эти переменные:

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `CONNECT_ADDR` | **обязательна** | IP и порт вашего VPN (бэкенда) |
| `LISTEN_ADDR` | `0.0.0.0:56000` | Внешний адрес прослушивания |
| `MODE` | `udp` | режим туннеля: `udp` (для WG) \| `tcp` (для Xray) |
| `OBF_PROFILE` | `none` | значение `-obf-profile`: `none` \| `rtpopus` |
| `OBF_KEY` | пусто | значение `-obf-key` (обязателен при `OBF_PROFILE != none`) |
| `DEBUG` | `false` | включает `-debug` |

> **Внимание при Bridge Mode:** Если вы не используете `network_mode: "host"` и пробрасываете порты через `-p 56000:56000/udp`, то `CONNECT_ADDR=127.0.0.1:51820` будет указывать **внутрь** контейнера Docker. В таком случае прокси не найдет ваш WireGuard. Используйте IP хоста в `CONNECT_ADDR` или оставляйте `network_mode: "host"`.
