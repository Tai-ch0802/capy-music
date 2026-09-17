// common.js:頁面共用的小工具。頁面本身不打 /api/run 以外的東西——表單只負責組出一條 capy 命令
// (設計規格 §5),輸出的呈現靠 Console 的 hooks。
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

export function btn(label, cls, fn) {
  const b = el('button', 'btn ' + (cls || ''), label);
  b.type = 'button';
  b.addEventListener('click', fn);
  return b;
}

export function field(labelText, node) {
  const wrap = el('label', 'field');
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

export function select(options, value) {
  const s = el('select', 'in in--mono');
  for (const o of options) {
    const op = el('option', null, o);
    op.value = o;
    s.appendChild(op);
  }
  if (value) s.value = value;
  return s;
}

// emptyState:空白態本身就是下一步(設計規格 §10)——點了把命令填進命令列,不執行。
export function emptyState(cmd) {
  const p = el('p', 'empty');
  const a = el('button', 'empty__cmd', '> ' + cmd);
  a.type = 'button';
  a.addEventListener('click', () => {
    const i = document.getElementById('cmd');
    i.value = cmd;
    i.focus();
  });
  p.appendChild(a);
  return p;
}

// pageHead:標題 + 該頁的 CLI 等價命令(點了填進命令列、不執行)。
export function pageHead(root, title, cliCmd) {
  const h = el('div', 'page__head');
  h.appendChild(el('h2', 'page__title', title));
  if (cliCmd) {
    const c = el('button', 'page__cli', cliCmd);
    c.type = 'button';
    c.title = '填進命令列';
    c.addEventListener('click', () => {
      const i = document.getElementById('cmd');
      i.value = cliCmd;
      i.focus();
    });
    h.appendChild(c);
  }
  root.appendChild(h);
  return h;
}
