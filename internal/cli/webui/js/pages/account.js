// account.js:#/account —— auth status 的三段文字解析成三列。登入一律打進主控台(Apple 的揭露是提示橋的 note,
// 不可收合);web 不自動擷取任何 token、不開瀏覽器抓 cookie、--auto 在伺服器端 403。
import { el, btn, providerName, emptyState, pageHead } from './common.js';

const PROVIDERS = [
  { id: 'spotify', label: providerName('spotify') },
  { id: 'apple', label: providerName('apple') },
  { id: 'google', label: `${providerName('google')}(保管你的清單)` },
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

// stateOf:逐個 provider 認它自己的關鍵欄位,不要把整段 join 起來做子字串比對。
// auth status 的三段用的是不同字眼(auth.go / auth_google.go):
//   spotify  refresh token: keychain 存在 / 不存在(…)
//   google   token: keychain 存在(…) / 不存在(…)
//   apple    developer token: 有效至 … | 已於 … 過期 | 不存在(…);user token: 存在 | 不存在(…)
// 「讀取 keychain 失敗」三家都可能印,那是要使用者處理的錯誤態,不是「未登入」(review #62)。
export function stateOf(id, lines) {
  const j = (lines || []).join('\n');
  if (/讀取 keychain 失敗/.test(j)) return { mark: '⚠', text: '讀取 keychain 失敗', kind: 'warn' };
  if (id === 'apple') {
    if (/developer token: 已於 .* 過期/.test(j)) return { mark: '⚠', text: '已過期', kind: 'warn' };
    if (/developer token: 有效至/.test(j) && /user token: 存在/.test(j)) return { mark: '✓', text: '已登入', kind: 'ok' };
    return { mark: '·', text: '未登入', kind: 'muted' };
  }
  if (id === 'google') {
    return /token: keychain 存在/.test(j)
      ? { mark: '✓', text: '已登入', kind: 'ok' }
      : { mark: '·', text: '未登入', kind: 'muted' };
  }
  return /refresh token: keychain 存在/.test(j)
    ? { mark: '✓', text: '已登入', kind: 'ok' }
    : { mark: '·', text: '未登入', kind: 'muted' };
}

export function initAccount(root, api, con, notice) {
  pageHead(root, '帳號', '連接你自己的帳號。登入資料只存在這台電腦的鑰匙圈裡,capy 沒有伺服器可以存它。');
  const out = el('div', 'page__out');
  root.appendChild(out);

  const refresh = () => {
    let text = '';
    con.run('auth status', {
      onStdout: (t) => { text += t; },
      onExit: (code, msg) => { // refused(-1)也會到這裡,否則 409 之後這一頁永遠空白
        render(parseStatus(text));
        if (code !== 0 && !text) notice(msg || '');
      },
    }, { label: '檢查帳號的連接狀態' });
  };

  function render(parsed) {
    out.replaceChildren();
    if (!Object.keys(parsed).length) {
      out.appendChild(emptyState('讀不到帳號狀態。按「重新整理」再試一次。'));
      return;
    }
    for (const p of PROVIDERS) {
      const lines = parsed[p.id];
      const st = stateOf(p.id, lines);
      const row = el('div', 'acct');
      row.dataset.state = st.kind;
      row.appendChild(el('span', 'acct__name', p.label));
      row.appendChild(el('span', 'acct__state', `${st.mark} ${st.text}`));
      const detail = el('span', 'acct__detail', (lines || []).join(' · ') || '—');
      row.appendChild(detail);
      row.appendChild(btn(st.kind === 'ok' ? '重新連接' : '連接', st.kind === 'ok' ? 'btn--ghost' : '', () => {
        con.run(`auth login ${p.id}`, { onExit: () => refresh() }, { label: `連接 ${providerName(p.id)}` });
      }));
      out.appendChild(row);
    }
    const note = el('p', 'page__note',
      'Apple 的兩個 token 由你自己從網頁播放器複製;揭露頁在主控台的提示裡,不可跳過。capy 不會讀你的瀏覽器資料。');
    out.appendChild(note);
  }

  root.appendChild(btn('重新整理', 'btn--ghost', refresh));
  con.idle(refresh); // 第一次進來時若有命令在跑,等它結束再讀,不要撞上它、畫成「未登入」
}
