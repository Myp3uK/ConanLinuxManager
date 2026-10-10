# HTTP API

Конечные точки JSON API.

## API

Текущие endpoint’ы:

```text
GET    /healthz
GET    /api/v1/info

GET    /api/v1/servers
POST   /api/v1/servers
GET    /api/v1/servers/{id}
PATCH  /api/v1/servers/{id}
DELETE /api/v1/servers/{id}

POST   /api/v1/servers/{id}/start
POST   /api/v1/servers/{id}/stop
POST   /api/v1/servers/{id}/install

POST   /api/v1/servers/{id}/restart         {"warn": true}
POST|DELETE /api/v1/servers/{id}/delayed-restart
POST   /api/v1/servers/{id}/kill
POST   /api/v1/servers/{id}/verify
GET    /api/v1/servers/{id}/update
POST   /api/v1/servers/{id}/update
POST   /api/v1/servers/{id}/backup
GET    /api/v1/servers/{id}/backups
DELETE /api/v1/servers/{id}/backups/{backup}
POST   /api/v1/servers/{id}/backups/{backup}/restore
POST   /api/v1/servers/{id}/rcon
GET    /api/v1/servers/{id}/players
GET    /api/v1/servers/{id}/hooks/runs
GET|PATCH|PUT /api/v1/servers/{id}/config/{ServerSettings|Game|Engine}
GET    /api/v1/servers/{id}/config/{file}/versions
POST   /api/v1/servers/{id}/config/{file}/versions/{v}/restore
GET|PUT|DELETE /api/v1/servers/{id}/schedule
GET    /api/v1/servers/{id}/schedule/runs

GET    /api/v1/jobs/{job-id}
GET    /api/v1/servers/{id}/logs?name=game&limit=200&filter=...
GET    /api/v1/events
GET    /api/v1/hooks
GET|POST /api/v1/notifications
GET|PATCH|DELETE /api/v1/notifications/{id}
POST   /api/v1/notifications/{id}/test
```

Подробности - в разделе «Управление серверами».

Долгая установка возвращает job:

```text
POST /api/v1/servers/{id}/install -> 202
GET  /api/v1/jobs/{job-id}
```

Сейчас jobs хранятся только в памяти процесса. После перезапуска менеджера
история jobs теряется.

Пример конфигурации сервера:

```json
{
  "id": "server-1",
  "name": "My Conan Server",
  "engine": "ue5",
  "runtime": "native",
  "install_dir": "/var/lib/conan-manager/servers/server-1/game",
  "steamcmd_dir": "/var/lib/conan-manager/steamcmd",
  "branch": "public",
  "launch_args": "-log -port=37777 -QueryPort=37015",
  "game_port": 37777,
  "query_port": 37015,
  "ramdisk_enabled": true,
  "ramdisk_path": "/run/conan-manager/server-1",
  "ramdisk_size_mb": 2048,
  "sync_interval_seconds": 300,
  "periodic_backup_interval_seconds": 2400
}
```

`PATCH /api/v1/servers/{id}` меняет настройки уже созданного сервера. Тело -
частичный JSON, отсутствующие поля не трогаются:

```json
{ "periodic_backup_interval_seconds": 2400 }
```

Меняются `name`, `branch`, `launch_args`, `game_port`, `query_port`,
`ramdisk_enabled`, `ramdisk_size_mb`, `sync_interval_seconds`,
`periodic_backup_interval_seconds`, `restart_on_crash`, `hooks`, `auto_update`
и `warnings` (`rcon_port` принимается, но устарел). Неизвестное поле отклоняется с 400, а не
игнорируется молча. `id`, `engine`, `runtime`, `install_dir`, `steamcmd_dir` и
`ramdisk_path` изменить нельзя: они идентифицируют состояние на диске, и их
подмена осиротила бы установку или RAM-сессию.

Запрос берёт ту же lifecycle-резервацию, что старт и удаление, поэтому на
работающем или занятом сервере он возвращает 409. Это не формальность: игра
читает свои INI только при старте, и правка на ходу выглядела бы принятой,
ничего не меняя до следующей остановки.
