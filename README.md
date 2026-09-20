# Conan Manager

Веб-менеджер для нескольких Conan Exiles Dedicated Server на одной Ubuntu-машине.
Проект находится на стадии технического прототипа: основной backend уже умеет
устанавливать и запускать UE5-сервер, но полноценная веб-морда, планировщик,
моды, RCON и production-защита ещё не реализованы.

## Цели проекта

Менеджер должен управлять несколькими серверами Conan Exiles на одном Linux-хосте:

- устанавливать и обновлять Conan через SteamCMD;
- поддерживать актуальный UE5 и Legacy UE4 через Wine/Proton;
- запускать, останавливать и перезапускать серверы;
- редактировать настройки и моды;
- выполнять плановые рестарты и проверки модов;
- читать логи только по запросу;
- делать резервные копии и отправлять их на другой сервер;
- уведомлять через Discord Webhook и RCON;
- запускать pre/post-скрипты;
- хранить активную Conan SQLite-базу в RAM без помещения всех backup-файлов в память.

## Текущее состояние

### Watchdog зависаний и логов

**Наблюдение за доступностью (A2S_INFO):** отдельная проверка раз в 15 секунд,
таймаут 3 секунды. После старта действует 10 минут ожидания загрузки; первый успешный
query завершает этот период раньше. После него непрерывная серия неудач длительностью
5 минут выставляет `health.availability.state=unavailable` и `would_recover=true`.
Короткие задержки дают только `suspect`; успешный ответ сбрасывает серию неудач.
Режим пока строго `observe`: query **никогда не останавливает и не перезапускает**
сервер. Это позволяет подобрать пороги по реальным задержкам сохранений и модов.
Наблюдение приостанавливается на остановке/сохранении, отменяет сетевой запрос при
выходе процесса и заново начинает период загрузки для следующего запуска.

В `health.availability` доступны адрес проверки, время проверки и последнего
успешного ответа, начало серии неудач, число ошибок и последняя ошибка.
Настройки окружения (см. `deploy/conan-manager.env.example`):
`CONAN_MANAGER_QUERY_HOST`, `CONAN_MANAGER_QUERY_INTERVAL`,
`CONAN_MANAGER_QUERY_TIMEOUT`, `CONAN_MANAGER_QUERY_STARTUP_GRACE`,
`CONAN_MANAGER_QUERY_UNAVAILABLE_FOR`. Длительности задаются как `15s`, `5m`, `10m`;
некорректные значения и значения меньше секунды заменяются значениями по умолчанию.
Порт берётся из `query_port` сервера и должен совпадать с реальными launch args:
автоматической передачи порта игре пока нет. При MULTIHOME нужно указать доступный
адрес вместо стандартного `127.0.0.1`. Ошибочный адрес/порт даёт недоступность,
а не доказанное зависание. Поддерживаются обычный A2S_INFO и challenge-handshake;
split/compressed-ответы пока дают диагностическую ошибку.
RCON и heartbeat игрового потока пока не подключены. Успешный query означает только
работоспособность query-обработчика и не сбрасывает hang-события из логов.

Каждые 5 секунд контроллер проверяет новые данные в `conan-manager-process.log`
и `ConanSandbox/Saved/Logs/ConanSandbox.log`. `Hang detected on GameThread`
вызывает SIGTERM, затем существующий watchdog отправляет SIGKILL через 180 секунд,
если процесс не вышел. Query-ответ не отменяет обнаруженное зависание.
После выхода и успешного сохранения RAM разрешены до трёх автоматических
перезапусков на сервер за время жизни менеджера. Ручной stop отменяет ожидающий
перезапуск. При ошибке сохранения автоматического старта нет.

Размер одного контролируемого лога от 256 MiB либо прирост более 1 MiB между
проверками вызывает остановку без автоперезапуска. Чтение ограничено 1 MiB на файл;
лог на 15 GiB определяется по метаданным, без загрузки содержимого. Старые hang-записи
на старте пропускаются. Причина и счётчик рестартов доступны в поле `health` GET сервера.
Это пороги прототипа, пока заданные в коде. Контроль размера является реактивным:
до завершения процесса файл может продолжать расти. Ротация, жёсткий лимит stdout,
контроль суммарного размера архивных логов и свободного диска ещё нужны.
Нестандартные имена игровых логов пока не отслеживаются. Зависание без указанного
сообщения не определяется: нужен дополнительный heartbeat именно игрового потока.
Unix zombie (уже завершившийся процесс) отличается от живого зависшего GameThread;
дочерний процесс менеджер забирает через `Wait`, это не проверка query.

