// plflow.js:「我的清單」與「同步」兩頁共用的寫入流程、連結面板與狀態判斷(計畫 docs/superpowers/plans/2026-10-01-sync-page-redesign.md
// §3.6:搬過來共用,不是複製一份)。寫入一律走 CLI 自己的變更表與確認,頁面絕不代加確認或越過的旗標(決策 46)。
// 會變的狀態(auth、devices、各平台上的清單)由頁面放在一個 state 物件裡,這裡每次用到時才讀,不在建立時抓一份。
import { el, btn, input, providerName } from './common.js';
import { renderTable, linkColumn, byProvider, spotifyLink } from '../table.js';
import { t } from '../i18n.js';

export const COLUMNS = ['spotify', 'apple', 'youtube']; // 會佔一欄的平台:本機沒有播放、也沒有網頁,不佔欄;也是找得到對應、建得出清單的平台

// same:兩個清單名算不算同名(不分大小寫,同 CLI 的 canonState.find 與 sameNamePlaylists 的 EqualFold)。
export const same = (a, b) => String(a || '').toLowerCase() === String(b || '').toLowerCase();

// mappingOf:一首歌在一個平台上的對應 { id, pinned }(不是字串的壞資料 = 沒有)。格子(§3.2)與連結面板的計數(§3.5)共用。
export const norm = (m) => ({ id: m && typeof m.id === 'string' ? m.id : '', pinned: !!(m && m.pinned) });
export const mappingOf = (trk, p) => norm((trk.mappings || {})[p]);

// missingOn:缺對應 = 沒有 id、也不是你標過「這個平台沒有這首」(那種 resolve 幫不了,不該叫人去找)。
export const missingOn = (trk, p) => {
  const m = mappingOf(trk, p);
  return !m.id && !m.pinned;
};

// links:真的連著的 [平台, id](空字串 = 沒連結,同 pull.go / resolve 的規則;不是字串的壞資料也不算)。
export function links(pl) {
  return Object.entries(pl.links || {}).filter(([, id]) => typeof id === 'string' && id);
}

// driveState:Google Drive 本身的狀態。auth status 的 google.state 是 ok | missing | keychain_error;keychain_error 不是「沒連」,
// 是要到帳號頁處理的錯誤(同帳號頁,review #62;#122 review 第 2 點)。讀不到 auth = 不知道,當成 ok。
export function driveState(auth) {
  const st = auth.google && auth.google.state;
  return !st || st === 'ok' ? 'ok' : st;
}

// loggedOut:這台電腦沒登入那個平台(missing / expired,或 YouTube 要重新登入)。auth 讀不到 = 當成有登入。
export function loggedOut(auth, p) {
  const st = auth[p] && auth[p].state;
  return st === 'missing' || st === 'expired' || relogin(auth, p);
}
// relogin:YouTube 有登入卻沒有 channel_id(舊版登入):分不出清單是不是這個帳號的(CLI 也會當成別的帳號而跳過),
// 請人重新登入一次(#124 review)。
export function relogin(auth, p) {
  return p === 'youtube' && auth.youtube?.state === 'ok' && !auth.youtube.channel_id;
}

// linkState:這份正本在一個平台上的狀態。判斷順序:這台有沒有登入 → 是不是別台電腦 / 別的帳號的 → 才是自己的
//(auth 讀不到 = 當成有登入、是自己的,同 ▶)。沒連的本機不顯示:本機不能 --create,連既有的 M3U 會碰到順序問題。
export function linkState(auth, devices, p, pl) {
  const id = (pl.links || {})[p];
  const out = loggedOut(auth, p);
  const re = relogin(auth, p);
  if (typeof id !== 'string' || !id) {
    if (!COLUMNS.includes(p)) return null;
    return { kind: out ? 'unlinked_out' : 'unlinked', relogin: re };
  }
  if (out) return { kind: 'out', id, relogin: re };
  const owner = id.split('/')[0]; // local:<device_id>/<檔名>;youtube:<channel_id>/<playlistId>
  const dev = auth.google && auth.google.device_id;
  if (p === 'local' && dev && owner !== dev) {
    const d = devices.find((x) => x.id === owner);
    return { kind: 'foreign', id, device: (d && d.name) || owner };
  }
  const ch = auth.youtube && auth.youtube.channel_id;
  if (p === 'youtube' && ch && owner !== ch) return { kind: 'foreign', id };
  return { kind: 'linked', id };
}

