# Сборка

Нужен Go 1.25.x.

```bash
go build -o client ./cmd/client
go build -o server ./cmd/server
go test ./...
```

## Кросс-Сборка

**Linux amd64:**

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o server-linux-amd64 ./cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o client-linux-amd64 ./cmd/client
```

**Android arm64 как `.so`** (упаковка в приложение, PowerShell):

```powershell
$env:GOOS="android"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"
go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn.so .\cmd\client
```

Bash:

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn.so ./cmd/client
```

**Linux arm64 как `.so`** (в обход NDK, ABI совпадает):

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn.so ./cmd/client
```

**Linux amd64 как `.so`:**

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn-client.so ./cmd/client
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -checklinkname=0" -trimpath -o libvkturn-server.so ./cmd/server
```

> Все `.so` выше — обычные ELF под `.so`-именем (`CGO_ENABLED=0`, не настоящий shared library). Настоящий `c-shared` — `CGO_ENABLED=1` + NDK toolchain (см. `.github/workflows/release.yml`).
