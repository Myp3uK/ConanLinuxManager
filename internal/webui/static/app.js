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
    // Titles use the page's tooltip rather than the browser's.
    else if (key === 'title') { if (value) el.setAttribute('data-tip', value); }
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

// csrf comes with the session and must accompany every change.
let csrf = '';
let me = null;

async function api(method, path, body) {
  const options = { method, headers: {} };
  if (body !== undefined) {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  if (method !== 'GET' && csrf) options.headers['X-CSRF-Token'] = csrf;
  // location.origin carries no user:password, so the request is valid even
  // when the page was opened through a URL that had them.
  const response = await fetch(location.origin + '/api/v1' + path, options);
  if (response.status === 204) return null;
  const text = await response.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!response.ok) {
    if (response.status === 401 && path !== '/login') showLogin(me ? 'Сессия закончилась - войдите снова.' : '');
    const error = new Error(response.status === 401 && path !== '/login' ? 'Нужно войти' : (data && data.error) || response.statusText || ('HTTP ' + response.status));
    error.status = response.status;
    error.data = data || {};
    throw error;
  }
  return data;
}

// ---------- login ----------

function locked() { return document.body.classList.contains('locked'); }

async function loadMe() {
  me = await api('GET', '/me');
  csrf = me.csrf || '';
  document.getElementById('who').textContent = me.username;
  document.getElementById('logout').hidden = !me.session;
}

function waitText(seconds) {
  const m = Math.ceil((seconds || 60) / 60);
  return m <= 1 ? 'минуту' : m + ' мин';
}

// showLogin covers the page with the login form; whatever the page was
// doing stops until someone logs in.
function showLogin(message) {
  if (locked()) return;
  leaving.forEach(fn => fn());
  leaving = [];
  csrf = ''; me = null;
  document.getElementById('confirm').close();
  document.body.classList.add('locked');
  const user = h('input', { name: 'username', autocomplete: 'username', required: true, spellcheck: 'false', autocapitalize: 'off' });
  const pass = h('input', { name: 'password', type: 'password', autocomplete: 'current-password', required: true });
  const eye = h('button', { type: 'button', class: 'ghost login-eye', text: 'Показать', tabindex: '-1', onclick: () => {
    pass.type = pass.type === 'password' ? 'text' : 'password';
    eye.textContent = pass.type === 'password' ? 'Показать' : 'Скрыть';
    pass.focus();
  } });
  const remember = h('input', { type: 'checkbox' });
  const error = h('div', { class: 'login-error', role: 'alert' });
  const submit = h('button', { class: 'primary login-submit', type: 'submit', text: 'Войти' });
  const form = h('form', { class: 'login-card', novalidate: true, onsubmit: async e => {
    e.preventDefault();
    if (!user.value.trim() || !pass.value) { error.textContent = 'Введите логин и пароль'; (user.value.trim() ? pass : user).focus(); return; }
    submit.disabled = true; submit.textContent = 'Вход…'; error.textContent = '';
    try {
      const out = await api('POST', '/login', { username: user.value.trim(), password: pass.value, remember: remember.checked });
      csrf = out.csrf;
      box.remove();
      document.body.classList.remove('locked');
      await loadMe();
      render();
    } catch (err) {
      pass.value = '';
      pass.focus();
      error.textContent = err.status === 401 ? 'Неверный логин или пароль.'
        : err.status === 429 ? 'Слишком много неудачных попыток с этого адреса. Повторите через ' + waitText(err.data.retry_after) + '.'
        : err.status === 403 ? 'Вход с этого адреса запрещён.'
        : err.status === 503 ? 'Администратор не настроен: задайте его в /etc/conan-manager/conan-manager.env.'
        : 'Не удалось войти: ' + err.message;
      form.classList.remove('shake'); void form.offsetWidth; form.classList.add('shake');
    } finally { submit.disabled = false; submit.textContent = 'Войти'; }
  } },
    h('div', { class: 'login-brand' }, h('div', { class: 'login-logo', text: 'CM' }),
      h('div', {}, h('div', { class: 'login-title', text: 'Conan Manager' }), h('div', { class: 'muted small', text: 'Управление серверами Conan Exiles' }))),
    message ? h('div', { class: 'notice small', text: message }) : null,
    h('label', { class: 'login-field' }, h('span', { text: 'Логин' }), user),
    h('label', { class: 'login-field' }, h('span', { text: 'Пароль' }), h('div', { class: 'login-pass' }, pass, eye)),
    h('label', { class: 'login-remember' }, remember, h('span', { text: 'Запомнить на этом устройстве' })),
    error, submit);
  const box = h('div', { id: 'login' }, form,
    h('p', { class: 'muted small login-foot', text: remember.checked ? '' : 'Без «Запомнить» вход действует до закрытия браузера.' }));
  document.body.append(box);
  setTimeout(() => user.focus(), 0);
}

document.getElementById('logout').onclick = e => act(e.target, async () => {
  await api('POST', '/logout');
  showLogin('Вы вышли из панели.');
});
const enc = encodeURIComponent;

function toast(message, kind = 'ok', ms = 5000) {
  const el = h('div', { class: 'toast ' + kind, text: message, onclick: () => leave() });
  const leave = () => {
    if (el.classList.contains('leaving')) return;
    el.classList.add('leaving');
    el.addEventListener('animationend', () => el.remove(), { once: true });
    setTimeout(() => el.remove(), 600);
  };
  document.getElementById('toasts').append(el);
  setTimeout(leave, kind === 'bad' ? Math.max(ms, 9000) : ms);
}

// fillHeight makes el reach the bottom of the window, less reserve pixels,
// and keeps it so when the window is resized.
function fillHeight(el, reserve = 24, min = 240) {
  const fit = () => {
    if (!el.isConnected) return;
    const top = el.getBoundingClientRect().top + window.scrollY;
    let height = Math.max(min, window.innerHeight - top - reserve);
    el.style.height = height + 'px';
    // What still overflows is padding below the element; take it back.
    const excess = document.documentElement.scrollHeight - window.innerHeight;
    if (excess > 0 && height - excess >= min) el.style.height = (height - excess) + 'px';
  };
  requestAnimationFrame(fit);
  window.addEventListener('resize', fit);
  onLeave(() => window.removeEventListener('resize', fit));
  return fit;
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
function ask(text, { ok = 'Да', danger = true, extra = [], cancel = true } = {}) {
  const dialog = document.getElementById('confirm');
  document.getElementById('confirm-text').textContent = text;
  replace(document.getElementById('confirm-extra'), extra);
  const okButton = document.getElementById('confirm-ok');
  const cancelButton = document.getElementById('confirm-cancel');
  okButton.textContent = ok;
  okButton.className = danger ? 'danger' : 'primary';
  cancelButton.style.display = cancel ? '' : 'none';
  return new Promise(resolve => {
    const done = value => {
      okButton.onclick = cancelButton.onclick = dialog.oncancel = dialog.onkeydown = null;
      dialog.close();
      resolve(value);
    };
    okButton.onclick = () => done(true);
    cancelButton.onclick = () => done(false);
    dialog.oncancel = () => done(false);
    // Enter confirms, wherever the focus is, except in a text area; Escape
    // cancels, as a dialog does anyway.
    dialog.onkeydown = e => {
      if (e.key !== 'Enter' || e.isComposing || e.target.tagName === 'TEXTAREA') return;
      e.preventDefault();
      done(e.target === cancelButton ? false : true);
    };
    dialog.showModal();
    okButton.focus();
  });
}

const JOB_NAMES = { stop: 'Остановка', start: 'Запуск', install: 'Установка', restart: 'Рестарт', update: 'Обновление', backup: 'Бэкап', restore: 'Восстановление', mods: 'Моды' };
// What a job is doing, said while it starts.
const JOB_BEGUN = { stop: 'Останавливается', start: 'Запускается', install: 'Устанавливается', restart: 'Перезапускается',
  update: 'Обновляется', backup: 'Создаётся бэкап', restore: 'Восстанавливается', mods: 'Моды качаются' };

// runJob follows a background job to its end and reports how it went.
async function runJob(job) {
  const name = JOB_NAMES[job.kind] || job.kind;
  toast(JOB_BEGUN[job.kind] || name + ': начато', 'info', 3000);
  while (job.status === 'running' || job.status === 'queued') {
    await new Promise(r => setTimeout(r, 2000));
    try { job = await api('GET', '/jobs/' + enc(job.id)); } catch (e) { toast(name + ': ' + e.message, 'bad'); return job; }
  }
  if (job.status === 'failed') toast(name + ': ошибка - ' + (job.error || 'без подробностей'), 'bad');
  // A started process still loads its world; «Работает» comes after that.
  else if (job.kind === 'start') toast('Процесс запущен, сервер загружает мир', 'info');
  else toast(name + ': готово' + (job.error ? ' (' + job.error + ')' : ''));
  return job;
}

function isSet(t) { return t && !t.startsWith('0001-'); }
function fmtTime(t) { return isSet(t) ? new Date(t).toLocaleString('ru-RU') : '-'; }
function fmtAgo(t) {
  if (!isSet(t)) return '-';
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
  idle: ['Остановлен', ''], starting: ['Запускается', 'warn'], loading: ['Запускается', 'warn'], running: ['Работает', 'ok'],
  stopping: ['Останавливается', 'warn'], flushing: ['Сохраняет', 'warn'], installing: ['Устанавливается', 'warn'],
  configuring: ['Настраивается', 'warn'], verifying: ['Проверка базы', 'warn'], restoring: ['Восстановление', 'warn'],
  deleting: ['Удаляется', 'warn'], recovery_required: ['Нужно восстановление', 'bad'],
};
// shownState is the state as players see it: a running server is still
// starting until the game reports it has loaded.
function shownState(d) {
  const state = d.lifecycle.state;
  return state === 'running' && d.health && d.health.ready === false ? 'loading' : state;
}
function stateBadge(state) {
  const [label, kind] = STATES[state] || [state || '-', ''];
  return h('span', { class: 'badge ' + kind, text: label });
}
const AVAILABILITY = {
  responsive: ['Отвечает', 'ok'], starting: ['Запускается', 'warn'], suspect: ['Задержки', 'warn'],
  unavailable: ['Не отвечает', 'bad'], inactive: ['-', ''],
};
function availabilityBadge(a) {
  if (!a || !a.state) return h('span', { class: 'muted', text: '-' });
  // Before the first query the state is only a guess.
  if (!isSet(a.checked_at)) return h('span', { class: 'badge', text: 'проверка…' });
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
  return h('dl', { class: 'facts' }, pairs.filter(Boolean).map(([k, v]) => [h('dt', {}, k), h('dd', {}, v)]));
}
function table(headers, rows, empty = 'Пусто', { onRow, cls } = {}) {
  if (!rows.length) return h('p', { class: 'muted', text: empty });
  return h('div', { class: 'scroll' }, h('table', { class: cls || '' },
    h('thead', {}, h('tr', {}, headers.map(x => h('th', { text: x })))),
    h('tbody', {}, rows.map((r, i) => h('tr', onRow ? { class: 'clickable', onclick: e => { if (!e.target.closest('a, button')) onRow(i); } } : {}, r.map(c => h('td', {}, c)))))));
}
// rawPort is a locked field that follows the game port: the game always
// takes the next port for Raw UDP.
function rawPort(gameInput) {
  const raw = h('input', { type: 'number', disabled: true });
  const show = () => { const n = parseInt(gameInput.value, 10); raw.value = n > 0 ? n + 1 : ''; };
  gameInput.addEventListener('input', show);
  show();
  return raw;
}

function field(label, input, hint) {
  return [h('label', {}, label, hint ? helpIcon(hint) : null), input];
}

// ---------- tooltips ----------

// One bubble for the whole page, positioned from the document rather than the
// element, so containers with overflow do not clip it (as on moorgame.ru).
const tooltip = h('div', { class: 'tooltip', role: 'tooltip' });
document.body.append(tooltip);
let tipOwner = null;

function helpIcon(text) {
  return h('span', { class: 'help', tabindex: '0', 'data-tip': text, 'aria-label': text, text: '?' });
}
function showTip(el) {
  const text = el.getAttribute('data-tip');
  if (!text) return;
  tipOwner = el;
  tooltip.textContent = text;
  tooltip.classList.add('visible');
  const r = el.getBoundingClientRect();
  const t = tooltip.getBoundingClientRect();
  const side = r.top - t.height - 10 < 0 ? 'bottom' : 'top';
  const center = r.left + r.width / 2;
  const left = Math.min(Math.max(8, center - t.width / 2), document.documentElement.clientWidth - t.width - 8);
  const top = side === 'top' ? r.top - t.height - 9 : r.bottom + 9;
  tooltip.dataset.side = side;
  tooltip.style.left = left + 'px';
  tooltip.style.top = top + 'px';
  tooltip.style.setProperty('--arrow-x', Math.min(Math.max(10, center - left), t.width - 10) + 'px');
}
function hideTip() { tipOwner = null; tooltip.classList.remove('visible'); }
// A page change removes the element a tooltip was for without a mouseout.
window.addEventListener('hashchange', hideTip);
document.addEventListener('mouseover', e => { const el = e.target.closest && e.target.closest('[data-tip]'); if (el && el !== tipOwner) showTip(el); else if (!el && tipOwner) hideTip(); });
document.addEventListener('focusin', e => { const el = e.target.closest && e.target.closest('[data-tip]'); if (el) showTip(el); });
document.addEventListener('focusout', hideTip);
document.addEventListener('scroll', hideTip, true);

// ---------- INI editor ----------

// highlightIni colours one INI text. Unreal values are often structs such as
// (Name="x",Count=2), so keys, strings and numbers inside them are coloured too.
function highlightIni(text) {
  const out = [];
  const span = (cls, t) => h('span', { class: cls, text: t });
  const value = v => {
    const trimmed = v.trim();
    if (/^-?\d+(\.\d+)?$/.test(trimmed)) return [span('ini-number', v)];
    if (/^(true|false)$/i.test(trimmed)) return [span('ini-bool', v)];
    const parts = [];
    const re = /("(?:[^"\\]|\\.)*"?)|([A-Za-z_][\w.]*)(?==)|(-?\b\d+(?:\.\d+)?\b)|\b(True|False|true|false)\b|([(),=])/g;
    let last = 0;
    for (let m; (m = re.exec(v));) {
      if (m.index > last) parts.push(v.slice(last, m.index));
      parts.push(span(m[1] ? 'ini-string' : m[2] ? 'ini-key' : m[3] ? 'ini-number' : m[4] ? 'ini-bool' : 'ini-punct', m[0]));
      last = re.lastIndex;
    }
    if (last < v.length) parts.push(v.slice(last));
    return parts;
  };
  for (const line of text.split('\n')) {
    let m;
    if (/^\s*[;#]/.test(line)) out.push(span('ini-comment', line));
    else if ((m = /^(\s*)(\[[^\]]*\])(.*)$/.exec(line))) out.push(m[1], span('ini-section', m[2]), m[3]);
    else if ((m = /^(\s*)([^=]+?)(\s*=)(.*)$/.exec(line))) out.push(m[1], span('ini-key', m[2]), span('ini-punct', m[3]), value(m[4]));
    else out.push(line);
    out.push('\n');
  }
  return out;
}

function iniEditor(textarea) {
  const pre = h('pre', { 'aria-hidden': 'true' });
  const paint = () => { replace(pre, highlightIni(textarea.value)); sync(); };
  const sync = () => { pre.scrollTop = textarea.scrollTop; pre.scrollLeft = textarea.scrollLeft; };
  textarea.setAttribute('wrap', 'off');
  textarea.addEventListener('input', paint);
  textarea.addEventListener('scroll', sync);
  // Tab indents instead of leaving the editor.
  textarea.addEventListener('keydown', e => {
    if (e.key !== 'Tab' || e.shiftKey || e.ctrlKey || e.altKey) return;
    e.preventDefault();
    textarea.setRangeText('\t', textarea.selectionStart, textarea.selectionEnd, 'end');
    paint();
  });
  paint();
  return h('div', { class: 'ini-editor' }, pre, textarea);
}

// ---------- server actions shared by the list and the server page ----------

function actionButtons(detail, refresh) {
  const id = detail.server.id;
  const state = detail.lifecycle.state;
  const running = state === 'running';
  const steps = (detail.server.warnings && detail.server.warnings.steps) || [];
  const start = h('button', { class: 'primary', text: 'Запустить', disabled: state !== 'idle',
    onclick: e => act(e.target, async () => {
      const r = await api('POST', `/servers/${enc(id)}/start`);
      if (r && r.kind === 'start') { refresh(); await runJob(r); } else toast('Запускается', 'info', 3000);
      refresh();
    }) });
  const stop = h('button', { text: 'Остановить', disabled: !running,
    onclick: e => act(e.target, async () => {
      if (!await ask(`Остановить «${detail.server.name}» сейчас? Игроки получат «Остановка по кнопке»; игра сохранит мир, а если не выйдет за 5 минут, процесс будет убит и база проверена.`, { ok: 'Остановить' })) return;
      const job = await api('POST', `/servers/${enc(id)}/stop`, { warn: false });
      refresh(); await runJob(job); refresh();
    }) });
  const restart = h('button', { text: 'Рестарт', disabled: !running,
    onclick: e => act(e.target, async () => {
      if (!await ask(`Перезапустить «${detail.server.name}» сейчас? Игроки получат «Рестарт по кнопке».`, { ok: 'Перезапустить' })) return;
      const job = await api('POST', `/servers/${enc(id)}/restart`, { warn: false });
      refresh(); await runJob(job); refresh();
    }) });
  const delay = (detail.server.warnings && detail.server.warnings.delay_minutes) || 15;
  const pending = detail.pending;
  const later = pending
    ? h('button', { class: 'primary', text: 'Отменить рестарт', title: (pending.reason === 'update' ? 'Рестарт ради обновления' : 'Отложенный рестарт') + ' в ' + fmtClock(pending.due_at),
      onclick: e => act(e.target, async () => {
        const why = pending.reason === 'update' ? ' Обновление тогда поставится при следующем рестарте.' : '';
        if (!await ask(`Отменить рестарт «${detail.server.name}» в ${fmtClock(pending.due_at)}? Игроки получат «Отмена отложенного рестарта».` + why, { ok: 'Отменить рестарт' })) return;
        await api('DELETE', `/servers/${enc(id)}/delayed-restart`); toast('Отложенный рестарт отменён'); refresh();
      }) })
    : h('button', { text: 'Отложенный рестарт', title: `Рестарт через ${delay} мин с отсчётом`, disabled: !running,
      onclick: e => act(e.target, async () => {
        const at = new Date(Date.now() + delay * 60000);
        const marks = steps.map(x => x.minutes).filter(m => m < delay).sort((a, b) => b - a);
        const plan = detail.planned;
        if (plan && plan.source === 'schedule' && new Date(plan.at) <= at) {
          toast(`Плановый ${plan.action === 'stop' ? 'стоп' : 'рестарт'} раньше - в ${fmtClock(plan.at)}`, 'bad'); return;
        }
        if (!await ask(`Перезапустить «${detail.server.name}» в ${fmtClock(at)}, через ${delay} мин? Сразу уйдёт «Отложенный рестарт» с ${delay} мин.` +
          (marks.length ? `, затем «Рестарт по расписанию» за ${marks.join(', ')} мин.` : '') + ' Рестарт - ровно в срок.', { ok: 'Поставить', danger: false })) return;
        await api('POST', `/servers/${enc(id)}/delayed-restart`); toast(`Рестарт в ${fmtClock(at)}`); refresh();
      }) });
  const kill = h('button', { class: 'danger', text: 'Убить', disabled: !(running || state === 'stopping'),
    onclick: e => act(e.target, async () => {
      if (!await ask(`Убить процесс «${detail.server.name}» без сохранения? Будет потерян прогресс с последнего сохранения; после выхода база проверяется.`, { ok: 'Убить' })) return;
      await api('POST', `/servers/${enc(id)}/kill`); toast('Процесс убит'); refresh();
    }) });
  return h('div', { class: 'row' }, start, stop, restart, later, kill);
}

// ---------- server statistics ----------

function fmtUptime(seconds) {
  if (!seconds) return '-';
  const d = Math.floor(seconds / 86400), h = Math.floor(seconds % 86400 / 3600), m = Math.floor(seconds % 3600 / 60);
  return d ? `${d} д ${h} ч` : h ? `${h} ч ${m} мин` : `${m} мин`;
}

// sparkline draws a small line of values; the page's policy allows SVG built
// through the DOM.
function sparkline(values, max) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 100 24');
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('class', 'spark');
  if (values.length < 2) return svg;
  const top = Math.max(max || 0, ...values) || 1;
  const points = values.map((v, i) => `${(i / (values.length - 1) * 100).toFixed(2)},${(23 - v / top * 22).toFixed(2)}`).join(' ');
  const line = document.createElementNS(ns, 'polyline');
  line.setAttribute('points', points);
  svg.append(line);
  return svg;
}

