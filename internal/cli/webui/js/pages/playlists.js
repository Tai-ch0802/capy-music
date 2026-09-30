// playlists.js:#/playlists —— 左邊是 capy 保管的清單(正本;來源 export:唯讀、只讀本機 state.db),右邊是選中那份的歌曲表
// (計畫 docs/superpowers/plans/2026-09-30-my-playlists-redesign.md,決策 61)。順序永遠照清單本來的順序,沒有排序、沒有拖曳
// (決策 38);篩選只藏列。每首歌每個平台一欄:▶ 用 capy 在那個平台放、「在 Spotify 上聽」/「開啟」開新分頁(計畫 §3.2)。
// 右欄上面是連結面板(正本連到哪幾份平台清單)與就地的寫入流程(同步、在 X 建一份、找對應;計畫 §3.5):寫入一律走 CLI 自己的
// 變更表與確認,頁面絕不代加確認或越過的旗標(決策 46)。第二段是平台清單(pl list)。顯示 Spotify 的曲目或清單的地方
// 都連回 Spotify(計畫 2026-09-24 §1.7 S7)。
import { el, btn, field, input, select, providerOptions, providerName, emptyState, pageHead } from './common.js';
import { renderTable, linkColumn, spotifyLink, youtubeLink, mmss, SPOTIFY_ID } from '../table.js';
import { parseStatus } from './account.js';
import { changeTable } from './sync.js';
import { t } from '../i18n.js';

