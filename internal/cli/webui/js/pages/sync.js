// sync.js:#/sync —— 表單只組出一條命令,確認與變更表都在 dock 的區塊裡(寫入的確認是提示橋的 confirm,
// 頁面絕不代加 --yes、絕不代加 --force)。這一頁再把同一份變更表畫大一點。
import { el, quote, btn, field, input, select, providerOptions, emptyState, pageHead } from './common.js';
import { renderTable } from '../table.js';
import { t } from '../i18n.js';

export function initSync(root, api, con, notice, providers) {
  pageHead(root, t('webui.sync.title'), t('webui.sync.lead'));
  const name = input('sans', t('webui.sync.name_placeholder'));
  // 「全部平台」的值是空字串 = 不帶 --provider(值不拿顯示文字當,顯示文字跟著語系)。
  const prov = select([{ value: '', label: t('webui.sync.all_platforms') }, ...providerOptions(providers.list)], '');
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
  const flags = () => (prov.value ? ` --provider ${prov.value}` : '') + (dry.checked ? ' --dry-run' : '');
  // 按鈕說人話,命令原文在主控台(決策 45)。
  const VERBS = [
    ['pull', t('webui.sync.pull'), t('webui.sync.pull_label')],
    ['push', t('webui.sync.push'), t('webui.sync.push_label')],
    ['sync', t('webui.sync.sync'), t('webui.sync.sync_label')],
    ['dedup', t('webui.sync.dedup'), t('webui.sync.dedup_label')],
  ];
  const dryLabel = t('webui.sync.dry_run');
  const run = (verb, label) => {
    const wasDry = dry.checked; // 按下去那一刻的值:命令跑到一半才改勾選,不該改變這一次的收尾
    out.replaceChildren();
    con.run(`pl ${verb}${target(verb)}${flags()}`, {
      onTable: (h, r) => out.replaceChildren(table(h, r)),
      onExit: (code, msg) => {
        // 只看變更 + 有變更 = exit 2,這是最常見的一次操作,是正常結果不是警告;CLI 的原文會叫人「加 --yes」,
        // 而那正是這一頁永遠不會做的事(決策 46)——說這一頁上的下一步(review #67 第二輪)。
        if (code === 2 && wasDry) out.appendChild(el('p', 'page__note', t('webui.sync.dry_note', { option: dryLabel })));
        else if (code !== 0 && msg) out.appendChild(el('p', 'page__warn', msg));
        else if (code === 0 && !out.firstChild) out.appendChild(emptyState(t('webui.sync.in_sync')));
      },
    }, { label });
  };

  const bar = el('div', 'form-row');
  bar.append(field(t('webui.sync.playlist'), name, true), field(t('webui.common.platform'), prov), field(dryLabel, dry));
  const acts = el('div', 'form-row');
  for (const [verb, text, label] of VERBS) acts.appendChild(btn(text, verb === 'sync' ? 'btn--primary' : '', () => run(verb, label)));

  root.append(bar, acts,
    el('p', 'page__note', t('webui.sync.dedup_note', { button: t('webui.sync.dedup') })),
    out);
  out.appendChild(emptyState(t('webui.sync.empty', { button: t('webui.sync.sync') })));

  // 同步表(最後一欄是 REASON_CODE):自己的捲動容器 + sticky 表頭;ACTION 的字本身上色,remove 另外標記(不靠顏色單獨表意)。
  function table(header, rows) {
    const wrap = el('div', 'tbl-wrap tbl-wrap--tall');
    const tbl = renderTable(header, rows);
    const ai = header.indexOf('ACTION');
    if (ai >= 0) {
      for (const tr of tbl.querySelectorAll('tbody tr')) {
        const cell = tr.children[ai];
        if (cell) cell.dataset.action = cell.textContent.trim();
      }
    }
    wrap.appendChild(tbl);
    const n = rows.filter((r) => ai < 0 || r[ai] !== 'skip').length;
    wrap.appendChild(el('p', 'page__note', t('webui.sync.changes', { count: n })));
    return wrap;
  }
}
