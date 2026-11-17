// playlists.js:#/playlists —— 左邊是 capy 保管的清單(正本;來源 export:唯讀、只讀本機 state.db),右邊是選中那份的歌曲表
// (計畫 docs/superpowers/plans/2026-09-30-my-playlists-redesign.md,決策 61)。順序永遠照清單本來的順序,沒有排序、沒有拖曳
// (決策 38);篩選只藏列。每首歌每個平台一欄:▶ 用 capy 在那個平台放、「在 Spotify 上聽」/「開啟」開新分頁(計畫 §3.2)。
// 右欄上面是連結面板(正本連到哪幾份平台清單)與就地的寫入流程(同步、在 X 建一份、找對應;計畫 §3.5):寫入一律走 CLI 自己的
// 變更表與確認,頁面絕不代加確認或越過的旗標(決策 46)。第二段是「各平台上的清單」:連著哪份正本、還沒納入的可以納入(§3.4 / §3.5)。
// 顯示 Spotify 的曲目或清單的地方都連回 Spotify(計畫 2026-09-24 §1.7 S7)。
import { el, btn, select, providerName, emptyState, pageHead } from './common.js';
import { spotifyLink, youtubeLink, mmss, SPOTIFY_ID } from '../table.js';
import { postPlayerAction } from '../player.js';
import { COLUMNS, norm, mappingOf, links, indexExport, driveState, loggedOut, relogin, listState, cell, filterBox, makeFlow, readLists, linkFlows } from './plflow.js';
import { lineSplitter, wikiRenderer } from './wiki.js';
import { t } from '../i18n.js';

const CATALOG_ID = /^\d+$/;                      // Apple 的 catalog id;i. / a. 是只在資料庫的列,play --id 會打 catalog 端點失敗
const FILTER_PLAYLISTS = 8;                      // 清單超過這麼多份才出現篩選框(同搬家精靈第二步)
const FILTER_SONGS = 20;                         // 一份清單超過這麼多首才出現「在這份清單裡找歌」
// isMac:Apple 的播放只在 macOS。瀏覽器與 capy 一定在同一台電腦(只綁 127.0.0.1,決策 40),看瀏覽器就夠。
// 寫成函式:測試要換 navigator。
const isMac = () => /Mac/.test(globalThis.navigator?.userAgent || '');

