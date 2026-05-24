# Развёртывание сервера

Для работы сервера 24/7 его необходимо настроить как службу. У вас есть два пути: **Docker** (рекомендуется) или классический **systemd**.

> [!TIP]
> **Автоматическая установка**
> Проще всего развернуть сервер с помощью интерактивного скрипта (выполнит установку Docker/systemd, настройку файрвола и обфускации):
> ```bash
> curl -fsSL https://raw.githubusercontent.com/samosvalishe/free-turn-proxy/main/scripts/install-server.sh -o install-server.sh
> sudo bash install-server.sh
> ```
> Если вы хотите всё настроить руками — следуйте инструкциям ниже.

---

## Подготовка: Ключ обфускации

Если вы настраиваете всё вручную, сгенерируйте общий 64-символьный hex-ключ. Он должен совпадать на сервере и клиенте:

```bash
openssl rand -hex 32
```
*Скопируйте ключ — далее в примерах он обозначен как `<ВАШ_КЛЮЧ>`.*

---

## Способ 1: Docker Compose (Рекомендуется)

1. Установите Docker, если его еще нет:
   ```bash
   curl -fsSL https://get.docker.com -o get-docker.sh && sudo sh get-docker.sh
   ```

2. Создайте директорию проекта и файл `docker-compose.yml`:
   ```bash
   mkdir -p /opt/free-turn-proxy && cd /opt/free-turn-proxy
   nano docker-compose.yml
   ```

3. Вставьте конфиг (замените порты и `<ВАШ_КЛЮЧ>` на свои):
   ```yaml
   services:
     free-turn-proxy:
       image: ghcr.io/samosvalishe/free-turn-proxy:latest
       container_name: free-turn-proxy
       network_mode: "host" # Важно для прямого доступа к локальному WireGuard (127.0.0.1:51820)
       restart: unless-stopped
       environment:
         - CONNECT_ADDR=127.0.0.1:51820  # Порт ВАШЕГО VPN (WG/AmneziaWG/Xray)
         - LISTEN_ADDR=0.0.0.0:56000     # Внешний порт, к которому будет подключаться клиент
         - MODE=udp                      # udp для WG/Amnezia, tcp для Xray/VLESS
         - OBF_PROFILE=rtpopus           # Обязательная маскировка
         - OBF_KEY=<ВАШ_КЛЮЧ>            # Ваш сгенерированный 64-hex ключ
         # Раскомментируйте ниже, чтобы включить авторизацию по Client ID
         # - CLIENTS_FILE=/opt/free-turn-proxy/clients.json
       # volumes:
       #   - /opt/free-turn-proxy/clients.json:/opt/free-turn-proxy/clients.json
   ```

4. Запустите контейнер:
   ```bash
   docker compose up -d
   ```
   *Посмотреть логи: `docker compose logs -f`*

---

## Способ 2: systemd (Без Docker)

1. Скачайте бинарник в `/opt/free-turn-proxy`:
   ```bash
   sudo mkdir -p /opt/free-turn-proxy
   sudo curl -L -o /opt/free-turn-proxy/server https://github.com/samosvalishe/free-turn-proxy/releases/latest/download/server-linux-amd64
   sudo chmod +x /opt/free-turn-proxy/server
   ```
   *(Для ARM-сервера замените `-amd64` на `-arm64`)*

2. Создайте файл службы:
   ```bash
   sudo nano /etc/systemd/system/free-turn-proxy.service
   ```

3. Вставьте конфиг (замените порты и `<ВАШ_КЛЮЧ>` на свои):
   ```ini
   [Unit]
   Description=Free TURN Proxy Server
   After=network.target

   [Service]
   Type=simple
   # Укажите ваши порты и ключ обфускации.
   # Для авторизации по Client ID добавьте: -clients-file /opt/free-turn-proxy/clients.json
   ExecStart=/opt/free-turn-proxy/server -listen 0.0.0.0:56000 -connect 127.0.0.1:51820 -obf-profile rtpopus -obf-key <ВАШ_КЛЮЧ>
   Restart=always
   RestartSec=5
   User=nobody
   Group=nogroup

   [Install]
   WantedBy=multi-user.target
   ```
   *(Примечание: `User=nobody` не может биндить порты < 1024 — используйте внешний порт > 1024)*

4. Запустите службу и добавьте в автозагрузку:
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now free-turn-proxy.service
   ```
   *Статус: `sudo systemctl status free-turn-proxy.service`*

---

## Настройка Файрвола

Откройте внешний порт. Сервер слушает по **UDP** (DTLS-over-UDP), даже если вы используете `MODE=tcp` для Xray:

```bash
# Для UFW (Ubuntu/Debian)
sudo ufw allow 56000/udp

# Либо через iptables напрямую
sudo iptables -I INPUT -p udp --dport 56000 -j ACCEPT
sudo netfilter-persistent save  # если установлен
```

---

## Авторизация по Client ID (Опционально)

По умолчанию любой клиент с верным `-obf-key` может релеить трафик. Чтобы ограничить доступ конкретным списком клиентов, включите авторизацию.

1. Создайте пустой список на сервере:
   ```bash
   echo "[]" | sudo tee /opt/free-turn-proxy/clients.json
   ```
2. Включите авторизацию:
   - **В Docker:** раскомментируйте переменные `CLIENTS_FILE` и `volumes` в `docker-compose.yml`, затем выполните `docker compose up -d`.
   - **В systemd:** добавьте флаг `-clients-file /opt/free-turn-proxy/clients.json` в `ExecStart`, затем `systemctl daemon-reload && systemctl restart free-turn-proxy`.
3. Управляйте клиентами (см. раздел [Управление Client ID в flags.md](flags.md#управление-client-id-команды-сервера)).
4. **Не забудьте** передать флаг `-auth` и `-client-id <id>` при запуске клиента.

---

## Справка: Переменные окружения для Docker

Если вы запускаете через голый `docker run`, вам пригодятся эти переменные (аналогичны флагам бинарника):

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `CONNECT_ADDR` | **обязательна** | IP и порт вашего VPN (бэкенда) |
| `LISTEN_ADDR` | `0.0.0.0:56000` | Внешний адрес прослушивания |
| `MODE` | `udp` | Режим туннеля: `udp` (для WG/Amnezia) \| `tcp` (для Xray/VLESS) |
| `OBF_PROFILE` | `none` | Значение `-obf-profile`: `none` \| `rtpopus` |
| `OBF_KEY` | пусто | Значение `-obf-key` (обязателен при `OBF_PROFILE != none`) |
| `CLIENTS_FILE`| пусто | Путь к JSON-файлу (`/opt/free-turn-proxy/clients.json`) для авторизации |
| `DEBUG` | `false` | Включает debug-логи (`true` / `false`) |

> **Внимание при Bridge Mode:** Если вы уберете `network_mode: "host"` и пробросите порты через `-p 56000:56000/udp`, то `CONNECT_ADDR=127.0.0.1:51820` будет указывать **внутрь** контейнера Docker. В таком случае прокси не найдет ваш WireGuard. Используйте IP хоста в `CONNECT_ADDR` или оставляйте `network_mode: "host"`.