### Уже реализовано

- Go backend;
- SQLite с WAL и миграцией схемы;
- модель нескольких серверов;
- модели UE5/native, UE4/Wine и UE4/Proton;
- создание, получение, список и удаление серверов через API;
- запуск и остановка процесса Conan;
- определение состояния процесса;
- отдельная process group на Unix;
- асинхронная установка сервера через SteamCMD job;
- блокировки lifecycle по ID, каноническим install/RAM-каталогам (включая вложенные пути) и SteamCMD-каталогу на время установки;
- ожидание выхода процесса и сохранения RAM перед освобождением блокировок, включая неожиданный выход Conan;
- поле `lifecycle` в ответах GET сервера и start/stop: состояние операции и ошибки сохранения;
- скачивание и распаковка SteamCMD для Linux;
- установка/обновление Conan Dedicated Server через `app_update 443030`;
- чтение последних строк логов по запросу;
- Basic Auth для текущего development API;
- заготовка AES-256-GCM для секретов backup-хранилищ;
- модели local/SFTP/rsync/S3 backup targets;
- systemd unit;
- install.sh для Ubuntu;
- tmpfs-проверка через `/proc/self/mountinfo`;
- RAM lifecycle для активной базы и WAL/SHM;
- автоматический перенос backup-файлов, случайно созданных в RAM рядом с symlink-базой;
- тесты repository, API, SteamCMD, backup, логов, tmpfs и RAM lifecycle.

### Не реализовано

Блокировки lifecycle действуют в пределах одного процесса менеджера. При ошибке
загрузки/сохранения RAM состояние `recovery_required` удерживает каталоги и запрещает
start/install/delete. Автоматическое восстановление и снятие такой блокировки через
API пока не реализованы; перезапуск менеджера не является восстановлением данных.
Состояния: `idle`, `starting`, `running`, `stopping`, `flushing`, `installing`,
`deleting`, `recovery_required`. Конфликтующие операции возвращают HTTP 409.
Одинаковые каталоги в конфигурациях разрешены, но использовать их одновременно нельзя.
Рабочая RAM-схема пока использует symlink и sweeper; bind mounts остаются экспериментом.

- frontend/web UI;
- полноценные пользователи, сессии и CSRF;
- HTTPS/reverse proxy configuration;
- визуальный редактор INI;
- Steam Workshop mod manager;
- генерация и проверка `modlist.txt`;
- проверка обновлений модов по расписанию;
- планировщик рестартов;
- RCON client;
- Discord Webhook notifications;
- pre-start/post-start и другие scripts;
- удалённая отправка backup-файлов;
- восстановление backup через API;
- автоматическая установка Wine/Proton;
- проверенная поддержка UE4;
- постоянное хранение job history в SQLite;
- мониторинг игроков и готовности сервера;
- полноценная диагностика портов и зависимостей.

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

## RAM и SQLite

### Правильная цель

В RAM должны находиться только активные SQLite-файлы:

```text
game_0.db
game_0.db-wal
game_0.db-shm
```

В RAM не должны постоянно находиться:

- `game_0_backup_*.db`;
- логи;
- `ServerSettings.ini`;
- `Engine.ini`;
- `Game.ini`;
- другие файлы `Saved`.

Пример для базы размером 400 MB и четырёх серверов:

```text
4 × 400 MB активных баз ≈ 1.6 GB
```

Backup-файлы не должны умножать расход RAM: они хранятся на SSD.
Параметр размера tmpfs является верхним ограничением, а не предварительным
резервированием всей указанной ёмкости.

### Текущая реализация прототипа

Сейчас `internal/ramstate`:

1. оставляет основную директорию `ConanSandbox/Saved` на SSD;
2. создаёт `/run/conan-manager/<id>/database`;
3. копирует туда `game_0.db` и существующие `-wal`/`-shm`;
4. заменяет активные файлы в `Saved` symlink-файлами;
5. после запуска следит за backup-файлами, которые SQLite может создать рядом
   с разрешённым symlink-путём;
6. переносит такие backup-файлы обратно на SSD каждые несколько секунд;
7. после остановки копирует активную базу и sidecar-файлы обратно на SSD.

