// console.js:POST /api/run 的 SSE 串流 → 區塊(回聲 / stdout / stderr / table / exit)。
import { renderTable } from './table.js';

// CAPYBARA:與 tui_capybara.go 的 capybaraStill() 逐字元相同(TestWebCapybaraMatchesTUI 釘住)。
// 全部純 ASCII:方框繪製字元在 CJK 終端機是兩欄,會讓橫幅垮掉——網頁沿用同一份是為了兩邊長得一樣。
export const CAPYBARA = [
  '    __            __      ',
  '   /  \\__________/  \\     ',
  '  |                  |    ',
  '  |   O          O   |    ',
  '  |                  |    ',
  '  |      ______      |    ',
  '  |     (__..__)~~~~~~~~~ ',
  '   \\________________/     ',
];

const SYSTEM_DIALOG = ['auth logout', 'config set', 'history clear', 'doctor', 'auth login'];
// 回聲遮罩是第二層(這三個 flag 在伺服器端本來就 403):值不進畫面、不留在 DOM(設計規格 §8)。
const SECRET_FLAGS = ['--developer-token', '--user-token', '--client-secret'];

function maskSecrets(line) {
  const parts = line.split(' ');
  for (let i = 0; i < parts.length; i++) {
    const name = parts[i].split('=')[0];
    if (!SECRET_FLAGS.includes(name)) continue;
    if (parts[i].includes('=')) parts[i] = name + '=***';
    else if (i + 1 < parts.length) parts[i + 1] = '***';
  }
  return parts.join(' ');
}

export class Console {
  constructor(root, api, notice) {
    this.root = root; this.api = api; this.notice = notice;
    this.running = false; this.job = null; this.hooks = {};
  }

  // 空白態:水豚 + 招牌(設計規格 §10)。第一次繪製時 power-on(§7 的簽名時刻),每個 session 一次。
  showIdle(provider) {
    if (this.root.querySelector('.block')) return;
    // 已經畫過就只更新招牌:route() 會先叫一次(還不知道 provider),/api/commands 回來後再叫一次,
    // 重畫會把 power-on 那一幀洗掉——簽名時刻一個 session 只有一次,洗掉就永遠看不到了。
    const shown = this.root.querySelector('.capy');
    if (shown) {
      shown.querySelector('.capy__tag').textContent = provider ? `capy · ${provider}` : 'capy';
      return;
    }
    const box = document.createElement('div');
    box.className = 'capy';
    const pre = document.createElement('pre');
    pre.className = 'capy__art';
    pre.setAttribute('aria-hidden', 'true');
    pre.textContent = CAPYBARA.join('\n');
    box.appendChild(pre);
    box.appendChild(Object.assign(document.createElement('p'), {
      className: 'capy__tag', textContent: provider ? `capy · ${provider}` : 'capy',
    }));
    let seen = false;
    try { seen = sessionStorage.getItem('capy.poweron') === '1'; } catch (_) { /* 私密視窗 */ }
    if (!seen) {
      box.dataset.poweron = '';
      try { sessionStorage.setItem('capy.poweron', '1'); } catch (_) { /* 同上 */ }
    }
    this.root.replaceChildren(box);
  }

  block(line) {
    const idle = this.root.querySelector('.capy');
    if (idle) idle.remove();
    const b = document.createElement('article');
    b.className = 'block';
    b.dataset.running = '';
    const head = document.createElement('div');
    head.className = 'block__head';
    head.setAttribute('role', 'status'); // 狀態行才播報;串流 pre 不是 live region(設計規格 §11)
    head.innerHTML = '<span class="prompt">capy</span>';
    head.appendChild(document.createTextNode(' ' + maskSecrets(line)));
    b.appendChild(head);
    if (SYSTEM_DIALOG.some((p) => line.startsWith(p))) {
      const h = document.createElement('div');
      h.className = 'block__hint';
      h.textContent = '可能在這台電腦跳出系統對話框(keychain / Music.app)';
      b.appendChild(h);
    }
    this.root.appendChild(b);
    b.scrollIntoView({ block: 'end' });
    return b;
  }

