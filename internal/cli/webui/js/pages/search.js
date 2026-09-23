// search.js:#/search —— 表單組出 capy search,結果表加一個「播放」列動作(play --id 是精確命中、不再搜尋)。
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
        ? resultTable(header, rows, prov.value, con)
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
function resultTable(header, rows, prov, con) {
  const wrap = el('div', 'tbl-wrap');
  const tbl = renderTable(header, rows);
  const head = tbl.querySelector('thead tr');
  head.appendChild(el('th', null, ''));
  [...tbl.querySelectorAll('tbody tr')].forEach((tr, i) => {
    const td = el('td', 'row-actions');
    const id = rows[i][0];
    // local 的 id 是 <device_id>/<檔名>,含空白是常態:不 quote 會被 splitArgs 切斷(review #62)。
    td.appendChild(btn(t('webui.search.play'), 'btn--ghost', () => con.run(`play --id ${quote(id)} --provider ${prov}`, {}, { label: t('webui.search.play_label', { title: rows[i][1] || id }) })));
    tr.appendChild(td);
  });
  wrap.appendChild(tbl);
  return wrap;
}
