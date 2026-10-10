# Тестирование

Как прогнать тесты.

## Тестирование

Локальные проверки:

```bash
gofmt -w internal
go test ./...
go vet ./...
```

Кросс-компиляция Linux amd64:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o conan-manager-linux-amd64 ./cmd/conan-manager
```

Smoke-тест на Ubuntu уже проверил:

- systemd deployment;
- SQLite CRUD;
- HTTP Basic Auth;
- SteamCMD;
- установку UE5;
- запуск и остановку Conan;
- реальные логи и порты;
- tmpfs;
- активную базу в RAM;
- сохранение backup-файлов на SSD.
