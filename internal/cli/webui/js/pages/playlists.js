// playlists.js:#/playlists —— 左邊是 canonical 清單(來源 export:唯讀、只讀本機 state.db),右邊是選中清單的內容。
// 第二段是平台清單(pl list / pl show)。順序永遠照清單本來的順序,沒有排序、沒有拖曳(決策 38)。
// 顯示 Spotify 的曲目或清單的地方都連回 Spotify(table.js 的 spotifyLink;計畫 2026-09-24 §1.7 S7)。
import { el, quote, btn, field, select, providerOptions, providerName, emptyState, pageHead } from './common.js';
import { renderTable, linkColumn, spotifyLink } from '../table.js';
import { t } from '../i18n.js';

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

  root.append(btn(t('webui.playlists.refresh'), 'btn--ghost', load), cols, el('h3', 'card__sub', t('webui.playlists.platform_heading')), pbar, pout);
  pbar.append(
    btn(t('webui.playlists.list'), '', () => {
      const p = prov.value; // 按下去那一刻的平台:命令跑的時候選單可能被換掉,表要照送出去的那一家判斷
      con.run(`pl list --provider ${p}`, {
        onTable: (h, r) => pout.replaceChildren(wrapTable(h, r, p === 'spotify' && ((row) => ({ kind: 'playlist', id: row[h.indexOf('ID')], title: row[h.indexOf('NAME')] })))),
      }, { label: t('webui.playlists.list_label', { platform: providerName(p) }) });
    }),
  );
  con.idle(load); // 同帳號頁:有命令在跑就等它結束,不要撞上它、畫成「沒有清單」

  function load() {
    let text = '';
    left.replaceChildren();
    right.replaceChildren();
    con.run('export', {
      onStdout: (s) => { text += s; },
      onExit: (code) => {
        if (code !== 0) { left.appendChild(emptyState(t('webui.playlists.empty'))); return; }
        let files;
        try {
          files = JSON.parse(text);
        } catch (e) {
          left.appendChild(el('p', 'card__error', t('webui.playlists.bad_export', { error: e.message })));
          return;
        }
        render(files);
      },
    }, { label: t('webui.playlists.load_label') });
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
    if (!pls.length) { left.appendChild(emptyState(t('webui.playlists.empty'))); return; }
    for (const pl of pls) {
      const row = el('button', 'pl__item');
      row.type = 'button';
      row.appendChild(el('span', 'pl__name', pl.name || pl.pid));
      const chips = el('span', 'pl__chips');
      for (const [linked] of links(pl)) chips.appendChild(el('span', 'chip', `${linked} ✓`));
      row.appendChild(chips);
      row.appendChild(el('span', 'pl__count num', String((pl.items || []).length)));
      row.addEventListener('click', () => {
        for (const other of left.querySelectorAll('.pl__item')) other.classList.remove('is-active');
        row.classList.add('is-active');
        showItems(pl, track);
      });
      left.appendChild(row);
    }
    left.firstElementChild.click();
  }

  function showItems(pl, track) {
    right.replaceChildren();
    right.appendChild(el('h3', 'card__sub', t('webui.playlists.items_title', { name: pl.name, count: (pl.items || []).length })));
    const items = pl.items || [];
    const rows = items.map((it) => {
      const trk = track(it.cid);
      return [it.cid, trk.title || t('webui.playlists.no_track_data'), (trk.artists || []).join(', '), trk.album || '', String(trk.duration_ms || 0)];
    });
    // 正本的曲名、歌手不一定是從 Spotify 來的(第一個看到這首的平台給的,export 沒記來源):有 Spotify 對應的每一列都連,
    // 多連不違規,少連才是。對應是 capy 認定的同一首(自動寫入要 85 分以上,或人工裁決)。
    right.appendChild(wrapTable(['CID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'], rows, (row, i) => {
      const m = (track(items[i].cid).mappings || {}).spotify;
      return m ? { kind: 'track', id: m.id, title: row[1] } : null;
    }));
    // 每個連著的平台各一顆。送 links 裡的 id,不送正本的名稱:pl show 用名稱找平台上的清單,改過名或有同名清單就會找錯。
    for (const [p, id] of links(pl)) {
      right.appendChild(btn(t('webui.playlists.view_on', { platform: providerName(p) }), 'btn--ghost', () =>
        con.run(`pl show ${quote(id)} --provider ${p}`, {
          onTable: (h, r) => pout.replaceChildren(wrapTable(h, r, p === 'spotify' && ((row) => ({ kind: 'track', id: row[h.indexOf('ID')], title: row[h.indexOf('TITLE')] })))),
        }, { label: t('webui.playlists.view_label', { platform: providerName(p), name: pl.name }) })));
    }
    // 連著的 Spotify 清單本身(放在按鈕後面,不放進左欄那一列:那一列整個是 <button>,裡面不能再放連結)。
    const sp = spotifyLink('playlist', (pl.links || {}).spotify, pl.name);
    if (sp) right.appendChild(sp);
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
