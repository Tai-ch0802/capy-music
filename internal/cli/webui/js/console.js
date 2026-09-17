// console.js:POST /api/run 的 SSE 串流 → 區塊(回聲 / stdout / stderr / table / exit)。
import { renderTable } from './table.js';

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
    this.running = false; this.job = null;
  }

  block(line) {
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
    const m = this.root.parentElement || this.root;
    if (m.scrollHeight - m.scrollTop - m.clientHeight < 80) b.scrollIntoView({ block: 'end' });
  }

  async run(line) {
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
      this.running = false; this.job = null;
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
      case 'stdout': this.append(b, 'block__out', ev.text); break;
      case 'stderr': this.append(b, 'block__err', ev.text); break;
      case 'table': b.appendChild(renderTable(ev.header, ev.rows)); break;
      case 'exit': this.exit(b, ev.code, ev.message, ev.reason); break;
      default: break; // prompt / prompt_closed / open_url:T3b
    }
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
  }

  async cancel() {
    if (!this.job) return;
    await this.api.fetch(`/api/jobs/${encodeURIComponent(this.job)}/cancel`, { method: 'POST' });
  }
}
