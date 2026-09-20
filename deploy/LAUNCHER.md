# Подключение launcher к менеджеру (opt-in)

Backend включается только для ID из root-controlled environment:

```ini
CONAN_MANAGER_LAUNCHER_IDS=test
```

Пустой список сохраняет прежний способ запуска. Для каждого включённого ID:

1. Подготовить отдельную установку и tmpfs по `cmd/conan-launch/README.md`.
2. Создать `/etc/conan-manager/launch/ID.json`: root:root, 0644, родители
   root-owned без group/other write. В конфигурации нет секретов; manager должен
   читать её для сравнения с API-записью. ID: `[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}`.
3. Установить launcher root:root 0700 в `/opt/conan-manager/conan-launch` и
   `deploy/conan-launch@.service` в `/etc/systemd/system/`.
4. На хосте с polkit установить адаптированную копию `conan-launch.rules.example`
   в `/etc/polkit-1/rules.d/50-conan-launch.rules`. В allowlist перечислить только
   конкретные provisioned units. Не разрешать произвольные unit names/verbs.
5. Выполнить `systemctl daemon-reload`, обновить unit менеджера (TimeoutStopSec=870),
   затем перезапустить менеджер. Сначала проверить на отдельной тестовой установке.

HTTP start сверяет ID, install/RAM paths, native runtime, executable и аргументы
с JSON. В systemd передаётся только имя заранее разрешённого unit. Старт означает
принятие запуска сервиса, а не готовность Conan. PID в snapshot — PID launcher.
Monitor опрашивает systemd до окончания сервиса, включая persist. Ошибка опроса
не освобождает reservation. Stop/watchdog/shutdown используют systemctl stop;
symlink Load/Drain/Flush для этих ID отключены. Время остановки unit 810 секунд,
shutdown менеджера 840 секунд, systemd timeout менеджера 870 секунд.

После перезапуска менеджер подключается к активным units включённых ID, имеющим
запись в repository. Не удалять JSON, API-запись или ID из environment, пока
unit активен. При неуспешном persist marker даёт recovery_required.

Общие directory-flock применяются controller (start/install/delete), launcher
и offline recovery. При передаче управления controller отпускает flock;
launcher обязан взять его сам и отказывает при конкуренции. До завершения unit
HTTP reservation остаётся. Проверка Pending повторяется после взятия lease.
Несуществующий путь блокирует ближайшего существующего родителя консервативно:
для параллельного provision/install заранее создавайте отдельные каталоги.
Вложенные существующие установки между разными процессами не поддерживаются:
flock inode не является иерархической блокировкой. Пути должны быть выделенными,
без внешних переименований/замены каталогов. Windows — только in-process leases.

## Текущие ограничения включения

- Launcher очищает только три известных regular DB-файла в RAM/database после
  успешного SQLite persist и durable архивирования маркера в этой же операции,
  под общей блокировкой, после остановки потомков и снятия bind mounts.
  Ошибка persist сохраняет RAM; неизвестные файлы/симлинки блокируют очистку.
  Прерывание между commit и cleanup может оставить RAM: непустой каталог
  по-прежнему блокирует старт и требует проверки оператором. Старые архивы сами
  по себе не разрешают автоочистку. Offline conan-recover сохраняет RAM как раньше.
- stdout/stderr launcher и игры идут в journal; `journalctl -u conan-launch@ID`
  показывает ошибки persist. Файловый watchdog продолжает читать ConanSandbox.log,
  но stdout journal пока не включён в его лимиты. Нужны отдельные log limits.
- Установленный шаблон сам не даёт непривилегированному менеджеру право start/stop:
  права задаёт только отдельная явная polkit allowlist.
- Автоматические проверки
  используют SQLite-процесс с WAL и реальные systemd units на одноразовых данных.
  На node2 отдельная копия UE5 была запущена через launcher, достигла рабочего
  состояния и корректно остановилась по SIGTERM. Persist прошёл, SQLite
  integrity был `ok`, RAM очищена. При штатном `systemctl stop` launcher теперь
  считает SIGTERM/SIGINT Conan успешным завершением только после успешного
  persist. Ошибка persist по-прежнему возвращает exit 2; произвольный аварийный
  exit игры также не маскируется.

Проверка polkit выполняется скриптом `scripts/test-polkit-launcher.py`: временный
unit запускается и останавливается пользователем `conan-manager`; дополнительно
проверяются отказ для чужого unit и отказ для `restart`. Скрипт отказывается
перезаписывать существующие тестовые пути, после чего удаляет все временные
unit/config/rules/data.

Полный API smoke/integration-тест выполняется скриптом
`scripts/test-api-launcher.py`. Он запускает отдельный manager на порту 18080 с
временной SQLite-базой, создаёт disposable server record и выполняет два полных
цикла API start/stop через systemd launcher. Между циклами manager принудительно
перезапускается, пока launcher остаётся активным; новый manager должен усыновить
(`adopt`) работающий unit и показать lifecycle `running`. Тест проверяет переход
lifecycle в `idle`, а также очищает unit, config, manager process и tmpfs в
`finally`.

В финальной части того же disposable-теста создаётся session marker без запуска
игры. API должен показать `recovery_required`, а start и delete должны быть
отклонены с `409` до удаления marker. После удаления marker lifecycle снова
становится `idle`. Также выполняются два одновременных start-запроса: ровно один
принимается, второй получает `409`; затем принятый запуск штатно останавливается.

На node2 backend по умолчанию выключен; template не запускает игровых серверов.

Офлайн CLI recovery можно безопасно проверить на отдельной Linux-копии командой
`python3 scripts/test-recovery-cli.py` после сборки `conan-recover`. Скрипт требует
root и останавливает manager только на время disposable-проверки; production paths
и записи рабочих серверов не используются.
