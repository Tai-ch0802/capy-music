// sync.js:#/sync —— 表單只組出一條命令,確認與變更表都在 dock 的區塊裡(寫入的確認是提示橋的 confirm,
// 頁面絕不代加 --yes、絕不代加 --force)。這一頁再把同一份變更表畫大一點。
import { el, quote, btn, field, input, select, providerOptions, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';

export function initSync(root, api, con, notice, providers) {
  pageHead(root, '同步', '讓 capy 保管的清單跟平台上的保持一致。會先列出要改什麼,你確認了才寫入。');
  const name = input('sans', '清單名稱(留空 = 全部)');
  const prov = select([{ value: '(全部)', label: '全部平台' }, ...providerOptions(providers.list)], '(全部)');
  const dry = el('input', null);
  dry.type = 'checkbox';
  dry.checked = true;
  const out = el('div', 'page__out');

  // pl dedup 沒有 --all(dedup.go 只有 --provider / --dry-run / --yes / --force),清單留空時不要送它:
  // web 的 isInteractive 是 true,不帶清單名會開挑選器,比送一個 cobra 一定退回的 flag 好(review #62)。
  const target = (verb) => {
    const n = name.value.trim();
    if (n) return ' ' + quote(n);
    return verb === 'dedup' ? '' : ' --all';
  };
  const flags = () => (prov.value === '(全部)' ? '' : ` --provider ${prov.value}`) + (dry.checked ? ' --dry-run' : '');
  // 按鈕說人話,命令原文在主控台(決策 45)。
  const VERBS = [
    ['pull', '從平台更新', '把平台上的變更拉回來'],
    ['push', '推到平台', '把 capy 保管的清單推到平台'],
    ['sync', '雙向同步', '雙向同步'],
    ['dedup', '去除重複', '去除重複的歌'],
  ];
  const run = (verb, label) => {
    out.replaceChildren();
    con.run(`pl ${verb}${target(verb)}${flags()}`, {
      onTable: (h, r) => out.replaceChildren(table(h, r)),
      onExit: (code, msg) => {
        if (code !== 0 && msg) out.appendChild(el('p', 'page__warn', msg));
        else if (code === 0 && !out.firstChild) out.appendChild(emptyState('兩邊已經一致,沒有要改的東西。'));
      },
    }, { label });
  };

  const bar = el('div', 'form-row');
  bar.append(field('清單', name, true), field('平台', prov), field('只看變更,先不寫入', dry));
  const acts = el('div', 'form-row');
  for (const [verb, text, label] of VERBS) acts.appendChild(btn(text, verb === 'sync' ? 'btn--primary' : '', () => run(verb, label)));

  root.append(bar, acts,
    el('p', 'page__note', '「去除重複」不分平台:它整理的是 capy 保管的那一份;清單留空時會讓你挑一個。要把清單搬到另一個平台,請到「搬家」。'),
    out);
  out.appendChild(emptyState('選好清單與平台,按「雙向同步」。預設只列出變更,不會寫入。'));

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
