# План оставшихся работ

Этот файл описывает задачи после milestone `v0.1.0` и commit
`c2d9fe1 Add retention recovery alerts and scheduler`.

## 1. Зафиксировать текущий milestone на Linux

- [ ] Отправить текущие commits в `origin/main`.
- [ ] Проверить миграцию SQLite на существующей установке `node2`.
- [ ] Проверить scheduler на Linux: `start`, `stop`, `restart`, перезапуск manager
      и корректное завершение scheduler при SIGTERM.
- [ ] Выполнить disposable-тест retention на реальных Conan backup-файлах.
- [ ] Выполнить disposable recovery drill с recovery alert-файлом.
- [ ] Проверить bind-helper на Linux-хосте с private mount namespace.

## 2. Надёжность scheduler

- [ ] Добавить persistent execution history: запуск, завершение, duration, result
      и error.
- [ ] Добавить retry/backoff для временных ошибок lifecycle-операций.
- [ ] Защититься от повторного выполнения одного просроченного задания после
      длительного простоя manager.
- [ ] Добавить cron/calendar schedules поверх текущих interval schedules.
- [ ] Добавить проверку active players перед плановым stop/restart.
- [ ] Добавить pre/post scripts с ограниченными окружением, timeout и логированием.

## 3. Backup storage

- [ ] Завершить backup history в SQLite: checksum, размер, source, destination,
      причина, статус и timestamp.
- [ ] Реализовать рабочий local backup target.
- [ ] Реализовать SFTP target.
- [ ] Реализовать rsync over SSH target.
- [ ] Реализовать S3-compatible target.
- [ ] Добавить SSH key authentication.
- [ ] Подключить шифрование секретов backup targets через master key.
- [ ] Добавить проверку свободного места и атомарную отправку backup-файлов.
- [ ] Добавить восстановление конкретной backup-копии через offline workflow.
- [ ] Расширить retention: age/size limits, dry-run и отчёт об удалённых файлах.

## 4. Recovery и alerting

- [ ] Подключить recovery alert к внешнему уведомлению.
- [ ] Добавить Discord Webhook уведомления для `recovery_required`, failed jobs
      и ошибок scheduler.
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
- [ ] Реализовать RCON.
- [ ] Добавить предупреждения игрокам перед scheduled stop/restart.
- [ ] Проверить и стабилизировать UE4 через Wine/Proton.
- [ ] Добавить отдельный Ubuntu smoke-test для UE4.
- [ ] Добавить автоматический импорт существующего Conan-сервера.
- [ ] Добавить Steam Workshop/mod management.

## 8. Тестирование и эксплуатация

- [ ] Добавить Linux integration suite для launcher, bind-helper и tmpfs.
- [ ] Добавить disposable tests для scheduler и retention.
- [ ] Добавить upgrade test с предыдущей схемой SQLite.
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
