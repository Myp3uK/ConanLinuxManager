# План оставшихся работ

Сверено с исходниками после итерации «стадии 0-2».
Отметка [x] означает реализованный объём. Пункты раздела 1 дополнительно
подтверждены acceptance-тестами на Linux-хосте `node2` (Ubuntu 24.04, реальный
Conan UE5): миграция существующей БД, scheduler, retention, recovery drill и
bind mount в private mount namespace.

## 1. Зафиксировать текущий milestone на Linux

- [x] Отправить implementation commits до `e9795f1` в `origin/main`.
- [x] Проверить миграцию SQLite на существующей установке `node2`.
- [x] Проверить scheduler на Linux: `start`, `stop`, `restart`, перезапуск manager
      и корректное завершение scheduler при SIGTERM.
- [x] Выполнить disposable-тест retention на реальных Conan backup-файлах.
- [x] Выполнить disposable recovery drill с recovery alert-файлом.
- [x] Проверить bind-helper на Linux-хосте с private mount namespace.

## 1a. Управление серверами

Проверено на `node2` с реальным Conan UE5; Valheim на хосте не затрагивался.

- [x] Запускать каждый сервер отдельным systemd-юнитом `conan-server@<id>`:
      перезапуск или обновление менеджера сервер не останавливает (PID
      сохраняется), менеджер подхватывает работающие юниты при старте.
- [x] Восстанавливать после перезагрузки хоста серверы, которые оператор
      оставил запущенными (`desired_state`).
- [x] Перезапускать упавший сервер (`restart_on_crash`): задержка 15 с × попытка,
      после 3 падений за 30 минут сервер остаётся выключенным с уведомлением.
- [x] Останавливать по схеме «SIGTERM → 5 минут → kill → `PRAGMA
      integrity_check`». Повреждённая база блокирует start/install до
      `POST /servers/{id}/verify`; блокировка переживает перезапуск менеджера.
- [x] Передавать игре `-Port`/`-QueryPort` из конфигурации: без них два сервера
      на одном хосте занимали порты по умолчанию.
- [x] `restart` ждёт остановки до 40 минут вместо 2 и не оставляет сервер
      выключенным.
- [x] Хуки `pre/post_start`, `pre/post_stop`, `pre/post_update`,
      `pre/post_backup`, `on_ready`, `on_crash`: только root-скрипты из
      `/etc/conan-manager/hooks`, режимы gate/wait/async, минимальное окружение
      без секретов менеджера, timeout с убийством группы процессов, история
      запусков в SQLite.
- [x] Шина событий и история событий в SQLite (`GET /api/v1/events`).
- [x] Уведомления Discord: каналы через API, webhook хранится зашифрованным и
      маскируется в ответах, повтор на 429/5xx, упоминание только на ошибках,
      ru/en. Живая отправка ещё не проверена — нужен webhook.
- [x] Редактор `ServerSettings.ini`/`Game.ini`/`Engine.ini` с сохранением
      форматирования и историей версий (50 на файл); правка только на
      остановленном сервере, откат к любой версии.
- [x] Обновления: проверка сборки через SteamCMD, задание «stop → update →
      start», `auto_update`, периодическая проверка
      (`CONAN_MANAGER_UPDATE_CHECK_INTERVAL`).
- [x] Прагмы SQLite (`busy_timeout`, `foreign_keys`, WAL) задаются в DSN для
      каждого соединения пула, а не только для первого.
- [ ] RCON: клиент (Source RCON, UTF-8), CLI `conan-manager rcon -a -p -P -q`,
      `POST /servers/{id}/rcon`, `GET /servers/{id}/players`, настройки из
      `[RconPlugin]` в `Game.ini`, предупреждения игрокам (`warnings`) перед
      плановым stop/restart, автообновлением и по `"warn": true`, с досрочным
      завершением, когда игроков нет. **Код и тесты готовы; не проверено на
      живом Conan UE5** — формат `listplayers` и имя команды `broadcast`.
- [x] Бэкапы менеджера: `VACUUM INTO` с работающего сервера без остановки
      (проверено на живой базе node2: 23 мс), `quick_check`, sha256, хуки
      `pre/post_backup`, хранение в `CONAN_MANAGER_BACKUP_DIR/<id>` (можно на
      другом диске), `CONAN_MANAGER_BACKUP_KEEP`, бэкап перед каждым
      обновлением, восстановление с бэкапом `pre-restore` и снятием
      `recovery_reason` после `integrity_check`.
- [x] Логи: чтение с конца (цена не зависит от размера лога), имена `game` и
      `process`, фильтр по подстроке, проверка пути после раскрытия симлинков.
