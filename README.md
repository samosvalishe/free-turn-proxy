<div align="center">

![License](https://img.shields.io/badge/license-Happy_Bunny-ff69b4?style=flat-square&logoColor=white&labelColor=0D1117)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white&labelColor=0D1117)
![Docker](https://img.shields.io/badge/docker-ready-2496ED?style=flat-square&logo=docker&logoColor=white&labelColor=0D1117)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20iOS-green?style=flat-square&labelColor=0D1117)
</div>

## О проекте

**Free Turn Proxy** - универсальный прокси-туннель для инкапсуляции UDP/TCP трафика поверх протокола TURN. Клиент извлекает временные TURN-учётки из ссылок на WebRTC-звонки и прозрачно маршрутизирует ваш VPN-трафик (WireGuard, AmneziaWG, Xray/VLESS) до сервера на VPS, используя DTLS и механизмы маскировки пакетов.

## Android-приложение - рекомендуемый способ использования

- Добавляйте подключения по ссылке, QR-коду или вручную.
- Устанавливайте и настраивайте сервер на VPS из приложения.
- Переключайте серверы, настраивайте раздельное туннелирование и делитесь доступом.
- Сохраняйте настройки в зашифрованную резервную копию.

[Скачать APK](https://github.com/samosvalishe/turn-proxy-android/releases/latest)

Требования: Android 7.0+ и устройство ARM (arm64-v8a или armeabi-v7a). Для собственного подключения нужен VPS; в режиме Relay также нужна ссылка на звонок.

> [!WARNING]
> При переходе Android-приложения на версию 5 потребуется настройка с нуля. Перед обновлением сохраните данные доступа к VPS и параметры подключения. Затем заново добавьте сервер или импортируйте новую ссылку и проверьте параметры VPN и раздельного туннелирования.

## Разработка

### Зависимости

- **Go** ≥ 1.26 - `https://go.dev/dl/`
- **Task** (runner) - `go install github.com/go-task/task/v3/cmd/task@v3.40.0` или `winget install Task.Task` / `brew install go-task`

Остальные dev-инструменты (`golangci-lint`, `govulncheck`, `goimports`, `goreleaser`) ставит сам Task:

```bash
task tools:install
```

### Команды

```bash
task                # список доступных задач
task build          # собрать client + server в dist/ для текущего хоста
task build:all      # кросс-сборка всех target через goreleaser snapshot
task build:ios      # сборка XCFramework для iOS в dist/Mobile.xcframework
task build:android  # сборка AAR архива для Android в dist/freeturn.aar
task test           # go test -race
task test:cover     # тесты + покрытие -> cover.html
task lint           # golangci-lint
task fmt            # gofmt + goimports (форматирование)
task fmt:check      # проверить форматирование (используется в CI)
task vet            # go vet
task vuln           # govulncheck
task ci             # полный набор: fmt:check + vet + lint + test + vuln
task tidy           # go mod tidy
task clean          # удалить dist/, cover.out, cover.html
```

## Документация

- [Быстрый старт](./docs/quickstart.md)
- [Режимы](./docs/modes.md)
- [Флаги](./docs/flags.md)
- [Развёртывание](./docs/deploy.md)
- [Мобильные Устройства](./docs/mobile.md)
- [URI и форматы ссылок](./docs/uri.md)
- [Подписки (Subscriptions)](./docs/sub.md)
- [Провайдеры](./docs/providers.md)
- [Решение проблем](./docs/troubleshooting.md)

## Благодарности

Огромное спасибо за вклад и идеи:
- [@cacggghp](https://github.com/cacggghp)
- [@Moroka8](https://github.com/Moroka8)
- [@alxmcp](https://github.com/alxmcp)
- [@TheAirBlow](https://github.com/TheAirBlow)
