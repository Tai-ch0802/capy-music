// move.js:#/move —— 搬家(首頁,決策 45)。這一版只有開場與一張表單:組出一條 capy migrate,
// 確認與變更表照舊由 CLI 自己問(頁面絕不代加 --yes / --force,決策 46)。三步精靈與路線示意在下一個 PR。
import { el, quote, btn, field, input, select, providerOptions, providerName, emptyState } from './common.js';
import { renderTable } from '../table.js';

// 首頁的每一句主張都要查得到出處(決策 48):MIT LICENSE、憑證只進鑰匙圈、migrate 只新增不刪來源。
const FACTS = ['免費', '開源(MIT)', '在你自己的電腦上執行', '不刪來源,只新增'];

export function initMove(root, api, con, notice, providers) {
  const hero = el('header', 'hero');
  const intro = el('div');
  intro.appendChild(el('h1', 'hero__title', '把歌單搬過去,一首都不用重找。'));
  intro.appendChild(el('p', 'hero__sub',
    'capy 讀出你在一個平台的播放清單,在另一個平台找到同樣的歌,照原本的順序放好。它在你自己的電腦上執行,你的帳號不經過任何人的伺服器。'));
  const facts = el('ul', 'facts');
  for (const f of FACTS) facts.appendChild(el('li', 'fact', f));
  intro.appendChild(facts);
  hero.appendChild(intro);

  const panel = el('section', 'panel');
  panel.appendChild(el('h2', 'panel__title', '搬一個清單'));
  const from = select(providerOptions(providers.list), providers.list.includes('apple') ? 'apple' : providers.current);
  // Apple Music 目前只讀:留在選單裡並說原因,不是直接消失(決策 46)。
  const to = select(providers.list.map((id) => (id === 'apple'
    ? { value: id, label: `${providerName(id)}(目前只能當來源)`, disabled: true }
    : { value: id, label: providerName(id) })), 'spotify');
  const name = input('sans', '清單名稱,例如:公路旅行(留空會讓你挑)');
  const form = el('div', 'form-row');
  form.append(field('從', from), field('搬到', to), field('哪一個清單', name, true));
  const out = el('div', 'page__out');

  const run = (dry) => {
    out.replaceChildren();
    const n = name.value.trim() ? ' ' + quote(name.value) : '';
    con.run(`migrate${n} --from ${from.value} --to ${to.value}${dry ? ' --dry-run' : ''}`, {
      onTable: (h, r) => out.replaceChildren(wrap(h, r)),
      onExit: (code, msg) => {
        if (code === 1 || code === 3) out.appendChild(el('p', 'page__warn', msg || ''));
        else if (code === 0 && !out.firstChild) out.appendChild(emptyState('沒有需要搬的歌:目的地已經都有了。'));
      },
    }, { label: dry ? '看看會搬哪些歌' : `把清單搬到 ${providerName(to.value)}` });
  };
  const acts = el('div', 'form-row');
  acts.append(
    btn('開始搬家', 'btn--primary', () => run(false)),
    btn('先看看會搬哪些歌', '', () => run(true)),
  );
  panel.append(form, acts,
    el('p', 'page__note', '開始之後會先列出要搬的歌,你確認了才會在目的地建立清單。來源的清單不會被更動,原本的順序也不會變。'),
    out);

  root.append(hero, panel);
}

function wrap(header, rows) {
  const w = el('div', 'tbl-wrap tbl-wrap--tall');
  w.appendChild(renderTable(header, rows));
  return w;
}