На `node2` эта схема была проверена на реальном Conan UE5:

```text
RAM:
database/game_0.db
database/game_0.db-wal
database/game_0.db-shm

SSD:
Saved/game_0_backup_1.db
Saved/game_0_backup_2.db
Saved/game_0_backup_3.db
Saved/Logs/
Saved/Config/
```

### Следующее обязательное улучшение RAM-схемы

Кандидат для дальнейшего исследования — bind mount отдельных файлов:

```text
Saved/game_0.db      bind mount из tmpfs
Saved/game_0.db-wal  bind mount из tmpfs
Saved/game_0.db-shm  bind mount из tmpfs
```

Цель: сохранить путь базы внутри обычной SSD-директории `Saved`, чтобы backup
создавался на SSD. Это ещё не подтверждённое решение для Conan.

Изолированный тест `scripts/test-sqlite-bind.py` на node2 (SQLite 3.45.1)
подтвердил три цикла записи, checkpoint TRUNCATE, закрытия/повторного открытия
и создание целостной backup-базы на SSD. Но попытка удалить смонтированные
WAL/SHM возвращает `EBUSY` (Device or resource busy). SQLite/Conan могут
удалять или пересоздавать sidecar-файлы, поэтому нельзя обещать совместимость
bind mounts без тестирования штатного закрытия, восстановления и обслуживания
именно Conan. Предыдущее утверждение об «идеальной production-схеме» было преждевременным.

Тест запускается отдельно от игры и менеджера:

```bash
sudo unshare --mount --propagation private python3 scripts/test-sqlite-bind.py
```

Все mountpoints создаются во временном каталоге и снимаются в конце теста.

### Последняя проверка сохранности RAM

#### Подготовка process lifecycle к bind mounts

В исходниках ожидание SIGTERM увеличено с 30 до 180 секунд. Контроллер ждёт
завершения до 210 секунд перед Flush. Пока это константа `process.StopTimeout`;
настройка через UI/env и согласование остановки всего systemd-сервиса ещё нужны.

Start в process manager сериализован: конкурентные запросы не создают несколько
процессов с одним ID. Запуск при `stopping` отклоняется. Повторный Stop безопасен
и не отправляет сигнал уже завершённому PID. Завершённый процесс не выдаёт старый
PID в snapshot; `forced_stop` показывает успешное применение SIGKILL watchdog-ом.

Linux-тест `TestConcurrentStartAndStopping` выполнен на node2: 12 конкурентных
запусков дали один дочерний процесс; отмена request context его не завершила;
повторный запуск во время остановки отклонён; Wait дождался реального выхода.
Это тест process manager, а не гарантия сериализации RAM/SteamCMD: блокировки
на уровне controller и shared install_dir всё ещё необходимы.

Изменения сервиса на node2 не установлены; менеджер остаётся остановленным.

#### Эксперимент с реальным Conan и bind mounts

`scripts/test-conan-bind.py` выполнен на node2 с копией остановленной базы,
отдельной SSD Saved и tmpfs 512 MiB в private mount namespace. Менеджер был
остановлен. Тест использовал loopback MULTIHOME и порты 38777/38015.

Результаты:

- Наблюдались две генерации автоматических backup с промежутком не менее
  четырёх минут. Копии появились непосредственно на SSD, без sweeper.
- В tmpfs оставались только `game_0.db`, `game_0.db-wal`, `game_0.db-shm`.
- Проверки `PRAGMA integrity_check` копий backup вернули `ok`.
- Остановка по SIGTERM: выход 143, ожидание 141 секунду, SIGKILL не применялся.
- Повторный запуск, намеренный SIGKILL и повторный запуск прошли. Процесс
  после восстановления оставался жив через 90 секунд.
- После следующей остановки (136 секунд) integrity_check активной базы и
  всех трёх найденных backup вернул `ok`.
- Первый тест не смог выполнить umount SHM из-за открытых соединений самого
  Python-проверяющего: context manager SQLite не закрывает connection.
  Исправлено через `contextlib.closing`.
- Дополнительный реальный запуск/остановка подтвердил integrity `ok` и
  успешное снятие всех mounts; исходный Saved восстановлен в namespace.

Отчёты на node2:

```text
/var/tmp/conan-bind-2gp21gir/report.json   основной тест и ошибка cleanup
/var/tmp/conan-bind-grw_unpc/report.json  повторная проверка cleanup: успешно
```

