// console.js:POST /api/run 的 SSE 串流 → 區塊(回聲 / stdout / stderr / table / exit)。
import { renderTable } from './table.js';

// CAPYBARA:與 tui_capybara.go 的 capybaraStill() 逐字元相同(TestWebCapybaraMatchesTUI 釘住)。
// 全部純 ASCII:方框繪製字元在 CJK 終端機是兩欄,會讓橫幅垮掉——網頁沿用同一份是為了兩邊長得一樣。
// 這幾行用雙引號:水豚身上有單引號與反引號(輪廓的轉角),雙引號字串裡只有反斜線要跳脫。
export const CAPYBARA = [
  "                         _                ",
  "        ________________( )_____          ",
  "     .-'                    o   `-.       ",
  "   .'                           .  \\      ",
  "  /                                 |  ,-\"",
  " |                            ____.'--'   ",
  "  \\                       __.-'           ",
  "   `.|  |`----------'|  |'                ",
  "     |__|            |__|                 ",
];

const SYSTEM_DIALOG = ['auth logout', 'config set', 'history clear', 'doctor', 'auth login'];
// 回聲遮罩是第二層(這三個 flag 在伺服器端本來就 403):值不進畫面、不留在 DOM(設計規格 §8)。
const SECRET_FLAGS = ['--developer-token', '--user-token', '--client-secret'];

// maskSecrets:切法要跟伺服器的 splitArgs(tui.go)一樣寬:空白與 tab 都是分隔、可以連續、flag 名字可以被
// 雙引號包住、值可以是含空白的引號字串。所以不逐 token 對,而是找到秘密 flag 之後,後面整段一律換成 ***
// ——多遮幾個參數無妨(這三個 flag 伺服器本來就 403,命令根本不會跑),漏遮一個就是秘密上了畫面(review)。
const SECRET_RE = new RegExp(`(^|[ \\t])"?(${SECRET_FLAGS.join('|')})(?=["= \\t]|$)`);

export function maskSecrets(line) {
  const m = SECRET_RE.exec(line);
  return m ? line.slice(0, m.index + m[0].length) + ' ***' : line;
}

// isCancelled:使用者中止(或關掉提示)而沒做完。exit 0 + reason cancelled 是「中止落在不吃取消的那一段、
// 命令其實做完了」(伺服器也改成回 done,這裡是第二層),照完成算——不然會叫人重跑一個已經做完的命令。
function isCancelled([code, , reason]) {
  return reason === 'cancelled' && code !== 0;
}

// progress 事件的階段 → 白話(決策 47)。進度條只吃伺服器送來的 done / total;沒有事件就不畫,不編百分比。
// 兩個都是函式、用到時才算:模組頂層不可以算使用者看得到的字(i18n.js 開頭的載入順序鐵則)。
export const stages = () => ({ read: '讀取來源清單', match: '比對歌曲', write: '寫入目的地' });
// 使用者自己中止時,交給頁面 onExit 的訊息。頁面要認它就 import 這個函式,不要各自抄一份字面(review #69)。
export const cancelledMsg = () => '已中止';

// 播放控制(quiet)跑超過這麼久才亮執行狀態列:按一下暫停不該整條 dock 閃一下,
// 但卡住(等 token 鎖沒有上限、在等系統對話框)時一定要看得到在等什麼、也要按得到中止(review #65 第 1 點)。
const QUIET_MS = 800;