- [x] Добавление и удаление: проверка ID (имя юнита и путей), отказ при
      пересечении портов (`game_port`, `game_port+1`, `query_port`) и каталогов
      с другим сервером; удаление убирает спецификацию юнита и alert, файлы и
      бэкапы — только по `?files=true`/`?backups=true`, с защитой от удаления
      общих и корневых каталогов.
- [x] Документация README: раздел «Управление серверами», список API,
      переменные окружения в `deploy/conan-manager.env.example`.
- [ ] Бэкап по расписанию (расписание сейчас одно на сервер и только
      start/stop/restart).

## 2. Надёжность scheduler

- [x] Сохранять завершённые попытки scheduler в SQLite: timestamps, result и error.
- [x] Добавить in-memory retry/backoff 1/2/4 минуты для ошибок controller.
- [x] Не воспроизводить все пропущенные интервалы при успешном продвижении next_run.
- [x] Добавить атомарный claim задания и защиту от повторов после crash/ошибки БД.
- [x] Обрабатывать ошибки записи истории/next_run, фиксировать running/interrupted,
      дождаться завершения scheduler при shutdown.
- [x] Сохранять retry-счётчики, различать постоянные и временные ошибки,
      учитывать изменение/удаление расписания во время выполнения.
- [x] Проверить длительные операции: актуальность next_run и блокирование очереди.
- [x] Добавить календарное расписание daily HH:MM UTC и миграцию interval schedules.
- [x] Добавить пяти-полевой cron и поддержку IANA часовых поясов.
- [x] Исправить fallback при отсутствии cron-совпадения; проверить leap-year,
      невозможные даты, DST, шаги и DOM/DOW semantics.
- [ ] Добавить cron-расширения `L`, `W`, `#` при необходимости.
- [ ] Добавить проверку active players перед плановым stop/restart (код готов,
      ждёт живой проверки RCON — см. 1a).
- [x] Добавить pre/post scripts с ограниченными окружением, timeout и логированием
      (см. раздел 1a, «Хуки»).

## 2a. RAM-диск и развёртывание

Исправлено в итерации «стадии 0-2»:

- [x] Заменить нерабочий шаблон `conan-manager-tmpfs@.mount` генератором
      `scripts/provision-ramdisk.sh`: systemd не поддерживает template-юниты для
      `.mount`, имя выводится из пути монтирования и шаблон отвергался как
      `Invalid argument`. Генератор добавляет `uid=`/`gid=`, без которых корень
      tmpfs принадлежит root и непривилегированный менеджер не может писать.
- [x] Добавить `RuntimeDirectory=` в unit менеджера: `/run` — tmpfs, и после
      перезагрузки `/run/conan-manager` исчезал, а `install.sh` создаёт его
      только при установке.
- [x] Не удалять backup-файл из RAM, пока игра его ещё пишет: дренаж копировал
      и сразу удалял исходник без проверки завершения записи.
- [x] Освобождать RAM после подтверждённого сброса (`ReleaseRAM`), чтобы
      остановленный сервер не удерживал базу в tmpfs.
- [x] Остановить сервер при заполнении tmpfs выше `RamdiskUsageLimit`.
- [x] Разделить частый дренаж RAM и редкую очистку retention.
- [x] Давать каждой копии при дренаже уникальное имя со временем записи. В
      symlink-режиме дренаж забирает бэкап из RAM раньше, чем игра его
      ротирует, поэтому игра всегда пишет `game_0_backup_1.db`, и дренаж под
      исходным именем затирал предыдущую копию: на SSD оставался один файл
      возрастом в один цикл вместо истории. Проверено на `node2`: три цикла с
      каденцией 180 с дают три файла.

Осталось:

- [ ] Сделать bind mount режимом по умолчанию: он монтирует три файла по
      отдельности, каталог `Saved` остаётся на SSD, и бэкапы ложатся туда
      напрямую, минуя RAM (проверено `scripts/test-conan-bind.py` с реальным
      Conan). В symlink-режиме игра разрешает ссылку и пишет бэкап рядом с
      базой в RAM, поэтому там нужен дренаж. Настройки каталога бэкапов у игры
      нет (см. README, «Каденция бэкапов Conan»).
- [ ] Автоматизировать provisioning bind-режима (JSON, unit, polkit) из API.

## 2b. Конфигурация игры

- [x] Записывать `PeriodicBackupInterval` в `Game.ini` перед стартом — это
      главный рычаг износа SSD: каждый цикл копирует базу целиком, и при базе
      435 МБ стоковая каденция ~480 с стоит ~3.2 ГБ записи в час.
- [x] Добавить `PATCH /api/v1/servers/{id}`: сервер вообще нельзя было
      отредактировать после создания, только удалить и создать заново.
