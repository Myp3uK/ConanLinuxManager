'use strict';

// Conan Manager web interface. A single page over the JSON API; every value
// that comes from the game (log lines, player names, RCON answers) is put into
// the page as text, never as markup.

const app = document.getElementById('app');

// ---------- small helpers ----------

let leaving = [];
function onLeave(fn) { leaving.push(fn); }
function every(ms, fn) { const t = setInterval(fn, ms); onLeave(() => clearInterval(t)); }

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') el.className = value;
    else if (key === 'text') el.textContent = value;
    // A style attribute would be refused by the page's content policy;
    // styles set through the CSSOM are not.
    else if (key === 'style') el.style.cssText = value;
    else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
    else if (['value', 'checked', 'disabled', 'selected', 'open'].includes(key)) el[key] = value;
    else el.setAttribute(key, value === true ? '' : value);
  }
  add(el, children);
  return el;
}
function add(el, children) {
  for (const child of children.flat(Infinity)) {
    if (child === null || child === undefined || child === false) continue;
    el.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return el;
}
function replace(el, ...children) { el.replaceChildren(); return add(el, children); }

async function api(method, path, body) {
  const options = { method, headers: {} };
  if (body !== undefined) {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  // location.origin carries no user:password, so the request is valid even
  // when the page was opened through a URL that had them.
  const response = await fetch(location.origin + '/api/v1' + path, options);
  if (response.status === 204) return null;
  const text = await response.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!response.ok) {
    const error = new Error((data && data.error) || response.statusText || ('HTTP ' + response.status));
    error.status = response.status;
    throw error;
  }
  return data;
}
const enc = encodeURIComponent;

function toast(message, kind = 'ok', ms = 5000) {
  const el = h('div', { class: 'toast ' + kind, text: message });
  document.getElementById('toasts').append(el);
  setTimeout(() => el.remove(), kind === 'bad' ? Math.max(ms, 9000) : ms);
}

// act runs an action from a button: the button is disabled meanwhile and a
// failure is shown instead of thrown.
async function act(button, fn) {
  if (button) button.disabled = true;
  try { return await fn(); } catch (e) { toast(e.message, 'bad'); return undefined; }
  finally { if (button && button.isConnected) button.disabled = false; }
}

// ask shows a confirmation and resolves to whether it was accepted. extra
// holds inputs shown in the dialog; the caller reads them afterwards.
function ask(text, { ok = 'Да', danger = true, extra = [] } = {}) {
  const dialog = document.getElementById('confirm');
  document.getElementById('confirm-text').textContent = text;
  replace(document.getElementById('confirm-extra'), extra);
  const okButton = document.getElementById('confirm-ok');
  const cancelButton = document.getElementById('confirm-cancel');
  okButton.textContent = ok;
  okButton.className = danger ? 'danger' : 'primary';
  return new Promise(resolve => {
    const done = value => {
      okButton.onclick = cancelButton.onclick = dialog.oncancel = null;
      dialog.close();
      resolve(value);
    };
    okButton.onclick = () => done(true);
    cancelButton.onclick = () => done(false);
    dialog.oncancel = () => done(false);
    dialog.showModal();
  });
}

const JOB_NAMES = { install: 'Установка', restart: 'Рестарт', update: 'Обновление', backup: 'Бэкап', restore: 'Восстановление' };

// runJob follows a background job to its end and reports how it went.
async function runJob(job) {
  const name = JOB_NAMES[job.kind] || job.kind;
  toast(name + ': начато', 'info', 3000);
  while (job.status === 'running' || job.status === 'queued') {
    await new Promise(r => setTimeout(r, 2000));
    try { job = await api('GET', '/jobs/' + enc(job.id)); } catch (e) { toast(name + ': ' + e.message, 'bad'); return job; }
  }
  if (job.status === 'failed') toast(name + ': ошибка — ' + (job.error || 'без подробностей'), 'bad');
  else toast(name + ': готово' + (job.error ? ' (' + job.error + ')' : ''));
  return job;
}

function isSet(t) { return t && !t.startsWith('0001-'); }
function fmtTime(t) { return isSet(t) ? new Date(t).toLocaleString('ru-RU') : '—'; }
function fmtAgo(t) {
  if (!isSet(t)) return '—';
  let s = Math.round((Date.now() - new Date(t).getTime()) / 1000);
  const future = s < 0; s = Math.abs(s);
  const text = s < 60 ? s + ' с' : s < 3600 ? Math.floor(s / 60) + ' мин' : s < 86400 ? Math.floor(s / 3600) + ' ч ' + Math.floor(s % 3600 / 60) + ' мин' : Math.floor(s / 86400) + ' д ' + Math.floor(s % 86400 / 3600) + ' ч';
  return future ? 'через ' + text : text + ' назад';
}
function fmtBytes(n) {
  if (!n) return '0 Б';
  const units = ['Б', 'КБ', 'МБ', 'ГБ'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + ' ' + units[i];
}

const STATES = {
  idle: ['Остановлен', ''], starting: ['Запускается', 'warn'], running: ['Работает', 'ok'],
  stopping: ['Останавливается', 'warn'], flushing: ['Сохраняет', 'warn'], installing: ['Устанавливается', 'warn'],
  configuring: ['Настраивается', 'warn'], verifying: ['Проверка базы', 'warn'], restoring: ['Восстановление', 'warn'],
  deleting: ['Удаляется', 'warn'], recovery_required: ['Нужно восстановление', 'bad'],
};
function stateBadge(state) {
  const [label, kind] = STATES[state] || [state || '—', ''];
  return h('span', { class: 'badge ' + kind, text: label });
}
const AVAILABILITY = {
  responsive: ['Отвечает', 'ok'], starting: ['Загружается', 'warn'], suspect: ['Задержки', 'warn'],
  unavailable: ['Не отвечает', 'bad'], inactive: ['—', ''],
};
function availabilityBadge(a) {
  if (!a || !a.state) return h('span', { class: 'muted', text: '—' });
  const [label, kind] = AVAILABILITY[a.state] || [a.state, ''];
  return h('span', { class: 'badge ' + kind, text: label });
}
const RUN_STATUS = { succeeded: ['успешно', 'ok'], failed: ['ошибка', 'bad'], running: ['идёт', 'warn'], interrupted: ['прерван', 'bad'] };
const SEVERITY = { info: ['инфо', ''], warning: ['внимание', 'warn'], error: ['ошибка', 'bad'] };
function severityBadge(s) {
  const [label, kind] = SEVERITY[s] || [s, ''];
  return h('span', { class: 'badge plain ' + kind, text: label });
}

function facts(pairs) {
  return h('dl', { class: 'facts' }, pairs.filter(Boolean).map(([k, v]) => [h('dt', { text: k }), h('dd', {}, v)]));
}
function table(headers, rows, empty = 'Пусто') {
  if (!rows.length) return h('p', { class: 'muted', text: empty });
  return h('div', { class: 'scroll' }, h('table', {},
    h('thead', {}, h('tr', {}, headers.map(x => h('th', { text: x })))),
    h('tbody', {}, rows.map(r => h('tr', {}, r.map(c => h('td', {}, c)))))));
}
function field(label, input, hint) {
  return [h('label', { text: label }), input, hint ? h('div', { class: 'hint', text: hint }) : null];
}

// ---------- server actions shared by the list and the server page ----------

function actionButtons(detail, refresh) {
  const id = detail.server.id;
  const state = detail.lifecycle.state;
  const running = state === 'running';
  const steps = (detail.server.warnings && detail.server.warnings.steps) || [];
  const warnings = steps.length;
  const start = h('button', { class: 'primary', text: 'Запустить', disabled: state !== 'idle',
    onclick: e => act(e.target, async () => { await api('POST', `/servers/${enc(id)}/start`); toast('Запуск отправлен'); refresh(); }) });
  const stop = h('button', { text: 'Остановить', disabled: !running,
    onclick: e => act(e.target, async () => {
      if (!await ask(`Остановить «${detail.server.name}»? Игра сохранит мир; если не выйдет за 5 минут, процесс будет убит и база проверена.`, { ok: 'Остановить' })) return;
      await api('POST', `/servers/${enc(id)}/stop`); toast('Остановка начата'); refresh();
    }) });
  const restart = h('button', { text: 'Рестарт', disabled: !running,
    onclick: e => act(e.target, async () => {
      const warn = h('input', { type: 'checkbox', checked: !!warnings, disabled: !warnings });
      const label = h('label', { class: 'row' }, warn, warnings ? 'Предупредить игроков (за ' + steps.map(x => x.minutes).sort((a, b) => b - a).join(', ') + ' мин., если кто-то онлайн)' : 'Предупреждения не настроены');
      if (!await ask(`Перезапустить «${detail.server.name}»?`, { ok: 'Перезапустить', extra: [label] })) return;
      const job = await api('POST', `/servers/${enc(id)}/restart`, { warn: warn.checked });
      refresh(); await runJob(job); refresh();
    }) });
  const kill = h('button', { class: 'danger', text: 'Убить', disabled: !(running || state === 'stopping'),
    onclick: e => act(e.target, async () => {
      if (!await ask(`Убить процесс «${detail.server.name}» без сохранения? Будет потерян прогресс с последнего сохранения; после выхода база проверяется.`, { ok: 'Убить' })) return;
      await api('POST', `/servers/${enc(id)}/kill`); toast('Процесс убит'); refresh();
    }) });
  return h('div', { class: 'row' }, start, stop, restart, kill);
}

// ---------- views ----------

const players = {}; // server id -> { count, error, at }

async function refreshPlayers(details) {
  await Promise.all(details.filter(d => d.lifecycle.state === 'running').map(async d => {
    try {
      const r = await api('GET', `/servers/${enc(d.server.id)}/players`);
      players[d.server.id] = { count: r.count, at: Date.now() };
    } catch (e) {
      players[d.server.id] = { error: e.message, at: Date.now() };
    }
  }));
}

function playersText(id, state) {
  if (state !== 'running') return h('span', { class: 'muted', text: '—' });
  const p = players[id];
  if (!p) return h('span', { class: 'muted', text: '…' });
  if (p.error) return h('span', { class: 'muted', title: p.error, text: 'нет RCON' });
  return String(p.count);
}

async function viewServers() {
  const grid = h('div', { class: 'grid' });
  const eventsBox = h('div');
  replace(app,
    h('div', { class: 'head' }, h('h1', { text: 'Серверы' }),
      h('button', { class: 'primary', text: '+ Добавить сервер', onclick: () => { location.hash = '#/new'; } })),
    grid,
    h('h3', { text: 'Последние события' }),
    h('div', { class: 'panel' }, eventsBox));

  let details = [];
  const draw = () => {
    if (!details.length) {
      replace(grid, h('p', { class: 'muted', text: 'Серверов пока нет.' }));
      return;
    }
    replace(grid, details.map(d => {
      const s = d.server, state = d.lifecycle.state;
      return h('div', { class: 'card' },
        h('div', { class: 'title' }, h('a', { href: `#/server/${enc(s.id)}`, text: s.name }), h('span', { class: 'muted small mono', text: s.id }),
          h('span', { class: 'spacer' }), stateBadge(state)),
        s.recovery_reason ? h('div', { class: 'notice bad small', text: 'Запуск заблокирован: ' + s.recovery_reason }) : null,
        d.lifecycle.error ? h('div', { class: 'notice bad small', text: d.lifecycle.error }) : null,
        facts([
          ['Доступность', availabilityBadge(d.health && d.health.availability)],
          ['Игроки', playersText(s.id, state)],
          ['Порты', `${s.game_port} / ${s.query_port}`],
          state === 'running' && d.process && ['Работает', fmtAgo(d.process.started_at).replace(' назад', '')],
          d.health && d.health.recent_crashes > 0 && ['Падений', String(d.health.recent_crashes)],
          d.health && d.health.restart_pending && ['Перезапуск', fmtAgo(d.health.restart_at)],
        ]),
        actionButtons(d, refresh));
    }));
  };
  const refresh = async () => {
    try {
      const list = await api('GET', '/servers');
      details = await Promise.all(list.map(s => api('GET', `/servers/${enc(s.id)}`)));
      draw();
    } catch (e) { replace(grid, h('div', { class: 'notice bad', text: 'Не удалось получить список: ' + e.message })); }
  };
  const refreshEvents = async () => {
    try { replace(eventsBox, eventsTable(await api('GET', '/events?limit=15'), true)); } catch (e) { replace(eventsBox, h('p', { class: 'muted', text: e.message })); }
  };
  await refresh();
  refreshEvents();
  refreshPlayers(details).then(draw);
  every(5000, refresh);
  every(15000, refreshEvents);
  every(30000, () => refreshPlayers(details).then(draw));
}

function eventsTable(items, withServer) {
  return table(
    ['Время', 'Уровень', withServer ? 'Сервер' : null, 'Событие', 'Подробности'].filter(Boolean),
    items.map(e => [
      h('span', { class: 'small', text: fmtTime(e.time) }),
      severityBadge(e.severity),
      withServer ? (e.server_id ? h('a', { href: `#/server/${enc(e.server_id)}`, text: e.server_name || e.server_id }) : '—') : null,
      h('span', {}, e.message, h('div', { class: 'muted small mono', text: e.type })),
      h('span', { class: 'small mono' }, Object.entries(e.details || {}).map(([k, v]) => h('div', { text: `${k}: ${v}` }))),
    ].filter(x => x !== null)), 'Событий нет');
}

async function viewEvents() {
  const box = h('div', { class: 'panel' });
  const more = h('button', { text: 'Показать ещё' });
  let items = [];
  replace(app, h('div', { class: 'head' }, h('h1', { text: 'События' })), box, h('div', { class: 'row' }, more));
  const load = async before => {
    const page = await api('GET', '/events?limit=100' + (before ? '&before=' + before : ''));
    items = items.concat(page);
    replace(box, eventsTable(items, true));
    more.disabled = page.length < 100;
  };
  more.onclick = e => act(e.target, () => load(items.length ? items[items.length - 1].id : 0));
  await act(null, () => load(0));
}

// ---------- new server ----------

async function viewNewServer() {
  let existing = [];
  try { existing = await api('GET', '/servers'); } catch { existing = []; }
  // Suggest ports clear of every server: each uses game, game+1 and query.
  const used = new Set(existing.flatMap(s => [s.game_port, s.game_port + 1, s.query_port]));
  let game = 7777; while (used.has(game) || used.has(game + 1)) game += 10;
  let query = 27015; while (used.has(query)) query += 10;

  const id = h('input', { placeholder: 'server-1', required: true, pattern: '[A-Za-z0-9][A-Za-z0-9_-]{0,63}' });
  const name = h('input', { value: 'Conan Exiles' });
  const install = h('input', { placeholder: '/var/lib/conan-manager/servers/server-1/game' });
  const steam = h('input', { value: '/var/lib/conan-manager/steamcmd' });
  const branch = h('input', { value: 'public' });
  const gamePort = h('input', { type: 'number', min: 1, max: 65535, value: game });
  const queryPort = h('input', { type: 'number', min: 1, max: 65535, value: query });
  const args = h('input', { value: '-log' });
  const backup = h('input', { type: 'number', min: 0, value: 2400 });
  const restart = h('input', { type: 'checkbox', checked: true });
  const installNow = h('input', { type: 'checkbox', checked: true });
  let installTouched = false;
  install.oninput = () => { installTouched = true; };
  id.oninput = () => { if (!installTouched) install.value = id.value ? `/var/lib/conan-manager/servers/${id.value}/game` : ''; };
  const create = h('button', { class: 'primary', text: 'Создать' });
  create.onclick = e => act(e.target, async () => {
    const config = {
      id: id.value.trim(), name: name.value.trim() || id.value.trim(), engine: 'ue5', runtime: 'native',
      install_dir: install.value.trim(), steamcmd_dir: steam.value.trim(), branch: branch.value.trim() || 'public',
      launch_args: args.value.trim(), game_port: +gamePort.value, query_port: +queryPort.value,
      periodic_backup_interval_seconds: +backup.value, restart_on_crash: restart.checked,
      ramdisk_path: `/run/conan-manager/${id.value.trim()}`, ramdisk_size_mb: 2048,
    };
    const created = await api('POST', '/servers', config);
    toast('Сервер создан');
    location.hash = `#/server/${enc(created.id)}`;
    if (installNow.checked) runJob(await api('POST', `/servers/${enc(created.id)}/install`));
  });
  replace(app,
    h('div', { class: 'head' }, h('a', { href: '#/', text: '← Серверы' }), h('h1', { text: 'Новый сервер' })),
    h('div', { class: 'panel' }, h('div', { class: 'form' },
      field('ID', id, 'Латиница, цифры, - и _. Входит в имя systemd-юнита и пути; изменить потом нельзя.'),
      field('Название', name),
      field('Каталог установки', install, 'Может быть на отдельном диске.'),
      field('Каталог SteamCMD', steam, 'Общий для всех серверов.'),
      field('Ветка', branch),
      field('Игровой порт', gamePort, 'Игра занимает этот порт и следующий за ним.'),
      field('Query-порт', queryPort),
      field('Аргументы запуска', args),
      field('Интервал бэкапов игры, с', backup, '0 — оставить как в игре. Каждый бэкап копирует базу целиком: чем реже, тем меньше износ SSD.'),
      field('Перезапускать при падении', restart),
      field('Сразу установить файлы', installNow, 'SteamCMD скачает сервер (~30 ГБ).'),
    ), h('div', { class: 'row end' }, create)));
  id.focus();
}

// ---------- server settings helpers ----------

const MAPS = [['/Game/Maps/ConanSandbox/ConanSandbox', 'Изгнанные земли (Exiled Lands)']];
const DEFAULT_WARNING_TEXT = 'Внимание! Сервер будет {action} через {minutes} мин.';
const DEFAULT_WARNINGS = [10, 5, 2].map(minutes => ({ minutes, message: DEFAULT_WARNING_TEXT }));
const HOOK_LABELS = {
  pre_start: 'Перед запуском', post_start: 'После запуска процесса', on_ready: 'Сервер готов',
  pre_stop: 'Перед остановкой', post_stop: 'После остановки', on_crash: 'После падения',
  pre_backup: 'Перед бэкапом', post_backup: 'После бэкапа', pre_update: 'Перед обновлением', post_update: 'После обновления',
};

function hooksHelp(info) {
  if (!info.enabled) return h('p', { class: 'muted', text: 'Скрипты отключены на этом менеджере.' });
  return h('div', {},
    h('p', { class: 'small' }, 'Доступные скрипты: ', info.scripts.length
      ? info.scripts.map((x, i) => [i ? ', ' : '', h('span', { class: 'mono', text: x.name }), x.usable ? '' : h('span', { class: 'badge bad', text: 'не запустится' })])
      : h('span', { class: 'muted', text: 'пока нет' }), '. Несколько скриптов — через запятую, выполняются по порядку.'),
    h('details', {}, h('summary', { class: 'small', text: 'Как добавить свой скрипт' }),
      h('ol', { class: 'small' },
        h('li', {}, 'Положите файл в ', h('span', { class: 'mono', text: info.dir }), ' на сервере (по SSH).'),
        h('li', {}, 'Сделайте владельцем root и исполняемым: ', h('span', { class: 'mono', text: 'chown root:root скрипт; chmod 755 скрипт' }), '. Файл, который может менять кто-то кроме root, не запустится — иначе через него можно было бы выполнить что угодно с правами менеджера.'),
        h('li', {}, 'Обновите эту страницу — скрипт появится в списке, впишите его имя в нужное событие и сохраните.')),
      h('p', { class: 'small', text: 'Скрипту передаются переменные: CONAN_SERVER_ID, CONAN_SERVER_NAME, CONAN_INSTALL_DIR, CONAN_SAVED_DIR, CONAN_GAME_PORT, CONAN_QUERY_PORT, CONAN_RCON_PORT, CONAN_RCON_PASSWORD; для бэкапа ещё CONAN_BACKUP_PATH, для падения — CONAN_EXIT_CODE. «Перед запуском», «Перед обновлением» и «Перед бэкапом» могут отменить действие, завершившись с ненулевым кодом. Лимит — 5 минут.' }),
      h('pre', { class: 'log', text: '#!/bin/sh\n# /etc/conan-manager/hooks/upload-backup — копия бэкапа на другой сервер\nscp "$CONAN_BACKUP_PATH" backup@storage:/backups/"$CONAN_SERVER_ID"/\n\n#!/bin/sh\n# /etc/conan-manager/hooks/hello — сообщение в игру через RCON\nconan-manager rcon -p "$CONAN_RCON_PORT" -q "broadcast Сервер снова в строю!"' })));
}

// GAME_FIELDS are the settings the Dedicated Server Launcher shows on its main
// page. They live in the game's INI files, which stay the only place they are
// stored; the form reads and writes those keys.
const REGIONS = [['0', 'Европа'], ['1', 'Северная Америка'], ['2', 'Азия'], ['3', 'Австралия'], ['4', 'Южная Америка'], ['5', 'Япония']];
const GAME_FIELDS = [
  { file: 'Engine', section: 'OnlineSubsystem', key: 'ServerName', label: 'Название в списке серверов', def: '' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'ServerPassword', label: 'Пароль для входа', type: 'password', def: '', hint: 'Пусто — вход без пароля.' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'AdminPassword', label: 'Пароль администратора', type: 'password', def: '' },
  { file: 'Game', section: '/Script/Engine.GameSession', key: 'MaxPlayers', label: 'Максимум игроков', type: 'number', def: '40' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'serverRegion', label: 'Регион', options: REGIONS, def: '0' },
  { file: 'Engine', section: '/Script/OnlineSubsystemUtils.IpNetDriver', key: 'NetServerMaxTickRate', label: 'Максимальный тикрейт', type: 'number', def: '30' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'PVPEnabled', label: 'PvP', bool: true, def: 'False' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'IsBattlEyeEnabled', label: 'BattlEye', bool: true, def: 'False' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'ServerMessageOfTheDay', label: 'Сообщение дня', def: '' },
  { group: 'RCON' },
  { file: 'Game', section: 'RconPlugin', key: 'RconEnabled', label: 'Включить RCON', bool: true, def: 'False', hint: 'Нужен для консоли, списка игроков и предупреждений.' },
  { file: 'Game', section: 'RconPlugin', key: 'RconPort', label: 'Порт RCON', type: 'number', def: '25575' },
  { file: 'Game', section: 'RconPlugin', key: 'RconPassword', label: 'Пароль RCON', type: 'password', def: '' },
  { file: 'Game', section: 'RconPlugin', key: 'RconMaxKarma', label: 'Karma', type: 'number', def: '60', hint: 'Сколько неверных паролей подряд терпит сервер, прежде чем заблокировать адрес.' },
];

async function gameSettings(ctx, running, reload) {
  const views = {};
  try {
    for (const file of new Set(GAME_FIELDS.filter(f => f.file).map(f => f.file))) {
      views[file] = await api('GET', `/servers/${enc(ctx.id)}/config/${file}`);
    }
  } catch (e) { return h('div', { class: 'notice bad', text: 'Не удалось прочитать настройки игры: ' + e.message }); }
  // Unreal matches sections and keys without regard to case.
  const current = f => {
    const section = views[f.file].sections.find(s => s.name.toLowerCase() === f.section.toLowerCase());
    const entry = section && section.entries.find(e => e.key.toLowerCase() === f.key.toLowerCase());
    return entry ? entry.value : null;
  };
  const rows = [];
  const controls = [];
  for (const f of GAME_FIELDS) {
    if (f.group) { rows.push(h('h3', { style: 'grid-column:1/-1;margin:8px 0 0', text: f.group })); continue; }
    const stored = current(f);
    const value = stored ?? f.def;
    let input;
    if (f.bool) input = h('input', { type: 'checkbox', checked: /^true$/i.test(value) });
    else if (f.options) input = h('select', {}, f.options.map(([v, t]) => h('option', { value: v, text: `${v} — ${t}`, selected: v === value })));
    else input = h('input', { type: f.type || 'text', value, autocomplete: 'off' });
    if (f.type === 'password') input.onfocus = () => { input.type = 'text'; };
    input.disabled = running;
    controls.push({ f, input, stored, value });
    rows.push(field(f.label, input, f.hint));
  }
  const save = h('button', { class: 'primary', text: 'Сохранить настройки игры', disabled: running, onclick: e => act(e.target, async () => {
    const byFile = {};
    for (const { f, input, stored } of controls) {
      const now = f.bool ? (input.checked ? 'True' : 'False') : input.value.trim();
      // A key the file does not have yet is written only when it differs from
      // the game's default, so saving does not fill the file with defaults.
      if (now === (stored ?? f.def)) continue;
      ((byFile[f.file] = byFile[f.file] || {})[f.section] = byFile[f.file][f.section] || {})[f.key] = now;
    }
    if (!Object.keys(byFile).length) { toast('Изменений нет', 'info'); return; }
    for (const [file, set] of Object.entries(byFile)) await api('PATCH', `/servers/${enc(ctx.id)}/config/${file}`, { set });
    toast('Настройки игры сохранены'); reload();
  }) });
  return h('div', {}, h('h2', { text: 'Настройки игры' }),
    running ? h('div', { class: 'notice', text: 'Сервер работает: игра читает эти файлы при старте и переписывает при остановке, поэтому менять их можно только на остановленном сервере.' })
      : h('p', { class: 'muted small', text: 'Хранятся в INI-файлах игры (вкладка «Конфиги»), вступают в силу при следующем запуске.' }),
    h('div', { class: 'form' }, rows),
    h('div', { class: 'row end', style: 'margin-top:8px' }, save));
}

// ---------- server page ----------

const TABS = [
  ['overview', 'Обзор'], ['settings', 'Настройки'], ['console', 'RCON'], ['logs', 'Логи'],
  ['schedule', 'Рестарты'], ['backups', 'Бэкапы'], ['config', 'Конфиги'], ['history', 'История'],
];

async function viewServer(id, tab) {
  tab = TABS.some(t => t[0] === tab) ? tab : 'overview';
  const headBox = h('div', { class: 'head' });
  const body = h('div');
  replace(app, headBox,
    h('nav', { class: 'tabs' }, TABS.map(([key, label]) => h('a', { href: `#/server/${enc(id)}/${key}`, class: key === tab ? 'active' : '', text: label }))),
    body);
  const ctx = { id, detail: null };
  ctx.refresh = async () => {
    try { ctx.detail = await api('GET', `/servers/${enc(id)}`); }
    catch (e) {
      if (e.status === 404) { replace(app, h('div', { class: 'notice bad', text: `Сервер «${id}» не найден.` }), h('a', { href: '#/', text: '← Серверы' })); }
      else toast(e.message, 'bad');
      return false;
    }
    const d = ctx.detail;
    replace(headBox, h('a', { href: '#/', text: '← Серверы' }), h('h1', { text: d.server.name }),
      h('span', { class: 'muted mono small', text: d.server.id }), stateBadge(d.lifecycle.state),
      h('span', { class: 'spacer' }), actionButtons(d, ctx.refresh));
    if (ctx.onRefresh) ctx.onRefresh(d);
    return true;
  };
  if (!await ctx.refresh()) return;
  every(5000, ctx.refresh);
  await TAB_VIEWS[tab](body, ctx);
}

const TAB_VIEWS = {
  async overview(body, ctx) {
    const info = h('div', { class: 'panel' });
    const recovery = h('div');
    const update = h('div', { class: 'panel' });
    replace(body, recovery, info, update);
    const draw = d => {
      const s = d.server, p = d.process || {}, hl = d.health || {}, a = hl.availability || {};
      replace(recovery, s.recovery_reason ? h('div', { class: 'notice bad' },
        h('strong', { text: 'Запуск заблокирован. ' }), s.recovery_reason,
        h('div', { class: 'row', style: 'margin-top:8px' },
          h('button', { text: 'Проверить базу ещё раз', onclick: e => act(e.target, async () => {
            await api('POST', `/servers/${enc(s.id)}/verify`); toast('База цела, блокировка снята'); ctx.refresh();
          }) }),
          h('a', { href: `#/server/${enc(s.id)}/backups`, text: 'Восстановить из бэкапа' }))) : null);
      replace(info, h('h2', { text: 'Состояние' }), facts([
        ['Состояние', stateBadge(d.lifecycle.state)],
        d.lifecycle.error && ['Ошибка', d.lifecycle.error],
        ['Процесс', p.pid ? `PID ${p.pid}, запущен ${fmtTime(p.started_at)}` : '—'],
        ['Доступность', h('span', {}, availabilityBadge(a), a.checked_at && isSet(a.checked_at) ? h('span', { class: 'muted small', text: '  проверено ' + fmtAgo(a.checked_at) }) : null)],
        a.last_error && ['Ошибка query', a.last_error],
        ['Последний выход', hl.last_exit ? `${hl.last_exit.signal ? 'сигнал ' + hl.last_exit.signal : 'код ' + hl.last_exit.code}, ${fmtTime(hl.last_exit.at)}` : '—'],
        ['Падений за 30 мин', String(hl.recent_crashes || 0)],
        hl.restart_pending && ['Автоперезапуск', fmtAgo(hl.restart_at)],
        hl.reason && ['Watchdog', hl.reason],
        ['Желаемое состояние', s.desired_state === 'running' ? 'работает' : 'остановлен'],
        ['Порты', `игра ${s.game_port} (+1), query ${s.query_port}`],
        ['Каталог', h('span', { class: 'mono', text: s.install_dir })],
        ['Ветка', s.branch],
        ['Перезапуск при падении', s.restart_on_crash ? 'да' : 'нет'],
        ['Автообновление', s.auto_update ? 'да' : 'нет'],
        ['Бэкап игры', s.periodic_backup_interval_seconds ? `каждые ${Math.round(s.periodic_backup_interval_seconds / 60)} мин` : 'как в игре'],
      ]));
    };
    ctx.onRefresh = draw;
    draw(ctx.detail);

    const status = h('div', { class: 'muted', text: 'Проверка обращается к Steam и занимает несколько секунд.' });
    const validate = h('input', { type: 'checkbox' });
    const force = h('input', { type: 'checkbox' });
    const warn = h('input', { type: 'checkbox', checked: true });
    replace(update, h('h2', { text: 'Обновление' }), status,
      h('div', { class: 'row', style: 'margin-top:10px' },
        h('button', { text: 'Проверить', onclick: e => act(e.target, async () => {
          const u = await api('GET', `/servers/${enc(ctx.id)}/update`);
          replace(status, u.error ? h('span', { class: 'badge bad', text: 'Ошибка: ' + u.error })
            : h('span', {}, u.available ? h('span', { class: 'badge warn', text: 'Есть обновление' }) : h('span', { class: 'badge ok', text: 'Актуально' }),
              `  установлена ${u.installed || '—'}, в Steam ${u.latest || '—'} (ветка ${u.branch})`));
        }) }),
        h('label', { class: 'row' }, warn, 'предупредить игроков'),
        h('label', { class: 'row' }, validate, 'проверить все файлы'),
        h('label', { class: 'row' }, force, 'даже если актуально'),
        h('button', { class: 'primary', text: 'Обновить', onclick: e => act(e.target, async () => {
          if (!await ask('Обновить сервер? Работающий сервер будет остановлен, мир сохранён в бэкап, затем сервер запустится снова.', { ok: 'Обновить' })) return;
          await runJob(await api('POST', `/servers/${enc(ctx.id)}/update`, { validate: validate.checked, force: force.checked, warn: warn.checked }));
          ctx.refresh();
        }) })));
  },

  async console(body, ctx) {
    const playersBox = h('div');
    const out = h('pre', { class: 'log', style: 'min-height:260px' });
    const input = h('input', { placeholder: 'Команда RCON, например listplayers', style: 'flex:1', autocomplete: 'off' });
    const broadcast = h('input', { placeholder: 'Сообщение всем игрокам', style: 'flex:1' });
    const history = []; let cursor = 0;
    const print = (text, cls) => {
      const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 30;
      out.append(h('span', { class: cls || '', text: text.endsWith('\n') ? text : text + '\n' }));
      if (atBottom) out.scrollTop = out.scrollHeight;
    };
    const run = async command => {
      if (!command.trim()) return;
      history.push(command); cursor = history.length;
      print('> ' + command, 'cmd');
      try { print((await api('POST', `/servers/${enc(ctx.id)}/rcon`, { command })).response || '(пустой ответ)'); }
      catch (e) { print(e.message, 'err'); }
    };
    input.onkeydown = e => {
      if (e.key === 'Enter') { const c = input.value; input.value = ''; run(c); }
      else if (e.key === 'ArrowUp' && cursor > 0) { input.value = history[--cursor]; e.preventDefault(); }
      else if (e.key === 'ArrowDown') { cursor = Math.min(history.length, cursor + 1); input.value = history[cursor] || ''; }
    };
    const say = () => { const text = broadcast.value.trim(); if (text) { broadcast.value = ''; run('broadcast ' + text); } };
    broadcast.onkeydown = e => { if (e.key === 'Enter') say(); };
    const loadPlayers = async () => {
      if (ctx.detail.lifecycle.state !== 'running') { replace(playersBox, h('p', { class: 'muted', text: 'Сервер не запущен.' })); return; }
      try {
        const r = await api('GET', `/servers/${enc(ctx.id)}/players`);
        const columns = r.players.length ? Object.keys(r.players[0]) : [];
        replace(playersBox, h('p', { text: `Онлайн: ${r.count}` }), table(columns, r.players.map(p => columns.map(c => p[c] || ''))  , 'Никого нет'));
      } catch (e) { replace(playersBox, h('div', { class: 'notice', text: e.message })); }
    };
    replace(body,
      h('div', { class: 'panel' }, h('div', { class: 'head' }, h('h2', { text: 'Игроки' }), h('span', { class: 'spacer' }),
        h('button', { text: 'Обновить', onclick: e => act(e.target, loadPlayers) })), playersBox),
      h('div', { class: 'panel' }, h('h2', { text: 'Консоль RCON' }), out,
        h('div', { class: 'row', style: 'margin-top:8px' }, input,
          h('button', { class: 'primary', text: 'Выполнить', onclick: () => { const c = input.value; input.value = ''; run(c); } }),
          h('button', { text: 'help', onclick: () => run('help') }),
          h('button', { text: 'listplayers', onclick: () => run('listplayers') })),
        h('div', { class: 'row', style: 'margin-top:8px' }, broadcast, h('button', { text: 'Объявить', onclick: say })),
        h('p', { class: 'muted small', text: 'Настройки RCON берутся из секции [RconPlugin] файла Game.ini (вкладка «Конфиги»). Команды пишутся в лог менеджера.' })));
    loadPlayers();
    every(20000, loadPlayers);
  },

  async logs(body, ctx) {
    const name = h('select', {}, h('option', { value: 'game', text: 'Игра (ConanSandbox.log)' }), h('option', { value: 'process', text: 'Процесс (вывод)' }));
    const filter = h('input', { placeholder: 'Фильтр, например Join succeeded', style: 'flex:1' });
    const limit = h('select', {}, [200, 500, 1000, 5000].map(n => h('option', { value: n, text: n + ' строк' })));
    const auto = h('input', { type: 'checkbox', checked: true });
    const out = h('pre', { class: 'log', style: 'min-height:300px' });
    const load = async () => {
      try {
        const q = `name=${enc(name.value)}&limit=${limit.value}` + (filter.value ? `&filter=${enc(filter.value)}` : '');
        const r = await api('GET', `/servers/${enc(ctx.id)}/logs?${q}`);
        const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 30 || !out.childNodes.length;
        replace(out, r.lines.length ? r.lines.map(line => h('span', {
          class: /\b(Error|Fatal|Critical)\b/.test(line) ? 'err' : /\bWarning\b/.test(line) ? 'hit' : '',
          text: line + '\n' })) : h('span', { class: 'muted', text: filter.value ? 'Нет строк с таким текстом.' : 'Лог пуст.' }));
        if (atBottom) out.scrollTop = out.scrollHeight;
      } catch (e) { replace(out, h('span', { class: 'err', text: e.message })); }
    };
    let timer = null;
    filter.oninput = () => { clearTimeout(timer); timer = setTimeout(load, 400); };
    name.onchange = limit.onchange = load;
    replace(body, h('div', { class: 'panel' },
      h('div', { class: 'row', style: 'margin-bottom:10px' }, name, filter, limit,
        h('label', { class: 'row' }, auto, 'обновлять'),
        h('button', { text: 'Обновить', onclick: e => act(e.target, load) })),
      out));
    await load();
    every(5000, () => { if (auto.checked && document.visibilityState === 'visible') load(); });
  },

  async config(body, ctx) {
    let file = 'ServerSettings';
    const box = h('div');
    const draw = async () => {
      const running = ['running', 'starting', 'stopping'].includes(ctx.detail.lifecycle.state);
      let view;
      try { view = await api('GET', `/servers/${enc(ctx.id)}/config/${file}`); }
      catch (e) { replace(box, h('div', { class: 'notice bad', text: e.message })); return; }
      const text = h('textarea', { class: 'mono', rows: 32, value: view.content, disabled: running, spellcheck: 'false' });
      replace(box,
        h('div', { class: 'row', style: 'margin-bottom:12px' },
          ['ServerSettings', 'Game', 'Engine'].map(f => h('button', { class: f === file ? 'primary' : '', text: f + '.ini', onclick: () => { file = f; draw(); } }))),
        h('p', { class: 'muted small mono', text: view.path }),
        running ? h('div', { class: 'notice', text: 'Сервер работает: править можно только после остановки. Игра читает файлы при старте и переписывает при остановке — правка на ходу потерялась бы.' }) : null,
        !view.exists ? h('div', { class: 'notice', text: 'Файла ещё нет: игра создаст его при первом запуске. Можно написать его заранее.' }) : null,
        view.managed && view.managed.length ? h('div', { class: 'notice', text: 'Менеджер переписывает перед стартом: ' + view.managed.join(', ') + '.' }) : null,
        h('p', { class: 'muted small', text: 'Основные настройки удобнее менять во вкладке «Настройки» — там они собраны в форму. Здесь — всё остальное.' }),
        h('div', { class: 'panel' }, text, h('div', { class: 'row end', style: 'margin-top:8px' },
          h('button', { class: 'primary', text: 'Сохранить файл', disabled: running, onclick: e => act(e.target, async () => {
            await api('PUT', `/servers/${enc(ctx.id)}/config/${file}`, { content: text.value });
            toast('Сохранено'); draw();
          }) }))),
        await versionsPanel(running));
    };
    const versionsPanel = async running => {
      let versions = [];
      try { versions = await api('GET', `/servers/${enc(ctx.id)}/config/${file}/versions`); } catch { versions = []; }
      return h('div', { class: 'panel' }, h('h2', { text: 'Прошлые версии' }),
        table(['Сохранена', 'Причина', 'Размер', ''], versions.map(v => [fmtTime(v.created_at), v.reason, fmtBytes(v.size),
          h('button', { text: 'Вернуть', disabled: running, onclick: e => act(e.target, async () => {
            if (!await ask(`Вернуть ${file}.ini к версии от ${fmtTime(v.created_at)}? Текущее содержимое тоже сохранится как версия.`, { ok: 'Вернуть', danger: false })) return;
            await api('POST', `/servers/${enc(ctx.id)}/config/${file}/versions/${v.id}/restore`);
            toast('Версия возвращена'); draw();
          }) })]), 'Правок через менеджер ещё не было'));
    };
    let lastState = ctx.detail.lifecycle.state;
    ctx.onRefresh = d => { if (d.lifecycle.state !== lastState) { lastState = d.lifecycle.state; draw(); } };
    replace(body, box);
    await draw();
  },

  async backups(body, ctx) {
    const box = h('div');
    const reason = h('input', { value: 'manual', style: 'width:160px', pattern: '[a-z0-9][a-z0-9-]{0,31}' });
    const draw = async () => {
      const running = ['running', 'starting', 'stopping'].includes(ctx.detail.lifecycle.state);
      let list;
      try { list = await api('GET', `/servers/${enc(ctx.id)}/backups`); } catch (e) { replace(box, h('div', { class: 'notice bad', text: e.message })); return; }
      replace(box, table(['Время', 'Причина', 'Размер', 'SHA-256', 'Файл', ''], list.map(b => [
        fmtTime(b.created_at), b.reason, fmtBytes(b.size_bytes),
        h('span', { class: 'mono small', title: b.sha256, text: (b.sha256 || '').slice(0, 12) }),
        b.exists ? h('span', { class: 'mono small', title: b.path, text: b.path.split('/').pop() }) : h('span', { class: 'badge bad', text: 'файла нет' }),
        h('div', { class: 'row' },
          h('button', { text: 'Восстановить', disabled: running || !b.exists, title: running ? 'Сначала остановите сервер' : '', onclick: e => act(e.target, async () => {
            if (!await ask(`Заменить мир бэкапом от ${fmtTime(b.created_at)}? Текущий мир будет сохранён отдельным бэкапом «pre-restore».`, { ok: 'Восстановить' })) return;
            await runJob(await api('POST', `/servers/${enc(ctx.id)}/backups/${b.id}/restore`));
            draw(); ctx.refresh();
          }) }),
          h('button', { class: 'danger', text: 'Удалить', onclick: e => act(e.target, async () => {
            if (!await ask(`Удалить бэкап от ${fmtTime(b.created_at)}? Файл будет удалён с диска.`, { ok: 'Удалить' })) return;
            await api('DELETE', `/servers/${enc(ctx.id)}/backups/${b.id}`); toast('Удалено'); draw();
          }) })),
      ]), 'Бэкапов менеджера ещё нет'));
    };
    let lastState = ctx.detail.lifecycle.state;
    ctx.onRefresh = d => { if (d.lifecycle.state !== lastState) { lastState = d.lifecycle.state; draw(); } };
    replace(body, h('div', { class: 'panel' },
      h('div', { class: 'head' }, h('h2', { text: 'Бэкапы менеджера' }), h('span', { class: 'spacer' }), reason,
        h('button', { class: 'primary', text: 'Сделать бэкап', onclick: e => act(e.target, async () => {
          await runJob(await api('POST', `/servers/${enc(ctx.id)}/backup`, { reason: reason.value.trim() || 'manual' }));
          draw();
        }) })),
      h('p', { class: 'muted small', text: 'Копия снимается с работающего сервера без остановки. Восстановление — только на остановленном. Бэкап также делается перед каждым обновлением.' }),
      box));
    await draw();
  },

  async schedule(body, ctx) {
    const box = h('div');
    const runsBox = h('div');
    const pad = n => String(n).padStart(2, '0');
    const PER_DAY = [1, 2, 3, 4, 6, 8, 12, 24];
    // A simple schedule is restarts spread evenly over the day from a first
    // time, which is what the form edits; anything else is shown as cron.
    const parseSimple = s => {
      if (!s || s.mode !== 'cron' || s.action !== 'restart') return null;
      const m = /^(\d{1,2}) ([\d,]+) \* \* \*$/.exec(s.cron_expression || '');
      if (!m) return null;
      const hours = m[2].split(',').map(Number).sort((a, b) => a - b);
      const n = hours.length;
      if (!PER_DAY.includes(n)) return null;
      const step = 24 / n;
      const first = hours.find(x => x < step) ?? hours[0];
      const expected = Array.from({ length: n }, (_, i) => (first + i * step) % 24).sort((a, b) => a - b);
      if (expected.join() !== hours.join()) return null;
      return { minute: +m[1], first, perDay: n };
    };
    const hoursOf = (first, perDay) => Array.from({ length: perDay }, (_, i) => (first + i * (24 / perDay)) % 24);
    const zones = ['Europe/Moscow', 'Europe/Kaliningrad', 'Europe/Samara', 'Asia/Yekaterinburg', 'Asia/Novosibirsk', 'Asia/Vladivostok', 'Europe/Kyiv', 'Europe/Minsk', 'Europe/Berlin', 'Europe/London', 'UTC'];
    const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

    const draw = async () => {
      let current = null;
      try { current = await api('GET', `/servers/${enc(ctx.id)}/schedule`); } catch (e) { if (e.status !== 404) toast(e.message, 'bad'); }
      const s = ctx.detail.server;
      const simple = parseSimple(current);
      const custom = current && !simple;

      const enabled = h('input', { type: 'checkbox', checked: current ? current.enabled && !custom : false });
      const first = h('input', { type: 'time', value: simple ? `${pad(simple.first)}:${pad(simple.minute)}` : '06:00' });
      const perDay = h('select', {}, PER_DAY.map(n => h('option', { value: n, selected: simple ? simple.perDay === n : n === 1,
        text: n === 1 ? '1 — раз в сутки' : `${n} — каждые ${24 / n} ч` })));
      const zone = h('input', { list: 'zones', value: current && current.timezone && current.timezone !== 'UTC' ? current.timezone : localZone });
      const minUptime = h('input', { type: 'time', value: `${pad(Math.floor((s.restart_min_uptime_seconds || 0) / 3600))}:${pad(Math.floor((s.restart_min_uptime_seconds || 0) % 3600 / 60))}` });
      const preview = h('p', { class: 'notice' });
      const showPreview = () => {
        const [hh, mm] = (first.value || '06:00').split(':').map(Number);
        const hours = hoursOf(hh, +perDay.value).sort((a, b) => a - b);
        replace(preview, enabled.checked
          ? ['Сервер будет перезапускаться каждый день в ', h('strong', { text: hours.map(x => `${pad(x)}:${pad(mm)}`).join(', ') }), ` (${zone.value}).`,
            minUptime.value !== '00:00' ? ` Рестарт пропускается, если сервер проработал меньше ${minUptime.value}.` : '']
          : 'Автоматический рестарт выключен.');
      };
      [enabled, first, perDay, zone, minUptime].forEach(x => { x.oninput = x.onchange = showPreview; });
      showPreview();

      const warnings = s.warnings && s.warnings.steps && s.warnings.steps.length;
      const save = h('button', { class: 'primary', text: 'Сохранить', onclick: e => act(e.target, async () => {
        const [hh, mm] = first.value.split(':').map(Number);
        const hours = hoursOf(hh, +perDay.value).sort((a, b) => a - b);
        const [uh, um] = (minUptime.value || '00:00').split(':').map(Number);
        const uptime = uh * 3600 + um * 60;
        if (uptime !== (s.restart_min_uptime_seconds || 0)) await api('PATCH', `/servers/${enc(s.id)}`, { restart_min_uptime_seconds: uptime });
        if (enabled.checked) {
          await api('PUT', `/servers/${enc(s.id)}/schedule`, { action: 'restart', enabled: true, mode: 'cron', cron_expression: `${mm} ${hours.join(',')} * * *`, timezone: zone.value.trim() || 'UTC' });
        } else if (current && !custom) {
          await api('DELETE', `/servers/${enc(s.id)}/schedule`);
        }
        toast('Сохранено'); await ctx.refresh(); draw();
      }) });

      // A schedule the simple form cannot show stays editable as cron.
      const action = h('select', {}, [['restart', 'Рестарт'], ['stop', 'Остановка'], ['start', 'Запуск']].map(([v, t]) => h('option', { value: v, text: t, selected: current ? current.action === v : v === 'restart' })));
      const cron = h('input', { class: 'mono', value: custom && current.mode === 'cron' ? current.cron_expression : '0 6 * * *' });
      const cronZone = h('input', { list: 'zones', value: current && current.timezone ? current.timezone : localZone });
      const advanced = h('details', { open: custom },
        h('summary', { text: 'Своё расписание (cron)' }),
        h('p', { class: 'muted small', text: 'Для случаев, которые форма выше не покрывает: например, остановка на ночь и запуск утром (для этого нужны два расписания — сейчас на сервер одно). Формат: минута час день месяц день-недели; 0 — воскресенье. Пример: «30 4 * * 1» — по понедельникам в 04:30.' }),
        custom ? h('div', { class: 'notice', text: `Сейчас задано своё расписание: ${current.mode === 'cron' ? current.cron_expression + ' (' + current.timezone + ')' : current.mode === 'daily' ? 'ежедневно в ' + current.daily_time + ' UTC' : 'каждые ' + Math.round(current.interval_seconds / 60) + ' мин'}, действие «${current.action}».` }) : null,
        h('div', { class: 'form' }, field('Действие', action), field('Cron', cron), field('Часовой пояс', cronZone)),
        h('div', { class: 'row end' }, h('button', { text: 'Сохранить своё расписание', onclick: e => act(e.target, async () => {
          await api('PUT', `/servers/${enc(s.id)}/schedule`, { action: action.value, enabled: true, mode: 'cron', cron_expression: cron.value.trim(), timezone: cronZone.value.trim() || 'UTC' });
          toast('Сохранено'); draw();
        }) })));

      replace(box,
        h('datalist', { id: 'zones' }, zones.concat(zones.includes(localZone) ? [] : [localZone]).map(z => h('option', { value: z }))),
        h('div', { class: 'panel' }, h('h2', { text: 'Автоматический рестарт' }),
          current && current.enabled ? h('p', {}, 'Следующий: ', h('strong', { text: fmtTime(current.next_run) }), h('span', { class: 'muted', text: ' (' + fmtAgo(current.next_run) + ')' }),
            current.failure_count ? h('span', { class: 'badge bad', style: 'margin-left:8px', text: 'ошибок подряд: ' + current.failure_count }) : null) : null,
          h('div', { class: 'form' },
            field('Рестарт каждый день', enabled),
            field('Первый рестарт в', first, 'Остальные — через равные промежутки от этого времени.'),
            field('Рестартов в сутки', perDay),
            field('Часовой пояс', zone),
            field('Минимальный аптайм (чч:мм)', minUptime, 'Если сервер запущен недавно (вручную или после падения), плановый рестарт пропускается. 00:00 — не проверять.')),
          preview,
          h('p', { class: 'muted small' }, warnings
            ? `Перед рестартом игроки получат предупреждения за ${s.warnings.steps.map(x => x.minutes).sort((a, b) => b - a).join(', ')} мин. — если кто-то онлайн. `
            : 'Предупреждения игрокам не настроены. ', h('a', { href: `#/server/${enc(s.id)}/settings`, text: 'Настроить' })),
          h('div', { class: 'row end' }, save)),
        h('div', { class: 'panel' }, advanced));

      try {
        const runs = await api('GET', `/servers/${enc(ctx.id)}/schedule/runs`);
        replace(runsBox, h('div', { class: 'panel' }, h('h2', { text: 'Последние запуски' }),
          table(['Начало', 'Конец', 'Действие', 'Результат', 'Ошибка'], runs.slice(0, 30).map(r => [
            fmtTime(r.started_at), fmtTime(r.finished_at), r.action,
            h('span', { class: 'badge ' + (RUN_STATUS[r.status] || [0, 'bad'])[1], text: (RUN_STATUS[r.status] || [r.status])[0] }),
            h('span', { class: 'small', text: r.error || '' })]), 'Запусков ещё не было')));
      } catch { replace(runsBox); }
    };
    replace(body, box, runsBox);
    await draw();
  },

  async settings(body, ctx) {
    let hooksInfo = { enabled: false, events: [], scripts: [], dir: '' };
    try { hooksInfo = await api('GET', '/hooks'); } catch { /* shown as disabled */ }
    const s = ctx.detail.server;
    const running = ['running', 'starting', 'stopping'].includes(ctx.detail.lifecycle.state);

    // ----- manager and launch settings (the server record) -----
    const knownMap = MAPS.some(([path]) => path === (s.map || MAPS[0][0]));
    const mapSelect = h('select', {}, MAPS.map(([path, label]) => h('option', { value: path, text: label, selected: (s.map || MAPS[0][0]) === path })),
      h('option', { value: 'custom', text: 'Свой путь…', selected: !knownMap }));
    const mapPath = h('input', { class: 'mono', value: s.map || MAPS[0][0], style: knownMap ? 'display:none' : '' });
    mapSelect.onchange = () => {
      mapPath.style.display = mapSelect.value === 'custom' ? '' : 'none';
      if (mapSelect.value !== 'custom') mapPath.value = mapSelect.value;
    };
    const inputs = {
      name: h('input', { value: s.name }),
      restart_on_crash: h('input', { type: 'checkbox', checked: s.restart_on_crash }),
      auto_update: h('input', { type: 'checkbox', checked: s.auto_update }),
      backup_on_stop: h('input', { type: 'checkbox', checked: s.backup_on_stop }),
      branch: h('input', { value: s.branch }),
      launch_args: h('input', { class: 'mono', value: s.launch_args }),
      multihome: h('input', { value: s.multihome || '' }),
      game_port: h('input', { type: 'number', value: s.game_port }),
      query_port: h('input', { type: 'number', value: s.query_port }),
      periodic_backup_interval_seconds: h('input', { type: 'number', min: 0, value: s.periodic_backup_interval_seconds }),
    };
    const zombieOn = h('input', { type: 'checkbox', checked: s.zombie_check_seconds > 0 });
    const zombieMinutes = h('input', { type: 'number', min: 1, max: 60, value: s.zombie_check_seconds > 0 ? Math.round(s.zombie_check_seconds / 60) : 5, style: 'width:90px' });

    // ----- warnings -----
    const w = s.warnings && s.warnings.steps && s.warnings.steps.length ? s.warnings : null;
    const warnOn = h('input', { type: 'checkbox', checked: !!w });
    const warnCommand = h('input', { value: (w && w.command) || 'broadcast', style: 'width:160px' });
    const stepsBox = h('div');
    const addStep = (minutes, message) => {
      const m = h('input', { type: 'number', min: 1, max: 120, value: minutes, style: 'width:80px' });
      const text = h('input', { value: message, style: 'flex:1' });
      const row = h('div', { class: 'row step', style: 'margin-bottom:6px' }, h('span', { class: 'muted', text: 'за' }), m, h('span', { class: 'muted', text: 'мин.' }), text,
        h('button', { class: 'ghost', text: '✕', title: 'Убрать', onclick: () => row.remove() }));
      row.read = () => ({ minutes: +m.value, message: text.value.trim() });
      stepsBox.append(row);
    };
    (w ? w.steps : DEFAULT_WARNINGS).slice().sort((a, b) => b.minutes - a.minutes).forEach(x => addStep(x.minutes, x.message || DEFAULT_WARNING_TEXT));
    const warnBody = h('div', { style: w ? '' : 'display:none' }, stepsBox,
      h('div', { class: 'row', style: 'margin:6px 0 12px' }, h('button', { text: '+ Шаг', onclick: () => addStep(1, DEFAULT_WARNING_TEXT) })),
      h('div', { class: 'form' }, field('RCON-команда объявления', warnCommand, 'У Conan это broadcast.')),
      h('p', { class: 'muted small', text: '{minutes} заменяется числом минут, {action} — словом «перезапущен», «остановлен» или «обновлён».' }));
    warnOn.onchange = () => { warnBody.style.display = warnOn.checked ? '' : 'none'; };

    // ----- Discord texts -----
    const dm = s.discord_messages || {};
    const discord = {
      ready: h('input', { value: dm.ready || '' }),
      restarted: h('input', { value: dm.restarted || '' }),
      stopped: h('input', { value: dm.stopped || '' }),
    };

    // ----- hooks -----
    const datalist = h('datalist', { id: 'hook-scripts' }, hooksInfo.scripts.map(x => h('option', { value: x.name })));
    const hookInputs = Object.fromEntries(hooksInfo.events.map(ev => [ev, h('input', { class: 'mono', list: 'hook-scripts', value: ((s.hooks || {})[ev] || []).join(', ') })]));
    const list = text => text.split(',').map(x => x.trim()).filter(Boolean);

    const save = h('button', { class: 'primary', text: 'Сохранить', onclick: e => act(e.target, async () => {
      const patch = {};
      for (const [key, input] of Object.entries(inputs)) {
        const value = input.type === 'checkbox' ? input.checked : input.type === 'number' ? +input.value : input.value.trim();
        if (value !== (s[key] ?? (input.type === 'checkbox' ? false : ''))) patch[key] = value;
      }
      const map = mapSelect.value === 'custom' ? mapPath.value.trim() : mapSelect.value;
      const mapValue = map === MAPS[0][0] ? '' : map;
      if (mapValue !== (s.map || '')) patch.map = mapValue;
      const zombie = zombieOn.checked ? Math.round(+zombieMinutes.value * 60) : 0;
      if (zombie !== (s.zombie_check_seconds || 0)) patch.zombie_check_seconds = zombie;
      const warnings = warnOn.checked
        ? { steps: [...stepsBox.children].map(r => r.read()), command: warnCommand.value.trim() || 'broadcast' }
        : { steps: [] };
      if (JSON.stringify(warnings) !== JSON.stringify(w ? { steps: w.steps, command: w.command || 'broadcast' } : { steps: [] })) patch.warnings = warnings;
      const messages = Object.fromEntries(Object.entries(discord).map(([k, i]) => [k, i.value.trim()]));
      if (JSON.stringify(messages) !== JSON.stringify({ ready: dm.ready || '', restarted: dm.restarted || '', stopped: dm.stopped || '' })) patch.discord_messages = messages;
      if (hooksInfo.events.length) {
        const hooks = {};
        for (const [ev, input] of Object.entries(hookInputs)) { const scripts = list(input.value); if (scripts.length) hooks[ev] = scripts; }
        if (JSON.stringify(hooks) !== JSON.stringify(s.hooks || {})) patch.hooks = hooks;
      }
      if (!Object.keys(patch).length) { toast('Изменений нет', 'info'); return; }
      await api('PATCH', `/servers/${enc(s.id)}`, patch);
      toast('Сохранено'); await ctx.refresh(); TAB_VIEWS.settings(body, ctx);
    }) });

    const deleteButton = h('button', { class: 'danger', text: 'Удалить сервер', onclick: e => act(e.target, async () => {
      const files = h('input', { type: 'checkbox' });
      const backups = h('input', { type: 'checkbox' });
      const confirmId = h('input', { placeholder: 'Введите ID сервера: ' + s.id });
      const ok = await ask(`Удалить сервер «${s.name}» из менеджера? Сервер должен быть остановлен.`, { ok: 'Удалить', extra: [
        h('label', { class: 'row' }, files, 'удалить файлы игры и мир (' + s.install_dir + ')'),
        h('label', { class: 'row' }, backups, 'удалить бэкапы менеджера'),
        confirmId] });
      if (!ok) return;
      if (confirmId.value.trim() !== s.id) { toast('ID не совпал — удаление отменено', 'bad'); return; }
      await api('DELETE', `/servers/${enc(s.id)}?files=${files.checked}&backups=${backups.checked}`);
      toast('Сервер удалён'); location.hash = '#/';
    }) });

    const gameBox = h('div', { class: 'panel' }, h('p', { class: 'muted', text: 'Загрузка настроек игры…' }));
    replace(body,
      h('div', { class: 'panel' }, h('h2', { text: 'Менеджер' }),
        h('p', { class: 'muted small', text: 'Эти настройки меняются на работающем сервере.' }),
        h('div', { class: 'form' },
          field('Название в менеджере', inputs.name),
          field('Перезапускать при падении', inputs.restart_on_crash, 'До 3 раз за 30 минут, затем сервер остаётся выключенным и приходит уведомление.'),
          field('Проверка на зомби', h('div', { class: 'row' }, zombieOn, zombieMinutes, h('span', { class: 'muted', text: 'мин. без ответа' })),
            'Если процесс жив, но не отвечает на запросы Steam столько минут после загрузки — сервер останавливается (с проверкой базы) и запускается снова.'),
          field('Автообновление', inputs.auto_update, 'Новая сборка ставится сразу после выхода, с предупреждением игроков и бэкапом.'),
          field('Бэкап при остановке', inputs.backup_on_stop, 'Копия мира в бэкапы менеджера после каждой нормальной остановки.'))),
      h('div', { class: 'panel' }, h('h2', { text: 'Запуск' }),
        h('p', { class: 'muted small', text: 'Меняются только на остановленном сервере.' }),
        h('div', { class: 'form' },
          field('Карта', h('div', { class: 'row' }, mapSelect, mapPath)),
          field('Игровой порт', inputs.game_port, 'Игра занимает этот порт и следующий (Raw UDP).'),
          field('Query-порт', inputs.query_port),
          field('Multihome (IP)', inputs.multihome, 'Пусто — слушать все адреса.'),
          field('Ветка Steam', inputs.branch),
          field('Доп. аргументы запуска', inputs.launch_args, 'Порты, карта и multihome передаются автоматически.'),
          field('Интервал бэкапов игры, с', inputs.periodic_backup_interval_seconds, '0 — как в игре. Пишется в Game.ini перед стартом; каждый бэкап копирует базу целиком.'))),
      gameBox,
      h('div', { class: 'panel' }, h('h2', { text: 'Предупреждения игрокам (RCON)' }),
        h('p', { class: 'muted small', text: 'Перед плановым рестартом или остановкой, автообновлением и рестартом с галочкой «предупредить». Если на сервере никого нет — действие сразу, без ожидания. Нужен включённый RCON (раздел выше).' }),
        h('div', { class: 'form' }, field('Предупреждать игроков', warnOn)), warnBody),
      h('div', { class: 'panel' }, h('h2', { text: 'Тексты для Discord' }),
        h('p', { class: 'muted small' }, 'Добавляются к стандартному уведомлению этого сервера. Пусто — только стандартный заголовок. Куда слать — на странице ', h('a', { href: '#/notifications', text: 'Уведомления' }), '.'),
        h('div', { class: 'form' },
          field('Сервер готов', discord.ready), field('Сервер перезапущен', discord.restarted), field('Сервер остановлен', discord.stopped))),
      h('div', { class: 'panel' }, h('h2', { text: 'Скрипты (хуки)' }), hooksHelp(hooksInfo), datalist,
        hooksInfo.enabled ? h('div', { class: 'form' }, Object.entries(hookInputs).map(([ev, input]) => field(HOOK_LABELS[ev] || ev, input))) : null),
      h('div', { class: 'row end', style: 'margin-bottom:16px' }, save),
      h('h3', { text: 'Опасная зона' }),
      h('div', { class: 'panel' }, h('div', { class: 'row' },
        h('button', { text: 'Установить / проверить файлы', disabled: ctx.detail.lifecycle.state !== 'idle', onclick: e => act(e.target, async () => {
          if (!await ask('Запустить SteamCMD для этого сервера? Сервер должен быть остановлен.', { ok: 'Запустить', danger: false })) return;
          await runJob(await api('POST', `/servers/${enc(s.id)}/install`)); ctx.refresh();
        }) }),
        h('span', { class: 'spacer' }), deleteButton)));
    replace(gameBox, await gameSettings(ctx, running, () => TAB_VIEWS.settings(body, ctx)));
  },

  async history(body, ctx) {
    const eventsBox = h('div');
    const hooksBox = h('div');
    const load = async () => {
      try { replace(eventsBox, eventsTable(await api('GET', `/events?server=${enc(ctx.id)}&limit=100`), false)); } catch (e) { replace(eventsBox, h('p', { class: 'muted', text: e.message })); }
      try {
        const runs = await api('GET', `/servers/${enc(ctx.id)}/hooks/runs`);
        replace(hooksBox, table(['Время', 'Событие', 'Скрипт', 'Итог', 'Вывод'], runs.map(r => [
          fmtTime(r.started_at), r.event, h('span', { class: 'mono', text: r.script }),
          r.timed_out ? h('span', { class: 'badge bad', text: 'таймаут' }) : r.error ? h('span', { class: 'badge bad', title: r.error, text: 'ошибка' })
            : h('span', { class: 'badge ' + (r.exit_code === 0 ? 'ok' : 'bad'), text: 'код ' + r.exit_code }),
          r.output ? h('details', {}, h('summary', { class: 'small', text: r.output.split('\n')[0].slice(0, 60) || 'вывод' }), h('pre', { class: 'log', text: r.output })) : '']), 'Хуки ещё не запускались'));
      } catch (e) { replace(hooksBox, h('p', { class: 'muted', text: e.message })); }
    };
    replace(body, h('div', { class: 'panel' }, h('h2', { text: 'События' }), eventsBox), h('div', { class: 'panel' }, h('h2', { text: 'Запуски хуков' }), hooksBox));
    await load();
    every(15000, load);
  },
};

// ---------- notifications ----------

async function viewNotifications() {
  const listBox = h('div');
  const formBox = h('div');
  let servers = [];
  try { servers = await api('GET', '/servers'); } catch { servers = []; }
  const draw = async () => {
    let channels = [];
    try { channels = await api('GET', '/notifications'); } catch (e) { replace(listBox, h('div', { class: 'notice bad', text: e.message })); return; }
    replace(listBox, table(['Канал', 'Серверы', 'Уровень', 'Язык', 'Состояние', ''], channels.map(c => [
      h('span', {}, h('strong', { text: c.name }), h('div', { class: 'muted small mono', text: c.webhook })),
      c.server_id || 'все',
      (SEVERITY[c.min_severity] || [c.min_severity])[0] + (c.event_types && c.event_types.length ? ` + ${c.event_types.length} событий` : ''),
      c.language,
      h('span', {}, c.enabled ? h('span', { class: 'badge ok', text: 'включён' }) : h('span', { class: 'badge', text: 'выключен' }),
        c.last_error ? h('div', { class: 'small', style: 'color:var(--bad)', text: c.last_error }) : c.last_sent_at ? h('div', { class: 'muted small', text: 'отправлено ' + fmtAgo(c.last_sent_at) }) : null),
      h('div', { class: 'row' },
        h('button', { text: 'Тест', onclick: e => act(e.target, async () => { await api('POST', `/notifications/${enc(c.id)}/test`); toast('Тестовое сообщение отправлено'); draw(); }) }),
        h('button', { text: 'Изменить', onclick: () => showForm(c) }),
        h('button', { class: 'danger', text: 'Удалить', onclick: e => act(e.target, async () => {
          if (!await ask(`Удалить канал «${c.name}»?`, { ok: 'Удалить' })) return;
          await api('DELETE', `/notifications/${enc(c.id)}`); toast('Удалено'); draw();
        }) })),
    ]), 'Каналов нет'));
  };
  const showForm = channel => {
    const c = channel || { enabled: true, language: 'ru', min_severity: 'warning', event_types: [] };
    const name = h('input', { value: c.name || '', placeholder: 'Админы' });
    const url = h('input', { placeholder: channel ? 'оставьте пустым, чтобы не менять' : 'https://discord.com/api/webhooks/…', autocomplete: 'off' });
    const server = h('select', {}, h('option', { value: '', text: 'Все серверы' }), servers.map(s => h('option', { value: s.id, text: s.name, selected: c.server_id === s.id })));
    const severity = h('select', {}, [['info', 'Всё (инфо и выше)'], ['warning', 'Предупреждения и ошибки'], ['error', 'Только ошибки']].map(([v, t]) => h('option', { value: v, text: t, selected: c.min_severity === v })));
    const types = h('input', { class: 'mono', value: (c.event_types || []).join(', '), placeholder: 'server.ready, backup.finished' });
    const language = h('select', {}, [['ru', 'Русский'], ['en', 'English']].map(([v, t]) => h('option', { value: v, text: t, selected: c.language === v })));
    const mention = h('input', { value: c.mention || '', placeholder: '<@&ID роли> — только для ошибок' });
    const enabled = h('input', { type: 'checkbox', checked: c.enabled });
    replace(formBox, h('div', { class: 'panel' }, h('h2', { text: channel ? 'Изменить канал' : 'Новый канал Discord' }),
      h('div', { class: 'form' },
        field('Название', name), field('Webhook URL', url, 'Хранится зашифрованным и в ответах маскируется.'),
        field('Сервер', server), field('Уровень', severity),
        field('Дополнительные события', types, 'Эти типы приходят независимо от уровня. Через запятую.'),
        field('Язык', language), field('Упоминание', mention), field('Включён', enabled)),
      h('div', { class: 'row end' },
        h('button', { class: 'ghost', text: 'Отмена', onclick: () => replace(formBox) }),
        h('button', { class: 'primary', text: 'Сохранить', onclick: e => act(e.target, async () => {
          const body = { name: name.value.trim(), server_id: server.value, min_severity: severity.value,
            event_types: types.value.split(',').map(x => x.trim()).filter(Boolean), language: language.value, mention: mention.value.trim(), enabled: enabled.checked };
          if (url.value.trim()) body.webhook_url = url.value.trim();
          if (channel) await api('PATCH', `/notifications/${enc(channel.id)}`, body);
          else await api('POST', '/notifications', body);
          toast('Сохранено'); replace(formBox); draw();
        }) }))));
    name.focus();
  };
  replace(app,
    h('div', { class: 'head' }, h('h1', { text: 'Уведомления' }),
      h('button', { class: 'primary', text: '+ Добавить канал', onclick: () => showForm(null) })),
    formBox, h('div', { class: 'panel' }, listBox),
    h('div', { class: 'panel' }, h('h2', { text: 'Как настроить' }), h('ol', { class: 'small' },
      h('li', {}, 'В Discord: настройки канала → Интеграции → Вебхуки → Новый вебхук → «Копировать URL».'),
      h('li', {}, 'Здесь: «Добавить канал», вставьте URL. Выберите сервер (или все) и уровень: «Предупреждения и ошибки» — падения, зависания, ошибки бэкапов и обновлений, повреждённая база; «Всё» — ещё запуски, остановки, готовность, бэкапы.'),
      h('li', {}, 'Нужны отдельные события при уровне «только ошибки» — впишите их в «Дополнительные события», например server.ready, server.restarted.'),
      h('li', {}, 'Упоминание (<@&ID роли>, @here) ставится только к ошибкам, чтобы не будить людей из-за обычных рестартов.'),
      h('li', {}, 'Нажмите «Тест». Ошибка отправки показывается в списке каналов. Если хост не видит Discord (блокировка), укажите прокси в CONAN_MANAGER_NOTIFY_PROXY.'),
      h('li', {}, 'Свои тексты «Сервер готов / перезапущен / остановлен» задаются у каждого сервера во вкладке «Настройки». Предупреждения игрокам в игре — там же, раздел RCON.'))));
  await draw();
}

// ---------- routing ----------

async function render() {
  leaving.forEach(fn => fn());
  leaving = [];
  const parts = location.hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
  const section = parts[0] || '';
  const nav = section === 'events' ? 'events' : section === 'notifications' ? 'notifications' : 'servers';
  document.querySelectorAll('[data-nav]').forEach(a => a.classList.toggle('active', a.dataset.nav === nav));
  try {
    if (section === '') await viewServers();
    else if (section === 'new') await viewNewServer();
    else if (section === 'server' && parts[1]) await viewServer(parts[1], parts[2]);
    else if (section === 'events') await viewEvents();
    else if (section === 'notifications') await viewNotifications();
    else replace(app, h('p', {}, 'Страница не найдена. ', h('a', { href: '#/', text: 'К серверам' })));
  } catch (e) {
    replace(app, h('div', { class: 'notice bad', text: 'Ошибка: ' + e.message }));
  }
}

window.addEventListener('hashchange', render);
render();
