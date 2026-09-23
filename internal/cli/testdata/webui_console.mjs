// webui_console.mjs:在 node 裡跑真的 console.js(TestWebConsoleBehaviour 把它與 console.js / table.js 複製到暫存目錄)。
// 字串契約證明不了時序:這裡用最小的 DOM 替身與假的 /api/run,釘住執行狀態列、中止與「一次一個」的行為。
// 任何一條不成立就印出來並以 1 結束。

const pages = { hidden: false };
function mk(tag = 'div') {
  return {
    tagName: tag.toUpperCase(), dataset: {}, children: [], attrs: {}, hidden: false, value: '', _text: '',
    classList: {
      s: new Set(),
      add(c) { this.s.add(c); },
      remove(c) { this.s.delete(c); },
      contains(c) { return this.s.has(c); },
    },
    get className() { return [...this.classList.s].join(' '); },
    set className(v) { this.classList.s = new Set(String(v).split(/\s+/).filter(Boolean)); },
    get textContent() { return this._text + this.children.map((c) => c.textContent).join(''); },
    set textContent(v) { this._text = String(v); this.children = []; },
    set innerHTML(_) { this._text = ''; this.children = []; },
    appendChild(c) { if (c.parentNode?.children) c.parentNode.children = c.parentNode.children.filter((x) => x !== c); this.children.push(c); c.parentNode = this; return c; }, // 同真的 DOM:append 已掛著的節點 = 搬家
    append(...cs) { cs.forEach((c) => this.appendChild(c)); },
    replaceChildren(...cs) { this.children = []; this.append(...cs); },
    remove() { if (this.parentNode) this.parentNode.children = this.parentNode.children.filter((x) => x !== this); },
    insertBefore(c, ref) { const i = this.children.indexOf(ref); this.children.splice(i < 0 ? this.children.length : i, 0, c); c.parentNode = this; return c; },
    get firstChild() { return this.children[0] || null; },
    contains(x) { for (let n = x; n; n = n.parentNode) if (n === this) return true; return false; },
    get lastElementChild() { return this.children[this.children.length - 1] || null; },
    querySelector(sel) { return find(this, sel); },
    querySelectorAll() { return []; },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return this.attrs[k] ?? null; },
    removeAttribute(k) { delete this.attrs[k]; },
    hasAttribute(k) { return k in this.attrs; },
    addEventListener(t, f) { (this.l ||= {})[t] = f; },
    click() { this.l?.click?.(); },
    scrollIntoView() {},
    focus() { globalThis.document.activeElement = this; },
    closest() { return pages; },
    scrollHeight: 0, scrollTop: 0, clientHeight: 0,
  };
}
// 只認 '.class'、'tag'、'[data-x]' 與 '.class[data-k="v"]':console.js 用到的就這些,其餘回 null。
const camel = (k) => k.replace(/-(\w)/g, (_, c) => c.toUpperCase());
function find(root, sel) {
  const cls = /^\.([\w-]+)$/.exec(sel);
  const tag = /^([a-z]+)$/.exec(sel);
  const data = /^\[data-([\w-]+)\]$/.exec(sel);
  const clsData = /^\.([\w-]+)\[data-([\w-]+)="([^"]*)"\]$/.exec(sel);
  if (!cls && !tag && !data && !clsData) return null;
  const hit = (e) => {
    if (cls) return e.classList?.contains(cls[1]);
    if (tag) return e.tagName === tag[1].toUpperCase();
    if (data) return e.dataset && camel(data[1]) in e.dataset;
    return e.classList?.contains(clsData[1]) && e.dataset?.[camel(clsData[2])] === clsData[3];
  };
  const walk = (e) => {
    for (const c of e.children || []) {
      if (hit(c)) return c;
      const r = walk(c);
      if (r) return r;
    }
    return null;
  };
  return walk(root);
}
const ids = {};
globalThis.document = {
  body: mk('body'),
  activeElement: null,
  getElementById(id) { return (ids[id] ||= mk()); },
  createElement: mk,
  createTextNode: (t) => ({ textContent: t, children: [] }),
  querySelectorAll() { return []; },
};
globalThis.sessionStorage = { getItem() { return null; }, setItem() {} };
globalThis.location = { hash: '' };
globalThis.CSS = { escape: (s) => s };

const { Console, maskSecrets } = await import('./console.mjs');

