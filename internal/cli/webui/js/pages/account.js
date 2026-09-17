// account.js:#/account —— auth status 的三段文字解析成三列。登入一律打進主控台(Apple 的揭露是提示橋的 note,
// 不可收合);web 不自動擷取任何 token、不開瀏覽器抓 cookie、--auto 在伺服器端 403。
import { el, btn, emptyState, pageHead } from './common.js';

const PROVIDERS = [
  { id: 'spotify', label: 'spotify' },
  { id: 'google', label: 'google(Drive 同步)' },
  { id: 'apple', label: 'apple' },
];

// parseStatus:auth status 的輸出是「平台:」後面接縮排兩格的欄位行。不改 CLI 的純文字輸出,在這邊解析。
export function parseStatus(text) {
  const out = {};
  let cur = null;
  for (const raw of (text || '').split('\n')) {
    const line = raw.replace(/\s+$/, '');
    if (!line) continue;
    if (!line.startsWith('  ')) {
      cur = line.replace(/:$/, '').trim();
      out[cur] = [];
      continue;
    }
    if (cur) out[cur].push(line.trim());
  }
  return out;
}

// stateOf:三態由文字承載(✓ 已登入 / · 未登入 / ⚠ 已過期),不靠顏色單獨表意。
export function stateOf(lines) {
  const joined = (lines || []).join(' ');
  if (/過期/.test(joined)) return { mark: '⚠', text: '已過期', kind: 'warn' };
  if (/keychain 存在/.test(joined)) return { mark: '✓', text: '已登入', kind: 'ok' };
  return { mark: '·', text: '未登入', kind: 'muted' };
}

export function initAccount(root, api, con, notice) {
  pageHead(root, '帳號', 'auth status');
  const out = el('div', 'page__out');
  root.appendChild(out);

  const refresh = () => {
    let text = '';
    con.run('auth status', {
      onStdout: (t) => { text += t; },
      onExit: () => render(parseStatus(text)),
    });
  };

  function render(parsed) {
    out.replaceChildren();
    if (!Object.keys(parsed).length) {
      out.appendChild(emptyState('auth login spotify'));
      return;
    }
    for (const p of PROVIDERS) {
      const lines = parsed[p.id];
      const st = stateOf(lines);
      const row = el('div', 'acct');
      row.dataset.state = st.kind;
      row.appendChild(el('span', 'acct__name', p.label));
      row.appendChild(el('span', 'acct__state', `${st.mark} ${st.text}`));
      const detail = el('span', 'acct__detail', (lines || []).join(' · ') || '—');
      row.appendChild(detail);
      row.appendChild(btn(st.kind === 'ok' ? '重新登入' : '登入', 'btn--ghost', () => {
        con.run(`auth login ${p.id}`, { onExit: () => refresh() });
      }));
      out.appendChild(row);
    }
    const note = el('p', 'page__note',
      'Apple 的兩個 token 由你自己從網頁播放器複製;揭露頁在主控台的提示裡,不可跳過。capy 不會讀你的瀏覽器資料。');
    out.appendChild(note);
  }

  root.appendChild(btn('重新讀取', 'btn--ghost', refresh));
  refresh();
}
