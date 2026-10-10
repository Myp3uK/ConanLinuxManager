# Установка на Ubuntu

Три способа: одной командой, из исходников, из готового пакета.

## Одной командой

```bash
curl -fsSL https://raw.githubusercontent.com/Myp3uK/ConanLinuxManager/main/scripts/get.sh | sudo bash -s -- --domain manager.example.com
```

Скрипт спрашивает у GitHub последний релиз, скачивает архив, **сверяет его с
опубликованной рядом контрольной суммой** и отказывается ставить, если она не
сошлась или её нет. Дальше он передаёт работу `install.sh` — всё, что после
`--`, уходит туда без изменений, так что `--no-deps`, `--allow-ips` и
остальные ключи работают как обычно. Go при таком способе не нужен.

Это скрипт из интернета, выполняемый под root. Прочитайте его перед запуском:
`curl -fsSL .../get.sh | less`.

Ключи самого `get.sh`:

- `--version TAG` — поставить конкретный релиз, а не последний;
- `--from-source` — склонировать и собрать вместо скачивания (нужен Go).

## Обновление

«Панель» → «Версия» сравнивает установленную версию с опубликованной и, если
вышла новее, предлагает кнопку. Нажатие просит systemd запустить
`conan-manager-update.service`; тот выполняет `/opt/conan-manager/self-update.sh`
от root. Скрипт сам решает, что ставить: только текущий официальный релиз из
этого репозитория, только по HTTPS и только если контрольная сумма сошлась.
Он меняет бинарники и юниты и не трогает файл окружения, пользователя службы,
данные и веб-сервер.

Polkit-правило `50-conan-manager-update.rules` разрешает непривилегированному
менеджеру запустить ровно этот юнит и ничего больше, поэтому проникший в
панель может вызвать обновление до официального релиза, но не подсунуть свой
код.

То же самое из терминала: `sudo /opt/conan-manager/self-update.sh`.

Если обновлятора на хосте нет (менеджер собран из исходников или поставлен до
того, как обновление появилось), панель всё равно покажет, что вышла новая
версия, и предложит обновиться вручную.

## Установка из исходников

Прямо на сервере (Ubuntu 22.04/24.04, Debian 12), по шагам.

**1. Git и curl**

```bash
sudo apt-get update && sudo apt-get install -y git curl
```

**2. Go 1.24 или новее.** В apt Ubuntu 24.04 он старее, поэтому - с go.dev:

```bash
GO=$(curl -s 'https://go.dev/VERSION?m=text' | head -1)
curl -LO "https://go.dev/dl/${GO}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf "${GO}.linux-amd64.tar.gz"
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile && export PATH=$PATH:/usr/local/go/bin
go version
```

**3. Ключ для GitHub.** Репозиторий приватный: по HTTPS git спросит логин и
пароль, а пароли GitHub не принимает. Клонируют по SSH-ключу. Ключ создаёт
тот же пользователь, который потом запускает `git clone` (без `sudo`: иначе
git возьмёт ключи root, и GitHub откажет):

```bash
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -N ""   # ничего не спрашивает
cat ~/.ssh/id_ed25519.pub                          # строка ssh-ed25519 AAAA...
```

`~/.ssh/id_ed25519` - закрытый ключ, остаётся на сервере;
`~/.ssh/id_ed25519.pub` - открытый. Строку открытого ключа целиком вставьте в
GitHub → репозиторий → Settings → Deploy keys → Add deploy key («Allow write
access» не отмечать).

**4. Клон по SSH.** При первом подключении git спросит, доверять ли
github.com: ответьте `yes`.

```bash
git clone -b server-management git@github.com:Myp3uK/ConanLinuxManager.git
cd ConanLinuxManager
```

**5. Сборка и установка**

```bash
scripts/build.sh                                  # бинарники в bin/
sudo ./install.sh --domain manager.example.com    # или без --domain
```

Go и исходники после установки не нужны: менеджер работает из
`/opt/conan-manager`.

## Установка из пакета

Пакет собирается на любой машине с Go:

```bash
scripts/package.sh        # dist/conan-manager-<версия>-linux-amd64.tar.gz
```

На сервере (Ubuntu 22.04/24.04, Debian 12 - нужны systemd и apt):

```bash
tar xzf conan-manager-*-linux-amd64.tar.gz && cd conan-manager-*-linux-amd64
sudo ./install.sh --domain manager.example.com   # панель по HTTPS через Caddy
sudo ./install.sh                                # или только 127.0.0.1:8080
```

`install.sh`:

1. ставит зависимости через apt: 32-битные библиотеки для SteamCMD, polkit,
   curl (`--no-deps` - не ставить);
2. создаёт пользователя `conan-manager`, `/opt/conan-manager` (бинарники),
   `/var/lib/conan-manager` (0750: серверы, бэкапы, база), `/etc/conan-manager`;
3. при первой установке создаёт файл окружения root:root 0600 со случайным
   паролем администратора и ключом шифрования и один раз показывает пароль;
4. ставит службу менеджера, шаблоны `conan-server@` и `conan-launch@`,
   правило polkit, запускает менеджер и ждёт `/healthz`;
5. с `--domain`: ставит Caddy из официального репозитория, добавляет сайт в
   `/etc/caddy/Caddyfile` между метками `# conan-manager begin/end` (остальные
   сайты не трогает, стандартную заглушку на :80 убирает, старую версию
   сохраняет рядом), прописывает `TRUSTED_PROXIES` и `ALLOWED_ORIGIN`,
   открывает 80/443 в ufw, если он включён, сверяет DNS с внешним адресом и
   ждёт сертификат;
6. `--allow-ips "203.0.113.10,192.168.1.0/24"` - сразу ограничивает адреса;
   `--email` - адрес для писем Let's Encrypt.

Повторный запуск - обновление: файл окружения, база и серверы остаются.
`sudo ./install.sh --uninstall` удаляет менеджер, оставляя данные;
с `--purge` - удаляет и серверы, бэкапы, базу, файл окружения и пользователя.
Caddy при удалении остаётся, из Caddyfile убирается только сайт панели.

Для домена за домашним роутером: A-запись на внешний адрес, проброс TCP 80 и
443 на машину. Изнутри сети домен может не открываться, если роутер не умеет
NAT loopback - проверяйте с мобильного интернета.

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
