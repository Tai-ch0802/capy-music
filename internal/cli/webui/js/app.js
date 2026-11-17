// app.js:token 引導、Smart Landing(/api/commands 的 has_local_db)、命令列與快捷鍵層;命令的串流與區塊在 console.js。
import { Console } from './console.js';
import { loadI18n, applyStatic, t } from './i18n.js';
import { languageMenu } from './lang.js';
import { Player } from './player.js';
import { createCollector } from './collect.js';
import { createTheater } from './theater.js';
import { initISRC } from './pages/isrc.js';
import { initSearch } from './pages/search.js';
import { initPlaylists } from './pages/playlists.js';
import { initSync } from './pages/sync.js';
import { initAccount } from './pages/account.js';
import { initDoctor } from './pages/doctor.js';
import { initMove } from './pages/move.js';
import { initWiki } from './pages/wiki.js';

export const api = {
  token: '',
  async fetch(path, init = {}) {
    const headers = new Headers(init.headers || {});
    headers.set('X-Capy-Token', api.token);
    return fetch(path, { ...init, headers });
  },
};

let hasExplicitRoute = false;
let defaultPage = 'move';

function bootToken() {
  const raw = location.hash || '';
  const routeMatch = /^#\/([a-z]+(?:\/[^&#?]*)?)/.exec(raw);
  if (routeMatch) hasExplicitRoute = true;
  const m = /[#&]t=([A-Za-z0-9_-]+)/.exec(raw);
  if (m) {
    try { sessionStorage.setItem('capy.token', m[1]); } catch (_) { /* 私密視窗:留在記憶體 */ }
    api.token = m[1];
    const target = routeMatch ? `#/${routeMatch[1]}` : '#/move';
    history.replaceState(null, '', location.pathname + target);
    return;
  }
  try { api.token = sessionStorage.getItem('capy.token') || ''; } catch (_) { api.token = ''; }
}

async function loadCommands() {
  const r = await api.fetch('/api/commands');
  // /api/i18n 也要 token:token 不對時目錄讀不到(t() 只會回 key 本身)。401 的回應本身就帶著伺服器語系的那一句
  // (api() 的 web.err.bad_token),照印——跟 console.js 的 refused() 一樣;t() 只是回應不是 JSON 時的退路。
  if (r.status === 401) {
    let msg = '';
    try { msg = (await r.json()).error || ''; } catch (_) { /* 非 JSON */ }
    notice(msg || t('web.err.bad_token'));
    return;
  }
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
// 目錄要在任何畫面算字之前到(i18n.js 開頭的載入順序鐵則)。讀不到(token 不對 / 過期、伺服器不在)時 t() 只會回 key 本身:
// 頁面、播放列與鍵盤層一個都不起(route()、底下的 player、keydown 都看 i18nOK),畫面上只有 notice 說原因——
// loadCommands 印 401 回應裡伺服器語系的那一句,連不上時印瀏覽器的錯誤。
const i18nOK = await loadI18n(api);
applyStatic();
const con = new Console(document.getElementById('console'), api, notice);
languageMenu(document.getElementById('lang'), con);
const input = document.getElementById('cmd');
const runBtn = document.getElementById('run');
const busyHint = () => t('webui.shell.busy_hint');

// 不用 <form>:CSP form-action 'none' 與 submit 的互動零暴露;Enter 與按鈕都走 submit()。
async function submit() {
  if (con.running) { notice(busyHint()); return; }
  if (document.body.hasAttribute('data-stale')) { notice(t('webui.shell.stale')); return; }
  const line = input.value.trim();
  input.value = '';
  const [, , reason] = await con.run(line);
  if (reason === 'refused' && !input.value) input.value = line; // 根本沒跑(別的分頁佔著槽、403…):打好的那行還給使用者
  input.focus();
}

input.addEventListener('keydown', (ev) => {
  if (ev.ctrlKey && !ev.metaKey && !ev.altKey && ev.key.toLowerCase() === 'c' && con.running &&
      input.selectionStart === input.selectionEnd) {
    ev.preventDefault();
    con.stop();
    return;
  }
  if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
  ev.preventDefault();
  submit();
});
runBtn.addEventListener('click', submit);
document.addEventListener('capy:busy', () => notice(busyHint()));
window.addEventListener('beforeunload', (ev) => { if (con.running) { ev.preventDefault(); ev.returnValue = ''; } });

// ── 路由:九頁,#/<page>[/<arg>];無指定路由時依 has_local_db 決定 Smart Landing(#/playlists 或新戶 #/move) ──
const providers = { list: ['spotify', 'apple', 'local', 'youtube'], current: 'spotify' };
const PAGES = ['move', 'playlists', 'sync', 'search', 'wiki', 'account', 'console', 'isrc', 'doctor'];
const ADVANCED = ['console', 'isrc', 'doctor'];
const ready = new Set();
let isrcPage = null;
let playlistsPage = null;
let searchPage = null;

function showPage(name) {
  document.body.dataset.page = name;
  for (const p of document.querySelectorAll('.page')) p.hidden = p.id !== 'page-' + name;
  for (const d of document.querySelectorAll('.page[hidden] dialog[open]')) d.close();
  for (const it of document.querySelectorAll('.rail__item')) it.classList.toggle('is-active', it.dataset.page === name);
  const more = document.getElementById('rail-more');
  const open = ADVANCED.includes(name);
  if (!open && more.open && more.contains(document.activeElement)) document.querySelector(`.rail__item[data-page="${name}"]`)?.focus();
  more.open = open;
}

function route() {
  if (!i18nOK) return;
  const m = /^#\/([a-z]+)(?:\/([^/?#]+))?$/.exec(location.hash || '');
  const name = m && PAGES.includes(m[1]) ? m[1] : defaultPage;
  const arg = m && m[2] ? decodeURIComponent(m[2]) : '';
  showPage(name);
  const root = document.getElementById('page-' + name);
  if (!ready.has(name)) {
    ready.add(name);
    const args = [root, api, con, notice, providers];
    if (name === 'move') initMove(...args);
    else if (name === 'search') searchPage = initSearch(...args);
    else if (name === 'playlists') playlistsPage = initPlaylists(...args);
    else if (name === 'sync') initSync(...args);
    else if (name === 'wiki') initWiki(...args, player);
    else if (name === 'account') initAccount(...args);
    else if (name === 'doctor') initDoctor(...args);
    else if (name === 'isrc') isrcPage = initISRC(root, api, arg);
  } else if (name === 'isrc' && arg) {
    isrcPage?.show(arg);
  } else if (name === 'playlists') {
    playlistsPage?.refresh();
  } else if (name === 'search') {
    searchPage?.refresh?.();
  }
  const prompted = con.focusPrompt(root);
  if (name === 'console') { con.showIdle(providers.current); if (!prompted) input.focus(); }
  else if (name === 'isrc') document.getElementById('isrc-input').focus();
}
window.addEventListener('hashchange', route);

function inInput() {
  const a = document.activeElement;
  if (!a) return false;
  if (a.isContentEditable) return true;
  return ['INPUT', 'TEXTAREA', 'SELECT'].includes(a.tagName);
}

const player = i18nOK ? new Player(document.getElementById('now'), api, notice, con) : null;
if (i18nOK) createCollector(document.getElementById('collect'), api, con, notice);
const theater = i18nOK ? createTheater(document.getElementById('theater'), player, api, con, notice) : null;
if (player && theater) player.onOpenTheater = () => theater.open();

const keysDialog = document.getElementById('keys');
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && inInput()) { document.activeElement.blur(); return; }
  if (!i18nOK || inInput() || ev.metaKey || ev.ctrlKey || ev.altKey) return;
  const openDlg = document.querySelector('dialog[open]');
  // 任何非劇場對話框(鍵位表、一鍵收藏、清單頁歌曲 wiki)開著時單鍵全部失效;劇場開啟時放行播放單鍵與 z(#9 §2.3.3)。
  if (openDlg && openDlg.id !== 'theater') return;
  if (ev.key === ' ' && ['BUTTON', 'SUMMARY'].includes(document.activeElement?.tagName)) return;

  if (!openDlg) {
    const n = PAGES[Number(ev.key) - 1];
    if (n) { location.hash = '#/' + n; return; }
    switch (ev.key) {
      case '?': ev.preventDefault(); keysDialog.showModal(); return;
      case '/': ev.preventDefault(); location.hash = '#/console'; input.focus(); break;
      case 's': {
        ev.preventDefault();
        if (document.body.dataset.page === 'playlists') {
          const pf = document.getElementById('page-playlists').querySelector('.pl__filter input');
          if (pf) { pf.focus(); return; }
        }
        location.hash = '#/search';
        searchPage?.focusSearch?.();
        return;
      }
      case 'a': ev.preventDefault(); player?.openCollectNow(); return;
      default: break;
    }
    if (ev.key === '/') return;
  }

  switch (ev.key) {
    case 'z': ev.preventDefault(); theater?.toggle(); break;
    case 'r': player?.start(); break;
    case ' ': ev.preventDefault(); player?.togglePlay(); break;
    case 'n': player?.act({ action: 'next' }); break;
    case 'p': player?.act({ action: 'prev' }); break;
    case 'ArrowLeft': ev.preventDefault(); player?.seekBy(-10); break;
    case 'ArrowRight': ev.preventDefault(); player?.seekBy(10); break;
    case '+': case '=': player?.volBy(5); break;
    case '-': player?.volBy(-5); break;
    case 'm': player?.toggleMute(); break;
    default: break;
  }
});

loadCommands()
  .then((d) => {
    if (d && d.default_provider) providers.current = d.default_provider;
    if (d && d.providers) providers.list = d.providers;
    if (d && d.has_local_db) {
      defaultPage = 'playlists';
      if (!hasExplicitRoute) history.replaceState(null, '', location.pathname + '#/playlists');
    }
  })
  .catch((e) => notice(i18nOK ? t('webui.shell.unreachable', { err: e.message }) : e.message))
  .finally(() => {
    route();
    player?.start();
    if (!i18nOK && !document.getElementById('notice').textContent) notice("capy --web couldn't load the interface texts; reload the page");
  });