В этих каталогах сохранены тестовые логи и финальные копии DB/WAL/SHM.
Тестовая база небольшая (около 650 KiB), игроков и модов не было: результат
не является нагрузочным тестом базы 400 MB и не моделирует отключение питания.

**Решение:** продолжить интеграцию файловых bind mounts вместо symlink+sweeper
для проверенного UE5. Следующий этап — privileged helper, блокировки операций,
сохранение согласованной DB/WAL на SSD, recovery-маркеры и отображение ошибок.
Установленный менеджер пока использует старую схему и остаётся остановленным.
Нужно заменить 30-секундный watchdog на настраиваемое ожидание (начальное
значение не менее 180 секунд для этой сборки), согласовать его с systemd и
ожиданием Flush. Сам по себе exit 143 не доказывает полноценное сохранение:
в тесте дополнительно проверялась целостность SQLite.

- `Load` теперь отказывается работать, если DB/WAL/SHM ещё являются symlink:
  это предотвращает очистку единственной актуальной RAM-базы при повторном старте.
- Ошибки копирования WAL/SHM больше не игнорируются в `Load`.
- Linux-тест проверяет отказ повторного Load, сохранность новых данных и
  цикл Flush → Load. Все три теста пакета ramstate выполнены на node2.
- Исправлен тест, который ошибочно ожидал копирования backup из RAM при Flush:
  retained backup проверяется в обычной SSD Saved.
- Изменения пока не развёрнуты в сервис. Менеджер остаётся остановленным.

Открытые проблемы: sweeper может копировать ещё записываемый backup; его ошибки
и ошибки Flush не отображаются пользователю. Нужны блокировки операций на сервер
и каталог, восстановление после сбоя и проверка целостности копий. Короткий
smoke-тест не подтверждает надёжность этих сценариев.

## Реально проверенный UE5

На тестовом Ubuntu-сервере `node2`:

```text
Ubuntu 24.04.4 LTS
x86_64
Kernel 6.8.0-139-generic
systemd 255
RAM 15 GiB
```

SteamCMD успешно подключается анонимно к Steam.

Conan UE5 установлен в:

```text
/var/lib/conan-manager/servers/ue5-test/game
```

Размер установки — примерно 4 GiB.

Фактический executable:

```text
ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping
```

Фактический launcher:

```text
ConanSandboxServer.sh
```

Менеджер запускает Linux executable напрямую с аргументом `ConanSandbox`,
чтобы корректно отслеживать процесс и process group.

Проверено:

- запуск UE5 после HTTP-запроса;
- работа процесса после завершения HTTP-запроса;
- UDP game port;
- UDP query port;
- чтение Conan log по API;
- graceful stop;
- watchdog принудительной остановки;
- закрытие портов после завершения;
- создание `game_0.db`;
- создание WAL/SHM;
- создание Conan backup-файлов;
- RAM lifecycle.

## SQLite-структура Conan UE5

Установленный сервер использует:

```text
ConanSandbox/Saved/game_0.db
ConanSandbox/Saved/game_0.db-wal
ConanSandbox/Saved/game_0.db-shm
ConanSandbox/Saved/game_0_backup_1.db
ConanSandbox/Saved/game_0_backup_2.db
ConanSandbox/Saved/Logs/ConanSandbox.log
ConanSandbox/Saved/Config/LinuxServer/ServerSettings.ini
ConanSandbox/Saved/Config/LinuxServer/Engine.ini
```

Нельзя использовать простой `cp` для постоянно изменяемой базы как способ
гарантировать консистентность. Управляемый stop должен дождаться завершения
Conan, после чего сохранять базу и sidecar-файлы.

## API

Текущие endpoint’ы:

