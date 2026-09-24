// player.js:dock 的正在播放列。每 2.5 秒打 GET /api/now(輪詢不串流),document.hidden 時停;
// 伺服器決定顯示哪個平台(跟著正在播的那個,決策 51)、多久真的問一次 Spotify——這裡的輪詢只打本機。
// 控制鈕不另做端點,跑既有命令(決策 42):走 Console.run 的 quiet 模式,跟其他命令共用同一個序列槽、閘與中止。
import { t } from './i18n.js';
import { providerName } from './pages/common.js';

// POLL_MS 要大於伺服器的 webNowWait(2 s):相等的話單飛的 TryLock 幾乎每兩輪就固定失敗一次。
const POLL_MS = 2500;
// 上一份快照超過這麼久沒更新才算失聯。用時間而不是「連續幾次 stale」:State 穩定超過 webNowWait 時
// 每一輪都會是 stale,用次數會把「慢但一直有新資料」判成「伺服器死了」而且永遠回不來(review #61)。
const STALE_DEAD_MS = 30000;

function mmss(ms) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

export class Player {
  constructor(root, api, notice, con) {
    this.root = root; this.api = api; this.notice = notice;
    this.con = con; // 控制命令也佔伺服器的序列槽:走 con.run(quiet),同一個閘、同一顆中止
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
    this.line.textContent = t('webui.player.disconnected');
  }

  render(d) {
    this.last = d; // ← → 與 + - 要用它算絕對值:seek 吃秒數、vol 吃 0-100;控制鈕的 --provider 也看它
    this.root.dataset.playing = String(!!d.playing);
    this.root.dataset.stale = d.stale ? 'true' : '';
    const name = providerName(d.provider); // 面板會自己換平台:每一行都先說是哪個平台
    if (d.error) {
      this.line.textContent = t('webui.player.error', { provider: name, error: d.error });
      return;
    }
    if (!d.track) { // 在播卻沒有曲目 = Spotify 的 podcast 或廣告(item 是 null)
      this.line.textContent = d.playing ? `${name} · ▶` : t('webui.player.idle', { provider: name });
      return;
    }
    const tr = d.track;
    const dev = d.device ? ` · ${d.device.name}${d.device.volume_known ? ` · 🔊 ${d.device.volume_pct}` : ''}` : '';
    const age = d.stale ? ' · ' + t('webui.player.age', { count: Math.round((d.stale_ms || 0) / 1000) }) : '';
    // 曲名連回平台(Spotify Developer Policy II:顯示 Spotify 的內容要附連回去的連結)。只收 https,不讓 javascript: 之類進 href。
    let title = document.createElement('span');
    if (/^https:\/\//.test(tr.url || '')) {
      title = document.createElement('a');
      title.href = tr.url;
      title.target = '_blank';
      title.rel = 'noopener noreferrer';
    }
    title.textContent = tr.title;
    const head = document.createElement('span');
    head.textContent = `${name} · ${d.playing ? '▶' : '⏸'} `;
    const tail = document.createElement('span');
    tail.textContent = ` — ${(tr.artists || []).join(', ')} · ${mmss(d.position_ms)} / ${mmss(tr.duration_ms)}${dev}${age}`;
    this.line.replaceChildren(head, title, tail);
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

  // 控制走 con.run 的 quiet 模式:同一個序列槽、同一個閘、同一顆中止,只是不畫區塊,卡住超過 0.8 秒才亮狀態列。
  // 以前自己打 /api/run 再 body.cancel():關連線 = 伺服器把命令當成分頁關了而取消(自我取消);改成讀完之後
  // 又因為沒有中止路徑,卡在等 token 鎖時整個介面會鎖死到重啟(review #65 第 1 點)。
  async control(cmd) {
    if (this.con.running) { // 連按兩下的第二下不必說話;別的命令在跑才說
      if (!this.con.quiet) this.notice(t('webui.player.busy'));
      return;
    }
    // 控制的是面板上看到的那個平台(以前不帶,一律打到 default_provider:面板顯示 Spotify、⏸ 卻暫停了 Apple)。
    // 還沒有任何一份狀態就不帶,跟終端機一樣走預設平台。
    const p = this.last && this.last.provider;
    if (p) cmd += ` --provider ${p}`;
    const [code] = await this.con.run(cmd, {}, { quiet: true }); // 失敗的說明由 Console.report 負責
    if (code !== -1) this.tick(); // 命令已經跑完,立刻再輪詢一次,不等下一個 2.5 秒
  }
}
