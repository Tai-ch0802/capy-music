// player.js:dock 的正在播放列。每 2 秒打 GET /api/now(輪詢不串流),document.hidden 時停;
// 控制鈕不另做端點,直接 POST /api/run 跑既有命令(決策 42)。
const POLL_MS = 2000;

function mmss(ms) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

export class Player {
  constructor(root, api, notice) {
    this.root = root; this.api = api; this.notice = notice;
    this.timer = null; this.fails = 0; this.stales = 0;
    this.line = root.querySelector('#now-line');
    this.root.querySelectorAll('[data-cmd]').forEach((b) => {
      b.addEventListener('click', () => this.control(b.dataset.cmd));
    });
    document.addEventListener('visibilitychange', () => (document.hidden ? this.stop() : this.start()));
  }

  start() {
    if (this.timer) return;
    this.tick();
    this.timer = setInterval(() => this.tick(), POLL_MS);
  }

  stop() {
    clearInterval(this.timer);
    this.timer = null;
  }

  async tick() {
    let d;
    try {
      const r = await this.api.fetch('/api/now');
      if (!r.ok) throw new Error('HTTP ' + r.status);
      d = await r.json();
    } catch (_) {
      if (++this.fails >= 5) this.disconnected();
      return;
    }
    this.fails = 0;
    this.stales = d.stale ? this.stales + 1 : 0;
    if (this.stales >= 5) { this.disconnected(); return; }
    document.body.dataset.connected = 'true';
    this.render(d);
  }

  disconnected() {
    document.body.dataset.connected = 'false';
    this.line.textContent = 'capy --web 已停止或讀不到播放狀態';
  }

  render(d) {
    this.root.dataset.playing = String(!!d.playing);
    this.root.dataset.stale = d.stale ? 'true' : '';
    if (d.error) {
      this.line.textContent = `${d.provider}:${d.error}`;
      return;
    }
    if (!d.track) {
      this.line.textContent = `${d.provider}:目前沒有播放內容`;
      return;
    }
    const t = d.track;
    const dev = d.device ? ` · ${d.device.name}${d.device.volume_known ? ` · 🔊 ${d.device.volume_pct}` : ''}` : '';
    const age = d.stale ? ` · ${Math.round((d.stale_ms || 0) / 1000)} 秒前` : '';
    this.line.textContent = `${d.playing ? '▶' : '⏸'} ${t.title} — ${(t.artists || []).join(', ')}` +
      ` · ${mmss(d.position_ms)} / ${mmss(t.duration_ms)}${dev}${age}`;
  }

  // 控制走 /api/run:序列槽忙碌時會 409,照實說,不假裝按了有效。
  async control(cmd) {
    const r = await this.api.fetch('/api/run', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ line: cmd }),
    });
    if (!r.ok) {
      let msg = r.statusText;
      try { msg = (await r.json()).error || msg; } catch (_) { /* 非 JSON */ }
      this.notice(msg);
      return;
    }
    await r.body.cancel().catch(() => {}); // 控制命令的輸出不進主控台,取消串流讓伺服器那邊收工
    setTimeout(() => this.tick(), 200);    // 命令跑完立刻再輪詢一次,不等下一個 2 秒
  }
}