// ── 假的伺服器:/api/run 回 SSE(start → [gate] → events),cancel 端點記下來 ──
const calls = [];
const bodies = [];
const answers = [];
const cancels = [];
let script = {};
const sse = (evs) => evs.map((e) => `data: ${JSON.stringify(e)}\n\n`).join('');
const done = { type: 'exit', code: 0, message: '', reason: 'done' };
const api = {
  async fetch(path, init) {
    if (path === '/api/run') {
      const sent = JSON.parse(init.body);
      bodies.push(sent);
      const line = sent.line ?? sent.args.join(' ');
      calls.push(line);
      const spec = script[line] || {};
      if (spec.fetchGate) await spec.fetchGate;
      if (spec.status) {
        return new Response(JSON.stringify({ error: spec.error }), { status: spec.status, headers: { 'Content-Type': 'application/json' } });
      }
      const job = String(calls.length);
      const enc = new TextEncoder();
      const body = new ReadableStream({
        async start(c) {
          c.enqueue(enc.encode(sse([{ type: 'start', job }, ...(spec.first || [])])));
          if (spec.gate) await spec.gate;
          c.enqueue(enc.encode(sse(spec.events || [done])));
          c.close();
        },
      });
      return new Response(body, { status: 200 });
    }
    if (/^\/api\/jobs\/[^/]+\/answer$/.test(path)) {
      answers.push(JSON.parse(init.body));
      script.onAnswer?.();
      return new Response(null, { status: 204 });
    }
    if (/^\/api\/jobs\/[^/]+\/cancel$/.test(path)) {
      cancels.push(path);
      return new Response(null, { status: 204 });
    }
    throw new Error('unexpected fetch ' + path);
  },
};
const gate = () => {
  let release;
  const p = new Promise((r) => { release = r; });
  return [p, release];
};
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));

const notices = [];
const root = mk();
const con = new Console(root, api, (t) => notices.push(t));
const allByClass = (e, cls, out = []) => { for (const c of e.children || []) { if (c.classList?.contains(cls)) out.push(c); allByClass(c, cls, out); } return out; };
const failures = [];
const check = (ok, msg) => { if (!ok) failures.push(msg); };
// 一組情境丟例外(例如舊版沒有某個方法)只算那一組失敗,其餘照跑,才看得出哪幾條不成立。
const scenario = async (name, fn) => {
  try { await fn(); } catch (e) { failures.push(`情境 ${name} 丟出例外:${e.message}`); }
};
const reset = () => { bodies.length = 0; answers.length = 0; globalThis.location.hash = ''; calls.length = 0; cancels.length = 0; notices.length = 0; script = {}; pages.hidden = false; globalThis.document.getElementById('cmd').value = ''; };

// 1. 回聲遮罩:切法跟伺服器的 splitArgs 一樣寬(連續空白、tab、引號包住的 flag 名與值)。
check(typeof maskSecrets === 'function', 'console.js 要匯出 maskSecrets');
if (typeof maskSecrets === 'function') for (const [line, secret] of [
  ['auth login google --client-secret  s3cr3t', 's3cr3t'],
  ['auth login google --client-secret\ts3cr3t', 's3cr3t'],
  ['auth login apple "--user-token" s3cr3t', 's3cr3t'],
  ['auth login apple --user-token "ab cd"', 'cd'],
  ['auth login apple --developer-token=abc', 'abc'],
]) check(!maskSecrets(line).includes(secret), `maskSecrets 漏遮:${JSON.stringify(line)} → ${JSON.stringify(maskSecrets(line))}`);
check(typeof maskSecrets !== 'function' || maskSecrets('pl pull --all') === 'pl pull --all', '沒有秘密的命令要原樣');

// 2. 一次一個:進行中再 run() 就地擋下,不送出、不碰進行中那一次的 job / hooks。
await scenario('2', async () => {
  reset();
  const [g, release] = gate();
  script = { A: { gate: g, events: [{ type: 'stdout', text: 'hi\n' }, done] } };
  let outA = '';
  let exA = null;
  let exB = null;
  const pA = con.run('A', { onStdout: (t) => { outA += t; }, onExit: (...e) => { exA = e; } });
  await tick(5);
  const rB = await con.run('B', { onExit: (...e) => { exB = e; } });
  check(calls.length === 1, `進行中的第二次 run() 不可以送出:${calls}`);
  check(exB?.[2] === 'busy' && rB?.[2] === 'busy', `被擋的那一次要以 busy 通知頁面:${exB}`);
  check(con.job === '1' && con.running, '被擋的那一次不可以清掉進行中那次的 job');
  release();
  await pA;
  check(outA === 'hi\n' && exA?.[0] === 0, `進行中那一次的 hooks 要照常收到輸出與 exit:${outA} ${exA}`);
});

