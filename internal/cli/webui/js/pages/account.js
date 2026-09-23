// account.js:#/account —— auth status --json 的三段畫成三列。登入一律打進主控台(Apple 的揭露是提示橋的 note,
// 不可收合);web 不自動擷取任何 token、不開瀏覽器抓 cookie、--auto 在伺服器端 403。
import { el, btn, providerName, emptyState, pageHead } from './common.js';
import { countryLabel } from './isrc.js';
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

// localTime:到期時間換成這台電腦的時區,寫成 YYYY-MM-DD HH:MM。不用 toLocaleString:它的格式跟著瀏覽器與 ICU 版本變。
function localTime(iso) {
  const d = new Date(iso || '');
  if (Number.isNaN(d.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// details:auth status --json 的事實 → 給人看、跟著語系的句子;沒有的事實不畫。JSON 的欄名與列舉值是給腳本的,不上畫面;
// 也沒有任何 token 值可畫(--json 本來就沒有,auth_status_json_test.go)。Google 的 client 來源、access token 到期
// (會自動換發)與 device_id 對使用者沒有意義,不列。
// 不用 switch:TestWebAccountPageKeysOnAuthStatusJSON 把這個檔的每個 case '…' 都當成 state 的值核對。
const details = {
  spotify: (st) => [
    st.client_id === 'set' && t('webui.account.detail.client_id_set'),
    st.client_id === 'malformed' && t('webui.account.detail.client_id_malformed'),
  ],
  google: (st) => [st.email],
  apple: (st) => {
    const when = localTime(st.developer_token_expiry);
    return [
      when && (st.developer_token === 'expired' ? t('webui.account.detail.dev_token_expired', { when }) : t('webui.account.detail.dev_token_valid', { when })),
      st.storefront && t('webui.account.detail.storefront', { region: countryLabel(st.storefront.toUpperCase(), true) }),
    ];
  },
};
const detail = (id, st) => details[id](st || {}).filter(Boolean).join(' · ') || '—';

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
      row.appendChild(el('span', 'acct__detail', detail(p.id, parsed[p.id])));
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
