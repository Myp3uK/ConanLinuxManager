# conan-launch - автономный root-operated launcher

Экспериментальный Linux launcher для **отдельных тестовых установок** с RAM-базой.
HTTP controller поддерживает opt-in backend (см. `deploy/LAUNCHER.md`), по умолчанию
используется прежний путь symlink+sweeper. Launcher не предназначен для setuid, capabilities или выдачи
непривилегированному пользователю произвольного sudo-доступа.

## Контракт

Root запускает бинарный файл с root-owned JSON из каталога, все родители которого
принадлежат root и не доступны для записи группе/остальным. Симлинки запрещены.
Пример `/etc/conan-manager/launch/test.json`:

```json
{
  "server_id": "test",
  "install_dir": "/var/lib/conan-manager/servers/test/game",
  "ram_dir": "/run/conan-manager/test",
  "executable": "/var/lib/conan-manager/servers/test/game/ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping",
  "args": ["ConanSandbox", "-log"],
  "uid": 1001,
  "gid": 1001
}
```

UID/GID должны соответствовать выделенному непривилегированному пользователю.
Каталоги установки, `ConanSandbox/Saved`, RAM и RAM/`database` уже существуют;
оба RAM-каталога находятся на заранее смонтированном tmpfs. `database` пуст.
Saved и установка не на tmpfs. Игра должна иметь необходимые права на установку
и Saved. Симлинки в путях и имени executable запрещены. Конфигурация поддерживает
только перечисленные поля; environment задан явно (PATH, LANG, HOME).

```sh
/opt/conan-manager/conan-launch --config /etc/conan-manager/launch/test.json
```

Нельзя одновременно управлять той же установкой через manager, recovery или
внешние команды. Launcher удерживает неблокирующие flock на inode каталогов
установки и RAM до конца persist. Это кооперативные блокировки
между launcher, controller и recovery; при systemd handoff controller передаёт
владение, и launcher заново получает lease перед изменениями.
Предполагаются доверенные offline-пути и отсутствие внешних изменений. Проверки
путей и root-owned конфигурация не делают весь процесс границей безопасности
для враждебного игрового UID: persist пока использует pathname-операции.

## Последовательность

1. Родитель создаёт worker в отдельных mount и PID namespaces; worker - PID 1.
2. Worker делает propagation private, берёт locks, проверяет отсутствие старой
   сессии и создаёт совместимые с `conan-recover` baseline/manifest на SSD.
3. Копирует DB/WAL/SHM в RAM и устанавливает три файловых bind mounts.
4. Запускает игру с заданными UID/GID, без supplementary groups и с
   `no_new_privs`. Игра видит procfs своего PID namespace.
5. SIGTERM/SIGINT родителя передаётся worker, тот отправляет SIGTERM группе игры;
   через 180 секунд отправляет SIGKILL всем процессам своего PID namespace.
6. После выхода игры убивает и reap-ит оставшихся потомков, включая setsid,
   снимает mounts, проверяет отдельную копию SQLite и checkpoint-ит WAL.
7. Сохраняет проверенную базу в Saved, восстанавливает владельца от Saved,
   архивирует marker и синхронизирует каталог архива на SSD.
8. Под той же блокировкой проверяет содержимое RAM/database и удаляет только
   три известных regular DB-файла. Архив на SSD сохраняется для диагностики.

Exit 0 - успешный выход игры и persist. Exit 2 - ошибка launcher, persist или
неуспешный выход игры (даже если persist успешен; это явно отмечается в stderr).
При SIGKILL родителя worker получает SIGKILL, ядро завершает все процессы его
PID namespace. Persist тогда не выполняется: marker требует offline recovery.
При повреждённой базе marker/RAM сохраняются, Saved до успешной валидации
не заменяется. Ошибка записи после валидации может оставить частично обновлённый
Saved: marker и stage нужны для повторного восстановления.

После полного успешного persist/cleanup следующий запуск выполняется автоматически.
Если cleanup прерван или найдены неизвестные файлы/симлинки, непустой RAM/database
блокирует запуск до проверки оператором. Старый архив или отсутствие marker не
разрешают автоматическое удаление оставшихся данных. Offline recovery RAM не удаляет.
Перезагрузка/потеря питания уничтожает tmpfs, baseline отражает начало сессии.

## Проверка

```sh
go build -o conan-launch ./cmd/conan-launch
sudo python3 scripts/test-launcher.py /absolute/conan-launch
```

Тест рассчитан на Ubuntu 24.04 (`/usr/bin/python3.12`, пользователь nobody).
Создаёт одноразовые данные в `/var/tmp`, конфигурации в `/root`, tmpfs 64 MiB.
Проверяет нормальный выход, SIGKILL игры, graceful сигнал launcher, SIGKILL
launcher, повреждённую базу, lock contention, WAL, SSD backup, UID/GID,
capabilities/no_new_privs, потомка с setsid, архив и владельца сохранённой базы,
очистку RAM и три последовательных запуска с накоплением записей.

Systemd backend, единые locks и согласованный budget остановки описаны в
`deploy/LAUNCHER.md`. После успешного persist launcher очищает известные RAM DB
файлы под общей блокировкой; ошибки persist сохраняют RAM для recovery.
Реальная UE5-копия была проверена на node2: startup, SIGTERM, persist и очистка
RAM прошли. Штатный SIGTERM/SIGINT после успешного persist классифицируется как
успешная остановка; ошибка persist или другой exit остаётся ошибкой. Unit менеджера с `NoNewPrivileges=true`
напрямую создавать такие namespaces не может; используются отдельные root units.