// 3. onExit 在串流收尾後才叫:此刻 running 已歸零,在 onExit 裡接著跑的命令(帳號頁登入完刷新)要送得出去。
await scenario('3', async () => {
  reset();
  let runningAtExit = null;
  let nested = null;
  await con.run('login', {
    onExit: () => {
      runningAtExit = con.running;
      con.run('status', { onExit: (...e) => { nested = e; } });
    },
  });
  await tick(20);
  check(runningAtExit === false, 'onExit 被叫時 running 要已經是 false');
  check(calls.includes('status') && nested?.[2] === 'done', `onExit 裡接著跑的命令要送得出去:${calls} ${nested}`);
});

// 4. 兩頁同時在等 idle():兩個都要跑到,第二個不能被閘擋掉畫成空白(review)。
await scenario('4', async () => {
  reset();
  const [g, release] = gate();
  script = { sync: { gate: g } };
  const p = con.run('sync');
  await tick(5);
  const got = {};
  con.idle(() => con.run('auth status', { onExit: (...e) => { got.a = e; } }));
  con.idle(() => con.run('export', { onExit: (...e) => { got.b = e; } }));
  check(calls.length === 1, 'idle() 在命令結束前不可以跑');
  release();
  await p;
  await tick(50);
  check(calls.includes('auth status') && calls.includes('export'), `兩個等待中的頁面都要跑到:${calls}`);
  check(got.a?.[2] === 'done' && got.b?.[2] === 'done', `兩個都不可以被擋成 busy:${JSON.stringify(got)}`);
});

// 5. start 之前就按了中止:job id 一到就送 cancel。
await scenario('5', async () => {
  reset();
  const [fg, release] = gate();
  script = { slow: { fetchGate: fg } };
  const p = con.run('slow');
  await tick(5);
  await con.stop();
  check(cancels.length === 0, '還不知道 job id 時不可以亂送 cancel');
  release();
  await p;
  check(cancels.length === 1 && cancels[0] === `/api/jobs/${calls.length}/cancel`, `start 一到就要送 cancel:${cancels}`);
});

// 6. 從別頁發出的命令失敗:收尾那句不能被接著自動跑的命令清掉(review)。
await scenario('6', async () => {
  reset();
  pages.hidden = true;
  const [g, release] = gate();
  script = { sync: { gate: g, events: [{ type: 'exit', code: 1, message: 'Error: boom', reason: 'done' }] } };
  // onExit 裡接著跑(帳號頁登入完刷新就是這樣):它一開跑就 notice(''),收尾那句要在它之後才說。
  const p = con.run('sync', { onExit: () => con.run('auth status') });
  await tick(5);
  release();
  await p;
  await tick(30);
  const last = notices[notices.length - 1] || '';
  check(last.includes('✗') && last.includes('boom'), `失敗的那句要留在命令列上方:${JSON.stringify(notices)}`);
});

// 7. exit 0 + reason cancelled = 命令其實做完了:不說「已中止」、不預填重跑。
await scenario('7', async () => {
  reset();
  script = { p: { events: [{ type: 'exit', code: 0, message: '', reason: 'cancelled' }] } };
  let e = null;
  await con.run('p', { onExit: (...x) => { e = x; } });
  const cmdValue = globalThis.document.getElementById('cmd').value;
  check(e?.[1] !== '已中止' && cmdValue === '', `做完的命令不可以當成中止:${e} / 預填「${cmdValue}」`);
});

// 8. 播放控制(quiet):同一個閘、idle() 要等;不畫區塊;卡住超過 0.8 秒要亮狀態列、而且中止得了(review #65 第 1 點)。
await scenario('8', async () => {
  reset();
  const bar = globalThis.document.getElementById('busy');
  bar.hidden = true;
  const blocksBefore = con.root.children.length;
  const [g, release] = gate();
  script = { play: { gate: g } };
  const p = con.run('play', {}, { quiet: true });
  await tick(5);
  const r = await con.run('x');
  check(r?.[2] === 'busy' && !calls.includes('x'), '播放控制在跑時 run() 要就地擋下');
  let ran = false;
  con.idle(() => { ran = true; });
  check(!ran, 'idle() 要等播放控制結束');
  check(bar.hidden === true, '短的播放控制不亮狀態列');
  check(globalThis.document.body.dataset.slot === '' && !('busy' in globalThis.document.body.dataset), '佔槽當下就標 data-slot,看得到的 data-busy 要等 0.8 秒');
  await tick(900);
  check(bar.hidden === false && 'busy' in globalThis.document.body.dataset, '卡住超過 0.8 秒要亮狀態列');
  await con.stop();
  check(cancels.length === 1, `卡住的播放控制要中止得了:${cancels}`);
  release();
  await p;
  check(ran, '播放控制結束後 idle() 要跑');
  check(con.root.children.length === blocksBefore, '播放控制不在主控台畫區塊');
});

