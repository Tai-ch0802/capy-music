// app.js:token 引導、/api/commands、命令列;命令的串流與區塊在 console.js。
import { Console } from './console.js';

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
input.addEventListener('keydown', (ev) => { if (ev.key === 'Enter') { ev.preventDefault(); submit(); } });
runBtn.addEventListener('click', submit);
cancelBtn.addEventListener('click', () => con.cancel());
window.addEventListener('beforeunload', (ev) => { if (con.running) { ev.preventDefault(); ev.returnValue = ''; } });
loadCommands().catch((e) => notice('連不上 capy --web:' + e.message));
input.focus();