function fmtClock(at) {
  return new Date(at).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
}

function countdownText(at) {
  let s = Math.max(0, Math.round((new Date(at).getTime() - Date.now()) / 1000));
  const d = Math.floor(s / 86400); s %= 86400;
  const hh = Math.floor(s / 3600), mm = Math.floor(s % 3600 / 60), ss = s % 60;
  const pad = n => String(n).padStart(2, '0');
  return 'через ' + (d ? d + ' д ' : '') + `${pad(hh)}:${pad(mm)}:${pad(ss)}`;
}

function tile(label, value, sub, spark, hint) {
  return h('div', { class: 'tile' },
    h('div', { class: 'tile-label' }, label, hint ? helpIcon(hint) : null),
    h('div', { class: 'tile-value', text: value }),
    sub ? h('div', { class: 'tile-sub' }, sub) : null,
    spark || null);
}

function statsPanel(d) {
  const st = d.stats || {}, g = st.game, p = st.process, host = st.host || {}, hist = st.history || [];
  const running = d.lifecycle.state === 'running';
  const tiles = [];
  if (running && (g || p)) {
    const online = d.rcon && d.rcon.players_at && isSet(d.rcon.players_at) ? d.rcon.players.length : g ? g.players : null;
    tiles.push(tile('Онлайн', online === null ? '-' : String(online),
      d.rcon && d.rcon.players_at && isSet(d.rcon.players_at) ? 'RCON, ' + fmtAgo(d.rcon.players_at) : 'по отчёту игры',
      sparkline(hist.map(x => x.players))));
    if (g) tiles.push(tile('FPS сервера', g.fps_avg.toFixed(1), `мин ${g.fps_min.toFixed(1)} · макс ${g.fps_max.toFixed(1)}`,
      sparkline(hist.map(x => x.fps)), 'Средний, минимальный и максимальный FPS сервера за последнюю минуту, из отчёта игры. Ниже 15-20 игроки замечают лаги.'));
    if (p) tiles.push(tile('CPU', p.cpu_known ? p.cpu_percent.toFixed(0) + '%' : '-', p.cpu_known ? `${(p.cpu_percent / (host.cpus || 1)).toFixed(1)}% от всех ${host.cpus || '?'} ядер` : 'первый замер через 30 с',
      sparkline(hist.map(x => x.cpu)), '100% - одно ядро целиком. По данным Linux (/proc) за последние 30 секунд.'));
    if (p) tiles.push(tile('Память', fmtBytes(p.rss_bytes), g && g.process_peak_bytes ? 'пик ' + fmtBytes(g.process_peak_bytes) : `${p.threads} потоков`,
      sparkline(hist.map(x => x.rss)), 'Сколько оперативной памяти занимает процесс сервера (RSS). Пик - из отчёта игры.'));
    if (g) tiles.push(tile('Аптайм', fmtUptime(g.uptime_seconds), g.npcs ? `NPC в мире: ${g.npcs}` : null, null));
    const plan = d.planned;
    if (plan && isSet(plan.at)) {
      const at = new Date(plan.at);
      const today = at.toDateString() === new Date().toDateString();
      const label = plan.source === 'update' ? 'Рестарт: обновление' : plan.source === 'manual' ? 'Отложенный рестарт' : plan.action === 'stop' ? 'След. остановка' : 'След. рестарт';
      tiles.push(tile(label, (today ? '' : at.toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' }) + ' ') + fmtClock(at),
        h('span', { class: 'countdown', 'data-at': plan.at, text: countdownText(plan.at) }), null,
        plan.source === 'schedule' ? null : 'Отменить - кнопкой «Отменить рестарт» вверху.'));
    }
  }
  if (host.mem_total_bytes) {
    const used = host.mem_total_bytes - host.mem_available_bytes;
    tiles.push(tile('Хост', `${fmtBytes(used)} / ${fmtBytes(host.mem_total_bytes)}`, `нагрузка ${host.load1.toFixed(2)} на ${host.cpus} ядер`, null,
      'Память всей машины: занято / всего. Нагрузка - load average за минуту; больше числа ядер - процессору не хватает времени.'));
  }
  return h('div', { class: 'panel' },
    h('h2', { text: 'Ресурсы' }),
    tiles.length ? h('div', { class: 'tiles' }, tiles) : h('p', { class: 'muted', text: 'Нет данных.' }),
    running ? h('p', { class: 'muted small', style: 'margin:10px 0 0' },
      g ? `Отчёт игры от ${new Date(g.at).toLocaleTimeString('ru-RU')} (игра пишет его раз в минуту). ` : 'Отчёт игры появится через минуту после загрузки. ',
      'Графики - последний час.') : h('p', { class: 'muted small', style: 'margin:10px 0 0', text: 'Сервер не запущен: показаны только данные хоста.' }));
}

function rconBadge(r) {
  if (!r) return '-';
  if (r.connected) return h('span', { class: 'badge ok', text: 'подключён' });
  if (r.error) return h('span', { class: 'badge bad', title: r.error, text: 'ошибка' });
  return h('span', { class: 'badge', text: 'не подключён' });
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
  if (state !== 'running') return h('span', { class: 'muted', text: '-' });
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
          h('span', { class: 'spacer' }), stateBadge(shownState(d))),
        s.recovery_reason ? h('div', { class: 'notice bad small', text: 'Запуск заблокирован: ' + s.recovery_reason }) : null,
        d.lifecycle.error ? h('div', { class: 'notice bad small', text: d.lifecycle.error }) : null,
        facts([
          ['Доступность', availabilityBadge(d.health && d.health.availability)],
          ['Игроки', playersText(s.id, state)],
          ['Порты', `${s.game_port} (raw ${s.game_port + 1}) / ${s.query_port}`],
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
      withServer ? (e.server_id ? h('a', { href: `#/server/${enc(e.server_id)}`, text: e.server_name || e.server_id }) : '-') : null,
      h('span', {}, e.message, h('div', { class: 'muted small mono', text: e.type })),
      h('span', { class: 'small mono muted details-line', text: Object.entries(e.details || {}).map(([k, v]) => `${k}: ${v}`).join(' · ') }),
    ].filter(x => x !== null)), 'Событий нет', { onRow: i => showEvent(items[i]) });
}

function showEvent(e) {
  const rows = [['Время', fmtTime(e.time)], ['Уровень', severityBadge(e.severity)], ['Сервер', e.server_name || e.server_id || '-'], ['Тип', h('span', { class: 'mono', text: e.type })]]
    .concat(Object.entries(e.details || {}).map(([k, v]) => [k, v.includes('\n') || v.length > 80 ? h('pre', { class: 'log', text: v }) : h('span', { class: 'mono', text: v })]));
  ask(e.message, { ok: 'Закрыть', danger: false, cancel: false, extra: [facts(rows)] });
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

// serverSlug turns a name into a server ID: Russian letters transliterated,
// everything else not allowed in an ID turned into dashes.
const TRANSLIT = { а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'e', ж: 'zh', з: 'z', и: 'i', й: 'y', к: 'k', л: 'l',
  м: 'm', н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f', х: 'h', ц: 'ts', ч: 'ch', ш: 'sh', щ: 'sch',
  ъ: '', ы: 'y', ь: '', э: 'e', ю: 'yu', я: 'ya' };
function serverSlug(name) {
  return [...name.toLowerCase()].map(ch => TRANSLIT[ch] ?? ch).join('')
    .replace(/[^a-z0-9_-]+/g, '-').replace(/-{2,}/g, '-').replace(/^[-_]+|[-_]+$/g, '').slice(0, 64).replace(/[-_]+$/, '');
}

async function viewNewServer() {
  let existing = [];
  try { existing = await api('GET', '/servers'); } catch { existing = []; }
  // Suggest ports clear of every server: each uses game, game+1 and query.
  const used = new Set(existing.flatMap(s => [s.game_port, s.game_port + 1, s.query_port]));
  let game = 7777; while (used.has(game) || used.has(game + 1)) game += 10;
  let query = 27015; while (used.has(query)) query += 10;

  const id = h('input', { class: 'mono', placeholder: 'server-1', required: true, pattern: '[A-Za-z0-9][A-Za-z0-9_-]{0,63}', spellcheck: 'false', autocomplete: 'off' });
  const name = h('input', { placeholder: 'Мой сервер', autocomplete: 'off' });
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
  // The ID follows the name until the operator edits it.
  let idTouched = false;
  id.addEventListener('input', e => { if (e.isTrusted) idTouched = true; });
  const taken = new Set(existing.map(s => s.id.toLowerCase()));
  name.oninput = () => {
    if (idTouched) return;
    const base = serverSlug(name.value);
    let candidate = base, n = 2;
    while (base && taken.has(candidate)) candidate = `${base.slice(0, 60)}-${n++}`;
    id.value = candidate;
    id.oninput();
  };
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
    if (!installNow.checked) { location.hash = `#/server/${enc(created.id)}`; return; }
    // The install is watched in SteamCMD's log from the start.
    const job = await api('POST', `/servers/${enc(created.id)}/install`);
    openLog(created.id, 'steamcmd');
    runJob(job);
  });
  replace(app,
    h('div', { class: 'head' }, h('a', { href: '#/', text: '← Серверы' }), h('h1', { text: 'Новый сервер' })),
    h('div', { class: 'panel' }, h('div', { class: 'form' },
      field('Название', name, 'Как сервер называется в менеджере и в списке серверов игры. Можно по-русски.'),
      field('ID', id, 'Заполняется из названия: латиница в нижнем регистре, цифры, - и _. Входит в имя systemd-юнита и пути; изменить потом нельзя.'),
      field('Каталог установки', install, 'Может быть на отдельном диске.'),
      field('Каталог SteamCMD', steam, 'Общий для всех серверов.'),
      field('Ветка', branch),
      field('Игровой порт', gamePort),
      field('Raw UDP-порт', rawPort(gamePort), 'Игра всегда занимает следующий за игровым порт. Меняется вместе с игровым; открыть в файрволе нужно оба.'),
      field('Query-порт', queryPort),
      field('Аргументы запуска', args),
      field('Интервал бэкапов игры, с', backup, '0 - оставить как в игре. Каждый бэкап копирует базу целиком: чем реже, тем меньше износ SSD.'),
      field('Перезапускать при падении', restart),
      field('Сразу установить файлы', installNow, 'SteamCMD скачает сервер (~30 ГБ).'),
    ), h('div', { class: 'row end' }, create)));
  name.focus();
}

