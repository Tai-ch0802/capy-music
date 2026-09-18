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
    appendChild(c) { this.children.push(c); c.parentNode = this; return c; },
    append(...cs) { cs.forEach((c) => this.appendChild(c)); },
    replaceChildren(...cs) { this.children = []; this.append(...cs); },
    remove() { if (this.parentNode) this.parentNode.children = this.parentNode.children.filter((x) => x !== this); },
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
// 只認 '.class' 與 'tag':block() / showIdle() 用到的就這些,其餘回 null。
function find(root, sel) {
  const cls = /^\.([\w-]+)$/.exec(sel);
  const tag = /^([a-z]+)$/.exec(sel);
  if (!cls && !tag) return null;
  const hit = (e) => (cls ? e.classList?.contains(cls[1]) : e.tagName === tag[1].toUpperCase());
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
const cancels = [];
let script = {};
const sse = (evs) => evs.map((e) => `data: ${JSON.stringify(e)}\n\n`).join('');
const done = { type: 'exit', code: 0, message: '', reason: 'done' };
const api = {
  async fetch(path, init) {
    if (path === '/api/run') {
      const { line } = JSON.parse(init.body);
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
          c.enqueue(enc.encode(sse([{ type: 'start', job }])));
          if (spec.gate) await spec.gate;
          c.enqueue(enc.encode(sse(spec.events || [done])));
          c.close();
        },
      });
      return new Response(body, { status: 200 });
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
const con = new Console(mk(), api, (t) => notices.push(t));
const failures = [];
const check = (ok, msg) => { if (!ok) failures.push(msg); };
// 一組情境丟例外(例如舊版沒有某個方法)只算那一組失敗,其餘照跑,才看得出哪幾條不成立。
const scenario = async (name, fn) => {
  try { await fn(); } catch (e) { failures.push(`情境 ${name} 丟出例外:${e.message}`); }
};
const reset = () => { calls.length = 0; cancels.length = 0; notices.length = 0; script = {}; pages.hidden = false; globalThis.document.getElementById('cmd').value = ''; };

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

// 9. 被伺服器拒絕(別的分頁佔著槽):說一句,並回報 refused 讓命令列把那行還給使用者。
await scenario('9', async () => {
  reset();
  script = { r: { status: 409, error: '另一個命令執行中' } };
  const res = await con.run('r');
  check(res?.[2] === 'refused' && notices[notices.length - 1] === '另一個命令執行中', `409 要說原因並回報 refused:${res} ${notices}`);
});

if (failures.length) {
  console.log(failures.join('\n'));
  process.exit(1);
}
console.log('ok');
