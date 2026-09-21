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
  const warnings = detail.server.warnings && detail.server.warnings.minutes && detail.server.warnings.minutes.length;
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
      const label = h('label', { class: 'row' }, warn, warnings ? 'Предупредить игроков (' + detail.server.warnings.minutes.join(', ') + ' мин.)' : 'Предупреждения не настроены');
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
    h('div', { class: 'head' }, h('h1', { text: 'Серверы' }), h('span', { class: 'spacer' }),
      h('button', { class: 'primary', text: 'Добавить сервер', onclick: () => { location.hash = '#/new'; } })),
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
  const name = h('input', { placeholder: 'Мой сервер Conan' });
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

// ---------- server page ----------

const TABS = [
  ['overview', 'Обзор'], ['console', 'Консоль'], ['logs', 'Логи'], ['config', 'Конфиги'],
  ['backups', 'Бэкапы'], ['schedule', 'Расписание'], ['settings', 'Настройки'], ['history', 'История'],
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
    let file = 'ServerSettings', mode = 'fields';
    const box = h('div');
    const draw = async () => {
      const running = ['running', 'starting', 'stopping'].includes(ctx.detail.lifecycle.state);
      let view;
      try { view = await api('GET', `/servers/${enc(ctx.id)}/config/${file}`); }
      catch (e) { replace(box, h('div', { class: 'notice bad', text: e.message })); return; }
      const switcher = h('div', { class: 'row', style: 'margin-bottom:12px' },
        ['ServerSettings', 'Game', 'Engine'].map(f => h('button', { class: f === file ? 'primary' : '', text: f + '.ini', onclick: () => { file = f; draw(); } })),
        h('span', { class: 'spacer', style: 'flex:1' }),
        h('button', { class: mode === 'fields' ? 'primary' : '', text: 'Поля', onclick: () => { mode = 'fields'; draw(); } }),
        h('button', { class: mode === 'raw' ? 'primary' : '', text: 'Файл целиком', onclick: () => { mode = 'raw'; draw(); } }));
      const notices = [
        running ? h('div', { class: 'notice', text: 'Сервер работает: править можно только после остановки. Игра читает файлы при старте и переписывает при остановке — правка на ходу потерялась бы.' }) : null,
        !view.exists ? h('div', { class: 'notice', text: 'Файла ещё нет: игра создаст его при первом запуске. Можно добавить ключи заранее.' }) : null,
        view.managed && view.managed.length ? h('div', { class: 'notice', text: 'Менеджер переписывает перед стартом: ' + view.managed.join(', ') + ' (настройка сервера).' }) : null,
      ];
      const editor = mode === 'fields' ? fieldsEditor(view, running) : rawEditor(view, running);
      replace(box, switcher, h('p', { class: 'muted small mono', text: view.path }), notices, editor, await versionsPanel(running));
    };
    const save = async body => {
      await api('PATCH', `/servers/${enc(ctx.id)}/config/${file}`, body);
      toast('Сохранено'); draw();
    };
    const fieldsEditor = (view, running) => {
      const changed = {};
      const sections = view.sections.map(section => {
        const counts = {};
        section.entries.forEach(e => { const k = e.key.toLowerCase(); counts[k] = (counts[k] || 0) + 1; });
        return h('div', { class: 'ini-section' }, h('h4', { text: '[' + section.name + ']' }),
          section.entries.map(entry => {
            const array = counts[entry.key.toLowerCase()] > 1;
            const secret = /password|secret|token/i.test(entry.key);
            const input = h('input', { value: entry.value, type: secret ? 'password' : 'text', autocomplete: 'off', disabled: running || array,
              title: array ? 'Массив Unreal: правится в режиме «Файл целиком»' : '' });
            input.oninput = () => {
              (changed[section.name] = changed[section.name] || {})[entry.key] = input.value;
              input.classList.toggle('changed', input.value !== entry.value);
            };
            if (secret) input.onfocus = () => { input.type = 'text'; };
            return h('div', { class: 'ini-row' }, h('label', { text: entry.key + (array ? ' []' : '') }), input);
          }));
      });
      const newSection = h('input', { placeholder: 'Секция, например RconPlugin', disabled: running });
      const newKey = h('input', { placeholder: 'Ключ', disabled: running });
      const newValue = h('input', { placeholder: 'Значение', disabled: running });
      return h('div', { class: 'panel' },
        sections.length ? sections : h('p', { class: 'muted', text: 'Файл пуст.' }),
        h('div', { class: 'row end' }, h('button', { class: 'primary', text: 'Сохранить изменения', disabled: running, onclick: e => act(e.target, async () => {
          if (!Object.keys(changed).length) { toast('Изменений нет', 'info'); return; }
          await save({ set: changed });
        }) })),
        h('h3', { text: 'Добавить ключ' }),
        h('div', { class: 'row' }, newSection, newKey, newValue, h('button', { text: 'Добавить', disabled: running, onclick: e => act(e.target, async () => {
          if (!newSection.value.trim() || !newKey.value.trim()) { toast('Нужны секция и ключ', 'bad'); return; }
          await save({ set: { [newSection.value.trim()]: { [newKey.value.trim()]: newValue.value } } });
        }) })));
    };
    const rawEditor = (view, running) => {
      const text = h('textarea', { class: 'mono', rows: 28, value: view.content, disabled: running, spellcheck: 'false' });
      return h('div', { class: 'panel' }, text, h('div', { class: 'row end', style: 'margin-top:8px' },
        h('button', { class: 'primary', text: 'Сохранить файл', disabled: running, onclick: e => act(e.target, async () => {
          await api('PUT', `/servers/${enc(ctx.id)}/config/${file}`, { content: text.value });
          toast('Сохранено'); draw();
        }) })));
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
    const draw = async () => {
      let current = null;
      try { current = await api('GET', `/servers/${enc(ctx.id)}/schedule`); } catch (e) { if (e.status !== 404) { toast(e.message, 'bad'); } }
      const enabled = h('input', { type: 'checkbox', checked: current ? current.enabled : true });
      const action = h('select', {}, [['restart', 'Рестарт'], ['stop', 'Остановка'], ['start', 'Запуск']].map(([v, t]) => h('option', { value: v, text: t, selected: current && current.action === v })));
      const mode = h('select', {}, [['daily', 'Каждый день в заданное время'], ['interval', 'С интервалом'], ['cron', 'Cron-выражение']].map(([v, t]) => h('option', { value: v, text: t, selected: current ? current.mode === v : v === 'daily' })));
      const minutes = h('input', { type: 'number', min: 1, value: current && current.interval_seconds ? Math.round(current.interval_seconds / 60) : 360 });
      const daily = h('input', { type: 'time', value: current && current.daily_time ? current.daily_time : '05:00' });
      const cron = h('input', { class: 'mono', placeholder: '0 5 * * *', value: current && current.cron_expression ? current.cron_expression : '0 5 * * *' });
      const zone = h('input', { value: current && current.timezone ? current.timezone : Intl.DateTimeFormat().resolvedOptions().timeZone });
      const rows = {
        interval: field('Интервал, мин', minutes),
        daily: field('Время (UTC)', daily, 'Время в UTC. Для местного времени используйте cron с часовым поясом.'),
        cron: [field('Выражение', cron, 'минута час день месяц день-недели (0 — воскресенье)'), field('Часовой пояс', zone)],
      };
      const variable = h('div', { class: 'form', style: 'grid-column:1/-1' });
      const showMode = () => replace(variable, rows[mode.value]);
      mode.onchange = showMode; showMode();
      const warnings = ctx.detail.server.warnings && ctx.detail.server.warnings.minutes && ctx.detail.server.warnings.minutes.length;
      replace(box, h('div', { class: 'panel' }, h('h2', { text: 'Расписание' }),
        current ? h('p', {}, 'Следующий запуск: ', h('strong', { text: fmtTime(current.next_run) }), h('span', { class: 'muted', text: ' (' + fmtAgo(current.next_run) + ')' }),
          current.failure_count ? h('span', { class: 'badge bad', style: 'margin-left:8px', text: 'ошибок подряд: ' + current.failure_count }) : null)
          : h('p', { class: 'muted', text: 'Расписание не задано.' }),
        h('div', { class: 'form' }, field('Включено', enabled), field('Действие', action), field('Режим', mode), variable),
        h('p', { class: 'muted small', text: warnings ? 'Перед остановкой и рестартом игроки будут предупреждены (вкладка «Настройки»).' : 'Предупреждения игрокам не настроены (вкладка «Настройки»).' }),
        h('div', { class: 'row end' },
          current ? h('button', { class: 'danger', text: 'Удалить расписание', onclick: e => act(e.target, async () => {
            if (!await ask('Удалить расписание?', { ok: 'Удалить' })) return;
            await api('DELETE', `/servers/${enc(ctx.id)}/schedule`); toast('Удалено'); draw();
          }) }) : null,
          h('button', { class: 'primary', text: 'Сохранить', onclick: e => act(e.target, async () => {
            const value = { action: action.value, enabled: enabled.checked, mode: mode.value };
            if (mode.value === 'interval') value.interval_seconds = Math.round(+minutes.value * 60);
            if (mode.value === 'daily') value.daily_time = daily.value;
            if (mode.value === 'cron') { value.cron_expression = cron.value.trim(); value.timezone = zone.value.trim(); }
            await api('PUT', `/servers/${enc(ctx.id)}/schedule`, value); toast('Сохранено'); draw();
          }) }))));
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
    let hooksInfo = { enabled: false, events: [], scripts: [] };
    try { hooksInfo = await api('GET', '/hooks'); } catch { /* shown as disabled */ }
    const s = ctx.detail.server;
    const w = s.warnings || {};
    const inputs = {
      name: h('input', { value: s.name }),
      branch: h('input', { value: s.branch }),
      launch_args: h('input', { class: 'mono', value: s.launch_args }),
      game_port: h('input', { type: 'number', value: s.game_port }),
      query_port: h('input', { type: 'number', value: s.query_port }),
      periodic_backup_interval_seconds: h('input', { type: 'number', min: 0, value: s.periodic_backup_interval_seconds }),
      restart_on_crash: h('input', { type: 'checkbox', checked: s.restart_on_crash }),
      auto_update: h('input', { type: 'checkbox', checked: s.auto_update }),
    };
    const warnMinutes = h('input', { placeholder: '15, 5, 1', value: (w.minutes || []).join(', ') });
    const warnCommand = h('input', { placeholder: 'broadcast', value: w.command || '' });
    const warnMessages = Object.fromEntries(['stop', 'restart', 'update'].map(a => [a, h('input', { value: (w.messages || {})[a] || '', placeholder: 'по умолчанию' })]));
    const datalist = h('datalist', { id: 'hook-scripts' }, hooksInfo.scripts.map(x => h('option', { value: x.name })));
    const hookInputs = Object.fromEntries(hooksInfo.events.map(ev => [ev, h('input', { class: 'mono', list: 'hook-scripts', value: ((s.hooks || {})[ev] || []).join(', '), placeholder: '—' })]));
    const list = text => text.split(',').map(x => x.trim()).filter(Boolean);

    const save = h('button', { class: 'primary', text: 'Сохранить', onclick: e => act(e.target, async () => {
      const patch = {};
      for (const [key, input] of Object.entries(inputs)) {
        const value = input.type === 'checkbox' ? input.checked : input.type === 'number' ? +input.value : input.value;
        if (value !== s[key]) patch[key] = value;
      }
      const warnings = { minutes: list(warnMinutes.value).map(Number), command: warnCommand.value.trim() || undefined, messages: {} };
      for (const [a, input] of Object.entries(warnMessages)) if (input.value.trim()) warnings.messages[a] = input.value.trim();
      if (!Object.keys(warnings.messages).length) delete warnings.messages;
      if (JSON.stringify(warnings) !== JSON.stringify({ minutes: w.minutes || [], command: w.command || undefined, messages: w.messages })) patch.warnings = warnings;
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

    replace(body,
      h('div', { class: 'panel' }, h('h2', { text: 'Сервер' }),
        h('p', { class: 'muted small', text: 'Название, перезапуск при падении, автообновление, предупреждения и хуки меняются на ходу. Остальное — только на остановленном сервере.' }),
        h('div', { class: 'form' },
          field('Название', inputs.name),
          field('Перезапускать при падении', inputs.restart_on_crash, 'До 3 раз за 30 минут, затем сервер остаётся выключенным.'),
          field('Автообновление', inputs.auto_update, 'Новая сборка ставится сразу, с предупреждением игроков.'),
          field('Ветка Steam', inputs.branch),
          field('Аргументы запуска', inputs.launch_args, 'Порты передаются автоматически.'),
          field('Игровой порт', inputs.game_port),
          field('Query-порт', inputs.query_port),
          field('Интервал бэкапов игры, с', inputs.periodic_backup_interval_seconds, '0 — как в игре. Пишется в Game.ini перед стартом.'))),
      h('div', { class: 'panel' }, h('h2', { text: 'Предупреждения игрокам' }),
        h('p', { class: 'muted small', text: 'Перед плановой остановкой, рестартом и автообновлением. Если на сервере никого нет, действие выполняется сразу.' }),
        h('div', { class: 'form' },
          field('За сколько минут', warnMinutes, 'Через запятую; пусто — не предупреждать.'),
          field('Текст перед остановкой', warnMessages.stop, '{minutes} заменяется числом минут.'),
          field('Текст перед рестартом', warnMessages.restart),
          field('Текст перед обновлением', warnMessages.update),
          field('RCON-команда', warnCommand, 'По умолчанию broadcast.'))),
      h('div', { class: 'panel' }, h('h2', { text: 'Хуки' }),
        hooksInfo.enabled ? h('p', { class: 'muted small' }, 'Скрипты из ', h('span', { class: 'mono', text: hooksInfo.dir }), ` (владелец root). Доступно: ${hooksInfo.scripts.map(x => x.name).join(', ') || 'нет'}. Несколько — через запятую.`)
          : h('p', { class: 'muted', text: 'Хуки отключены на этом менеджере.' }),
        datalist,
        hooksInfo.enabled ? h('div', { class: 'form' }, Object.entries(hookInputs).map(([ev, input]) => field(ev, input))) : null),
      h('div', { class: 'row end' }, save),
      h('h3', { text: 'Опасная зона' }),
      h('div', { class: 'panel' }, h('div', { class: 'row' },
        h('button', { text: 'Установить / проверить файлы', disabled: ctx.detail.lifecycle.state !== 'idle', onclick: e => act(e.target, async () => {
          if (!await ask('Запустить SteamCMD для этого сервера? Сервер должен быть остановлен.', { ok: 'Запустить', danger: false })) return;
          await runJob(await api('POST', `/servers/${enc(s.id)}/install`)); ctx.refresh();
        }) }),
        h('span', { style: 'flex:1' }), deleteButton)));
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
    h('div', { class: 'head' }, h('h1', { text: 'Уведомления' }), h('span', { class: 'spacer' }),
      h('button', { class: 'primary', text: 'Добавить канал', onclick: () => showForm(null) })),
    formBox, h('div', { class: 'panel' }, listBox));
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
