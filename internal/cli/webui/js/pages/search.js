// search.js:#/search —— 表單組出 capy search,結果表加一個「播放」列動作(play --id 是精確命中、不再搜尋)。
import { el, quote, btn, field, input, select, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';

export function initSearch(root, api, con, notice, providers) {
  pageHead(root, '搜尋', 'search <關鍵字>');
  const q = input('sans', '五月天 派對動物');
  const prov = select(providers.list, providers.current);
  const limit = el('input', 'in in--mono');
  limit.type = 'number';
  limit.min = '1';
  limit.value = '10';
  const bar = el('div', 'form-row');
  bar.append(field('關鍵字', q), field('平台', prov), field('結果數', limit));
  const out = el('div', 'page__out');

  const go = () => {
    const text = q.value.trim();
    if (!text) { q.focus(); return; }
    out.replaceChildren();
    const n = Math.max(1, Number(limit.value) || 10); // 輸入框可以被清空,min 只在原生送出時驗
    con.run(`search ${quote(text)} --provider ${prov.value} --limit ${n}`, {
      onTable: (header, rows) => out.replaceChildren(resultTable(header, rows, prov.value, con)),
      onExit: (code) => { if (code !== 0 && !out.firstChild) out.appendChild(emptyState('search <關鍵字>')); },
    });
  };
  bar.appendChild(btn('搜尋', 'btn--primary', go));
  q.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
    ev.preventDefault();
    go();
  });
  root.append(bar, out);
  out.appendChild(emptyState('search <關鍵字>'));
  q.focus();
}

// resultTable:沿用 table.js 的表格(同一份原子欄與時長規則),再補一欄常駐的列動作。
function resultTable(header, rows, prov, con) {
  const wrap = el('div', 'tbl-wrap');
  const t = renderTable(header, rows);
  const head = t.querySelector('thead tr');
  head.appendChild(el('th', null, ''));
  [...t.querySelectorAll('tbody tr')].forEach((tr, i) => {
    const td = el('td', 'row-actions');
    const id = rows[i][0];
    // local 的 id 是 <device_id>/<檔名>,含空白是常態:不 quote 會被 splitArgs 切斷(review #62)。
    td.appendChild(btn('播放', 'btn--ghost', () => con.run(`play --id ${quote(id)} --provider ${prov}`)));
    tr.appendChild(td);
  });
  wrap.appendChild(t);
  return wrap;
}
