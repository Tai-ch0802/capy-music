// common.js:頁面共用的小工具。頁面本身不打 /api/run 以外的東西——表單只負責組出一條 capy 命令,
// 輸出的呈現靠 Console 的 hooks。頁面的第一眼不出現命令字串(決策 45):按鈕說人話,命令原文在主控台。

// 平台的顯示名稱:文字就好,不用官方標誌(決策 48)。表寫在函式裡、用到時才算(i18n.js 開頭的載入順序鐵則)。
export const providerName = (id) => ({ spotify: 'Spotify', apple: 'Apple Music', local: '本機曲庫', google: 'Google Drive' })[id] || id;
export const el = (tag, cls, text) => {
  const x = document.createElement(tag);
  if (cls) x.className = cls;
  if (text != null) x.textContent = text;
  return x;
};

// quote:清單名有空白時要加引號,與 splitArgs(tui.go)的規則對齊——沒有跳脫、沒有單引號。
export function quote(s) {
  const v = String(s || '').trim();
  // splitArgs(tui.go)沒有跳脫語法,所以內含雙引號的值組不出正確的命令:明確標出來,不要默默組出壞命令。
  if (v.includes('"')) return v.replace(/"/g, '\uFFFD');
  return v.includes(' ') ? `"${v}"` : v;
}

// btn:頁面上用它建的按鈕一律是「發起一個 capy 命令」(純 UI 的控制用 el('button') 自己建),所以在這裡標 data-run。
// 序列槽被佔著(body[data-slot],播放控制也算)時點了不呼叫 fn——頁面不會先清掉自己的內容再被擋,改由 app.js
// 說明(capy:busy 事件);調暗由 CSS 看 data-busy。真的開跑的那一顆掛 data-pending,● 脈衝到命令收尾。
const slotTaken = () => document.body.hasAttribute('data-slot');

export function btn(label, cls, fn) {
  const b = el('button', 'btn ' + (cls || ''), label);
  b.type = 'button';
  b.dataset.run = '';
  if (slotTaken()) b.setAttribute('aria-disabled', 'true'); // 執行中才長出來的(例如結果表的「播放」)也要標
  b.addEventListener('click', () => {
    if (slotTaken()) { document.dispatchEvent(new Event('capy:busy')); return; }
    fn();
    if (slotTaken()) b.dataset.pending = '';
  });
  return b;
}

export function field(labelText, node, grow) {
  const wrap = el('label', grow ? 'field field--grow' : 'field');
  wrap.appendChild(el('span', 'field__label', labelText));
  wrap.appendChild(node);
  return wrap;
}

export function input(kind, placeholder, value) {
  const i = el('input', kind === 'mono' ? 'in in--mono' : 'in');
  i.type = 'text';
  i.autocomplete = 'off';
  i.spellcheck = false;
  if (placeholder) i.placeholder = placeholder;
  if (value) i.value = value;
  return i;
}

// select:options 可以是字串,或 { value, label, disabled }(不可選的要留著並說原因,不是直接消失)。
export function select(options, value) {
  const s = el('select', 'in');
  for (const o of options) {
    const v = typeof o === 'string' ? { value: o, label: o } : o;
    const op = el('option', null, v.label);
    op.value = v.value;
    if (v.disabled) op.disabled = true;
    s.appendChild(op);
  }
  if (value) s.value = value;
  return s;
}

// providerOptions:平台下拉,顯示名稱、送出 id。
export const providerOptions = (ids) => ids.map((id) => ({ value: id, label: providerName(id) }));

// emptyState:空白態是一句白話,告訴人下一步是什麼(主控台的空白態是水豚)。
export function emptyState(text) {
  return el('p', 'empty', text);
}

// pageHead:頁標題 + 一句白話說明。
export function pageHead(root, title, lead) {
  const h = el('div', 'page__head');
  h.appendChild(el('h2', 'page__title', title));
  if (lead) h.appendChild(el('p', 'page__lead', lead));
  root.appendChild(h);
  return h;
}