// ---------- server settings helpers ----------

const MAPS = [['/Game/Maps/ConanSandbox/ConanSandbox', 'Изгнанные земли (Exiled Lands)']];
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
      : h('span', { class: 'muted', text: 'пока нет' }), '. Несколько скриптов - через запятую, выполняются по порядку.'),
    h('details', {}, h('summary', { class: 'small', text: 'Как добавить свой скрипт' }),
      h('ol', { class: 'small' },
        h('li', {}, 'Положите файл в ', h('span', { class: 'mono', text: info.dir }), ' на сервере (по SSH).'),
        h('li', {}, 'Сделайте владельцем root и исполняемым: ', h('span', { class: 'mono', text: 'chown root:root скрипт; chmod 755 скрипт' }), '. Файл, который может менять кто-то кроме root, не запустится - иначе через него можно было бы выполнить что угодно с правами менеджера.'),
        h('li', {}, 'Обновите эту страницу - скрипт появится в списке, впишите его имя в нужное событие и сохраните.')),
      h('p', { class: 'small', text: 'Скрипту передаются переменные: CONAN_SERVER_ID, CONAN_SERVER_NAME, CONAN_INSTALL_DIR, CONAN_SAVED_DIR, CONAN_GAME_PORT, CONAN_QUERY_PORT, CONAN_RCON_PORT, CONAN_RCON_PASSWORD; для бэкапа ещё CONAN_BACKUP_PATH, для падения - CONAN_EXIT_CODE. «Перед запуском», «Перед обновлением» и «Перед бэкапом» могут отменить действие, завершившись с ненулевым кодом. Лимит - 5 минут.' }),
      h('pre', { class: 'log', text: '#!/bin/sh\n# /etc/conan-manager/hooks/upload-backup - копия бэкапа на другой сервер\nscp "$CONAN_BACKUP_PATH" backup@storage:/backups/"$CONAN_SERVER_ID"/\n\n#!/bin/sh\n# /etc/conan-manager/hooks/hello - сообщение в игру через RCON\nconan-manager rcon -p "$CONAN_RCON_PORT" -q "broadcast Сервер снова в строю!"' })));
}

// GAME_FIELDS are the settings the Dedicated Server Launcher shows on its main
// page. They live in the game's INI files, which stay the only place they are
// stored; the form reads and writes those keys.
const REGIONS = [['0', 'Европа'], ['1', 'Северная Америка'], ['2', 'Азия'], ['3', 'Австралия'], ['4', 'Южная Америка'], ['5', 'Япония']];
const GAME_FIELDS = [
  { file: 'Engine', section: 'OnlineSubsystem', key: 'ServerName', label: 'Название в списке серверов', def: '' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'ServerPassword', label: 'Пароль для входа', type: 'password', def: '' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'AdminPassword', label: 'Пароль администратора', type: 'password', def: '' },
  { file: 'Game', section: '/Script/Engine.GameSession', key: 'MaxPlayers', label: 'Максимум игроков', type: 'number', def: '40' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'serverRegion', label: 'Регион', options: REGIONS, def: '0' },
  { file: 'Engine', section: '/Script/OnlineSubsystemUtils.IpNetDriver', key: 'NetServerMaxTickRate', label: 'Максимальный тикрейт', type: 'number', def: '30' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'PVPEnabled', label: 'PvP', bool: true, def: 'False' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'IsBattlEyeEnabled', label: 'BattlEye', bool: true, def: 'False' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'IsVACEnabled', label: 'VAC', bool: true, def: 'False' },
  { file: 'ServerSettings', section: 'ServerSettings', key: 'ServerMessageOfTheDay', label: 'Сообщение дня', def: '' },
  { group: 'RCON' },
  { file: 'Game', section: 'RconPlugin', key: 'RconEnabled', label: 'Включить RCON', bool: true, def: 'False', hint: 'Нужен для консоли, списка игроков и предупреждений.' },
  { file: 'Game', section: 'RconPlugin', key: 'RconPort', label: 'Порт RCON', type: 'number', def: '25575' },
  { file: 'Game', section: 'RconPlugin', key: 'RconPassword', label: 'Пароль RCON', type: 'password', def: '' },
  { file: 'Game', section: 'RconPlugin', key: 'RconMaxKarma', label: 'Karma', type: 'number', def: '60', hint: 'Сколько неверных паролей подряд терпит сервер, прежде чем заблокировать адрес.' },
];

// cached answers requests whose answer does not change while the page is
// open, such as the hook scripts and the message kinds.
const apiCache = new Map();
function cached(path) {
  if (!apiCache.has(path)) apiCache.set(path, api('GET', path).catch(e => { apiCache.delete(path); throw e; }));
  return apiCache.get(path);
}

// loadGameViews reads the three INI files at once.
async function loadGameViews(ctx) {
  const files = ['ServerSettings', 'Game', 'Engine'];
  const list = await Promise.all(files.map(file => api('GET', `/servers/${enc(ctx.id)}/config/${file}`)));
  return Object.fromEntries(files.map((file, i) => [file, list[i]]));
}

function gameSettings(ctx, running, reload, views) {
  if (views instanceof Error) return h('div', { class: 'notice bad', text: 'Не удалось прочитать настройки игры: ' + views.message });
  // Unreal matches sections and keys without regard to case.
  const current = f => {
    const section = (views[f.file].sections || []).find(s => s.name.toLowerCase() === f.section.toLowerCase());
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
    else if (f.options) input = h('select', {}, f.options.map(([v, t]) => h('option', { value: v, text: `${v} - ${t}`, selected: v === value })));
    else input = h('input', { type: f.type === 'password' ? 'text' : (f.type || 'text'), class: f.type === 'password' ? 'secret' : '', value, autocomplete: 'off', spellcheck: 'false' });
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
    running ? h('div', { class: 'notice', text: 'Сервер работает: Изменение настроек заблокирован, сначала выключите сервер.' })
      : h('p', { class: 'muted small', text: 'Основные настройки сервера' }),
    h('div', { class: 'form' }, rows),
    h('div', { class: 'row end', style: 'margin-top:8px' }, save));
}

// ---------- game log levels ----------

// Unreal's log levels, quietest first.
const LOG_LEVELS = [
  ['None', 'ничего - лог выключен'], ['Fatal', 'только фатальные ошибки'], ['Error', 'ошибки'], ['Warning', 'предупреждения'],
  ['Display', 'сообщения в лог и консоль'], ['Log', 'обычный лог, без консоли'], ['Verbose', 'подробно'], ['VeryVerbose', 'очень подробно'],
];

// gameLogLevels edits [Core.Log] of Engine.ini: one row per log category
// with its level, the recommended categories first, then the operator's own,
// and an empty row at the end for a new one.
function gameLogLevels(ctx, running, reload, views, recommended) {
  if (views instanceof Error) return h('div', { class: 'notice bad', text: 'Не удалось прочитать Engine.ini: ' + views.message });
  const section = (views.Engine.sections || []).find(s => s.name.toLowerCase() === 'core.log');
  const stored = new Map((section ? section.entries : []).map(e => [e.key.toLowerCase(), e]));
  const known = new Set(recommended.map(c => c.name.toLowerCase()));
  const rows = [];   // { name(), select, stored }
  const list = h('div', { class: 'log-levels' });

  const levelSelect = value => {
    const select = h('select', { disabled: running },
      h('option', { value: '', text: 'по умолчанию игры' }),
      LOG_LEVELS.map(([v, t]) => h('option', { value: v, text: `${v} - ${t}` })));
    // Unreal takes the level in any case; the list shows the canonical one.
    const match = LOG_LEVELS.find(([v]) => v.toLowerCase() === (value || '').toLowerCase());
    if (value && !match) select.append(h('option', { value, text: value }));
    select.value = match ? match[0] : (value || '');
    return select;
  };
  const removeButton = (row, line) => h('button', { class: 'ghost icon-button', text: '×', title: 'Убрать категорию', disabled: running, onclick: () => {
    rows.splice(rows.indexOf(row), 1);
    line.remove();
    if (!rows.some(r => r.blank)) addBlank();
  } });
  const addRow = (name, value, custom) => {
    const row = { name: () => name, select: levelSelect(value), stored: value };
    const line = h('div', { class: 'log-level' }, h('label', { class: 'mono', text: name, title: name }), row.select, h('span'));
    if (custom) line.lastChild.replaceWith(removeButton(row, line));
    rows.push(row);
    list.append(line);
  };
  // The empty row: typing a name into it turns it into a new category and
  // opens another empty row below.
  const addBlank = () => {
    const input = h('input', { class: 'mono', placeholder: 'Своя категория, например LogMyMod', disabled: running, autocomplete: 'off', spellcheck: 'false' });
    const row = { name: () => input.value.trim(), select: levelSelect(''), stored: null, blank: true };
    const line = h('div', { class: 'log-level' }, input, row.select, h('span'));
    input.oninput = () => {
      if (!row.blank || !input.value.trim()) return;
      row.blank = false;
      if (!row.select.value) row.select.value = 'Warning';
      line.lastChild.replaceWith(removeButton(row, line));
      addBlank();
    };
    rows.push(row);
    list.append(line);
  };
  for (const c of recommended) {
    const e = stored.get(c.name.toLowerCase());
    addRow(e ? e.key : c.name, e ? e.value : '', false);
  }
  for (const e of (section ? section.entries : [])) {
    if (!known.has(e.key.toLowerCase())) addRow(e.key, e.value, true);
  }
  addBlank();

  const recommend = h('button', { text: 'Как рекомендуется', disabled: running, onclick: () => {
    const levels = new Map(recommended.map(c => [c.name.toLowerCase(), c.level]));
    for (const r of rows) if (levels.has(r.name().toLowerCase())) r.select.value = levels.get(r.name().toLowerCase());
    toast('Выставлены рекомендуемые уровни - сохраните', 'info');
  } });
  const save = h('button', { class: 'primary', text: 'Сохранить уровни логов', disabled: running, onclick: e => act(e.target, async () => {
    const named = rows.filter(r => r.name());
    const seen = new Set();
    for (const r of named) {
      if (!/^[A-Za-z0-9_.]+$/.test(r.name())) throw new Error(`«${r.name()}»: название категории - латиница, цифры, _ и точка`);
      if (seen.has(r.name().toLowerCase())) throw new Error(`Категория ${r.name()} указана дважды`);
      seen.add(r.name().toLowerCase());
    }
    const set = {}, remove = [];
    for (const r of named) {
      if (r.select.value === (r.stored || '')) continue;
      if (r.select.value) set[r.name()] = r.select.value; else if (r.stored) remove.push(r.name());
    }
    // Custom categories removed from the list go from the file too.
    for (const [key, entry] of stored) if (!seen.has(key)) remove.push(entry.key);
    if (!Object.keys(set).length && !remove.length) { toast('Изменений нет', 'info'); return; }
    await api('PATCH', `/servers/${enc(ctx.id)}/config/Engine`, { set: Object.keys(set).length ? { 'Core.Log': set } : undefined,
      remove: remove.length ? { 'Core.Log': remove } : undefined });
    toast('Уровни логов сохранены'); reload();
  }) });

  return h('div', {},
    running ? h('div', { class: 'notice', text: 'Сервер работает: уровни меняются только на остановленном сервере - игра переписывает Engine.ini при остановке.' })
      : h('p', { class: 'muted small', text: 'Сколько игра пишет в ConanSandbox.log по каждой категории ([Core.Log] в Engine.ini). С уровнями игры по умолчанию лог вырастает до гигабайтов; рекомендуемые - как у Dedicated Server Launcher. Свою категорию впишите в пустую строку внизу. Вступают в силу при следующем запуске.' }),
    list,
    h('div', { class: 'row end', style: 'margin-top:12px' }, recommend, h('span', { class: 'spacer' }), save));
}

// ---------- server page ----------

const TABS = [
  ['overview', 'Обзор'], ['settings', 'Настройки'], ['console', 'RCON'], ['logs', 'Логи'],
  ['schedule', 'Рестарты'], ['backups', 'Бэкапы'], ['config', 'Конфиги'], ['mods', 'Моды'],
  ['history', 'История'],
];

// serverPage is the server page on screen. Switching tabs of the same server
// keeps its header and tab bar, so they do not jump while the tab loads.
let serverPage = null;

async function viewServer(id, tab) {
  tab = TABS.some(t => t[0] === tab) ? tab : 'overview';
  let page = serverPage && serverPage.id === id && app.contains(serverPage.body) ? serverPage : null;
  if (!page) {
    const headBox = h('div', { class: 'head server-head' });
    const tabs = h('nav', { class: 'tabs' }, TABS.map(([key, label]) => h('a', { href: `#/server/${enc(id)}/${key}`, 'data-tab': key, text: label })));
    const body = h('div');
    replace(app, headBox, tabs, body);
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
        h('span', { class: 'muted mono small', text: d.server.id }), stateBadge(shownState(d)),
        h('span', { class: 'spacer' }), actionButtons(d, ctx.refresh));
      if (ctx.onRefresh) ctx.onRefresh(d);
      return true;
    };
    page = serverPage = { id, tabs, body, ctx };
    if (!await ctx.refresh()) return;
  }
  page.tabs.querySelectorAll('a').forEach(a => a.classList.toggle('active', a.dataset.tab === tab));
  // The old tab's timers are gone with the route change; the header's are
  // started again here, and the body keeps its height until the tab draws.
  page.ctx.onRefresh = null;
  every(5000, page.ctx.refresh);
  // Each tab draws into its own box: a late answer for the tab just left
  // lands in a detached box instead of over the new tab.
  page.body.style.minHeight = page.body.offsetHeight + 'px';
  const box = h('div');
  replace(page.body, box);
  try { await TAB_VIEWS[tab](box, page.ctx); }
  finally {
    if (box.isConnected) {
      page.body.style.minHeight = '';
      // Heights fitted while the old tab's height held the page are wrong.
      window.dispatchEvent(new Event('resize'));
    }
  }
}

