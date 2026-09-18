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
const BUSY_HINT = '正在執行別的命令:等它結束,或按「中止」(命令列有焦點時 Ctrl-C)';

// 不用 <form>:CSP form-action 'none' 與 submit 的互動零暴露;Enter 與按鈕都走 submit()。
// 執行中命令列照樣可以打字(設計規格 §5 / §10):Enter 只說明、不排隊、不並行,打好的那一行留著。
// 執行狀態列與中止鈕由 Console.run 管,頁面按鈕發起的命令也一樣。
async function submit() {
  if (con.running) { notice(BUSY_HINT); return; }
  if (document.body.hasAttribute('data-stale')) { notice('binary 已更新,這個 capy --web 仍是舊版,請重啟'); return; }
  const line = input.value.trim();
  input.value = '';
  await con.run(line);
  input.focus();
}
// 組字中的 Enter 是「確認候選字」,不是「送出」:注音 / 拼音使用者按的第一個 Enter 會被輸入法吃掉。
// Safari 先送 compositionend 再送這個 keydown(isComposing 已是 false),所以還要看 keyCode 229。
input.addEventListener('keydown', (ev) => {
  // Ctrl-C = 中止(設計規格 §10,同終端機)。只在有命令在跑、輸入框沒有選取文字時攔:Windows 的 Ctrl-C 是複製。
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
document.addEventListener('capy:busy', () => notice(BUSY_HINT)); // 頁面按鈕在執行中被按(common.js btn)
window.addEventListener('beforeunload', (ev) => { if (con.running) { ev.preventDefault(); ev.returnValue = ''; } });
// ── 路由:七頁,#/<page>[/<arg>];每頁第一次到達時才初始化 ──
const providers = { list: ['spotify', 'apple', 'local'], current: 'spotify' };
const PAGES = ['console', 'search', 'playlists', 'sync', 'isrc', 'account', 'doctor'];
const ready = new Set();
let isrcPage = null;

function showPage(name) {
  for (const p of document.querySelectorAll('.page')) p.hidden = p.id !== 'page-' + name;
  for (const it of document.querySelectorAll('.rail__item')) it.classList.toggle('is-active', it.dataset.page === name);
}

function route() {
  // 結尾錨點:沒有的話 #/isrcfoo 也會被判成 isrc 頁(review #61)。
  const m = /^#\/([a-z]+)(?:\/([^/?#]+))?$/.exec(location.hash || '');
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
    else if (name === 'isrc') isrcPage = initISRC(root, api, arg);
  } else if (name === 'isrc' && arg) {
    // 每次都餵目前 hash 的值:上一頁 / 直接改網址列都要生效,不然網址寫 A、畫面是 B(review #61)。
    // show() 只在與輸入框現值不同時才重查,所以 look() 自己設 hash 造成的那次 hashchange 不會打成迴圈。
    isrcPage?.show(arg);
  }
  if (name === 'console') { con.showIdle(providers.current); if (!con.focusPrompt()) input.focus(); }
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
  // 鍵位表開著時 activeElement 是裡面的 <button>,inInput() 擋不到:1–7 會在背後換頁(review #62)。
  if (inInput() || keysDialog.open || ev.metaKey || ev.ctrlKey || ev.altKey) return;
  // 按鈕有焦點時空白鍵就是「按下它」:在這裡搶走,整頁的按鈕都不能用空白鍵按了(review #62 第 8 點)。
  if (ev.key === ' ' && document.activeElement?.tagName === 'BUTTON') return;
  const n = PAGES[Number(ev.key) - 1];
  if (n) { location.hash = '#/' + n; return; }
  switch (ev.key) {
    case '?': ev.preventDefault(); keysDialog.showModal(); break;
    case '/': ev.preventDefault(); input.focus(); break;
    case 'r': player.start(); break;
    case ' ': ev.preventDefault(); player.control(player.root.dataset.playing === 'true' ? 'pause' : 'play'); break;
    case 'n': player.control('next'); break;
    case 'p': player.control('prev'); break;
    case 'ArrowLeft': ev.preventDefault(); player.seekBy(-10); break;
    case 'ArrowRight': ev.preventDefault(); player.seekBy(10); break;
    case '+': case '=': player.volBy(5); break;
    case '-': player.volBy(-5); break;
    default: break;
  }
});

const player = new Player(document.getElementById('now'), api, notice, () => con.running);
// 先拿到 providers 再路由:深連結或在某頁 F5 時,該頁的平台下拉才不會用寫死的預設值建起來
// (ready 保證每頁只初始化一次,建好之後不會補正)。連不上時照樣路由,頁面至少畫得出來。
loadCommands()
  .then((d) => {
    if (d && d.default_provider) providers.current = d.default_provider;
    if (d && d.providers) providers.list = d.providers;
  })
  .catch((e) => notice('連不上 capy --web:' + e.message))
  .finally(() => {
    route();
    player.start();
  });
