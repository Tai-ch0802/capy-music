// search.js:#/search —— 表單組出 capy search,結果表加一個「播放」列動作(play --id 是精確命中、不再搜尋)。
import { el, quote, btn, field, input, select, providerOptions, providerName, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';

export function initSearch(root, api, con, notice, providers) {
  pageHead(root, '搜尋', '在平台上找歌,找到了可以直接播。');
  const q = input('sans', '五月天 派對動物');
  const prov = select(providerOptions(providers.list), providers.current);
  const limit = el('input', 'in');
  limit.type = 'number';
  limit.min = '1';
  limit.value = '10';
  const bar = el('div', 'form-row');
  bar.append(field('關鍵字', q, true), field('平台', prov), field('結果數', limit));
  const out = el('div', 'page__out');

  const go = () => {
    const text = q.value.trim();
    if (!text) { q.focus(); return; }
    out.replaceChildren();
    const n = Math.max(1, Number(limit.value) || 10); // 輸入框可以被清空,min 只在原生送出時驗
    con.run(`search ${quote(text)} --provider ${prov.value} --limit ${n}`, {
      onTable: (header, rows) => out.replaceChildren(resultTable(header, rows, prov.value, con)),
      onExit: (code, msg) => { if (code !== 0 && !out.firstChild) out.appendChild(emptyState(msg || '沒有找到。換個關鍵字試試。')); },
    }, { label: `在 ${providerName(prov.value)} 找「${text}」` });
  };
  const goBtn = btn('搜尋', 'btn--primary', go);
  bar.appendChild(goBtn);
  q.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
    ev.preventDefault();
    goBtn.click(); // 走按鈕那條路:執行中會被擋下並說明,不會先把目前的結果清掉
  });
  root.append(bar, out);
  out.appendChild(emptyState('輸入歌名或歌手,按「搜尋」。'));
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
    td.appendChild(btn('播放', 'btn--ghost', () => con.run(`play --id ${quote(id)} --provider ${prov}`, {}, { label: `播放「${rows[i][1] || id}」` })));
    tr.appendChild(td);
  });
  wrap.appendChild(t);
  return wrap;
}
