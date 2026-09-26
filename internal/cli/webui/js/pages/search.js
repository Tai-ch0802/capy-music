// search.js:#/search —— 表單組出 capy search,結果表加一個「播放」列動作(play --id 是精確命中、不再搜尋;Apple 叫「在 Music.app 開啟」)。
import { el, quote, btn, field, input, select, providerOptions, providerName, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';
import { t } from '../i18n.js';

export function initSearch(root, api, con, notice, providers) {
  pageHead(root, t('webui.search.title'), t('webui.search.lead'));
  const q = input('sans', t('webui.search.placeholder'));
  const prov = select(providerOptions(providers.list), providers.current);
  const limit = el('input', 'in');
  limit.type = 'number';
  limit.min = '1';
  limit.value = '10';
  const bar = el('div', 'form-row');
  bar.append(field(t('webui.search.keywords'), q, true), field(t('webui.common.platform'), prov), field(t('webui.search.limit'), limit));
  const out = el('div', 'page__out');

  const go = () => {
    const text = q.value.trim();
    if (!text) { q.focus(); return; }
    out.replaceChildren();
    const n = Math.max(1, Number(limit.value) || 10); // 輸入框可以被清空,min 只在原生送出時驗
    con.run(`search ${quote(text)} --provider ${prov.value} --limit ${n}`, {
      // 沒有命中也是 exit 0,而且照樣送一張只有表頭的空表:要在這裡看列數,不然使用者只會看到一個空格子(review #67 第二輪)。
      onTable: (header, rows) => out.replaceChildren(rows.length
        ? resultTable(header, rows, prov.value, con, notice)
        : emptyState(t('webui.search.not_found', { platform: providerName(prov.value), query: text }))),
      onExit: (code, msg) => { if (code !== 0 && !out.firstChild) out.appendChild(emptyState(msg || t('webui.search.failed'))); },
    }, { label: t('webui.search.label', { platform: providerName(prov.value), query: text }) });
  };
  const goBtn = btn(t('webui.search.go'), 'btn--primary', go);
  bar.appendChild(goBtn);
  q.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
    ev.preventDefault();
    goBtn.click(); // 走按鈕那條路:執行中會被擋下並說明,不會先把目前的結果清掉
  });
  root.append(bar, out);
  out.appendChild(emptyState(t('webui.search.empty', { button: t('webui.search.go') })));
  q.focus();
}

// resultTable:沿用 table.js 的表格(同一份原子欄與時長規則),再補一欄常駐的列動作。
// Apple 那一列叫「在 Music.app 開啟」(決策 52):搜尋結果多半是資料庫裡沒有的目錄歌曲,capy 只能在 Music.app 打開並標出那首;
// 資料庫裡有的會真的播,這時底部的播放列換成 Apple 就是確認,說「開啟」只是少說,不會說謊。
function resultTable(header, rows, prov, con, notice) {
  const wrap = el('div', 'tbl-wrap');
  const tbl = renderTable(header, rows);
  const head = tbl.querySelector('thead tr');
  head.appendChild(el('th', null, ''));
  const apple = prov === 'apple';
  [...tbl.querySelectorAll('tbody tr')].forEach((tr, i) => {
    const td = el('td', 'row-actions');
    const id = rows[i][0];
    const title = rows[i][1] || id;
    const play = () => {
      let said = '';
      // local 的 id 是 <device_id>/<檔名>,含空白是常態:不 quote 會被 splitArgs 切斷(review #62)。
      con.run(`play --id ${quote(id)} --provider ${prov}`, {
        onStdout: (s) => { said += s; },
        // exit 0 卻不是 ▶ 開頭(player.go 印 ▶ 的那一行是另一端)= 只在 Music.app 打開、沒開始播:那句一定要讓人看到,
        // 不然成功時的輸出只進看不到的主控台頁。頁面照抄命令的那句,不自己另寫一句。
        onExit: (code) => { const line = said.trim(); if (code === 0 && line && !line.startsWith('▶')) notice(line); },
      }, { label: apple ? t('webui.search.open_music_label', { title }) : t('webui.search.play_label', { title }) });
    };
    td.appendChild(btn(apple ? t('webui.search.open_music') : t('webui.search.play'), 'btn--ghost', play));
    tr.appendChild(td);
  });
  wrap.appendChild(tbl);
  return wrap;
}
