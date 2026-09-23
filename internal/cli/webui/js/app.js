// app.js:token 引導、/api/commands、命令列;命令的串流與區塊在 console.js。
import { Console } from './console.js';
import { loadI18n, applyStatic, t } from './i18n.js';
import { languageMenu } from './lang.js';
import { Player } from './player.js';
import { initISRC } from './pages/isrc.js';
import { initSearch } from './pages/search.js';
import { initPlaylists } from './pages/playlists.js';
import { initSync } from './pages/sync.js';
import { initAccount } from './pages/account.js';
import { initDoctor } from './pages/doctor.js';
import { initMove } from './pages/move.js';

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
    history.replaceState(null, '', location.pathname + '#/move');
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
// ponytail: 兩個端點過同一道守門,一個失敗另一個也會失敗;只有目錄那一次失敗、下一次又連上的話頁面是空的,重新整理即可。
const i18nOK = await loadI18n(api);
applyStatic();
const con = new Console(document.getElementById('console'), api, notice);
languageMenu(document.getElementById('lang'), con);
const input = document.getElementById('cmd');
const runBtn = document.getElementById('run');
const busyHint = () => t('webui.shell.busy_hint');

// 不用 <form>:CSP form-action 'none' 與 submit 的互動零暴露;Enter 與按鈕都走 submit()。
// 執行中命令列照樣可以打字(設計規格 §5 / §10):Enter 只說明、不排隊、不並行,打好的那一行留著。
// 執行狀態列與中止鈕由 Console.run 管,頁面按鈕發起的命令也一樣。
async function submit() {
  if (con.running) { notice(busyHint()); return; }
  if (document.body.hasAttribute('data-stale')) { notice(t('webui.shell.stale')); return; }
  const line = input.value.trim();
  input.value = '';
  const [, , reason] = await con.run(line);
  if (reason === 'refused' && !input.value) input.value = line; // 根本沒跑(別的分頁佔著槽、403…):打好的那行還給使用者
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
document.addEventListener('capy:busy', () => notice(busyHint())); // 頁面按鈕在執行中被按(common.js btn)
window.addEventListener('beforeunload', (ev) => { if (con.running) { ev.preventDefault(); ev.returnValue = ''; } });
// ── 路由:八頁,#/<page>[/<arg>];每頁第一次到達時才初始化。順序 = 導覽的順序 = 鍵位 1–8;
// 預設落在搬家(決策 45),後三頁收在「進階」。
const providers = { list: ['spotify', 'apple', 'local'], current: 'spotify' };
const PAGES = ['move', 'playlists', 'sync', 'search', 'account', 'console', 'isrc', 'doctor'];
const ADVANCED = ['console', 'isrc', 'doctor'];
const ready = new Set();
let isrcPage = null;

function showPage(name) {
  document.body.dataset.page = name; // 命令列只在主控台頁(CSS 看這個屬性)
  for (const p of document.querySelectorAll('.page')) p.hidden = p.id !== 'page-' + name;
  for (const it of document.querySelectorAll('.rail__item')) it.classList.toggle('is-active', it.dataset.page === name);
  // 人在進階頁時那一段一定是打開的(active 項目與焦點不能落在收合區裡;review #66);離開就收起來。
  // 收起來之前,焦點若還在裡面就先搬到新的 active 項目:不然它會掉回 <body>,鍵盤使用者的位置就沒了(review #67)。
  const more = document.getElementById('rail-more');
  const open = ADVANCED.includes(name);
  if (!open && more.open && more.contains(document.activeElement)) document.querySelector(`.rail__item[data-page="${name}"]`)?.focus();
  more.open = open;
}

function route() {
  if (!i18nOK) return;
  // 結尾錨點:沒有的話 #/isrcfoo 也會被判成 isrc 頁(review #61)。
  const m = /^#\/([a-z]+)(?:\/([^/?#]+))?$/.exec(location.hash || '');
  const name = m && PAGES.includes(m[1]) ? m[1] : 'move';
  const arg = m && m[2] ? decodeURIComponent(m[2]) : '';
  showPage(name);
  const root = document.getElementById('page-' + name);
  if (!ready.has(name)) {
    ready.add(name);
    const args = [root, api, con, notice, providers];
    if (name === 'move') initMove(...args);
    else if (name === 'search') initSearch(...args);
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
  const prompted = con.focusPrompt(root); // 這一頁有開著的提示(主控台的區塊或精靈的就地提示)就把焦點給它
  if (name === 'console') { con.showIdle(providers.current); if (!prompted) input.focus(); }
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
  if (!i18nOK || inInput() || keysDialog.open || ev.metaKey || ev.ctrlKey || ev.altKey) return;
  // 按鈕有焦點時空白鍵就是「按下它」:在這裡搶走,整頁的按鈕都不能用空白鍵按了(review #62 第 8 點)。
  if (ev.key === ' ' && document.activeElement?.tagName === 'BUTTON') return;
  const n = PAGES[Number(ev.key) - 1];
  if (n) { location.hash = '#/' + n; return; }
  switch (ev.key) {
    case '?': ev.preventDefault(); keysDialog.showModal(); break;
    case '/': ev.preventDefault(); location.hash = '#/console'; input.focus(); break; // 命令列只在主控台頁
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

const player = i18nOK ? new Player(document.getElementById('now'), api, notice, con) : null; // 建構子就掛 visibilitychange → 輪詢
// 先拿到 providers 再路由:深連結或在某頁 F5 時,該頁的平台下拉才不會用寫死的預設值建起來
// (ready 保證每頁只初始化一次,建好之後不會補正)。連不上時照樣路由,頁面至少畫得出來。
loadCommands()
  .then((d) => {
    if (d && d.default_provider) providers.current = d.default_provider;
    if (d && d.providers) providers.list = d.providers;
  })
  .catch((e) => notice(i18nOK ? t('webui.shell.unreachable', { err: e.message }) : e.message)) // 沒有目錄時 t() 只會回 key
  .finally(() => {
    route();
    player?.start();
  });