// logSource is the log the logs tab opens with, set by links to it.
let logSource = null;
const LOG_MISSING = {
  game: 'Лога игры ещё нет: игра начинает его при первом запуске.',
  process: 'Сервер ещё не запускался из менеджера.',
  steamcmd: 'SteamCMD для этого сервера ещё не запускался.',
};
function openLog(id, source) {
  logSource = source;
  location.hash = `#/server/${enc(id)}/logs`;
}

// installNotice tells how the game install stands: running, failed with a
// retry, or never done.
function installNotice(d, ctx) {
  const job = d.install;
  const id = d.server.id;
  const log = h('button', { text: 'Лог SteamCMD', onclick: () => openLog(id, 'steamcmd') });
  const install = text => h('button', { class: 'primary', text, onclick: e => act(e.target, async () => {
    const job = await api('POST', `/servers/${enc(id)}/install`);
    openLog(id, 'steamcmd');
    await runJob(job);
  }) });
  if (d.lifecycle.state === 'installing' || (job && ['running', 'queued'].includes(job.status))) {
    return h('div', { class: 'notice' }, h('strong', { text: 'Идёт установка. ' }),
      'SteamCMD качает игру, около 4 ГБ. Ход загрузки - в логе.', h('div', { class: 'row', style: 'margin-top:8px' }, log));
  }
  if (job && job.status === 'failed') {
    return h('div', { class: 'notice bad' }, h('strong', { text: 'Установка не удалась: ' }), job.error || 'без подробностей',
      h('div', { class: 'row', style: 'margin-top:8px' }, install('Повторить установку'), log));
  }
  if (!d.build && d.lifecycle.state === 'idle') {
    return h('div', { class: 'notice' }, h('strong', { text: 'Игра не установлена. ' }), 'Без неё сервер не запустится.',
      h('div', { class: 'row', style: 'margin-top:8px' }, install('Установить'), log));
  }
  return null;
}

