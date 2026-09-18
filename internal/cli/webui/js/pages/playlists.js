// playlists.js:#/playlists —— 左邊是 canonical 清單(來源 export:唯讀、只讀本機 state.db),右邊是選中清單的內容。
// 第二段是平台清單(pl list / pl show)。順序永遠照清單本來的順序,沒有排序、沒有拖曳(決策 38)。
import { el, quote, btn, field, select, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';

export function initPlaylists(root, api, con, notice, providers) {
  pageHead(root, '播放清單', 'export');
  const cols = el('div', 'pl__cols');
  const left = el('div', 'pl__left');
  const right = el('div', 'pl__right');
  cols.append(left, right);

  const platform = el('section', 'pl__platform');
  const prov = select(providers.list, providers.current);
  const pbar = el('div', 'form-row');
  pbar.appendChild(field('平台', prov));
  const pout = el('div', 'page__out');

  root.append(btn('重新讀取', 'btn--ghost', load), cols, el('h3', 'card__sub', '平台上的清單'), pbar, pout);
  pbar.append(
    btn('pl list', 'btn--ghost', () => con.run(`pl list --provider ${prov.value}`, {
      onTable: (h, r) => pout.replaceChildren(wrapTable(h, r)),
    })),
  );
  con.idle(load); // 同帳號頁:有命令在跑就等它結束,不要撞上它、畫成「沒有清單」

  function load() {
    let text = '';
    left.replaceChildren();
    right.replaceChildren();
    con.run('export', {
      onStdout: (t) => { text += t; },
      onExit: (code) => {
        if (code !== 0) { left.appendChild(emptyState('pl link 通勤 spotify --create')); return; }
        let files;
        try {
          files = JSON.parse(text);
        } catch (e) {
          left.appendChild(el('p', 'card__error', 'export 的輸出不是 JSON:' + e.message));
          return;
        }
        render(files);
      },
    });
  }

  function render(files) {
    const tracks = (files['tracks.json'] || {}).tracks || {};
    // 不重排:export 的鍵序本來就是決定性的(map 鍵排序),而 localeCompare 的結果會隨瀏覽器的 ICU 版本浮動,
    // 同一份資料在不同瀏覽器上會不一樣。清單集合與清單內容都照原順序(review #62)。
    const pls = Object.keys(files)
      .filter((k) => k.startsWith('pl__'))
      .sort()
      .map((k) => files[k]);
    left.replaceChildren();
    if (!pls.length) { left.appendChild(emptyState('pl link 通勤 spotify --create')); return; }
    for (const pl of pls) {
      const row = el('button', 'pl__item');
      row.type = 'button';
      row.appendChild(el('span', 'pl__name', pl.name || pl.pid));
      const chips = el('span', 'pl__chips');
      for (const linked of Object.keys(pl.links || {})) chips.appendChild(el('span', 'chip', `${linked} ✓`));
      row.appendChild(chips);
      row.appendChild(el('span', 'pl__count num', String((pl.items || []).length)));
      row.addEventListener('click', () => {
        for (const other of left.querySelectorAll('.pl__item')) other.classList.remove('is-active');
        row.classList.add('is-active');
        showItems(pl, tracks);
      });
      left.appendChild(row);
    }
    left.firstElementChild.click();
  }

  function showItems(pl, tracks) {
    right.replaceChildren();
    right.appendChild(el('h3', 'card__sub', `${pl.name}(${(pl.items || []).length} 首)`));
    const rows = (pl.items || []).map((it) => {
      const t = tracks[it.cid] || {};
      return [it.cid, t.title || '(本機沒有這首的資料)', (t.artists || []).join(', '), t.album || '', String(t.duration_ms || 0)];
    });
    right.appendChild(wrapTable(['CID', '曲名', '藝人', '專輯', '時長'], rows));
    const names = Object.keys(pl.links || {});
    if (names.length) {
      right.appendChild(btn(`pl show ${quote(pl.name)}`, 'btn--ghost', () =>
        con.run(`pl show ${quote(pl.name)} --provider ${names[0]}`, {
          onTable: (h, r) => pout.replaceChildren(wrapTable(h, r)),
        })));
    }
  }

  function wrapTable(h, r) {
    const w = el('div', 'tbl-wrap');
    w.appendChild(renderTable(h, r));
    return w;
  }
}