```text
GET    /healthz
GET    /api/v1/info

GET    /api/v1/servers
POST   /api/v1/servers
GET    /api/v1/servers/{id}
DELETE /api/v1/servers/{id}

POST   /api/v1/servers/{id}/start
POST   /api/v1/servers/{id}/stop
POST   /api/v1/servers/{id}/install

GET    /api/v1/jobs/{job-id}
GET    /api/v1/servers/{id}/logs?path=...&limit=200
```

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
  "sync_interval_seconds": 300
}
```

## Аутентификация и безопасность

Сейчас используется Basic Auth:

```text
CONAN_MANAGER_ADMIN_USERNAME
CONAN_MANAGER_ADMIN_PASSWORD
```

`/healthz` доступен без авторизации.

Если один из параметров Basic Auth пустой, management API блокируется с HTTP 503
(fail closed); это предотвращает случайный запуск API без защиты. Для запросов с
заголовком `Origin` изменяющие методы принимают только same-origin запросы.
Также устанавливаются базовые security-заголовки и ограничивается размер POST body
до 1 MiB.
Если API работает за reverse proxy, публичный origin можно явно указать через
`CONAN_MANAGER_ALLOWED_ORIGIN`.

Перед доступом из интернета обязательно нужно добавить:

- HTTPS через Caddy или Nginx;
- session-based login;
- Argon2id password hashes;
- CSRF tokens;
- SameSite cookies;
- rate limiting;
- IP allowlist;
- журнал действий;
- запрет передачи секретов во frontend.

Basic Auth без HTTPS нельзя считать достаточной production-защитой. Для production
нужно использовать HTTPS через Caddy или Nginx и хранить env-файл с правами 0600.

## Установка Ubuntu

Сборка для Ubuntu amd64:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o conan-manager ./cmd/conan-manager
sudo ./install.sh
```

`install.sh`:

1. проверяет права root;
2. проверяет системные команды;
3. создаёт пользователя `conan-manager`;
4. создаёт `/opt/conan-manager`;
5. создаёт `/var/lib/conan-manager`;
6. создаёт `/etc/conan-manager`;
7. генерирует env-файл с секретами;
8. устанавливает systemd unit;
9. включает и перезапускает сервис.

Основные файлы:

```text
/opt/conan-manager/conan-manager
/etc/conan-manager/conan-manager.env
/var/lib/conan-manager/manager.db
/etc/systemd/system/conan-manager.service
```

Управление сервисом:

```bash
systemctl start conan-manager.service
systemctl stop conan-manager.service
systemctl restart conan-manager.service
systemctl status conan-manager.service
journalctl -u conan-manager.service -f
```

## Остановка тестового сервиса

На момент подготовки этой документации `conan-manager` на `node2` остановлен:

```bash
systemctl stop conan-manager.service
```

Порт API `127.0.0.1:8080` закрыт. Тестовый Conan-процесс также остановлен,
игровые порты свободны.

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

## План оставшейся разработки

### Этап 1 — исправить RAM production-схему

1. заменить symlink активных файлов на bind mounts;
2. сделать systemd/helper lifecycle для mount/umount;
3. не запускать сервер, если bind mounts не подготовлены;
4. атомарно вернуть базу на SSD при остановке;
5. обработать reboot/crash recovery;
6. добавить проверку свободной RAM и размера базы.

### Этап 2 — конфигурация Conan

1. редактор `ServerSettings.ini`;
2. редактор `Engine.ini` и `Game.ini`;
3. backup конфигов перед изменением;
4. история изменений;
5. шаблоны настроек;
6. безопасная проверка путей и аргументов.

### Этап 3 — моды

1. таблица модов;
2. Steam Workshop IDs;
3. установка модов через SteamCMD;
4. проверка обновлений;
5. генерация `modlist.txt`;
6. порядок загрузки;
7. ручной режим управления модами;
8. рестарт после обновления.

### Этап 4 — планировщик и уведомления

1. плановые рестарты;
2. предупреждения игрокам;
3. определение количества игроков;
4. RCON;
5. Discord Webhook;
6. pre/post scripts;
7. аварийное восстановление.

В текущем milestone добавлен persistent interval scheduler для действий `start`,
`stop` и `restart`. Расписание хранится в SQLite и управляется через
`GET/PUT/DELETE /api/v1/servers/{id}/schedule`. Поле `next_run` передаётся в
UTC RFC3339, минимальный `interval_seconds` — 60. Scheduler вызывает обычные
controller-операции, поэтому применяются те же lifecycle locks, launcher handoff
и блокировка `recovery_required`; расписание не является обходом offline recovery.
История попыток доступна через `GET /api/v1/servers/{id}/schedule/runs` и хранит
статус `succeeded` или `failed`, причину и timestamps.
После ошибки scheduler делает до трёх повторов с backoff `1m`, `2m`, `4m`; после
исчерпания повторов возвращается к обычному интервалу расписания.
После простоя manager просроченное расписание выполняется один раз, а следующий
запуск рассчитывается от текущего времени; пропущенные интервалы не запускаются
серией catch-up операций.