  append(b, cls, text) {
    let el = b.lastElementChild;
    if (!el || !el.classList.contains(cls)) {
      el = document.createElement('pre');
      el.className = cls;
      b.appendChild(el);
    }
    el.textContent += text;
    this.stick(b);
  }

  // 貼底:已經在底部才跟著捲,使用者往上捲讀舊輸出就不打斷他(設計規格 §5;浮動「新輸出 ↓」鈕留給 T5)。
  stick(b) {
    const m = document.getElementById('main') || this.root; // .main 才是捲動容器(頁面容器不捲)
    if (m.scrollHeight - m.scrollTop - m.clientHeight < 80) b.scrollIntoView({ block: 'end' });
  }

  // run(line, hooks):hooks.onTable / onStdout / onExit 讓發起命令的頁面拿到解析後的輸出。
  // 命令本身照樣完整跑在 dock 裡(回聲、串流、提示、退出碼都在),頁面只是多一份結構化的複本。
  async run(line, hooks = {}) {
    // hooks / running / job 綁在「這一次呼叫」上:onExit 裡再叫一次 run()(帳號頁登入完要刷新)時,
    // 外層的 finally 會晚一步執行,不能把內層那次的狀態洗掉(review #62)。
    const mine = Symbol('run');
    this.cur = mine;
    this.hooks = hooks;
    const b = this.block(line || '(help)');
    this.running = true; this.notice('');
    document.body.dataset.connected = 'true';
    try {
      const r = await this.api.fetch('/api/run', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ line }),
      });
      if (!r.ok) {
        let msg = r.statusText;
        try { msg = (await r.json()).error || msg; } catch (_) { /* 非 JSON */ }
        this.refused(b, r.status, msg);
        return;
      }
      await this.stream(r.body, b);
    } catch (e) {
      this.exit(b, 1, '連線中斷:' + e.message, 'disconnected');
      document.body.dataset.connected = 'false';
    } finally {
      if (this.cur === mine) { this.running = false; this.job = null; this.hooks = {}; }
      delete b.dataset.running;
    }
  }

  async stream(body, b) {
    const reader = body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf('\n\n')) >= 0) {
        const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
        for (const ln of chunk.split('\n')) {
          if (ln.startsWith('data: ')) this.event(JSON.parse(ln.slice(6)), b);
        }
      }
    }
  }

  event(ev, b) {
    switch (ev.type) {
      case 'start': this.job = ev.job; b.dataset.job = ev.job; break;
      case 'stdout': this.append(b, 'block__out', ev.text); this.hooks.onStdout?.(ev.text); break;
      case 'stderr': this.append(b, 'block__err', ev.text); break;
      case 'table': b.appendChild(renderTable(ev.header, ev.rows)); this.hooks.onTable?.(ev.header, ev.rows); break;
      case 'exit': this.exit(b, ev.code, ev.message, ev.reason); this.hooks.onExit?.(ev.code, ev.message, ev.reason); break;
      case 'prompt': this.prompt(ev, b); break;
      case 'prompt_closed': this.promptClosed(ev, b); break;
      case 'open_url': this.openURL(ev, b); break;
      default: break;
    }
  }

  // 提示橋(決策 40 第 3 點):confirm / select / input / form 都畫在同一個區塊裡;答案 POST /api/jobs/{job}/answer。
  prompt(ev, b) {
    const box = document.createElement('div');
    box.className = 'prompt';
    box.dataset.id = String(ev.id);
    box.dataset.kind = ev.kind;
    const el = (tag, cls, text) => { const x = document.createElement(tag); if (cls) x.className = cls; if (text != null) x.textContent = text; return x; };
    if (ev.note) {
      const n = el('div', 'prompt__note');
      n.appendChild(el('div', 'prompt__note-title', ev.note.title));
      n.appendChild(el('pre', 'prompt__note-body', ev.note.body));
      box.appendChild(n);
    }
    if (ev.error) box.appendChild(el('div', 'prompt__error', ev.error));
    box.appendChild(el('div', 'prompt__title', ev.title));
    const controls = () => box.querySelectorAll('button,input');
    const answer = (cancel, value) => {
      controls().forEach((c) => { c.disabled = true; });
      this.answer(ev.id, cancel, value).then((ok) => { if (!ok) controls().forEach((c) => { c.disabled = false; }); });
    };
    const btn = (label, cls, fn) => { const x = el('button', 'btn ' + cls, label); x.type = 'button'; x.addEventListener('click', fn); return x; };
    // 組字中的 Enter 是確認候選字、Esc 是取消候選字,兩個都不該當成回答(同 app.js 的命令列)。
    const onKeys = (inp, submit) => inp.addEventListener('keydown', (k) => {
      if (k.isComposing || k.keyCode === 229) return;
      if (k.key === 'Enter') { k.preventDefault(); submit(); }
      if (k.key === 'Escape') { k.preventDefault(); answer(true, null); }
    });
    const row = el('div', 'prompt__row');
    let focus = null;
    switch (ev.kind) {
      case 'confirm': {
        const yes = btn(ev.affirmative || '確定', ev.default === true ? 'btn--primary' : '', () => answer(false, true));
        const no = btn(ev.negative || '取消', ev.default === false ? 'btn--primary' : '', () => answer(false, false));
        row.append(yes, no);
        focus = ev.default === false ? no : yes;
        break;
      }
      case 'select': {
        const list = el('div', 'prompt__options');
        (ev.options || []).forEach((o, i) => list.appendChild(btn(o, 'btn--option', () => answer(false, i))));
        box.appendChild(list);
        focus = list.firstElementChild;
        break;
      }
      case 'input': {
        const inp = el('input', 'prompt__input');
        inp.type = 'text'; inp.spellcheck = false; inp.autocomplete = 'off';
        inp.value = ev.default == null ? '' : String(ev.default);
        onKeys(inp, () => answer(false, inp.value));
        row.append(inp, btn('確定', 'btn--primary', () => answer(false, inp.value)));
        focus = inp;
        break;
      }
      case 'form': {
        const inputs = {};
        const fields = el('div', 'prompt__fields');
        const submit = () => { const v = {}; for (const [n, inp] of Object.entries(inputs)) v[n] = inp.value; answer(false, v); };
        (ev.fields || []).forEach((f) => {
          const lab = el('label', 'prompt__field');
          lab.appendChild(el('span', null, f.label));
          const inp = el('input', 'prompt__input');
          inp.type = f.secret ? 'password' : 'text';
          inp.autocomplete = f.secret ? 'new-password' : 'off';
          inp.spellcheck = false;
          if (f.value) inp.value = f.value;                      // 重問時帶回上一輪的值(非 secret 欄)
          if (f.filled) inp.placeholder = '已填,留空 = 沿用上次'; // secret 欄的值絕不回到頁面
          onKeys(inp, submit);
          inputs[f.name] = inp;
          lab.appendChild(inp);
          fields.appendChild(lab);
          if (!focus) focus = inp;
        });
        box.appendChild(fields);
        row.appendChild(btn('送出', 'btn--primary', submit));
        break;
      }
      default: break;
    }
    row.appendChild(btn('✕ 關掉', 'btn--ghost', () => answer(true, null)));
    box.appendChild(row);
    b.appendChild(box);
    if (focus) focus.dataset.autofocus = ''; // 切頁時 route() 靠它把焦點交回提示,見 reveal()
    this.reveal();
    this.stick(b);
    if (focus) setTimeout(() => focus.focus({ preventScroll: true }), 0); // 不覆蓋 stick() 的捲動判斷
  }

  // 從別頁按鈕發出的命令,區塊一樣在主控台頁裡,而那一頁此刻是 hidden:提示與授權連結畫在那裡等於沒畫,
  // 命令只會卡到提示逾時,Apple 的揭露也只剩伺服器端「送出過」(review #62 第 5 點)。所以要切回主控台。
  // 切頁的 hashchange 可能晚於上面的 setTimeout(那時焦點落在 hidden 子樹裡是 no-op),由 route() 叫 focusPrompt() 補上。
  reveal() {
    const page = this.root.closest('.page');
    if (page && page.hidden) location.hash = '#/console';
  }

  focusPrompt() {
    const f = this.root.querySelector('.prompt:not(.is-closed) [data-autofocus]');
    if (!f) return false;
    f.focus(); // 不帶 preventScroll:剛從別頁切過來,要捲到提示那裡
    return true;
  }

  async answer(id, cancel, value) {
    if (!this.job) return false;
    let r;
    try {
      r = await this.api.fetch(`/api/jobs/${encodeURIComponent(this.job)}/answer`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, cancel, value }),
      });
    } catch (e) { // fetch 在網路層失敗是 reject,不是 r.ok === false:自己收,讓控制項解鎖可以重送
      this.notice('回答沒送到:' + e.message);
      return false;
    }
    if (r.ok) return true;
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch (_) { /* 非 JSON */ }
    this.notice('回答沒送到:' + msg);
    return r.status !== 400; // 400 = 型別不合,讓使用者改;其他(404 / 409)不必重試
  }

  promptClosed(ev, b) {
    const box = b.querySelector(`.prompt[data-id="${CSS.escape(String(ev.id))}"]`);
    if (!box) return;
    box.classList.add('is-closed');
    box.dataset.reason = ev.reason;
    box.querySelectorAll('button,input').forEach((c) => { c.disabled = true; });
    const text = { answered: '已回答', dismissed: '已關掉', timeout: '等待回答逾時,命令已取消', cancelled: '命令已取消' }[ev.reason] || ev.reason;
    const tag = document.createElement('div');
    tag.className = 'prompt__closed';
    tag.textContent = text;
    box.appendChild(tag);
  }

  openURL(ev, b) {
    const p = document.createElement('p');
    p.className = 'block__link';
    const a = document.createElement('a');
    a.href = ev.url; a.target = '_blank'; a.rel = 'noopener noreferrer';
    a.textContent = '在瀏覽器開啟授權頁:' + ev.url;
    p.appendChild(a);
    b.appendChild(p);
    this.reveal();
    this.stick(b);
  }

  exit(b, code, message, reason) {
    b.dataset.exit = String(code);
    const el = document.createElement('div');
    el.className = 'block__exit';
    el.dataset.code = String(code);
    el.setAttribute('role', 'status');
    const mark = code === 0 ? '✓' : (code === 2 || code === 3 ? '·' : '✗');
    let text = `${mark} exit ${code}`;
    if (reason === 'cancelled') text += ' · 已取消';
    else if (reason === 'shutdown') text += ' · capy --web 已結束';
    else if (reason === 'timeout') text += ' · 等待回答逾時';
    else if (reason === 'busy') text += ' · 另一個命令執行中,等它結束或取消';
    else if (reason === 'stale') text += ' · 請重啟 capy --web';
    if (message) text += ' · ' + message.replace(/^Error: /, '');
    if (code === 2 && /--yes/.test(message || '')) text += '(未套用:加 --yes 重跑)';
    el.textContent = text;
    b.appendChild(el);
    this.stick(b);
  }

  // 非 200 = 命令根本沒跑:不畫成 ✗ exit 1(那是命令真的執行而失敗的樣子),標「未執行」並把原因放到命令列上方。
  refused(b, status, msg) {
    b.dataset.refused = String(status);
    const el = document.createElement('div');
    el.className = 'block__exit';
    el.dataset.code = 'refused';
    el.setAttribute('role', 'status');
    const why = { 401: 'token 不對或已失效', 403: '這個命令在 web 不提供', 409: '另一個命令執行中', 503: '請重啟 capy --web' }[status] || ('HTTP ' + status);
    el.textContent = `· 未執行(${why})· ${msg}`;
    b.appendChild(el);
    this.notice(msg);
    if (status === 503) document.body.dataset.stale = '';
    this.stick(b);
    // 沒跑成也要通知發起的頁面:否則 409(單一序列槽)之後那一頁的 render 永遠不會收尾,
    // 而 route() 的 ready 又保證不會重新初始化——頁面就永久空白了(review #62)。
    this.hooks.onExit?.(-1, msg, 'refused');
  }

  async cancel() {
    if (!this.job) return;
    try {
      await this.api.fetch(`/api/jobs/${encodeURIComponent(this.job)}/cancel`, { method: 'POST' });
    } catch (e) { // 同 answer():裸 await 在斷線時是 unhandled rejection,使用者只看到「按了沒反應」
      this.notice('取消沒送到:' + e.message);
    }
  }
}