const TAB_VIEWS = {
  async overview(body, ctx) {
    const info = h('div', { class: 'panel' });
    const recovery = h('div');
    const installBox = h('div');
    const resources = h('div');
    const update = h('div', { class: 'panel' });
    replace(body, installBox, recovery, resources, info, update);
    const draw = d => {
      const s = d.server, p = d.process || {}, hl = d.health || {}, a = hl.availability || {};
      replace(installBox, installNotice(d, ctx));
      replace(recovery, s.recovery_reason ? h('div', { class: 'notice bad' },
        h('strong', { text: 'Запуск заблокирован. ' }), s.recovery_reason,
        h('div', { class: 'row', style: 'margin-top:8px' },
          h('button', { text: 'Проверить базу ещё раз', onclick: e => act(e.target, async () => {
            await api('POST', `/servers/${enc(s.id)}/verify`); toast('База цела, блокировка снята'); ctx.refresh();
          }) }),
          h('a', { href: `#/server/${enc(s.id)}/backups`, text: 'Восстановить из бэкапа' }))) : null);
      replace(resources, statsPanel(d));
      replace(info, h('h2', { class: 'row' }, 'Состояние', stateBadge(shownState(d))), facts([
        d.lifecycle.error && ['Ошибка', d.lifecycle.error],
        ['Процесс', p.pid ? `PID ${p.pid}, запущен ${fmtTime(p.started_at)}` : '-'],
        [h('span', { class: 'row', style: 'gap:6px' }, 'Виден игрокам', helpIcon('Менеджер раз в 15 секунд опрашивает query-порт сервера - так же, как список серверов в игре.\n«Отвечает» - сервер загрузился и виден игрокам.\nСостояние в заголовке - это процесс: запущен он или нет. Процесс может работать, но не отвечать: пока мир грузится или если сервер завис. Долгое молчание работающего сервера - признак зомби.')), h('span', {}, availabilityBadge(a), a.checked_at && isSet(a.checked_at) ? h('span', { class: 'muted small', text: '  проверено ' + fmtAgo(a.checked_at) }) : null)],
        a.last_error && ['Ошибка query', a.last_error],
        d.lifecycle.state === 'running' && ['RCON', h('span', {}, rconBadge(d.rcon), d.rcon && d.rcon.error ? h('span', { class: 'muted small', text: '  ' + d.rcon.error }) : null)],
        hl.recent_crashes > 0 && ['Падения', `${hl.recent_crashes} за 30 мин` + (hl.last_exit ? `, последнее ${fmtTime(hl.last_exit.at)} (${hl.last_exit.signal ? 'сигнал ' + hl.last_exit.signal : 'код ' + hl.last_exit.code})` : '')],
        hl.restart_pending && ['Автоперезапуск', fmtAgo(hl.restart_at)],
        hl.reason && ['Watchdog', hl.reason],
        ['Порты', `игра ${s.game_port}, raw ${s.game_port + 1}, query ${s.query_port}`],
        ['Каталог', h('span', { class: 'mono', text: s.install_dir })],
        ['Ветка', s.branch],
        ['Перезапуск при падении', s.restart_on_crash ? 'да' : 'нет'],
        ['Автообновление', s.auto_update ? 'да' : 'нет'],
        ['Бэкап игры', s.periodic_backup_interval_seconds ? `каждые ${Math.round(s.periodic_backup_interval_seconds / 60)} мин` : 'как в игре'],
      ]));
    };
    ctx.onRefresh = draw;
    draw(ctx.detail);
    every(1000, () => document.querySelectorAll('.countdown[data-at]').forEach(el => { el.textContent = countdownText(el.dataset.at); }));

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
              `  установлена ${u.installed || '-'}, в Steam ${u.latest || '-'} (ветка ${u.branch})`));
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
    const out = h('pre', { class: 'log rcon-out' });
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
    const loadPlayers = async fresh => {
      if (ctx.detail.lifecycle.state !== 'running') { replace(playersBox, h('p', { class: 'muted', text: 'Сервер не запущен.' })); return; }
      try {
        const r = await api('GET', `/servers/${enc(ctx.id)}/players` + (fresh ? '?fresh=1' : ''));
        const columns = r.players.length ? Object.keys(r.players[0]) : [];
        const rc = ctx.detail.rcon || {};
        replace(playersBox, h('p', {}, `Онлайн: ${r.count} `, fresh ? '' : rc.players_at && isSet(rc.players_at) ? h('span', { class: 'muted small', text: `(список обновляется раз в минуту, последний раз ${fmtAgo(rc.players_at)})` }) : ''),
          table(columns, r.players.map(p => columns.map(c => p[c] || '')), 'Никого нет'));
      } catch (e) { replace(playersBox, h('div', { class: 'notice', text: e.message })); }
    };
    const layout = h('div', { class: 'rcon-layout' },
      h('div', { class: 'panel rcon-console' }, h('h2', { text: 'Консоль RCON' }), out,
        h('div', { class: 'row', style: 'margin-top:8px' }, input,
          h('button', { class: 'primary', text: 'Выполнить', onclick: () => { const c = input.value; input.value = ''; run(c); } }),
          h('button', { text: 'help', onclick: () => run('help') }),
          h('button', { text: 'listplayers', onclick: () => run('listplayers') })),
        h('div', { class: 'row', style: 'margin-top:8px' }, broadcast, h('button', { text: 'Объявить', onclick: say }))),
      h('div', { class: 'panel rcon-players' }, h('div', { class: 'head' }, h('h2', { text: 'Игроки' }), h('span', { class: 'spacer' }),
        h('button', { text: 'Обновить', onclick: e => act(e.target, () => loadPlayers(true)) })), playersBox));
    replace(body, layout);
    fillHeight(layout, 20, 360);
    loadPlayers();
    every(20000, loadPlayers);
  },

  async logs(body, ctx) {
    const name = h('select', {}, h('option', { value: 'game', text: 'Игра (ConanSandbox.log)' }), h('option', { value: 'process', text: 'Процесс (вывод)' }),
      h('option', { value: 'steamcmd', text: 'SteamCMD (установка и обновления)' }));
    name.value = logSource || 'game';
    logSource = null;
    const filter = h('input', { placeholder: 'Фильтр, например Join succeeded', style: 'flex:1' });
    const limit = h('select', {}, [200, 500, 1000, 5000].map(n => h('option', { value: n, text: n + ' строк' })));
    const auto = h('input', { type: 'checkbox', checked: true });
    const out = h('pre', { class: 'log', style: 'max-height:none' });
    // Following the end: the box is refitted after the tab draws, and a
    // scroll position taken before that would leave the last lines hidden.
    // Only the operator stops the following: a scroll event also fires when
    // the content is replaced, and would be taken for one.
    let follow = true;
    const measure = () => requestAnimationFrame(() => { follow = out.scrollHeight - out.scrollTop - out.clientHeight < 30; });
    for (const type of ['wheel', 'touchend', 'keyup', 'mouseup']) out.addEventListener(type, measure, { passive: true });
    const observer = new ResizeObserver(() => { if (follow) out.scrollTop = out.scrollHeight; });
    observer.observe(out);
    onLeave(() => observer.disconnect());
    const load = async () => {
      try {
        const q = `name=${enc(name.value)}&limit=${limit.value}` + (filter.value ? `&filter=${enc(filter.value)}` : '');
        const r = await api('GET', `/servers/${enc(ctx.id)}/logs?${q}`);
        const atBottom = follow || !out.childNodes.length;
        if (r.missing) { replace(out, h('span', { class: 'muted', text: LOG_MISSING[name.value] || 'Этого лога ещё нет.' })); return; }
        replace(out, r.lines.length ? r.lines.map(line => h('span', {
          class: /\b(Error|Fatal|Critical)\b|ERROR!|\bFailed\b/.test(line) ? 'err' : /\bWarning\b/.test(line) ? 'hit' : '',
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
    fillHeight(out, 20, 260);
    await load();
    // SteamCMD's progress moves every second; the game's log is read less often.
    let refreshTimer = null;
    const tick = async () => {
      if (auto.checked && document.visibilityState === 'visible') await load();
      refreshTimer = setTimeout(tick, name.value === 'steamcmd' ? 1000 : 5000);
    };
    refreshTimer = setTimeout(tick, name.value === 'steamcmd' ? 1000 : 5000);
    onLeave(() => clearTimeout(refreshTimer));
  },

  async config(body, ctx) {
    let file = 'ServerSettings';
    // files comes from the server: besides the game's own three, a server has
    // whatever INI files its mods and its operator put there.
    let files = [];
    const box = h('div');
    const draw = async () => {
      const running = ['running', 'starting', 'stopping'].includes(ctx.detail.lifecycle.state);
      try { files = await api('GET', `/servers/${enc(ctx.id)}/configs`); }
      catch { files = files.length ? files : [{ name: 'ServerSettings', file: 'ServerSettings.ini' }, { name: 'Game', file: 'Game.ini' }, { name: 'Engine', file: 'Engine.ini' }]; }
      if (file !== 'logs' && !files.some(f => f.name === file)) file = 'ServerSettings';
      const addFile = h('button', { class: 'ghost', text: '+ Свой файл', title: 'Добавить конфиг',
        disabled: running, onclick: e => act(e.target, async () => {
          const name = h('input', { style: 'width:100%' });
          if (!await ask('Имя файла',
            { ok: 'Создать', danger: false, extra: [name] })) return;
          let wanted = name.value.trim();
          if (!wanted) return;
          if (!/\.ini$/i.test(wanted)) wanted += '.ini';
          if (files.some(f => f.file === wanted)) { toast('Такой файл уже есть', 'bad'); file = wanted; draw(); return; }
          // An empty file is created at once, so its name is checked by the
          // server before anything is typed into it.
          await api('PUT', `/servers/${enc(ctx.id)}/config/${enc(wanted)}`, { content: '' });
          file = wanted;
          toast('Файл создан');
          draw();
        }) });
      const switches = extra => h('div', { class: 'row', style: 'margin-bottom:12px' },
        files.map(f => h('button', { class: f.name === file ? 'primary' : '', text: f.file,
          onclick: () => { file = f.name; draw(); } }))
          .concat(h('button', { class: file === 'logs' ? 'primary' : '', text: 'Уровни логов', onclick: () => { file = 'logs'; draw(); } }), addFile),
        extra);
      if (file === 'logs') {
        let engine, categories;
        try { [engine, categories] = await Promise.all([api('GET', `/servers/${enc(ctx.id)}/config/Engine`), cached('/game-log-categories')]); }
        catch (e) { replace(box, switches(), h('div', { class: 'notice bad', text: e.message })); return; }
        replace(box, switches(h('span', { class: 'muted small mono', style: 'margin-left:8px', text: engine.path })),
          h('div', { class: 'panel' }, gameLogLevels(ctx, running, draw, { Engine: engine }, categories)));
        return;
      }
      let view;
      try { view = await api('GET', `/servers/${enc(ctx.id)}/config/${enc(file)}`); }
      catch (e) { replace(box, h('div', { class: 'notice bad', text: e.message })); return; }
      const text = h('textarea', { value: view.content, disabled: running, spellcheck: 'false', autocapitalize: 'off', autocomplete: 'off' });
      const editor = iniEditor(text);
      replace(box,
        switches(h('span', { class: 'muted small mono', style: 'margin-left:8px', text: view.path })),
        await versionsPanel(running),
        running ? h('div', { class: 'notice', text: 'Сервер работает: править можно только после остановки. Игра читает файлы при старте и переписывает при остановке - правка на ходу потерялась бы.' }) : null,
        !view.exists ? h('div', { class: 'notice', text: 'Файла ещё нет: игра создаст его при первом запуске. Можно написать его заранее.' }) : null,
        view.managed && view.managed.length ? h('div', { class: 'notice', text: 'Менеджер переписывает перед стартом: ' + view.managed.join(', ') + '.' }) : null,
        editor, h('div', { class: 'row end', style: 'margin-top:10px' },
          // Only a file somebody added can be removed: the game writes its own
          // three itself, and a missing one is a server quietly on defaults.
          (files.find(f => f.name === file) || {}).custom
            ? h('button', { class: 'danger', text: 'Удалить файл', disabled: running, onclick: e => act(e.target, async () => {
              if (!await ask(`Удалить ${file}? Содержимое останется в истории версий, оттуда его можно прочитать.`, { ok: 'Удалить' })) return;
              await api('DELETE', `/servers/${enc(ctx.id)}/config/${enc(file)}`);
              file = 'ServerSettings';
              toast('Файл удалён'); draw();
            }) })
            : null,
          h('span', { class: 'spacer' }),
          h('button', { class: 'primary', text: 'Сохранить файл', disabled: running, onclick: e => act(e.target, async () => {
            await api('PUT', `/servers/${enc(ctx.id)}/config/${enc(file)}`, { content: text.value });
            toast('Сохранено'); draw();
          }) })));
      fillHeight(editor, 70, 280);
    };
    const versionsPanel = async running => {
      let versions = [];
      try { versions = await api('GET', `/servers/${enc(ctx.id)}/config/${enc(file)}/versions`); } catch { versions = []; }
      return h('details', { class: 'panel versions' }, h('summary', { text: `История версий (${versions.length})` }),
        table(['Сохранена', 'Причина', 'Размер', ''], versions.map(v => [fmtTime(v.created_at), v.reason, fmtBytes(v.size),
          h('button', { text: 'Вернуть', disabled: running, onclick: e => act(e.target, async () => {
            if (!await ask(`Вернуть ${file}.ini к версии от ${fmtTime(v.created_at)}? Текущее содержимое тоже сохранится как версия.`, { ok: 'Вернуть', danger: false })) return;
            await api('POST', `/servers/${enc(ctx.id)}/config/${enc(file)}/versions/${v.id}/restore`);
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
        h('span', { class: 'mono small sha', text: b.sha256 || '-' }),
        b.exists ? h('span', { class: 'mono small', title: b.path, text: b.path.split('/').pop() }) : h('span', { class: 'badge bad', text: 'файла нет' }),
        h('div', { class: 'row end nowrap-row' },
          h('button', { text: 'Восстановить', disabled: running || !b.exists, title: running ? 'Сначала остановите сервер' : '', onclick: e => act(e.target, async () => {
            if (!await ask(`Заменить мир бэкапом от ${fmtTime(b.created_at)}? Текущий мир будет сохранён отдельным бэкапом «pre-restore».`, { ok: 'Восстановить' })) return;
            await runJob(await api('POST', `/servers/${enc(ctx.id)}/backups/${b.id}/restore`));
            draw(); ctx.refresh();
          }) }),
          h('button', { class: 'danger', text: 'Удалить', onclick: e => act(e.target, async () => {
            if (!await ask(`Удалить бэкап от ${fmtTime(b.created_at)}? Файл будет удалён с диска.`, { ok: 'Удалить' })) return;
            await api('DELETE', `/servers/${enc(ctx.id)}/backups/${b.id}`); toast('Удалено'); draw();
          }) })),
      ]), 'Бэкапов менеджера ещё нет', { cls: 'middle' }));
    };
    let lastState = ctx.detail.lifecycle.state;
    ctx.onRefresh = d => { if (d.lifecycle.state !== lastState) { lastState = d.lifecycle.state; draw(); } };
    replace(body, h('div', { class: 'panel' },
      h('div', { class: 'head' }, h('h2', { text: 'Бэкапы менеджера' }), h('span', { class: 'spacer' }), reason,
        h('button', { class: 'primary', text: 'Сделать бэкап', onclick: e => act(e.target, async () => {
          await runJob(await api('POST', `/servers/${enc(ctx.id)}/backup`, { reason: reason.value.trim() || 'manual' }));
          draw();
        }) })),
      h('p', { class: 'muted small', text: 'Копия снимается с работающего сервера без остановки. Восстановление - только на остановленном. Бэкап также делается перед каждым обновлением.' }),
      box));
    await draw();
  },

  async mods(body, ctx) {
    const box = h('div');
    const notice = h('div');
    // local is the list being edited. It is written only when saved: putting
    // every move through the API would leave a version of modlist.txt for
    // each one.
    let local = [];
    // The running server has its mods loaded, so the list can only be edited
    // while it is stopped; «Проверить обновления» and the install job stay
    // available, because the job stops the server itself.
    const editable = () => ctx.detail.lifecycle.state === 'idle';
    const save = h('button', { class: 'primary', text: 'Сохранить', disabled: true });
    const dirty = () => { save.disabled = false; };
    const sameList = saved => saved.length === local.length &&
      saved.every((m, i) => m.workshop_id === local[i].workshop_id && m.enabled === local[i].enabled);

    const statusOf = m => {
      if (m.last_error) return h('span', { class: 'badge bad', title: m.last_error, text: 'ошибка' });
      if (!m.paks || !m.paks.length) return h('span', { class: 'badge', text: 'не установлен' });
      if (isSet(m.latest_updated) && isSet(m.installed_updated) &&
        new Date(m.latest_updated) > new Date(m.installed_updated)) {
        return h('span', { class: 'badge warn', text: 'есть обновление' });
      }
      return h('span', { class: 'badge ok', text: 'установлен' });
    };
    // from is the row being dragged; it lives outside draw() because a redraw
    // during a drag would otherwise lose it.
    let from = null;
    // dragRows makes the drawn rows draggable. The handlers go on after the
    // table is built rather than into the shared table helper, which has no
    // say over a row's attributes.
    const dragRows = () => {
      if (!editable()) return;
      const rows = Array.from(box.querySelectorAll('tbody tr'));
      rows.forEach((tr, i) => {
        tr.draggable = true;
        tr.addEventListener('dragstart', e => {
          from = i;
          e.dataTransfer.effectAllowed = 'move';
          // Firefox starts no drag at all without data on the transfer.
          e.dataTransfer.setData('text/plain', String(i));
          tr.classList.add('dragging');
        });
        tr.addEventListener('dragend', () => {
          from = null;
          rows.forEach(r => r.classList.remove('dragging', 'drop-here'));
        });
        tr.addEventListener('dragover', e => {
          if (from === null || from === i) return;
          e.preventDefault();
          e.dataTransfer.dropEffect = 'move';
          rows.forEach(r => r.classList.toggle('drop-here', r === tr));
        });
        tr.addEventListener('dragleave', () => tr.classList.remove('drop-here'));
        tr.addEventListener('drop', e => {
          e.preventDefault();
          if (from === null || from === i) return;
          const [moved] = local.splice(from, 1);
          local.splice(i, 0, moved);
          from = null;
          dirty(); draw();
        });
      });
    };

    const draw = () => {
      const can = editable();
      replace(box, table(['', 'Вкл.', 'Мод', 'Размер', 'Обновлён в Steam', 'Состояние', ''], local.map((m, i) => [
        h('span', { class: 'grip', text: '⠿', title: can ? 'Перетащите, чтобы изменить порядок' : '' }),
        h('input', { type: 'checkbox', checked: m.enabled, disabled: !can, onchange: e => { m.enabled = e.target.checked; dirty(); } }),
        h('div', {},
          h('a', { href: 'https://steamcommunity.com/sharedfiles/filedetails/?id=' + enc(m.workshop_id),
            target: '_blank', rel: 'noopener noreferrer', text: m.title || m.workshop_id }),
          h('div', { class: 'muted mono small', text: m.workshop_id + (m.paks && m.paks.length ? ' · ' + m.paks.join(', ') : '') })),
        m.size_bytes ? fmtBytes(m.size_bytes) : '-',
        isSet(m.latest_updated) ? fmtTime(m.latest_updated) : (isSet(m.installed_updated) ? fmtTime(m.installed_updated) : '-'),
        statusOf(m),
        h('div', { class: 'row end' }, h('button', { class: 'danger', text: 'Убрать', onclick: () => {
          local.splice(i, 1); dirty(); draw();
        } })),
      ]), 'Модов нет. Добавьте их по ID или по ссылке из Workshop.', { cls: 'middle' }));
      dragRows();
      idInput.disabled = collectionInput.disabled = addModButton.disabled = addCollection.disabled = !can;
      if (!can) save.disabled = true;
      replace(notice, can ? null : h('div', { class: 'notice' },
        h('strong', { text: 'Сервер работает. ' }),
        'Список модов меняется только на остановленном сервере: у работающего моды уже загружены. ' +
        '«Установить и обновить» доступно - это задание само предупредит игроков и остановит сервер.'));
    };

    const load = async () => {
      let status;
      try { status = await api('GET', `/servers/${enc(ctx.id)}/mods`); }
      catch (e) { replace(box, h('div', { class: 'notice bad', text: e.message })); return; }
      local = (status.mods || []).map(m => ({ ...m }));
      save.disabled = true;
      draw();
    };
    // put sends the list as it stands; the array's order is the load order.
    const put = async () => {
      const status = await api('PUT', `/servers/${enc(ctx.id)}/mods`,
        { mods: local.map(m => ({ workshop_id: m.workshop_id, enabled: !!m.enabled })) });
      local = (status.mods || []).map(m => ({ ...m }));
      save.disabled = true;
      draw();
      return status;
    };

    // parseModIds reads what an operator has at hand: one id, one link, or a
    // whole list of either separated by commas, spaces or new lines, which is
    // how mod lists are passed around. What it cannot read is returned so it
    // can be named rather than silently dropped.
    const parseModIds = raw => {
      const ids = [], bad = [];
      for (const piece of raw.split(/[\s,;]+/)) {
        const part = piece.trim();
        if (!part) continue;
        const found = /(?:[?&]id=|^|\/)([1-9][0-9]{5,19})(?:$|&|$)/.exec(part);
        if (found) ids.push(found[1]); else bad.push(part);
      }
      return { ids, bad };
    };
    const idInput = h('input', { placeholder: 'ID или ссылка; можно списком через запятую', style: 'min-width:280px;flex:1' });
    const addMod = async button => act(button, async () => {
      const raw = idInput.value.trim();
      if (!raw) return;
      const { ids, bad } = parseModIds(raw);
      if (!ids.length) { toast(`Не похоже на ID или ссылку: ${bad.join(', ')}`, 'bad'); return; }
      const before = local.length;
      let known = 0;
      for (const id of ids) {
        if (local.some(m => m.workshop_id === id)) { known++; continue; }
        local.push({ workshop_id: id, enabled: true, paks: [] });
      }
      const added = local.length - before;
      if (!added) { toast(known === 1 ? 'Этот мод уже в списке' : 'Все эти моды уже в списке', 'bad'); return; }
      idInput.value = '';
      try { await put(); } catch (e) { local.length = before; draw(); throw e; }
      const notes = [added === 1 ? 'Добавлен 1 мод' : `Добавлено модов: ${added}`];
      if (known) notes.push(`уже были: ${known}`);
      if (bad.length) notes.push(`не распознано: ${bad.join(', ')}`);
      toast(notes.join(', '), bad.length ? 'bad' : 'ok');
    });

    const collectionInput = h('input', { placeholder: 'ID или ссылка на коллекцию', style: 'min-width:240px;flex:1' });
    const addModButton = h('button', { text: 'Добавить моды', onclick: e => addMod(e.target) });
    const addCollection = h('button', { text: 'Добавить коллекцию', onclick: e => act(e.target, async () => {
      const raw = collectionInput.value.trim();
      if (!raw) return;
      const status = await api('POST', `/servers/${enc(ctx.id)}/mods/collection`, { collection: raw });
      local = (status.mods || []).map(m => ({ ...m }));
      collectionInput.value = '';
      save.disabled = true;
      draw();
      toast('Моды коллекции добавлены');
    }) });

    // Not act(): it re-enables the button it was given, and this one being
    // enabled is how the tab says the list has unsaved changes.
    save.onclick = async () => {
      save.disabled = true;
      try { await put(); toast('Список сохранён'); }
      catch (e) { toast(e.message, 'bad'); save.disabled = false; }
    };

    const check = h('button', { text: 'Проверить обновления', onclick: e => act(e.target, async () => {
      const status = await api('POST', `/servers/${enc(ctx.id)}/mods/check`);
      // A list edited meanwhile is not thrown away by a check.
      if (save.disabled || sameList(status.mods || [])) { local = (status.mods || []).map(m => ({ ...m })); save.disabled = true; draw(); }
      toast(status.error ? 'Steam не ответил: ' + status.error
        : status.pending ? `Нужно установить или обновить: ${status.pending}` : 'Все моды свежие',
        status.error ? 'bad' : 'ok');
    }) });

    const install = h('button', { class: 'primary', text: 'Установить и обновить', onclick: e => act(e.target, async () => {
      if (!save.disabled) { toast('Сначала сохраните список', 'bad'); return; }
      const running = ['running', 'starting', 'stopping'].includes(ctx.detail.lifecycle.state);
      if (running && !await ask('Моды нельзя подменить под работающим сервером. Менеджер скачает их, затем предупредит игроков, остановит сервер, поставит моды и запустит его снова. Продолжить?', { ok: 'Обновить моды' })) return;
      const job = await api('POST', `/servers/${enc(ctx.id)}/mods/install`, { warn: running });
      openLog(ctx.id, 'steamcmd');
      await runJob(job);
      ctx.refresh();
    }) });

    replace(body, h('div', { class: 'panel' },
      h('div', { class: 'head' }, h('h2', { text: 'Моды' }), h('span', { class: 'spacer' }), check, install, save),
      h('p', { class: 'muted small', text: 'Порядок важен: если два мода меняют одно и то же, выигрывает тот, кто ниже; перетащите строку за ручку слева и нажмите «Сохранить». Список пишется в modlist.txt и вступает в силу при следующем запуске; у игроков должен быть тот же набор и тот же порядок, иначе они не зайдут.' }),
      notice,
      h('div', { class: 'row', style: 'margin-bottom:10px' }, idInput, addModButton),
      h('div', { class: 'row', style: 'margin-bottom:12px' }, collectionInput, addCollection),
      box));
    idInput.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addMod(null); } });
    // A stop or a start elsewhere on the page opens or closes the editing.
    let lastState = ctx.detail.lifecycle.state;
    ctx.onRefresh = d => { if (d.lifecycle.state !== lastState) { lastState = d.lifecycle.state; draw(); } };
    await load();
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
        text: n === 1 ? '1 - раз в сутки' : `${n} - каждые ${24 / n} ч` })));
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
        h('p', { class: 'muted small', text: 'Для случаев, которые форма выше не покрывает: например, остановка на ночь и запуск утром (для этого нужны два расписания - сейчас на сервер одно). Формат: минута час день месяц день-недели; 0 - воскресенье. Пример: «30 4 * * 1» - по понедельникам в 04:30.' }),
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
            field('Первый рестарт в', first, 'Остальные - через равные промежутки от этого времени.'),
            field('Рестартов в сутки', perDay),
            field('Часовой пояс', zone),
            field('Минимальный аптайм (чч:мм)', minUptime, 'Если сервер запущен недавно (вручную или после падения), плановый рестарт пропускается. 00:00 - не проверять.')),
          preview,
          h('p', { class: 'muted small' }, warnings
            ? `Игроки получат предупреждения за ${s.warnings.steps.map(x => x.minutes).sort((a, b) => b - a).join(', ')} мин. до рестарта (если кто-то онлайн). Сам рестарт - ровно в указанное время, даже если на сервере никого нет. `
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
    // One round trip instead of one per request: over a slow link they add up.
    const [hooksInfo, kindsList, gameViews] = await Promise.all([
      cached('/hooks').catch(() => ({ enabled: false, events: [], scripts: [], dir: '' })),
      cached('/message-kinds').catch(() => []),
      loadGameViews(ctx).catch(e => e),
    ]);
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

    // ----- announcements: warning marks and the text of every message -----
    const kinds = kindsList;
    const w = s.warnings || {};
    const warnCommand = h('input', { value: w.command || 'broadcast', style: 'width:160px' });
    const delayInput = h('input', { type: 'number', min: 1, max: 120, value: w.delay_minutes || 15, style: 'width:90px' });
    const marksBox = h('div', { class: 'row' });
    const addMark = minutes => {
      const m = h('input', { type: 'number', min: 1, max: 120, value: minutes, style: 'width:76px' });
      const chip = h('span', { class: 'mark' }, m, h('span', { class: 'muted', text: 'мин' }), h('button', { class: 'ghost', text: '✕', title: 'Убрать', onclick: () => chip.remove() }));
      chip.read = () => +m.value;
      marksBox.insertBefore(chip, addButton);
    };
    const addButton = h('button', { text: '+', title: 'Добавить интервал', onclick: () => addMark(1) });
    marksBox.append(addButton);
    ((w.steps && w.steps.length) ? w.steps.map(x => x.minutes) : []).sort((a, b) => b - a).forEach(addMark);
    const readMarks = () => [...marksBox.querySelectorAll('.mark')].map(c => c.read()).filter(n => n > 0).sort((a, b) => b - a);
    const current = key => {
      const k = kinds.find(x => x.key === key);
      const m = (s.messages || {})[key];
      return m && m.text ? { text: m.text, game: !!m.game && k.game_allowed, discord: !!m.discord } : { ...k.default };
    };
    const msgInputs = {};
    const msgRows = kinds.map(k => {
      const c = current(k.key);
      const text = h('input', { value: c.text });
      const game = h('input', { type: 'checkbox', checked: c.game, disabled: !k.game_allowed });
      const discord = h('input', { type: 'checkbox', checked: c.discord });
      msgInputs[k.key] = { text, game, discord };
      return [h('div', { class: 'msg-title' }, h('span', { text: k.title }), helpIcon(k.help + '\nПеременные: ' + k.vars + '.\nЧасть в [квадратных скобках] пропадает, если переменной внутри нет значения.')),
        text,
        k.game_allowed ? h('label', { class: 'row msg-flag' }, game, 'в игру') : h('span', { class: 'muted small msg-flag', text: 'только Discord' }),
        h('label', { class: 'row msg-flag' }, discord, 'в Discord')];
    });
    const readMessages = () => Object.fromEntries(Object.entries(msgInputs).map(([key, i]) => [key, { text: i.text.value.trim(), game: i.game.checked, discord: i.discord.checked }]));

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
      const marks = readMarks();
      if (new Set(marks).size !== marks.length) throw new Error('Интервалы предупреждений не должны повторяться');
      const warnings = { steps: marks.map(minutes => ({ minutes, message: '' })), command: warnCommand.value.trim() || 'broadcast', delay_minutes: +delayInput.value || 15 };
      const before = { steps: ((w.steps || []).map(x => x.minutes)).sort((a, b) => b - a).map(minutes => ({ minutes, message: '' })), command: w.command || 'broadcast', delay_minutes: w.delay_minutes || 15 };
      if (JSON.stringify(warnings) !== JSON.stringify(before)) patch.warnings = warnings;
      if (kinds.length) {
        const messages = readMessages();
        const was = Object.fromEntries(kinds.map(k => [k.key, current(k.key)]));
        if (JSON.stringify(messages) !== JSON.stringify(was)) patch.messages = messages;
      }
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
        h('label', { class: 'check-row' }, files, h('span', {}, 'удалить файлы игры и мир',
          h('span', { class: 'muted small mono', text: s.install_dir }))),
        h('label', { class: 'check-row' }, backups, h('span', {}, 'удалить бэкапы менеджера')),
        confirmId] });
      if (!ok) return;
      if (confirmId.value.trim() !== s.id) { toast('ID не совпал - удаление отменено', 'bad'); return; }
      await api('DELETE', `/servers/${enc(s.id)}?files=${files.checked}&backups=${backups.checked}`);
      toast('Сервер удалён'); location.hash = '#/';
    }) });

    const gameBox = h('div', { class: 'panel' }, gameSettings(ctx, running, () => TAB_VIEWS.settings(body, ctx), gameViews));
    replace(body,
      h('div', { class: 'panel' }, h('h2', { text: 'Менеджер' }),
        h('p', { class: 'muted small', text: 'Настройки менеджера' }),
        h('div', { class: 'form' },
          field('Название в менеджере', inputs.name),
          field('Перезапускать при падении', inputs.restart_on_crash, 'До 3 раз за 30 минут, затем сервер остаётся выключенным и приходит уведомление.'),
          field('Проверка на зомби', h('div', { class: 'row' }, zombieOn, zombieMinutes, h('span', { class: 'muted', text: 'мин' })),
            'Если процесс жив, но не отвечает на запросы Steam столько минут после загрузки - сервер останавливается (с проверкой базы) и запускается снова.'),
          field('Автообновление', inputs.auto_update, 'Проверка обновлений игры при запуске сервера (вручную или по расписанию) и каждые 30 минут во время работы.\nНашлась новая сборка на работающем сервере: сразу сообщение «Найдено обновление» и отложенный рестарт (см. «Оповещения»), дальше отсчёт как у планового; при рестарте - бэкап и обновление. Если плановый рестарт раньше - обновление ставится при нём. Выключенный сервер обновляется сразу.\nПосле падения и после перезагрузки хоста сервер поднимается без проверки, чтобы не ждать Steam.\nМоды пока не обновляются - они будут отдельным этапом.'),
          field('Бэкап при остановке', inputs.backup_on_stop, 'Копия мира в бэкапы менеджера после каждой нормальной остановки.'))),
      h('div', { class: 'panel' }, h('h2', { text: 'Запуск' }),
        h('p', { class: 'muted small', text: 'Меняются только на остановленном сервере.' }),
        h('div', { class: 'form' },
          field('Карта', h('div', { class: 'row' }, mapSelect, mapPath)),
          field('Игровой порт', inputs.game_port),
          field('Raw UDP-порт', rawPort(inputs.game_port), 'Игра всегда занимает следующий за игровым порт. Меняется вместе с игровым; открыть в файрволе нужно оба.'),
          field('Query-порт', inputs.query_port),
          field('Multihome (IP)', inputs.multihome, 'Пусто - слушать все адреса.'),
          field('Ветка Steam', inputs.branch),
          field('Доп. аргументы запуска', inputs.launch_args, 'Порты, карта и multihome передаются автоматически.'),
          field('Интервал бэкапов игры, с', inputs.periodic_backup_interval_seconds, '0 - как в игре. Пишется в Game.ini перед стартом; каждый бэкап копирует базу целиком.'))),
      gameBox,
      h('div', { class: 'panel' }, h('h2', { text: 'Оповещения' }),
        h('p', { class: 'muted small', text: 'Для уведомлений в игру нужен включённый RCON. Эмодзи и коды вида :warning: вычищаются для игры.' }),
        h('div', { class: 'form' },
          field('Предупреждать за', marksBox, 'Интервалы отсчёта перед плановым и отложенным рестартом: сообщение «Рестарт по расписанию» уходит за каждый интервал, рестарт - ровно в назначенное время.\nКнопки «Рестарт» и «Остановить» действуют сразу, без отсчёта.\nПусто - без отсчёта.'),
          field('Отложенный рестарт через, мин', delayInput, 'Кнопка «Отложенный рестарт» и рестарт ради найденного обновления ставятся на столько минут вперёд. Если плановый рестарт наступит раньше, обновление ставится при нём, а кнопка откажет.'),
          field('RCON-команда объявления', warnCommand, 'У Conan это broadcast.')),
        h('h3', { text: 'Для игроков' }),
        h('div', { class: 'msg-grid' }, kinds.map((k, i) => k.group === 'players' ? msgRows[i] : null)),
        h('h3', { text: 'Для админов (только Discord)' }),
        h('div', { class: 'msg-grid' }, kinds.map((k, i) => k.group === 'admins' ? msgRows[i] : null))),
      h('div', { class: 'panel' }, h('h2', { text: 'Скрипты (хуки)' }), hooksHelp(hooksInfo), datalist,
        hooksInfo.enabled ? h('div', { class: 'form' }, Object.entries(hookInputs).map(([ev, input]) => field(HOOK_LABELS[ev] || ev, input))) : null),
      h('div', { class: 'floating-save' }, save),
      h('h3', { text: 'Опасная зона' }),
      h('div', { class: 'panel' }, h('div', { class: 'row' },
        h('button', { text: 'Установить / проверить файлы', disabled: ctx.detail.lifecycle.state !== 'idle', onclick: e => act(e.target, async () => {
          if (!await ask('Запустить SteamCMD для этого сервера? Сервер должен быть остановлен.', { ok: 'Запустить', danger: false })) return;
          const job = await api('POST', `/servers/${enc(s.id)}/install`);
          openLog(s.id, 'steamcmd');
          await runJob(job);
        }) }),
        h('span', { class: 'spacer' }), deleteButton)));
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