- [ ] Перенести в менеджер остальные проверенные настройки `ServerSettings.ini`
      (после того, как каждая будет проверена на остановленном сервере — правка
      на ходу теряется).

## 3. Backup storage

- [x] Завершить backup history в SQLite: checksum, размер, source, destination,
      причина и timestamp (записываются только успешные копии; ошибки — в
      событиях).
- [ ] Реализовать рабочий local backup target.
- [ ] Реализовать SFTP target.
- [ ] Реализовать rsync over SSH target.
- [ ] Реализовать S3-compatible target.
- [ ] Добавить SSH key authentication.
- [ ] Подключить шифрование секретов backup targets через master key.
- [ ] Добавить проверку свободного места и атомарную отправку backup-файлов.
- [x] Добавить восстановление конкретной backup-копии
      (`POST /servers/{id}/backups/{backup}/restore`, без RAM-диска).
- [ ] Расширить retention: age/size limits, dry-run и отчёт об удалённых файлах.

## 4. Recovery и alerting

- [x] Добавить best-effort JSON recovery alert и очистку после offline recovery.
- [x] Обрабатывать ошибки записи/очистки alert, добавить fsync и обнаружение
      оставшихся markers без необходимости HTTP-запроса (`recoveryalert.Pending`).
- [ ] Подключить recovery alert к внешнему уведомлению.
- [x] Добавить Discord Webhook уведомления для `recovery_required`, failed jobs
      и ошибок scheduler (живая отправка ещё не проверена).
- [ ] Добавить systemd timer или отдельный health-check для alert-файлов.
- [ ] Документировать и автоматизировать reboot/crash recovery drill.
- [ ] Проверить восстановление после SIGKILL, power-loss simulation и повреждённой
      SQLite-копии.
- [ ] Добавить checksum/ownership verification для recovery artifacts.

## 5. Web UI

- [ ] Добавить dashboard состояния manager и серверов.
- [ ] Добавить список серверов и lifecycle controls.
- [ ] Добавить просмотр логов.
- [ ] Добавить редактор server configuration.
- [ ] Добавить редактор scheduler.
- [ ] Добавить jobs и execution history.
- [ ] Добавить отображение `recovery_required`, alert-файла и доступных recovery
      действий.
- [ ] Добавить backup history, retention status и запуск offline recovery guide.

## 6. Production security

- [ ] Заменить Basic Auth на session authentication.
- [ ] Добавить CSRF tokens для browser mutations.
- [ ] Подготовить HTTPS/reverse-proxy deployment.
- [ ] Добавить IP allowlist.
- [ ] Добавить rate limiting.
- [ ] Добавить audit log для lifecycle, configuration, backup и recovery actions.
- [ ] Проверить permissions, ownership и secret handling на чистой Linux-установке.

## 7. Game/server integrations

- [ ] Реализовать player count через query protocol.
- [ ] Реализовать RCON (код готов, см. 1a).
- [ ] Добавить предупреждения игрокам перед scheduled stop/restart (код готов, см. 1a).
- [ ] Проверить и стабилизировать UE4 через Wine/Proton.
- [ ] Добавить отдельный Ubuntu smoke-test для UE4.
- [ ] Добавить автоматический импорт существующего Conan-сервера.
- [ ] Добавить Steam Workshop/mod management.

## 8. Тестирование и эксплуатация

- [x] Добавить отдельные scripts для launcher, API/adoption, polkit и recovery CLI.
- [x] Объединить Linux integration suite (`scripts/node2.sh`) и выполнить
      bind-helper/private namespace tests. На node2 `unshare` теперь разрешён;
      bind mount проверен на живом Conan UE5, включая восстановление после SIGKILL.
- [x] Добавить disposable-тесты retention: пустой каталог, количество копий
      относительно лимита, повторная очистка, сохранность DB/WAL/SHM и symlink.
- [ ] Добавить end-to-end disposable tests для scheduler на Linux.
- [x] Проверить upgrade daily → cron: сохранение расписания, расширение CHECK,
      повторное открытие базы, откат при ошибке и cron без interval_seconds.
- [ ] Добавить systemd deployment test на чистом Ubuntu.
- [ ] Проверить rollback после неудачного deploy.
- [ ] Подготовить release checklist и operational runbook.
- [ ] Создать следующий version tag после Linux acceptance tests.

## Definition of Done для production-кандидата

- Linux integration tests проходят на чистом Ubuntu.
- Recovery drill проходит после штатного shutdown, SIGKILL и simulated reboot.
- Backup target и retention проверены на фактических Conan-файлах.
- Scheduler имеет execution history, retry policy и уведомления об ошибках.
- Web UI защищён session auth, CSRF и HTTPS reverse proxy.
- Есть operational runbook, rollback procedure и release tag.