// listState:一份平台清單跟正本的關係(計畫 §3.2)。只是提示,真正的護欄在 CLI(--new-only 以名稱命中正本就擋)。
// masters 是 null = 這台還沒讀到正本(export 失敗),分不出來。回 { kind, master }:
// unknown / linked(連著那一份)/ same_name(不分大小寫同名、那份正本在這個平台沒連)/ same_name_taken(同名、那份正本已連著這個平台的另一份)/ unlinked。
export function listState(masters, p, r) {
  if (!masters) return { kind: 'unknown' };
  const linked = masters.find((m) => (m.links || {})[p] === r.id);
  if (linked) return { kind: 'linked', master: linked };
  const dup = masters.find((m) => same(m.name, r.name));
  if (!dup) return { kind: 'unlinked' };
  const other = (dup.links || {})[p];
  return { kind: typeof other === 'string' && other ? 'same_name_taken' : 'same_name', master: dup };
}

// outcome:寫入命令的收尾(計畫 §3.5 的表;照 move.js 的做法用 onPromptClosed 分辨關掉 / 逾時)。wrote 見 step();
// pending:resolve 沒有自動寫入、只剩等人決定的列時,指到頁面上的「逐首決定」(CLI 那句叫人去終端機,這裡看不到)。
export function outcome(code, msg, reason, closedBy, wrote, pending) {
  const why = (msg || '').replace(/^Error: /, '');
  if (reason === 'cancelled' && code !== 0) return { text: t('webui.playlists.flow.stopped') };
  if (code === 0 && wrote) return { text: t('webui.playlists.flow.done') };
  if (code === 0 && pending) return { text: t('webui.playlists.flow.review_left', { count: pending, button: t('webui.playlists.links.review') }) };
  if (code === 0) return { text: t('webui.playlists.flow.up_to_date') };
  if (code === 2) return { text: t('webui.playlists.flow.cancelled') };
  if (code === 1 && closedBy === 'dismissed') return { text: t('webui.playlists.flow.dismissed') };
  if (code === 1 && closedBy === 'timeout') return { text: t('webui.playlists.flow.timeout') };
  if (code === 3) return { text: why, extra: t('webui.playlists.flow.blocked'), warn: true }; // exit 3 的原文各自帶了出路,不暗示一定是 --force
  return { text: why || t('webui.move.failed'), warn: true, console: code > 0 };
}

export function cell(cls, text) {
  const td = el('td', cls, text);
  td.setAttribute('role', 'cell');
  return td;
}

// filterBox:篩選框,不分大小寫比對;extra 是放在框旁邊的東西(顯示了幾首);initial:重畫時帶回來的篩選字。
export function filterBox(label, onFilter, extra, initial) {
  const bar = el('div', 'form-row pl__filter');
  const f = input('sans', label);
  f.type = 'search';
  f.setAttribute('aria-label', label);
  f.addEventListener('input', () => onFilter(f.value.trim().toLowerCase()));
  bar.appendChild(f);
  if (extra) bar.appendChild(extra);
  if (initial) { f.value = initial; onFilter(initial.trim().toLowerCase()); }
  return bar;
}