// channelCategories tells which categories a stored channel takes. Channels
// from before the categories have a level and no event list.
function channelCategories(c, cats) {
  if (!c.event_types || !c.event_types.length) {
    const byLevel = { error: ['crash', 'attention'], warning: ['crash', 'attention', 'technical'] };
    return byLevel[c.min_severity] || cats.map(x => x.key);
  }
  return cats.filter(x => x.types.some(t => c.event_types.includes(t))).map(x => x.key);
}

const KIND_NAMES = { discord: 'Discord', telegram: 'Telegram', http: 'HTTP' };
// The same as the server's DefaultBody.
const DEFAULT_BODY = `{
  "event": "{event}",
  "kind": "{kind}",
  "severity": "{severity}",
  "server": "{server}",
  "server_id": "{server_id}",
  "title": "{title}",
  "text": "{text}",
  "time": "{time}",
  "details": {details}
}`;


async function viewNotifications() {
  const listBox = h('div');
  const formBox = h('div');
  let servers = [], cats = [];
  try { [servers, cats] = await Promise.all([api('GET', '/servers'), api('GET', '/notification-categories')]); } catch (e) { toast(e.message, 'bad'); }
  const serverName = id => (servers.find(s => s.id === id) || {}).name || id;

  const draw = async () => {
    let channels = [];
    try { channels = await api('GET', '/notifications'); } catch (e) { replace(listBox, h('div', { class: 'notice bad', text: e.message })); return; }
    if (!channels.length) {
      replace(listBox, h('div', { class: 'panel' }, h('p', { class: 'muted', text: 'Каналов пока нет. Канал - это Discord-канал (вебхук), чат Telegram (бот) или свой адрес (HTTP), куда менеджер будет писать.' })));
      return;
    }
    replace(listBox, h('div', { class: 'grid' }, channels.map(c => {
      const keys = channelCategories(c, cats);
      return h('div', { class: 'card' },
        h('div', { class: 'title' }, h('strong', { text: c.name }), h('span', { class: 'spacer' }),
          c.enabled ? h('span', { class: 'badge ok', text: 'включён' }) : h('span', { class: 'badge', text: 'выключен' })),
        facts([
          ['Серверы', (c.server_ids || []).length ? c.server_ids.map(serverName).join(', ') : 'все'],
          ['Присылает', cats.filter(x => keys.includes(x.key)).map(x => x.title).join(', ') || '-'],
          c.kind === 'discord' && ['Упоминание', c.mention ? h('span', { class: 'mono', text: c.mention }) : 'нет'],
          ['Куда', h('span', {}, h('span', { class: 'badge', text: KIND_NAMES[c.kind] || c.kind }), ' ', h('span', { class: 'mono small', text: c.target }))],
          ['Последняя отправка', c.last_error ? h('span', { class: 'small', style: 'color:var(--bad)', text: c.last_error }) : c.last_sent_at ? fmtAgo(c.last_sent_at) : 'ещё не было'],
        ]),
        h('div', { class: 'row' },
          h('button', { text: 'Отправить тест', onclick: e => act(e.target, async () => { await api('POST', `/notifications/${enc(c.id)}/test`); toast('Тестовое сообщение отправлено - проверьте, дошло ли'); draw(); }) }),
          h('button', { text: 'Изменить', onclick: () => showForm(c) }),
          h('span', { class: 'spacer' }),
          h('button', { class: 'danger', text: 'Удалить', onclick: e => act(e.target, async () => {
            if (!await ask(`Удалить канал «${c.name}»?`, { ok: 'Удалить' })) return;
            await api('DELETE', `/notifications/${enc(c.id)}`); toast('Удалено'); draw();
          }) })));
    })));
  };

  const showForm = (channel, kind) => {
    const c = channel || { kind, enabled: true, language: 'ru', min_severity: 'info', event_types: [], settings: kind === 'http' ? { method: 'POST', content_type: 'application/json', body: DEFAULT_BODY } : { proxy: kind === 'telegram' } };
    kind = c.kind;
    const st = c.settings || {};
    const keep = channel ? 'оставьте пустым, чтобы не менять' : '';
    const name = h('input', { value: c.name || { discord: 'Discord', telegram: 'Telegram', http: 'Мой сайт' }[kind], autocomplete: 'off' });
    // A channel watches any set of servers, so the field is a list of them
    // with one switch for "all": an empty set is what "all" means on the
    // server, and it keeps taking servers added later.
    const watched = c.server_ids || [];
    const allServers = h('input', { type: 'checkbox', checked: !watched.length });
    const serverBoxes = Object.fromEntries(servers.map(x => [x.id, h('input', { type: 'checkbox', checked: watched.includes(x.id) })]));
    const serverList = h('div', { class: 'server-pick' }, servers.length
      ? servers.map(x => h('label', { class: 'check-row' }, serverBoxes[x.id], h('span', {}, x.name)))
      : h('p', { class: 'muted small', text: 'Серверов пока нет.' }));
    const syncServers = () => {
      serverList.classList.toggle('off', allServers.checked);
      Object.values(serverBoxes).forEach(b => { b.disabled = allServers.checked; });
    };
    allServers.addEventListener('change', syncServers);
    syncServers();
    const server = h('div', {}, h('label', { class: 'check-row' }, allServers, h('span', { text: 'Все серверы' })), serverList);
    const language = h('select', {}, [['ru', 'Русский'], ['en', 'English']].map(([v, t]) => h('option', { value: v, text: t, selected: c.language === v })));
    const enabled = h('input', { type: 'checkbox', checked: c.enabled });
    const proxy = h('input', { type: 'checkbox', checked: !!st.proxy });
    const proxyHelp = 'Через прокси со страницы «Панель» (CONAN_MANAGER_NOTIFY_PROXY). Нужен, если хост не видит сервис напрямую. Без прокси на панели - напрямую.';

    // What differs between the kinds: where to send, and how.
    let where = [], extra = [], collect;
    if (kind === 'discord') {
      const url = h('input', { class: 'mono secret', placeholder: keep || 'https://discord.com/api/webhooks/...', autocomplete: 'off' });
      const mention = h('input', { class: 'mono', value: c.mention || '', placeholder: '<@&123456789012345678>' });
      const json = h('textarea', { class: 'mono', rows: 6, spellcheck: 'false', placeholder: '{"content": "{text}"}' });
      json.value = st.body || '';
      where = [field('Webhook URL', url, 'В Discord: настройки канала → Интеграции → Вебхуки → Новый вебхук → «Копировать URL веб-хука».\nАдрес хранится зашифрованным и дальше показывается замаскированным.')];
      extra = [field('Упоминание роли или пользователя', mention, 'Роль: <@&ID роли>, человек: <@ID>, или @here.')];
      extra.push(field('Свой JSON', json, 'Пусто - в канал уходит только текст сообщения из настроек сервера («Настройки» → «Оповещения»).\nСвой JSON - тело запроса к вебхуку целиком, например с embeds: {"content": "{mention} {text}", "embeds": [{"title": "{server}", "description": "{log}"}]}.\nПеременные: {text}, {title}, {server}, {server_id}, {event}, {kind}, {severity}, {time}, {mention} - упоминание (только у ошибок), {log} - строки из лога игры при падении, {details} - все поля объектом. Переменные пишутся внутри кавычек, кроме {details}.'));
      collect = body => { if (url.value.trim()) body.webhook_url = url.value.trim(); body.mention = mention.value.trim(); body.settings = { body: json.value }; };
    } else if (kind === 'telegram') {
      const token = h('input', { class: 'mono secret', placeholder: keep || '123456789:AA...', autocomplete: 'off' });
      const chat = h('input', { class: 'mono', value: st.chat_id || '', placeholder: '-1001234567890 или @channel' });
      const thread = h('input', { type: 'number', min: 0, value: st.thread_id || '', placeholder: 'нет', style: 'width:140px' });
      where = [
        field('Токен бота', token, 'Telegram → @BotFather → /newbot → токен вида 123456789:AA…\nХранится зашифрованным.'),
        field('Чат', chat, 'Куда писать. Группа: добавьте бота в группу; её id (начинается с -100) покажет, например, @getidsbot, если его добавить туда же. Канал: сделайте бота админом канала и укажите @имя_канала или id.\nЛичка: напишите боту /start и укажите свой id.'),
        field('Тема (topic id)', thread, 'Для группы с темами: номер темы - последнее число в ссылке на сообщение в ней. Пусто - в общий чат.')];
      extra = [field('Через прокси', proxy, proxyHelp + '\nTelegram из России часто недоступен напрямую.')];
      collect = body => {
        if (token.value.trim()) body.token = token.value.trim();
        body.settings = { chat_id: chat.value.trim(), thread_id: +thread.value || 0, proxy: proxy.checked };
      };
    } else {
      const url = h('input', { class: 'mono secret', placeholder: keep || 'https://example.com/api/conan', autocomplete: 'off' });
      const method = h('select', {}, ['POST', 'PUT', 'PATCH', 'GET', 'DELETE'].map(m => h('option', { value: m, text: m, selected: (st.method || 'POST') === m })));
      const type = h('select', {}, [['application/json', 'JSON'], ['application/x-www-form-urlencoded', 'Форма (x-www-form-urlencoded)'], ['text/plain', 'Текст']].map(([v, t]) => h('option', { value: v, text: t, selected: (st.content_type || 'application/json') === v })));
      const body = h('textarea', { class: 'mono', rows: 11, spellcheck: 'false' });
      body.value = st.body || '';
      const headers = h('textarea', { class: 'mono secret-area', rows: 3, spellcheck: 'false', placeholder: (c.headers && c.headers.length ? 'Сохранены: ' + c.headers.join(', ') + '. ' + keep : 'Authorization: Bearer ...\nX-Api-Key: ...') });
      const clearHeaders = h('input', { type: 'checkbox' });
      const allowPrivate = h('input', { type: 'checkbox', checked: !!st.allow_private });
      const vars = 'Переменные: {text} - текст сообщения, {title} - заголовок, {server} - имя сервера, {server_id}, {event} - тип события (server.crashed…), {kind} - вид сообщения (crash, update…), {severity} - info / warning / error, {time} - время UTC, {log} - строки из лога игры при падении, {details} - все поля события объектом JSON.';
      where = [
        field('Адрес', url, 'http:// или https://. В адресе тоже можно ставить переменные, например ?text={text} - они кодируются для URL.\nХранится зашифрованным: в адресе часто ключ.'),
        field('Метод', method, 'GET и DELETE - без тела: всё нужное передавайте в адресе.'),
        field('Заголовки', h('div', {}, headers, c.headers && c.headers.length ? h('label', { class: 'row small' }, clearHeaders, 'удалить сохранённые') : null), 'По одному на строку: Имя: значение. Для ключей и токенов. Хранятся зашифрованными, значения больше не показываются.\nContent-Type ставится сам по типу тела.')];
      extra = [
        field('Тип тела', type, 'JSON: пишите переменные внутри кавычек - "{text}"; кавычки и переносы в значениях экранируются сами. {details} - без кавычек, это объект.\nФорма: text={text}&server={server}.\nТекст: как есть.'),
        field('Тело', body, vars + '\nПусто - запрос без тела.'),
        field('Через прокси', proxy, proxyHelp),
        field('Разрешить локальные адреса', allowPrivate, 'Без галочки менеджер не отправит запрос на свой хост (127.0.0.1), в локальные сети (10.x, 192.168.x…) и на служебные адреса облака - чтобы через панель нельзя было достучаться до внутренних сервисов. Включите, если ваш сайт в той же сети.')];
      collect = b => {
        if (url.value.trim()) b.url = url.value.trim();
        if (headers.value.trim()) b.headers = headers.value;
        else if (clearHeaders.checked) b.headers = '';
        b.settings = { method: method.value, content_type: type.value, body: body.value, proxy: proxy.checked, allow_private: allowPrivate.checked };
      };
    }

    // A new channel starts on the messages meant for players; crashes and
    // technical notices are ticked by whoever wants them.
    const selected = channel ? channelCategories(c, cats) : cats.filter(x => x.audience === 'players').map(x => x.key);
    const boxes = {};
    // One list, two columns: the switch and its name on the left, what the
    // message is on the right. The explanation is the point of the row, so it
    // is written out rather than hidden behind a question mark.
    const columns = cats.flatMap(x => {
      boxes[x.key] = h('input', { type: 'checkbox', checked: selected.includes(x.key) });
      return [
        h('label', { class: 'row event-item' }, boxes[x.key], h('span', { text: x.title })),
        h('p', { class: 'muted small event-help', text: x.help }),
      ];
    });
    const kindSwitch = channel ? null : h('div', { class: 'row kind-switch' }, Object.entries(KIND_NAMES).map(([k, t]) =>
      h('button', { class: k === kind ? 'primary' : '', text: t, onclick: () => showForm(null, k) })));

    replace(formBox, h('div', { class: 'panel' }, h('h2', { text: channel ? `Канал «${c.name}»` : 'Новый канал' }),
      kindSwitch,
      h('h3', { text: '1. Куда писать' }),
      h('div', { class: 'form' }, where,
        field('Название', name, 'Для себя: чтобы отличать каналы в этом списке.'),
        field('Серверы', server, 'От каких серверов присылать сообщения.')),
      h('h3', { text: '2. Что присылать' }),
      h('div', { class: 'category-list' }, columns),
      h('h3', { text: '3. Оформление' }),
      h('div', { class: 'form' }, extra,
        field('Язык сообщений', language, 'Для заголовков, которые менеджер пишет сам.'),
        field('Включён', enabled)),
      h('div', { class: 'row end', style: 'margin-top:14px' },
        h('button', { class: 'ghost', text: 'Отмена', onclick: () => replace(formBox) }),
        h('button', { class: 'primary', text: channel ? 'Сохранить' : 'Добавить канал', onclick: e => act(e.target, async () => {
          const types = [...new Set(cats.filter(x => boxes[x.key].checked).flatMap(x => x.types))];
          if (!types.length) throw new Error('Выберите хотя бы одно сообщение');
          const chosen = Object.entries(serverBoxes).filter(([, b]) => b.checked).map(([id]) => id);
          if (!allServers.checked && !chosen.length) throw new Error('Выберите серверы или отметьте «Все серверы»');
          const body = { name: name.value.trim(), server_ids: allServers.checked ? [] : chosen, min_severity: 'info', event_types: types,
            language: language.value, enabled: enabled.checked };
          if (!channel) body.kind = kind;
          collect(body);
          const saved = channel ? await api('PATCH', `/notifications/${enc(channel.id)}`, body) : await api('POST', '/notifications', body);
          replace(formBox); await draw();
          if (!channel && saved && saved.id && await ask('Канал добавлен. Отправить тестовое сообщение, чтобы проверить?', { ok: 'Отправить', danger: false })) {
            await api('POST', `/notifications/${enc(saved.id)}/test`); toast('Тестовое сообщение отправлено - проверьте, дошло ли'); draw();
          } else toast('Сохранено');
        }) }))));
    formBox.scrollIntoView({ behavior: 'smooth', block: 'start' });
    name.focus();
  };

  replace(app,
    h('div', { class: 'head' }, h('h1', { text: 'Уведомления' }),
      h('button', { class: 'primary', text: '+ Добавить канал', onclick: () => showForm(null, 'discord') })),
    h('p', { class: 'muted small', text: 'Настройка канала, куда менеджер будет отправлять уведомления.' }),
    formBox, listBox);
  await draw();
}

