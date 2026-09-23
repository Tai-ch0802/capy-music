// account.js:#/account —— auth status --json 的三段畫成三列。登入一律打進主控台(Apple 的揭露是提示橋的 note,
// 不可收合);web 不自動擷取任何 token、不開瀏覽器抓 cookie、--auto 在伺服器端 403。
import { el, btn, providerName, emptyState, pageHead } from './common.js';
import { t } from '../i18n.js';

// 函式、用到時才算:模組頂層不可以算使用者看得到的字(i18n.js 開頭的載入順序鐵則)。
const providerRows = () => [
  { id: 'spotify', label: providerName('spotify') },
  { id: 'apple', label: providerName('apple') },
  { id: 'google', label: t('webui.account.google_label', { name: providerName('google') }) },
];

// parseStatus:auth status --json 的輸出(README「登入狀態給腳本讀」;欄位只增不改、列舉值不翻譯)→ { spotify, google, apple }。
// 讀不懂就是 {}(頁面畫「讀不到帳號狀態」)。move.js 也用它:跑的命令要帶 --json。
export function parseStatus(text) {
  try {
    const d = JSON.parse(text || '');
    return d && typeof d === 'object' ? d : {};
  } catch (_) {
    return {};
  }
}

// stateOf:只看每家的 state(apple 的 state 已是兩個 token 合起來的結果:keychain_error > expired > missing > ok)。
// keychain_error 是要使用者處理的錯誤態,不是「未登入」(review #62)。
export function stateOf(id, st) {
  switch (st?.state) {
    case 'keychain_error': return { mark: '⚠', text: t('webui.account.state.keychain_error'), kind: 'warn' };
    case 'expired': return { mark: '⚠', text: t('webui.account.state.expired'), kind: 'warn' };
    case 'ok': return { mark: '✓', text: t('webui.account.state.ok'), kind: 'ok' };
  }
  return { mark: '·', text: t('webui.account.state.missing'), kind: 'muted' };
}

// detail:state 以外的欄位原樣列出。欄名與列舉值是機器欄位,不翻譯;裡面絕不含 token 值(auth_status_json_test.go)。
const detail = (st) => Object.entries(st || {}).filter(([k]) => k !== 'state').map(([k, v]) => `${k}: ${v}`).join(' · ') || '—';

export function initAccount(root, api, con, notice) {
  pageHead(root, t('webui.account.title'), t('webui.account.lead'));
  const out = el('div', 'page__out');
  root.appendChild(out);

  const refresh = () => {
    let text = '';
    con.run('auth status --json', {
      onStdout: (s) => { text += s; },
      onExit: (code, msg) => { // refused(-1)也會到這裡,否則 409 之後這一頁永遠空白
        render(parseStatus(text));
        if (code !== 0 && !text) notice(msg || '');
      },
    }, { label: t('webui.account.checking') });
  };

  function render(parsed) {
    out.replaceChildren();
    if (!Object.keys(parsed).length) {
      out.appendChild(emptyState(t('webui.account.unreadable')));
      return;
    }
    for (const p of providerRows()) {
      const st = stateOf(p.id, parsed[p.id]);
      const row = el('div', 'acct');
      row.dataset.state = st.kind;
      row.appendChild(el('span', 'acct__name', p.label));
      row.appendChild(el('span', 'acct__state', `${st.mark} ${st.text}`));
      row.appendChild(el('span', 'acct__detail', detail(parsed[p.id])));
      const ok = st.kind === 'ok';
      row.appendChild(btn(ok ? t('webui.account.reconnect') : t('webui.account.connect'), ok ? 'btn--ghost' : '', () => {
        con.run(`auth login ${p.id}`, { onExit: () => refresh() }, { label: t('webui.account.connecting', { name: providerName(p.id) }) });
      }));
      out.appendChild(row);
    }
    out.appendChild(el('p', 'page__note', t('webui.account.apple_note')));
  }

  root.appendChild(btn(t('webui.account.refresh'), 'btn--ghost', refresh));
  con.idle(refresh); // 第一次進來時若有命令在跑,等它結束再讀,不要撞上它、畫成「未登入」
}