export class Console {
  constructor(root, api, notice) {
    this.root = root; this.api = api; this.notice = notice;
    this.running = false; this.job = null; this.hooks = {}; this.waiters = [];
    // dock 的執行狀態列(設計規格 §6 規則 2 / §10 running):七頁都看得到,頁面按鈕發起的命令也算。
    this.bar = document.getElementById('busy');
    this.barCmd = document.getElementById('busy-cmd');
    this.barAct = document.getElementById('busy-act');
    this.barTime = document.getElementById('busy-time');
    this.barProg = document.getElementById('busy-progress');
    this.barSR = document.getElementById('busy-sr');
    this.stopBtn = document.getElementById('cancel');
    this.stopBtn.addEventListener('click', () => this.stop());
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
  // quiet:播放控制用——同一個序列槽、同一個閘、同一顆中止,但不畫區塊(輸出不進主控台),
  // 執行狀態列過了 QUIET_MS 還沒結束才亮。
  // label:頁面給的白話(「讀取你的清單」);執行狀態列與收尾的那句話用它,命令原文留在主控台與 title(決策 45)。
  // args:直接送 argv 陣列(精靈用,決策 46):伺服器的 splitArgs 只認雙引號、沒有跳脫,而 local 的清單 ID 含空白是常態、
  //   含 " 就組不出來。有給 args 時 line 只拿來顯示(沒給就用 args 接起來),伺服器端的拒絕清單本來就是對 argv 做的。
  // promptHost:提示與授權連結畫進這個容器,不畫進主控台的區塊(精靈做到一半不把人丟進終端機);hooks.onPrompt 讓
  //   頁面在提示旁補一句白話。容器所在的頁面若是 hidden,照樣會切過去(同 #64:看不到的提示等於沒有)。
  async run(line, hooks = {}, { quiet = false, label = '', args = null, promptHost = null } = {}) {
    // 一次一個(決策 40 的序列槽)。進行中再叫就地擋下:不送出、不碰進行中那一次的 job / hooks。
    // 以前是照送、吃 409,而被擋的那一次收尾時會把進行中那次的 job 與 hooks 清掉——中止、提示回答、
    // 頁面結果全跟著失效;連點兩下、或跑 sync 時第一次切到帳號頁就會撞到。
    if (this.running) {
      this.notice(`正在執行 ${this.barCmd.textContent},等它結束` + (this.bar.hidden ? '' : '或按「中止」'));
      const busy = [-1, '另一個命令執行中', 'busy'];
      hooks.onExit?.(...busy);
      return busy;
    }
    if (args && !line) line = args.join(' ');
    const raw = maskSecrets(line || '(help)');
    const shown = label || raw;
    this.running = true; this.quiet = quiet; this.job = null; this.hooks = hooks; this.ex = null;
    this.promptHost = promptHost; this.openPrompt = null;
    // quiet 的區塊不掛上主控台;有提示 / 授權連結時才掛上去(event())。
    const b = quiet ? document.createElement('article') : this.block(line || '(help)');
    this.slotOn(shown, raw);
    if (quiet) this.later = setTimeout(() => this.busyOn(), QUIET_MS);
    else { this.busyOn(); this.notice(''); }
    document.body.dataset.connected = 'true';
    try {
      const r = await this.api.fetch('/api/run', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(args ? { args } : { line }),
      });
      if (!r.ok) {
        let msg = r.statusText;
        try { msg = (await r.json()).error || msg; } catch (_) { /* 非 JSON */ }
        this.refused(b, r.status, msg);
        this.ex = [-1, msg, 'refused'];
      } else {
        await this.stream(r.body, b);
      }
    } catch (e) {
      const msg = '連線中斷:' + e.message;
      this.exit(b, 1, msg, 'disconnected');
      document.body.dataset.connected = 'false';
      this.ex = [1, msg, 'disconnected'];
    } finally {
      this.running = false; this.job = null; this.hooks = {};
      // 串流斷掉時 prompt_closed 不會到:還開著的那一則自己收掉(不可以看起來還能按),也不可以再搶焦點(review #64 / #68)。
      if (this.openPrompt) this.closeBox(this.openPrompt, 'disconnected', b);
      this.promptHost = null; this.openPrompt = null;
      delete b.dataset.running;
      this.busyOff();
    }
    // 串流結束才通知頁面:此刻伺服器已放開序列槽(runMu 在 handler return 前 Unlock,回應在那之後才收尾)、
    // 這邊的 running 也已歸零。在 exit 事件當下叫的話,onExit 裡再跑一個命令(帳號頁登入完刷新)
    // 會被上面的閘擋掉,或撞上伺服器還沒放的鎖(review #62 第 2 點)。
    let ex = this.ex || [1, '串流在 exit 之前就結束了', 'disconnected'];
    // 使用者自己中止的:頁面拿到的是「已中止」,不是一串「Get …: context canceled」(完整原文留在主控台)。
    // exit 0 = 命令其實做完了(中止落在不吃取消的那一段,例如 osascript),照「完成」算。
    if (isCancelled(ex)) ex = [ex[0], cancelledMsg(), ex[2]];
    if (!quiet || this.barShown) this.announce(shown, ex); // 按一下暫停不必念「完成:pause」
    // 頁面的 onExit 與等著的自動讀取可能立刻接著跑下一個命令(它會清掉命令列上方那行):
    // 收尾的那句話放在它們之後說,不然從別頁看的人連一眼都看不到。
    try { hooks.onExit?.(...ex); } finally { this.wake(); this.lastDone = ''; }
    // args 的那一次不預填命令列:接起來的那一行走 splitArgs 會被切碎(含空白的 local ID),正是 args 要避開的事;
    // 發起它的頁面有自己的「再試一次」。
    this.report(args ? '' : line, shown, ex, quiet);
    return ex;
  }

