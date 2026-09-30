// playlists.js:#/playlists —— 左邊是 capy 保管的清單(正本;來源 export:唯讀、只讀本機 state.db),右邊是選中那份的歌曲表
// (計畫 docs/superpowers/plans/2026-09-30-my-playlists-redesign.md,決策 61)。順序永遠照清單本來的順序,沒有排序、沒有拖曳
// (決策 38);篩選只藏列。每首歌每個平台一欄:▶ 用 capy 在那個平台放、「在 Spotify 上聽」/「開啟」開新分頁(計畫 §3.2)。
// 第二段是平台清單(pl list / pl show)。顯示 Spotify 的曲目或清單的地方都連回 Spotify(計畫 2026-09-24 §1.7 S7)。
import { el, quote, btn, field, input, select, providerOptions, providerName, emptyState, pageHead } from './common.js';
import { renderTable, linkColumn, spotifyLink, youtubeLink, mmss, SPOTIFY_ID } from '../table.js';
import { parseStatus } from './account.js';
import { t } from '../i18n.js';

const COLUMNS = ['spotify', 'apple', 'youtube']; // 會佔一欄的平台:本機沒有播放、也沒有網頁,不佔欄
const CATALOG_ID = /^\d+$/;                      // Apple 的 catalog id;i. / a. 是只在資料庫的列,play --id 會打 catalog 端點失敗
const FILTER_PLAYLISTS = 8;                      // 清單超過這麼多份才出現篩選框(同搬家精靈第二步)
const FILTER_SONGS = 20;                         // 一份清單超過這麼多首才出現「在這份清單裡找歌」
// isMac:Apple 的播放只在 macOS。瀏覽器與 capy 一定在同一台電腦(只綁 127.0.0.1,決策 40),看瀏覽器就夠。
// 寫成函式:測試要換 navigator。
const isMac = () => /Mac/.test(globalThis.navigator?.userAgent || '');

