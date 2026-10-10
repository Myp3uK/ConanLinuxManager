# Что проверено в Conan Exiles UE5

Факты о самой игре, добытые на живом сервере: что она пишет, когда и куда.

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

Размер установки - примерно 4 GiB.

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

## Каденция бэкапов Conan - что настраивается, а что нет

Измерено на `node2`, Linux UE5 Shipping, сервер останавливался перед каждой
правкой конфигурации.

**Конфигурационные файлы нельзя править при работающем сервере.** Игра читает
их один раз при старте и переписывает при остановке, поэтому значение,
записанное на ходу, молча теряется. Менеджер поэтому пишет их в `start()`,
под тем же lifecycle-локом, который не даёт стартовать дважды.

| Ключ | Где работает | Результат |
| --- | --- | --- |
| `PeriodicBackupInterval` | `Game.ini`, секция `[/Script/ConanSandbox.ConanGameMode]` | Работает. Значение из `Game.ini` перекрывает `ServerSettings.ini`. Нижнего порога нет: 120 и 180 секунд соблюдались точно. |
| `MaxPeriodicBackupsToKeep` | не выяснено | Ротация не «последние N»: в `libDreamworld.so` есть `HistogramBackupScheduler`, `useTimedBackups` и `allBackupTimes`, то есть копии распределяются по времени. Наблюдения: интервал 1–2 мин → 2 копии; 10 мин → 15 за 2.5 часа и число растёт; на боевом сервере - 40. Точная формула не установлена. |
| `BackupDirectory` | не существует | Такого свойства в бинарнике нет. Строка `Build Config: BackupDirectory: %ls` относится к инсталлятору Epic (BuildPatchServices), рядом с `InstallDirectory` и `StagingDirectory`. Бэкапы всегда пишутся рядом с базой. |

Разводящий эксперимент: при `PeriodicBackupInterval=120` в `Game.ini` и `=300`
в `ServerSettings.ini` каденция была 120 с. При `MaxPeriodicBackupsToKeep=10`
в `Game.ini` и `=6` в `ServerSettings.ini` за 17 минут и ~8 созданных бэкапов
на диске оставалось 2. Это доказывает только, что при коротком интервале
копий две; на длинных интервалах их больше (см. строку выше).

### Почему это главный рычаг износа SSD

Каждый цикл копирует базу целиком. При базе 435 МБ и стоковой каденции около
480 секунд это ~3.2 ГБ записи в час на сервер. `PeriodicBackupInterval=2400`
снижает это до ~650 МБ/час, то есть примерно в пять раз, и не зависит от того,
включён ли RAM-диск.

Настройка живёт в `periodic_backup_interval_seconds` конфигурации сервера.
Ноль означает «не трогать» - существующие серверы после обновления сохраняют
поведение игры, а не получают каденцию, которую никто не выбирал.

### Глубина истории - задача менеджера

В symlink-режиме игра пишет бэкап рядом с реальным путём базы, то есть в
RAM, а дренаж забирает `game_0_backup_1.db` раньше, чем игра успевает его
ротировать. Своя ротация игры в этом режиме не работает вовсе, и без
дополнительных мер на SSD оставался бы один файл возрастом в один цикл.
В bind-режиме этой проблемы нет: смонтированы только три файла, каталог
`Saved` остаётся на SSD, бэкапы ложатся туда напрямую и ротируются самой игрой. Поэтому каждая копия при дренаже получает в имени
время своей записи (`game_0_backup_1_20260921T043250Z.db`), а `internal/backup`
подрезает серию до `CONAN_MANAGER_BACKUP_RETENTION`. Время модификации
сохраняется от оригинала, чтобы retention сортировал по моменту сохранения
игры, а не по моменту работы дренажа.

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