Календарный режим задаётся через тот же PUT endpoint:
```json
{"action":"restart","enabled":true,"mode":"daily","daily_time":"03:00","next_run":"2026-09-21T03:00:00Z"}
```
`daily_time` — время UTC в формате `HH:MM`; `next_run` явно задаёт первую попытку.
В daily-режиме можно опустить `interval_seconds` (в хранилище используется 86400).
После попытки следующий календарный запуск выбирается строго после текущего времени;
retry временно использует задержки 1/2/4 минуты. Счётчики retry пока живут в памяти
и сбрасываются при перезапуске manager.

Поддержан пяти-полевой cron-режим с IANA timezone:
```json
{"action":"restart","enabled":true,"mode":"cron","cron_expression":"0 3 * * 1-5","timezone":"Europe/Berlin","next_run":"2026-09-21T01:00:00Z"}
```
Поля cron: `minute hour day-of-month month day-of-week`; диапазоны, списки,
`*` и шаги поддерживаются. Сложные расширения (`L`, `W`, `#`) не поддерживаются.

### Этап 5 — backup storage

1. backup history в SQLite;
2. локальное хранилище;
3. SFTP;
4. rsync по SSH;
5. SSH key authentication;
6. зашифрованные пароли;
7. retention policy;
8. восстановление конкретной копии.

В текущем milestone добавлена локальная retention policy для Conan backup-файлов:
`CONAN_MANAGER_BACKUP_RETENTION` сохраняет указанное число самых новых файлов с
именем, содержащим `backup`, и расширением `.db`, `.bak` или `.backup`. Активная
`game.db` в retention не участвует. Очистка выполняется во время monitor tick и
не затрагивает каталоги recovery-сессии.

При переходе сервера в `recovery_required` менеджер также атомарно создаёт
`.conan-manager-recovery_required.json` рядом с persistent ConanSandbox. Файл
переживает перезапуск процесса и содержит server ID, причину и timestamp; он
удаляется только после успешного offline `conan-recover`. Это позволяет
systemd timer или внешнему мониторингу обнаружить незавершённое восстановление
без чтения journal.

### Этап 6 — web UI и production security

1. dashboard;
2. список серверов;
3. start/stop/restart;
4. логи по кнопке;
5. редактор настроек;
6. моды;
7. jobs и история;
8. полноценная авторизация;
9. CSRF;
10. HTTPS/reverse proxy;
11. IP allowlist.

### Этап 7 — UE4

1. установка Wine/Proton;
2. compatibility prefix;
3. отдельные runtime-настройки;
4. запуск Legacy dedicated server;
5. проверка портов и логов;
6. отдельный smoke-тест на Ubuntu.

## Важные ограничения текущего прототипа

- приложение сейчас слушает `127.0.0.1:8080`;
- frontend отсутствует;
- Basic Auth не заменяет HTTPS и session auth;
- jobs не сохраняются после перезапуска;
- bind mount-файлы ещё не реализованы;
- UE4 не тестировался;
- backup targets пока являются схемой данных, но не рабочим transport layer;
- нет автоматического планировщика;
- нет RCON и Discord;
- нет автоматического импорта существующего Conan-сервера;
- перед production-развёртыванием нужно реализовать корректный crash/reboot recovery
  для активной базы.
# Завершение менеджера

## Долговечный маркер RAM-сессии

Перед изменением файлов Saved новая RAM-сессия создаёт на SSD каталог
`ConanSandbox/.conan-manager-ram-session/`. В `baseline/` сохраняется исходный набор
`game_0.db`, `game_0.db-wal`, `game_0.db-shm` (существующие файлы), а в
`manifest.json` — версия, ID сервера, RAM-путь и перечень файлов. Файлы и каталоги
синхронизируются на диск до удаления исходных файлов Saved. Это копия на момент
старта, не актуальный онлайн-бэкап: после потери tmpfs прогресс текущей сессии
может быть потерян.

Само наличие каталога, даже без готового manifest, блокирует start/install/delete.
После перезапуска менеджера GET сервера показывает `recovery_required`. Блокировка
действует также для другой записи сервера с той же установкой.