// 8b. 兩段式中止的警告過期:按鈕與活動列都要還原,別留著一句看起來還在等確認的警告(review #65 第 2 點)。
await scenario('8b', async () => {
  reset();
  const [g, release] = gate();
  script = { w: { gate: g } };
  const p = con.run('w');
  await tick(5);
  con.activity('讀取 Drive…\n');
  con.wrote = true;
  await con.stop();
  const act = globalThis.document.getElementById('busy-act');
  check(act.textContent.includes('寫'), '已答應寫入時第一次按中止要先警告');
  await tick(5200);
  check(!con.armed && act.textContent === '讀取 Drive…', `警告過期後活動列要還原成最後一行:「${act.textContent}」`);
  release();
  await p;
});

// 8c. label(決策 45):執行狀態列與說明用頁面給的白話,命令原文只在 title 與主控台;秘密照樣遮。
await scenario('8c', async () => {
  reset();
  const [g, release] = gate();
  script = { 'pl list --provider spotify': { gate: g } };
  const p = con.run('pl list --provider spotify', {}, { label: '讀取 Spotify 上的清單' });
  await tick(5);
  const cmd = globalThis.document.getElementById('busy-cmd');
  check(cmd.textContent === '讀取 Spotify 上的清單', `執行狀態列要顯示白話標籤:「${cmd.textContent}」`);
  check((cmd.title || '').includes('pl list --provider spotify'), `命令原文要留在 title:「${cmd.title}」`);
  await con.run('x');
  check((notices[notices.length - 1] || '').includes('讀取 Spotify 上的清單'), `被擋的說明也用白話:${JSON.stringify(notices)}`);
  release();
  await p;
});

// 8d. 精靈的兩個選項(決策 46):args 送出的 body 只有 args(伺服器端 line 會蓋掉 args;review #66);
//     promptHost 讓提示畫進頁面的容器、不切頁,onPrompt 讓頁面補白話;回答照樣走 answer 端點。
await scenario('8d', async () => {
  reset();
  const host = mk();
  const [g, release] = gate();
  const args = ['migrate', 'dev1/My "Road" Trip.m3u8', '--from', 'local', '--to', 'spotify'];
  script = {
    [args.join(' ')]: {
      first: [{ type: 'prompt', id: 7, kind: 'confirm', title: '把 14 首加進去?', affirmative: '套用', negative: '取消', default: false }],
      gate: g,
      events: [{ type: 'prompt_closed', id: 7, reason: 'answered' }, { type: 'exit', code: 2, message: '已取消', reason: 'done' }],
    },
    onAnswer: release,
  };
  let prompted = null;
  let ex = null;
  const blocksBefore = con.root.children.length;
  const p = con.run('', { onPrompt: (ev, box) => { prompted = [ev.title, box]; }, onExit: (...e) => { ex = e; } },
    { args, label: '把清單搬到 Spotify', promptHost: host });
  await tick(20);
  check(JSON.stringify(bodies[0]) === JSON.stringify({ args }), `args 送出的 body 只能有 args、不可以帶 line:${JSON.stringify(bodies[0])}`);
  const box = host.children.find((c) => c.classList.contains('prompt'));
  check(!!box, '提示要畫進呼叫端給的容器');
  const block = con.root.children[con.root.children.length - 1];
  check(con.root.children.length === blocksBefore + 1 && !block.children.some((c) => c.classList?.contains('prompt')), '主控台的區塊照畫,但提示不畫在裡面');
  check(globalThis.location.hash === '', `容器所在的頁面看得到時不切頁:${globalThis.location.hash}`);
  check(prompted?.[0] === '把 14 首加進去?' && prompted?.[1] === box, 'onPrompt 要拿到事件與那個提示框');
  check(con.focusPrompt() === true && globalThis.document.activeElement?.textContent === '取消', '焦點給提示的預設鍵(取消)');
  const no = find(box, '.prompt__row').children.find((b) => b.textContent === '取消');
  no.click();
  await p;
  check(answers.length === 1 && answers[0].id === 7 && answers[0].value === false, `回答走 answer 端點,頁面沒有替使用者回答:${JSON.stringify(answers)}`);
  check(box.classList.contains('is-closed'), 'prompt_closed 要在容器裡找得到那個提示並收掉它');
  check(block.children.includes(box) && !host.children.includes(box), '收掉的提示要搬進主控台的區塊(主控台是完整紀錄),頁面上只留現在在問的那一則');
  check(ex?.[0] === 2, `取消 → onExit 拿到 exit 2:${ex}`);
  check(con.focusPrompt() === false, '命令收尾後殘留的提示不可以再搶焦點');
});