export function initPlaylists(root, api, con, notice, providers) {
  pageHead(root, t('webui.playlists.title'), t('webui.playlists.lead'));
  const cols = el('div', 'pl__cols');
  const left = el('div', 'pl__left');
  const right = el('div', 'pl__right');
  cols.append(left, right);

  const platform = el('section', 'pl__platform');
  const prov = select(providerOptions(providers.list), providers.current);
  const pbar = el('div', 'form-row');
  pbar.appendChild(field(t('webui.common.platform'), prov));
  const pout = el('div', 'page__out');

  root.append(about(), btn(t('webui.playlists.refresh'), 'btn--ghost', load), cols, el('h3', 'card__sub', t('webui.playlists.platform_heading')), pbar, pout);
  pbar.append(
    btn(t('webui.playlists.list'), '', () => {
      const p = prov.value; // 按下去那一刻的平台:命令跑的時候選單可能被換掉,表要照送出去的那一家判斷
      con.run(`pl list --provider ${p}`, {
        onTable: (h, r) => pout.replaceChildren(wrapTable(h, r, p === 'spotify' && ((row) => ({ kind: 'playlist', id: row[h.indexOf('ID')], title: row[h.indexOf('NAME')] })))),
      }, { label: t('webui.playlists.list_label', { platform: providerName(p) }) });
    }),
  );
  con.idle(load); // 同帳號頁:有命令在跑就等它結束,不要撞上它、畫成「沒有清單」

  // auth:`auth status --json`(不連網)。▶ 要不要給、空白態怎麼說都看它;讀不到 = {} = 不知道,▶ 照給、讓 CLI 說原因。
  let auth = {};
  // 兩個命令都 quiet:export 的整份 JSON 不灌進主控台。第二個在第一個的 onExit 裡送——用 await 串的話,
  // 排隊中的 con.idle 會先被叫醒而插隊(console.js:onExit 先於 wake())。
  function load() {
    left.replaceChildren();
    right.replaceChildren();
    let status = '';
    con.run('', {
      onStdout: (s) => { status += s; },
      onExit: () => { auth = parseStatus(status); readExport(); },
    }, { args: ['auth', 'status', '--json'], label: t('webui.move.label.status'), quiet: true });
  }

  function readExport() {
    let text = '';
    con.run('export', {
      onStdout: (s) => { text += s; },
      onExit: (code, msg, reason) => {
        if (code !== 0) {
          // 只有命令真的跑完、以非 0 結束才算「這台沒有本機資料」;斷線、被別的分頁佔著、中止、逾時都不是,照說原因
          //(#122 review 第 1 點;原因的句子 Console 也會放進 notice)。
          if (reason === 'done') noLocal();
          else left.appendChild(emptyState(msg || t('webui.playlists.not_loaded', { button: t('webui.playlists.refresh') })));
          return;
        }
        let files;
        try {
          files = JSON.parse(text);
        } catch (e) {
          left.appendChild(el('p', 'card__error', t('webui.playlists.bad_export', { error: e.message })));
          return;
        }
        render(files);
      },
    }, { label: t('webui.playlists.load_label'), quiet: true });
  }

  // 沒有東西可以畫的三種情況,下一步各不一樣:還沒連 Google Drive(正本就放在那裡)→ 帳號頁;連了、但這台電腦還沒讀過 Drive
  // (export 失敗 = 本機沒有資料,例如第二台電腦;export 沒辦法知道 Drive 上有沒有清單)→ 同步頁把正本拉回來;
  // 讀過了、真的沒有清單 → 搬家。
  function noLocal() {
    if (!drive()) hint(t('webui.playlists.empty_no_local', { option: t('webui.sync.dry_run'), button: t('webui.sync.pull') }), t('webui.playlists.go_sync'), '#/sync');
  }
  function empty() {
    if (!drive()) left.appendChild(emptyState(t('webui.playlists.empty')));
  }
  // drive:Google Drive 本身有問題就先說它(畫了回 true)。auth status 的 google.state 是 ok | missing | keychain_error;
  // keychain_error 不是「沒連」,是要到帳號頁處理的錯誤(同帳號頁,review #62;#122 review 第 2 點)。
  function drive() {
    const st = auth.google && auth.google.state;
    if (!st || st === 'ok') return false;
    hint(st === 'missing' ? t('webui.playlists.empty_no_drive') : t('webui.playlists.empty_drive_error'), t('webui.playlists.go_account'), '#/account');
    return true;
  }
  function hint(text, go, href) {
    left.appendChild(emptyState(text));
    const a = el('a', 'wiz__link', go);
    a.href = href;
    left.appendChild(a);
  }

  // canPlay:這台電腦能不能用 capy 在平台上放。沒登入(missing)/ 過期(expired)就不給 ▶(按了必定失敗);keychain_error
  // 是要處理的錯誤、不是沒登入,照給,讓 CLI 把原因說出來(#122 review 第 2 點);不知道(讀不到 auth)也照給。
  function canPlay(p) {
    if (p === 'apple' && !isMac()) return false;
    const st = auth[p] && auth[p].state;
    return st !== 'missing' && st !== 'expired';
  }

  function render(files) {
    const tr = files['tracks.json'] || {};
    const tracks = tr.tracks || {};
    const merged = tr.merged || {};
    // 清單檔裡可能還是敗者的 cid(別台裝置寫的、還沒被 pull 改指勝者):沿墓碑找勝者。寫入時已壓平,一步就到(canon.Tracks.Merged)。
    const track = (cid) => tracks[merged[cid] || cid] || {};
    // 不重排:export 的鍵序本來就是決定性的(map 鍵排序),而 localeCompare 的結果會隨瀏覽器的 ICU 版本浮動,
    // 同一份資料在不同瀏覽器上會不一樣。清單集合與清單內容都照原順序(review #62)。
    const pls = Object.keys(files)
      .filter((k) => k.startsWith('pl__'))
      .sort()
      .map((k) => files[k]);
    left.replaceChildren();
    if (!pls.length) { empty(); return; }
    const list = el('div', 'pl__list');
    const rows = pls.map((pl) => {
      const row = el('button', 'pl__item');
      row.type = 'button';
      row.appendChild(el('span', 'pl__name', pl.name || pl.pid));
      row.appendChild(el('span', 'pl__count num', String((pl.items || []).length)));
      const chips = el('span', 'pl__chips');
      for (const [linked] of links(pl)) chips.appendChild(el('span', 'chip', providerName(linked)));
      row.appendChild(chips);
      row.addEventListener('click', () => {
        for (const other of rows) other.classList.remove('is-active');
        row.classList.add('is-active');
        showItems(pl, track);
      });
      return row;
    });
    list.append(...rows);
    if (pls.length > FILTER_PLAYLISTS) {
      left.appendChild(filterBox(t('webui.playlists.filter_playlists'), (q) => {
        pls.forEach((pl, i) => { rows[i].hidden = !!q && !String(pl.name || pl.pid).toLowerCase().includes(q); });
      }));
    }
    left.appendChild(list);
    rows[0].click();
  }

  function showItems(pl, track) {
    right.replaceChildren();
    const items = pl.items || [];
    const head = el('div', 'pl__head');
    head.append(el('h3', 'card__sub', t('webui.playlists.items_title', { name: pl.name, count: items.length })),
      el('p', 'page__note', t('webui.playlists.fresh')));
    right.appendChild(head);
    // 每個連著的平台各一顆。送 links 裡的 id,不送正本的名稱:pl show 用名稱找平台上的清單,改過名或有同名清單就會找錯。
    const acts = el('div', 'form-row pl__acts');
    for (const [p, id] of links(pl)) {
      acts.appendChild(btn(t('webui.playlists.view_on', { platform: providerName(p) }), 'btn--ghost', () =>
        con.run(`pl show ${quote(id)} --provider ${p}`, {
          onTable: (h, r) => pout.replaceChildren(wrapTable(h, r, p === 'spotify' && ((row) => ({ kind: 'track', id: row[h.indexOf('ID')], title: row[h.indexOf('TITLE')] })))),
        }, { label: t('webui.playlists.view_label', { platform: providerName(p), name: pl.name }) })));
    }
    // 連著的 Spotify 清單本身(放在按鈕後面,不放進左欄那一列:那一列整個是 <button>,裡面不能再放連結)。
    const sp = spotifyLink('playlist', (pl.links || {}).spotify, pl.name);
    if (sp) acts.appendChild(sp);
    if (acts.firstChild) right.appendChild(acts);

    const { tbl, rows, texts } = songTable(pl, items, track);
    const wrap = el('div', 'tbl-wrap tbl-wrap--tall pl__songs');
    wrap.appendChild(tbl);
    if (items.length > FILTER_SONGS) {
      const count = el('span', 'pl__shown num');
      right.appendChild(filterBox(t('webui.playlists.filter_songs'), (q) => {
        let shown = 0;
        rows.forEach((tr, i) => { tr.hidden = !!q && !texts[i].includes(q); if (!tr.hidden) shown++; });
        count.textContent = q ? t('webui.playlists.shown', { shown, count: items.length }) : '';
      }, count));
    }
    right.appendChild(wrap);
  }

  // songTable:這一頁自己的表(不是 TSV 的直譯,不用 renderTable):#、歌曲(曲名 + 歌手 · 專輯)、時長、每個平台一欄。
  // 窄的時候每首拆成多行(app.css 的容器查詢把表格元素改成 block / flex),所以每個元素都標上表格的 role,報讀才還是表格。
  function songTable(pl, items, track) {
    const linked = new Set(links(pl).map(([p]) => p));
    // 平台欄:這份清單連著那個平台,或至少一首有那個平台的對應才出現;Apple 的格子只有 ▶,這台放不了就整欄不出現。
    const cols = COLUMNS.filter((p) => providers.list.includes(p) && (p !== 'apple' || canPlay('apple')) &&
      (linked.has(p) || items.some((it) => idOf(track(it.cid), p))));
    const tbl = el('table', 'tbl pl__tbl');
    tbl.setAttribute('role', 'table');
    const cg = el('colgroup');
    cg.append(el('col', 'pl__c-pos'), el('col', 'pl__c-song'), el('col', 'pl__c-dur'), ...cols.map((p) => el('col', `pl__c-${p}`)));
    const thead = el('thead');
    const hr = el('tr');
    hr.setAttribute('role', 'row');
    for (const text of ['#', t('webui.playlists.col.song'), t('webui.playlists.col.duration'), ...cols.map(providerName)]) {
      const th = el('th', null, text);
      th.setAttribute('role', 'columnheader');
      hr.appendChild(th);
    }
    thead.appendChild(hr);
    const tbody = el('tbody');
    const rows = [];
    const texts = [];
    items.forEach((it, i) => {
      const trk = track(it.cid);
      const title = trk.title || '';
      const artists = (trk.artists || []).join(', ');
      const tr = el('tr');
      tr.setAttribute('role', 'row');
      tr.dataset.cid = it.cid;
      const song = cell('pl__song');
      song.appendChild(el('span', 'pl__title', title || t('webui.playlists.no_track_data')));
      const sub = [artists, trk.album].filter(Boolean).join(' · ');
      if (sub) song.appendChild(el('span', 'pl__sub', sub));
      song.title = sub ? `${title}\n${sub}` : title; // 省略掉的字,滑過去看得到全文
      tr.append(cell('pl__pos num', String(i + 1)), song, cell('pl__dur num', trk.duration_ms > 0 ? mmss(trk.duration_ms) : ''));
      for (const p of cols) tr.appendChild(platformCell(p, (trk.mappings || {})[p], title || it.cid));
      tbody.appendChild(tr);
      rows.push(tr);
      texts.push(`${title}\n${artists}\n${trk.album || ''}`.toLowerCase());
    });
    tbl.append(cg, thead, tbody);
    return { tbl, rows, texts };
  }

  // platformCell:一首歌在一個平台的那一格(計畫 §3.2 的表)。
  function platformCell(p, m, title) {
    const td = cell('row-actions pl__cell');
    const platform = providerName(p);
    td.dataset.platform = platform; // 窄版沒有表頭可以對:CSS 用 attr() 把平台名寫在格子前面
    const id = m && typeof m.id === 'string' ? m.id : '';
    const note = (cls, text, why) => { const s = el('span', cls, text); s.title = why; td.appendChild(s); return td; };
    if (!id) {
      return note('pl__none', '—', m && m.pinned ? t('webui.playlists.cell.pinned_none', { platform }) : t('webui.playlists.cell.none', { platform }));
    }
    if (p === 'spotify') {
      if (!SPOTIFY_ID.test(id)) {
        return id.startsWith('spotify:local:')
          ? note('pl__only', t('webui.playlists.cell.spotify_local'), t('webui.playlists.cell.spotify_local_title'))
          : note('pl__none', '—', t('webui.playlists.cell.unplayable'));
      }
      if (canPlay(p)) td.appendChild(playButton(p, id, title));
      td.appendChild(spotifyLink('track', id, title)); // S7:有 Spotify 對應的每一列都看得到,不收進選單
      return td;
    }
    if (p === 'apple') {
      if (!CATALOG_ID.test(id)) return note('pl__only', t('webui.playlists.cell.apple_library'), t('webui.playlists.cell.apple_library_title'));
      td.appendChild(playButton(p, id, title)); // 這一欄只在這台放得了的時候才出現(songTable)
      return td;
    }
    if (p === 'youtube') td.appendChild(youtubeLink(id, title, 'btn btn--ghost', t('webui.playlists.open'), t('webui.playlists.open_label', { title })));
    return td;
  }

  // playButton:用 capy 在那個平台放這首(同搜尋頁的列動作,走 args 陣列)。Apple 的歌不在資料庫時只會在 Music.app 打開、
  // 沒開始播:命令那句(不以 ▶ 開頭)照抄給 notice,不然它只進看不到的主控台(決策 52)。等 run() 整個收尾才說:
  // 收尾時會叫醒排隊的自動讀取,它一開跑就清掉 notice。
  function playButton(p, id, title) {
    const b = btn('▶', 'btn--ghost btn--icon', async () => {
      let said = '';
      const [code] = await con.run('', { onStdout: (s) => { said += s; } }, {
        args: ['play', '--id', id, '--provider', p],
        label: p === 'apple' ? t('webui.search.open_music_label', { title }) : t('webui.search.play_label', { title }),
      });
      const line = said.trim();
      if (code === 0 && line && !line.startsWith('▶')) notice(line);
    });
    b.setAttribute('aria-label', t('webui.playlists.play_on', { platform: providerName(p), title }));
    return b;
  }

  // links:真的連著的 [平台, id](空字串 = 沒連結,同 pull.go / resolve 的規則;不是字串的壞資料也不算)。
  function links(pl) {
    return Object.entries(pl.links || {}).filter(([, id]) => typeof id === 'string' && id);
  }

  // wrapTable:pick 見 table.js 的 linkColumn;沒給(或 false)就是一般的表。
  function wrapTable(h, r, pick) {
    const w = el('div', 'tbl-wrap');
    const tbl = renderTable(h, r);
    w.appendChild(pick ? linkColumn(tbl, r, pick) : tbl);
    return w;
  }
}

