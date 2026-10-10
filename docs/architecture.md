# Архитектура и данные

Из чего собран менеджер и что он хранит на диске.

## Архитектура проекта

```text
cmd/conan-manager/main.go       точка входа
internal/config                  env-конфигурация
internal/store                   SQLite и миграции
internal/server                  модель и repository серверов
internal/controller              объединение lifecycle-компонентов
internal/process                 запуск/остановка Conan-процессов
internal/steamcmd                SteamCMD и установка Conan
internal/ramstate                RAM-каталог активной базы
internal/tmpfs                   проверка RAM mountpoint
internal/logreader               чтение последних строк логов
internal/backup                  копирование backup-файлов
internal/scheduler               interval/daily/cron и retry
internal/recoveryalert           JSON-alert на SSD
internal/security                шифрование секретов
internal/httpapi                 HTTP API и development auth
deploy/                          systemd и env-шаблоны
install.sh                       Ubuntu installer
```

## Структура данных на сервере

Рекомендуемая структура:

```text
/var/lib/conan-manager/
├── manager.db
├── steamcmd/
└── servers/
    └── <server-id>/
        └── game/
            ├── ConanSandbox/
            ├── Engine/
            ├── steamapps/
            └── ConanSandboxServer.sh

/run/conan-manager/
└── <server-id>/
    └── database/
        ├── game_0.db
        ├── game_0.db-wal
        └── game_0.db-shm
```


## Чего пока нет

Актуальный список - в [ROADMAP.md](../ROADMAP.md); здесь то, обо что проще
всего споткнуться:

- по умолчанию панель слушает `127.0.0.1:8080`; наружу - через Caddy или
  nginx, см. [install.md](install.md);
- задания установки не переживают перезапуск менеджера (история расписаний -
  переживает);
- внешние хранилища бэкапов (SFTP, rsync, S3) пока только схема данных, без
  рабочего транспорта; бэкапа по расписанию нет;
- счётчик игроков берётся по RCON; без него число игроков не показывается;
- схема с базой мира в tmpfs доведена до рабочей, но от неё отказались - см.
  [ram-disk.md](ram-disk.md);
- автоматического импорта сервера, заведённого мимо менеджера, нет и не
  планируется.