// 8f. 中止後的預填:手打的命令(line)預填回命令列供重跑;args 的那一次不預填——接起來的那一行走 splitArgs 會被切碎。
await scenario('8f', async () => {
  reset();
  const cancelled = [{ type: 'exit', code: 1, message: 'Error: context canceled', reason: 'cancelled' }];
  const args = ['migrate', 'dev1/My Road Trip.m3u8', '--from', 'local', '--to', 'spotify'];
  script = { [args.join(' ')]: { events: cancelled }, 'pl pull 通勤': { events: cancelled } };
  await con.run('', {}, { args });
  const cmd = globalThis.document.getElementById('cmd');
  check(cmd.value === '', `args 的那一次中止後不可以預填命令列:「${cmd.value}」`);
  await con.run('pl pull 通勤');
  check(cmd.value === 'pl pull 通勤', `手打的命令中止後要預填回去:「${cmd.value}」`);
});

// 8g. 真實進度(決策 47):progress 事件才有進度條與 n / total;total 0 只是階段標記;下一個命令開始時條要收起來。
await scenario('8g', async () => {
  reset();
  const [g, release] = gate();
  script = {
    m: {
      first: [{ type: 'progress', stage: 'read', done: 0, total: 0 }, { type: 'progress', stage: 'match', done: 37, total: 120 }],
      gate: g,
    },
  };
  const seen = [];
  const p = con.run('m', { onProgress: (ev) => seen.push(`${ev.stage}:${ev.done}/${ev.total}`) });
  await tick(10);
  const bar = globalThis.document.getElementById('busy-progress');
  const act = globalThis.document.getElementById('busy-act');
  check(seen.join(' ') === 'read:0/0 match:37/120', `onProgress 要逐筆收到:${seen}`);
  check(bar.hidden === false && bar.max === 120 && bar.value === 37, `進度條吃伺服器的數字:hidden=${bar.hidden} ${bar.value}/${bar.max}`);
  check(act.textContent === '比對歌曲 37 / 120', `狀態列說人話:「${act.textContent}」`);
  release();
  await p;
  const q = con.run('x');
  check(bar.hidden === true, '下一個命令開始時,上一次的進度條要收起來(沒有事件就不畫)');
  await q;
});

// 8e. 容器所在的頁面是 hidden(使用者做到一半切去別頁):提示出現時要切回那一頁,不是主控台。
await scenario('8e', async () => {
  reset();
  const host = mk();
  host.closest = () => ({ hidden: true, id: 'page-move' });
  script = { q: { first: [{ type: 'prompt', id: 1, kind: 'confirm', title: '?', default: false }], events: [{ type: 'exit', code: 2, message: '', reason: 'done' }] } };
  await con.run('q', {}, { promptHost: host });
  check(globalThis.location.hash === '#/move', `提示所在的頁面 hidden 時要切回那一頁:${globalThis.location.hash}`);
});

// 8h. 串流在提示開著時斷掉(prompt_closed 不會到):那一則要自己收掉,不可以留在頁面的容器裡看起來還能按(review #68)。
await scenario('8h', async () => {
  reset();
  const host = mk();
  script = { d: { first: [{ type: 'prompt', id: 3, kind: 'confirm', title: '?', default: false }], events: [] } };
  let closedBy = 'none';
  await con.run('d', { onPromptClosed: (ev) => { closedBy = ev.reason; } }, { promptHost: host });
  const block = con.root.children[con.root.children.length - 1];
  const box = block.children.find((c) => c.classList?.contains('prompt'));
  check(!!box && box.classList.contains('is-closed') && box.dataset.reason === 'disconnected', '斷線時開著的提示要收掉並標 disconnected');
  check(!host.children.some((c) => c.classList?.contains('prompt')), '收掉的提示不留在頁面的容器裡');
  check(closedBy === 'none', 'onPromptClosed 只轉交伺服器真的送來的 prompt_closed');
});

