# conan-bind: экспериментальный offline helper

Linux/root-only утилита для проверки файловых bind mounts. Не подключена к
controller и не является setuid/sudo-интерфейсом для непривилегированного менеджера.
Требует Linux с `openat2` (5.6+) и отдельный namespace с private propagation.

```sh
go build -o conan-bind ./cmd/conan-bind
unshare --mount --propagation private /bin/bash
./conan-bind --action mount --saved /absolute/Saved --ram /absolute/ram/database
# Здесь можно запустить SQLite/Conan, затем обязательно дождаться его выхода.
./conan-bind --action unmount --saved /absolute/Saved --ram /absolute/ram/database
```

Оба каталога и все три regular-файла `game_0.db`, `game_0.db-wal`,
`game_0.db-shm` должны быть заранее подготовлены. RAM-файлы должны находиться
на tmpfs; Saved — вне tmpfs (проверка не доказывает физическую долговечность
произвольной файловой системы). Helper не создаёт tmpfs и не копирует базу.
После unmount снова видны исходные SSD-файлы: перенос последних данных с RAM
на SSD — обязанность вызывающей стороны. Использовать только на offline-копиях.

Проверки выполняются для всего набора до mount/unmount: абсолютные нормализованные
пути, отсутствие симлинков во всех компонентах, regular single-link файлы,
tmpfs, отсутствие существующих mounts при mount и совпадение device/inode
с RAM-источником при unmount. Mount использует закреплённые O_PATH-дескрипторы.
При ошибке mount выполняется rollback уже установленных mounts, ошибки rollback
возвращаются вызывающему. Unmount обычный, без force/lazy; при ошибке возможен
частично снятый набор, требующий ручного завершения в том же namespace.

Вызывающая сторона обязана исключить параллельные процессы, переименования и
изменения mounts на всё время операции. Проверки не заменяют эту блокировку:
в частности, unmount использует pathname после закрытия проверочных дескрипторов.
Утилита не является границей привилегий для недоверенных путей/пользователей.
Не назначать setuid, capabilities или sudo-правила. Для production-интеграции
нужен доверенный launcher с фиксированными путями, общим namespace с Conan,
блокировкой сессии, сбросом привилегий и согласованным persist/recovery.

Проверка на одноразовых данных (root, SSD `/var/tmp`):

```sh
unshare --mount --propagation private \
  python3 scripts/test-bind-helper.py /absolute/conan-bind
```

Тест проверяет отказ для обычного RAM-каталога, симлинков, повторного mount,
неверного источника unmount; три SQLite WAL/reopen-цикла, integrity_check,
создание backup на SSD и сохранение RAM-данных после unmount.