  // idle(fn):目前沒有命令在跑就立刻跑 fn,否則等它結束(頁面第一次進來的自動讀取用:跑 sync 時切到帳號頁,
  // 不該撞上它、也不該畫成「未登入」)。檢查與開跑在同一個同步段裡:兩頁同時在等時,第一頁開跑後 running
  // 已經是 true,第二頁會重新排隊——拆成「await idle() 再 run()」的話,兩頁都看到 running=false,第二頁被閘擋掉。
  idle(fn) {
    if (this.running) { this.waiters.push(() => this.idle(fn)); return; }
    fn();
  }

  wake() {
    const w = this.waiters;
    this.waiters = [];
    for (const f of w) {
      try { f(); } catch (e) { console.error(e); } // 一頁的錯不能讓後面等著的頁面永遠醒不來
    }
  }

  // 佔槽(每一次都有,當下就做):頁面按鈕的閘(body[data-slot])與 aria-disabled——按了沒用這件事,
  // 輔助技術從第一刻就要知道。看得到的部分(調暗、狀態列、頂線)在 busyOn()。
  slotOn(shown, raw) {
    this.t0 = Date.now();
    this.barCmd.title = `${raw}(到主控台看完整輸出)`;
    this.stopping = false; this.wrote = false; this.armed = false; this.barShown = false; this.lastAct = '';
    this.barCmd.textContent = shown;
    this.barAct.textContent = '';
    this.barProg.hidden = true;
    this.stopBtn.removeAttribute('aria-disabled');
    this.stopBtn.textContent = '中止';
    document.body.dataset.slot = '';
    // <select data-run>(語言選單)沒有 click 可以擋,換值就會送命令:直接停用。
    // ponytail: 停用會讓焦點離開它;換語言成功就重新載入,只有失敗那次焦點回不去。
    document.querySelectorAll('[data-run]').forEach((x) => { x.setAttribute('aria-disabled', 'true'); if (x.tagName === 'SELECT') x.disabled = true; });
  }

  // 執行狀態列:一般命令點下去的當下就亮(不等伺服器回 start),播放控制過了 QUIET_MS 才亮;
  // 串流收尾才熄——在 exit 事件就熄的話,使用者以為可以按下一個了,伺服器卻還握著序列槽。
  busyOn() {
    this.barShown = true;
    this.tick();
    this.timer = setInterval(() => this.tick(), 1000);
    this.bar.hidden = false;
    document.body.dataset.busy = '';
    // 同一拍剛結束的那一次(onExit 接著跑的刷新、等著的自動讀取)的結果不能被蓋掉沒念到:接在前面。
    this.barSR.textContent = (this.lastDone ? this.lastDone + '。' : '') + '執行中:' + this.barCmd.textContent;
    this.lastDone = '';
  }

  busyOff() {
    clearTimeout(this.later); clearInterval(this.timer); clearTimeout(this.stuck); clearTimeout(this.disarm);
    if (document.activeElement === this.stopBtn) document.getElementById('cmd')?.focus(); // 別讓焦點跟著列一起消失
    this.bar.hidden = true;
    delete document.body.dataset.busy;
    delete document.body.dataset.slot;
    document.querySelectorAll('[data-pending]').forEach((x) => { delete x.dataset.pending; });
    document.querySelectorAll('[data-run]').forEach((x) => { x.removeAttribute('aria-disabled'); if (x.tagName === 'SELECT') x.disabled = false; });
  }

  // 計時是頁面自己算的(伺服器沒有進度事件,也不該編一個百分比);這一格 aria-hidden,不會每秒播報。
  tick() {
    const s = Math.floor((Date.now() - this.t0) / 1000);
    const mm = String(Math.floor(s / 60)).padStart(2, '0');
    this.barTime.textContent = `running ${mm}:${String(s % 60).padStart(2, '0')}`;
  }