// 8i. 精靈的計數(move.js 的 tally;review #66 第三輪 / #68):migrate 與 push 兩種列都吃、以 CID 去重。
await scenario('8i', async () => {
  const { tally } = await import('./pages/move.mjs');
  const H = ['DIR', 'ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  const row = (dir, action, cid, reason, code) => [dir, action, 'spotify', '公路旅行', '0', cid, 'x', 'song-' + cid, 'artist', reason, code];
  const ok = '推到 spotify:x(isrc 95)';
  // 加進既有清單:migrate 列的 ACTION 永遠是 add,推不出去只寫在原因欄;同一批 CID 還會有 push 列
  let t = tally(H, [row('pull', 'add', 'z', '', 'added_on_platform'), row('migrate', 'add', 'a', ok, 'push'), row('migrate', 'add', 'b', ok, 'push'), row('migrate', 'add', 'c', 'spotify 沒有對應,這次不推', 'no_mapping'),
    row('push', 'add', 'a', '', 'push'), row('push', 'add', 'b', '', 'push'), row('push', 'skip', 'c', '沒有對應', 'no_mapping')]);
  check(t.moved === 2 && t.missed.length === 1 && t.missed[0].title === 'song-c', `加進既有清單:2 首搬、1 首沒搬:${JSON.stringify(t)}`);
  // 新建清單、正本已連著來源(follow):一列 migrate 都沒有,全部是 push 列
  t = tally(H, [row('push', 'add', 'a', ok, 'push'), row('push', 'add', 'b', ok, 'push'), row('push', 'add', 'c', ok, 'push'), row('push', 'skip', 'd', '有 mapping 但推不出去', 'unpushable')]);
  check(t.moved === 3 && t.missed.length === 1, `follow:整份都是 push 列,不可以報成 0 首:${JSON.stringify(t)}`);
  // 新建清單、正本原本就有曲目:push 列(既有的)+ migrate 列(新接的)
  t = tally(H, [row('push', 'add', 'a', ok, 'push'), row('migrate', 'add', 'b', ok, 'push'), row('migrate', 'add', 'c', ok, 'push')]);
  check(t.moved === 3 && t.missed.length === 0, `既有的與新接的都算:${JSON.stringify(t)}`);
  // 英文模式:REASON 是英文、沒有「推到 」開頭,判斷只看 REASON_CODE(Q52)
  t = tally(H, [row('migrate', 'add', 'a', 'push to spotify:x (isrc 95)', 'push'), row('migrate', 'add', 'b', 'no match on spotify', 'no_mapping'), row('migrate', 'add', 'c', 'has a mapping but cannot be pushed', 'unpushable')]);
  check(t.moved === 1 && t.missed.length === 2 && t.missed[0].reason === 'no match on spotify', `只看 REASON_CODE,不看 REASON 的字:${JSON.stringify(t)}`);
  check(tally(null, null).moved === 0, '沒有表 = 0');
});

// 9. 被伺服器拒絕(別的分頁佔著槽):說一句,並回報 refused 讓命令列把那行還給使用者。
await scenario('9', async () => {
  reset();
  script = { r: { status: 409, error: '另一個命令執行中' } };
  const res = await con.run('r');
  check(res?.[2] === 'refused' && notices[notices.length - 1] === '另一個命令執行中', `409 要說原因並回報 refused:${res} ${notices}`);
});

// 10. 取消的原因本身不重印,兩種語系都是(errWebCancelled 跟著語系:zh-TW「已取消」、en「cancelled」;決策 50)。
await scenario('10', async () => {
  for (const message of ['Error: 已取消', 'Error: cancelled', 'Error: context canceled']) {
    reset();
    script = { c: { events: [{ type: 'exit', code: 1, message, reason: 'cancelled' }] } };
    await con.run('c');
    const exits = allByClass(root, 'block__exit');
    const text = exits[exits.length - 1]?.textContent;
    check(text === '· exit 1 · 已取消', `取消時「${message}」只是在重複已取消,不該印出來:「${text}」`);
  }
});

if (failures.length) {
  console.log(failures.join('\n'));
  process.exit(1);
}
console.log('ok');