// ---------- manager panel: administrators and settings ----------

const SOURCE = { panel: ['Задано в панели', 'info'], env: ['Из файла окружения', 'plain'], default: ['По умолчанию', 'plain'] };

async function viewPanel() {
  const securityBox = h('div');
  const adminsBox = h('div');
  const sessionsBox = h('div');
  const settingsBox = h('div');
  replace(app, h('div', { class: 'head' }, h('h1', { text: 'Панель' })), securityBox, adminsBox, sessionsBox, settingsBox);

  const drawSecurity = async () => {
    const data = await api('GET', '/security');
    const x = data.access;
    const worst = data.checks.some(c => c.level === 'bad') ? 'bad' : data.checks.some(c => c.level === 'warn') ? 'warn' : 'ok';
    const list = x => x.length ? x.join(', ') : null;
    replace(securityBox, h('div', { class: 'panel' },
      h('div', { class: 'row' }, h('h2', { text: 'Безопасность' }), h('span', { class: 'spacer' }),
        h('span', { class: 'badge ' + worst, text: worst === 'ok' ? 'всё в порядке' : worst === 'warn' ? 'есть замечания' : 'есть проблемы' })),
      h('div', { class: 'security-grid' },
        h('ul', { class: 'checks' }, data.checks.map(c => h('li', { class: 'check ' + c.level },
          h('span', { class: 'check-dot' }), h('div', {}, h('strong', { text: c.title }), c.detail ? h('div', { class: 'muted small', text: c.detail }) : null)))),
        h('dl', { class: 'facts' },
          h('dt', { text: 'Ваш адрес' }), h('dd', { class: 'mono', text: x.client_ip || '-' }),
          h('dt', { text: 'Соединение' }), h('dd', { text: x.https ? 'HTTPS' : 'HTTP без шифрования' }),
          h('dt', { text: 'Разрешённые адреса' }), h('dd', { class: 'mono', text: list(x.allowed_ips) || 'любые' }),
          h('dt', { text: 'Доверенные прокси' }), h('dd', { class: 'mono', text: list(x.trusted_proxies) || 'нет' }),
          h('dt', { text: 'Слушает' }), h('dd', { class: 'mono', text: x.listen + (x.tls ? ' (TLS)' : '') }),
          h('dt', { text: 'Basic Auth' }), h('dd', { text: x.basic_auth ? 'включён для скриптов' : 'выключен' }),
          h('dt', { text: 'Сессия' }), h('dd', { text: (x.session_idle === '0' ? 'без выхода по бездействию' : 'выход через ' + x.session_idle + ' бездействия') + ', не дольше ' + x.session_max_age }))),
      h('p', { class: 'muted small', text: 'Адреса, прокси, TLS и Basic Auth задаются только в /etc/conan-manager/conan-manager.env: вход в панель не должен позволять расширить доступ к ней. После 10 неудачных входов адрес блокируется на 15 минут.' })));
  };

  const drawSessions = async () => {
    const list = await api('GET', '/sessions');
    const mine = list.filter(s => me && s.username === me.username && !s.current).length;
    replace(sessionsBox, h('div', { class: 'panel' }, h('div', { class: 'row' }, h('h2', { text: 'Сессии' }), h('span', { class: 'spacer' }),
      h('button', { text: 'Завершить мои другие сессии', disabled: !mine, onclick: e => act(e.target, async () => {
        if (!await ask('Завершить все ваши сессии, кроме этой? На других устройствах придётся войти заново.', { ok: 'Завершить' })) return;
        const out = await api('POST', '/sessions/end-others');
        toast('Завершено сессий: ' + out.ended); drawSessions();
      }) })),
      table(['Администратор', 'Адрес', 'Браузер', 'Вход', 'Активность', ''], list.map(s => [
        h('span', {}, h('strong', { text: s.username }), s.current ? h('span', { class: 'badge info plain', style: 'margin-left:8px', text: 'эта сессия' }) : null),
        h('span', { class: 'mono', text: s.ip || '-' }),
        h('span', { title: s.user_agent, text: shortAgent(s.user_agent) }),
        fmtTime(s.created_at), fmtAgo(s.last_seen),
        h('div', { class: 'row end' }, h('button', { class: s.current ? 'ghost' : 'danger', text: s.current ? 'Выйти' : 'Завершить', onclick: e => act(e.target, async () => {
          if (s.current) { await api('POST', '/logout'); showLogin('Вы вышли из панели.'); return; }
          if (!await ask(`Завершить сессию ${s.username} (${s.ip || 'адрес неизвестен'})?`, { ok: 'Завершить' })) return;
          await api('DELETE', `/sessions/${enc(s.id)}`);
          toast('Сессия завершена'); drawSessions();
        }) }))]), 'Нет открытых сессий'),
      h('p', { class: 'muted small', text: 'Смена пароля или удаление администратора сразу завершает все его сессии.' })));
  };

  const passwordInput = () => h('input', { type: 'password', autocomplete: 'new-password', minlength: 10, placeholder: 'не короче 10 символов' });

  const drawAdmins = async () => {
    const list = await api('GET', '/admins');
    const username = h('input', { autocomplete: 'off', pattern: '[A-Za-z0-9][A-Za-z0-9._-]{1,31}' });
    const password = passwordInput();
    const show = h('input', { type: 'checkbox' });
    show.onchange = () => { password.type = show.checked ? 'text' : 'password'; };
    replace(adminsBox, h('div', { class: 'panel' }, h('h2', { text: 'Администраторы' }),
      h('p', { class: 'muted small', text: 'У всех администраторов одинаковые права. Основной задан в файле окружения, его пароль меняется только там.' }),
      table(['Логин', 'Откуда', 'Добавлен', 'Пароль изменён', ''], list.map(a => [
        h('strong', { text: a.username }),
        a.builtin ? h('span', { class: 'badge plain', text: 'файл окружения' }) : h('span', { class: 'badge info plain', text: 'панель' }),
        a.builtin ? '-' : fmtTime(a.created_at), a.builtin ? '-' : fmtTime(a.password_changed_at),
        a.builtin ? h('span', { class: 'muted small', text: 'CONAN_MANAGER_ADMIN_PASSWORD' }) : h('div', { class: 'row end' },
          h('button', { text: 'Сменить пароль', onclick: e => act(e.target, async () => {
            const input = passwordInput();
            if (!await ask(`Новый пароль для ${a.username}:`, { ok: 'Сменить', danger: false, extra: [input] })) return;
            await api('PUT', `/admins/${enc(a.username)}/password`, { password: input.value });
            // Own password: the session was renewed with a new token.
            if (me && a.username === me.username) await loadMe();
            toast('Пароль изменён, сессии с прежним паролем завершены'); drawAdmins(); drawSessions();
          }) }),
          h('button', { class: 'danger', text: 'Удалить', onclick: e => act(e.target, async () => {
            if (!await ask(`Удалить администратора ${a.username}? Он сразу потеряет доступ.`, { ok: 'Удалить' })) return;
            await api('DELETE', `/admins/${enc(a.username)}`);
            toast('Администратор удалён'); drawAdmins(); drawSessions();
          }) }))])),
      h('h3', { text: 'Добавить администратора' }),
      h('div', { class: 'form' },
        field('Логин', username, 'Латиница, цифры, точка, дефис, подчёркивание; 2-32 символа.'),
        field('Пароль', password, 'Не короче 10 символов. Хранится как хеш PBKDF2, восстановить его нельзя - только задать новый.'),
        field('Показать пароль', show)),
      h('div', { class: 'row end', style: 'margin-top:10px' },
        h('button', { class: 'primary', text: '+ Добавить', onclick: e => act(e.target, async () => {
          await api('POST', '/admins', { username: username.value.trim(), password: password.value });
          toast('Администратор добавлен'); drawAdmins();
        }) }))));
  };

  const drawSettings = async data => {
    data = data || await api('GET', '/panel/settings');
    const inputs = {};
    const groups = [];
    for (const s of data.settings) {
      let group = groups.find(g => g.name === s.group);
      if (!group) groups.push(group = { name: s.group, rows: [] });
      const input = s.kind === 'bool'
        ? h('input', { type: 'checkbox', checked: /^(1|true|yes|on)$/i.test(s.value) })
        : h('input', { class: ['path', 'proxy', 'origin'].includes(s.kind) ? 'mono' : '', value: s.value, autocomplete: 'off',
            placeholder: s.kind === 'proxy' ? 'socks5://host:1080' : s.kind === 'origin' ? 'https://manager.example.com' : '' });
      inputs[s.key] = { s, input };
      const help = s.help + '\n\nПеременная: ' + s.key + (s.default ? '\nПо умолчанию: ' + s.default : '') +
        (s.env ? '\nВ файле окружения: ' + s.env : '') + (s.live ? '\nПрименяется сразу.' : '\nПрименяется после перезапуска менеджера.');
      const dot = h('span', { class: 'src-dot src-' + s.source, 'data-tip': SOURCE[s.source][0] });
      const waiting = !s.live && s.value !== s.active
        ? h('span', { class: 'src-wait', text: '⟳', 'data-tip': 'Сейчас действует: ' + (s.active || 'пусто') + '. Новое значение - после перезапуска менеджера.' }) : null;
      const reset = s.source === 'panel' ? h('button', { class: 'ghost icon-button', text: '↺', title: s.env ? 'Сбросить: вернуть значение из файла окружения (' + s.env + ')' : 'Сбросить: вернуть значение по умолчанию' + (s.default ? ' (' + s.default + ')' : ''), onclick: e => act(e.target, async () => {
        toast('Сброшено'); drawSettings(await api('PUT', '/panel/settings', { [s.key]: null }));
      }) }) : null;
      group.rows.push(field(s.label, h('div', { class: 'setting-row' }, dot, input, waiting, reset), help));
    }
    const restart = h('button', { class: data.restart_pending ? 'primary' : '', text: 'Перезапустить менеджер', disabled: !data.can_restart, onclick: e => act(e.target, async () => {
      if (!await ask('Перезапустить менеджер? Серверы продолжат работать, страница переподключится через несколько секунд.', { ok: 'Перезапустить', danger: false })) return;
      await api('POST', '/panel/restart');
      toast('Менеджер перезапускается…', 'info');
      await waitForManager(new Date(data.started_at).getTime());
      toast('Менеджер снова в строю'); drawSettings();
    }) });
    replace(settingsBox, h('div', { class: 'panel' }, h('h2', { text: 'Настройки менеджера' }),
      h('p', { class: 'muted small', text: 'Значение из панели главнее файла окружения. Кнопка ↺ возвращает значение из файла или по умолчанию. Адрес, база, основной администратор, ключ шифрования и папка скриптов меняются только в /etc/conan-manager/conan-manager.env.' }),
      h('div', { class: 'src-legend muted small' },
        h('span', {}, h('span', { class: 'src-dot src-panel' }), 'задано в панели'),
        h('span', {}, h('span', { class: 'src-dot src-env' }), 'из файла окружения'),
        h('span', {}, h('span', { class: 'src-dot src-default' }), 'по умолчанию'),
        h('span', {}, h('span', { class: 'src-wait', text: '⟳' }), 'ждёт перезапуска менеджера')),
      data.restart_pending ? h('div', { class: 'notice' }, 'Часть изменений применится после перезапуска менеджера. Серверы при этом не останавливаются.') : null,
      groups.map(g => [h('h3', { text: g.name }), h('div', { class: 'form' }, g.rows)]),
      h('div', { class: 'row', style: 'margin-top:14px' }, restart,
        data.can_restart ? null : h('span', { class: 'muted small', text: 'Менеджер запущен не как служба systemd - перезапустите его вручную.' }),
        h('span', { class: 'spacer' }),
        h('button', { class: 'primary', text: 'Сохранить', onclick: e => act(e.target, async () => {
          const patch = {};
          for (const [key, { s, input }] of Object.entries(inputs)) {
            const value = s.kind === 'bool' ? String(input.checked) : input.value.trim();
            const current = s.kind === 'bool' ? String(/^(1|true|yes|on)$/i.test(s.value)) : s.value;
            if (value !== current) patch[key] = value;
          }
          if (!Object.keys(patch).length) { toast('Изменений нет', 'info'); return; }
          toast('Сохранено'); drawSettings(await api('PUT', '/panel/settings', patch));
        }) }))));
  };

  await Promise.all([drawSecurity(), drawAdmins(), drawSessions(), drawSettings()]);
}