При штатном сохранении после выхода игрового процесса весь набор RAM-файлов
сначала копируется в отдельный `flush-*` на SSD. Только после успешного копирования
заменяются файлы Saved; отсутствующие sidecar-файлы удаляются. Маркер остаётся до
синхронизации Saved, затем атомарно переименовывается в
`.conan-manager-ram-session.completed`. Последняя завершённая сессия сохраняется
для диагностики и заменяется при следующем успешном flush. Требуется дополнительное
место на SSD для baseline, staged snapshot и предыдущей завершённой сессии.

При ошибке или пропаже RAM-базы flush не считается успешным. Не удаляйте маркер
для обхода блокировки: сначала убедитесь, что Conan завершён, сохраните копии
RAM-каталога и каталога сессии и выберите согласованный набор DB/WAL для проверки
SQLite и восстановления. Восстановление через API не выполняется намеренно: оно
остаётся отдельной offline-операцией. Bind-mount helper используется launcher-путём;
старые RAM-сессии без manifest не мигрируются автоматически.

### Офлайн-восстановление

Сборка: `go build -o conan-recover ./cmd/conan-recover`.
На node2 утилита установлена в `/opt/conan-manager/conan-recover`.
Остановите менеджер и все процессы Conan, исключите их параллельный запуск на всё
время восстановления. Команда проверяет `/proc` перед началом, но не блокирует
внешние запуски процессов. Запускайте от root:

```sh
systemctl stop conan-manager.service
/opt/conan-manager/conan-recover --server ue5-test \
  --install-dir /var/lib/conan-manager/servers/ue5-test/game \
  --ram-dir /run/conan-manager/ue5-test --source ram
```

`--source ram` выбирает оставшиеся RAM-файлы; `--source baseline` явно выбирает
исходную SSD-копию и теряет прогресс после старта сессии. Автоматического fallback нет.
ID и RAM-путь должны совпадать с manifest. Утилита копирует DB/WAL в отдельный
каталог, пересоздаёт SHM, выполняет `PRAGMA integrity_check` и WAL checkpoint.
Только проверенная база устанавливается в Saved; владелец берётся от каталога Saved.
Ошибка проверки сохраняет маркер и не меняет Saved. После успеха каталог сессии
архивируется как `.conan-manager-ram-session.recovered-*`, исходные RAM-файлы
остаются. Архивы не удаляются автоматически. После проверки результата можно
запустить менеджер и затем сервер.

Disposable API-проверка recovery выполняется в `scripts/test-api-launcher.py`:
она проверяет блокировку start/delete при активном marker. Низкоуровневые проверки
выбора `ram`/`baseline`, SQLite integrity, WAL checkpoint, сохранения marker при
ошибке и архивирования успешной сессии находятся в
`internal/ramstate/*_test.go`.
Полный disposable CLI-дрилл `scripts/test-recovery-cli.py` запускает реальный
`conan-recover` в offline-режиме на временных каталогах и проверяет оба источника
(`ram` и `baseline`) вместе с архивированием marker.

## Порядок остановки

При SIGTERM/SIGINT менеджер закрывает приём новых lifecycle-операций и отменяет
ожидающие автоперезапуски. Работающим серверам отправляется остановка без
последовательного ожидания каждого сервера. Менеджер ждёт завершения процессов,
остановки query-наблюдения и финального переноса RAM-данных на SSD. Уже начатые
установки и другие lifecycle-операции также входят в ожидание.

Общий бюджет завершения — 5 минут, включая остановку Conan (до 180 секунд перед
SIGKILL) и сохранение. В systemd используются `KillMode=mixed` и
`TimeoutStopSec=330`: первоначальный SIGTERM получает только менеджер, а управление
игровыми процессами остаётся у controller. Main не завершает процесс сразу после
закрытия HTTP listener.

Ошибка сохранения, состояние `recovery_required` или истечение бюджета дают код
выхода 2 и сообщение `manager shutdown incomplete` в journal. Для этого кода
автоматический рестарт systemd запрещён (`RestartPreventExitStatus=2`). Нужно
проверить RAM-данные перед ручным запуском. Это не заменяет долговечный recovery
manifest: сбой питания, SIGKILL и перезагрузка с несохранённым tmpfs по-прежнему
требуют отдельного механизма восстановления.

Проверка установленного API без изменения серверов: `sudo python3 scripts/check-api.py`.
Скрипт читает credentials из локального env-файла и не выводит пароль.
