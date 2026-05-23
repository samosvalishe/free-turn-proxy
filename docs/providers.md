# Providers

Источник TURN-реквизитов выбирается флагом `-provider` (default `vk`). Реализации удовлетворяют интерфейс `internal/provider.Provider` и подключаются в `cmd/client/main.go` через `buildProvider`.

## Доступные провайдеры

### `vk` (default)

VK Calls API. Перебирает встроенные `app_id/app_secret`, получает короткоживущие (≈10 мин) TURN-creds через 4-шаговый token chain. Solver captcha auto+manual.

**Обязательные флаги:**
- `-link` — VK callroom URL вида `https://vk.com/call/join/<code>` (нормализуется до join-кода).

**Опциональные:**
- `-streams-per-cred` (default 10) — сколько TURN-стримов делят один кеш креденшалов.
- `-manual-captcha` — пропустить auto-solver, сразу открыть браузер.

Пакет: `internal/provider/vk/`. VK-специфичные зависимости (`vkauth`, `captcha`, `captcha/manual`, `browserprofile`, `namegen`) живут под `internal/provider/vk/internal/*` и недоступны извне `provider/vk`. Общие: `internal/client/{ish,dnsdial}`.

## Как добавить новый провайдер

1. Создать пакет `internal/provider/<name>/`.
2. Определить `Config` (опции из CLI или из встроенной БД).
3. Реализовать тип, удовлетворяющий `provider.Provider`:

   ```go
   type Provider interface {
       GetCredentials(ctx context.Context, streamID int) (Credentials, error)
       IsAuthError(err error) bool
       HandleAuthError(streamID int) bool
       ResetErrors(streamID int)
       BackoffUntilUnix() int64
       Name() string
   }
   ```

4. Добавить флаги в `internal/config/config.go` (`<Name>Opts`, регистрация в `ParseClient`, валидация в `switch c.Provider.Name`).
5. Добавить ветку в `buildProvider` в `cmd/client/main.go`.
6. Тесты в `internal/provider/<name>/<name>_test.go` (минимум: validation, GetCredentials, контракт интерфейса).

### Соглашения

- **Backoff**: если провайдер просит pipeline подождать, оборачивай ошибку в `provider.ErrBackoffActive` (через `errors.Join` или `fmt.Errorf("%w: ...", provider.ErrBackoffActive, inner)`) + возвращай `BackoffUntilUnix() > 0` при наличии точного дедлайна.
- **Фатально**: если креды нельзя получить и ни один стрим не поднят, оборачивай в `provider.ErrFatalNoStreams`. Pipeline вернёт `udprelay.ErrFatal`, хост-процесс сделает `os.Exit(1)`.
- **Thread-safety**: все методы зовутся из N горутин (по числу стримов).
- **Логи**: префикс `[Provider:<name>]` или `[<Name> Auth]` внутри пакета. Generic pipeline не пишет имя провайдера сам.

## Что НЕ относится к провайдеру

- DNS-стратегия резолвинга control-plane API (`internal/client/dnsdial`) — ортогональна провайдеру. VK-провайдер использует её для VK API, другой провайдер может использовать для своего HTTP API или не использовать вовсе.
- Транспорт до TURN-реле (`-transport tcp|udp`) — определяется на pipeline-уровне, провайдер только выдаёт `host:port`.
- Application-mode (`-mode udp|tcp`, `-bond`, `-obf`) — поверх TURN, не зависит от источника creds.

## Известные ограничения текущей абстракции

1. **Per-process один провайдер** — нет fallback chain. Можно добавить wrapper-провайдер позже.
2. **`StreamsAlive` callback** — VK-провайдер использует для решения fatal-vs-throttle. Другие провайдеры могут игнорировать (поле `Config.StreamsAlive` nil-safe).
