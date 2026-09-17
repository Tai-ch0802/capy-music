// sync.js:#/sync —— 表單只組出一條命令,確認與變更表都在 dock 的區塊裡(寫入的確認是提示橋的 confirm,
// 頁面絕不代加 --yes、絕不代加 --force)。這一頁再把同一份變更表畫大一點。
import { el, quote, btn, field, input, select, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';

export function initSync(root, api, con, notice, providers) {
  pageHead(root, '同步', 'pl sync --all --dry-run');
  const name = input('mono', '清單名稱(留空 = --all)');
  const prov = select(['(全部)', ...providers.list], '(全部)');
  const dry = el('input', null);
  dry.type = 'checkbox';
  dry.checked = true;
  const out = el('div', 'page__out');

  const target = () => (name.value.trim() ? quote(name.value) : '--all');
  const flags = () => (prov.value === '(全部)' ? '' : ` --provider ${prov.value}`) + (dry.checked ? ' --dry-run' : '');
  const run = (verb) => {
    out.replaceChildren();
    con.run(`pl ${verb} ${target()}${flags()}`, {
      onTable: (h, r) => out.replaceChildren(table(h, r)),
      onExit: (code, msg) => { if (code !== 0 && msg) out.appendChild(el('p', 'page__warn', msg)); },
    });
  };

  const bar = el('div', 'form-row');
  bar.append(field('清單', name), field('平台', prov), field('只看變更(--dry-run)', dry));
  const acts = el('div', 'form-row');
  for (const v of ['pull', 'push', 'sync', 'dedup']) acts.appendChild(btn(v, v === 'sync' ? 'btn--primary' : '', () => run(v)));

  // migrate 的參數與上面四個不同(來源 / 目標平台),自己一列。
  const from = select(providers.list, providers.current);
  const to = select(providers.list, providers.list.find((p) => p !== providers.current) || providers.current);
  const mig = el('div', 'form-row');
  mig.append(field('搬移:從', from), field('到', to));
  mig.appendChild(btn('migrate', '', () => {
    out.replaceChildren();
    con.run(`migrate --from ${from.value} --to ${to.value}${dry.checked ? ' --dry-run' : ''}`, {
      onTable: (h, r) => out.replaceChildren(table(h, r)),
    });
  }));

  root.append(bar, acts, mig, out);
  out.appendChild(emptyState('pl sync --all --dry-run'));

  // 十欄同步表:自己的捲動容器 + sticky 表頭;ACTION 的字本身上色,remove 另外標記(不靠顏色單獨表意)。
  function table(header, rows) {
    const wrap = el('div', 'tbl-wrap tbl-wrap--tall');
    const t = renderTable(header, rows);
    const ai = header.indexOf('ACTION');
    if (ai >= 0) {
      for (const tr of t.querySelectorAll('tbody tr')) {
        const cell = tr.children[ai];
        if (cell) cell.dataset.action = cell.textContent.trim();
      }
    }
    wrap.appendChild(t);
    const n = rows.filter((r) => ai < 0 || r[ai] !== 'skip').length;
    wrap.appendChild(el('p', 'page__note', `${n} 筆變更(skip 不算變更)`));
    return wrap;
  }
}