// shortAgent names the browser and system of a User-Agent.
function shortAgent(ua) {
  if (!ua) return '-';
  const browser = /Edg\//.test(ua) ? 'Edge' : /OPR\/|Opera/.test(ua) ? 'Opera' : /YaBrowser/.test(ua) ? 'Яндекс Браузер'
    : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : null;
  const os = /Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS'
    : /Mac OS X/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : null;
  if (browser) return browser + (os ? ', ' + os : '');
  return ua.split(/[\s(]/)[0].slice(0, 40);
}

// ---------- audit ----------

const AUDIT_ACTIONS = {
  'login': 'Вход', 'login.failed': 'Неудачный вход', 'login.blocked': 'Адрес заблокирован', 'logout': 'Выход',
  'csrf.refused': 'Запрос отклонён',
  'POST /servers': 'Создание сервера', 'PATCH /servers/{}': 'Настройки сервера', 'DELETE /servers/{}': 'Удаление сервера',
  'POST /servers/{}/start': 'Запуск', 'POST /servers/{}/stop': 'Остановка', 'POST /servers/{}/restart': 'Рестарт',
  'POST /servers/{}/kill': 'Убийство процесса', 'POST /servers/{}/install': 'Установка', 'POST /servers/{}/update': 'Обновление',
  'POST /servers/{}/verify': 'Проверка файлов', 'POST /servers/{}/rcon': 'Команда RCON',
  'POST /servers/{}/delayed-restart': 'Отложенный рестарт', 'DELETE /servers/{}/delayed-restart': 'Отмена рестарта',
  'POST /servers/{}/backup': 'Бэкап', 'POST /servers/{}/backups/{}/restore': 'Восстановление из бэкапа',
  'DELETE /servers/{}/backups/{}': 'Удаление бэкапа',
  'PUT /servers/{}/config/{}': 'Изменение конфига',
  'DELETE /servers/{}/config/{}': 'Удаление конфига', 'POST /servers/{}/config/{}/versions/{}/restore': 'Откат конфига',
  'PUT /servers/{}/schedule': 'Расписание', 'DELETE /servers/{}/schedule': 'Удаление расписания',
  'PUT /servers/{}/mods': 'Список модов', 'POST /servers/{}/mods/install': 'Установка модов',
  'POST /servers/{}/mods/check': 'Проверка модов', 'POST /servers/{}/mods/collection': 'Добавление коллекции модов',
  'POST /notifications': 'Добавление канала', 'PATCH /notifications/{}': 'Изменение канала', 'PUT /notifications/{}': 'Изменение канала',
  'DELETE /notifications/{}': 'Удаление канала', 'POST /notifications/{}/test': 'Проверка канала',
  'POST /admins': 'Добавление администратора', 'DELETE /admins/{}': 'Удаление администратора', 'PUT /admins/{}/password': 'Смена пароля',
  'PUT /panel/settings': 'Настройки менеджера', 'POST /panel/restart': 'Перезапуск менеджера',
  'DELETE /sessions/{}': 'Завершение сессии', 'POST /sessions/end-others': 'Завершение других сессий',
};

function auditDetails(e) {
  if (e.action === 'login.failed') return e.details === 'basic' ? 'Basic Auth' : 'форма входа';
  if (e.action === 'login.blocked') return 'адрес заблокирован на ' + e.details.replace(/0s$/, '').replace(/m$/, ' мин');
  if (e.action === 'csrf.refused') return 'без CSRF-токена: ' + e.details;
  return e.details;
}

async function viewAudit() {
  const box = h('div', { class: 'panel' });
  const more = h('button', { text: 'Показать ещё' });
  const user = h('input', { placeholder: 'все', autocomplete: 'off' });
  const target = h('input', { placeholder: 'сервер, администратор…', autocomplete: 'off' });
  const failed = h('input', { type: 'checkbox' });
  let items = [];
  const query = () => '&user=' + enc(user.value.trim()) + '&target=' + enc(target.value.trim()) + (failed.checked ? '&failed=1' : '');
  const load = async before => {
    const page = await api('GET', '/audit?limit=100' + query() + (before ? '&before=' + before : ''));
    items = before ? items.concat(page) : page;
    replace(box, table(['Время', 'Администратор', 'Адрес', 'Действие', 'Объект', 'Результат'], items.map(e => {
      const label = AUDIT_ACTIONS[e.action];
      const server = e.action.includes('/servers/{}') ? e.target : '';
      return [
        h('span', { class: 'nowrap', title: fmtAgo(e.time), text: fmtTime(e.time) }),
        e.username ? h('strong', { text: e.username }) : h('span', { class: 'muted', text: '-' }),
        h('span', { class: 'mono', text: e.ip || '-' }),
        h('div', {}, h('span', { title: e.action, text: label || e.action }),
          e.details ? h('div', { class: 'muted small mono audit-details', text: auditDetails(e) }) : null),
        server ? h('a', { href: '#/server/' + enc(server), text: server })
          : e.action.startsWith('log') ? h('span', { class: 'muted', text: '-' }) : h('span', { text: e.target || '-' }),
        e.status < 400 ? h('span', { class: 'badge ok', text: 'выполнено' })
          : h('span', { class: 'badge ' + (e.status >= 500 ? 'bad' : 'warn'), title: 'HTTP ' + e.status, text: e.status === 401 ? 'отказ' : e.status === 429 ? 'блокировка' : 'ошибка ' + e.status }),
      ];
    }), 'Записей нет'));
    more.disabled = page.length < 100;
  };
  const apply = h('button', { class: 'primary', text: 'Показать', onclick: e => act(e.target, () => load(0)) });
  for (const input of [user, target]) input.addEventListener('keydown', e => { if (e.key === 'Enter') apply.click(); });
  failed.onchange = () => apply.click();
  replace(app, h('div', { class: 'head' }, h('h1', { text: 'Аудит' }),
      h('span', { class: 'muted small', text: 'Кто и что менял через панель и API: входы, запуски, настройки, бэкапы. Хранится год.' })),
    h('div', { class: 'panel audit-filters' },
      h('label', {}, 'Администратор', user), h('label', {}, 'Объект', target),
      h('label', { class: 'inline' }, failed, 'Только отказы и ошибки'), apply),
    box, h('div', { class: 'row' }, more));
  more.onclick = e => act(e.target, () => load(items.length ? items[items.length - 1].id : 0));
  await act(null, () => load(0));
}

// waitForManager polls until a manager started after `since` answers.
async function waitForManager(since) {
  for (let i = 0; i < 60; i++) {
    await new Promise(r => setTimeout(r, 2000));
    try {
      const data = await api('GET', '/panel/settings');
      if (new Date(data.started_at).getTime() > since) return;
    } catch { /* still restarting */ }
  }
  throw new Error('менеджер не ответил за 2 минуты - проверьте journalctl -u conan-manager');
}

// ---------- routing ----------

async function render() {
  if (locked()) return;
  leaving.forEach(fn => fn());
  leaving = [];
  const parts = location.hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
  const section = parts[0] || '';
  const nav = ['events', 'notifications', 'panel', 'audit'].includes(section) ? section : 'servers';
  document.querySelectorAll('[data-nav]').forEach(a => a.classList.toggle('active', a.dataset.nav === nav));
  try {
    if (section === '') await viewServers();
    else if (section === 'new') await viewNewServer();
    else if (section === 'server' && parts[1]) await viewServer(parts[1], parts[2]);
    else if (section === 'events') await viewEvents();
    else if (section === 'notifications') await viewNotifications();
    else if (section === 'panel') await viewPanel();
    else if (section === 'audit') await viewAudit();
    else replace(app, h('p', {}, 'Страница не найдена. ', h('a', { href: '#/', text: 'К серверам' })));
  } catch (e) {
    if (locked()) return;
    replace(app, h('div', { class: 'notice bad', text: 'Ошибка: ' + e.message }));
  }
}

window.addEventListener('hashchange', render);
// The page asks who is logged in first; without a session the login form
// comes up instead of the page.
loadMe().then(render, e => { if (e.status !== 401) render(); });