// idOf:一首歌在一個平台上的 id(沒有就空字串)。
const idOf = (trk, p) => {
  const m = (trk.mappings || {})[p];
  return m && typeof m.id === 'string' ? m.id : '';
};

function cell(cls, text) {
  const td = el('td', cls, text);
  td.setAttribute('role', 'cell');
  return td;
}

// filterBox:篩選框,不分大小寫比對;extra 是放在框旁邊的東西(顯示了幾首)。
function filterBox(label, onFilter, extra) {
  const bar = el('div', 'form-row pl__filter');
  const f = input('sans', label);
  f.type = 'search';
  f.setAttribute('aria-label', label);
  f.addEventListener('input', () => onFilter(f.value.trim().toLowerCase()));
  bar.appendChild(f);
  if (extra) bar.appendChild(extra);
  return bar;
}

// about:正本、連結、同步是什麼(計畫 §3.4 的第二層);預設收起,第一眼只有 lead。
function about() {
  const d = el('details', 'pl__about');
  d.appendChild(el('summary', null, t('webui.playlists.about.summary')));
  const ul = el('ul', 'pl__about-list');
  for (const [k, v] of [
    [t('webui.playlists.about.master.title'), t('webui.playlists.about.master.text')],
    [t('webui.playlists.about.link.title'), t('webui.playlists.about.link.text')],
    [t('webui.playlists.about.sync.title'), t('webui.playlists.about.sync.text')],
    [t('webui.playlists.about.move.title'), t('webui.playlists.about.move.text')],
  ]) {
    const li = el('li');
    li.append(el('strong', null, k), el('span', null, v));
    ul.appendChild(li);
  }
  d.appendChild(ul);
  return d;
}