// changeTable:變更表(計畫 2026-10-01 §3.7)。CLI 的表一欄一欄都是機器欄位(英文的 action / dir、cid、reason_code),
// 這裡先畫一張給人看的精簡表:「動作 / 歌曲 / 說明」(表裡不只一份清單時前面多一欄「清單」),動作依 DIR + ACTION 翻成白話,
// 說明是 CLI 已經照語系翻好的 REASON;完整的原表收在「看完整表格」裡,打開才畫(合併時一張表可能上千列)。
// 列與動作那一格保留機器值(data-action / data-dir):上色看它,拿掉那一列另外有「拿掉」兩個字,不靠顏色單獨表意。
// 去重報告(pl dedup <平台>:<清單>,沒有 ACTION)畫成「# / 歌曲 / 說明」,# 照 CLI 的 POS(說明裡的 pos 也是同一個數)。
// resolve 的表(有 CONFIDENCE)照舊畫原表。opts.dir:沒有 DIR 欄的表(pl pull / pl push)由呼叫端說是哪個方向;
// opts.spotifyReport:Spotify 清單的去重報告,沒有 PROVIDER 欄、整張都是 Spotify 的。
export function changeTable(header, rows, opts = {}) {
  const ai = header.indexOf('ACTION');
  const report = ai < 0 && header.includes('POS') && header.includes('REASON');
  const pick = byProvider(header) || (opts.spotifyReport && ((r) => ({ kind: 'track', id: r[header.indexOf('ID')], title: r[header.indexOf('TITLE')] })));
  // skip(pull / push)與 review / conflict(resolve:等人決定、這次不寫)都不算變更;resolve 的表另一句,數字要跟確認句的筆數一樣
  const idle = new Set(['skip', 'review', 'conflict']);
  const n = rows.filter((r) => ai < 0 || !idle.has(r[ai])).length;
  const note = el('p', 'page__note', header.includes('CONFIDENCE') ? t('webui.sync.resolve_changes', { count: n }) : t('webui.sync.changes', { count: n }));
  if (header.includes('CONFIDENCE') || (ai < 0 && !report)) {
    const wrap = fullTable(header, rows, pick);
    wrap.appendChild(note);
    return wrap;
  }
  const box = el('div', 'chg');
  const wrap = el('div', 'tbl-wrap tbl-wrap--tall');
  wrap.appendChild(compactTable(header, rows, report, opts.dir || '', pick));
  const full = el('details', 'chg__full');
  full.appendChild(el('summary', null, t('webui.changes.full')));
  full.addEventListener('toggle', () => { if (full.open && !full.querySelector('.tbl')) full.appendChild(fullTable(header, rows, pick)); });
  box.append(wrap, note, full);
  return box;
}

// fullTable:CLI 的原表(renderTable)+ Spotify 連結欄;ACTION 那一格標上機器值。
function fullTable(header, rows, pick) {
  const wrap = el('div', 'tbl-wrap tbl-wrap--tall');
  const tbl = renderTable(header, rows);
  if (pick) linkColumn(tbl, rows, pick);
  const ai = header.indexOf('ACTION');
  if (ai >= 0) {
    for (const tr of tbl.querySelectorAll('tbody tr')) {
      const cell = tr.children[ai];
      if (cell) cell.dataset.action = cell.textContent.trim();
    }
  }
  wrap.appendChild(tbl);
  return wrap;
}

// ACTS:DIR + ACTION → 白話的動作。key 要寫字串字面(i18n 的靜態檢查),所以每一種一個函式;沒列到的組合照原字顯示、不藏列。
const ACTS = {
  'pull add': (p) => t('webui.changes.pull.add', { platform: p }),
  'pull remove': () => t('webui.changes.pull.remove'),
  'pull move': () => t('webui.changes.pull.move'),
  'pull rename': () => t('webui.changes.pull.rename'),
  'pull unlink': (p) => t('webui.changes.pull.unlink', { platform: p }),
  'push add': (p) => t('webui.changes.push.add', { platform: p }),
  'push remove': (p) => t('webui.changes.push.remove', { platform: p }),
  'push move': (p) => t('webui.changes.push.move', { platform: p }),
  'push rename': (p) => t('webui.changes.push.rename', { platform: p }),
  'dedup remove': () => t('webui.changes.dedup.remove'),
};