export function initPlaylists(root, api, con, notice, providers, arg) {
  pageHead(root, t('webui.playlists.title'), t('webui.playlists.lead'));
  const cols = el('div', 'pl__cols');
  const left = el('div', 'pl__left');
  const right = el('div', 'pl__right');
  cols.append(left, right);

  // 各平台上的清單(計畫 §3.4 / §3.5「納入」):按了才讀(會連網),只讀這台有登入的平台。pflow 是納入的就地流程
  //(提示畫在這裡,在 #page-playlists 裡);重讀清單時不清它。
  const plats = el('section', 'pl__plats');
  const platList = el('div', 'pl__plat-list');
  const pflow = el('div', 'pl__flow');
  let masters = null;   // 目前的正本:比對「連著哪一份」「有沒有同名」;null = 這台還沒讀到正本(export 失敗),分不出來
  plats.append(el('h3', 'card__sub', t('webui.playlists.platforms.title')), el('p', 'page__note', t('webui.playlists.platforms.lead')),
    btn(t('webui.playlists.platforms.read'), '', readPlatforms), pflow, platList);

  const refreshBtn = el('button', 'btn btn--ghost', t('webui.playlists.refresh'));
  refreshBtn.type = 'button';
  refreshBtn.addEventListener('click', () => load());
  root.append(about(), refreshBtn, cols, plats);

  // st.auth:/api/library 的 auth(同 auth status --json,不連網、零 secret)。▶ 要不要給、連結面板與空白態怎麼說都看它。
  // st.devices:export 的 manifest.json:裝置 id → 名稱(連在別台電腦的本機清單說得出是哪一台)。
  // st.platLists:{ 平台: { rows: [{ id, name, tracks }], error } };null = 還沒讀過。
  // 放在一個物件裡交給共用的連結面板(plflow.js):它每次用到時才讀,重讀後拿到的是新的值。
  const st = { auth: {}, devices: [], platLists: null, masters: null };
  let selected = arg || ''; // 選中的那份(pid):寫入後、回到這一頁時重讀,畫回同一份;#/playlists/<pid> 直接打開那一份
  let currentSongRows = []; // [{ tr, trk }]:跟著 capy:now 標記 data-playing="true"
  let lastNow = null;
  // flow:寫入命令的就地區塊(步驟條、狀態句、變更表、提示;表在上、提示在下——CLI 先印表再問確認)。
  // 只在切到別份清單時清空;重讀後搬進新畫的右欄,收尾那句不會因為重讀而消失。
  const flow = el('div', 'pl__flow');
  // fl:寫入流程(plflow.js 的 makeFlow);流程區裡有寫入命令在跑時,換一份清單或開新的流程會被 fl.blocked() 擋下並說明。
  const fl = makeFlow(con);
  const blocked = fl.blocked;
  const lf = linkFlows({ flow: fl, state: st, providers, reload: load });

  // load:走唯讀直達端點 GET /api/library,不進 runMu、不阻塞也不被背景 sync / wiki 卡住。
  // 判斷「是否已畫過清單」看 masters?.length 而非 selected:#/playlists/<pid> 進頁時 selected 一開始就有值,第一次就失敗不可留整頁空白。
  let loadSeq = 0;
  async function load() {
    const mine = ++loadSeq;
    const hadMasters = !!(masters && masters.length);
    let r;
    try {
      r = await api.fetch('/api/library');
    } catch (e) {
      if (mine !== loadSeq || hadMasters) return;
      left.replaceChildren(emptyState(e.message || t('webui.playlists.not_loaded', { button: t('webui.playlists.refresh') })));
      right.replaceChildren();
      return;
    }
    if (mine !== loadSeq) return;
    if (!r.ok) {
      let msg = '';
      try { msg = (await r.json()).error || ''; } catch (_) { /* ignore */ }
      if (hadMasters) { if (msg) notice(msg); return; }
      left.replaceChildren(emptyState(msg || t('webui.playlists.not_loaded', { button: t('webui.playlists.refresh') })));
      right.replaceChildren();
      return;
    }
    let lib;
    try {
      lib = await r.json();
    } catch (e) {
      if (hadMasters) return;
      left.replaceChildren(el('p', 'card__error', e.message));
      right.replaceChildren();
      return;
    }
    if (mine !== loadSeq) return;
    if (lib && lib.auth && typeof lib.auth === 'object') st.auth = lib.auth;
    if (lib && lib.store_error) {
      masters = null;
      st.masters = null;
      renderPlatforms();
      if (hadMasters) { notice(lib.store_error); return; }
      left.replaceChildren(el('p', 'card__error', lib.store_error));
      right.replaceChildren();
      return;
    }
    if (!lib || !lib.has_db) {
      masters = null;
      st.masters = null;
      renderPlatforms();
      if (hadMasters) return;
      left.replaceChildren();
      right.replaceChildren();
      noLocal();
      return;
    }
    render(lib.files || {});
  }
  load();

  // 沒有東西可以畫的三種情況,下一步各不一樣:還沒連 Google Drive(正本就放在那裡)→ 帳號頁;連了、但這台電腦還沒讀過 Drive
  // (export 失敗 = 本機沒有資料,例如第二台電腦;export 沒辦法知道 Drive 上有沒有清單)→ 同步頁把正本拉回來;
  // 讀過了、真的沒有清單 → 搬家。
  function noLocal() {
    if (!drive()) hint(t('webui.playlists.empty_no_local', { button: t('webui.sync.readback') }), t('webui.playlists.go_sync'), '#/sync');
  }
  function empty() {
    if (!drive()) left.appendChild(emptyState(t('webui.playlists.empty')));
  }
  // drive:Google Drive 本身有問題就先說它(畫了回 true);狀態怎麼判斷見 plflow.js 的 driveState。
  function drive() {
    const ds = driveState(st.auth);
    if (ds === 'ok') return false;
    hint(ds === 'missing' ? t('webui.playlists.empty_no_drive') : t('webui.playlists.empty_drive_error'), t('webui.playlists.go_account'), '#/account');
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
    const s = st.auth[p] && st.auth[p].state;
    return s !== 'missing' && s !== 'expired';
  }

  function render(files) {
    // 清單集合照檔名排、清單內容照原順序;敗者的 cid 沿墓碑找勝者(plflow.js 的 indexExport)
    const { masters: pls, track, devices } = indexExport(files);
    st.devices = devices;
    masters = pls;
    st.masters = pls;
    renderPlatforms(); // 讀過平台清單的話,「連著哪一份」跟著新的正本更新
    left.replaceChildren();
    if (!pls.length) { right.replaceChildren(); empty(); return; } // 右欄交給 showItems 換:它要先讀舊畫面的篩選字與捲動位置
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
        if (pl.pid !== flow.dataset.pid && blocked()) return;
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
    (rows[pls.findIndex((pl) => pl.pid === selected)] || rows[0]).click();
  }

  function showItems(pl, track) {
    // 同一份重畫(寫入後、回到這一頁時的重讀):找歌的篩選字與歌曲表的捲動位置帶回去,不要每次都被重置(#124 review)
    const keep = selected === pl.pid
      ? { q: right.querySelector('.pl__filter input')?.value || '', top: right.querySelector('.pl__songs')?.scrollTop || 0 } : null;
    selected = pl.pid;
    right.replaceChildren();
    const items = pl.items || [];
    const head = el('div', 'pl__head');
    const title = el('div', 'pl__head-main');
    title.append(el('h3', 'card__sub', t('webui.playlists.items_title', { name: pl.name, count: items.length })),
      el('p', 'page__note', t('webui.playlists.fresh')));
    // 這一頁唯一的主要動作(視覺規格:一頁一顆膠囊鈕)。沒有任何連結時停用並說原因。
    const sync = btn(t('webui.playlists.sync'), 'btn--primary', () => lf.syncOne(['pl', 'sync', pl.pid], t('webui.playlists.label.sync', { name: pl.name }), flow));
    sync.disabled = !links(pl).length;
    head.append(title, sync);
    if (sync.disabled) head.appendChild(el('p', 'page__note', t('webui.playlists.sync_none')));
    right.appendChild(head);
    const pbar = playBar(items, track);
    if (pbar) right.appendChild(pbar);
    right.appendChild(lf.panel(pl, items, track, flow));
    if (flow.dataset.pid !== pl.pid) { flow.replaceChildren(); flow.dataset.pid = pl.pid; }
    right.appendChild(flow);

    const { tbl, rows, texts, songMeta } = songTable(pl, items, track);
    currentSongRows = songMeta;
    applyPlayingHighlight();
    const wrap = el('div', 'tbl-wrap tbl-wrap--tall pl__songs');
    wrap.appendChild(tbl);
    if (items.length > FILTER_SONGS) {
      const count = el('span', 'pl__shown num');
      right.appendChild(filterBox(t('webui.playlists.filter_songs'), (q) => {
        let shown = 0;
        rows.forEach((tr, i) => { tr.hidden = !!q && !texts[i].includes(q); if (!tr.hidden) shown++; });
        count.textContent = q ? t('webui.playlists.shown', { shown, count: items.length }) : '';
      }, count, keep && keep.q));
    }
    right.appendChild(wrap);
    if (keep) wrap.scrollTop = keep.top;
  }

  function playableIds(items, track, p) {
    const out = [];
    for (const it of items) {
      const id = mappingOf(track(it.cid), p).id;
      if (p === 'spotify' && SPOTIFY_ID.test(id)) out.push(id);
      else if (p === 'apple' && CATALOG_ID.test(id)) out.push(id);
    }
    return out;
  }

  // playBar:歌單頂部依序連播與記憶體隨機連播(走直達 POST /api/player,不掛 data-run、100% 不改動正本順序)。
  function playBar(items, track) {
    const playableProvs = ['spotify', 'apple'].filter((p) => providers.list.includes(p) && canPlay(p) && playableIds(items, track, p).length > 0);
    if (!playableProvs.length) return null;
    let curProv = playableProvs.includes(providers.current) ? providers.current : playableProvs[0];
    const bar = el('div', 'pl__playbar');
    if (playableProvs.length > 1) {
      const sel = select(playableProvs.map((p) => ({ value: p, label: providerName(p) })), curProv);
      sel.setAttribute('aria-label', t('webui.playlists.play_platform'));
      sel.addEventListener('change', () => { curProv = sel.value; });
      bar.appendChild(sel);
    }
    const playAll = el('button', 'btn', t('webui.playlists.play_all'));
    playAll.type = 'button';
    playAll.addEventListener('click', () => {
      const ids = playableIds(items, track, curProv);
      if (ids.length) postPlayerAction(api, notice, { action: 'play', provider: curProv, track_ids: ids, shuffle: false });
    });
    const shuf = el('button', 'btn btn--ghost', t('webui.playlists.shuffle_play'));
    shuf.type = 'button';
    shuf.title = t('webui.playlists.shuffle_note');
    shuf.addEventListener('click', () => {
      const ids = playableIds(items, track, curProv);
      if (ids.length) postPlayerAction(api, notice, { action: 'play', provider: curProv, track_ids: ids, shuffle: true });
    });
    bar.append(playAll, shuf, el('span', 'pl__playbar-note', t('webui.playlists.shuffle_note')));
    return bar;
  }

  function applyPlayingHighlight() {
    const d = lastNow;
    const nowTr = d && d.track;
    for (const { tr, trk } of currentSongRows) {
      let hit = false;
      if (nowTr) {
        if (d.provider && nowTr.id && mappingOf(trk, d.provider).id === nowTr.id) {
          hit = true;
        } else if (nowTr.isrc && Array.isArray(trk.isrc) && trk.isrc.includes(nowTr.isrc)) {
          hit = true;
        } else if (!nowTr.id && nowTr.title && trk.title &&
          String(trk.title).trim().toLowerCase() === String(nowTr.title).trim().toLowerCase() &&
          (trk.artists || [])[0] && (nowTr.artists || [])[0] &&
          String(trk.artists[0]).trim().toLowerCase() === String(nowTr.artists[0]).trim().toLowerCase()) {
          hit = true;
        }
      }
      if (hit) {
        tr.dataset.playing = 'true';
        tr.setAttribute('aria-label', t('webui.playlists.playing_badge'));
      } else {
        delete tr.dataset.playing;
        tr.removeAttribute('aria-label');
      }
    }
  }
  document.addEventListener('capy:now', (ev) => {
    lastNow = ev && ev.detail;
    applyPlayingHighlight();
  });

  // songTable:這一頁自己的表(不是 TSV 的直譯,不用 renderTable):#、歌曲(曲名 + 歌手 · 專輯)、時長、每個平台一欄。
  // 窄的時候每首拆成多行(app.css 的容器查詢把表格元素改成 block / flex),所以每個元素都標上表格的 role,報讀才還是表格。
  function songTable(pl, items, track) {
    const linked = new Set(links(pl).map(([p]) => p));
    // 平台欄:這份清單連著那個平台,或至少一首有那個平台的對應才出現;Apple 的格子只有 ▶,這台放不了就整欄不出現。
    const cols = COLUMNS.filter((p) => providers.list.includes(p) && (p !== 'apple' || canPlay('apple')) &&
      (linked.has(p) || items.some((it) => mappingOf(track(it.cid), p).id)));
    const tbl = el('table', 'tbl pl__tbl');
    tbl.setAttribute('role', 'table');
    const cg = el('colgroup');
    cg.append(el('col', 'pl__c-pos'), el('col', 'pl__c-song'), el('col', 'pl__c-dur'), ...cols.map((p) => el('col', `pl__c-${p}`)), el('col', 'pl__c-wiki'));
    const thead = el('thead');
    const hr = el('tr');
    hr.setAttribute('role', 'row');
    for (const text of ['#', t('webui.playlists.col.song'), t('webui.playlists.col.duration'), ...cols.map(providerName), '']) {
      const th = el('th', null, text);
      th.setAttribute('role', 'columnheader');
      hr.appendChild(th);
    }
    thead.appendChild(hr);
    const tbody = el('tbody');
    const rows = [];
    const texts = [];
    const songMeta = [];
    items.forEach((it, i) => {
      const trk = track(it.cid);
      const title = (trk.title || '').trim(); // 同 CLI 的 TrimSpace:只有空白的曲名當成沒有(不然 Wiki 會問到正在播的那首)
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
      // Wiki(計畫 §3.3):曲名取正本的 title,沒有(這台沒有這首的資料)就不給——空的 --title 會變成問正在播的那首
      const wk = cell('row-actions pl__wiki');
      if (title) {
        const b = btn(t('webui.playlists.wiki'), 'btn--ghost', () => openWiki(title, artists));
        b.setAttribute('aria-label', t('webui.playlists.wiki_label', { title }));
        wk.appendChild(b);
      }
      tr.appendChild(wk);
      tbody.appendChild(tr);
      rows.push(tr);
      texts.push(`${title}\n${artists}\n${trk.album || ''}`.toLowerCase());
      songMeta.push({ tr, trk });
    });
    tbl.append(cg, thead, tbody);
    return { tbl, rows, texts, songMeta };
  }

  // ── 各平台上的清單(計畫 §3.4)與納入(§3.5)──
  // readPlatforms:依序讀這台有登入的每個平台,下一家在上一家的 onExit 裡送;某一家讀不到就在那一段說原因,其他家照讀。
  function readPlatforms() {
    if (blocked()) return;
    const todo = COLUMNS.filter((p) => providers.list.includes(p) && !loggedOut(st.auth, p));
    const got = {};
    // YouTube 有登入卻缺帳號資料:照送 pl list 只會被拒,不讀,那一段說要重新登入(不要默默消失、也不要說成沒登入任何平台)
    for (const p of COLUMNS) if (providers.list.includes(p) && relogin(st.auth, p)) got[p] = { rows: [], relogin: true };
    const next = () => {
      const p = todo.shift();
      if (!p) { st.platLists = got; renderPlatforms(); return; }
      readLists(con, p, t('webui.playlists.list_label', { platform: providerName(p) }), (res, reason, code) => {
        got[p] = res;
        // 使用者按了中止:後面幾家不讀(同 console.js 的 isCancelled),但照樣列出來、說沒讀——整段不見會看起來像沒有那個平台(#125 review 第 4 點)
        if (reason === 'cancelled' && code !== 0) for (const q of todo.splice(0)) got[q] = { rows: [], skipped: true };
        next();
      });
    };
    next();
  }

  function renderPlatforms() {
    const platLists = st.platLists;
    if (!platLists) return;
    const ps = COLUMNS.filter((p) => platLists[p]); // 照 COLUMNS 的順序:要重新登入的 YouTube 先塞進去也不會排到最前面(#125 review 第 1 點)
    if (!ps.length) {
      const a = el('a', 'wiz__link', t('webui.playlists.go_account'));
      a.href = '#/account';
      platList.replaceChildren(emptyState(t('webui.playlists.platforms.none')), a);
      return;
    }
    // 還沒讀到這台的正本:分不出每一份是不是已經交給 capy 了,只列出來、不標狀態也不給納入(左欄說了下一步)
    const unknown = masters ? [] : [el('p', 'page__note', t('webui.playlists.platforms.unknown'))];
    platList.replaceChildren(...unknown, ...ps.map((p) => {
      const { rows, error } = platLists[p];
      const g = el('div', 'pl__plat-group');
      g.appendChild(el('h4', 'pl__plat-name', providerName(p)));
      if (platLists[p].relogin) {
        const a = el('a', 'wiz__link', t('webui.playlists.go_account'));
        a.href = '#/account';
        g.append(el('p', 'page__warn', t('webui.playlists.links.relogin', { platform: providerName(p) })), a);
      }
      if (error) g.appendChild(el('p', 'page__warn', t('webui.playlists.platforms.error', { platform: providerName(p), error })));
      else if (platLists[p].relogin) { /* 上面說了 */ }
      else if (platLists[p].skipped) g.appendChild(el('p', 'page__note', t('webui.playlists.platforms.skipped')));
      else if (!rows.length) g.appendChild(el('p', 'page__note', t('webui.playlists.platforms.empty', { platform: providerName(p) })));
      for (const r of rows) g.appendChild(platRow(p, r));
      return g;
    }));
  }

  // platRow:一份平台清單。連著哪一份正本(links 裡的 id 相同)→ 說出來;有同名的正本(不分大小寫,同 CLI 的 find)→ 不給納入,
  // 說要先在平台上改名(納入會連到那一份、第一次 pull 照平台順序重排它;真正的護欄是 --new-only,這裡只是提示);其餘給「納入」。
  function platRow(p, r) {
    const row = el('div', 'pl__plat');
    row.dataset.platform = p;
    const main = el('span', 'pl__plat-main');
    const acts = el('span', 'pl__link-acts');
    const sp = p === 'spotify' && spotifyLink('playlist', r.id, r.name); // S7:Spotify 的清單都連回去(連結與按鈕是兄弟,不包進按鈕)
    if (sp) acts.appendChild(sp);
    const ls = listState(masters, p, r);
    const dup = ls.kind === 'same_name' || ls.kind === 'same_name_taken';
    let state = '';
    if (ls.kind === 'unknown') { /* 分不出來:不標、不給納入 */ }
    else if (ls.kind === 'linked') state = t('webui.playlists.platforms.linked', { name: ls.master.name });
    else if (dup) {
      // 不給納入(會連到那一份、第一次 pull 照平台順序重排它)、不指去搬家:要一起同步就連到那份正本(合併,決策 62),要分開就先改名
      // 那份正本已經連著這個平台的另一份:一份正本在每個平台只連一份,只能先改名(會變成另一份正本)
      if (ls.kind === 'same_name_taken') state = t('webui.playlists.platforms.same_name_taken', { name: ls.master.name, platform: providerName(p) });
      else {
        state = t('webui.playlists.platforms.same_name', { name: ls.master.name, platform: providerName(p) });
        acts.appendChild(btn(t('webui.sync.same_name_go', { name: ls.master.name }), '', () => lf.askMerge(p, ls.master, pflow, r.id)));
      }
    }
    else { state = t('webui.playlists.platforms.unlinked'); acts.appendChild(btn(t('webui.playlists.platforms.adopt'), '', () => lf.adopt(p, r, pflow))); }
    if (ls.kind !== 'unknown') row.dataset.state = dup ? 'same_name' : ls.kind;
    const count = /^\d+$/.test(r.tracks || '') ? t('webui.playlists.platforms.count', { count: Number(r.tracks) }) : '';
    main.append(el('span', 'pl__name', r.name), el('small', null, [count, state].filter(Boolean).join(' · ')));
    row.append(main, acts);
    return row;
  }

  // ── 歌曲 wiki 對話框(計畫 §3.3,Q2 A):就地開,在 #page-playlists 裡(setup 的表單用它當 promptHost,reveal() 才不會切到主控台)。
  // showModal() 會讓 dock 整個 inert——中止鈕與 #notice 都看不到、按不到——所以停止、收尾那句、失敗的原因都畫在對話框裡。
  // 關掉對話框(✕ / Esc)= 停止:把序列槽還給播放。wiki 不寫入,con.stop() 一次就停(不用第二次確認)。
  let dlg, dX, dTitle, dBy, dStatus, dOut, dPrompts, dFoot;
  let wikiSong = null;   // { title, artists }
  let dlgRunning = false; // 對話框發出的命令(wiki / wiki setup)在跑
  root.appendChild(wikiDialog()); // 在上面這幾個 let 之後才建(建在頁首那一行會碰到 TDZ)
  function wikiDialog() {
    dlg = el('dialog', 'pl__wiki-dlg');
    const top = el('div', 'pl__wiki-top');
    const head = el('div');
    dTitle = el('h3', 'pl__wiki-title');
    dBy = el('span', 'pl__wiki-by');
    head.append(dTitle, dBy);
    dX = el('button', 'btn btn--ghost btn--icon', '✕'); // 純 UI 控制:命令在跑時也要按得到,不標 data-run
    dX.type = 'button';
    dX.setAttribute('aria-label', t('webui.playlists.wiki_close'));
    dX.addEventListener('click', () => dlg.close());
    top.append(head, dX);
    dStatus = el('p', 'page__note pl__wiki-status');
    dStatus.setAttribute('role', 'status');
    dOut = el('div', 'wiki__out');
    dPrompts = el('div', 'pl__wiki-prompts');
    dFoot = el('div', 'form-row pl__wiki-foot');
    dlg.append(top, dStatus, dFoot, dPrompts, dOut); // 按鈕在回答上面:串流中長出來的字不會把「中止」推出畫面
    dlg.addEventListener('close', () => { if (dlgRunning) con.stop(); });
    return dlg;
  }

  function openWiki(title, artists) {
    wikiSong = { title, artists };
    dTitle.textContent = title;
    dBy.textContent = artists;
    dlg.setAttribute('aria-label', t('webui.playlists.wiki_label', { title }));
    if (!dlg.open) dlg.showModal();
    ask(false);
  }

  // ask:capy wiki --title=<曲名> [--artist=<歌手>]。用 = 的寫法:web 的擋字檢查對每個參數做 strings.Cut(a, "="),
  // 歌名剛好是 --header 這類字、或以 - 開頭時分開寫會被擋或被當成旗標(#121 review 第 4 點)。歌手用 ", " 串,
  // 跟「正在播」那條路算快取 key 的方式一樣(ai.WikiCacheKey)。
  function ask(refresh) {
    const { title, artists } = wikiSong;
    dOut.replaceChildren();
    dPrompts.replaceChildren();
    wikiSay(t('webui.playlists.wiki_running'));
    const stop = el('button', 'btn', t('webui.console.stop')); // dock 的中止鈕被對話框蓋住了,這裡另給一顆
    stop.type = 'button';
    // 連點「查詢 / 再問一次」的第二下會落在同一個位置剛換上的這顆:detail > 1 不算(鍵盤觸發的 click 是 0,照停)
    stop.addEventListener('click', (ev) => { if (ev.detail > 1) return; stop.textContent = t('webui.console.stopping'); con.stop(); });
    foot(stop);
    const r = wikiRenderer(dOut);
    const split = lineSplitter((l) => r.line(l));
    dlgRunning = true;
    con.run('', {
      onStdout: (s) => split.push(s),
      onExit: (code, msg, reason) => {
        dlgRunning = false;
        split.end();
        // 做完也說一句:dock 的報讀(barSR)被對話框的 inert 蓋住,不說的話螢幕閱讀器聽不到結束
        if (code === 0) { wikiSay(t('webui.playlists.wiki_done')); foot(btn(t('webui.wiki.refresh'), 'btn--ghost', () => ask(true))); return; }
        const again = btn(t('webui.wiki.ask'), code > 0 ? 'btn--ghost' : '', () => ask(false));
        if (reason === 'cancelled') { wikiSay(t('webui.playlists.wiki_stopped')); foot(again); return; }
        // 失敗的原因(沒設定 AI 端點、端點連不上、伺服器拒絕、序列槽被佔著…)畫在這裡:dock 的 notice 被對話框遮住了。
        wikiSay(why(msg), true);
        // 命令真的跑了而失敗(code > 0)才給「設定 AI」:不看錯誤字串決定(頁面拿不到那句的 key),同 wiki 頁的設定鈕;
        // 被拒絕(-1:refused / busy)命令根本沒跑,設定也救不了。
        foot(...(code > 0 ? [btn(t('webui.wiki.setup'), '', setup)] : []), again);
      },
    }, { args: ['wiki', `--title=${title}`, ...(artists ? [`--artist=${artists}`] : []), ...(refresh ? ['--refresh'] : [])], label: t('webui.wiki.label.ask', { song: title }) });
  }

  // setup:capy wiki setup,表單就地出現在對話框裡(promptHost);設定好了就可以再問一次。
  function setup() {
    dOut.replaceChildren();
    dPrompts.replaceChildren();
    wikiSay('');
    foot(); // 精靈跑的時候沒有自己的中止:✕ 就是停(表單裡的 Esc 是取消那一題,console.js 已經 preventDefault、不會關掉對話框)
    dlgRunning = true;
    let closedBy = '';
    con.run('', {
      onPromptClosed: (ev) => { closedBy = ev.reason; },
      onExit: (code, msg, reason) => {
        dlgRunning = false;
        if (code === 0) { wikiSay(t('webui.playlists.wiki_ready')); foot(btn(t('webui.wiki.ask'), '', () => ask(false))); return; }
        // 表單被關掉(✕ / Esc)或等太久:CLI 回的是 huh 的 "user aborted",不給人看原文(決策 50)。也不說「沒有寫入」:
        // 選模型之前端點、金鑰、母語已經存了。不比對錯誤字串,看機器欄位。
        const unfinished = code === 1 && (reason === 'timeout' || closedBy === 'dismissed');
        if (reason === 'cancelled') wikiSay(t('webui.playlists.wiki_stopped'));
        else if (unfinished) wikiSay(t('webui.playlists.wiki_setup_unfinished'));
        else wikiSay(why(msg), true);
        foot(btn(t('webui.wiki.setup'), '', setup));
      },
    }, { args: ['wiki', 'setup'], label: t('webui.wiki.label.setup'), promptHost: dPrompts });
  }

  // foot:換掉收尾區的按鈕。按下的那顆被換掉時焦點會掉到 body、鍵盤使用者就失去位置(同 console.js 的中止鈕不用 disabled 的理由):
  // 焦點不在對話框裡了就交給新的第一顆,沒有按鈕就交給 ✕。
  function foot(...bs) {
    dFoot.replaceChildren(...bs);
    if (dlg.open && !dlg.contains(document.activeElement)) (bs[0] || dX).focus();
  }

  const why = (msg) => (msg || '').replace(/^Error: /, '') || t('webui.playlists.wiki_failed');
  function wikiSay(text, warn) {
    dStatus.className = warn ? 'page__warn pl__wiki-status' : 'page__note pl__wiki-status';
    dStatus.textContent = text;
  }

  // platformCell:一首歌在一個平台的那一格(計畫 §3.2 的表)。
  function platformCell(p, m, title) {
    const td = cell('row-actions pl__cell');
    const platform = providerName(p);
    td.dataset.platform = platform; // 窄版沒有表頭可以對:CSS 用 attr() 把平台名寫在格子前面
    const { id, pinned } = norm(m);
    const note = (cls, text, why) => { const s = el('span', cls, text); s.title = why; td.appendChild(s); return td; };
    if (!id) {
      return note('pl__none', '—', pinned ? t('webui.playlists.cell.pinned_none', { platform }) : t('webui.playlists.cell.none', { platform }));
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

  // playButton:走直達端點 POST /api/player,不掛 data-run、不被背景 sync / wiki 鎖死;Apple 只打開未開始播時照印回傳訊息。
  function playButton(p, id, title) {
    const b = el('button', 'btn btn--ghost btn--icon', '▶');
    b.type = 'button';
    b.setAttribute('aria-label', t('webui.playlists.play_on', { platform: providerName(p), title }));
    b.addEventListener('click', () => postPlayerAction(api, notice, { action: 'play', provider: p, id }));
    return b;
  }

  document.addEventListener('capy:player-settled', () => load());

  // 回到這一頁時直接讀唯讀 /api/library(瞬開不搶 runMu);帶 pid 就打開那一份。
  return { refresh: (pid) => { if (pid) selected = pid; return load(); } };
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