  // 活動列:stderr 最後一行(等鎖、rate limit 退避、doctor 的逐項結果……stderr 是進度不是失敗,設計規格 §5)。
  activity(text) {
    const last = text.split('\n').map((s) => s.trim()).filter(Boolean).pop();
    if (!last) return;
    this.lastAct = last; // 兩段式中止的警告過期時要還原成這一行
    if (!this.stopping && !this.armed) this.barAct.textContent = last; // 中止相關的說明優先,不被後面的輸出蓋掉
  }

  // 真實進度(決策 47):total > 0 才有進度條與「n / total」;total == 0 只是階段的標記。
  progress(ev) {
    const counted = ev.total > 0;
    this.barProg.hidden = !counted;
    if (counted) { this.barProg.max = ev.total; this.barProg.value = ev.done; }
    this.lastAct = (stages()[ev.stage] || ev.stage) +(counted ? ` ${ev.done} / ${ev.total}` : '');
    if (!this.stopping && !this.armed && !this.openPrompt) this.barAct.textContent = this.lastAct;
  }

  // 結束的那一句給螢幕閱讀器(role=status)。
  announce(shown, ex) {
    const [code, , reason] = ex;
    const done = isCancelled(ex) ? cancelledMsg() :reason === 'refused' ? '未執行' : (code === 0 ? '完成' : `結束(exit ${code})`);
    this.lastDone = `${done}:${shown}`;
    this.barSR.textContent = this.lastDone;
  }

  // 收尾的一句話(命令列上方):被伺服器拒絕的一律說;讀不到主控台那一頁時(命令從別頁的按鈕發出),
  // 失敗與中止也要說,不然沒掛 onExit 的頁面動作(play、pl list、migrate …)失敗了一點痕跡都沒有。
  // 播放控制(quiet)的輸出不進主控台:它的失敗(沒有作用中的裝置、授權過期…)一律在這裡說,也不預填重跑。
  report(line, shown, ex, quiet) {
    const [code, msg, reason] = ex;
    if (isCancelled(ex) && !quiet) {
      const i = document.getElementById('cmd'); // 設計規格 §10:中止後命令列預填同一條命令供重跑
      if (i && !i.value && line) i.value = line;
    }
    if (reason === 'refused') { this.notice(msg); return; }
    if (!quiet && !this.root.closest('.page')?.hidden) return;
    const where = quiet ? '' : '(完整輸出在主控台)';
    if (isCancelled(ex)) this.notice(`已中止:${shown}`);
    else if (code > 0) this.notice(`✗ ${shown}:${(msg || '').replace(/^Error: /, '')}${where}`);
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
      case 'start':
        this.job = ev.job; b.dataset.job = ev.job;
        if (this.stopping) this.cancel(); // start 之前就按了中止:job id 一到就送
        break;
      case 'stdout': this.append(b, 'block__out', ev.text); this.hooks.onStdout?.(ev.text); break;
      case 'stderr': this.append(b, 'block__err', ev.text); this.activity(ev.text); break;
      case 'table': b.appendChild(renderTable(ev.header, ev.rows)); this.hooks.onTable?.(ev.header, ev.rows); break;
      case 'progress': this.progress(ev); this.hooks.onProgress?.(ev); break;
      // onExit 不在這裡叫,由 run() 在串流收尾後叫(理由見 run())。
      case 'exit': this.exit(b, ev.code, ev.message, ev.reason); this.ex = [ev.code, ev.message, ev.reason]; break;
      case 'prompt':
        if (!b.parentNode && !this.promptHost) this.root.appendChild(b); // quiet 的區塊平常不掛上去;要問人就得看得到
        this.prompt(ev, b);
        // 別蓋掉「確定中止?」的警告。提示畫在頁面自己的容器裡時,那一頁就看得到原文,狀態列只說在等。
        if (!this.stopping && !this.armed) this.barAct.textContent = this.promptHost ? '等你回答' : '等你回答:' + ev.title;
        break;
      case 'prompt_closed':
        this.promptClosed(ev, b);
        if (!this.stopping && !this.armed) this.barAct.textContent = '';
        break;
      case 'open_url':
        if (!b.parentNode && !this.promptHost) this.root.appendChild(b);
        this.openURL(ev, b);
        break;
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
        const yes = btn(ev.affirmative || '確定', ev.default === true ? 'btn--primary' : '', () => {
          // 區塊裡有變更表、又按了肯定 = 答應寫入:之後的中止可能停在半套,stop() 要按第二次。
          if (b.querySelector('table')) this.wrote = true;
          answer(false, true);
        });
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
    const host = this.promptHost || b;
    host.appendChild(box);
    this.openPrompt = box;
    if (focus) focus.dataset.autofocus = ''; // 切頁時 route() 靠它把焦點交回提示,見 reveal()
    this.hooks.onPrompt?.(ev, box);
    this.reveal(host);
    this.stick(b);
    if (focus) setTimeout(() => focus.focus({ preventScroll: true }), 0); // 不覆蓋 stick() 的捲動判斷
  }