function compactTable(header, rows, report, dir, pick) {
  const col = (k) => header.indexOf(k);
  const [di, ai, pi, li, ti, ri, ari, posi] = ['DIR', 'ACTION', 'PROVIDER', 'PLAYLIST', 'TITLE', 'REASON', 'ARTISTS', 'POS'].map(col);
  const many = li >= 0 && new Set(rows.map((r) => r[li])).size > 1;
  const links = pick ? rows.map((r, i) => { const x = pick(r, i); return x && spotifyLink(x.kind, x.id, x.title); }) : [];
  const withLinks = links.some(Boolean);
  const tbl = el('table', 'tbl chg__tbl');
  const cg = el('colgroup');
  const heads = [];
  const add = (cls, text) => { cg.appendChild(el('col', cls)); heads.push(text); };
  if (report) add('chg__c-pos', '#');
  if (many) add('chg__c-pl', t('webui.changes.col.playlist'));
  if (!report) add('chg__c-act', t('webui.changes.col.action'));
  add('chg__c-song', t('webui.changes.col.song'));
  add('chg__c-why', t('webui.changes.col.reason'));
  if (withLinks) add('chg__c-sp', '');
  const thead = el('thead');
  const hr = el('tr');
  for (const h of heads) hr.appendChild(el('th', null, h));
  thead.appendChild(hr);
  const tbody = el('tbody');
  rows.forEach((r, i) => {
    const tr = el('tr');
    if (report) tr.appendChild(el('td', 'num chg__pos', r[posi] || ''));
    if (many) tr.appendChild(el('td', 'chg__pl', r[li] || ''));
    if (!report) {
      const d = (di >= 0 ? r[di] : dir) || '';
      const a = r[ai] || '';
      const f = a === 'skip' ? () => t('webui.changes.skip') : ACTS[`${d} ${a}`];
      const td = el('td', 'chg__act', f ? f(providerName(pi >= 0 ? r[pi] : '')) : a);
      td.dataset.action = a; // 上色看機器值(app.css 的 td[data-action])
      tr.dataset.action = a;
      if (d) tr.dataset.dir = d;
      tr.appendChild(td);
    }
    const song = el('td', 'chg__song');
    const title = ti >= 0 ? r[ti] || '' : '';
    const sub = ari >= 0 ? r[ari] || '' : '';
    song.appendChild(el('span', 'pl__title', title));
    if (sub) song.appendChild(el('span', 'pl__sub', sub));
    song.title = sub ? `${title}\n${sub}` : title; // 省略掉的字,滑過去看得到全文
    tr.append(song, el('td', 'chg__why', ri >= 0 ? r[ri] || '' : ''));
    if (withLinks) {
      const td = el('td', 'row-actions');
      if (links[i]) td.appendChild(links[i]);
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  });
  tbl.append(cg, thead, tbody);
  return tbl;
}

// ── 就地的寫入流程(計畫 §3.5)──
export function markStep(f, n) {
  [...f.steps.children].forEach((li, i) => { // 真的 DOM 的 children 是 HTMLCollection,沒有 forEach
    li.dataset.state = i < n ? 'done' : i === n ? 'now' : 'todo';
    if (i === n) li.setAttribute('aria-current', 'step'); else li.removeAttribute('aria-current');
  });
}

// say:收尾那一句;lead 是這個流程自己的說明(停在哪一步),接在 CLI 的原因前面。
export function say(f, o, lead) {
  f.status.className = o.warn ? 'page__warn pl__flow-status' : 'page__note pl__flow-status';
  f.status.textContent = [lead, o.text, o.extra].filter(Boolean).join(' ');
  if (o.console) {
    const a = el('a', 'wiz__link', t('webui.move.see_console'));
    a.href = '#/console';
    f.status.appendChild(el('span', null, ' '));
    f.status.appendChild(a);
  }
}

// makeFlow:一頁一個。busy:流程區裡有寫入命令在跑(提示畫在它裡面)。這時換一份清單或開新的流程會清掉流程區、把提示一起拿掉,
// 命令就卡在序列槽裡等一個看不到的提示:擋下並說明(同 btn() 被擋時的 capy:busy)。▶ 與自動重讀不算,照常可以換清單。
export function makeFlow(con) {
  let busy = false;
  const blocked = () => { if (busy) document.dispatchEvent(new Event('capy:busy')); return busy; };

  function openFlow(stepTexts, box) {
    const steps = el('ol', 'wiz__steps');
    for (const s of stepTexts || []) steps.appendChild(el('li', 'wiz__step', s));
    const status = el('p', 'page__note pl__flow-status');
    status.setAttribute('role', 'status');
    const table = el('div', 'pl__flow-table');
    const host = el('div', 'pl__flow-prompts'); // promptHost:在頁面裡,reveal() 不會把人丟去主控台
    box.replaceChildren(...(stepTexts ? [steps] : []), status, table, host);
    return { steps, status, table, host };
  }

  // step:跑一個會寫入的命令;done(code, outcome, wrote) 在 onExit 裡叫——下一步要在那裡送(用 await 串的話,
  // 排隊中的 con.idle 會先被叫醒而插隊:console.js 的 onExit 先於 wake())。
  // 表在上、提示在下:table 事件一定先到(CLI 先印表再 confirmWrite),畫進表格區就自然在提示上面。
  // wrote:頁面從不代加 --yes,sync / resolve / pull 的每一次寫入都要先有人答應確認(或逐首裁決)——
  // exit 0 而且答過提示才算有寫入;只收到表(只有 skip 列、只有待決定的列)不算。
  function step(f, args, label, done) {
    let closedBy = '';
    let answered = false;
    let pending = 0; // resolve 表裡等人逐首決定的列(review / conflict)
    f.table.replaceChildren();
    busy = true;
    con.run('', {
      onTable: (h, r) => {
        const a = h.indexOf('ACTION');
        pending = h.includes('CONFIDENCE') ? r.filter((x) => x[a] === 'review' || x[a] === 'conflict').length : 0;
        f.table.replaceChildren(changeTable(h, r, { dir: args[0] === 'pl' && (args[1] === 'pull' || args[1] === 'push') ? args[1] : '' }));
      },
      onPromptClosed: (ev) => { closedBy = ev.reason; if (ev.reason === 'answered') answered = true; },
      onExit: (code, msg, reason) => {
        busy = false;
        const wrote = code === 0 && answered;
        done(code, outcome(code, msg, reason, closedBy, wrote, pending), wrote);
      },
    }, { args, label, promptHost: f.host });
  }

  return { openFlow, step, blocked, get busy() { return busy; } };
}

// readLists:讀一個平台上的清單(pl list --provider p)。done({ rows } 或 { rows: [], error }, reason, code) 在 onExit 裡叫。
export function readLists(con, p, label, done) {
  const rows = [];
  con.run('', {
    onTable: (h, r) => {
      const [i, n, c] = ['ID', 'NAME', 'TRACKS'].map((k) => h.indexOf(k)); // 機器欄位,不跟語系(決策 50)
      for (const x of r) rows.push({ id: x[i], name: x[n], tracks: x[c] });
    },
    onExit: (code, msg, reason) => done(code === 0 ? { rows } : { rows: [], error: (msg || '').replace(/^Error: /, '') }, reason, code),
  }, { args: ['pl', 'list', '--provider', p], label });
}

// playlistLink:平台上那份清單的網頁(Apple 的資料庫清單沒有公開網址;本機沒有網頁)。
export function playlistLink(p, id, name) {
  if (p === 'spotify') return spotifyLink('playlist', id, name);
  if (p !== 'youtube') return null;
  const a = el('a', 'btn btn--ghost', t('webui.search.open_youtube'));
  a.href = `https://music.youtube.com/playlist?list=${encodeURIComponent(id.split('/').pop())}`;
  a.target = '_blank';
  a.rel = 'noopener';
  a.setAttribute('aria-label', `${t('webui.search.open_youtube')}: ${name}`);
  return a;
}

export function moveLink() {
  const a = el('a', 'btn btn--ghost', t('webui.playlists.platforms.go_move'));
  a.href = '#/move';
  return a;
}

// linkFlows:連結面板(正本連到哪幾份平台清單)與它發起的寫入流程(同步這份、找對應、逐首決定、在 X 建一份、納入)。
// flow 是 makeFlow 的實例;state 是頁面的 { auth, devices, platLists }(用到時才讀);reload 是寫入收尾後的重讀;
// 每個流程畫在呼叫端給的 box 裡(那一頁的流程區,提示也畫在那裡)。
export function linkFlows({ flow, state, providers, reload }) {
  // runOne:單一命令(同步這份清單、找對應、逐首決定);收尾後重讀。
  function runOne(args, label, box) {
    const f = flow.openFlow(null, box);
    flow.step(f, args, label, (code, o) => { say(f, o); reload(); });
  }

  // ── 連結面板(計畫 §3.5):Google Drive 正本 → 每個平台一列;本機只在連著時出現 ──
  function linkPanel(pl, items, track, box) {
    const panel = el('div', 'pl__links');
    const hub = el('div', 'pl__hub');
    hub.append(el('strong', null, t('webui.playlists.links.hub')), el('span', null, t('webui.playlists.links.hub_sub')));
    const list = el('div', 'pl__link-rows');
    // 算對應用不重複、這台電腦有資料的歌(懸空的 cid resolve 也會跳過)
    const cids = [...new Set(items.map((it) => it.cid))].filter((cid) => track(cid).title !== undefined || track(cid).mappings);
    for (const p of providers.list) {
      const st = linkState(state.auth, state.devices, p, pl);
      if (st) list.appendChild(linkRow(p, st, pl, cids, track, box));
    }
    panel.append(hub, list);
    return panel;
  }

  function linkRow(p, st, pl, cids, track, box) {
    const platform = providerName(p);
    const row = el('div', 'pl__link');
    row.dataset.state = st.kind;
    row.dataset.platform = p;
    const info = el('span', 'pl__link-info');
    const acts = el('span', 'pl__link-acts');
    const account = () => { const a = el('a', 'btn btn--ghost', t('webui.playlists.go_account')); a.href = '#/account'; return a; };
    const open = () => { const a = playlistLink(p, st.id, pl.name); if (a) acts.appendChild(a); };
    let label = t('webui.playlists.links.linked');
    if (st.kind === 'linked') {
      const missing = cids.filter((cid) => missingOn(track(cid), p)).length;
      info.textContent = missing ? t('webui.playlists.links.missing', { count: missing }) : t('webui.playlists.links.all_mapped');
      if (missing) info.dataset.warn = '';
      open();
      if (missing && COLUMNS.includes(p)) {
        acts.append(
          btn(t('webui.playlists.links.resolve'), '', () => runOne(['resolve', pl.pid, '--provider', p], t('webui.playlists.label.resolve', { name: pl.name, platform }), box)),
          btn(t('webui.playlists.links.review'), 'btn--ghost', () => runOne(['resolve', pl.pid, '--provider', p, '--review'], t('webui.playlists.label.review', { name: pl.name, platform }), box)));
      }
    } else if (st.kind === 'out') {
      info.textContent = st.relogin ? t('webui.playlists.links.relogin', { platform }) : t('webui.playlists.links.logged_out', { platform });
      acts.appendChild(account());
      open();
    } else if (st.kind === 'foreign') {
      info.textContent = st.device ? t('webui.playlists.links.foreign_device', { device: st.device }) : t('webui.playlists.links.foreign_account');
    } else {
      label = t('webui.playlists.links.unlinked');
      if (st.kind === 'unlinked_out') {
        info.textContent = st.relogin ? t('webui.playlists.links.relogin', { platform }) : t('webui.playlists.links.logged_out', { platform });
        acts.appendChild(account());
      } else {
        const known = cids.filter((cid) => mappingOf(track(cid), p).id).length;
        if (known) info.textContent = t('webui.playlists.links.known', { count: known, platform });
        const b = el('button', 'btn', t('webui.playlists.links.create', { platform })); // 只打開說明,還不送命令:不標 data-run
        b.type = 'button';
        b.addEventListener('click', () => askCreate(p, pl, box));
        acts.appendChild(b);
      }
    }
    row.append(el('span', 'pl__link-name', platform), el('span', 'pl__link-state', label), info, acts);
    return row;
  }

  // 在 X 建一份:先說清楚會發生什麼,按「開始」才依序跑三個命令;第一步(建立並連上)沒有 CLI 的確認,「開始」就是同意。
  function askCreate(p, pl, box) {
    if (flow.blocked()) return;
    const platform = providerName(p);
    const card = el('div', 'pl__flow-card');
    const acts = el('div', 'form-row');
    const no = el('button', 'btn btn--ghost', t('webui.playlists.create.cancel'));
    no.type = 'button';
    no.addEventListener('click', () => box.replaceChildren());
    // 「各平台上的清單」讀過、那個平台已經有同名(不分大小寫,同 CLI)的清單:CLI 會停下來不建,不給「開始」,先說清楚。
    // 同名的那份確定是空的(曲數 0,例如上次建好了但連結沒寫進 Drive)照給「開始」:CLI 會停在第一步、給接回去的 pl link
    // 命令,那是這種情況唯一的復原路(#125 review 第 3 點)。
    const lists = state.platLists;
    const dup = lists && lists[p] && lists[p].rows.some((r) => same(r.name, pl.name) && r.tracks !== '0');
    if (dup) {
      card.appendChild(el('p', null, t('webui.playlists.create.same_name', { platform, name: pl.name })));
      acts.append(moveLink(), no);
    } else {
      card.appendChild(el('p', null, t('webui.playlists.create.explain', { platform, name: pl.name, start: t('webui.playlists.create.start') })));
      if (p === 'apple') card.appendChild(el('p', 'page__note', t('webui.playlists.create.apple'))); // 決策 49:可能,不是必然
      acts.append(btn(t('webui.playlists.create.start'), '', () => create(p, pl, box)), no);
    }
    card.appendChild(acts);
    box.replaceChildren(card);
  }

  function create(p, pl, box) {
    const platform = providerName(p);
    const name = pl.name;
    const f = flow.openFlow([t('webui.playlists.step.create'), t('webui.playlists.step.resolve'), t('webui.playlists.step.push')], box);
    markStep(f, 0);
    // --new-only:頁面讀的 export 可能過時;平台那邊別台 / 別帳號剛連上的,CLI 會擋下來而不是接管(計畫 §3.5 前置 1)。
    const stop2 = t('webui.playlists.create.stop2', { platform, name, button: t('webui.playlists.links.resolve') });
    flow.step(f, ['pl', 'link', '--new-only', pl.pid, p, '--create'], t('webui.playlists.label.create', { name, platform }), (c1, o1) => {
      if (c1 !== 0) { say(f, o1, t('webui.playlists.create.stop1')); reload(); return; }
      markStep(f, 1);
      flow.step(f, ['resolve', pl.pid, '--provider', p], t('webui.playlists.label.resolve', { name, platform }), (c2, o2, w2) => {
        if (c2 !== 0) { say(f, o2, stop2); reload(); return; }
        markStep(f, 2);
        flow.step(f, ['pl', 'sync', pl.pid, '--provider', p], t('webui.playlists.label.push', { name, platform }), (c3, o3, w3) => {
          if (c3 === 0 && w3) { markStep(f, 3); say(f, { text: t('webui.playlists.create.done', { platform, name }) }); }
          else if (c3 === 0) say(f, { text: t('webui.playlists.create.none', { platform, name, button: t('webui.playlists.links.review') }) }); // 一首都沒加:照實說
          // 停在加歌:下一步一律是「同步這份清單」;「對應已經寫入」只在第二步真的寫了時才說(#124 review)
          else say(f, o3, w2 ? t('webui.playlists.create.stop3', { platform, button: t('webui.playlists.sync') })
            : t('webui.playlists.create.stop3_nowrite', { platform, name, button: t('webui.playlists.sync') }));
          reload();
        });
      });
    });
  }

  // adopt:納入 = 建一份新的空正本並連上(--new-only:名字已經是某份正本就擋,頁面讀的 export 可能過時)→ 把歌拉進來(第一次 pull
  // 採平台順序,新正本是空的,所以是對的)。名稱放在 -- 後面:以 - 開頭的清單名不被當成旗標。
  function adopt(p, r, box) {
    if (flow.blocked()) return;
    const platform = providerName(p);
    const name = r.name;
    const f = flow.openFlow([t('webui.playlists.step.adopt'), t('webui.playlists.step.pull')], box);
    box.scrollIntoView?.({ block: 'nearest' }); // 流程區在整張平台清單上面:按了下面某一列,變更表與確認要看得到
    markStep(f, 0);
    flow.step(f, ['pl', 'link', '--new-only', '--', name, `${p}:${r.id}`], t('webui.playlists.label.adopt', { name, platform }), (c1, o1) => {
      if (c1 !== 0) { say(f, o1, t('webui.playlists.adopt.stop1')); reload(); return; }
      markStep(f, 1);
      flow.step(f, ['pl', 'pull', '--', name], t('webui.playlists.label.adopt_pull', { name }), (c2, o2, w2) => {
        if (c2 === 0) {
          markStep(f, 2);
          say(f, { text: w2 ? t('webui.playlists.adopt.done', { name, platform }) : t('webui.playlists.adopt.done_empty', { name, platform }) });
        } else say(f, o2, t('webui.playlists.adopt.stop2', { name, button: t('webui.playlists.sync') })); // 留下一份連著的空正本:照實說下一步
        reload();
      });
    });
  }

  return { panel: linkPanel, askCreate, adopt, syncOne: runOne };
}
