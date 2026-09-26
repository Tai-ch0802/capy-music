// webui_console.mjs:在 node 裡跑真的 console.js(TestWebConsoleBehaviour 把它與 console.js / table.js 複製到暫存目錄)。
// 字串契約證明不了時序:這裡用最小的 DOM 替身與假的 /api/run,釘住執行狀態列、中止與「一次一個」的行為。
// 任何一條不成立就印出來並以 1 結束。
import { readFileSync, readdirSync, writeSync } from 'node:fs';

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
    replaceChildren(...cs) { this._text = ''; this.children = []; this.append(...cs); }, // 同真的 DOM:先前 textContent 設的字也一起換掉
    remove() { if (this.parentNode) this.parentNode.children = this.parentNode.children.filter((x) => x !== this); },
    insertBefore(c, ref) { const i = this.children.indexOf(ref); this.children.splice(i < 0 ? this.children.length : i, 0, c); c.parentNode = this; return c; },
    get firstChild() { return this.children[0] || null; },
    contains(x) { for (let n = x; n; n = n.parentNode) if (n === this) return true; return false; },
    get lastElementChild() { return this.children[this.children.length - 1] || null; },
    get firstElementChild() { return this.children[0] || null; },
    querySelector(sel) { return find(this, sel); },
    querySelectorAll(sel) { return findAll(this, sel); },
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
// 只認 '.class'、'tag'、'[data-x]' 與 '.class[data-k="v"]',以及用空白串起來的後代選擇器('thead tr':search.js 的結果表);
// 其餘回 null。
const camel = (k) => k.replace(/-(\w)/g, (_, c) => c.toUpperCase());
function find(root, sel) {
  const sp = sel.indexOf(' ');
  if (sp > 0) { const x = find(root, sel.slice(0, sp)); return x && find(x, sel.slice(sp + 1)); }
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
// findAll:只認以逗號分開的 '[attr]'(applyStatic 的 data-i18n*、Console 的 [data-run] / [data-pending])與 'tag tag'
// (search.js 的 'tbody tr'),其餘回 []。
// 屬性看 setAttribute 設的,data-* 也看 dataset(btn() 用 dataset.run 標)。
function findAll(root, sel) {
  const desc = /^([a-z]+) ([a-z]+)$/.exec(sel); // 'tbody tr'
  if (desc) {
    const out = [];
    const walk = (e) => { for (const c of e.children || []) { if (c.tagName === desc[2].toUpperCase()) out.push(c); walk(c); } };
    const x = find(root, desc[1]);
    if (x) walk(x);
    return out;
  }
  const names = sel.split(',').map((s) => /^\[([\w-]+)\]$/.exec(s.trim())?.[1]);
  if (names.some((n) => !n)) return [];
  const has = (e, n) => (e.attrs && n in e.attrs) || (n.startsWith('data-') && e.dataset && camel(n.slice(5)) in e.dataset);
  const out = [];
  const walk = (e) => { for (const c of e.children || []) { if (names.some((n) => has(c, n))) out.push(c); walk(c); } };
  walk(root);
  return out;
}
const ids = {};
globalThis.document = {
  body: mk('body'),
  documentElement: { lang: '' },
  activeElement: null,
  getElementById(id) { return (ids[id] ||= mk()); },
  createElement: mk,
  createTextNode: (t) => ({ textContent: t, children: [] }),
  querySelectorAll(sel) { return findAll(this.body, sel); },
  addEventListener() {}, // player.js 的 visibilitychange
};
globalThis.sessionStorage = { getItem() { return null; }, setItem() {} };
let reloads = 0;
globalThis.location = { hash: '', reload() { reloads++; } };
globalThis.CSS = { escape: (s) => s };

// 載入順序的鐵則(i18n.js 開頭):先 import 每個模組、再載目錄。哪個模組在頂層就叫 t(),import 當下就丟例外、這裡就紅。
const { loadI18n, t, applyStatic, languages, currentLang } = await import('./i18n.mjs');
let early = null;
try { t('webui.lang.label'); } catch (e) { early = e; }
const { Console, maskSecrets, timing } = await import('./console.mjs');
// 不真的等 0.8 秒(播放控制亮狀態列)與 5 秒(中止警告過期):計時器照到期先後觸發,縮短不改順序,只省下 CI 的時間。
timing.quiet = 300; // 情境 8 要在它亮之前先看到「沒亮」:留寬一點
timing.disarm = 50;
const { languageMenu } = await import('./lang.mjs');
const { Player } = await import('./player.mjs');
for (const f of readdirSync('./pages')) await import(`./pages/${f}`);

// ── 假的伺服器:/api/run 回 SSE(start → [gate] → events),cancel 端點記下來 ──
const calls = [];
const bodies = [];
const answers = [];
const cancels = [];
let script = {};
const sse = (evs) => evs.map((e) => `data: ${JSON.stringify(e)}\n\n`).join('');
const done = { type: 'exit', code: 0, message: '', reason: 'done' };
let i18nFile = './i18n.json'; // TestWebConsoleBehaviour 寫進來的真目錄(zh-TW;i18n-en.json 是英文)
const api = {
  async fetch(path, init) {
    if (path === '/api/i18n') return new Response(readFileSync(i18nFile), { status: 200, headers: { 'Content-Type': 'application/json' } });
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
const gates = []; // 每一道閘的 release:情境半路丟例外沒放開的,scenario() 收尾時補放(見下)
const gate = () => {
  let release;
  const p = new Promise((r) => { release = r; });
  gates.push(release);
  return [p, release];
};
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));

const failures = [];
const check = (ok, msg) => { if (!ok) failures.push(msg); };
check(early !== null, 'loadI18n() 之前叫 t() 要丟例外:不然模組頂層算字的陷阱不會被抓到');
check(await loadI18n(api) === true && currentLang() === 'zh-TW' && globalThis.document.documentElement.lang === 'zh-TW', `真目錄要載得進來(zh-TW):${currentLang()}`);
const notices = [];
const root = mk();
const con = new Console(root, api, (t) => notices.push(t));
const allByClass = (e, cls, out = []) => { for (const c of e.children || []) { if (c.classList?.contains(cls)) out.push(c); allByClass(c, cls, out); } return out; };
// 一組情境丟例外(例如舊版沒有某個方法)只算那一組失敗,其餘照跑,才看得出哪幾條不成立。
// 每組開始與結束都印一行(結束時連同這組新增的失敗):卡住被 Go 那邊的逾時砍掉時,輸出停在哪一組就是卡在哪一組。
// 用 writeSync:行程被砍掉之前寫的每一行都要已經進了管線,不留在 node 的緩衝裡。
const say = (s) => writeSync(1, s + '\n');
let printed = 0;
const flush = () => { if (failures.length > printed) say(failures.slice(printed).join('\n')); printed = failures.length; };
// 情境結束時還有命令在跑 = 半路丟例外、閘沒放開:串流永遠不收尾,之後每個 run() 都被擋成 busy,
// 等 idle() 的情境永遠等不到、busyOn() 的每秒計時又撐著 node 不結束——一路拖到 Go 那邊的逾時,而且看不出是哪一組。
// 所以在這裡記一條失敗、補放每一道閘,讓那個命令收尾,後面的情境照常跑。
const scenario = async (name, fn) => {
  flush();
  const t0 = Date.now();
  say(`── 情境 ${name} 開始`);
  try { await fn(); } catch (e) { failures.push(`情境 ${name} 丟出例外:${e.message}`); }
  if (con.running) {
    failures.push(`情境 ${name} 結束時還有命令在跑(閘沒放開)`);
    for (const r of gates) r();
    await tick(50);
  }
  gates.length = 0;
  say(`── 情境 ${name} 結束(${Date.now() - t0} ms,${failures.length - printed} 條不成立)`);
  flush();
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
  check(globalThis.document.body.dataset.slot === '' && !('busy' in globalThis.document.body.dataset), '佔槽當下就標 data-slot,看得到的 data-busy 要等 timing.quiet');
  await tick(timing.quiet * 2);
  check(bar.hidden === false && 'busy' in globalThis.document.body.dataset, '卡住超過 timing.quiet 要亮狀態列');
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
  await tick(timing.disarm * 2);
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

// 8j. 搬家精靈的字跟著語系(決策 50):英文目錄下整頁(開場、三步、提示旁的白話、收尾、狀態列的 label)沒有中文、沒有漏送的 key;
//     白話裡的按鈕名稱跟確認鈕查同一個 key(changeset.confirm.*,webSharedKeys 送到頁面);「逐筆裁決」只認提示的 key、不看標題;
//     中止只看 exit 的 reason。命令走假的 con(記下 label、照劇本叫 hooks):這裡驗的是 move.js 自己的字,不是 Console 的。
//     平台只用 apple / spotify:local 的顯示名稱在 common.js,不歸這一頁。
await scenario('8j', async () => {
  const { initMove } = await import('./pages/move.mjs');
  globalThis.document.createElementNS ||= (_, tag) => mk(tag); // 水豚是 SVG
  const CJK = /[　-〿㐀-鿿＀-￯]/;
  const texts = (n, out = []) => { for (const c of n.children || []) { out.push(c._text || '', ...Object.values(c.attrs || {}), c.placeholder || ''); texts(c, out); } return out; };
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const button = (r, text) => { let b = null; walk(r, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const radios = (r, name) => { const out = []; walk(r, (c) => { if (c.tagName === 'INPUT' && c.type === 'radio' && c.name === name) out.push(c); }); return out; };
  const lists = (rows) => (h) => { h.onTable(['ID', 'NAME', 'TRACKS', 'OWNER'], rows); h.onExit(0, '', 'done'); };
  let mig = null;
  const plan = {
    'auth status --json': (h) => { h.onStdout(JSON.stringify({ spotify: { state: 'missing', client_id: 'missing' }, google: { state: 'ok', client: 'builtin' }, apple: { state: 'ok', developer_token: 'ok', user_token: 'ok' } })); h.onExit(0, '', 'done'); },
    'pl list --provider apple': lists([['q1', 'Road trip', '12', 'me']]),
    'pl list --provider spotify': lists([['p1', 'road trip', '3', 'me']]),
    'migrate q1 --from apple --to spotify:p1': (h) => { mig = h; },
  };
  const labels = [];
  const fcon = { idle: (fn) => fn(), run(_line, hooks, o) { labels.push(o.label); plan[o.args.join(' ')]?.(hooks); return Promise.resolve(); } };
  const seen = [];
  const look = (r) => seen.push(...texts(r));
  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const r = mk();
    initMove(r, api, fcon, () => {}, { list: ['apple', 'spotify'] });
    look(r);
    check(r.textContent.includes('Move your playlists over'), `英文的開場:${r.textContent.slice(0, 120)}`);
    button(r, t('webui.move.connect', { provider: 'Spotify' })).click(); // 按鈕是祈使句,執行狀態列的 label 是進行式(兩個 key)
    button(r, t('webui.move.next.playlist')).click();
    radios(r, 'wiz-src')[0].l.change();
    look(r);
    check(r.textContent.includes('Spotify already has a playlist called "road trip"'), '同名清單的說明要是英文、帶清單名稱');
    button(r, t('webui.move.next.confirm')).click();
    look(r);
    button(r, t('webui.move.start')).click();
    check(r.textContent.includes('To stop, click "Stop" at the bottom.'), `執行中的說明指名底部的中止鈕(同一個 key):${r.textContent.slice(-200)}`);
    const titled = mk();
    mig.onPrompt({ kind: 'confirm', title: 'Review now, one by one?' }, titled); // 沒有 key、還沒有預覽:不補話
    const review = mk();
    mig.onPrompt({ kind: 'confirm', key: 'migrate.confirm.review', title: 'x' }, review);
    const help = review.children[0]?.textContent || '';
    check(titled.children.length === 0, '「逐筆裁決」只認提示的 key,不比對標題');
    check(t('changeset.confirm.apply') === 'Apply' && help.includes('"Apply"') && help.includes('"Cancel"') && !help.includes('{'),
      `白話裡的按鈕名稱跟確認鈕同一個 key(/api/i18n 要送 changeset.confirm.*):${help}`);
    const H = ['DIR', 'ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
    mig.onTable(H, [['migrate', 'add', 'spotify', 'road trip', '0', 'a', 'x', 'song-a', 'artist', 'push to spotify:x', 'push'],
      ['migrate', 'add', 'spotify', 'road trip', '1', 'b', '', 'song-b', 'artist', 'no match on spotify', 'no_mapping']]);
    const final = mk();
    mig.onPrompt({ kind: 'confirm', key: 'migrate.confirm.add', title: 'x' }, final);
    look(r); seen.push(...texts(titled), ...texts(review), ...texts(final));
    check(r.textContent.includes('Will move 1 song; 1 song can\'t be moved this time'), `預覽的計數要是英文、單數:${r.textContent}`);
    check((final.children[0]?.textContent || '').startsWith('This is the last confirmation'), '最後確認的白話');
    mig.onExit(1, '', 'cancelled');
    look(r);
    check(r.textContent.includes('Stopped. If writing had already started'), `中止只看 reason(訊息是空的):${r.textContent.slice(-300)}`);
    button(r, t('webui.move.retry')).click();
    mig.onPromptClosed({ id: 1, reason: 'timeout' });
    mig.onExit(1, '', 'timeout');
    look(r);
    check(r.textContent.includes("You didn't answer in time, so the move was cancelled. Nothing was written."), `逾時的收尾:${r.textContent.slice(-300)}`);
    button(r, t('webui.move.retry')).click();
    mig.onTable(H, [['migrate', 'add', 'spotify', 'road trip', '0', 'a', 'x', 'song-a', 'artist', 'push to spotify:x', 'push'],
      ['migrate', 'add', 'spotify', 'road trip', '1', 'c', 'y', 'song-c', 'artist', 'push to spotify:y', 'push']]);
    mig.onExit(0, '', 'done');
    look(r);
    check(r.textContent.includes('Done: 2 songs are now in "road trip" on Spotify.'), `完成要是英文、複數:${r.textContent.slice(-300)}`);
    const moving = 'Moving "Road trip" to Spotify';
    check(JSON.stringify(labels) === JSON.stringify(['Checking account connections', 'Connecting Spotify', 'Reading playlists on Apple Music', 'Reading playlists on Spotify', moving, moving, moving]),
      `執行狀態列的 label 在英文一律是進行式:${JSON.stringify(labels)}`);
    const bad = [...seen, ...labels].filter((s) => CJK.test(s) || s.includes('webui.'));
    check(bad.length === 0, `英文目錄下搬家頁不該有中文或沒送到的 key:${JSON.stringify([...new Set(bad)])}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
  // zh-TW:白話拼上共用的按鈕名稱之後,跟搬進目錄之前一個位元組都不差。
  const zh = t('webui.move.help.review', { apply: t('changeset.confirm.apply'), cancel: t('changeset.confirm.cancel') });
  check(zh === '有幾首歌在目的地找不到完全一樣的。按「套用」可以一首一首挑;按「取消」就先搬找得到的,其餘之後可以再處理。這一步不會寫入任何東西。', `zh-TW 的白話:${zh}`);
  const zhNote = t('webui.move.running_note', { button: t('webui.console.stop') });
  check(zhNote === '清單越長越久。想停下來,按底部的「中止」。', `zh-TW 執行中的說明:${zhNote}`);
});

// 8k. 清單 / 搜尋 / 同步三頁(i18n T3):zh-TW 畫出來的每一句、執行狀態列的每個 label,都跟搬進語系目錄之前一字不差;
//     換成英文目錄後頁面與 label 沒有一個中文字。「全部平台」的值是空字串(不拿顯示文字當值),送出的命令不帶 --provider。
await scenario('8k', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const { initSearch } = await import('./pages/search.mjs');
  const { initSync } = await import('./pages/sync.mjs');
  const providers = { list: ['spotify', 'local'], current: 'spotify' };
  const exported = JSON.stringify({ 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: {} }, items: [{ cid: 'c1' }, { cid: 'c2' }] }, 'tracks.json': { tracks: { c1: { title: 'T1' } } } });
  const stdout = (text) => ({ events: [{ type: 'stdout', text }, done] });
  const fail = { events: [{ type: 'exit', code: 1, message: '', reason: 'done' }] };
  const texts = (e, acc) => { for (const s of [e._text, e.placeholder]) if (s) acc.add(s); for (const c of e.children || []) texts(c, acc); return acc; };
  const paint = async () => {
    const pending = [];
    const labels = [];
    const seen = new Set();
    const roots = [];
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label); const p = Console.prototype.run.call(con, line, hooks, opts); pending.push(p); return p; };
    const settle = async () => { while (pending.length) await pending.shift(); for (const r of roots) texts(r, seen); };
    const page = (init) => { const r = mk(); roots.push(r); init(r, api, con, () => {}, providers); return r; };
    try {
      reset();
      script = { export: stdout(exported) };
      const pl = page(initPlaylists);
      await settle();
      pl.children[2].querySelector('.btn--ghost').click(); // 右欄的「看 Spotify 上的內容」
      await settle();
      pl.children[4].querySelector('.btn').click(); // 「列出來」
      await settle();
      for (const s of [fail, stdout('not json')]) { script = { export: s }; page(initPlaylists); await settle(); }

      script = { 'search x --provider spotify --limit 10': { events: [{ type: 'table', header: ['ID', 'TITLE'], rows: [] }, done] }, 'search y --provider spotify --limit 10': fail,
        'search z --provider spotify --limit 10': { events: [{ type: 'table', header: ['ID', 'TITLE'], rows: [['sp1', 'Song One']] }, done] } };
      const se = page(initSearch);
      await settle();
      for (const v of ['x', 'y', 'z']) { se.querySelector('input').value = v; se.querySelector('.btn--primary').click(); await settle(); }
      // 有結果的那一次真的畫出結果表(resultTable 裡的列變數若再叫 t,會遮住 i18n 的 t() 而丟例外),列尾的「播放」按得下去。
      const tw = se.querySelector('.tbl-wrap');
      const rowsDrawn = tw ? tw.querySelectorAll('tbody tr').map((tr) => tr.children.map((td) => td.textContent)) : null;
      tw?.querySelector('.btn--ghost')?.click();
      await settle();

      const table = { type: 'table', header: ['DIR', 'ACTION', 'CID', 'REASON_CODE'], rows: [['push', 'add', 'c1', 'push'], ['push', 'skip', 'c2', 'no_mapping']] };
      script = { 'pl sync --all --dry-run': { events: [table, { type: 'exit', code: 2, message: 'x', reason: 'done' }] } };
      const sy = page(initSync);
      await settle();
      for (const b of sy.children[2].children) { b.click(); await settle(); } // 從平台更新 / 推到平台 / 雙向同步 / 去除重複
      const all = sy.querySelector('select').children[0];
      return { seen, labels, calls: [...calls], all: [all.value, all.textContent], rowsDrawn };
    } finally {
      delete con.run;
    }
  };
  // 診斷頁的「檢查中…」是 app.css 的 ::after { content: attr(data-running) }:字要在 data-running 裡(CSS 叫不了 t())。
  const { initDoctor } = await import('./pages/doctor.mjs');
  const checking = async () => {
    reset();
    const dr = mk();
    initDoctor(dr, api, con, () => {}, providers);
    const [g, release] = gate();
    script = { doctor: { gate: g } };
    dr.querySelector('.btn--primary').click();
    await tick(5);
    const s = dr.querySelector('pre')?.dataset.running;
    release();
    await new Promise((r) => con.idle(r));
    return s;
  };
  const want = (r, lang, list) => { for (const s of list) check(r.seen.has(s), `${lang}:頁面上少了「${s}」:${JSON.stringify([...r.seen])}`); };
  const cmds = JSON.stringify(['export', 'pl show "road trip" --provider spotify', 'pl list --provider spotify', 'export', 'export', 'search x --provider spotify --limit 10', 'search y --provider spotify --limit 10',
    'search z --provider spotify --limit 10', 'play --id sp1 --provider spotify',
    'pl pull --all --dry-run', 'pl push --all --dry-run', 'pl sync --all --dry-run', 'pl dedup --dry-run']);

  const zh = await paint();
  want(zh, 'zh-TW', ['我的清單', 'capy 替你保管的清單(正本在你的 Google Drive),以及各平台上現有的清單。', '重新整理', '平台上現有的清單', '平台', '本機曲庫', '列出來',
    'road trip(2 首)', '(本機沒有這首的資料)', '看 Spotify 上的內容', '還沒有清單。到「搬家」搬一個過來,或到「同步」把平台上的清單連起來。',
    '搜尋', '在平台上找歌。Spotify 找到了可以直接播;Apple Music(macOS)只直接播你資料庫裡有的歌,其他的會在 Music.app 打開並標出那一首。', '五月天 派對動物', '關鍵字', '結果數', '輸入歌名或歌手,按「搜尋」。',
    '在 Spotify 找不到「x」。換個關鍵字,或換一個平台試試。', '沒有找到。換個關鍵字試試。',
    '同步', '讓 capy 保管的清單跟平台上的保持一致。會先列出要改什麼,你確認了才寫入。', '清單名稱(留空 = 全部)', '全部平台', '清單', '只看變更,先不寫入',
    '從平台更新', '推到平台', '雙向同步', '去除重複',
    '「去除重複」整理的是 capy 保管的那一份(不分平台);上面選的平台只決定這次去檢查哪個平台上的重複——沒選到的平台這次不會檢查。清單留空時會讓你挑一個。要把清單搬到另一個平台,請到「搬家」。',
    '選好清單與平台,按「雙向同步」。預設只列出變更,不會寫入。', '兩邊已經一致,沒有要改的東西。', '1 筆變更(skip 不算變更)',
    '以上是會改的東西,還沒有寫入。取消勾選「只看變更,先不寫入」再按一次,就會照這張表問你、確認後寫入。']);
  check([...zh.seen].some((s) => s.startsWith('export 的輸出不是 JSON:') && s.length > 'export 的輸出不是 JSON:'.length), 'zh-TW:export 不是 JSON 要說出來並附上原因');
  check(JSON.stringify(zh.labels) === JSON.stringify(['讀取你的清單', '讀取 Spotify 上的「road trip」', '讀取 Spotify 上的清單', '讀取你的清單', '讀取你的清單',
    '在 Spotify 找「x」', '在 Spotify 找「y」', '在 Spotify 找「z」', '播放「Song One」', '把平台上的變更拉回來', '把 capy 保管的清單推到平台', '雙向同步', '去除重複的歌']), `zh-TW 的 label:${JSON.stringify(zh.labels)}`);
  check(JSON.stringify(zh.calls) === cmds && JSON.stringify(zh.all) === JSON.stringify(['', '全部平台']), `送出的命令與「全部平台」的值:${JSON.stringify(zh.calls)} ${JSON.stringify(zh.all)}`);
  check(JSON.stringify(zh.rowsDrawn) === JSON.stringify([['sp1', 'Song One', '播放']]), `zh-TW 的搜尋結果表:${JSON.stringify(zh.rowsDrawn)}`);
  const zhChecking = await checking();
  check(zhChecking === '檢查中…', `zh-TW 診斷頁的 data-running:「${zhChecking}」`);

  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const en = await paint();
    const CJK = /[　-〿㐀-鿿＀-￯]/;
    for (const s of [...en.seen, ...en.labels]) check(!CJK.test(s), `en:頁面或 label 還有中文:「${s}」`);
    for (const s of en.labels) check(/^[A-Z][a-z]*ing /.test(s), `en:執行狀態列的 label 要是進行式:「${s}」`);
    want(en, 'en', ['My playlists', 'Local library', 'road trip (2 songs)', 'Enter a song or artist, then press "Search".', '1 change (skip rows don\'t count)', 'All platforms']);
    check([...en.seen].some((s) => s.includes('Uncheck "Preview only, don\'t write yet"')), 'en:只看變更的收尾要指名那個勾選框');
    check(en.labels.includes('Reading "road trip" on Spotify') && en.labels.includes('Playing "Song One"') && JSON.stringify(en.calls) === cmds && en.all[0] === '', `en 的 label 與命令:${JSON.stringify(en.labels)} ${JSON.stringify(en.calls)}`);
    check(JSON.stringify(en.rowsDrawn) === JSON.stringify([['sp1', 'Song One', 'Play']]), `en 的搜尋結果表:${JSON.stringify(en.rowsDrawn)}`);
    const enChecking = await checking();
    check(enChecking === 'Checking…', `en 診斷頁的 data-running:「${enChecking}」`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 8l. 搜尋頁的 Apple 列(決策 52):按鈕叫「在 Music.app 開啟」,送出的命令不變。命令成功卻不是 ▶ 開頭(只打開、沒開始播)時,
//     那句話原樣進底部的 notice——不然它只進看不到的主控台頁;真的播了(▶,播放列會換成 Apple)或失敗(Console.report 會說)都不多說。
await scenario('8l', async () => {
  const { initSearch } = await import('./pages/search.mjs');
  const honest = '已在 Music.app 打開「Radioactivity — Kraftwerk」並標出那一首。';
  const paint = async () => {
    reset();
    const said = [];
    const labels = [];
    const pending = [];
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label); const p = Console.prototype.run.call(con, line, hooks, opts); pending.push(p); return p; };
    const settle = async () => { while (pending.length) await pending.shift(); };
    try {
      script = {
        'search k --provider apple --limit 10': { events: [{ type: 'table', header: ['ID', 'TITLE'], rows: [['700050031', 'Radioactivity'], ['111', 'Lost Stars'], ['222', 'Boom']] }, done] },
        'play --id 700050031 --provider apple': { events: [{ type: 'stdout', text: honest + '\n' }, done] },
        'play --id 111 --provider apple': { events: [{ type: 'stdout', text: '▶ Lost Stars — Adam Levine\n' }, done] },
        'play --id 222 --provider apple': { events: [{ type: 'stdout', text: 'partial\n' }, { type: 'exit', code: 1, message: 'Error: boom', reason: 'done' }] },
      };
      const r = mk();
      initSearch(r, api, con, (s) => said.push(s), { list: ['spotify', 'apple'], current: 'apple' });
      r.querySelector('input').value = 'k';
      r.querySelector('.btn--primary').click();
      await settle();
      const btns = r.querySelector('.tbl-wrap').querySelectorAll('tbody tr').map((tr) => tr.querySelector('.btn--ghost')); // 替身的 querySelectorAll 只認 'tbody tr' 這種
      const after = [];
      for (const b of btns) { b.click(); await settle(); after.push([...said]); }
      return { buttons: btns.map((b) => b.textContent), labels: labels.slice(1), calls: calls.slice(1), after };
    } finally {
      delete con.run;
    }
  };
  const plays = ['play --id 700050031 --provider apple', 'play --id 111 --provider apple', 'play --id 222 --provider apple'];
  const zh = await paint();
  check(zh.buttons.length === 3 && zh.buttons.every((b) => b === '在 Music.app 開啟'), `zh-TW:Apple 列的按鈕:${JSON.stringify(zh.buttons)}`);
  check(JSON.stringify(zh.calls) === JSON.stringify(plays), `送出的命令不變:${JSON.stringify(zh.calls)}`);
  check(zh.labels[0] === '在 Music.app 開啟「Radioactivity」', `zh-TW 的 label:${JSON.stringify(zh.labels)}`);
  check(JSON.stringify(zh.after) === JSON.stringify([[honest], [honest], [honest]]), `只打開的那句要進 notice,▶ 與失敗不多說:${JSON.stringify(zh.after)}`);

  // 收尾時叫醒的自動讀取(別頁第一次打開時排的 con.idle)一開跑就會清掉 notice:那句要等 run() 整個收尾之後才說(review)。
  reset();
  const [g, release] = gate();
  script = {
    'search k --provider apple --limit 10': { events: [{ type: 'table', header: ['ID', 'TITLE'], rows: [['700050031', 'Radioactivity']] }, done] },
    'play --id 700050031 --provider apple': { gate: g, events: [{ type: 'stdout', text: honest + '\n' }, done] },
    'auth status --json': { events: [done] },
  };
  const r2 = mk();
  initSearch(r2, api, con, (s) => notices.push(s), { list: ['apple'], current: 'apple' });
  r2.querySelector('input').value = 'k';
  r2.querySelector('.btn--primary').click();
  await new Promise((res) => con.idle(res));
  r2.querySelector('.tbl-wrap').querySelectorAll('tbody tr')[0].querySelector('.btn--ghost').click();
  await tick(5);
  con.idle(() => con.run('auth status --json'));
  release();
  await tick(20);
  await new Promise((res) => con.idle(res));
  check(calls.includes('auth status --json') && notices.at(-1) === honest, `排隊的自動讀取不可以把那句清掉:${JSON.stringify(notices)} ${JSON.stringify(calls)}`);

  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const en = await paint();
    const CJK = /[　-〿㐀-鿿＀-￯]/;
    check(en.buttons.every((b) => b === 'Open in Music.app'), `en:Apple 列的按鈕:${JSON.stringify(en.buttons)}`);
    check(en.labels[0] === 'Opening "Radioactivity" in Music.app' && en.labels.every((l) => /^[A-Z][a-z]*ing /.test(l) && !CJK.test(l)), `en 的 label:${JSON.stringify(en.labels)}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 9. 被伺服器拒絕(別的分頁佔著槽):說一句,並回報 refused 讓命令列把那行還給使用者。
await scenario('9', async () => {
  reset();
  script = { r: { status: 409, error: '另一個命令執行中' } };
  const res = await con.run('r');
  check(res?.[2] === 'refused' && notices[notices.length - 1] === '另一個命令執行中', `409 要說原因並回報 refused:${res} ${notices}`);
});

// 10. 取消本身伺服器不送訊息(計畫 §2.4 第 5 點):頁面只看 reason,不比對跟著語系變的文字;中止時剛好撞上的別的錯誤照印。
await scenario('10', async () => {
  for (const [message, want] of [['', '· exit 1 · 已取消'], ['Error: boom', '· exit 1 · 已取消 · boom']]) {
    reset();
    script = { c: { events: [{ type: 'exit', code: 1, message, reason: 'cancelled' }] } };
    await con.run('c');
    const exits = allByClass(root, 'block__exit');
    const text = exits[exits.length - 1]?.textContent;
    check(text === want, `取消的 exit 行(訊息「${message}」):「${text}」,要「${want}」`);
  }
});

// 11. 語言選單(決策 50):選項來自 /api/i18n;換語言走一般的命令路徑、只送這四個 args,exit 0 才重新載入;
//     有命令在跑時停用;沒換成就回到目前的語言。
await scenario('11', async () => {
  reset();
  const before = reloads;
  const sel = mk('select');
  sel.setAttribute('data-run', '');
  globalThis.document.body.appendChild(sel);
  languageMenu(sel, con);
  const opts = sel.children.map((o) => `${o.value}=${o.textContent}`).join(' ');
  check(opts === 'en=English zh-TW=繁體中文' && sel.value === 'zh-TW', `選項是 value = 代碼、字 = 語系自己的名稱,預設選目前語系:${opts} / ${sel.value}`);
  const [g, release] = gate();
  script = { x: { gate: g } };
  const p = con.run('x');
  await tick(5);
  check(sel.disabled === true, '有命令在跑時語言選單要停用');
  release();
  await p;
  check(sel.disabled === false, '命令結束後語言選單要恢復');
  bodies.length = 0;
  sel.value = 'en';
  await sel.l.change();
  check(JSON.stringify(bodies) === JSON.stringify([{ args: ['config', 'set', 'language', 'en'] }]), `換語言只送 config set language <代碼>:${JSON.stringify(bodies)}`);
  check(reloads === before + 1, '換成功(exit 0)要重新載入整頁');
  script = { 'config set language en': { events: [{ type: 'exit', code: 1, message: 'Error: x', reason: 'done' }] } };
  sel.value = 'en';
  await sel.l.change();
  check(reloads === before + 1 && sel.value === 'zh-TW', `沒換成不重新載入、選單回到目前的語言:${sel.value}`);
  sel.remove();
});

// 12. t() 與 applyStatic:一趟替換、複數依 Intl.PluralRules、缺 key 回 key;最後換回真目錄(英文 → 中文都載得進來)。
await scenario('12', async () => {
  const fake = (d) => ({ fetch: async () => new Response(JSON.stringify(d), { status: 200 }) });
  await loadI18n(fake({ lang: 'en', supported: [], messages: { a: 'A {x} {y}', p: { one: '{count} track', other: '{count} tracks' }, l: 'L', h: 'H' } }));
  check(t('a', { x: '{y}', y: 'z' }) === 'A {y} z', `值裡的 {y} 不可以再被換:${t('a', { x: '{y}', y: 'z' })}`);
  check(t('a', { x: 1 }) === 'A 1 {y}', '沒給的佔位符原樣留著');
  check(t('p', { count: 1 }) === '1 track' && t('p', { count: 0 }) === '0 tracks' && t('p', { count: 21 }) === '21 tracks', '英文的單複數');
  check(t('nope') === 'nope', '缺 key 回 key 本身');
  const box = mk();
  const span = mk('span'); span.setAttribute('data-i18n', 'l');
  const inp = mk('input'); inp.setAttribute('data-i18n-placeholder', 'h'); inp.setAttribute('data-i18n-aria-label', 'l'); inp.setAttribute('data-i18n-title', 'h');
  box.append(span, inp);
  applyStatic(box);
  check(span.textContent === 'L' && inp.getAttribute('placeholder') === 'H' && inp.getAttribute('aria-label') === 'L' && inp.getAttribute('title') === 'H',
    `applyStatic 填文字與三個屬性:${span.textContent} ${JSON.stringify(inp.attrs)}`);
  await loadI18n(fake({ lang: 'zh-TW', supported: [], messages: { p: { other: '{count} 首' } } }));
  check(t('p', { count: 1 }) === '1 首', '中文只有 other');
  // 目錄讀不到(token 過期 → 401、連不上):applyStatic 不把 key 當字填上去,元素留空;原因由 app.js 的 notice 說。
  const stale = mk('span'); stale.setAttribute('data-i18n', 'webui.rail.move');
  const staleIn = mk('input'); staleIn.setAttribute('data-i18n-placeholder', 'webui.shell.cmd_placeholder');
  const staleBox = mk(); staleBox.append(stale, staleIn);
  for (const bad of [{ fetch: async () => new Response('{"error":"x"}', { status: 401 }) }, { fetch: async () => { throw new Error('down'); } }]) {
    check(await loadI18n(bad) === false, 'loadI18n 讀不到要回 false');
    applyStatic(staleBox);
    check(stale.textContent === '' && staleIn.getAttribute('placeholder') === null, `目錄讀不到時元素留空:「${stale.textContent}」「${staleIn.getAttribute('placeholder')}」`);
  }
  i18nFile = './i18n-en.json';
  await loadI18n(api);
  check(t('webui.lang.label') === 'Language' && globalThis.document.documentElement.lang === 'en', `英文的真目錄:${t('webui.lang.label')}`);
  i18nFile = './i18n.json';
  await loadI18n(api);
  check(t('webui.lang.label') === '語言' && languages().length === 2, `換回中文的真目錄:${t('webui.lang.label')}`);
});

// 13. 語系目錄的字(決策 50):播放列先看中文(跟搬進目錄之前一個位元組都不差),再換英文目錄跑一輪主控台——
//     執行狀態列、被擋的說明、中止、exit 行、提示的預設鈕、被拒絕、別頁失敗的那句,畫出來的沒有一個中日韓字元。
await scenario('13', async () => {
  const cjk = /[　-〿㐀-鿿＀-￯]/;
  const line = mk('span');
  const nowRoot = mk();
  nowRoot.querySelector = (sel) => (sel === '#now-line' ? line : null);
  const player = new Player(nowRoot, api, () => {}, con);
  const d = { provider: 'spotify', playing: true, position_ms: 61000, stale: true, stale_ms: 1000,
    track: { title: 'Song', artists: ['A', 'B'], duration_ms: 200000 }, device: { name: 'Mac', volume_known: true, volume_pct: 40 } };
  player.render(d);
  check(line.textContent === 'Spotify · ▶ Song — A, B · 1:01 / 3:20 · Mac · 🔊 40 · 1 秒前', `播放列(中文):「${line.textContent}」`);
  player.render({ provider: 'apple' });
  check(line.textContent === 'Apple Music:目前沒有播放內容', `播放列沒在播(中文):「${line.textContent}」`);
  // 待套用(exit 2、訊息提到 --yes)的補一句:中文接全形括號、英文值開頭是空白(接在訊息後面),兩邊都釘住。
  const pendingExit = async (msg) => {
    script = { y: { events: [{ type: 'exit', code: 2, message: 'Error: ' + msg, reason: 'done' }] } };
    await con.run('y');
    return allByClass(root, 'block__exit').at(-1)?.textContent;
  };
  reset();
  const zhPending = await pendingExit('3 筆變更待套用:加 --yes 套用,或在終端機執行以確認');
  check(zhPending === '· exit 2 · 3 筆變更待套用:加 --yes 套用,或在終端機執行以確認(未套用:加 --yes 重跑)', `待套用的 exit 行(中文):「${zhPending}」`);

  i18nFile = './i18n-en.json';
  await loadI18n(api);
  try {
    player.render(d);
    check(line.textContent === 'Spotify · ▶ Song — A, B · 1:01 / 3:20 · Mac · 🔊 40 · 1 second ago', `播放列(英文、單數):「${line.textContent}」`);
    player.render({ ...d, stale_ms: 5000 });
    check(line.textContent.endsWith(' · 5 seconds ago'), `播放列(英文、複數):「${line.textContent}」`);
    player.render({ provider: 'apple', error: 'boom' });
    check(line.textContent === 'Apple Music: boom', `播放列的錯誤(英文):「${line.textContent}」`);
    player.render({ provider: 'apple' });
    check(line.textContent === 'Apple Music: nothing playing', `播放列沒在播(英文):「${line.textContent}」`);
    player.disconnected();
    const lineGone = line.textContent;

    reset();
    const before = root.children.length;
    const stopBtn = globalThis.document.getElementById('cancel');
    const act = globalThis.document.getElementById('busy-act');
    const sr = globalThis.document.getElementById('busy-sr');
    const [g, release] = gate();
    script = { sync: { first: [{ type: 'progress', stage: 'match', done: 37, total: 120 }], gate: g, events: [{ type: 'exit', code: 1, message: '', reason: 'cancelled' }] } };
    const p = con.run('sync');
    await tick(5);
    check(stopBtn.textContent === 'Stop' && sr.textContent === 'Running: sync' && act.textContent === 'Matching songs 37 / 120',
      `執行狀態列(英文):「${stopBtn.textContent}」「${sr.textContent}」「${act.textContent}」`);
    const busy = await con.run('x');
    check(busy[1] === 'another command is running' && notices[notices.length - 1] === 'sync is running; wait for it to finish or press Stop',
      `被擋的那一次(英文):${busy} / ${notices[notices.length - 1]}`);
    await con.stop();
    check(stopBtn.textContent === 'Stopping…' && act.textContent === 'Stop sent; waiting for the command to wrap up', `中止中(英文):「${stopBtn.textContent}」「${act.textContent}」`);
    release();
    const ex = await p;
    const exits = () => allByClass(root, 'block__exit');
    check(ex[1] === 'Stopped' && sr.textContent === 'Stopped: sync', `中止交給頁面的訊息與播報(英文):${ex} / ${sr.textContent}`);
    check(exits().at(-1)?.textContent === '· exit 1 · cancelled', `取消的 exit 行(英文):「${exits().at(-1)?.textContent}」`);

    // 提示的預設鈕與收掉的標記;授權連結;逾時的 exit 行。
    script = {
      q: {
        first: [{ type: 'prompt', id: 1, kind: 'confirm', title: 'Go?' }],
        events: [{ type: 'prompt_closed', id: 1, reason: 'answered' }, { type: 'open_url', url: 'https://example.test/a' },
          { type: 'prompt', id: 2, kind: 'form', title: 'Secret', fields: [{ name: 's', label: 'Secret', secret: true, filled: true }] },
          { type: 'prompt_closed', id: 2, reason: 'timeout' }, { type: 'exit', code: 1, message: '', reason: 'timeout' }],
      },
    };
    await con.run('q');
    const block = root.children[root.children.length - 1];
    const [confirm, form] = allByClass(block, 'prompt');
    const labels = (box) => find(box, '.prompt__row').children.map((b) => b.textContent).join('|');
    check(labels(confirm) === 'OK|Cancel|✕ Dismiss' && labels(form) === 'Submit|✕ Dismiss', `提示的預設鈕(英文):${labels(confirm)} / ${labels(form)}`);
    check(find(form, '.prompt__input')?.placeholder === 'Already set; leave blank to keep it', `已填的 secret 欄(英文):${find(form, '.prompt__input')?.placeholder}`);
    check(find(confirm, '.prompt__closed')?.textContent === 'Answered' && find(form, '.prompt__closed')?.textContent === 'Timed out waiting for an answer; the command was cancelled',
      '收掉的提示要標明怎麼收的(英文)');
    check(find(block, '.block__link')?.textContent === 'Open the authorization page in your browser: https://example.test/a', `授權連結(英文):${find(block, '.block__link')?.textContent}`);
    check(exits().at(-1)?.textContent === '✗ exit 1 · timed out waiting for an answer', `逾時的 exit 行(英文):「${exits().at(-1)?.textContent}」`);

    script = { r: { status: 409, error: 'another command is running; wait for it to finish or press Stop' } };
    await con.run('r');
    check(exits().at(-1)?.textContent === '· Not run (another command is running) · another command is running; wait for it to finish or press Stop',
      `被伺服器拒絕(英文):「${exits().at(-1)?.textContent}」`);
    const enPending = await pendingExit('3 changes pending: pass --yes to apply them, or run this in a terminal to confirm');
    check(enPending === '· exit 2 · 3 changes pending: pass --yes to apply them, or run this in a terminal to confirm (not applied: rerun with --yes)',
      `待套用的 exit 行(英文):「${enPending}」`);
    pages.hidden = true;
    script = { f: { events: [{ type: 'exit', code: 1, message: 'Error: boom', reason: 'done' }] } };
    await con.run('f');
    check(notices[notices.length - 1] === '✗ f: boom (full output on the Console page)', `別頁發出的命令失敗(英文):${notices[notices.length - 1]}`);

    const drawn = [lineGone, ...root.children.slice(before).map((b) => b.textContent), ...notices, stopBtn.textContent, act.textContent, sr.textContent,
      stopBtn.title, globalThis.document.getElementById('busy-cmd').title].join('\n');
    check(!cjk.test(drawn), `英文目錄畫出來的字不可以有中日韓字元:\n${drawn.split('\n').filter((s) => cjk.test(s)).join('\n')}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 14. 帳號 / 診斷 / ISRC 三頁(i18n T3):帳號狀態只看 auth status --json 的 state,不比對給人看的字;
//     zh-TW 畫出來跟搬進目錄前一個字都不差;英文的真目錄畫出來沒有任何中文字。
// 13b. 播放列跟著平台走(決策 51):控制鈕送的是面板上看到的那個平台(以前不帶 --provider,一律打到 default_provider);
//      還沒有任何狀態就不帶。曲名連回平台(只收 https);Spotify 播 podcast / 廣告(沒有 track)照樣說在播。
await scenario('13b', async () => {
  const line = mk('span');
  const nowRoot = mk();
  nowRoot.querySelector = (sel) => (sel === '#now-line' ? line : null);
  const player = new Player(nowRoot, api, () => {}, con);
  reset();
  await player.control('pause');
  check(calls.at(-1) === 'pause', `還沒有狀態:不帶 --provider:「${calls.at(-1)}」`);
  const song = { title: 'Song', artists: ['A'], duration_ms: 200000 };
  player.render({ provider: 'apple', playing: true, position_ms: 61000, track: song, device: { name: 'Music.app', volume_known: false } });
  await player.control('pause');
  check(calls.at(-1) === 'pause --provider apple', `控制面板上的平台:「${calls.at(-1)}」`);
  player.seekBy(10);
  await tick(20);
  check(calls.at(-1) === 'seek 71 --provider apple', `← → 也送給面板上的平台:「${calls.at(-1)}」`);

  player.render({ provider: 'spotify', playing: true, position_ms: 0, track: { ...song, url: 'https://open.spotify.com/track/x' } });
  const a = line.children[1];
  check(a?.tagName === 'A' && a.href === 'https://open.spotify.com/track/x' && a.target === '_blank' && a.rel === 'noopener noreferrer' && a.textContent === 'Song',
    `曲名要連回 Spotify:${JSON.stringify({ tag: a?.tagName, href: a?.href, rel: a?.rel })}`);
  check(line.textContent === 'Spotify · ▶ Song — A · 0:00 / 3:20', `有連結也是同一行字:「${line.textContent}」`);
  // 每 2.5 秒一輪:同一個連結原地改字,不重建(重建會把停在連結上的鍵盤焦點丟掉)
  player.render({ provider: 'spotify', playing: true, position_ms: 2500, track: { ...song, url: 'https://open.spotify.com/track/x' } });
  check(line.children[1] === a && line.textContent === 'Spotify · ▶ Song — A · 0:02 / 3:20', `下一輪要沿用同一個連結節點:「${line.textContent}」`);
  player.render({ provider: 'spotify', playing: true, position_ms: 0, track: { ...song, url: 'javascript:alert(1)' } });
  check(line.children[1]?.tagName !== 'A', '不是 https 的網址不可以變成連結');
  player.render({ provider: 'spotify', playing: true, track: null, device: { name: 'iPhone' } });
  check(line.textContent === 'Spotify · ▶', `podcast / 廣告在播:「${line.textContent}」`);
  player.render({ provider: 'spotify', playing: true, track: null, stale: true, stale_ms: 8000 });
  check(line.textContent === 'Spotify · ▶ · 8 秒前', `podcast 那行也要說多久沒更新:「${line.textContent}」`);
  player.render({ provider: 'spotify', playing: true, position_ms: 0, track: { ...song, url: 'https://open.spotify.com/track/x' } });
  check(line.children[1]?.tagName === 'A' && line.textContent === 'Spotify · ▶ Song — A · 0:00 / 3:20', `純文字行之後要把曲目那行接回來:「${line.textContent}」`);
});

await scenario('14', async () => {
  const { parseStatus, stateOf, initAccount } = await import('./pages/account.mjs');
  const { initDoctor } = await import('./pages/doctor.mjs');
  const { initISRC } = await import('./pages/isrc.mjs');
  const status = JSON.stringify({
    spotify: { state: 'ok', client_id: 'set' },
    google: { state: 'keychain_error', client: 'builtin' },
    apple: { state: 'expired', developer_token: 'expired', developer_token_expiry: '2026-01-01T00:00:00Z', user_token: 'ok' },
  });
  const isrc = {
    isrc: 'TWK231680790',
    parts: { country: 'TW', geographic: true, registrant: 'K23', year: '16', year_full: 2016, designation: '80790' },
    providers: { spotify: { tracks: [{ id: 'sp1', title: 'x', artists: ['a'], url: 'https://open.spotify.com/track/sp1' }] }, apple: { tracks: [] } },
    canonical: { cid: 'c1', title: 'x', artists: ['a'], duration_ms: 200000, isrc: ['TWK231680790'], mappings: { spotify: { id: '', confidence: 95, source: 'isrc', pinned: true } }, playlists: [{ name: 'p', pos: 0, links: {} }] },
  };
  const draw = async () => {
    const runs = [];
    const labels = [];
    let running = null;
    const acct = mk();
    const doc = mk();
    const fake = {
      run(cmd, hooks, o) {
        runs.push(cmd);
        labels.push(o?.label);
        if (cmd === 'doctor') running = find(doc, '.doctor__out').dataset.running;
        hooks.onStdout?.(cmd === 'auth status --json' ? status : '');
        hooks.onExit?.(0, '');
      },
      idle(fn) { fn(); },
    };
    initAccount(acct, null, fake, () => {});
    initDoctor(doc, null, fake, () => {}, { list: ['spotify', 'apple'] });
    find(doc, '.btn').click();
    const parts = { '#isrc-out': mk(), '#isrc-input': mk('input'), '#isrc-status': mk(), '#isrc-go': mk('button') };
    const iroot = mk();
    iroot.querySelector = (s) => parts[s];
    // ISRC 頁的標題、說明與按鈕是 index.html 裡的 data-i18n(開機時 app.js 的 applyStatic() 填):照樣建出來、照樣填。
    for (const k of ['webui.isrc.title', 'webui.isrc.lead', 'webui.isrc.go']) { const e = mk(); e.setAttribute('data-i18n', k); iroot.appendChild(e); }
    applyStatic(iroot);
    globalThis.document.createElement = (tag) => Object.assign(mk(tag), { style: {} }); // 四段拆解設 style.minWidth
    try {
      initISRC(iroot, { fetch: async () => new Response(JSON.stringify(isrc), { status: 200 }) }, 'TWK231680790');
      await tick(20);
    } finally {
      globalThis.document.createElement = mk;
    }
    return { runs, labels, running, acct: acct.textContent, rows: allByClass(acct, 'acct__state').map((e) => e.textContent), doctor: doc.textContent, isrc: iroot.children.map((e) => e.textContent).join('\n') + '\n' + parts['#isrc-out'].textContent };
  };

  const zh = await draw();
  check(zh.runs.join('|') === 'auth status --json|doctor', `帳號頁跑 --json、診斷頁的預設平台不帶 --provider:${zh.runs}`);
  check(zh.rows.join('|') === '✓ 已登入|⚠ 已過期|⚠ 讀取 keychain 失敗', `三列的狀態看 state:${zh.rows}`);
  check(stateOf('apple', parseStatus('spotify:\n  refresh token: keychain 存在\n').apple).text === '未登入', '讀不懂(不是 JSON)就是未登入,不回頭解析文字');
  check(zh.acct.includes('Google Drive(保管你的清單)') && zh.acct.includes('重新連接') && zh.acct.includes('client ID 已設定') && !zh.acct.includes('client_id'), `帳號頁的 zh-TW:${zh.acct}`);
  check(zh.running === '檢查中…' && zh.doctor.includes('還沒檢查過。按「開始檢查」。'), `診斷頁的 zh-TW:${zh.running} ${zh.doctor}`);
  for (const s of ['ISRC 查詢', 'ISRC 是每首歌的國際編號。輸入一個,看它在三個平台上分別是哪一首。', '查詢', '台灣', '年份(推測)', '在 spotify 開啟', '這個平台沒有符合的曲目', '(不可得) · 95 分 · isrc · 已釘選', '含這首的清單(1)']) {
    check(zh.isrc.includes(s), `ISRC 頁的 zh-TW 少了「${s}」:${zh.isrc}`);
  }

  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const en = await draw();
    const all = [en.acct, en.doctor, en.running, en.isrc].join('\n');
    check(!/[\p{Script=Han}　-〿＀-￯]/u.test(all), `英文畫出來不可以有中文字:${all}`);
    check(en.rows.join('|') === "✓ Logged in|⚠ Expired|⚠ Couldn't read the keychain", `英文的三列狀態:${en.rows}`);
    check(JSON.stringify(en.labels) === JSON.stringify(['Checking account connections', 'Checking config, logins and connections']) && /^Switching /.test(t('webui.lang.switching')),
      `英文的 label 是進行式:${JSON.stringify(en.labels)} / ${t('webui.lang.switching')}`);
    check(en.running === 'Checking…' && en.isrc.includes('ISRC lookup') && en.isrc.includes('Look up') && en.isrc.includes('Taiwan') && en.isrc.includes('Open in spotify') && en.isrc.includes('(unavailable) · confidence 95 · isrc · pinned'),
      `診斷與 ISRC 頁的英文:${en.running} ${en.isrc}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 14b. 帳號頁的細節欄(i18n T3 自審):auth status --json 的事實畫成給人看、跟著語系的句子,沒有的事實不畫(整欄都沒有就是 —);
//      JSON 的欄名與列舉值(client_id: / missing / developer_token …)不上畫面。到期時間用這台電腦的時區
//      (預期值用同一台機器的 Date 算,不靠 TZ:CI 的 Windows 不一定吃 TZ 環境變數)。三列的順序是 Spotify、Apple Music、Google Drive。
await scenario('14b', async () => {
  const { initAccount } = await import('./pages/account.mjs');
  const fixtures = [
    { spotify: { state: 'ok', client_id: 'set' },
      google: { state: 'ok', client: 'builtin', access_token_expiry: '2026-09-24T05:00:00Z', email: 'me@example.com', device_id: 'dev1' },
      apple: { state: 'ok', developer_token: 'ok', developer_token_expiry: '2026-10-01T04:30:00Z', user_token: 'ok', storefront: 'tw' } },
    { spotify: { state: 'keychain_error', client_id: 'malformed' }, google: { state: 'missing', client: 'none' },
      apple: { state: 'expired', developer_token: 'expired', developer_token_expiry: '2026-01-01T00:00:00Z', user_token: 'missing' } },
    { spotify: { state: 'missing', client_id: 'missing' }, google: { state: 'keychain_error', client: 'config' },
      apple: { state: 'keychain_error', developer_token: 'keychain_error', user_token: 'keychain_error' } },
  ];
  const p2 = (n) => String(n).padStart(2, '0');
  const local = (iso) => { const d = new Date(iso); return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`; };
  const [valid, expired] = [local('2026-10-01T04:30:00Z'), local('2026-01-01T00:00:00Z')];
  const want = {
    'zh-TW': [['client ID 已設定', `developer token 有效至 ${valid} · 商店地區:台灣`, 'me@example.com'],
      ['client ID 格式不對', `developer token 已於 ${expired} 過期`, '—'], ['—', '—', '—']],
    en: [['client ID set', `developer token valid until ${valid} · store region: Taiwan`, 'me@example.com'],
      ['client ID has the wrong format', `developer token expired on ${expired}`, '—'], ['—', '—', '—']],
  };
  const raw = ['client_id', 'developer_token', 'user_token', 'access_token', 'device_id', 'dev1', 'storefront', 'missing', 'keychain_error', 'builtin', 'malformed', 'T04:30', ': ok', ': set'];
  try {
    for (const [lang, file] of [['zh-TW', './i18n.json'], ['en', './i18n-en.json']]) {
      i18nFile = file;
      await loadI18n(api);
      fixtures.forEach((st, i) => {
        const r = mk();
        initAccount(r, null, { run: (_c, h) => { h.onStdout(JSON.stringify(st)); h.onExit(0, ''); }, idle: (fn) => fn() }, () => {});
        const got = allByClass(r, 'acct__detail').map((e) => e.textContent);
        check(JSON.stringify(got) === JSON.stringify(want[lang][i]), `${lang} 帳號頁的細節欄(第 ${i + 1} 組):${JSON.stringify(got)}`);
        const leaked = raw.filter((k) => r.textContent.includes(k));
        check(!leaked.length, `${lang} 帳號頁不可以畫出 JSON 的欄名或列舉值(第 ${i + 1} 組):${leaked} / ${r.textContent}`);
      });
    }
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 15. 目錄讀不到(token 不對 / 過期 → 401、伺服器不在 → 連不上):app.js 不建頁面、不起播放列,畫面上沒有任何 webui.* 的 key,
//     只有 notice 說原因(401 的那一句是伺服器回應裡的;連不上是瀏覽器的錯誤)。放在最後:app.mjs 跟這裡共用 i18n.mjs,
//     跑完目錄是空的;每一種情況用不同的 ?case= 各載入一次(ESM 同一個網址只執行一次)。
await scenario('15', async () => {
  const badToken = JSON.parse(readFileSync('./i18n.json')).messages['web.err.bad_token'];
  globalThis.window = { addEventListener() {} };
  globalThis.history = { replaceState() {} };
  globalThis.location.pathname = '/';
  const empty = { fetch: async () => new Response(JSON.stringify({ lang: '', supported: [], messages: {} }), { status: 200 }) };
  for (const [name, fetchImpl, want] of [
    ['401', async () => new Response(JSON.stringify({ error: badToken }), { status: 401, headers: { 'Content-Type': 'application/json' } }), badToken],
    ['down', async () => { throw new TypeError('Failed to fetch'); }, 'Failed to fetch'],
    // 目錄那一發失敗、/api/commands 卻成功:畫面是空的,notice 要說一句(不能是空白一片)
    ['i18n-blip', async (path) => (String(path).includes('/api/i18n')
      ? new Response('oops', { status: 500 })
      : new Response(JSON.stringify({ version: 'dev', providers: ['spotify'], default_provider: 'spotify', commands: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })),
    "capy --web couldn't load the interface texts; reload the page"],
  ]) {
    await loadI18n(empty); // 真的瀏覽器裡這時還沒有目錄:別讓前面情境載進來的中文目錄替 t() 墊字
    for (const k of Object.keys(ids)) delete ids[k];
    globalThis.document.body = mk('body');
    globalThis.location.hash = ''; // 落在預設的搬家頁(前面的情境可能留下別頁的 hash)
    globalThis.fetch = fetchImpl;
    await import(`./app.mjs?case=${name}`);
    await tick(50);
    const drawn = [];
    const walk = (e) => { drawn.push(e._text || '', ...Object.values(e.attrs || {}), e.placeholder || ''); for (const c of e.children || []) walk(c); };
    for (const e of [...Object.values(ids), globalThis.document.body]) walk(e);
    const keys = drawn.filter((s) => s.includes('webui.') || /(^|\s)web\.err\./.test(s)); // web.err.* 也是 key:沒目錄時 t() 只會回它
    check(!keys.length, `${name}:目錄讀不到時畫面上不可以有 key:${JSON.stringify(keys.slice(0, 5))}`);
    check(ids.notice?.textContent === want && ids.notice.hidden === false, `${name}:notice 要說原因:「${ids.notice?.textContent}」`);
  }
});

flush();
if (failures.length) {
  say(`${failures.length} 條不成立`);
  process.exit(1);
}
say('ok');
