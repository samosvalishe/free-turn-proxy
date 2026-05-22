<div align="center">

<img src="logo.webp" width="250" height="250">

![License](https://img.shields.io/badge/license-GPL--3.0-0D1117?style=flat-square&logo=gnu&logoColor=A42E2B&labelColor=0D1117)
![Go](https://img.shields.io/badge/-Go-0D1117?style=flat-square&logo=go&logoColor=00ADD8)
</div>

## О проекте

**btp** — туннель UDP/TCP через TURN-реле VK Calls. Клиент берёт временные TURN-учётки из ссылки на звонок и гонит ваш трафик (WireGuard, Xray/VLESS) до сервера на VPS поверх DTLS.

## Быстрый старт

См. [docs/quickstart.md](docs/quickstart.md). Полная документация — в [docs/](docs/README.md).

## Разработка

### Зависимости

- **Go** ≥ 1.25 — `https://go.dev/dl/`
- **Task** (runner) — `go install github.com/go-task/task/v3/cmd/task@v3.40.0` или `winget install Task.Task` / `brew install go-task`

Остальные dev-инструменты (`golangci-lint`, `govulncheck`, `goimports`, `goreleaser`) ставит сам Task:

```bash
task tools:install
```

### Команды

```bash
task                # список доступных задач
task build          # собрать client + server в dist/ для текущего хоста
task build:all      # кросс-сборка всех target через goreleaser snapshot
task test           # go test -race
task test:cover     # тесты + покрытие → cover.html
task lint           # golangci-lint
task fmt            # gofmt + goimports (форматирование)
task fmt:check      # проверить форматирование (используется в CI)
task vet            # go vet
task vuln           # govulncheck
task ci             # полный набор: fmt:check + vet + lint + test + vuln
task tidy           # go mod tidy
task clean          # удалить dist/, cover.out, cover.html
```

### Релизы

Авто-релизы через [Release Please](https://github.com/googleapis/release-please) + [GoReleaser](https://goreleaser.com).

[Conventional Commits](https://www.conventionalcommits.org/):

- `feat: ...` — minor bump
- `fix: ...` / `perf: ...` — patch bump
- `break: ...` (или `feat!:`) — major bump
- `refactor:`, `docs:`, `test:`, `chore:`, `ci:`, `style:` — в CHANGELOG не попадают (либо группа Refactoring)

Поток: push в `main` → Release Please откроет PR `chore(main): release X.Y.Z` с обновлённым `CHANGELOG.md` → merge PR → тег `vX.Y.Z` → GoReleaser выпускает релиз (бинари + Docker image на GHCR).

Pre-release: создать тег вручную `git tag v0.X.Y-rc.1 && git push --tags`. GoReleaser детектит автоматически (`latest` Docker тег не обновляется).

---

<div align="center">

Telegram канал: [Free Turn](https://t.me/+5BdkU4q_CGQyNTdi)

</div>
