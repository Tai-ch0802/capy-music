// player.js:dock 的正在播放列。每 2 秒打 GET /api/now(輪詢不串流),document.hidden 時停;
// 控制鈕不另做端點,直接 POST /api/run 跑既有命令(決策 42)。
// POLL_MS 要大於伺服器的 webNowWait(2 s):相等的話單飛的 TryLock 幾乎每兩輪就固定失敗一次。
const POLL_MS = 2500;
// 上一份快照超過這麼久沒更新才算失聯。用時間而不是「連續幾次 stale」:State 穩定超過 webNowWait 時
// 每一輪都會是 stale,用次數會把「慢但一直有新資料」判成「伺服器死了」而且永遠回不來(review #61)。
const STALE_DEAD_MS = 30000;

function mmss(ms) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

// lastExit:整條 SSE 裡的 exit 事件(沒有 = null)。
function lastExit(sse) {
  let ex = null;
  for (const ln of sse.split('\n')) {
    if (!ln.startsWith('data: ')) continue;
    try {
      const ev = JSON.parse(ln.slice(6));
      if (ev.type === 'exit') ex = ev;
    } catch (_) { /* 不完整的一行 */ }
  }
  return ex;
}

export class Player {
  constructor(root, api, notice, busy) {
    this.root = root; this.api = api; this.notice = notice;
    this.busy = busy || (() => false); // 主控台有命令在跑:伺服器的序列槽一定 409,先在這邊說清楚
    this.timer = null; this.fails = 0;
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
    // stale 的語意是「伺服器忙」,不是「伺服器不在」:只降級顯示,不改連線狀態。真的太久沒有新資料才算失聯。
    if (d.stale && (d.stale_ms || 0) > STALE_DEAD_MS) { this.disconnected(); return; }
    this.root.dataset.nowConnected = 'true';
    this.render(d);
  }

  // 面板自己的旗標:body[data-connected] 是 console 在用的(那次 job 的串流斷了),兩個狀態不該互相蓋。
  disconnected() {
    this.root.dataset.nowConnected = 'false';
    this.line.textContent = 'capy --web 已停止或讀不到播放狀態';
  }

  render(d) {
    this.last = d; // ← → 與 + - 要用它算絕對值:seek 吃秒數、vol 吃 0-100
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

  // 前後各十秒、音量升降五格:鏡射 TUI 的鍵位。沒有最後一份狀態就不動作(算不出絕對值)。
  seekBy(sec) {
    const d = this.last;
    if (!d || !d.track) return;
    const pos = Math.max(0, Math.round((d.position_ms || 0) / 1000) + sec);
    this.control(`seek ${pos}`);
  }

  volBy(delta) {
    const dev = this.last && this.last.device;
    if (!dev || !dev.volume_known) return; // 平台沒回報音量時不亂猜一個基準
    this.control(`vol ${Math.min(100, Math.max(0, dev.volume_pct + delta))}`);
  }

  // 控制走 /api/run:序列槽忙碌時會 409,照實說,不假裝按了有效。
  async control(cmd) {
    if (this.busy()) { this.notice('正在執行別的命令:等它結束,或按「中止」'); return; }
    let text;
    try {
      const r = await this.api.fetch('/api/run', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ line: cmd }),
      });
      if (!r.ok) {
        let msg = r.statusText;
        try { msg = (await r.json()).error || msg; } catch (_) { /* 非 JSON */ }
        this.notice(msg);
        return;
      }
      // 一定要讀完:半路丟掉串流 = 關掉連線 = 伺服器當成「分頁關了」而取消這個命令(TestWebDisconnectCancelsJobAndPrompt
      // 釘住的行為)。以前這裡是 body.cancel(),瀏覽器約 1ms 就關連線,Spotify 的暫停 / 下一首幾乎都被自己腰斬。
      text = await r.text();
    } catch (e) {
      this.notice('播放控制沒送到:' + e.message);
      return;
    }
    const ex = lastExit(text); // 控制命令的輸出不進主控台;失敗(沒有作用中的裝置、授權過期…)要說
    if (ex && ex.code !== 0) this.notice(`${cmd}:${(ex.message || `exit ${ex.code}`).replace(/^Error: /, '')}`);
    this.tick(); // 命令已經跑完,立刻再輪詢一次,不等下一個 2.5 秒
  }
}