  // 從別頁按鈕發出的命令,區塊一樣在主控台頁裡,而那一頁此刻是 hidden:提示與授權連結畫在那裡等於沒畫,
  // 命令只會卡到提示逾時,Apple 的揭露也只剩伺服器端「送出過」(review #62 第 5 點)。所以要切回主控台。
  // 切頁的 hashchange 可能晚於上面的 setTimeout(那時焦點落在 hidden 子樹裡是 no-op),由 route() 叫 focusPrompt() 補上。
  // host = 提示實際畫在哪裡:主控台的區塊,或呼叫端給的 promptHost。它所在的頁面是 hidden 就切過去。
  reveal(host) {
    const page = (host && host.closest('.page')) || this.root.closest('.page');
    if (page && page.hidden) location.hash = page.id === 'page-console' || !page.id ? '#/console' : '#/' + page.id.replace(/^page-/, '');
  }

  // page:剛切到的那一頁。只認「還在跑的那一次」開著的提示(this.openPrompt 在 run() 收尾時清掉):串流斷掉時
  // prompt_closed 永遠不會到,殘留的提示不可以永久搶走命令列的焦點。disabled = 答案已送出、prompt_closed
  // 還沒回來,focus() 打在它上面是 no-op(review #64)。
  focusPrompt(page) {
    const box = this.running ? this.openPrompt : null;
    const f = box && !box.classList.contains('is-closed') ? box.querySelector('[data-autofocus]') : null;
    if (!f || f.disabled || (page && !page.contains(box))) return false;
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
    const box = (this.promptHost || b).querySelector(`.prompt[data-id="${CSS.escape(String(ev.id))}"]`);
    if (!box) return;
    this.closeBox(box, ev.reason, b);
    this.hooks.onPromptClosed?.(ev); // 頁面要知道是怎麼收的:關掉 / 逾時是 exit 1 + user aborted,不是「取消」的 exit 2
  }

  // closeBox:一則提示收掉的樣子(不能再按、標明怎麼收的)。reason 除了伺服器的四種,還有 disconnected:
  // 串流斷掉時 prompt_closed 不會到,run() 收尾時自己收——不然它畫在頁面的容器裡,看起來還能按、按了沒反應(review #68)。
  closeBox(box, reason, b) {
    if (this.openPrompt === box) this.openPrompt = null;
    // 提示畫在頁面的容器裡時,收掉之後搬進主控台的區塊:頁面上只留「現在在問的那一則」,
    // 主控台仍然是每個命令完整的紀錄(問了什麼、怎麼收的,含 Apple 的揭露)。
    if (this.promptHost && b.parentNode) b.appendChild(box);
    box.classList.add('is-closed');
    box.dataset.reason = reason;
    box.querySelectorAll('button,input').forEach((c) => { c.disabled = true; });
    const text = { answered: '已回答', dismissed: '已關掉', timeout: '等待回答逾時,命令已取消', cancelled: '命令已取消', disconnected: '連線中斷,這一則已經失效' }[reason] || reason;
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
    const host = this.promptHost || b;
    host.appendChild(p);
    if (host !== b) b.appendChild(p.cloneNode(true)); // 主控台的紀錄也留一份
    this.reveal(host);
    this.stick(b);
  }

