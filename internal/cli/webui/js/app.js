// app.js:token 引導、/api/commands、命令列;命令的串流與區塊在 console.js。
import { Console } from './console.js';
import { Player } from './player.js';
import { initISRC } from './pages/isrc.js';
import { initSearch } from './pages/search.js';
import { initPlaylists } from './pages/playlists.js';
import { initSync } from './pages/sync.js';
import { initAccount } from './pages/account.js';
import { initDoctor } from './pages/doctor.js';

export const api = {
  token: '',
  async fetch(path, init = {}) {
    const headers = new Headers(init.headers || {});
    headers.set('X-Capy-Token', api.token);
    return fetch(path, { ...init, headers });
  },
};

function bootToken() {
  const m = /[#&]t=([A-Za-z0-9_-]+)/.exec(location.hash);
  if (m) {
    try { sessionStorage.setItem('capy.token', m[1]); } catch (_) { /* 私密視窗:留在記憶體 */ }
    api.token = m[1];
    history.replaceState(null, '', location.pathname + '#/console');
    return;
  }
  try { api.token = sessionStorage.getItem('capy.token') || ''; } catch (_) { api.token = ''; }
}

async function loadCommands() {
  const r = await api.fetch('/api/commands');
  if (r.status === 401) { notice('token 不對或已失效:回到啟動 capy --web 時印的網址'); return; }
  const d = await r.json();
  document.getElementById('version').textContent = d.version;
  document.getElementById('default-provider').textContent = d.default_provider;
  const dl = document.getElementById('commands');
  for (const c of d.commands) {
    const o = document.createElement('option');
    o.value = c.path.replace(/^capy ?/, '');
    o.label = c.short;
    dl.appendChild(o);
  }
  return d;
}

export function notice(text) {
  const el = document.getElementById('notice');
  el.textContent = text || '';
  el.hidden = !text;
}

bootToken();
const con = new Console(document.getElementById('console'), api, notice);
const input = document.getElementById('cmd');
const runBtn = document.getElementById('run');
const cancelBtn = document.getElementById('cancel');

// 不用 <form>:CSP form-action 'none' 與 submit 的互動零暴露;Enter 與按鈕都走 submit()。
async function submit() {
  if (con.running) return;
  if (document.body.hasAttribute('data-stale')) { notice('binary 已更新,這個 capy --web 仍是舊版,請重啟'); return; }
  const line = input.value.trim();
  input.value = '';
  input.disabled = true; runBtn.disabled = true; cancelBtn.hidden = false;
  try {
    await con.run(line);
  } finally {
    input.disabled = false; runBtn.disabled = false; cancelBtn.hidden = true;
    input.focus();
  }
}
// 組字中的 Enter 是「確認候選字」,不是「送出」:注音 / 拼音使用者按的第一個 Enter 會被輸入法吃掉。
// Safari 先送 compositionend 再送這個 keydown(isComposing 已是 false),所以還要看 keyCode 229。
input.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
  ev.preventDefault();
  submit();
});
runBtn.addEventListener('click', submit);
cancelBtn.addEventListener('click', () => con.cancel());
window.addEventListener('beforeunload', (ev) => { if (con.running) { ev.preventDefault(); ev.returnValue = ''; } });
// ── 路由:七頁,#/<page>[/<arg>];每頁第一次到達時才初始化 ──
const providers = { list: ['spotify', 'apple', 'local'], current: 'spotify' };
const PAGES = ['console', 'search', 'playlists', 'sync', 'isrc', 'account', 'doctor'];
const ready = new Set();

function showPage(name) {
  for (const p of document.querySelectorAll('.page')) p.hidden = p.id !== 'page-' + name;
  for (const it of document.querySelectorAll('.rail__item')) it.classList.toggle('is-active', it.dataset.page === name);
}

function route() {
  const m = /^#\/([a-z]+)(?:\/([^/?#]+))?/.exec(location.hash || '');
  const name = m && PAGES.includes(m[1]) ? m[1] : 'console';
  const arg = m && m[2] ? decodeURIComponent(m[2]) : '';
  showPage(name);
  const root = document.getElementById('page-' + name);
  if (!ready.has(name)) {
    ready.add(name);
    const args = [root, api, con, notice, providers];
    if (name === 'search') initSearch(...args);
    else if (name === 'playlists') initPlaylists(...args);
    else if (name === 'sync') initSync(...args);
    else if (name === 'account') initAccount(...args);
    else if (name === 'doctor') initDoctor(...args);
    else if (name === 'isrc') initISRC(root, api, arg);
  }
  if (name === 'console') { con.showIdle(providers.current); input.focus(); }
  else if (name === 'isrc') document.getElementById('isrc-input').focus();
}
window.addEventListener('hashchange', route);

// ── 鍵盤層:任何可編輯元素有焦點時單鍵全部失效,由一個集中的 inInput() 守門(設計規格 §11)──
function inInput() {
  const a = document.activeElement;
  if (!a) return false;
  if (a.isContentEditable) return true;
  return ['INPUT', 'TEXTAREA', 'SELECT'].includes(a.tagName);
}

const keysDialog = document.getElementById('keys');
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && inInput()) { document.activeElement.blur(); return; }
  if (inInput() || ev.metaKey || ev.ctrlKey || ev.altKey) return;
  const n = PAGES[Number(ev.key) - 1];
  if (n) { location.hash = '#/' + n; return; }
  switch (ev.key) {
    case '?': ev.preventDefault(); keysDialog.showModal(); break;
    case '/': ev.preventDefault(); input.focus(); break;
    case 'r': player.start(); break;
    case ' ': ev.preventDefault(); player.control(player.root.dataset.playing === 'true' ? 'pause' : 'play'); break;
    case 'n': player.control('next'); break;
    case 'p': player.control('prev'); break;
    default: break;
  }
});

const player = new Player(document.getElementById('now'), api, notice);
loadCommands()
  .then((d) => {
    if (d && d.default_provider) providers.current = d.default_provider;
    if (d && d.providers) providers.list = d.providers;
    if (!location.hash || location.hash === '#/console') con.showIdle(providers.current);
  })
  .catch((e) => notice('連不上 capy --web:' + e.message));
route();
player.start();