const COLUMNS = ['spotify', 'apple', 'youtube']; // 會佔一欄的平台:本機沒有播放、也沒有網頁,不佔欄;也是找得到對應、建得出清單的平台
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

  // auth:`auth status --json`(不連網)。▶ 要不要給、連結面板與空白態怎麼說都看它;讀不到 = {} = 不知道,當成有登入、是自己的。
  let auth = {};
  let devices = [];  // export 的 manifest.json:裝置 id → 名稱(連在別台電腦的本機清單說得出是哪一台)
  let selected = ''; // 選中的那份(pid):寫入後、回到這一頁時重讀,畫回同一份
  // flow:寫入命令的就地區塊(步驟條、狀態句、變更表、提示;表在上、提示在下——CLI 先印表再問確認)。
  // 只在切到別份清單時清空;重讀後搬進新畫的右欄,收尾那句不會因為重讀而消失。
  const flow = el('div', 'pl__flow');
  // flowBusy:流程區裡有寫入命令在跑(提示畫在它裡面)。這時換一份清單或按「在 X 建一份」會清掉流程區、把提示一起拿掉,
  // 命令就卡在序列槽裡等一個看不到的提示:擋下並說明(同 btn() 被擋時的 capy:busy)。▶ 與自動重讀不算,照常可以換清單。
  let flowBusy = false;
  const blocked = () => { if (flowBusy) document.dispatchEvent(new Event('capy:busy')); return flowBusy; };
  // 兩個命令都 quiet:export 的整份 JSON 不灌進主控台。第二個在第一個的 onExit 裡送——用 await 串的話,
  // 排隊中的 con.idle 會先被叫醒而插隊(console.js:onExit 先於 wake())。畫面等資料到了才換,重讀時不閃。
  function load() {
    let status = '';
    con.run('', {
      onStdout: (s) => { status += s; },
      onExit: (code) => { if (code === 0) auth = parseStatus(status); readExport(); }, // 讀不到就沿用上一次讀到的
    }, { args: ['auth', 'status', '--json'], label: t('webui.move.label.status'), quiet: true });
  }

  function readExport() {
    let text = '';
    con.run('export', {
      onStdout: (s) => { text += s; },
      onExit: (code, msg, reason) => {
        if (code !== 0) {
          // 已經畫過清單(寫入後、回到這一頁時的重讀):保留畫面與流程區裡「停在哪一步」那句,原因 Console 已經放進 notice
          if (selected) return;
          left.replaceChildren();
          right.replaceChildren();
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
          left.replaceChildren(el('p', 'card__error', t('webui.playlists.bad_export', { error: e.message })));
          right.replaceChildren();
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
    devices = ((files['manifest.json'] || {}).devices) || [];
    left.replaceChildren();
    right.replaceChildren();
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
    selected = pl.pid;
    right.replaceChildren();
    const items = pl.items || [];
    const head = el('div', 'pl__head');
    const title = el('div', 'pl__head-main');
    title.append(el('h3', 'card__sub', t('webui.playlists.items_title', { name: pl.name, count: items.length })),
      el('p', 'page__note', t('webui.playlists.fresh')));
    // 這一頁唯一的主要動作(視覺規格:一頁一顆膠囊鈕)。沒有任何連結時停用並說原因。
    const sync = btn(t('webui.playlists.sync'), 'btn--primary', () => runOne(['pl', 'sync', pl.pid], t('webui.playlists.label.sync', { name: pl.name })));
    sync.disabled = !links(pl).length;
    head.append(title, sync);
    if (sync.disabled) head.appendChild(el('p', 'page__note', t('webui.playlists.sync_none')));
    right.append(head, linkPanel(pl, items, track));
    if (flow.dataset.pid !== pl.pid) { flow.replaceChildren(); flow.dataset.pid = pl.pid; }
    right.appendChild(flow);

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
      (linked.has(p) || items.some((it) => mappingOf(track(it.cid), p).id)));
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

  // ── 連結面板(計畫 §3.5):Google Drive 正本 → 每個平台一列;本機只在連著時出現 ──
  function linkPanel(pl, items, track) {
    const box = el('div', 'pl__links');
    const hub = el('div', 'pl__hub');
    hub.append(el('strong', null, t('webui.playlists.links.hub')), el('span', null, t('webui.playlists.links.hub_sub')));
    const list = el('div', 'pl__link-rows');
    // 算對應用不重複、這台電腦有資料的歌(懸空的 cid resolve 也會跳過)
    const cids = [...new Set(items.map((it) => it.cid))].filter((cid) => track(cid).title !== undefined || track(cid).mappings);
    for (const p of providers.list) {
      const st = linkState(p, pl);
      if (st) list.appendChild(linkRow(p, st, pl, cids, track));
    }
    box.append(hub, list);
    return box;
  }

  // linkState:這份正本在一個平台上的狀態。判斷順序:這台有沒有登入 → 是不是別台電腦 / 別的帳號的 → 才是自己的
  //(auth 讀不到 = 當成有登入、是自己的,同 ▶)。沒連的本機不顯示:本機不能 --create,連既有的 M3U 會碰到順序問題。
  function linkState(p, pl) {
    const id = (pl.links || {})[p];
    const st = auth[p] && auth[p].state;
    const out = st === 'missing' || st === 'expired' || (p === 'youtube' && auth.youtube && !auth.youtube.channel_id);
    if (typeof id !== 'string' || !id) {
      if (!COLUMNS.includes(p)) return null;
      return { kind: out ? 'unlinked_out' : 'unlinked' };
    }
    if (out) return { kind: 'out', id };
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

  function linkRow(p, st, pl, cids, track) {
    const platform = providerName(p);
    const row = el('div', 'pl__link');
    row.dataset.state = st.kind;
    row.dataset.platform = p;
    const info = el('span', 'pl__link-info');
    const acts = el('span', 'pl__link-acts');
    const account = () => { const a = el('a', 'btn btn--ghost', t('webui.playlists.go_account')); a.href = '#/account'; return a; };
    const open = () => { const a = playlistLink(p, st.id, pl.name); if (a) acts.appendChild(a); };
    let state = t('webui.playlists.links.linked');
    if (st.kind === 'linked') {
      const missing = cids.filter((cid) => missingOn(track(cid), p)).length;
      info.textContent = missing ? t('webui.playlists.links.missing', { count: missing }) : t('webui.playlists.links.all_mapped');
      if (missing) info.dataset.warn = '';
      open();
      if (missing && COLUMNS.includes(p)) {
        acts.append(
          btn(t('webui.playlists.links.resolve'), '', () => runOne(['resolve', pl.pid, '--provider', p], t('webui.playlists.label.resolve', { name: pl.name, platform }))),
          btn(t('webui.playlists.links.review'), 'btn--ghost', () => runOne(['resolve', pl.pid, '--provider', p, '--review'], t('webui.playlists.label.review', { name: pl.name, platform }))));
      }
    } else if (st.kind === 'out') {
      info.textContent = t('webui.playlists.links.logged_out', { platform });
      acts.appendChild(account());
      open();
    } else if (st.kind === 'foreign') {
      info.textContent = st.device ? t('webui.playlists.links.foreign_device', { device: st.device }) : t('webui.playlists.links.foreign_account');
    } else {
      state = t('webui.playlists.links.unlinked');
      if (st.kind === 'unlinked_out') {
        info.textContent = t('webui.playlists.links.logged_out', { platform });
        acts.appendChild(account());
      } else {
        const known = cids.filter((cid) => mappingOf(track(cid), p).id).length;
        if (known) info.textContent = t('webui.playlists.links.known', { count: known, platform });
        const b = el('button', 'btn', t('webui.playlists.links.create', { platform })); // 只打開說明,還不送命令:不標 data-run
        b.type = 'button';
        b.addEventListener('click', () => askCreate(p, pl));
        acts.appendChild(b);
      }
    }
    row.append(el('span', 'pl__link-name', platform), el('span', 'pl__link-state', state), info, acts);
    return row;
  }

  // playlistLink:平台上那份清單的網頁(Apple 的資料庫清單沒有公開網址;本機沒有網頁)。
  function playlistLink(p, id, name) {
    if (p === 'spotify') return spotifyLink('playlist', id, name);
    if (p !== 'youtube') return null;
    const a = el('a', 'btn btn--ghost', t('webui.search.open_youtube'));
    a.href = `https://music.youtube.com/playlist?list=${encodeURIComponent(id.split('/').pop())}`;
    a.target = '_blank';
    a.rel = 'noopener';
    a.setAttribute('aria-label', `${t('webui.search.open_youtube')}: ${name}`);
    return a;
  }

  // ── 就地的寫入流程(計畫 §3.5)──
  function openFlow(stepTexts) {
    const steps = el('ol', 'wiz__steps');
    for (const s of stepTexts || []) steps.appendChild(el('li', 'wiz__step', s));
    const status = el('p', 'page__note pl__flow-status');
    status.setAttribute('role', 'status');
    const table = el('div', 'pl__flow-table');
    const host = el('div', 'pl__flow-prompts'); // promptHost:在 #page-playlists 裡,reveal() 不會把人丟去主控台
    flow.replaceChildren(...(stepTexts ? [steps] : []), status, table, host);
    return { steps, status, table, host };
  }

  function markStep(f, n) {
    [...f.steps.children].forEach((li, i) => { // 真的 DOM 的 children 是 HTMLCollection,沒有 forEach
      li.dataset.state = i < n ? 'done' : i === n ? 'now' : 'todo';
      if (i === n) li.setAttribute('aria-current', 'step'); else li.removeAttribute('aria-current');
    });
  }

  // step:跑一個會寫入的命令;done(code, outcome, wrote) 在 onExit 裡叫——下一步要在那裡送(見 load 的註解)。
  // 表在上、提示在下:table 事件一定先到(CLI 先印表再 confirmWrite),畫進表格區就自然在提示上面。
  // wrote:這個頁面從不代加 --yes,sync / resolve / pull 的每一次寫入都要先有人答應確認(或逐首裁決)——
  // exit 0 而且答過提示才算有寫入;只收到表(只有 skip 列、只有待決定的列)不算。
  function step(f, args, label, done) {
    let closedBy = '';
    let answered = false;
    let pending = 0; // resolve 表裡等人逐首決定的列(review / conflict)
    f.table.replaceChildren();
    flowBusy = true;
    con.run('', {
      onTable: (h, r) => {
        const a = h.indexOf('ACTION');
        pending = h.includes('CONFIDENCE') ? r.filter((x) => x[a] === 'review' || x[a] === 'conflict').length : 0;
        f.table.replaceChildren(changeTable(h, r));
      },
      onPromptClosed: (ev) => { closedBy = ev.reason; if (ev.reason === 'answered') answered = true; },
      onExit: (code, msg, reason) => {
        flowBusy = false;
        const wrote = code === 0 && answered;
        done(code, outcome(code, msg, reason, closedBy, wrote, pending), wrote);
      },
    }, { args, label, promptHost: f.host });
  }

  // runOne:單一命令(同步這份清單、找對應、逐首決定);收尾後重讀。
  function runOne(args, label) {
    const f = openFlow();
    step(f, args, label, (code, o) => { say(f, o); load(); });
  }

  // 在 X 建一份:先說清楚會發生什麼,按「開始」才依序跑三個命令;第一步(建立並連上)沒有 CLI 的確認,「開始」就是同意。
  function askCreate(p, pl) {
    if (blocked()) return;
    const platform = providerName(p);
    const card = el('div', 'pl__flow-card');
    card.appendChild(el('p', null, t('webui.playlists.create.explain', { platform, name: pl.name, start: t('webui.playlists.create.start') })));
    if (p === 'apple') card.appendChild(el('p', 'page__note', t('webui.playlists.create.apple'))); // 決策 49:可能,不是必然
    const acts = el('div', 'form-row');
    const no = el('button', 'btn btn--ghost', t('webui.playlists.create.cancel'));
    no.type = 'button';
    no.addEventListener('click', () => flow.replaceChildren());
    acts.append(btn(t('webui.playlists.create.start'), '', () => create(p, pl)), no);
    card.appendChild(acts);
    flow.replaceChildren(card);
  }

  function create(p, pl) {
    const platform = providerName(p);
    const name = pl.name;
    const f = openFlow([t('webui.playlists.step.create'), t('webui.playlists.step.resolve'), t('webui.playlists.step.push')]);
    markStep(f, 0);
    // --new-only:頁面讀的 export 可能過時;平台那邊別台 / 別帳號剛連上的,CLI 會擋下來而不是接管(計畫 §3.5 前置 1)。
    const stop2 = t('webui.playlists.create.stop2', { platform, name, button: t('webui.playlists.links.resolve') });
    step(f, ['pl', 'link', '--new-only', pl.pid, p, '--create'], t('webui.playlists.label.create', { name, platform }), (c1, o1) => {
      if (c1 !== 0) { say(f, o1, t('webui.playlists.create.stop1')); load(); return; }
      markStep(f, 1);
      step(f, ['resolve', pl.pid, '--provider', p], t('webui.playlists.label.resolve', { name, platform }), (c2, o2, w2) => {
        if (c2 !== 0) { say(f, o2, stop2); load(); return; }
        markStep(f, 2);
        step(f, ['pl', 'sync', pl.pid, '--provider', p], t('webui.playlists.label.push', { name, platform }), (c3, o3, w3) => {
          if (c3 === 0 && w3) { markStep(f, 3); say(f, { text: t('webui.playlists.create.done', { platform, name }) }); }
          else if (c3 === 0) say(f, { text: t('webui.playlists.create.none', { platform, name, button: t('webui.playlists.links.review') }) }); // 一首都沒加:照實說
          else say(f, o3, w2 ? t('webui.playlists.create.stop3', { platform, button: t('webui.playlists.sync') }) : stop2);
          load();
        });
      });
    });
  }

  // say:收尾那一句;lead 是這個流程自己的說明(停在哪一步),接在 CLI 的原因前面。
  function say(f, o, lead) {
    f.status.className = o.warn ? 'page__warn pl__flow-status' : 'page__note pl__flow-status';
    f.status.textContent = [lead, o.text, o.extra].filter(Boolean).join(' ');
    if (o.console) {
      const a = el('a', 'wiz__link', t('webui.move.see_console'));
      a.href = '#/console';
      f.status.appendChild(el('span', null, ' '));
      f.status.appendChild(a);
    }
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

  // 回到這一頁時重讀(app.js 的 route;別頁寫入之後這一頁不會過時)。有命令在跑就等它結束。
  return { refresh: () => con.idle(load) };
}

// mappingOf:一首歌在一個平台上的對應 { id, pinned }(不是字串的壞資料 = 沒有)。格子(§3.2)與連結面板的計數(§3.5)共用。
const norm = (m) => ({ id: m && typeof m.id === 'string' ? m.id : '', pinned: !!(m && m.pinned) });
const mappingOf = (trk, p) => norm((trk.mappings || {})[p]);

// missingOn:缺對應 = 沒有 id、也不是你標過「這個平台沒有這首」(那種 resolve 幫不了,不該叫人去找)。
const missingOn = (trk, p) => {
  const m = mappingOf(trk, p);
  return !m.id && !m.pinned;
};

// outcome:寫入命令的收尾(計畫 §3.5 的表;照 move.js 的做法用 onPromptClosed 分辨關掉 / 逾時)。wrote 見 step();
// pending:resolve 沒有自動寫入、只剩等人決定的列時,指到頁面上的「逐首決定」(CLI 那句叫人去終端機,這裡看不到)。
function outcome(code, msg, reason, closedBy, wrote, pending) {
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
