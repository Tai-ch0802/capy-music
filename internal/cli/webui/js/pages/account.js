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

// SPOTIFY_RENEW_WARN_DAYS:剩這麼多天以內就叫人重新連接(= auth.SpotifyRenewWarn,auth_status_json_test.go 釘住)。
const SPOTIFY_RENEW_WARN_DAYS = 10;
const DAY = 24 * 60 * 60 * 1000;

// spotifyRenewal:Spotify 的登入約何時失效(決策 56:refresh token 從登入起六個月失效、refresh 不延長,capy 從上一次登入算 180 天)
// → { text, warn };沒登入(state 不是 ok)回 null。說法與門檻同 auth status 的文字版:剩的時間在 SPOTIFY_RENEW_WARN_DAYS 天以內
// 就提醒(比的是時間長度,不是天數),過了說大概已失效;天數無條件進位(剩 10 天又 1 小時 = 剩 11 天、不提醒)。
// 沒有 refresh_token_expiry = 不知道(這個版本之前登入的)。日期用這台電腦的時區、只到日。
export function spotifyRenewal(st, now = Date.now()) {
  if (st?.state !== 'ok') return null;
  const button = t('webui.account.reconnect');
  const exp = new Date(st.refresh_token_expiry || '').getTime();
  if (Number.isNaN(exp)) return { text: t('webui.account.detail.spotify_renew_unknown', { button }), warn: false };
  const left = exp - now;
  const date = localTime(st.refresh_token_expiry).slice(0, 10);
  if (left <= 0) return { text: t('webui.account.detail.spotify_renew_expired', { date, button }), warn: true };
  const count = Math.ceil(left / DAY);
  if (left <= SPOTIFY_RENEW_WARN_DAYS * DAY) return { text: t('webui.account.detail.spotify_renew_soon', { date, count, button }), warn: true };
  return { text: t('webui.account.detail.spotify_renew_by', { date, count }), warn: false };
}

// details:auth status --json 的事實 → 給人看、跟著語系的句子;沒有的事實不畫。JSON 的欄名與列舉值是給腳本的,不上畫面;
// 也沒有任何 token 值可畫(--json 本來就沒有,auth_status_json_test.go)。Google 的 client 來源、access token 到期
// (會自動換發)與 device_id 對使用者沒有意義,不列。
// 不用 switch:TestWebAccountPageKeysOnAuthStatusJSON 把這個檔的每個 case '…' 都當成 state 的值核對。
const details = {
  spotify: (st) => [
    spotifyRenewal(st)?.text,
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
      let st = stateOf(p.id, parsed[p.id]);
      // 快到期或大概已失效:那一列標成要處理(stateOf 不動:搬家精靈的平台卡片也用它,那裡只管連上了沒有)
      if (p.id === 'spotify' && spotifyRenewal(parsed.spotify)?.warn) st = { ...st, mark: '⚠', kind: 'warn' };
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
