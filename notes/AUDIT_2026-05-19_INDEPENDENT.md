# Независимый аудит — 2026-05-19

Аудит проведён без опоры на предыдущие AUDIT_* и без учёта обратной совместимости.
Билд чистый, `go vet` / `golangci-lint run` без замечаний. Дефекты ниже не ловятся
текущими тулзами.

## 1. Архитектура

- **`cmd/vk-turn-server/main.go` 396 LoC**: `handleUDPConnection`, `handleVLESSConnection`,
  `pipeConn`, `prefixedConn` — бизнес-логика. Должны переехать в `internal/proxy/...`.
  Клиент симметрично уже сделан (`tcpfwd.Run`), сервер отстал.
- **Глобалы в server main** (`cmd/vk-turn-server/main.go:27-30`): `logger`,
  `globalBondRegistry`. Mutable package-vars вместо передачи через стек.
- **Asymmetry клиента**: UDP-режим разворачивается inline в
  `cmd/vk-turn-client/main.go:157-261`. Должен быть `udprelay.Run(...)` симметрично
  `tcpfwd.Run(...)`.
- **Лишние `Deps`-структуры**: bondserver/bondclient/udprelay/tcpfwd. Большая часть
  содержит только Logger + 1-2 поля. Три разных стиля инициализации.
- **Captcha-пакет монолит**: `manual.go` 892 / `slider.go` 627 / `captcha.go` 579 —
  смешаны HTTP-flow, parsing, PoW-mining, fingerprinting.
- **`internal/proxy/common`** существует ради 2 функций (`DialTURN`, `NewClientWrap`).
  Слить в `turndial`.

## 2. Корректность / баги

- **`internal/proxy/udprelay/udprelay.go:96`** — `strings.Contains(err.Error(), "context deadline exceeded")`.
  Match по строке стандартной ошибки. Должно быть `errors.Is(err, context.DeadlineExceeded)`.
  Tight-loop risk: ветка `continue` без backoff если oneDTLS быстро возвращает DeadlineExceeded.
- **`internal/proxy/udprelay/udprelay.go:170-178`** — бесконечная публикация одного
  и того же `conn2` в `connchan`. TURNLoop читает один раз. Должно быть одноразово.
- **`internal/proxy/bondserver/bondserver.go:269`** — `pending` map без лимита, DoS-risk
  при дырах в seq. У клиента лимит 1024 есть (`bondclient.go:240`).
- **`internal/client/vkauth/token.go:168-202`** — `context.WithTimeout(context.Background(), 3m)`
  для manual captcha. При отмене parent ctx goroutine `c.manualSolve(...)` живёт 3 мин.
  Goroutine-leak на shutdown.
- **`internal/client/captcha/captcha.go:466`** — `solveCaptchaPoW` ctx-check каждые 4096
  итераций. Cancel может опоздать на сотни мс.
- **`internal/wire/srtpmimicry/relay.go:22,38`** — `make([]byte, MaxWire(...))` на каждый
  ReadFrom/WriteTo. На горячем пути UDP — GC давление. В `listen.go` есть `bufPool`.
- **`internal/client/captcha/captcha.go:47`** — `captchaDebugCache sync.Map` без TTL/eviction.
- **`internal/client/vkauth/token.go:36`** — `tlsclient.NewHttpClient` пересоздаётся
  внутри `getTokenChain` на каждую попытку.
- **`cmd/vk-turn-server/main.go:209,219,240,250`** — magic `time.Minute*30` для
  read/write deadline. Bidir UDP-relay с 30-минутным dead-timeout повисает при простое.
- **`internal/client/vkauth/client.go:213-238`** — sentinel-errors через
  `strings.Contains(err.Error(), "...")`. Хрупко.

## 3. Stale-комментарии (refactor-history протёк в godoc)

- `internal/proxy/tcpfwd/tcpfwd.go:38-39` — "Bond client lives in main during stage 4.2;
  it will move to internal/bond/client in stage 5.1". Уже мигрировано.
- `internal/proxy/tcpfwd/pool.go:6` — "bond client half (still living in main during
  refactor stage 4.2)". Ложь.
- `internal/proxy/common/common.go:2-4` — ссылка на `notes/AUDIT_2026-05-19_V2.md`,
  файл удалён.
- `internal/config/config.go:7-10` — "V2-7: options are grouped by domain...".
  Стадия рефакторинга в публичном godoc.
- `internal/proxy/udprelay/udprelay.go:54-55` — "Equivalent of the old
  client/main.go turnParams". Историческая ссылка.
- `cmd/vk-turn-client/main.go:1-2` — `SPDX-FileCopyrightText: 2023 The Pion community`
  на основном proxy main. Ложный copyright.
- `internal/logx/logx.go:1-4` — описывает что заменило, а не что есть.
- `internal/proxy/bondclient/bondclient.go:3-4` — "Frame wire-format lives in internal/bond"
  → должно быть `internal/wire/bondframe`.

## 4. Нейминг

- `internal/wire/bondframe/bondframe.go:1` — godoc `// Package bond ...`, пакет `bondframe`.
- `srtpmimicry.Conn` — AEAD-кодек, не `net.Conn`. Лучше `Cipher`/`Codec`.
- `kcptun.DtlsPacketConn` / `NewDtlsPacketConn` — экспортированы, используются только внутри.
- `udprelay.Pool` — глобальная `sync.Pool` экспортирована, никто извне не дёргает.
- `bondclient.lane.dead atomic.Bool` vs `bondserver` slice-search remove — разные стили.
- `turndial.Stream` vs `tcpfwd.PooledSession` — концептуальное пересечение.
- `connchan` / `okchan` / `cchan` в client/main.go — нечитаемо.
- Magic numbers: `2000` (inboundChan), `1024` (recvCh), 4 MiB vs 16 KiB чтение/запись фрейма.

## 5. Прочее

- 134 прямых `log.Printf` в 13 файлах при наличии `logx.Logger`. Mix — нельзя
  глобально перенаправить вывод.
- `selfsign.GenerateSelfSigned()` на каждый `dtlsdial.Dial`. Кэшируй.
- Hardcoded VK secrets `internal/client/vkauth/types.go:21-27` (известно).
- Нет тестов: `bondclient`, `tcpfwd`, `udprelay`, `captcha/captcha.go` (auto-solver),
  `captcha/slider.go`. Самые гоночные компоненты без тестов.
- Артефакты в репо: `client.exe`, `server.exe`, `libvkturn.so` — в `.gitignore` нет.

## Приоритеты исправлений

1. **Bugs (P0):**
   1.1. `udprelay`: `errors.Is(DeadlineExceeded)` вместо string-match + одноразовый conn2 + backoff.
   1.2. `bondserver`: cap `pending`.
   1.3. `srtpmimicry.RelayPacketConn`: bufPool как в listen.go.
   1.4. Goroutine-leak `manualSolve` (на shutdown).

2. **Stale-комменты / нейминг (P1):**
   2.1. Убрать stage-4.2 / V2-* / notes/AUDIT_2026-05-19_V2.md / Pion-copyright / "Package bond".
   2.2. Stale ссылка `internal/bond` → `internal/wire/bondframe`.

3. **Sentinel errors (P1):** `var ErrXxx = errors.New(...)` + `errors.Is`.

4. **Server-main → internal (P2):** вынести handle*Connection / pipeConn / prefixedConn,
   убрать глобалы.

5. **`udprelay.Run(...)` симметрично tcpfwd.Run (P2).**

6. **Logger unification (P2):** заменить `log.Printf` на `logx.Logger`.

7. **`.gitignore`:** `*.exe`, `*.so`.
