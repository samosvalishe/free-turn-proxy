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