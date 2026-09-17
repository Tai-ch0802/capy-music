// app.js:token 引導、/api/commands、命令列;命令的串流與區塊在 console.js。
import { Console } from './console.js';
import { Player } from './player.js';
import { initISRC } from './pages/isrc.js';

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
// 路由:只有兩頁(主控台 / ISRC),其餘 rail 項目在 T5 才開。
let isrcPage = null;
function showPage(name) {
  for (const p of document.querySelectorAll('.page')) p.hidden = p.id !== 'page-' + name;
  for (const it of document.querySelectorAll('.rail__item')) it.classList.toggle('is-active', it.dataset.page === name);
}
function route() {
  // 結尾錨點:沒有的話 #/isrcfoo 也會被判成 ISRC 頁。
  const m = /^#\/isrc(?:\/([^/?#]+))?$/.exec(location.hash || '');
  if (!m) { showPage('console'); input.focus(); return; }
  showPage('isrc');
  const want = m[1] ? decodeURIComponent(m[1]) : '';
  // 每次都把目前 hash 的值餵下去:上一頁 / 直接改網址列都要生效,不然網址寫 A、畫面是 B。
  // show() 只在與輸入框現值不同時才重查,所以 look() 自己設 hash 造成的那次 hashchange 不會打成迴圈。
  if (!isrcPage) isrcPage = initISRC(document.getElementById('page-isrc'), api, want);
  else if (want) isrcPage.show(want);
  document.getElementById('isrc-input').focus();
}
window.addEventListener('hashchange', route);

const player = new Player(document.getElementById('now'), api, notice);
loadCommands().catch((e) => notice('連不上 capy --web:' + e.message));
route();
player.start();