  exit(b, code, message, reason) {
    b.dataset.exit = String(code);
    const el = document.createElement('div');
    el.className = 'block__exit';
    el.dataset.code = String(code);
    el.setAttribute('role', 'status');
    // 使用者自己按的中止(或關掉提示)不是錯誤:· 與 muted 左線,不印「context canceled」這種內部字眼。
    const cancelled = isCancelled([code, message, reason]);
    if (cancelled) { b.dataset.exit = 'cancelled'; el.dataset.code = 'cancelled'; }
    const mark = code === 0 ? '✓' : (cancelled || code === 2 || code === 3 ? '·' : '✗');
    let text = `${mark} exit ${code}`;
    // 取消原因本身(errWebCancelled:zh-TW「已取消」、en「cancelled」)或 context canceled 那串都只是在重複「已取消」,不印。
    // ponytail: 比對兩種語系的文字,T3(計畫 §2.4 第 5 點)改成只看 reason。
    if (cancelled) { text += ' · 已取消'; if (/context canceled|^(Error: )?(已取消|cancelled)$/.test(message || '')) message = ''; }
    else if (reason === 'shutdown') text += ' · capy --web 已結束';
    else if (reason === 'timeout') text += ' · 等待回答逾時';
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
    if (status === 503) document.body.dataset.stale = '';
    this.stick(b);
    // 沒跑成也要通知發起的頁面(run() 收尾時以 -1 叫 onExit):否則那一頁的 render 永遠不會收尾,
    // 而 route() 的 ready 又保證不會重新初始化——頁面就永久空白了(review #62)。
  }

  // 中止(設計規格 §10 interrupted / cancelled):打 cancel 端點,生效點與終端機的 Ctrl-C 相同。
  // 按下去不等於停了:燈要等串流收尾才熄;start 還沒到(不知道 job id)就先記著,start 一到就送。
  async stop() {
    if (!this.running || this.stopping) return;
    // 已答應寫入:中止可能停在「平台已寫、Drive 未寫」的半套(計畫 Q24),要再按一次確認。
    if (this.wrote && !this.armed) {
      this.armed = true;
      this.stopBtn.textContent = '確定中止?';
      this.barAct.textContent = '已經開始寫入:現在中止可能只寫了一半,下一次 sync 會把差異列出來';
      this.disarm = setTimeout(() => { // 過期:按鈕與活動列都還原,別留著一句看起來還在等確認的警告(review #65 第 2 點)
        this.armed = false;
        this.stopBtn.textContent = '中止';
        this.barAct.textContent = this.lastAct || '';
      }, 5000);
      return;
    }
    clearTimeout(this.disarm);
    this.stopping = true;
    // 不用 disabled:disabled 的按鈕會把焦點丟到 body,鍵盤使用者就失去位置,收尾時也交不回命令列。
    // 重複按由上面的 this.stopping 擋。
    this.stopBtn.setAttribute('aria-disabled', 'true');
    this.stopBtn.textContent = '中止中…';
    this.barAct.textContent = '已送出中止,等命令收尾';
    // 網路請求、等鎖、退避都會立刻停;已送出的 token 換發(最多 30 秒)、鑰匙圈與 Music.app 的 osascript
    // 不吃取消,要等它們自己回來(review #65 第 3 點:換發是這個 PR 自己造出來的等待,要點名)。
    this.stuck = setTimeout(() => {
      this.barAct.textContent = '命令還沒停下:可能正在換發登入 token(最多 30 秒),或在等這台電腦上的系統對話框(鑰匙圈 / Music.app);真的卡住就在終端機按 Ctrl-C 結束 capy --web';
    }, 8000);
    if (this.job) await this.cancel();
  }

  async cancel() {
    if (!this.job) return;
    try {
      const r = await this.api.fetch(`/api/jobs/${encodeURIComponent(this.job)}/cancel`, { method: 'POST' });
      // 404 = 那個 job 已經收尾(中止與結束擦身而過),串流馬上就會結束,不必多說。
      if (!r.ok && r.status !== 404) throw new Error('HTTP ' + r.status);
    } catch (e) { // 同 answer():裸 await 在斷線時是 unhandled rejection,使用者只看到「按了沒反應」
      this.notice('中止沒送到:' + e.message);
      clearTimeout(this.stuck);
      this.stopping = false;
      this.stopBtn.removeAttribute('aria-disabled');
      this.stopBtn.textContent = '中止';
    }
  }
}
