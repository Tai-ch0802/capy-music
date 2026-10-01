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
    replaceChildren(...cs) { this.children.forEach((c) => { c.parentNode = null; }); this._text = ''; this.children = []; this.append(...cs); }, // 同真的 DOM:先前 textContent 設的字也一起換掉;換掉的節點脫離(contains 才認不到它)
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
    hasAttribute(k) { return k in this.attrs || (k.startsWith('data-') && camel(k.slice(5)) in this.dataset); }, // 同真的 DOM:dataset 設的也算(btn() 的序列槽閘看 body 的 data-slot)
    addEventListener(t, f) { (this.l ||= {})[t] = f; },
    click(detail = 1) { this.l?.click?.({ detail }); }, // detail:連點第幾下(真的 DOM:滑鼠 1、2…,鍵盤 0)
    scrollIntoView() { (globalThis.scrolled ||= []).push(this); },
    showModal() { this.open = true; }, // <dialog>:close() 同真的 DOM,開著才觸發 close 事件
    close() { if (!this.open) return; this.open = false; this.l?.close?.(); },
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
  dispatchEvent(ev) { (this.events ||= []).push(ev.type); return true; }, // player.js 每輪 render 後的 capy:now(歌曲 wiki 頁聽它)
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
    check(JSON.stringify(labels) === JSON.stringify(['Checking account connections', 'Connecting Spotify', 'Reading playlists on Apple Music', 'Reading playlists on Spotify', 'Reading your playlists', moving, moving, moving]),
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
  const exported = JSON.stringify({ 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: 'x' }, items: [{ cid: 'c1' }, { cid: 'c2' }] }, 'tracks.json': { tracks: { c1: { title: 'T1' } } } });
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
      allByClass(pl, 'btn').find((b) => b.textContent === t('webui.playlists.platforms.read')).click(); // 「讀取各平台的清單」:只讀這台有登入的(auth 讀不到 = 當成有登入)
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
      script = { export: stdout(exported), 'pl sync --all --dry-run': { events: [table, { type: 'exit', code: 2, message: 'x', reason: 'done' }] } };
      const sy = page(initSync);
      await settle();
      const adv = (k) => { let x = null; const w = (n) => { for (const c of n.children || []) { if (!x && c.dataset?.adv === k) x = c; w(c); } }; w(sy); return x; };
      for (const k of ['pull', 'push', 'sync', 'dedup']) { adv(k).click(); await settle(); } // 進階:從平台更新 / 推到平台 / 雙向同步 / 去除重複
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
  const reread = ['auth status --json', 'export']; // 同步頁進頁、每次寫入收尾後都安靜重讀
  const cmds = JSON.stringify(['auth status --json', 'export', 'pl list --provider spotify', 'auth status --json', 'export', 'auth status --json', 'export', 'search x --provider spotify --limit 10', 'search y --provider spotify --limit 10',
    'search z --provider spotify --limit 10', 'play --id sp1 --provider spotify', ...reread,
    'pl pull --all --dry-run', ...reread, 'pl push --all --dry-run', ...reread, 'pl sync --all --dry-run', ...reread, 'pl dedup --dry-run', ...reread]);

  const zh = await paint();
  want(zh, 'zh-TW', ['我的清單', '每份清單的正本存在你自己的 Google Drive。正本可以連到每個平台(Spotify、Apple Music、YouTube Music)上各一份清單;同步時,兩邊的新增、刪除與順序會互相帶過去。',
    '正本、連結、同步是什麼意思?', '搬家是一次性複製過去。同步是之後兩邊持續跟著彼此改。', '顯示的是這台電腦上次同步後的內容', '歌曲', '時長', '重新整理', '各平台上的清單', '讀取各平台的清單', 'Spotify 上沒有清單。',
    'road trip(2 首)', '(本機沒有這首的資料)', '同步這份清單', 'Google Drive 正本', '連到這些平台上的清單', '已連結', '這台電腦還沒讀過你 Google Drive 上的正本。到「同步」按「讀回正本(會順便讀已登入的平台)」就會讀進來;還沒有任何清單的話,到「搬家」搬一份過來。',
    '搜尋', '在平台上找歌。Spotify 找到了可以直接播;Apple Music(macOS)只直接播你資料庫裡有的歌,其他的會在 Music.app 打開並標出那一首。', '五月天 派對動物', '關鍵字', '結果數', '輸入歌名或歌手,按「搜尋」。',
    '在 Spotify 找不到「x」。換個關鍵字,或換一個平台試試。', '沒有找到。換個關鍵字試試。',
    '同步', '讓同一份清單在幾個平台上保持一樣:改了任何一邊,按「同步」,其他地方跟著改。加歌、拿掉、換位置、改名之前,capy 一定先列出要改什麼,你確認了才寫;建一份新的正本或平台清單並連上,按下去就會做。',
    '正本在你的 Google Drive', '從哪一份開始都可以', '名字只在連的那一刻能不同', '在同步的清單', '全部同步', '同步', '讓一份清單開始同步', '從哪個平台開始', '從左邊挑一份清單',
    '正本名稱或 pid(留空 = 全部)', '全部平台', '正本', '只看變更,先不寫入', '從平台更新', '推到平台', '雙向同步', '去除重複',
    '「去除重複」整理的是正本裡重複的歌(只留第一份),再推到連著的平台;上面選的平台只決定這次去檢查哪個平台上的重複,沒選到的平台這次不會檢查。正本留空時會讓你挑一份;填「平台:清單」(例如 spotify:清單 ID)只檢查那份平台清單,不改任何東西。',
    '1 筆變更(跳過的不算)',
    '以上是會改的東西,還沒有寫入。取消勾選「只看變更,先不寫入」再按一次,就會照這張表問你、確認後寫入。']);
  check([...zh.seen].some((s) => s.startsWith('export 的輸出不是 JSON:') && s.length > 'export 的輸出不是 JSON:'.length), 'zh-TW:export 不是 JSON 要說出來並附上原因');
  const st = t('webui.move.label.status');
  const rl = [st, '讀取你的清單'];
  check(JSON.stringify(zh.labels) === JSON.stringify([st, '讀取你的清單', '讀取 Spotify 上的清單', st, '讀取你的清單', st, '讀取你的清單',
    '在 Spotify 找「x」', '在 Spotify 找「y」', '在 Spotify 找「z」', '播放「Song One」', ...rl, '把平台上的變更拉回來', ...rl, '把 capy 保管的清單推到平台', ...rl, '雙向同步', ...rl, '去除重複的歌', ...rl]), `zh-TW 的 label:${JSON.stringify(zh.labels)}`);
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
    want(en, 'en', ['My playlists', 'What are master copies, links and syncing?', 'Song', 'Length', 'Playlists on each platform', 'road trip (2 songs)', 'Enter a song or artist, then press "Search".', '1 change (skipped rows don\'t count)', 'All platforms']);
    check([...en.seen].some((s) => s.includes('Uncheck "Preview only, don\'t write yet"')), 'en:只看變更的收尾要指名那個勾選框');
    check(en.labels.includes('Reading playlists on Spotify') && en.labels.includes('Playing "Song One"') && JSON.stringify(en.calls) === cmds && en.all[0] === '', `en 的 label 與命令:${JSON.stringify(en.labels)} ${JSON.stringify(en.calls)}`);
    check(JSON.stringify(en.rowsDrawn) === JSON.stringify([['sp1', 'Song One', 'Play']]), `en 的搜尋結果表:${JSON.stringify(en.rowsDrawn)}`);
    const enChecking = await checking();
    check(enChecking === 'Checking…', `en 診斷頁的 data-running:「${enChecking}」`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 8n. 連回 Spotify(計畫 §1.7 S7 其餘的部分):清單頁的正本曲目、清單本身、pl list 與 pl show 的表,同步頁與主控台的變更表,
//     搬家精靈的預覽表與搬不過去的歌。只有確定是 Spotify 的列才連(平台看呼叫端送出去的那一家、或每一列自己的 PROVIDER);
//     不像 Spotify id 的(local file、釘成「沒有」的空字串)不連;一列都連不出去的表不多一欄。連結本身不送命令。
await scenario('8n', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const { initSync } = await import('./pages/sync.mjs');
  const { initMove, tally } = await import('./pages/move.mjs');
  const T1 = '4uLU6hMCjMI75M1A2tKUQC', T2 = '7qiZfU4dY1lWllzX7mPBI3', PL = '37i9dQZF1DXcBWIGoYBM5M';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'A') out.push(c); }); return out; };
  const widths = (tw) => [tw.querySelector('thead tr').children.length, ...tw.querySelectorAll('tbody tr').map((tr) => tr.children.length)];
  const idle = () => new Promise((r) => con.idle(r));
  const label = (a) => a && a.getAttribute('aria-label');
  const track = (id) => `https://open.spotify.com/track/${id}`;
  const exit2 = { type: 'exit', code: 2, message: 'x', reason: 'done' };
  const table = (header, rows, end = done) => ({ events: [{ type: 'table', header, rows }, end] });
  const TH = ['ID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'];
  const LH = ['ID', 'NAME', 'TRACKS', 'OWNER'];

  // ── 清單頁
  reset();
  script = {
    export: { events: [{ type: 'stdout', text: JSON.stringify({
      'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: PL }, items: [{ cid: 'c1' }, { cid: 'c2' }, { cid: 'c3' }, { cid: 'c4' }] },
      'pl__b.json': { pid: 'b', name: 'mix', links: { apple: 'p.AAAA', spotify: {} }, items: [{ cid: 'c4' }] },
      'tracks.json': { tracks: {
        c1: { title: 'Song A', mappings: { spotify: { id: T1, confidence: 100, source: 'observed' } } },
        c2: { title: 'Song B', mappings: { spotify: { id: '', confidence: 100, source: 'review', pinned: true } } }, // 釘成「Spotify 沒有」
        c3: { title: 'Song C', mappings: { spotify: { id: 'spotify:local:a:b:c:1', source: 'observed' } } },       // Spotify 的 local file
        c4: { title: 'Song D', mappings: { apple: { id: 'i.XYZ', source: 'observed' } } },
      } } }) }, done] },
    [`pl show ${PL} --provider spotify`]: table(TH, [[T2, 'Live One', 'x', '', '1000'], ['spotify:local:q:r:s:2', 'Local', '', '', '0']]),
    'pl show p.AAAA --provider apple': table(TH, [[T2, 'Apple One', 'x', '', '1000']]), // id 長得像 Spotify 也不連:這張表是 Apple 的
    'pl list --provider spotify': table(LH, [[PL, 'road trip', '4', 'me']]),
    'pl list --provider apple': table(LH, [[T2, 'Mix', '-', '']]),
  };
  const pl = mk();
  initPlaylists(pl, api, con, () => {}, { list: ['spotify', 'apple'], current: 'spotify' });
  await idle();
  const [left] = allByClass(pl, 'pl__left');
  const [right] = allByClass(pl, 'pl__right');
  const items = allByClass(right, 'pl__songs')[0];
  const il = anchors(items);
  check(il.length === 1 && il[0].href === track(T1) && il[0].target === '_blank' && il[0].rel === 'noopener noreferrer' && il[0].textContent === '在 Spotify 上聽' && label(il[0]) === '在 Spotify 上聽「Song A」',
    `正本曲目:有 Spotify 對應的才連(空字串、local file、只有 Apple 的不連):${JSON.stringify(il.map((a) => [a.href, label(a)]))}`);
  // 欄:#、歌曲、時長 + 連著的 Spotify 一欄(Apple 的格子只有 ▶,node 不是 macOS,整欄不出現);每一列都有每一格
  check(JSON.stringify(widths(items)) === JSON.stringify([5, 5, 5, 5, 5]), `正本曲目表的欄(最後一欄是 Wiki)、每一列都有那一格:${JSON.stringify(widths(items))}`);
  const spRow = allByClass(right, 'pl__link').find((r) => r.dataset.platform === 'spotify');
  const pa = anchors(spRow).find((a) => a.href.includes('/playlist/'));
  check(pa && pa.href === `https://open.spotify.com/playlist/${PL}` && label(pa) === '在 Spotify 上聽「road trip」',
    `連著的 Spotify 清單本身:在連結面板的 Spotify 那一列:${pa && pa.href}`);
  check(anchors(left).length === 0, '左欄那一列整個是按鈕:裡面不放連結');

  allByClass(left, 'pl__item')[1].click(); // mix:連著 apple;spotify 的值是物件的壞資料,不算連著、不出按鈕
  await idle();
  check(anchors(right).length === 0 && JSON.stringify(widths(allByClass(right, 'pl__songs')[0])) === JSON.stringify([4, 4]), '沒有 Spotify 對應、清單的 spotify 連結不是字串:沒有連結、不多一欄');

  allByClass(pl, 'btn').find((b) => b.textContent === '讀取各平台的清單').click(); // 各平台上的清單:Spotify 的列連到清單,Apple 的不連
  await idle(); await tick(5); await idle();
  const platRows = (p) => allByClass(pl, 'pl__plat').filter((r) => r.dataset.platform === p);
  const listed = anchors(platRows('spotify')[0]);
  check(listed.length === 1 && listed[0].href === `https://open.spotify.com/playlist/${PL}` && label(listed[0]) === '在 Spotify 上聽「road trip」', `各平台上的清單(Spotify)連到清單:${JSON.stringify(listed.map((a) => a.href))}`);
  const ap = platRows('apple');
  check(ap.length === 1 && !anchors(ap[0]).some((a) => a.href.includes('spotify.com')) && ap[0].dataset.state === 'same_name' && !anchors(ap[0]).some((a) => a.href === '#/move') && ap[0].textContent.includes('它在 Apple Music 上已經連著另一份清單')
    && !allByClass(ap[0], 'btn').some((b) => ['連到正本「mix」', '納入 capy'].includes(b.textContent)),
    '各平台上的清單(Apple):跟正本「mix」只差大小寫 = 同名,但那份正本已連著 Apple 的另一份:不給納入、不給合併、不指去搬家,說只能先改名');

  // ── 同步頁與主控台的變更表:PROVIDER / PROVIDER_ID 的位置在 pull 與 sync 的表不一樣,照表頭找
  const PH = ['ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  const SH = ['DIR', ...PH];
  reset();
  script = {
    'pl pull --all --dry-run': table(PH, [
      ['add', 'spotify', 'road trip', '0', 'c1', T1, 'Song A', 'a', 'r', 'added_on_platform'],
      ['rename', 'spotify', 'road trip', '', '', '', 'New Name', '', 'r', 'renamed_on_platform'], // 改名那一列的 TITLE 是清單名、沒有曲目 id
      ['add', 'apple', 'road trip', '1', 'c9', T2, 'Song Z', 'a', 'r', 'added_on_platform']], exit2),
    'pl sync --all --dry-run': table(SH, [['push', 'add', 'spotify', 'road trip', '1', 'c2', T2, 'Song B', 'b', 'r', 'push']], exit2),
    'pl push --all --dry-run': table(PH, [['add', 'apple', 'road trip', '1', 'c2', 'i.B', 'Song B', 'b', 'r', 'push']], exit2),
  };
  const sy = mk();
  initSync(sy, api, con, () => {}, { list: ['spotify', 'apple'], current: 'spotify' });
  await idle();
  const adv = (k) => { let x = null; walk(sy, (c) => { if (!x && c.dataset?.adv === k) x = c; }); return x; }; // 進階區的控制項(不靠 DOM 位置)
  const [pullBtn, pushBtn, syncBtn] = ['pull', 'push', 'sync'].map(adv);
  pullBtn.click();
  await idle();
  check(allByClass(sy, 'chg__act')[0]?.textContent === '加進正本(從 Spotify)', `同步頁 pl pull 的表:依按下的那一顆給方向:${allByClass(sy, 'chg__act')[0]?.textContent}`);
  const pulled = anchors(sy.querySelector('.tbl-wrap'));
  check(pulled.length === 1 && pulled[0].href === track(T1) && label(pulled[0]) === '在 Spotify 上聽「Song A」', `pull 的表:只有 Spotify 的曲目列連(改名列、Apple 列不連):${JSON.stringify(pulled.map((a) => a.href))}`);
  const tables = allByClass(root, 'tbl');
  const inConsole = anchors(tables[tables.length - 1]);
  check(inConsole.length === 1 && inConsole[0].href === track(T1), `主控台裡同一張表也連:${JSON.stringify(inConsole.map((a) => a.href))}`);
  pushBtn.click();
  await idle();
  check(allByClass(sy, 'chg__act')[0]?.textContent === '加到 Apple Music', `同步頁 pl push 的表:方向是 push:${allByClass(sy, 'chg__act')[0]?.textContent}`);
  syncBtn.click();
  await idle();
  const synced = anchors(sy.querySelector('.tbl-wrap'));
  check(synced.length === 1 && synced[0].href === track(T2) && label(synced[0]) === '在 Spotify 上聽「Song B」', `sync 的表(多一個 DIR 欄):${JSON.stringify(synced.map((a) => a.href))}`);
  script = { 'search k': table(['ID', 'TITLE'], [[T1, 'Song A']]) };
  await con.run('search k');
  const typed = allByClass(root, 'tbl');
  check(anchors(typed[typed.length - 1]).length === 0, '主控台打的 search 不帶平台:猜不出是哪一家,不連');
  // resolve 的 review 列:PROVIDER_ID 是還沒確認的候選,報讀不說正本的歌名(那是另一首),改說 id
  const RH = ['ACTION', 'CID', 'PROVIDER', 'PROVIDER_ID', 'CONFIDENCE', 'SOURCE', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  script = { 'resolve x --provider spotify --dry-run': table(RH, [['map', 'c1', 'spotify', T1, '95', 'isrc', 'Song A', 'a', 'r', 'isrc'],
    ['review', 'c2', 'spotify', T2, '70', 'fuzzy', 'Song B', 'b', 'r', 'low_score']], exit2) };
  await con.run('resolve x --provider spotify --dry-run');
  const rt = allByClass(root, 'tbl');
  const rl = anchors(rt[rt.length - 1]).map(label);
  check(JSON.stringify(rl) === JSON.stringify(['在 Spotify 上聽「Song A」', `在 Spotify 上聽「${T2}」`]), `resolve:map 列說歌名、review 列說候選的 id:${JSON.stringify(rl)}`);
  // pl dedup spotify:<清單>:平台清單的去重報告沒有 PROVIDER 欄,但每一列都是 Spotify 的
  script = { 'pl dedup -- spotify:mix': table(['POS', 'ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'], [['1', T1, 'Song A', 'a', 'r', 'duplicate']]) };
  adv('name').value = 'spotify:mix'; // 平台清單的報告:勾著「只看變更」也只送名稱(帶旗標會被 dedup.go 退回)
  adv('dedup').click();
  await idle();
  const rep = anchors(sy.querySelector('.tbl-wrap'));
  check(rep.length === 1 && rep[0].href === track(T1), `Spotify 清單的去重報告也連:${JSON.stringify(rep.map((a) => a.href))}`);

  // ── 搜尋頁:命令還在跑就換掉平台,結果表照送出去的那一家(Spotify):有連結、播放鈕送 spotify
  const { initSearch } = await import('./pages/search.mjs');
  script = { [`search k --provider spotify --limit 10`]: table(['ID', 'TITLE'], [[T1, 'Song A']]) };
  const se = mk();
  initSearch(se, api, con, () => {}, { list: ['spotify', 'apple'], current: 'spotify' });
  se.querySelector('input').value = 'k';
  const psel = se.querySelector('select');
  psel.value = 'spotify';
  se.querySelector('.btn--primary').click();
  psel.value = 'apple';
  await idle();
  const srow = se.querySelector('.tbl-wrap').querySelector('tbody tr');
  check(anchors(srow).length === 1 && srow.querySelector('button')?.textContent === '播放', `搜尋頁照送出去的平台畫結果:${srow && srow.textContent}`);

  // ── 搬家精靈:從 Spotify 搬到 Apple Music。tally 記下帶歌名那一列的 Spotify id(migrate.go 產生得出的兩種形狀):
  //    加進既有清單 = migrate 列(來源 Spotify,有 id)在前、推向目的地的 push 列在後;
  //    正本已經連著來源(或同名正本原本就有這首)= 只有推向 Apple 的 push 列,表裡沒有 Spotify 的 id——已知不連(計畫 §1.7 S7)。
  const t1 = tally(SH, [['migrate', 'add', 'spotify', 'road trip', '0', 'c', T1, 'Song C', 'c', 'no match', 'no_mapping'],
    ['push', 'skip', 'apple', 'road trip', '0', 'c', '', 'Song C', 'c', 'no match', 'no_mapping']]);
  check(t1.missed.length === 1 && t1.missed[0].spotify === T1, `加進既有清單:搬不過去的歌記下 Spotify id:${JSON.stringify(t1)}`);
  const t2 = tally(SH, [['push', 'add', 'apple', 'road trip', '0', 'a', 'i.A1', 'Song A', 'a', '', 'push'],
    ['push', 'skip', 'apple', 'road trip', '1', 'b', '', 'Song B', 'b', 'no match', 'no_mapping'],
    ['push', 'skip', 'apple', 'road trip', '2', 'd', 'i.D4', 'Song D', 'd', 'cannot push', 'unpushable']]); // 有 Apple 的 id 也不是 Spotify 的
  check(t2.missed.length === 2 && t2.missed.every((m) => m.spotify === ''), `只有 push 列(正本已連著來源):沒有 Spotify id、不連:${JSON.stringify(t2)}`);
  globalThis.document.createElementNS ||= (_, tag) => mk(tag); // 水豚是 SVG
  const button = (r, text) => { let b = null; walk(r, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const radios = (r, name) => { const out = []; walk(r, (c) => { if (c.tagName === 'INPUT' && c.type === 'radio' && c.name === name) out.push(c); }); return out; };
  const lists = (rows) => (h) => { h.onTable(LH, rows); h.onExit(0, '', 'done'); };
  let mig = null;
  const plan = {
    'auth status --json': (h) => { h.onStdout(JSON.stringify({ spotify: { state: 'ok', client_id: 'set' }, google: { state: 'ok', client: 'builtin' }, apple: { state: 'ok', developer_token: 'ok', user_token: 'ok' } })); h.onExit(0, '', 'done'); },
    'pl list --provider spotify': lists([['p1', 'Road trip', '2', 'me']]),
    'pl list --provider apple': lists([]),
    'migrate p1 --from spotify --to apple': (h) => { mig = h; },
  };
  const fcon = { idle: (fn) => fn(), run(_line, hooks, o) { plan[o.args.join(' ')]?.(hooks); return Promise.resolve(); } };
  const mv = mk();
  initMove(mv, api, fcon, () => {}, { list: ['spotify', 'apple'] });
  radios(mv, 'wiz-to')[1].l.change();   // 到 Apple Music
  radios(mv, 'wiz-from')[0].l.change(); // 從 Spotify
  button(mv, t('webui.move.next.playlist')).click();
  radios(mv, 'wiz-src')[0].l.change();
  button(mv, t('webui.move.next.confirm')).click();
  button(mv, t('webui.move.start')).click();
  check(mig !== null, '精靈要送出 migrate p1 --from spotify --to apple');
  mig?.onTable(SH, [['migrate', 'add', 'spotify', 'Road trip', '0', 'a', T1, 'Song A', 'a', 'push to apple:x', 'push'],
    ['migrate', 'add', 'spotify', 'Road trip', '1', 'b', T2, 'Song B', 'b', 'no match on apple', 'no_mapping']]);
  const ml = anchors(mv.querySelector('.wiz__missed'));
  check(ml.length === 1 && ml[0].href === track(T2) && label(ml[0]) === '在 Spotify 上聽「Song B」' && ml[0].className === 'wiz__link', `搬不過去的歌(Spotify 的曲名)連回去,用文字連結的樣式:${JSON.stringify(ml.map((a) => [a.href, a.className]))}`);
  check(anchors(mv.querySelector('.wiz__more')).length === 2, '完整的表:兩首都是 Spotify 的列');
});

// 8o. 清單頁的兩個既有問題:(1) 清單檔裡還帶著敗者 cid 的項目(別台裝置的清單檔)要沿 tracks.json 的 merged 墓碑找到勝者,
//     曲名與 Spotify 連結都照勝者;(2) 連結面板每個連著的平台一列,平台清單的連結用 links 裡的 id(不是正本的名稱:
//     改過名或有同名清單時名稱會找錯);值是空字串的平台(沒連結)不算連著,左欄也不標。
await scenario('8o', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const T1 = '4uLU6hMCjMI75M1A2tKUQC', PL = '37i9dQZF1DXcBWIGoYBM5M';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'A') out.push(c); }); return out; };
  const idle = () => new Promise((r) => con.idle(r));
  const table = (rows) => ({ events: [{ type: 'table', header: ['ID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'], rows }, done] });
  reset();
  script = {
    export: { events: [{ type: 'stdout', text: JSON.stringify({
      'pl__a.json': { pid: 'a', name: 'renamed', links: { apple: 'p.AAAA', local: '', spotify: PL }, items: [{ cid: 'old' }] },
      'tracks.json': { tracks: { win: { title: 'Winner', mappings: { spotify: { id: T1, confidence: 100, source: 'observed' } } } }, merged: { old: 'win' } } }) }, done] },
    'pl show p.AAAA --provider apple': table([[T1, 'Apple One', 'x', '', '1000']]),
    [`pl show ${PL} --provider spotify`]: table([[T1, 'Live One', 'x', '', '1000']]),
  };
  const pl = mk();
  initPlaylists(pl, api, con, () => {}, { list: ['spotify', 'apple'], current: 'spotify' });
  await idle();
  const [left] = allByClass(pl, 'pl__left');
  const [right] = allByClass(pl, 'pl__right');
  const row = allByClass(right, 'pl__songs')[0].querySelector('tbody tr');
  const il = anchors(row);
  check(row.dataset.cid === 'old' && allByClass(row, 'pl__title')[0]?.textContent === 'Winner' && il.length === 1 && il[0].href === `https://open.spotify.com/track/${T1}`,
    `敗者 cid 沿 merged 找到勝者的曲名與 Spotify 連結:${row.dataset.cid} ${row.textContent} ${JSON.stringify(il.map((a) => a.href))}`);
  const chips = left.querySelector('.pl__chips').children.map((c) => c.textContent);
  check(JSON.stringify(chips) === JSON.stringify(['Apple Music', 'Spotify']), `左欄只標真的連著的平台(顯示名稱,不是原始 id):${JSON.stringify(chips)}`);
  const lrows = allByClass(right, 'pl__link');
  check(JSON.stringify(lrows.map((r) => [r.dataset.platform, r.dataset.state])) === JSON.stringify([['spotify', 'linked'], ['apple', 'linked']]),
    `連結面板:連著的兩家各一列、空字串的本機不算連著:${JSON.stringify(lrows.map((r) => [r.dataset.platform, r.dataset.state]))}`);
  const sp = anchors(lrows[0]).find((a) => a.href.includes('/playlist/'));
  check(sp && sp.href === `https://open.spotify.com/playlist/${PL}` && anchors(lrows[1]).length === 0, `Spotify 那一列連到 links 裡的 id;Apple 的資料庫清單沒有公開網址:${sp && sp.href}`);
  check(JSON.stringify(calls) === JSON.stringify(['auth status --json', 'export']) && allByClass(pl, 'pl__plat').length === 0, `進頁只讀 auth 與 export(平台清單按了才讀):${JSON.stringify(calls)}`);
});

// 8p. 我的清單改版 T1(計畫 2026-09-30 §3.1–§3.2,決策 61):歌曲表的欄與格子、▶ 的條件與送出的命令(args 陣列)、
//     篩選只藏列不重排(決策 38)、兩種空白態、英文目錄下沒有中文。navigator 換成 macOS / Windows 各跑一次(Apple 的 ▶ 只在 macOS)。
await scenario('8p', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const T1 = '4uLU6hMCjMI75M1A2tKUQC';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'A') out.push(c); }); return out; };
  const buttons = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'BUTTON') out.push(c); }); return out; };
  const idle = () => new Promise((r) => con.idle(r));
  const out = (o) => ({ events: [{ type: 'stdout', text: typeof o === 'string' ? o : JSON.stringify(o) }, done] });
  const nav = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  const ua = (s) => Object.defineProperty(globalThis, 'navigator', { value: { userAgent: s }, configurable: true, writable: true });
  const MAC = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15';
  const WIN = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36';
  const ok = { state: 'ok' };
  const tracks = {
    c1: { title: 'Song A', artists: ['X', 'Y'], album: 'Alb', duration_ms: 257000, mappings: { spotify: { id: T1 }, apple: { id: '1440' }, youtube: { id: 'vid-1' }, local: { id: 'Music/a.mp3' } } },
    c2: { title: 'Song B', mappings: { spotify: { id: 'spotify:local:a:b:c:1' }, apple: { id: 'i.LIB' } } },
    c3: { title: 'Song C', mappings: { apple: { id: '', pinned: true } } },
    c4: { title: 'Song D', mappings: { spotify: { id: 'spotify:episode:xyz' } } },
  };
  const one = { 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: 'PL', apple: 'p.A', local: 'dev/Music/road.m3u8' }, items: ['c1', 'c2', 'c1', 'c3', 'c4', 'gone'].map((cid) => ({ cid })) }, 'tracks.json': { tracks } };
  const providers = { list: ['spotify', 'apple', 'local', 'youtube'], current: 'spotify' };
  const open = async (auth, data) => {
    reset();
    script = { 'auth status --json': out(auth), export: data };
    const pl = mk();
    initPlaylists(pl, api, con, (x) => notices.push(x), providers);
    await idle();
    return pl;
  };
  const table = (pl) => allByClass(pl, 'pl__tbl')[0];
  const head = (pl) => table(pl).querySelector('thead tr').children.map((th) => th.textContent);
  const rows = (pl) => table(pl).querySelectorAll('tbody tr');
  const cellOf = (tr, platform) => tr.children.find((td) => td.dataset.platform === platform);
  try {
    // ── macOS、三家都登入:三個平台欄;每一格照計畫 §3.2 的表
    ua(MAC);
    let pl = await open({ spotify: ok, apple: ok, youtube: ok, google: ok }, out(one));
    check(JSON.stringify(head(pl)) === JSON.stringify(['#', '歌曲', '時長', 'Spotify', 'Apple Music', 'YouTube Music', '']), `平台欄照 providers 的順序、本機連著也有對應但不佔欄:${JSON.stringify(head(pl))}`);
    check(!anchors(table(pl)).some((a) => a.href.includes('a.mp3')) && !rows(pl).some((tr) => tr.children.some((td) => td.dataset.platform === '本機曲庫')), '本機沒有播放、沒有網頁:不佔欄、不連');
    const tall = allByClass(pl, 'tbl-wrap--tall');
    check(tall.length === 1 && allByClass(tall[0], 'pl__tbl').length === 1, '歌曲表放在固定高度的 .tbl-wrap--tall 裡');
    const rs = rows(pl);
    check(JSON.stringify(rs.map((tr) => tr.dataset.cid)) === JSON.stringify(['c1', 'c2', 'c1', 'c3', 'c4', 'gone']), `列序 = items 的順序,同一首兩次就兩列:${JSON.stringify(rs.map((tr) => tr.dataset.cid))}`);
    check(rs.every((tr) => !tr.children.some((td) => td.textContent === 'c1' || td.textContent === 'gone')), 'CID 不上畫面');
    const [r0, r1, , r3, r4, r5] = rs;
    check(r0.children[0].textContent === '1' && allByClass(r0, 'pl__title')[0].textContent === 'Song A' && allByClass(r0, 'pl__sub')[0].textContent === 'X, Y · Alb' && r0.children[2].textContent === '4:17',
      `位置、曲名、歌手 · 專輯、時長:${r0.textContent}`);
    const sp0 = cellOf(r0, 'Spotify');
    const [spPlay] = buttons(sp0);
    const [spLink] = anchors(sp0);
    check(spPlay && spPlay.textContent === '▶' && 'run' in spPlay.dataset && spPlay.getAttribute('aria-label') === '用 Spotify 播放「Song A」', `Spotify 的 ▶:${spPlay && spPlay.getAttribute('aria-label')}`);
    check(spLink && spLink.href === `https://open.spotify.com/track/${T1}` && spLink.textContent === '在 Spotify 上聽', `Spotify 的連結看得到(S7):${spLink && spLink.href}`);
    const [apPlay] = buttons(cellOf(r0, 'Apple Music'));
    check(apPlay && apPlay.getAttribute('aria-label') === '用 Apple Music 播放「Song A」' && anchors(cellOf(r0, 'Apple Music')).length === 0, 'Apple 的格子只有 ▶');
    const [yt] = anchors(cellOf(r0, 'YouTube Music'));
    check(yt && yt.href === 'https://music.youtube.com/watch?v=vid-1' && yt.target === '_blank' && yt.textContent === '開啟' && yt.getAttribute('aria-label') === '開啟「Song A」(YouTube Music)' && buttons(cellOf(r0, 'YouTube Music')).length === 0,
      `YouTube 只有「開啟」,報讀名稱以看得到的字開頭:${yt && [yt.textContent, yt.getAttribute('aria-label')]}`);
    const only = (td) => allByClass(td, 'pl__only')[0];
    const none = (td) => allByClass(td, 'pl__none')[0];
    check(only(cellOf(r1, 'Spotify'))?.textContent === 'Spotify 本機檔' && buttons(cellOf(r1, 'Spotify')).length === 0 && anchors(cellOf(r1, 'Spotify')).length === 0, 'spotify:local:… 沒有 ▶、沒有連結,寫出是本機檔');
    check(only(cellOf(r1, 'Apple Music'))?.textContent === '只在資料庫' && buttons(cellOf(r1, 'Apple Music')).length === 0, 'Apple 的 i. id 不給 ▶(play --id 會打 catalog 端點失敗)');
    check(none(cellOf(r3, 'Apple Music'))?.title === '你標過 Apple Music 沒有這首' && none(cellOf(r3, 'Spotify'))?.title === '這首在 Spotify 還沒有對應', '沒有對應與釘成「沒有」說法不同');
    check(none(cellOf(r4, 'Spotify'))?.title === 'capy 放不了這一首', '其他不是 22 碼的 Spotify id:放不了');
    check(allByClass(r5, 'pl__title')[0].textContent === '(本機沒有這首的資料)', '懸空的 cid:照舊說這台電腦沒有資料');
    // ▶ 走 args 陣列;Apple 只打開、沒開始播時,那句話進 notice(決策 52)
    script['play --id ' + T1 + ' --provider spotify'] = out('▶ ' + T1 + '\n');
    script['play --id 1440 --provider apple'] = out('opened in Music.app\n');
    spPlay.click();
    await idle(); await tick(5);
    check(JSON.stringify(bodies.at(-1)) === JSON.stringify({ args: ['play', '--id', T1, '--provider', 'spotify'] }) && notices.every((n) => !n.startsWith('▶')), `Spotify 的 ▶:${JSON.stringify(bodies.at(-1))} ${JSON.stringify(notices)}`);
    apPlay.click();
    await idle(); await tick(5);
    check(JSON.stringify(bodies.at(-1)) === JSON.stringify({ args: ['play', '--id', '1440', '--provider', 'apple'] }) && notices.includes('opened in Music.app'), `Apple 的 ▶ 只打開時 notice 照抄那句:${JSON.stringify(notices)}`);
    check(allByClass(pl, 'pl__filter').length === 0, '清單與歌都不多時沒有篩選框');

    // ── Windows、Spotify 沒登入:Spotify 只留連結;Apple 的格子只有 ▶,整欄不出現
    ua(WIN);
    pl = await open({ spotify: { state: 'missing' }, apple: ok, google: ok }, out(one));
    check(JSON.stringify(head(pl)) === JSON.stringify(['#', '歌曲', '時長', 'Spotify', 'YouTube Music', '']), `非 macOS 沒有 Apple 欄:${JSON.stringify(head(pl))}`);
    const sp = cellOf(rows(pl)[0], 'Spotify');
    check(buttons(sp).length === 0 && anchors(sp).length === 1, 'Spotify 沒登入:沒有 ▶,連結照給');
    // keychain_error 不是沒登入:▶ 照給、Apple 欄照出現,讓 CLI 把原因說出來;expired 才跟 missing 一樣拿掉(#122 review 第 2 點)
    ua(MAC);
    pl = await open({ spotify: { state: 'keychain_error' }, apple: { state: 'keychain_error' }, google: ok }, out(one));
    check(head(pl).includes('Apple Music') && buttons(cellOf(rows(pl)[0], 'Spotify')).length === 1, `keychain_error:▶ 照給、Apple 欄照出現:${JSON.stringify(head(pl))}`);
    pl = await open({ spotify: ok, apple: { state: 'expired' }, google: ok }, out(one));
    check(!head(pl).includes('Apple Music'), 'Apple 過期:整欄不出現');
    pl = await open({ spotify: ok, apple: { state: 'missing' }, google: ok }, out(one));
    check(!head(pl).includes('Apple Music'), 'Apple 沒登入:整欄不出現');
    // 平台欄:只連著、一首對應都沒有也出現(c3 只有 Apple 釘成「沒有」);沒連也沒對應的不出現(完整度稽核)
    pl = await open({ spotify: ok, apple: ok, youtube: ok, google: ok }, out({ ...one, 'pl__a.json': { ...one['pl__a.json'], links: { youtube: 'UCme/PLyt' }, items: [{ cid: 'c3' }] } }));
    check(JSON.stringify(head(pl)) === JSON.stringify(['#', '歌曲', '時長', 'YouTube Music', '']), `只連著沒有對應也出現:${JSON.stringify(head(pl))}`);
    // Apple 協作清單的資料庫列(a.)跟 i. 一樣不給 ▶(play --id 只收 catalog 的數字 id)
    pl = await open({ spotify: ok, apple: ok, google: ok }, out({ 'pl__b.json': { pid: 'b', name: 'collab', links: { apple: 'p.B' }, items: [{ cid: 'c5' }] }, 'tracks.json': { tracks: { c5: { title: 'Song E', mappings: { apple: { id: 'a.COL' } } } } } }));
    const ac = cellOf(rows(pl)[0], 'Apple Music');
    check(allByClass(ac, 'pl__only')[0]?.textContent === '只在資料庫' && buttons(ac).length === 0, 'Apple 的 a. id 也不給 ▶');
    // 讀不到 auth(命令失敗):不知道 = 照給 ▶,讓 CLI 說原因
    ua(MAC);
    reset();
    script = { 'auth status --json': { events: [{ type: 'exit', code: 1, message: 'x', reason: 'done' }] }, export: out(one) };
    pl = mk();
    initPlaylists(pl, api, con, () => {}, providers);
    await idle();
    check(buttons(cellOf(rows(pl)[0], 'Spotify')).length === 1 && head(pl).includes('Apple Music'), '讀不到 auth:▶ 照給');

    // ── 篩選:超過 20 首才有「在這份清單裡找歌」;只藏列、不重排;超過 8 份清單才有篩選清單
    const many = { 'tracks.json': { tracks } };
    for (let i = 0; i < 9; i++) many[`pl__p${i}.json`] = { pid: `p${i}`, name: i === 3 ? 'Mix Tape' : `list ${i}`, links: {}, items: [] };
    many['pl__p0.json'].items = [...Array(21).fill('c2'), 'c1', 'c2', 'c1', 'c1', 'c1'].map((cid) => ({ cid })); // c1 在第 22、24、25、26 首
    pl = await open({ spotify: ok, apple: ok, google: ok }, out(many));
    const [lf, sf] = allByClass(pl, 'pl__filter');
    check(lf && sf, '9 份清單有篩選清單、26 首有找歌');
    const q = (box, v) => { const i = box.querySelector('input'); i.value = v; i.l.input(); };
    q(sf, 'SONG a');
    const vis = rows(pl).filter((tr) => !tr.hidden);
    check(JSON.stringify(vis.map((tr) => [tr.dataset.cid, tr.children[0].textContent])) === JSON.stringify([['c1', '22'], ['c1', '24'], ['c1', '25'], ['c1', '26']]),
      `找歌不分大小寫、只藏列、位置照原本的順序:${JSON.stringify(vis.map((tr) => tr.children[0].textContent))}`);
    check(allByClass(pl, 'pl__shown')[0].textContent === '顯示 4 / 26 首', `顯示幾首:${allByClass(pl, 'pl__shown')[0].textContent}`);
    q(sf, '');
    check(rows(pl).every((tr) => !tr.hidden) && allByClass(pl, 'pl__shown')[0].textContent === '', '清掉就全部回來');
    q(lf, 'mix');
    const items = allByClass(pl, 'pl__item');
    check(items.filter((b) => !b.hidden).length === 1 && !items[3].hidden, '篩選清單只留名稱符合的那一份');

    // ── 空白態:還沒連 Google Drive / 連了但沒有清單
    pl = await open({ google: { state: 'missing' } }, { events: [{ type: 'exit', code: 1, message: 'x', reason: 'done' }] });
    const go = anchors(pl).find((a) => a.href === '#/account');
    check(pl.textContent.includes('先連接 Google Drive') && go && go.textContent === '到「帳號」', `沒連 Google Drive:指到帳號頁:${pl.textContent.slice(0, 200)}`);
    pl = await open({ google: ok }, { events: [{ type: 'exit', code: 1, message: 'x', reason: 'done' }] });
    const sync = anchors(pl).find((a) => a.href === '#/sync');
    check(pl.textContent.includes('這台電腦還沒讀過你 Google Drive 上的正本。到「同步」按「讀回正本(會順便讀已登入的平台)」就會讀進來') && sync && sync.textContent === '到「同步」' && !pl.textContent.includes('capy 還沒有替你保管任何清單'),
      `連了 Google Drive、這台沒有本機資料(第二台電腦):不說「沒有清單」,指到同步頁:${pl.textContent.slice(0, 200)}`);
    pl = await open({ google: ok }, out({}));
    check(pl.textContent.includes('capy 還沒有替你保管任何清單') && !anchors(pl).some((a) => a.href === '#/account' || a.href === '#/sync'), '讀過了、真的沒有清單:指到搬家');

    // Google 的 keychain_error 不是「沒連」:說讀不到登入資料、指到帳號頁(#122 review 第 2 點)
    pl = await open({ google: { state: 'keychain_error' } }, { events: [{ type: 'exit', code: 1, message: 'x', reason: 'done' }] });
    check(pl.textContent.includes('讀不到你的 Google Drive 登入資料(鑰匙圈出錯)') && !pl.textContent.includes('先連接') && anchors(pl).some((a) => a.href === '#/account'), `Google 鑰匙圈出錯:${pl.textContent.slice(0, 160)}`);
    // export 根本沒跑完(被別的分頁佔著 = refused)不是「這台沒有本機資料」:照說原因,不叫人去同步頁(#122 review 第 1 點)
    pl = await open({ google: ok }, { status: 409, error: 'another tab is running a command' });
    check(pl.textContent.includes('another tab is running a command') && !pl.textContent.includes('這台電腦還沒讀過') && !anchors(pl).some((a) => a.href === '#/sync'),
      `export 被拒:照說原因:${pl.textContent.slice(0, 160)}`);
    // ── 英文:整頁、報讀名稱、格子的說明都沒有中文
    i18nFile = './i18n-en.json';
    try {
      await loadI18n(api);
      pl = await open({ spotify: ok, apple: ok, youtube: ok, google: ok }, out(one));
      const CJK = /[　-〿㐀-鿿＀-￯]/;
      const all = [];
      walk(pl, (c) => all.push(c._text || '', c.title || '', c.dataset?.platform || '', ...Object.values(c.attrs || {})));
      const bad = all.filter((x) => CJK.test(x) || x.includes('webui.'));
      check(bad.length === 0, `英文目錄下清單頁不該有中文或沒送到的 key:${JSON.stringify([...new Set(bad)])}`);
      check(JSON.stringify(head(pl)) === JSON.stringify(['#', 'Song', 'Length', 'Spotify', 'Apple Music', 'YouTube Music', '']), `英文表頭:${JSON.stringify(head(pl))}`);
      const e0 = rows(pl)[0];
      check(anchors(cellOf(e0, 'Spotify'))[0]?.textContent === 'Listen on Spotify' && anchors(cellOf(e0, 'YouTube Music'))[0]?.textContent === 'Open', '英文的連結字');
    } finally {
      i18nFile = './i18n.json';
      await loadI18n(api);
    }
  } finally {
    if (nav) Object.defineProperty(globalThis, 'navigator', nav); else delete globalThis.navigator;
  }
});

// 8q. 我的清單 T2b(計畫 2026-09-30 §3.5):連結面板的每種狀態、同步這份清單(表在提示之上、收尾規則)、找對應 / 逐首決定、
//     在 X 建一份(按開始前零命令、--new-only、每一步在上一步的 onExit 裡送出——排隊的 con.idle 插不了隊、停在哪一步照實說、
//     Apple 的揭露只在 Apple)、重讀保留選中的那份、頁面送出的命令絕不帶 --yes / --force(決策 46)。用真的 Console。
await scenario('8q', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const T1 = '4uLU6hMCjMI75M1A2tKUQC', PL = '37i9dQZF1DXcBWIGoYBM5M';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'A') out.push(c); }); return out; };
  const byText = (n, text) => { let b = null; walk(n, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const idle = () => new Promise((r) => con.idle(r));
  const out = (o) => ({ events: [{ type: 'stdout', text: JSON.stringify(o) }, done] });
  const ok = { state: 'ok' };
  const SH = ['DIR', 'ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  const tbl = { type: 'table', header: SH, rows: [['push', 'add', 'spotify', 'road trip', '0', 'c1', T1, 'Song A', 'X', 'r', 'push']] };
  const exit = (code, message = '', reason = 'done') => ({ type: 'exit', code, message, reason });
  const tracks = {
    c1: { title: 'Song A', mappings: { spotify: { id: T1 }, apple: { id: '1440' }, youtube: { id: 'v1' } } },
    c2: { title: 'Song B', mappings: { apple: { id: '', pinned: true } } },
    c3: { title: 'Song C', mappings: { apple: { id: '1441' } } },
  };
  const data = (links, extra = {}) => ({
    'manifest.json': { devices: [{ id: 'devA', name: 'Mac mini' }, { id: 'devB', name: 'MacBook Air' }] },
    'pl__a.json': { pid: 'a', name: 'road trip', links, items: ['c1', 'c2', 'c3', 'c1'].map((cid) => ({ cid })) },
    'pl__b.json': { pid: 'b', name: 'mix', links: {}, items: [{ cid: 'c1' }] },
    'tracks.json': { tracks }, ...extra });
  const AUTH = { google: { state: 'ok', device_id: 'devA' }, spotify: ok, apple: ok, youtube: { state: 'ok', channel_id: 'UCme' } };
  const providers = { list: ['spotify', 'apple', 'local', 'youtube'], current: 'spotify' };
  let page = null;
  const open = async (links, auth = AUTH, more = {}) => {
    reset();
    script = { 'auth status --json': out(auth), export: out(data(links)), ...more };
    const pl = mk();
    page = initPlaylists(pl, api, con, (x) => notices.push(x), providers);
    await idle();
    return pl;
  };
  const row = (pl, p) => allByClass(pl, 'pl__link').find((r) => r.dataset.platform === p);
  const info = (r) => allByClass(r, 'pl__link-info')[0].textContent;
  const flowOf = (pl) => allByClass(pl, 'pl__flow')[0];
  const status = (pl) => allByClass(pl, 'pl__flow-status')[0]?.textContent || '';

  // ── 連結面板:四家、每種狀態
  let pl = await open({ spotify: PL, apple: 'p.A', local: 'devB/Music/road.m3u8', youtube: 'UCother/PLyt' });
  const states = allByClass(pl, 'pl__link').map((r) => [r.dataset.platform, r.dataset.state]);
  check(JSON.stringify(states) === JSON.stringify([['spotify', 'linked'], ['apple', 'linked'], ['local', 'foreign'], ['youtube', 'foreign']]), `面板的列與狀態:${JSON.stringify(states)}`);
  check(info(row(pl, 'spotify')) === '2 首還沒有對應,同步時會跳過' && 'warn' in allByClass(row(pl, 'spotify'), 'pl__link-info')[0].dataset, `缺對應(不重複計、標過沒有的不算):${info(row(pl, 'spotify'))}`);
  check(info(row(pl, 'apple')) === '全部都有對應' && !byText(row(pl, 'apple'), '找對應'), `Apple:標過「沒有這首」的不算缺,沒有找對應:${info(row(pl, 'apple'))}`);
  check(info(row(pl, 'local')) === '連在另一台電腦「MacBook Air」' && allByClass(row(pl, 'local'), 'btn').length === 0, `別台電腦的本機清單:說是哪一台、沒有動作:${info(row(pl, 'local'))}`);
  check(info(row(pl, 'youtube')) === '連在另一個 YouTube Music 帳號' && allByClass(row(pl, 'youtube'), 'btn').length === 0, `別的帳號:${info(row(pl, 'youtube'))}`);
  check(anchors(row(pl, 'spotify')).some((a) => a.href === `https://open.spotify.com/playlist/${PL}`), 'Spotify 那一列連到平台上的清單');

  // 找對應 / 逐首決定
  byText(row(pl, 'spotify'), '找對應').click();
  await idle();
  check(JSON.stringify(bodies.at(-3)?.args) === JSON.stringify(['resolve', 'a', '--provider', 'spotify']), `找對應:${JSON.stringify(bodies.map((b) => b.args || b.line))}`);
  byText(row(pl, 'spotify'), '逐首決定').click();
  await idle();
  check(JSON.stringify(bodies.at(-3)?.args) === JSON.stringify(['resolve', 'a', '--provider', 'spotify', '--review']), `逐首決定:${JSON.stringify(bodies.slice(-3).map((b) => b.args || b.line))}`);

  // 沒連、有登入 / 沒登入;連著、沒登入
  pl = await open({ spotify: PL }, { ...AUTH, spotify: { state: 'missing' }, youtube: { state: 'missing' } });
  check(row(pl, 'spotify').dataset.state === 'out' && info(row(pl, 'spotify')) === '這台電腦沒有登入 Spotify' && anchors(row(pl, 'spotify')).some((a) => a.href === '#/account')
    && anchors(row(pl, 'spotify')).some((a) => a.href.includes('/playlist/')), '連著、這台沒登入:指到帳號頁,平台清單的連結照給');
  check(row(pl, 'youtube').dataset.state === 'unlinked_out' && info(row(pl, 'youtube')) === '這台電腦沒有登入 YouTube Music' && !byText(row(pl, 'youtube'), '在 YouTube Music 建一份'), '沒連、沒登入:不給建一份');
  check(row(pl, 'apple').dataset.state === 'unlinked' && byText(row(pl, 'apple'), '在 Apple Music 建一份') && !row(pl, 'local'), '沒連、有登入:給建一份;沒連的本機不出現');
  check(info(row(pl, 'apple')) === '已經知道其中 2 首在 Apple Music 是哪一首', `已經知道的對應(全域共用):${info(row(pl, 'apple'))}`);
  pl = await open({ youtube: 'UCme/PLmine' }, { ...AUTH, youtube: { state: 'ok' } }); // 登入了卻沒有 channel_id(舊版登入):請人重新登入,不說成沒登入、也不算別的帳號
  check(row(pl, 'youtube').dataset.state === 'out' && info(row(pl, 'youtube')) === '這台電腦的 YouTube Music 登入少了帳號資料;到「帳號」重新登入一次', `YouTube 沒有 channel_id:${info(row(pl, 'youtube'))}`);

  // ── 同步這份清單:沒有任何連結時停用;有的話表在提示之上、答了才寫、收尾照規則說,重讀後說明還在
  pl = await open({});
  const syncOff = byText(pl, '同步這份清單');
  check(syncOff && syncOff.disabled === true && pl.textContent.includes('先連一個平台'), '沒有任何連結:同步鈕停用並說原因');
  const [g, release] = gate();
  pl = await open({ spotify: PL }, AUTH, {
    'pl sync a': { first: [tbl, { type: 'prompt', id: 1, kind: 'confirm', title: '套用以上 1 筆變更?', affirmative: '套用', negative: '取消', default: false }], gate: g,
      events: [{ type: 'prompt_closed', id: 1, reason: 'answered' }, done] },
    onAnswer: release,
  });
  const blocks0 = allByClass(root, 'block').length; // 主控台(root)跨情境共用:比新增的區塊數
  byText(pl, '同步這份清單').click();
  await tick(20);
  const f = flowOf(pl);
  const [tWrap, host] = [allByClass(f, 'pl__flow-table')[0], allByClass(f, 'pl__flow-prompts')[0]];
  const box = allByClass(host, 'prompt')[0];
  check(JSON.stringify(bodies.at(-1)) === JSON.stringify({ args: ['pl', 'sync', 'a'] }), `同步送出的命令:${JSON.stringify(bodies.at(-1))}`);
  check(allByClass(tWrap, 'tbl').length === 1 && box && f.children.indexOf(tWrap) < f.children.indexOf(host) && allByClass(pl, 'pl__right')[0].contains(host),
    '變更表畫在頁面上、在提示之上;提示在清單頁裡(不跳主控台)');
  check(globalThis.location.hash === '', '提示在清單頁:不切到主控台');
  byText(box, '套用').click();
  await idle(); await tick(5); await idle();
  check(answers.length === 1 && answers[0].value === true, '確認是使用者按的,頁面不替人回答');
  check(status(pl) === '完成。' && calls.slice(-2).join() === 'auth status --json,export', `完成之後重讀、收尾那句留著:${status(pl)} ${calls.slice(-3)}`);
  check(allByClass(root, 'block').length === blocks0 + 1, `寫入收尾後的重讀用 quiet:主控台只多 pl sync 那一塊:${allByClass(root, 'block').length - blocks0}`);
  // 收尾規則的其餘幾種(每次都是新的同步)
  const closing = async (spec, want, not = []) => {
    script['pl sync a'] = spec;
    byText(pl, '同步這份清單').click();
    await idle(); await tick(5); await idle();
    const got = status(pl);
    check(got.includes(want) && not.every((x) => !got.includes(x)), `收尾:要有「${want}」:${got}`);
  };
  await closing({ events: [done] }, '已經是最新的,沒有要改的東西。');
  const skipOnly = { type: 'table', header: SH, rows: [['push', 'skip', 'spotify', 'road trip', '1', 'c2', '', 'Song B', '', 'r', 'no_mapping']] };
  await closing({ events: [skipOnly, done] }, '已經是最新的,沒有要改的東西。', ['完成']); // 有表、沒有答過確認 = 沒有寫入
  await closing({ events: [tbl, exit(2, 'Error: 待套用')] }, '你按了取消,沒有寫入。', ['待套用']);
  await closing({ first: [tbl, { type: 'prompt', id: 2, kind: 'confirm', title: '?' }], events: [{ type: 'prompt_closed', id: 2, reason: 'dismissed' }, exit(1, 'Error: user aborted')] },
    '你關掉了提示,沒有寫入。', ['user aborted']);
  await closing({ first: [{ type: 'prompt', id: 3, kind: 'confirm', title: '?' }], events: [{ type: 'prompt_closed', id: 3, reason: 'timeout' }, exit(1, 'Error: user aborted', 'timeout')] },
    '等太久沒有回答,沒有寫入。', ['user aborted']);
  await closing({ events: [tbl, exit(3, 'Error: 這次會刪掉 12 首,超過閾值')] }, '這次會刪掉 12 首,超過閾值 capy 先停下來了,這一頁不會替你越過', ['Error:', '--force']);
  await closing({ first: [tbl], events: [exit(130, '', 'cancelled')] }, '已中止。中止前已經寫入的不會撤回', ['失敗', '主控台']);
  await closing({ events: [exit(1, 'Error: 讀不到 Drive')] }, '讀不到 Drive');
  check(anchors(flowOf(pl)).some((a) => a.href === '#/console'), '其他失敗:連到主控台');
  // resolve 沒有自動對上、只剩等人決定的列:指到頁面上的「逐首決定」;表的註記只算會寫入的(review / conflict 不算)
  const RH = ['ACTION', 'CID', 'PROVIDER', 'PROVIDER_ID', 'CONFIDENCE', 'SOURCE', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  pl = await open({ spotify: PL }, AUTH, { 'resolve a --provider spotify': { events: [{ type: 'table', header: RH, rows: [['review', 'c2', 'spotify', T1, '70', 'fuzzy', 'Song B', '', 'r', 'low_score'], ['conflict', 'c3', 'spotify', '', '', '', 'Song C', '', 'r', 'conflict']] }, done] } });
  byText(row(pl, 'spotify'), '找對應').click();
  await idle(); await tick(5); await idle();
  check(status(pl) === '沒有自動對上的;有 2 首要你決定。按「逐首決定」。', `resolve 只剩待決定的:${status(pl)}`);
  const note = allByClass(allByClass(pl, 'pl__flow-table')[0], 'page__note')[0]?.textContent;
  check(note === '0 筆會寫入的對應(其餘等你逐首決定)', `resolve 表的註記不算 review / conflict:${note}`);

  // 重讀保留篩選字與捲動位置(同一份重畫時)
  pl = await open({ spotify: PL }, AUTH);
  script.export = out(data({ spotify: PL }, { 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: PL }, items: Array.from({ length: 25 }, (_, i) => ({ cid: i === 7 ? 'c3' : 'c1' })) } }));
  page.refresh();
  await idle(); await tick(5); await idle();
  const fbox = allByClass(pl, 'pl__filter')[0].querySelector('input');
  fbox.value = 'song c';
  fbox.l.input();
  allByClass(pl, 'pl__songs')[0].scrollTop = 120;
  page.refresh();
  await idle(); await tick(5); await idle();
  const fbox2 = allByClass(pl, 'pl__filter')[0].querySelector('input');
  const shownRows = allByClass(pl, 'pl__tbl')[0].querySelectorAll('tbody tr').filter((tr) => !tr.hidden);
  check(fbox2 !== fbox && fbox2.value === 'song c' && shownRows.length === 1 && allByClass(pl, 'pl__songs')[0].scrollTop === 120,
    `重讀之後篩選字與捲動位置都帶回來:${JSON.stringify([fbox2 !== fbox, fbox2.value, shownRows.length, allByClass(pl, 'pl__songs')[0].scrollTop, allByClass(pl, 'pl__tbl')[0].querySelectorAll('tbody tr').length])}`);

  // 重讀保留選中的那份
  allByClass(pl, 'pl__item')[1].click();
  page.refresh();
  await idle(); await tick(5); await idle();
  check(allByClass(pl, 'pl__item')[1].classList.contains('is-active') && calls.slice(-2).join() === 'auth status --json,export', '回到頁面重讀:選中的那份還是那份');
  // 回到頁面的重讀:用 quiet(主控台不多區塊);有命令佔著序列槽時排在它後面(con.idle),不撞序列槽
  const [g6, r6] = gate();
  script.noop = { gate: g6, events: [done] };
  con.run('noop');
  await tick(10);
  const c6 = calls.length;
  const blocks6 = allByClass(root, 'block').length;
  page.refresh();
  await tick(10);
  check(!calls.slice(c6).includes('export'), `有命令佔著序列槽時,回到頁面的重讀要等它:${calls.slice(c6)}`);
  r6();
  await idle(); await tick(5); await idle();
  check(calls.slice(-2).join() === 'auth status --json,export' && allByClass(root, 'block').length === blocks6, `佔槽的命令結束後才重讀、主控台不多區塊:${calls.slice(-3)} ${allByClass(root, 'block').length - blocks6}`);

  // ── 在 YouTube Music 建一份:說明 → 開始 → 三步;第二步進行中排一個 con.idle,第三步照樣送出
  const [g2, release2] = gate();
  pl = await open({ spotify: PL }, AUTH, {
    'pl link --new-only a youtube --create': { events: [{ type: 'stdout', text: '已連結\n' }, done] },
    'resolve a --provider youtube': { first: [tbl, { type: 'prompt', id: 5, kind: 'confirm', title: '寫入以上 1 筆 mapping 到 Drive?' }], gate: g2,
      events: [{ type: 'prompt_closed', id: 5, reason: 'answered' }, done] },
    'pl sync a --provider youtube': { events: [tbl, { type: 'prompt', id: 6, kind: 'confirm', title: '?' }, { type: 'prompt_closed', id: 6, reason: 'answered' }, done] }, // 真的寫入前一定會問
    'pl list --provider youtube': { events: [{ type: 'table', header: ['ID', 'NAME', 'TRACKS', 'OWNER'], rows: [['PLx', 'other', '3', '']] }, done] },
    onAnswer: release2,
  });
  const before = bodies.length;
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(唯讀):看得到同名、有歌的才能改給「連到那一份」
  const card = allByClass(pl, 'pl__flow-card')[0];
  check(card && card.textContent.includes('會先在 YouTube Music 建一份叫「road trip」的空清單並連上') && !card.textContent.includes('Apple Music 資料庫')
    && JSON.stringify(bodies.slice(before).map((b) => (b.args || [b.line]).join(' '))) === JSON.stringify(['pl list --provider youtube']),
    `按「開始」前:只有說明、只讀了那個平台的清單、沒有寫入命令:${card && card.textContent}`);
  byText(card, '開始').click();
  await tick(20);
  let waited = false;
  con.idle(() => { waited = true; con.run('noop'); }); // 第二步在等確認時排進來的自動讀取(真的會跑命令:插得了隊,第三步就會撞閘)
  const box5 = allByClass(allByClass(pl, 'pl__flow-prompts')[0], 'prompt')[0];
  byText(box5, '確定') ? byText(box5, '確定').click() : box5.querySelector('.prompt__row').children[0].click();
  await idle(); await tick(10); await idle(); await tick(10); await idle();
  const sent = bodies.slice(before).map((b) => (b.args || [b.line]).join(' ')).filter((c) => c !== 'pl list --provider youtube');
  check(JSON.stringify(sent.slice(0, 3)) === JSON.stringify(['pl link --new-only a youtube --create', 'resolve a --provider youtube', 'pl sync a --provider youtube']),
    `三步依序、第一步帶 --new-only、排隊的讀取插不了隊:${JSON.stringify(sent)}`);
  check(waited, '排隊的讀取在流程之後照樣會跑');
  check(status(pl) === '完成:YouTube Music 上的「road trip」已經照正本的順序加好歌。', `三步都完成:${status(pl)}`);
  const steps = allByClass(pl, 'wiz__step').map((li) => li.dataset.state);
  check(JSON.stringify(steps) === JSON.stringify(['done', 'done', 'done']), `步驟條:${JSON.stringify(steps)}`);
  // 第一步失敗:後兩步不送;第二步取消:第三步不送,說停在哪
  pl = await open({ spotify: PL }, AUTH, { 'pl link --new-only a youtube --create': { events: [exit(1, 'Error: youtube 上已經有叫「road trip」的清單(PL1),而且不是空的')] } });
  const b1 = bodies.length;
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(看同名)才畫說明卡
  byText(allByClass(pl, 'pl__flow-card')[0], '開始').click();
  await idle(); await tick(5); await idle();
  check(bodies.slice(b1).filter((b) => b.args && b.args[0] !== 'auth' && b.args.join(' ') !== 'pl list --provider youtube').length === 1 && status(pl).startsWith('第一步沒有完成。 youtube 上已經有叫「road trip」的清單'), `第一步失敗:${status(pl)}`);
  pl = await open({ spotify: PL }, AUTH, {
    'pl link --new-only a youtube --create': { events: [done] },
    'resolve a --provider youtube': { events: [tbl, exit(2, 'Error: 待套用')] },
  });
  const b2 = bodies.length;
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(看同名)才畫說明卡
  byText(allByClass(pl, 'pl__flow-card')[0], '開始').click();
  await idle(); await tick(5); await idle(); await tick(5); await idle();
  check(!bodies.slice(b2).some((b) => b.args && b.args[0] === 'pl' && b.args[1] === 'sync') && status(pl) === '已經在 YouTube Music 建立並連上「road trip」;按「找對應」接著找對應。 你按了取消,沒有寫入。',
    `第二步取消:第三步不送、說停在哪:${status(pl)}`);
  pl = await open({ spotify: PL }, AUTH, {
    'pl link --new-only a youtube --create': { events: [done] },
    'resolve a --provider youtube': { events: [done] },            // 沒有新對應:沒有寫入
    'pl sync a --provider youtube': { events: [skipOnly, done] },  // 全部 skip:一首都沒加
  });
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(看同名)才畫說明卡
  byText(allByClass(pl, 'pl__flow-card')[0], '開始').click();
  await idle(); await tick(5); await idle(); await tick(5); await idle(); await tick(5); await idle();
  check(status(pl) === '已經在 YouTube Music 建立並連上「road trip」,但這次沒有加任何歌(還沒有找到對應)。按「逐首決定」接著處理。', `一首都沒加:照實說:${status(pl)}`);
  pl = await open({ spotify: PL }, AUTH, {
    'pl link --new-only a youtube --create': { events: [done] },
    'resolve a --provider youtube': { events: [done] },
    'pl sync a --provider youtube': { events: [exit(1, 'Error: 讀不到 youtube')] },
  });
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(看同名)才畫說明卡
  byText(allByClass(pl, 'pl__flow-card')[0], '開始').click();
  await idle(); await tick(5); await idle(); await tick(5); await idle(); await tick(5); await idle();
  check(status(pl).startsWith('已經在 YouTube Music 建立並連上「road trip」;按「同步這份清單」把歌加進去。') && !status(pl).includes('對應已經寫入') && !status(pl).includes('找對應'),
    `第二步沒寫入時第三步失敗:指到同步這份清單、不說「對應已經寫入」(對應早就都有時「找對應」那顆鈕根本不在):${status(pl)}`);
  // 第二步被拒、之後的重讀也被拒:畫面與「停在哪一步」那句都留著
  pl = await open({ spotify: PL }, AUTH, {
    'pl link --new-only a youtube --create': { events: [done] },
    'resolve a --provider youtube': { status: 409, error: 'another tab' },
  });
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(看同名)才畫說明卡
  script['auth status --json'] = { status: 409, error: 'another tab' };
  script.export = { status: 409, error: 'another tab' };
  byText(allByClass(pl, 'pl__flow-card')[0], '開始').click();
  await idle(); await tick(5); await idle(); await tick(5); await idle();
  check(status(pl).startsWith('已經在 YouTube Music 建立並連上「road trip」;按「找對應」接著找對應。') && allByClass(pl, 'pl__item').length === 2 && allByClass(allByClass(pl, 'pl__right')[0], 'pl__flow').length === 1,
    `重讀失敗:不清畫面、說明留著:${status(pl)}`);

  // ── 流程在跑時(提示開著),換一份清單與「在 X 建一份」擋下:提示還在頁面裡,答了照常收尾(提示畫在流程區裡,清掉就卡住)
  const [g3, release3] = gate();
  pl = await open({ spotify: PL }, AUTH, {
    'pl sync a': { first: [tbl, { type: 'prompt', id: 9, kind: 'confirm', title: '套用以上 1 筆變更?', affirmative: '套用', negative: '取消', default: false }], gate: g3,
      events: [{ type: 'prompt_closed', id: 9, reason: 'answered' }, { type: 'prompt', id: 10, kind: 'confirm', title: 'x' }, { type: 'prompt_closed', id: 10, reason: 'answered' }, done] },
    onAnswer: release3,
  });
  byText(pl, '同步這份清單').click();
  await tick(20);
  const evBefore = (globalThis.document.events || []).length;
  allByClass(pl, 'pl__item')[1].click();
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  const open9 = () => allByClass(pl, 'prompt').filter((b) => !b.classList.contains('is-closed'));
  check(allByClass(pl, 'pl__item')[0].classList.contains('is-active') && open9().length === 1 && allByClass(pl, 'pl__flow-card').length === 0
    && (globalThis.document.events || []).slice(evBefore).filter((e) => e === 'capy:busy').length === 2, '流程在跑:換清單與建一份都擋下、說明,提示還在');
  byText(open9()[0], '套用').click();
  await idle(); await tick(5); await idle();
  check(status(pl) === '完成。', `擋下之後照常收尾:${status(pl)}`);
  // 建一份的第一步進行中換清單:擋下;第二步的提示畫在頁面裡的流程區(不是脫離頁面的容器)
  const [g4, release4] = gate();
  const [g5, release5] = gate();
  pl = await open({ spotify: PL }, AUTH, {
    'pl link --new-only a youtube --create': { gate: g4, events: [done] },
    'resolve a --provider youtube': { first: [tbl, { type: 'prompt', id: 11, kind: 'confirm', title: '?' }], gate: g5,
      events: [{ type: 'prompt_closed', id: 11, reason: 'cancelled' }, exit(1, '', 'cancelled')] },
  });
  byText(row(pl, 'youtube'), '在 YouTube Music 建一份').click();
  await idle(); await tick(5); await idle(); // 先讀 YouTube 上的清單(看同名)才畫說明卡
  byText(allByClass(pl, 'pl__flow-card')[0], '開始').click();
  await tick(10);
  allByClass(pl, 'pl__item')[1].click();
  release4();
  await tick(30);
  check(allByClass(pl, 'pl__item')[0].classList.contains('is-active') && allByClass(pl, 'pl__flow-prompts').includes(con.promptHost) && open9().length === 1,
    '第一步進行中換清單被擋;第二步的提示在頁面裡');
  release5();
  await idle(); await tick(5); await idle();

  // Apple 的揭露只在 Apple;取消說明零命令
  pl = await open({ spotify: PL });
  byText(row(pl, 'apple'), '在 Apple Music 建一份').click();
  await idle(); await tick(5); await idle();
  const b3 = bodies.length;
  check(allByClass(pl, 'pl__flow-card')[0].textContent.includes('加進清單的歌可能也會進你的 Apple Music 資料庫(看你的 Apple Music 設定)。'), 'Apple 的揭露(決策 49:可能,不是必然)');
  byText(allByClass(pl, 'pl__flow-card')[0], '取消').click();
  check(allByClass(pl, 'pl__flow-card').length === 0 && bodies.length === b3, '取消說明:零命令');
  check(bodies.every((b) => !(b.args || [b.line]).some((a) => a === '--yes' || a === '--force')), '頁面送出的命令絕不帶 --yes / --force');

  // ── 英文:面板與建一份的說明沒有中文
  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    pl = await open({ spotify: PL, local: 'devB/x.m3u8' }, { ...AUTH, youtube: { state: 'missing' } });
    byText(row(pl, 'apple'), 'Create one on Apple Music').click();
    await idle(); await tick(5); await idle();
    const CJK = /[　-〿㐀-鿿＀-￯]/;
    const all = [];
    walk(pl, (c) => all.push(c._text || '', ...Object.values(c.attrs || {})));
    const bad = all.filter((x) => CJK.test(x) || x.includes('webui.'));
    check(bad.length === 0, `英文目錄下連結面板與說明不該有中文:${JSON.stringify([...new Set(bad)])}`);
    check(info(row(pl, 'local')) === 'Linked on another computer ("MacBook Air")' && pl.textContent.includes('Master copy in Google Drive'), '英文的面板');
    // 英文下把流程真的跑一次(計畫 §4 情境 14):執行狀態列的 label 是英文進行式、流程區的收尾句沒有中文
    const labels = [];
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label || ''); return Console.prototype.run.call(con, line, hooks, opts); };
    try {
      pl = await open({ spotify: PL }, AUTH, { 'pl sync a': { events: [done] }, 'resolve a --provider spotify': { events: [done] }, 'resolve a --provider spotify --review': { events: [done] },
        'pl link --new-only a youtube --create': { events: [done] }, 'resolve a --provider youtube': { events: [done] }, 'pl sync a --provider youtube': { events: [exit(130, '', 'cancelled')] } });
      const settle = async () => { await idle(); await tick(5); await idle(); };
      byText(pl, 'Sync this playlist').click(); await settle();
      byText(row(pl, 'spotify'), 'Find matches').click(); await settle();
      byText(row(pl, 'spotify'), 'Review one by one').click(); await settle();
      byText(row(pl, 'youtube'), 'Create one on YouTube Music').click(); await settle();
      byText(allByClass(pl, 'pl__flow-card')[0], 'Start').click(); await settle(); await settle();
      const flowText = [];
      walk(flowOf(pl), (c) => flowText.push(c._text || '', ...Object.values(c.attrs || {})));
      check(flowText.every((x) => !CJK.test(x) && !x.includes('webui.')) && flowOf(pl).textContent.includes('Stopped'), `英文的流程收尾句:${flowOf(pl).textContent.slice(0, 200)}`);
    } finally {
      delete con.run;
    }
    const want = ['Syncing', 'Matching', 'Reviewing', 'Creating', 'Adding songs'];
    check(want.every((w) => labels.some((l) => l.startsWith(w))) && labels.every((l) => !CJK.test(l) && /^[A-Z][a-z]*ing /.test(l)), `英文的 label 都是進行式、沒有中文:${JSON.stringify(labels)}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 8r. 我的清單 T2c(計畫 2026-09-30 §3.4 / §3.5「納入」):各平台上的清單只讀這台有登入的平台、依序讀(一家失敗其他家照讀);
//     每一列標出連著哪份正本 / 有同名的正本(不分大小寫,指到搬家)/ 還沒納入(可以納入);納入 = pl link --new-only -- 名稱 → pl pull -- 名稱,
//     停在哪一步照實說;讀過之後「在 X 建一份」撞到同名的平台清單不給「開始」。
await scenario('8r', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const PL = '37i9dQZF1DXcBWIGoYBM5M', SP2 = '7qiZfU4dY1lWllzX7mPBI3';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'A') out.push(c); }); return out; };
  const byText = (n, text) => { let b = null; walk(n, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const idle = async () => { for (let i = 0; i < 4; i++) { await new Promise((r) => con.idle(r)); await tick(5); } };
  const out = (o) => ({ events: [{ type: 'stdout', text: JSON.stringify(o) }, done] });
  const LH = ['ID', 'NAME', 'TRACKS', 'OWNER'];
  const list = (rows) => ({ events: [{ type: 'table', header: LH, rows }, done] });
  const exit = (code, message = '', reason = 'done') => ({ type: 'exit', code, message, reason });
  const tbl = { type: 'table', header: ['ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'], rows: [['add', 'spotify', 'Chill', '0', 'c1', 'x', 'Song A', 'X', 'r', 'added_on_platform']] };
  const answered = [tbl, { type: 'prompt', id: 1, kind: 'confirm', title: '?' }, { type: 'prompt_closed', id: 1, reason: 'answered' }, done];
  const data = { 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: PL }, items: [] }, 'pl__b.json': { pid: 'b', name: 'mix', links: {}, items: [] }, 'tracks.json': { tracks: {} } };
  const AUTH = { google: { state: 'ok' }, spotify: { state: 'ok' }, apple: { state: 'missing' }, youtube: { state: 'ok', channel_id: 'UCme' } };
  const providers = { list: ['spotify', 'apple', 'local', 'youtube'], current: 'spotify' };
  const open = async (auth, more = {}) => {
    reset();
    script = { 'auth status --json': out(auth), export: out(data), ...more };
    const pl = mk();
    initPlaylists(pl, api, con, (x) => notices.push(x), providers);
    await idle();
    return pl;
  };
  const read = async (pl) => { byText(pl, '讀取各平台的清單').click(); await idle(); };
  const prow = (pl, name) => allByClass(pl, 'pl__plat').find((r) => allByClass(r, 'pl__name')[0]?.textContent === name);
  const status = (pl) => allByClass(allByClass(pl, 'pl__plats')[0], 'pl__flow-status')[0]?.textContent || '';
  const spRows = [[PL, 'road trip', '2', 'me'], [SP2, 'Chill', '5', 'me'], ['sp4', 'MIX', '-', 'me'], ['sp5', '-dash-', '1', 'me']];

  // 只讀有登入的(Apple 沒登入),依序;YouTube 讀不到,Spotify 照畫
  let pl = await open(AUTH, { 'pl list --provider spotify': list(spRows), 'pl list --provider youtube': { events: [exit(1, 'Error: 讀不到 YouTube')] } });
  check(allByClass(pl, 'pl__plat').length === 0 && !calls.some((c) => c.startsWith('pl list')), '按了才讀');
  await read(pl);
  check(JSON.stringify(calls.filter((c) => c.startsWith('pl list'))) === JSON.stringify(['pl list --provider spotify', 'pl list --provider youtube']), `只讀有登入的平台、依序:${JSON.stringify(calls)}`);
  check(pl.textContent.includes('讀不到 YouTube Music 上的清單:讀不到 YouTube'), 'YouTube 讀不到:那一段說原因');
  check(prow(pl, 'road trip').dataset.state === 'linked' && prow(pl, 'road trip').textContent.includes('連著正本「road trip」'), '連著正本的列');
  check(pl.textContent.includes('本機曲庫不在這裡,M3U 請到主控台用 pl link 連。'), '各平台上的清單的說明:本機曲庫不在這裡、指到主控台的 pl link');
  check(prow(pl, 'MIX').dataset.state === 'same_name' && !byText(prow(pl, 'MIX'), '納入 capy') && !anchors(prow(pl, 'MIX')).some((a) => a.href === '#/move') && prow(pl, 'MIX').textContent.includes('先在 Spotify 上把這份改名') && byText(prow(pl, 'MIX'), '連到正本「mix」'), '只差大小寫也算同名:不給納入、不指去搬家、給連到正本或先改名');
  check(prow(pl, 'Chill').dataset.state === 'unlinked' && prow(pl, 'Chill').textContent.includes('5 首 · 還沒納入') && byText(prow(pl, 'Chill'), '納入 capy'), '還沒納入:給納入、說有幾首');
  check(anchors(prow(pl, 'Chill')).some((a) => a.href === `https://open.spotify.com/playlist/${SP2}`), 'Spotify 的列連回清單(S7)');

  // 納入:兩個命令、名稱放在 -- 後面;完成照實說,重讀後那一列變成連著
  script['pl link --new-only -- Chill spotify:' + SP2] = { events: [done] };
  script['pl pull -- Chill'] = { events: answered };
  script.export = out({ ...data, 'pl__c.json': { pid: 'c', name: 'Chill', links: { spotify: SP2 }, items: [] } }); // 納入之後重讀到的正本
  let before = bodies.length;
  byText(prow(pl, 'Chill'), '納入 capy').click();
  await idle();
  const sent = bodies.slice(before).filter((b) => b.args && b.args[0] === 'pl').map((b) => b.args);
  check(JSON.stringify(sent) === JSON.stringify([['pl', 'link', '--new-only', '--', 'Chill', 'spotify:' + SP2], ['pl', 'pull', '--', 'Chill']]), `納入的兩個命令:${JSON.stringify(sent)}`);
  check(status(pl) === '完成:「Chill」已經交給 capy 保管,正本照 Spotify 上的順序。', `納入完成:${status(pl)}`);
  const pulledAct = allByClass(allByClass(pl, 'pl__plats')[0], 'chg__act')[0];
  check(pulledAct?.textContent === '加進正本(從 Spotify)' && pulledAct.parentNode.dataset.dir === 'pull', `pl pull 的表沒有 DIR:從命令推出是 pull:${pulledAct && pulledAct.textContent}`);
  check(allByClass(pl, 'pl__plats')[0].contains(allByClass(pl, 'pl__flow-prompts')[0]), '納入的提示在頁面裡(各平台上的清單那一段)');
  check(prow(pl, 'Chill').dataset.state === 'linked' && prow(pl, 'Chill').textContent.includes('連著正本「Chill」') && allByClass(pl, 'pl__item').length === 3,
    '納入之後重讀:那一列變成連著正本、左欄多一份');
  // 名稱以 - 開頭:照樣在 -- 後面,不會被當成旗標
  script['pl link --new-only -- -dash- spotify:sp5'] = { events: [done] };
  script['pl pull -- -dash-'] = { events: [exit(2, 'Error: 待套用')] };
  before = bodies.length;
  byText(prow(pl, '-dash-'), '納入 capy').click();
  await idle();
  check(JSON.stringify(bodies.slice(before).filter((b) => b.args && b.args[0] === 'pl').map((b) => b.args)[0]) === JSON.stringify(['pl', 'link', '--new-only', '--', '-dash-', 'spotify:sp5']), '以 - 開頭的名稱放在 -- 後面');
  check(status(pl) === '已經建立並連上一份空的正本「-dash-」;選它、按「同步這份清單」把歌拉進來。 你按了取消,沒有寫入。', `停在第二步(不說方位:窄版時左欄在上面):${status(pl)}`);
  check((globalThis.scrolled || []).includes(allByClass(pl, 'pl__plats')[0].children.find((c) => c.classList.contains('pl__flow'))), '按了納入:畫面帶到流程區(它在整張平台清單上面)');
  // 第一步被擋(例如 --new-only 撞到別台剛建的同名正本):第二步不送
  pl = await open(AUTH, { 'pl list --provider spotify': list(spRows), 'pl list --provider youtube': list([]), ['pl link --new-only -- Chill spotify:' + SP2]: { events: [exit(1, 'Error: 已經有叫「Chill」的 canonical 清單')] } });
  await read(pl);
  byText(prow(pl, 'Chill'), '納入 capy').click();
  await idle();
  check(!calls.includes('pl pull -- Chill') && status(pl).startsWith('沒有納入。 已經有叫「Chill」的 canonical 清單') && anchors(allByClass(pl, 'pl__plats')[0]).some((x) => x.href === '#/console'), `第一步被擋:${status(pl)}`);
  check(pl.textContent.includes('YouTube Music 上沒有清單。'), '平台上沒有清單:照說');

  // 讀過之後,「在 Spotify 建一份」撞到同名(只差大小寫)的平台清單:不給開始,指到搬家
  allByClass(pl, 'pl__item')[1].click(); // mix:沒連 Spotify
  const sp = allByClass(pl, 'pl__link').find((r) => r.dataset.platform === 'spotify');
  byText(sp, '在 Spotify 建一份').click();
  const card = allByClass(pl, 'pl__flow-card')[0];
  check(card && card.textContent.includes('Spotify 上已經有叫「mix」的清單,capy 不會再建一份同名的') && !byText(card, '開始') && !anchors(card).some((a) => a.href === '#/move') && card.textContent.includes('先在 Spotify 上把那份改名'), `撞同名不給開始、不指去搬家:${card && card.textContent}`);

  // 第一家讀不到:其他家照讀、照畫
  pl = await open(AUTH, { 'pl list --provider spotify': { events: [exit(1, 'Error: 讀不到 Spotify')] }, 'pl list --provider youtube': list([['UCme/PL1', 'Drive', '3', 'me']]) });
  await read(pl);
  check(pl.textContent.includes('讀不到 Spotify 上的清單:讀不到 Spotify') && prow(pl, 'Drive')?.dataset.state === 'unlinked', '第一家讀不到,後面的平台照讀照畫');

  // 使用者按了中止:後面幾家不讀
  pl = await open(AUTH, { 'pl list --provider spotify': { events: [exit(130, '', 'cancelled')] }, 'pl list --provider youtube': list([['UCme/PL1', 'Drive', '3', 'me']]) });
  await read(pl);
  check(!calls.includes('pl list --provider youtube') && !pl.textContent.includes('這台電腦沒有登入任何平台'), `中止之後不接著讀下一家:${JSON.stringify(calls)}`);
  const ytGroup = allByClass(pl, 'pl__plat-group').find((g) => allByClass(g, 'pl__plat-name')[0]?.textContent === 'YouTube Music');
  check(ytGroup && ytGroup.textContent.includes('你按了中止,沒有讀。') && !ytGroup.textContent.includes('上沒有清單'), `沒讀的那家照列、說沒讀(不是「沒有清單」):${ytGroup && ytGroup.textContent}`);

  // 這台還沒讀到正本(export 失敗):只列出來,不標狀態也不給納入;Spotify 的清單連結照給
  pl = await open(AUTH, { export: { events: [exit(1, 'x')] }, 'pl list --provider spotify': list(spRows), 'pl list --provider youtube': list([]) });
  await read(pl);
  const unk = prow(pl, 'Chill');
  check(unk && !byText(unk, '納入 capy') && !unk.textContent.includes('還沒納入') && !('state' in unk.dataset) && anchors(unk).some((a) => a.href.includes('/playlist/'))
    && pl.textContent.includes('這台電腦還沒讀到正本,分不出這些清單是不是已經交給 capy 了'), `沒讀到正本:不標、不給納入:${unk && unk.textContent}`);

  // YouTube 有登入卻缺帳號資料:不讀、那一段說要重新登入;只有它的話也不說「沒有登入任何平台」
  pl = await open({ google: { state: 'ok' }, spotify: { state: 'missing' }, apple: { state: 'missing' }, youtube: { state: 'ok' } });
  await read(pl);
  check(!calls.some((c) => c.startsWith('pl list')) && pl.textContent.includes('這台電腦的 YouTube Music 登入少了帳號資料') && !pl.textContent.includes('這台電腦沒有登入任何平台'), `YouTube 缺帳號資料:${pl.textContent.slice(-200)}`);

  // 要重新登入的 YouTube 先記下、Spotify 後讀:段落還是照 Spotify → YouTube 的順序
  pl = await open({ google: { state: 'ok' }, spotify: { state: 'ok' }, apple: { state: 'missing' }, youtube: { state: 'ok' } }, { 'pl list --provider spotify': list(spRows) });
  await read(pl);
  const order = allByClass(pl, 'pl__plat-name').map((h) => h.textContent);
  check(JSON.stringify(order) === JSON.stringify(['Spotify', 'YouTube Music']), `平台段落照固定順序:${JSON.stringify(order)}`);

  // 同名的平台清單確定是空的(曲數 0):照給「開始」,讓 CLI 停在第一步、給接回去的 pl link(唯一的復原路)
  pl = await open(AUTH, { 'pl list --provider spotify': list([['sp6', 'MIX', '0', 'me']]), 'pl list --provider youtube': list([]) });
  await read(pl);
  allByClass(pl, 'pl__item')[1].click(); // mix
  byText(allByClass(pl, 'pl__link').find((r) => r.dataset.platform === 'spotify'), '在 Spotify 建一份').click();
  const card0 = allByClass(pl, 'pl__flow-card')[0];
  check(card0 && byText(card0, '開始') && !card0.textContent.includes('capy 不會再建一份同名的'), `同名的是空清單:照給開始:${card0 && card0.textContent}`);

  // 一個平台都沒登入
  pl = await open({ google: { state: 'ok' }, spotify: { state: 'missing' }, apple: { state: 'expired' }, youtube: { state: 'missing' } });
  await read(pl);
  check(pl.textContent.includes('這台電腦沒有登入任何平台。') && !calls.some((c) => c.startsWith('pl list')), '一個平台都沒登入:不讀、說明');

  // 英文
  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    pl = await open(AUTH, { 'pl list --provider spotify': list(spRows), 'pl list --provider youtube': list([]) });
    byText(pl, "Read each platform's playlists").click();
    await idle();
    const CJK = /[　-〿㐀-鿿＀-￯]/;
    const all = [];
    walk(allByClass(pl, 'pl__plats')[0], (c) => all.push(c._text || '', ...Object.values(c.attrs || {})));
    const bad = all.filter((x) => CJK.test(x) || x.includes('webui.'));
    check(bad.length === 0, `英文目錄下各平台上的清單不該有中文:${JSON.stringify([...new Set(bad)])}`);
    check(prow(pl, 'Chill').textContent.includes('5 songs · Not kept by capy yet'), `英文的列:${prow(pl, 'Chill').textContent}`);
    // 納入兩步的 label:英文進行式、沒有中文(計畫 §4 情境 14)
    const labels = [];
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label || ''); return Console.prototype.run.call(con, line, hooks, opts); };
    try {
      script['pl link --new-only -- Chill spotify:' + SP2] = { events: [done] };
      script['pl pull -- Chill'] = { events: answered };
      byText(prow(pl, 'Chill'), 'Add to capy').click();
      await idle();
    } finally {
      delete con.run;
    }
    check(labels.some((l) => l.startsWith('Adding "Chill"')) && labels.some((l) => l.startsWith('Pulling')) && labels.every((l) => !CJK.test(l) && /^[A-Z][a-z]*ing /.test(l)),
      `英文的納入 label:${JSON.stringify(labels)}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 8s. 清單頁的歌曲 wiki(計畫 2026-09-30 §3.3):每首一顆 Wiki(沒有曲名的不給);就地開 <dialog>、送 --title= / --artist=;
//     回答畫在對話框裡。showModal() 會讓 dock 整個 inert,所以中止鈕、失敗 / 被拒 / 中止的那句話、設定 AI 的表單都在對話框裡;
//     關掉對話框 = 中止。
await scenario('8s', async () => {
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const byText = (n, text) => { let b = null; walk(n, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const idle = async () => { for (let i = 0; i < 4; i++) { await new Promise((r) => con.idle(r)); await tick(5); } };
  const out = (o) => ({ events: [{ type: 'stdout', text: JSON.stringify(o) }, done] });
  const exit = (code, message = '', reason = 'done') => ({ type: 'exit', code, message, reason });
  const tracks = { c1: { title: '--header', artists: ['X', 'Y'] }, c2: { title: 'Solo' }, c3: { title: '  ' } };
  const data = { 'pl__a.json': { pid: 'a', name: 'road trip', links: {}, items: ['c1', 'c2', 'gone', 'c3'].map((cid) => ({ cid })) }, 'tracks.json': { tracks } };
  reset();
  script = { 'auth status --json': out({ google: { state: 'ok' } }), export: out(data) };
  const pl = mk();
  initPlaylists(pl, api, con, (x) => notices.push(x), { list: ['spotify', 'apple', 'youtube'], current: 'spotify' });
  await idle();
  const rows = allByClass(pl, 'pl__tbl')[0].querySelectorAll('tbody tr');
  const wiki = (tr) => byText(allByClass(tr, 'pl__wiki')[0], 'Wiki');
  check(rows.length === 4 && wiki(rows[0]) && wiki(rows[1]) && !wiki(rows[2]) && allByClass(rows[2], 'pl__wiki').length === 1,
    '每首一顆 Wiki;這台沒有資料(沒有曲名)的不給——空的 --title 會變成問正在播的那首——格子照留');
  check(!wiki(rows[3]) && allByClass(rows[3], 'pl__title')[0].textContent === '(本機沒有這首的資料)', '只有空白的曲名同 CLI 的 TrimSpace:當成沒有');
  check(wiki(rows[0]).getAttribute('aria-label') === '「--header」的歌曲 wiki' && 'run' in wiki(rows[0]).dataset, 'Wiki 鈕的報讀名稱帶曲名、有命令在跑時被擋(btn)');
  const dlg = allByClass(pl, 'pl__wiki-dlg')[0];
  const status = () => allByClass(dlg, 'pl__wiki-status')[0];
  check(dlg && dlg.tagName === 'DIALOG' && !dlg.open, '對話框在頁面裡、一開始關著');

  // 問:曲名以 - 開頭也用 = 的寫法送;歌手用 ", " 串;回答一行一行畫在對話框裡;跑的時候有自己的中止
  const [g, release] = gate();
  script['wiki --title=--header --artist=X, Y'] = { first: [{ type: 'stdout', text: '## 背' }], gate: g, events: [{ type: 'stdout', text: '景\n內容\n' }, done] };
  wiki(rows[0]).click();
  await tick(5);
  check(dlg.open && JSON.stringify(bodies.at(-1)) === JSON.stringify({ args: ['wiki', '--title=--header', '--artist=X, Y'] }), `開對話框、送 argv:${JSON.stringify(bodies.at(-1))}`);
  check(allByClass(dlg, 'pl__wiki-title')[0].textContent === '--header' && allByClass(dlg, 'pl__wiki-by')[0].textContent === 'X, Y', '對話框標題是曲名、下面是歌手');
  check(status().textContent.includes('關掉這個視窗都會停') && !status().textContent.includes('狀態列') && byText(dlg, '中止'), `跑的時候:對話框裡的說明與中止(不指 dock):${status().textContent}`);
  check(globalThis.document.getElementById('busy-cmd').textContent === '正在寫「--header」的歌曲 wiki', `執行狀態列用白話的 label,不是命令原文:${globalThis.document.getElementById('busy-cmd').textContent}`);
  const c0 = cancels.length;
  byText(dlg, '中止').click(2); // 連點「查詢」的第二下落在這裡:不算
  await tick(5);
  check(cancels.length === c0 && byText(dlg, '中止'), '連點的第二下不會把剛送出的問題停掉');
  release();
  await idle();
  const answer = allByClass(dlg, 'wiki__out')[0];
  check(allByClass(answer, 'wiki__h')[0]?.textContent === '背景' && allByClass(answer, 'wiki__p')[0]?.textContent === '內容', `切在行中間的 stdout 湊成整行畫出來:${answer.textContent}`);
  check(!byText(dlg, '中止') && status().textContent === '寫好了。', `做完:中止收起來、說一句(dock 的報讀被 inert 蓋住):${status().textContent}`);
  check(globalThis.document.activeElement === byText(dlg, '再問一次(不用快取的回答)'), '按下的中止被換掉:焦點交給新的第一顆,不掉到 body');
  byText(dlg, '再問一次(不用快取的回答)').click();
  await idle();
  check(JSON.stringify(bodies.at(-1).args) === JSON.stringify(['wiki', '--title=--header', '--artist=X, Y', '--refresh']), `再問一次帶 --refresh:${JSON.stringify(bodies.at(-1).args)}`);

  // 對話框自己的中止:dock 的中止被 inert 蓋住,這顆要真的停得下來;中止之後不給「設定 AI 端點」(設定救不了)
  const [gs, rs] = gate();
  script['wiki --title=Solo'] = { gate: gs, events: [exit(130, '', 'cancelled')] };
  wiki(rows[1]).click();
  await tick(5);
  const c1 = cancels.length;
  byText(dlg, '中止').click();
  await tick(5);
  check(cancels.length === c1 + 1 && byText(dlg, '中止中…'), `對話框的中止會送出中止:${cancels.length - c1}`);
  rs();
  await idle();
  check(status().textContent === '已停止。' && byText(dlg, '查詢') && !byText(dlg, '設定 AI 端點'), `中止之後只給查詢:${dlg.textContent.slice(0, 120)}`);

  // 沒有歌手就不帶 --artist;關掉對話框 = 中止,收尾那句寫在對話框裡
  dlg.close();
  const [g2, r2] = gate();
  script['wiki --title=Solo'] = { gate: g2, events: [exit(130, '', 'cancelled')] };
  const before = cancels.length;
  wiki(rows[1]).click();
  await tick(5);
  check(JSON.stringify(bodies.at(-1).args) === JSON.stringify(['wiki', '--title=Solo']) && allByClass(dlg, 'wiki__out')[0].children.length === 0, '沒有歌手不帶 --artist;上一首的回答清掉');
  dlg.close();
  await tick(5);
  check(cancels.length === before + 1, '關掉對話框 = 中止');
  // 中止還沒生效(命令還佔著序列槽)就去按別首的 Wiki:按鈕被擋,不開對話框、不送命令——舊命令剩下的輸出與收尾
  // 不會畫到另一首的標題底下(#126 review 第 2 點)
  const sent = calls.length;
  wiki(rows[0]).click();
  await tick(5);
  check(calls.length === sent && !dlg.open && allByClass(dlg, 'pl__wiki-title')[0].textContent === 'Solo', `命令還在跑時別首的 Wiki 被擋:${calls.slice(sent)}`);
  r2();
  await idle();
  check(status().textContent === '已停止。' && byText(dlg, '查詢') && !byText(dlg, '設定 AI 端點'), `中止之後:${status().textContent}`);

  // 失敗:原因畫在對話框裡(dock 被遮住),給「設定 AI 端點」;表單就地出現在對話框裡
  script['wiki --title=Solo'] = { events: [exit(1, 'Error: 還沒設定 AI 端點')] };
  wiki(rows[1]).click();
  await idle();
  check(status().textContent === '還沒設定 AI 端點' && status().classList.contains('page__warn') && byText(dlg, '設定 AI 端點') && byText(dlg, '查詢'), `失敗:${status().textContent}`);
  const [g3, r3] = gate();
  script['wiki setup'] = { first: [{ type: 'prompt', id: 7, kind: 'input', title: 'Base URL' }], gate: g3, events: [{ type: 'prompt_closed', id: 7, reason: 'answered' }, done] };
  byText(dlg, '設定 AI 端點').click();
  await tick(5);
  check(JSON.stringify(bodies.at(-1).args) === JSON.stringify(['wiki', 'setup']) && allByClass(dlg, 'pl__wiki-prompts')[0].children.length > 0, '設定 AI 端點:表單畫在對話框裡');
  check(dlg.contains(globalThis.document.activeElement), '精靈跑的時候收尾區是空的:焦點留在對話框裡(✕ 或表單)');
  r3();
  await idle();
  check(status().textContent === 'AI 端點設定好了,可以再問一次。' && byText(dlg, '查詢'), `設定好了:${status().textContent}`);

  // 精靈的表單被關掉(✕ / Esc)或等太久:CLI 回 "user aborted",對話框說語系裡的句子、不用警告色,照給「設定 AI 端點」
  for (const [how, ev, ex] of [['關掉', { type: 'prompt_closed', id: 8, reason: 'dismissed' }, exit(1, 'Error: user aborted')],
    ['等太久', { type: 'prompt_closed', id: 8, reason: 'timeout' }, exit(1, 'Error: user aborted', 'timeout')]]) {
    script['wiki --title=Solo'] = { events: [exit(1, 'Error: x')] }; // 先失敗一次,收尾區才有「設定 AI 端點」
    wiki(rows[1]).click();
    await idle();
    script['wiki setup'] = { events: [{ type: 'prompt', id: 8, kind: 'input', title: 'Base URL' }, ev, ex] };
    byText(dlg, '設定 AI 端點').click();
    await idle();
    check(status().textContent === '設定沒有做完,可以再設定一次。' && !status().classList.contains('page__warn') && byText(dlg, '設定 AI 端點'),
      `精靈的表單${how}:不給 huh 的英文原文:${status().textContent}`);
  }

  // 被伺服器拒絕(命令沒跑):照說原因,不給設定
  script['wiki --title=Solo'] = { status: 403, error: 'not offered' };
  wiki(rows[1]).click();
  await idle();
  check(status().textContent === 'not offered' && !byText(dlg, '設定 AI 端點') && byText(dlg, '查詢'), `被拒絕:${status().textContent}`);
  dlg.close();

  // 英文輪(計畫 §4「中英各跑一輪」):對話框每一種收尾都畫得出英文、label 是英文進行式、沒有中文
  i18nFile = './i18n-en.json';
  const CJK = /[\u3000-\u303f\u3400-\u9fff\uff00-\uffef]/;
  const labels = [];
  try {
    await loadI18n(api);
    reset();
    script = { 'auth status --json': out({ google: { state: 'ok' } }), export: out(data) };
    const en = mk();
    initPlaylists(en, api, con, (x) => notices.push(x), { list: ['spotify', 'apple', 'youtube'], current: 'spotify' });
    await idle();
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label || ''); return Console.prototype.run.call(con, line, hooks, opts); };
    const erows = allByClass(en, 'pl__tbl')[0].querySelectorAll('tbody tr');
    const edlg = allByClass(en, 'pl__wiki-dlg')[0];
    const est = () => allByClass(edlg, 'pl__wiki-status')[0].textContent;
    const said = [];
    const [ge, re] = gate();
    script['wiki --title=--header --artist=X, Y'] = { gate: ge, events: [{ type: 'stdout', text: 'x\n' }, done] };
    wiki(erows[0]).click();
    await tick(5);
    said.push(est());
    check(est() === t('webui.playlists.wiki_running') && byText(edlg, 'Stop') && globalThis.document.getElementById('busy-cmd').textContent === 'Writing the song wiki for --header', `英文:跑的時候:${est()}`);
    re();
    await idle();
    said.push(est());
    check(est() === 'Done.' && byText(edlg, 'Ask again (ignore the cached answer)'), `英文:做完:${est()}`);
    script['wiki --title=Solo'] = { events: [exit(130, '', 'cancelled')] };
    wiki(erows[1]).click();
    await idle();
    said.push(est());
    check(est() === 'Stopped.' && byText(edlg, 'Ask'), `英文:中止:${est()}`);
    script['wiki --title=Solo'] = { events: [exit(1, 'Error: x')] };
    wiki(erows[1]).click();
    await idle();
    script['wiki setup'] = { events: [{ type: 'prompt', id: 9, kind: 'input', title: 'Base URL' }, { type: 'prompt_closed', id: 9, reason: 'dismissed' }, exit(1, 'Error: user aborted')] };
    byText(edlg, 'Set up the AI endpoint').click();
    await idle();
    said.push(est());
    check(est() === "Setup didn't finish; you can run it again.", `英文:精靈沒做完:${est()}`);
    const all = [];
    walk(edlg, (c) => all.push(c._text || '', ...Object.values(c.attrs || {})));
    const bad = all.concat(said).filter((x) => CJK.test(x) || x.includes('webui.'));
    check(bad.length === 0, `英文目錄下對話框不該有中文:${JSON.stringify([...new Set(bad)])}`);
    edlg.close();
  } finally {
    delete con.run;
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
  check(labels.length >= 4 && labels.every((l) => !CJK.test(l) && /^[A-Z][a-z]*ing /.test(l)) && labels.some((l) => l === 'Setting up the AI endpoint'), `英文的 wiki label:${JSON.stringify(labels)}`);
});

// 8t. 精簡變更表(同步頁改版計畫 2026-10-01 §3.7,T1):CLI 的表先畫成「動作 / 歌曲 / 說明」,動作依 DIR + ACTION 翻成白話,
//     列與動作那一格保留機器值;表裡不只一份清單才多「清單」欄;去重報告是「# / 歌曲 / 說明」、# 照 POS;resolve 照舊畫原表;
//     完整原表收在「看完整表格」裡、打開才畫(只畫一次)。英文輪不出現中文。另外釘住共用的連結面板讀的是「現在」的 auth:
//     同一頁重讀後 Spotify 改成沒登入,那一列跟著變成 out(狀態放在 state 物件裡,不是建立時抓的一份)。
await scenario('8t', async () => {
  const { changeTable } = await import('./pages/plflow.mjs');
  const { initPlaylists } = await import('./pages/playlists.mjs');
  const T1 = '4uLU6hMCjMI75M1A2tKUQC';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const out = []; if (n) walk(n, (c) => { if (c.tagName === 'A') out.push(c); }); return out; };
  const SH = ['DIR', 'ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  const PH = SH.slice(1);
  const rows = [
    ['pull', 'add', 'spotify', 'road trip', '0', 'c1', T1, 'Song A', 'Artist A', 'r1', 'added_on_platform'],
    ['push', 'remove', 'apple', 'road trip', '1', 'c2', 'i.B', 'Song B', 'Artist B', 'r2', 'removed_in_master'],
    ['push', 'move', 'apple', 'road trip', '2', 'c3', 'i.C', 'Song C', '', 'r3', 'moved_in_master'],
    ['push', 'rename', 'apple', 'road trip', '', '', '', 'New Name', '', 'r4', 'renamed_in_master'],
    ['dedup', 'remove', '', 'road trip', '3', 'c4', '', 'Song D', '', 'r5', 'duplicate'],
    ['push', 'skip', 'apple', 'road trip', '4', 'c5', '', 'Song E', '', 'r6', 'no_mapping'],
  ];
  const heads = (box) => box.querySelector('thead tr').children.map((th) => th.textContent);
  const body = (box) => box.querySelector('tbody').children;
  const acts = (box) => body(box).map((tr) => allByClass(tr, 'chg__act')[0]?.textContent);
  const round = (want) => {
    const box = changeTable(SH, rows);
    check(JSON.stringify(heads(box)) === JSON.stringify(want.heads), `精簡表的表頭:${JSON.stringify(heads(box))}`);
    check(JSON.stringify(acts(box)) === JSON.stringify(want.acts), `動作翻成白話:${JSON.stringify(acts(box))}`);
    const trs = body(box);
    check(trs.map((tr) => `${tr.dataset.dir}/${tr.dataset.action}`).join() === 'pull/add,push/remove,push/move,push/rename,dedup/remove,push/skip', '列保留機器值 data-dir / data-action');
    check(allByClass(trs[1], 'chg__act')[0].dataset.action === 'remove', '動作那一格也標機器值(上色看它)');
    check(allByClass(trs[0], 'pl__sub')[0]?.textContent === 'Artist A' && allByClass(trs[3], 'pl__title')[0].textContent === 'New Name', '歌曲欄 = 曲名 + 歌手;改名列是清單名');
    check(allByClass(trs[0], 'chg__why')[0].textContent === 'r1', '說明 = CLI 的 REASON 原樣');
    // 每一種 DIR + ACTION 各畫一張一列的表,比對白話(ACTS 的鍵或語系字串接錯就紅)
    for (const [k, w] of Object.entries(want.each)) {
      const [d, a] = k.split(' ');
      const got = acts(changeTable(SH, [[d, a, 'apple', 'road trip', '0', 'c', 'i.X', 'S', '', 'r', 'x']]))[0];
      check(got === w, `「${k}」的白話:${got}`);
    }
    // 窄版拆行後報讀還是表格(同 songTable):table / row / columnheader / cell
    const tb = box.querySelector('.chg__tbl');
    check(tb.getAttribute('role') === 'table' && box.querySelector('thead tr').children.every((th) => th.getAttribute('role') === 'columnheader')
      && trs.every((tr) => tr.getAttribute('role') === 'row' && tr.children.every((td) => td.getAttribute('role') === 'cell')), '精簡表的每個元素都標了表格的 role');
    check(box.classList.contains('chg--sp') && !box.classList.contains('chg--pl'), '有 Spotify 連結欄、只有一份清單:標 chg--sp(拆行斷點跟著欄位走)');
    const links = anchors(box.querySelector('.tbl-wrap'));
    check(links.length === 1 && links[0].href === `https://open.spotify.com/track/${T1}`, `精簡表裡 Spotify 的列連回去:${links.map((a) => a.href)}`);
    check(allByClass(box, 'page__note')[0].textContent === want.note, `筆數那句不變:${allByClass(box, 'page__note')[0].textContent}`);
    // 完整原表:打開才畫、只畫一次
    const full = allByClass(box, 'chg__full')[0];
    check(allByClass(box, 'tbl').length === 1 && full.querySelector('summary').textContent === want.full, '完整原表一開始不畫');
    full.open = true;
    full.l.toggle();
    full.l.toggle();
    const tbls = allByClass(box, 'tbl');
    check(tbls.length === 2 && heads(full).includes('REASON_CODE') && anchors(full).length === 1, `打開「看完整表格」畫出原表(含 Spotify 連結),而且只畫一次:${tbls.length}`);
    // 不只一份清單:多「清單」欄
    const two = changeTable(SH, [rows[0], ['push', 'add', 'apple', 'mix', '0', 'c9', 'i.Z', 'Song Z', '', 'r', 'push']]);
    check(heads(two)[0] === want.heads2 && body(two)[1].children[0].textContent === 'mix' && two.classList.contains('chg--pl'), `兩份清單:第一欄是清單、標 chg--pl:${JSON.stringify(heads(two))}`);
    // 沒有 DIR 欄的表:呼叫端給方向;沒給就照原字
    const pulled = changeTable(PH, [PH.map((_, i) => rows[0][i + 1])], { dir: 'pull' });
    check(acts(pulled)[0] === want.acts[0], `pl pull 的表(沒有 DIR):照呼叫端給的方向:${acts(pulled)}`);
    check(acts(changeTable(PH, [PH.map((_, i) => rows[0][i + 1])]))[0] === 'add', '不知道方向:照原字顯示,不藏列');
    // 去重報告:# 照 POS(說明裡的 pos 是同一個數)
    const rep = changeTable(['POS', 'ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'], [['1', T1, 'Song A', 'a', 'same as pos 0', 'dup_id']], { spotifyReport: true });
    check(JSON.stringify(heads(rep)) === JSON.stringify(['#', want.heads[1], want.heads[2], '']) && body(rep)[0].children[0].textContent === '1' && anchors(rep.querySelector('.tbl-wrap')).length === 1,
      `去重報告:# / 歌曲 / 說明 + Spotify 連結:${JSON.stringify(heads(rep))}`);
    // resolve:照舊畫原表
    const RH = ['ACTION', 'CID', 'PROVIDER', 'PROVIDER_ID', 'CONFIDENCE', 'SOURCE', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
    const res = changeTable(RH, [['map', 'c1', 'spotify', T1, '95', 'isrc', 'Song A', 'a', 'r', 'isrc']]);
    check(allByClass(res, 'tbl').length === 1 && heads(res)[0] === 'ACTION' && allByClass(res, 'chg__full').length === 0, 'resolve 的表照舊畫原表');
  };
  round({ heads: ['動作', '歌曲', '說明', ''], heads2: '清單', full: '看完整表格', note: '5 筆變更(跳過的不算)',
    acts: ['加進正本(從 Spotify)', '從 Apple Music 拿掉', '在 Apple Music 換位置', 'Apple Music 上的清單改名', '從正本拿掉(重複)', '跳過'],
    each: { 'pull add': '加進正本(從 Apple Music)', 'pull remove': '從正本拿掉', 'pull move': '正本換位置', 'pull rename': '正本改名', 'pull unlink': '取消連結 Apple Music 上的清單',
      'push add': '加到 Apple Music', 'push remove': '從 Apple Music 拿掉', 'push move': '在 Apple Music 換位置', 'push rename': 'Apple Music 上的清單改名',
      'dedup remove': '從正本拿掉(重複)', 'push skip': '跳過', 'pull skip': '跳過', 'migrate add': 'add' } });
  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    round({ heads: ['Change', 'Song', 'Why', ''], heads2: 'Playlist', full: 'See the full table', note: '5 changes (skipped rows don\'t count)',
      acts: ['Add to the master copy (from Spotify)', 'Remove from Apple Music', 'Move on Apple Music', 'Rename the playlist on Apple Music', 'Remove from the master copy (duplicate)', 'Skip'],
      each: { 'pull add': 'Add to the master copy (from Apple Music)', 'pull remove': 'Remove from the master copy', 'pull move': 'Move in the master copy', 'pull rename': 'Rename the master copy',
        'pull unlink': 'Unlink the playlist on Apple Music', 'push add': 'Add on Apple Music', 'push remove': 'Remove from Apple Music', 'push move': 'Move on Apple Music',
        'push rename': 'Rename the playlist on Apple Music', 'dedup remove': 'Remove from the master copy (duplicate)', 'push skip': 'Skip', 'pull skip': 'Skip', 'migrate add': 'add' } });
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }

  // 共用的連結面板讀「現在」的 auth:同一頁重讀後 Spotify 改成沒登入,那一列跟著變
  const idle = () => new Promise((r) => con.idle(r));
  const out = (o) => ({ events: [{ type: 'stdout', text: JSON.stringify(o) }, done] });
  const data = { 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: '37i9dQZF1DXcBWIGoYBM5M' }, items: [{ cid: 'c1' }] }, 'tracks.json': { tracks: { c1: { title: 'Song A', mappings: { spotify: { id: T1 } } } } } };
  reset();
  script = { 'auth status --json': out({ google: { state: 'ok' }, spotify: { state: 'ok' } }), export: out(data) };
  const pl = mk();
  const page = initPlaylists(pl, api, con, () => {}, { list: ['spotify', 'apple'], current: 'spotify' });
  await idle();
  const spotifyRow = () => allByClass(pl, 'pl__link').find((r) => r.dataset.platform === 'spotify');
  check(spotifyRow()?.dataset.state === 'linked', `一開始 Spotify 是 linked:${spotifyRow()?.dataset.state}`);
  script['auth status --json'] = out({ google: { state: 'ok' }, spotify: { state: 'missing' } });
  page.refresh();
  await idle(); await tick(5); await idle();
  check(spotifyRow()?.dataset.state === 'out', `重讀後 Spotify 沒登入:那一列跟著變成 out:${spotifyRow()?.dataset.state}`);

  // #/playlists/<pid>:初始化與回到這一頁(refresh)都打開那一份(計畫 2026-10-01 §3.6)
  const two = { ...data, 'pl__b.json': { pid: 'b', name: 'mix', links: {}, items: [] } };
  reset();
  script = { 'auth status --json': out({ google: { state: 'ok' } }), export: out(two) };
  const deep = mk();
  const dp = initPlaylists(deep, api, con, () => {}, { list: ['spotify'], current: 'spotify' }, 'b');
  await idle();
  const active = () => allByClass(deep, 'pl__item').find((x) => x.classList.contains('is-active'))?.textContent || '';
  check(active().startsWith('mix'), `#/playlists/b 打開 mix:${active()}`);
  dp.refresh('a');
  await idle(); await tick(5); await idle();
  check(active().startsWith('road trip'), `回到這一頁帶 a:換成 road trip:${active()}`);
  // 帶 pid 進頁、export 第一次就失敗(這台沒有本機資料):照樣說下一步,不是整頁空白
  reset();
  script = { 'auth status --json': out({ google: { state: 'ok' } }), export: { events: [{ type: 'exit', code: 1, message: 'Error: x', reason: 'done' }] } };
  const deep2 = mk();
  initPlaylists(deep2, api, con, () => {}, { list: ['spotify'], current: 'spotify' }, 'b');
  await idle(); await tick(5); await idle();
  check(deep2.textContent.includes('這台電腦還沒讀過你 Google Drive 上的正本') && (() => { let ok = false; const w = (n) => { for (const c of n.children || []) { if (c.tagName === 'A' && c.href === '#/sync') ok = true; w(c); } }; w(deep2); return ok; })(),
    `#/playlists/b 進頁、export 失敗:照樣說下一步:${deep2.textContent.slice(0, 80)}`);
});

// 8u. 同步頁(計畫 2026-10-01-sync-page-redesign.md §4 的 node 情境,T2):進頁只安靜讀 auth 與 export;在同步的清單照 export 畫;
//     Google Drive 沒連 / 讀不到時不送 pl list;平台卡選了才讀、沒登入的不讀;每列狀態;選清單才 pl show、讀過不重送;納入的兩個命令與
//     下一步;只連一個平台時主要鈕不是同步;逐平台同步與 --all 失敗後的改法;讀回正本;看看有沒有重複;進階區的 args 與收尾;路由。
await scenario('8u', async () => {
  const { initSync } = await import('./pages/sync.mjs');
  const SP1 = '4uLU6hMCjMI75M1A2tKUQC', SP2 = '7qiZfU4dY1lWllzX7mPBI3', SP3 = '37i9dQZF1DXcBWIGoYBM5M', SP6 = '1A2B3C4D5E6F7G8H9I0J1K';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const anchors = (n) => { const o = []; if (n) walk(n, (c) => { if (c.tagName === 'A') o.push(c); }); return o; };
  const byText = (n, text) => { let b = null; if (n) walk(n, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const idle = () => new Promise((r) => con.idle(r));
  const settle = async () => { for (let k = 0; k < 4; k++) { await idle(); await tick(5); } };
  const out = (o) => ({ events: [{ type: 'stdout', text: JSON.stringify(o) }, done] });
  const table = (header, rows, end = done) => ({ events: [{ type: 'table', header, rows }, end] });
  const exit = (code, message = '', reason = 'done') => ({ type: 'exit', code, message, reason });
  const LH = ['ID', 'NAME', 'TRACKS', 'OWNER'];
  const TH = ['ID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'];
  const PH = ['ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'];
  const AUTH = { google: { state: 'ok', device_id: 'devA' }, spotify: { state: 'ok' }, apple: { state: 'ok' }, youtube: { state: 'ok', channel_id: 'UCme' } };
  const tracks = { c1: { title: 'Song A', mappings: { spotify: { id: SP1 } } } };
  const data = (extra = {}) => ({
    'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: SP3, youtube: 'UCme/PLy' }, items: [{ cid: 'c1' }, { cid: 'c1' }] },
    'pl__b.json': { pid: 'b', name: 'mix', links: { apple: 'p.B' }, items: [{ cid: 'c1' }] },
    'pl__c.json': { pid: 'c', name: 'Chill', links: {}, items: [] },
    'tracks.json': { tracks }, ...extra });
  const SPOTIFY_LISTS = table(LH, [[SP3, 'road trip', '2', 'me'], [SP2, 'Workout', '5', 'me'], ['3zzzzzzzzzzzzzzzzzzzzz', 'CHILL', '3', 'me'], [SP6, 'Road Trip', '1', 'me'], ['5mmmmmmmmmmmmmmmmmmmmm', 'MIX', '4', 'me']]);
  const providers = { list: ['spotify', 'apple', 'local', 'youtube'], current: 'spotify' };
  const radios = (r, name) => { const o = []; walk(r, (c) => { if (c.tagName === 'INPUT' && c.type === 'radio' && c.name === name) o.push(c); }); return o; };
  const pickPlat = async (sy, p) => { radios(sy, 'sync-plat')[providers.list.indexOf(p)].l.change(); await settle(); };
  const pickList = async (sy, name) => { const i = allByClass(sy, 'sync__item').findIndex((w) => allByClass(w, 'pl__name')[0]?.textContent === name); radios(sy, 'sync-list')[i].l.change(); await settle(); };
  const sec = (sy, i) => allByClass(sy, 'sync__sec')[i];
  const status = (box) => allByClass(box, 'pl__flow-status').map((x) => x.textContent).join(' | ');
  const sent = () => bodies.filter((b) => b.args).map((b) => b.args.join(' '));
  let page = null;
  const open = async (more = {}, auth = AUTH, d = data(), arg = '') => {
    reset();
    script = { 'auth status --json': out(auth), export: out(d), 'pl list --provider spotify': SPOTIFY_LISTS, ...more };
    const sy = mk();
    page = initSync(sy, api, con, () => {}, providers, arg);
    await settle();
    return sy;
  };

  // ── 進頁:只安靜讀 auth 與 export(主控台不多區塊);在同步的清單照 export 畫,零網路
  const blocks0 = allByClass(root, 'block').length;
  let sy = await open();
  check(JSON.stringify(calls) === JSON.stringify(['auth status --json', 'export']) && allByClass(root, 'block').length === blocks0, `進頁只送 auth status 與 export、都 quiet:${JSON.stringify(calls)}`);
  check(sy.textContent.includes('加歌、拿掉、換位置、改名之前,capy 一定先列出要改什麼,你確認了才寫;建一份新的正本或平台清單並連上,按下去就會做。'), 'lead 照實說哪些會先問、哪些按下去就做');
  const rws = allByClass(sy, 'sync__row');
  check(rws.map((r) => r.dataset.pid).join() === 'a,b' && rws.every((r) => byText(r, '同步')), `在同步的清單:只列有連結的、每列一顆同步:${rws.map((r) => r.dataset.pid)}`);
  check(anchors(rws[0]).some((a) => a.href === '#/playlists/a') && rws[0].textContent.includes('Spotify · YouTube Music'), '名稱連到「我的清單」那一份、列出連到哪些平台');
  check(byText(sec(sy, 0), '全部同步') && !byText(sec(sy, 0), '全部同步').disabled, '有可以同步的清單:全部同步可以按');

  // 連著的平台這台全都沒登入:那一列不給同步、說原因、指到帳號頁
  sy = await open({}, { ...AUTH, apple: { state: 'missing' } });
  const mixRow = allByClass(sy, 'sync__row').find((r) => r.dataset.pid === 'b');
  check(!byText(mixRow, '同步') && mixRow.textContent.includes('這台電腦沒有登入 Apple Music') && anchors(mixRow).some((a) => a.href === '#/account'), `全沒登入的列:${mixRow && mixRow.textContent}`);

  // 沒有任何連結:說從下面開始、全部同步停用
  sy = await open({}, AUTH, { 'pl__c.json': { pid: 'c', name: 'Chill', links: {}, items: [] }, 'tracks.json': { tracks } });
  check(sy.textContent.includes('還沒有清單在同步。從下面挑一份平台上的清單開始。') && byText(sec(sy, 0), '全部同步').disabled, '沒有連結:說從下面開始、全部同步停用');

  // ── Google Drive:沒連 / 讀不到登入資料。都不送 pl list
  sy = await open({}, { ...AUTH, google: { state: 'missing' } });
  await pickPlat(sy, 'spotify');
  check(sy.textContent.includes('capy 把正本存在你的 Google Drive,同步之前要先連接它。') && !sent().some((c) => c.startsWith('pl list')) && byText(sec(sy, 0), '全部同步').disabled,
    `Google Drive 沒連:說要先連、不讀平台清單、全部同步停用:${JSON.stringify(sent())}`);
  sy = await open({}, { ...AUTH, google: { state: 'keychain_error' } }, data(), '');
  check(sy.textContent.includes('讀不到你的 Google Drive 登入資料') && !sy.textContent.includes('先連接') && !byText(sy, '讀回正本(會順便讀已登入的平台)'), 'Google 讀不到登入資料:不說先連接、不給讀回正本');

  // ── 平台卡:選了才讀、讀過不重讀;每列狀態只看正本
  sy = await open({ 'pl list --provider apple': table(LH, [['p.B', 'mix', '-', ''], ['p.X', 'Gym', '-', '']]) });
  check(!sent().some((c) => c.startsWith('pl list')), '還沒選平台:不讀');
  await pickPlat(sy, 'spotify');
  const states = Object.fromEntries(allByClass(sy, 'sync__item').map((w) => [allByClass(w, 'pl__name')[0].textContent, w.dataset.state]));
  check(JSON.stringify(states) === JSON.stringify({ 'road trip': 'linked', Workout: 'unlinked', CHILL: 'same_name', 'Road Trip': 'same_name_taken', MIX: 'same_name' }), `每列的狀態(只差大小寫也算同名):${JSON.stringify(states)}`);
  check(allByClass(sy, 'sync__item').every((w) => !allByClass(w, 'pl__item')[0].children.some((c) => c.tagName === 'A')) && anchors(allByClass(sy, 'sync__item')[1]).some((a) => a.href === `https://open.spotify.com/playlist/${SP2}`),
    'Spotify 的列連回去,連結是單選的兄弟、不包進去');
  await pickPlat(sy, 'apple');
  check(allByClass(sy, 'sync__item').map((w) => allByClass(w, 'pl__count')[0].textContent).join('|') === '|' && !sy.textContent.includes('- 首'), 'Apple 的 TRACKS 是 -:首數留白');
  await pickPlat(sy, 'spotify');
  check(sent().filter((c) => c === 'pl list --provider spotify').length === 1, `讀過的平台不重讀:${JSON.stringify(sent())}`);
  sy = await open({}, { ...AUTH, youtube: { state: 'missing' } });
  await pickPlat(sy, 'youtube');
  check(!sent().includes('pl list --provider youtube') && sy.textContent.includes('這台電腦沒有登入 YouTube Music'), '沒登入的平台:不讀、說原因');
  sy = await open({}, { ...AUTH, youtube: { state: 'ok' } });
  await pickPlat(sy, 'youtube');
  check(!sent().includes('pl list --provider youtube') && sy.textContent.includes('重新登入一次'), 'YouTube 缺帳號資料:不讀、請人重新登入');

  // ── 選清單才 pl show;讀過不重送;預覽表的列與欄
  sy = await open({ [`pl show ${SP2} --provider spotify`]: table(TH, [[SP1, 'Song A', 'Artist', 'Album', '215000'], ['spotify:local:x:y:z:1', 'Local', '', '', '0']]) });
  await pickPlat(sy, 'spotify');
  await pickList(sy, 'Workout');
  const pv = allByClass(sy, 'sync__preview')[0];
  const prow = pv.querySelector('tbody').children;
  check(sent().includes(`pl show ${SP2} --provider spotify`) && prow.length === 2 && allByClass(prow[0], 'pl__dur')[0].textContent === '3:35' && anchors(pv).length === 1,
    `預覽表:兩首、時長 m:ss、只有 Spotify id 的那首連回去:${prow.length}`);
  const card = allByClass(sec(sy, 1), 'pl__flow-card')[0];
  check(card.dataset.state === 'unlinked' && byText(card, '納入 capy') && byText(card, '看看這份有沒有重複') && card.textContent.includes('這一步按下去就做'), '還沒納入:引導卡照實說、主要鈕是納入');
  await pickList(sy, 'road trip');
  await pickList(sy, 'Workout');
  check(sent().filter((c) => c === `pl show ${SP2} --provider spotify`).length === 1, '讀過的清單不重讀');

  // 預覽讀不到歌(Spotify 上追蹤的別人的清單):pl link 也會擋下,不給納入;Apple 的「找不到」可能只是空清單,照給
  const sy2 = await open({ [`pl show ${SP2} --provider spotify`]: { events: [exit(1, 'Error: 無法讀取這個清單的內容')] }, 'pl list --provider apple': table(LH, [['p.X', 'Gym', '-', '']]),
    'pl show p.X --provider apple': { events: [exit(1, 'Error: 找不到')] } });
  await pickPlat(sy2, 'spotify');
  await pickList(sy2, 'Workout');
  const c2 = allByClass(sec(sy2, 1), 'pl__flow-card')[0];
  check(!byText(c2, '納入 capy') && c2.textContent.includes('讀不到這份清單的歌') && sec(sy2, 1).textContent.includes('無法讀取這個清單的內容'), `Spotify 預覽讀不到:不給納入、說原因:${c2.textContent}`);
  await pickPlat(sy2, 'apple');
  await pickList(sy2, 'Gym');
  check(byText(allByClass(sec(sy2, 1), 'pl__flow-card')[0], '納入 capy') && sec(sy2, 1).textContent.includes('Apple Music 對空清單也會回「找不到」'), 'Apple 預覽找不到:可能是空的,照給納入');

  // 序列槽被佔著時換清單:pl show 排隊再送,不被擋成「讀不到」(引導卡照給納入)
  const [g1, rel1] = gate();
  const sy3 = await open({ [`pl show ${SP2} --provider spotify`]: { gate: g1, events: [{ type: 'table', header: TH, rows: [] }, done] }, [`pl show ${SP6} --provider spotify`]: table(TH, []) });
  await pickPlat(sy3, 'spotify');
  radios(sy3, 'sync-list')[allByClass(sy3, 'sync__item').findIndex((w) => allByClass(w, 'pl__name')[0]?.textContent === 'Workout')].l.change();
  await tick(5);
  radios(sy3, 'sync-list')[allByClass(sy3, 'sync__item').findIndex((w) => allByClass(w, 'pl__name')[0]?.textContent === 'Road Trip')].l.change();
  await tick(5);
  rel1();
  await settle();
  check(sent().includes(`pl show ${SP6} --provider spotify`) && !sec(sy3, 1).textContent.includes('讀不到'), `換清單時槽被佔:排隊再讀,不算讀不到:${JSON.stringify(sent())}`);
  // pl show 被中止:不算讀不到;引導卡照給納入、預覽給重新讀取
  const sy4 = await open({ [`pl show ${SP2} --provider spotify`]: { events: [exit(130, '', 'cancelled')] } });
  await pickPlat(sy4, 'spotify');
  await pickList(sy4, 'Workout');
  const rr = byText(allByClass(sec(sy4, 1), 'sync__preview')[0], '重新讀取');
  check(byText(sec(sy4, 1), '納入 capy') && rr, '預覽被中止:照給納入、給重新讀取');
  script[`pl show ${SP2} --provider spotify`] = table(TH, [[SP1, 'Song A', 'Artist', 'Album', '215000']]);
  rr?.click();
  await settle();
  check(sent().filter((c) => c === `pl show ${SP2} --provider spotify`).length === 2 && allByClass(sec(sy4, 1), 'sync__preview')[0].querySelector('tbody')?.children.length === 1, '按重新讀取:重送 pl show 並畫出來');
  // 平台清單讀取中按連結面板的「在 X 建一份」:不丟例外,說明卡照樣出現(同名由 CLI 擋)
  const [g2, rel2] = gate();
  const sy5 = await open({ 'pl list --provider apple': { gate: g2, events: [{ type: 'table', header: LH, rows: [] }, done] } });
  await pickPlat(sy5, 'spotify');
  await pickList(sy5, 'road trip');
  radios(sy5, 'sync-plat')[providers.list.indexOf('apple')].l.change();
  await tick(5);
  radios(sy5, 'sync-plat')[0].l.change();
  await tick(5);
  radios(sy5, 'sync-list')[0].l.change();
  await tick(5);
  let thrown = '';
  try { byText(allByClass(sy5, 'pl__link').find((x) => x.dataset.platform === 'apple'), '在 Apple Music 建一份').click(); } catch (e) { thrown = e.message; }
  check(!thrown && sec(sy5, 1).textContent.includes('在 Apple Music 建一份'), `Apple 的清單還在讀時按建一份:不丟例外:${thrown}`);
  rel2();
  await settle();
  // 名字附註:頁面讀不到 base,不預測改名的方向
  const sy6 = await open({ 'pl list --provider spotify': table(LH, [[SP3, 'road trip (old)', '2', 'me']]) });
  await pickPlat(sy6, 'spotify');
  await pickList(sy6, 'road trip (old)');
  check(sec(sy6, 1).textContent.includes('Spotify 上的這份現在叫「road trip (old)」,正本叫「road trip」') && !sec(sy6, 1).textContent.includes('下一次同步會改成'), '名字附註不預測方向');
  // export 不是因為「沒有副本」而失敗:不叫人按讀回正本(那顆鈕不在),指到重新讀取
  const sy7 = await open({ export: { events: [exit(1, 'Error: context canceled', 'cancelled')] } });
  await pickPlat(sy7, 'spotify');
  await pickList(sy7, 'Workout');
  check(sy7.textContent.includes('先按上面的「重新讀取」') && !sy7.textContent.includes('讀回正本') && byText(sy7, '重新讀取'), 'export 沒讀到(不是沒有副本):指到重新讀取');

  // ── 看看有沒有重複:pl dedup <平台>:<清單>,不帶任何旗標;有表時接下一步、沒有時一句
  script[`pl dedup spotify:${SP2}`] = table(['POS', 'ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'], [['2', SP1, 'Song A', 'a', 'r', 'dup_id']]);
  byText(sec(sy, 1), '看看這份有沒有重複').click();
  await settle();
  check(JSON.stringify(bodies.filter((b) => b.args?.[1] === 'dedup').at(-1).args) === JSON.stringify(['pl', 'dedup', `spotify:${SP2}`]) && status(sec(sy, 1)).includes('這份清單裡有 1 首重複。這只是檢查,沒有改任何東西。'),
    `看看有沒有重複:命令與結果:${status(sec(sy, 1))}`);
  script[`pl dedup spotify:${SP2}`] = { events: [done] };
  byText(sec(sy, 1), '看看這份有沒有重複').click();
  await settle();
  check(status(sec(sy, 1)) === '這份清單裡沒有重複。', `沒有重複:${status(sec(sy, 1))}`);

  // ── 納入:兩個命令、名稱放在 -- 後面、提示在這一頁;做完接下一步;只連一個平台時主要鈕不是同步
  script[`pl link --new-only -- Workout spotify:${SP2}`] = { events: [done] };
  script['pl pull -- Workout'] = { events: [{ type: 'table', header: PH, rows: [['add', 'spotify', 'Workout', '0', 'c1', SP1, 'Song A', 'X', 'r', 'added_on_platform']] }, { type: 'prompt', id: 1, kind: 'confirm', title: '?' }, { type: 'prompt_closed', id: 1, reason: 'answered' }, done] };
  script.export = out(data({ 'pl__w.json': { pid: 'w', name: 'Workout', links: { spotify: SP2 }, items: [{ cid: 'c1' }] } }));
  let before = bodies.length;
  byText(sec(sy, 1), '納入 capy').click();
  await settle();
  check(JSON.stringify(bodies.slice(before).filter((b) => ['link', 'pull'].includes(b.args?.[1])).map((b) => b.args)) === JSON.stringify([['pl', 'link', '--new-only', '--', 'Workout', `spotify:${SP2}`], ['pl', 'pull', '--', 'Workout']]),
    `納入的兩個命令:${JSON.stringify(bodies.slice(before).map((b) => b.args))}`);
  check(status(sec(sy, 1)).includes('完成:「Workout」已經交給 capy 保管') && status(sec(sy, 1)).includes('下一步:從下面挑一個平台「建一份」'), `納入完成接上下一步:${status(sec(sy, 1))}`);
  check(allByClass(sec(sy, 1), 'pl__flow-prompts').length > 0 && globalThis.location.hash === '', '納入的提示畫在同步頁裡(不切到主控台)');
  const one = allByClass(sec(sy, 1), 'pl__flow-card').find((c) => c.dataset.links);
  const primaries = (c) => { const o = []; walk(c, (x) => { if (x.tagName === 'BUTTON' && x.classList.contains('btn--primary')) o.push(x.textContent); }); return o; };
  check(one && one.dataset.links === '1' && !primaries(one).includes('同步這份清單') && one.textContent.includes('這份目前只連著 Spotify,同步要有第二個平台')
    && allByClass(one, 'wiz__step')[2]?.dataset.state === 'now', `剛納入、只連一個平台:主要鈕不是同步、停在第 3 步:${one && primaries(one)}`);
  await pickList(sy, 'road trip');
  const two = allByClass(sec(sy, 1), 'pl__flow-card').find((c) => c.dataset.links);
  check(two && two.dataset.links === '2' && JSON.stringify(primaries(two)) === JSON.stringify(['同步這份清單']) && byText(two, '去除重複'), `連兩個平台:主要鈕是同步這份清單、另有去除重複:${two && primaries(two)}`);
  check(anchors(sec(sy, 1)).some((a) => a.href === '#/playlists/a'), '連著正本:可以在「我的清單」打開那一份');

  // 寫入之後:清掉預覽、重讀選中的平台清單與那份的預覽(計畫 §3.2「寫入那份清單後清掉重讀」)
  script[`pl show ${SP3} --provider spotify`] = table(TH, [[SP1, 'Song A', 'Artist', 'Album', '215000']]);
  script['pl sync a'] = { events: [{ type: 'prompt', id: 2, kind: 'confirm', title: '?' }, { type: 'prompt_closed', id: 2, reason: 'answered' }, done] };
  const showsBefore = sent().filter((c) => c === `pl show ${SP3} --provider spotify`).length;
  const listsBefore = sent().filter((c) => c === 'pl list --provider spotify').length;
  script[`pl show ${SP3} --provider spotify`] = table(TH, [[SP1, 'Song A', 'Artist', 'Album', '215000'], [SP2, 'Song New', 'Artist', 'Album', '1000']]);
  byText(allByClass(sec(sy, 1), 'pl__flow-card').find((c) => c.dataset.links), '同步這份清單').click();
  await settle();
  const showsAfter = sent().filter((c) => c === `pl show ${SP3} --provider spotify`).length;
  check(showsAfter === showsBefore + 1 && allByClass(sec(sy, 1), 'sync__preview')[0].querySelector('tbody').children.length === 2 && sent().filter((c) => c === 'pl list --provider spotify').length === listsBefore + 1,
    `寫入之後重讀清單與預覽:${showsBefore} → ${showsAfter} ${JSON.stringify(sent().slice(-4))}`);

  // ── 逐平台:連著的平台這台沒登入時,先說明、按了才一家一家同步(不送整份)
  sy = await open({}, { ...AUTH, youtube: { state: 'missing' } });
  before = bodies.length;
  byText(allByClass(sy, 'sync__row')[0], '同步').click();
  await settle();
  check(sec(sy, 0).textContent.includes('這台電腦沒有登入 YouTube Music,全部一起同步會失敗。') && bodies.length === before, '有連著的平台沒登入:先說明,不送命令');
  byText(sec(sy, 0), '只同步有登入的平台').click();
  await settle();
  check(JSON.stringify(sent().filter((c) => c.startsWith('pl sync'))) === JSON.stringify(['pl sync a --provider spotify']), `只同步有登入的平台:${JSON.stringify(sent())}`);
  // 全部同步以 exit 1 結束:多一顆「改成一個平台一個平台同步」;逐平台時某一家失敗就記下、換下一家
  sy = await open({ 'pl sync --all': { events: [exit(1, 'Error: 還沒設 local_root')] }, 'pl sync --all --provider apple': { events: [exit(1, 'Error: Apple 讀不到')] } });
  byText(sec(sy, 0), '全部同步').click();
  await settle();
  check(status(sec(sy, 0)).includes('還沒設 local_root') && !status(sec(sy, 0)).includes('Error:') && byText(sec(sy, 0), '改成一個平台一個平台同步'), `全部同步失敗:說原因、給逐平台:${status(sec(sy, 0))}`);
  byText(sec(sy, 0), '改成一個平台一個平台同步').click();
  await settle();
  check(JSON.stringify(sent().filter((c) => c.startsWith('pl sync --all --provider'))) === JSON.stringify(['pl sync --all --provider spotify', 'pl sync --all --provider apple', 'pl sync --all --provider youtube'])
    && status(sec(sy, 0)).includes('有平台沒有同步成功:Apple Music:Apple 讀不到'), `逐平台:照平台順序、失敗的記下來繼續:${JSON.stringify(sent())} ${status(sec(sy, 0))}`);

  // 逐平台被後面的平台停下時,前面失敗過的也要說出來,後面沒跑的照實列出
  sy = await open({ 'pl sync --all': { events: [exit(1, 'Error: x')] }, 'pl sync --all --provider spotify': { events: [exit(1, 'Error: S 失敗')] }, 'pl sync --all --provider apple': { events: [exit(2, 'Error: 待套用')] } });
  byText(sec(sy, 0), '全部同步').click();
  await settle();
  byText(sec(sy, 0), '改成一個平台一個平台同步').click();
  await settle();
  check(status(sec(sy, 0)).includes('有平台沒有同步成功:Spotify:S 失敗') && status(sec(sy, 0)).includes('沒有同步的平台:YouTube Music') && status(sec(sy, 0)).includes('你按了取消'),
    `逐平台停下:前面失敗的、這一家的原因、後面沒跑的都說:${status(sec(sy, 0))}`);

  // ── 這台沒有副本:讀回正本。範圍是 auth 說 ok 的音樂平台;exit 1 換下一家,第一個成功就停並重讀
  const noCopy = { events: [exit(1, 'Error: 本機沒有資料')] };
  sy = await open({ 'pl pull --all --provider spotify': { events: [exit(1, 'Error: Spotify 讀不到')] } }, { ...AUTH, youtube: { state: 'missing' } }, data());
  script.export = noCopy;
  page.refresh();
  await settle();
  check(sec(sy, 0).textContent.includes('這台電腦還沒有正本的副本') && byText(sec(sy, 0), '讀回正本(會順便讀已登入的平台)'), '沒有副本:說明並給讀回正本');
  script.export = out(data());
  byText(sec(sy, 0), '讀回正本(會順便讀已登入的平台)').click();
  await settle();
  check(JSON.stringify(sent().filter((c) => c.startsWith('pl pull'))) === JSON.stringify(['pl pull --all --provider spotify', 'pl pull --all --provider apple'])
    && status(sec(sy, 0)).includes('完成:已經讀回正本') && allByClass(sy, 'sync__row').length === 2, `讀回正本:Spotify 失敗換 Apple、成功就停並重讀:${JSON.stringify(sent())} ${status(sec(sy, 0))}`);
  // 取消:照實說還是沒有副本,按鈕還在
  sy = await open({ 'pl pull --all --provider spotify': { events: [exit(2, 'Error: 待套用')] } }, AUTH, data());
  script.export = noCopy;
  page.refresh();
  await settle();
  byText(sec(sy, 0), '讀回正本(會順便讀已登入的平台)').click();
  await settle();
  check(status(sec(sy, 0)).includes('你按了取消') && status(sec(sy, 0)).includes('所以這台電腦還是沒有副本') && byText(sec(sy, 0), '讀回正本(會順便讀已登入的平台)'), `讀回正本被取消:${status(sec(sy, 0))}`);
  // exit 0 但重讀還是沒有副本(寫本機快取失敗只印警告)
  script['pl pull --all --provider spotify'] = { events: [done] };
  byText(sec(sy, 0), '讀回正本(會順便讀已登入的平台)').click();
  await settle();
  check(status(sec(sy, 0)).includes('所以這台電腦還是沒有副本') && !status(sec(sy, 0)).includes('完成'), `成功了卻還是沒有副本:照實說:${status(sec(sy, 0))}`);
  // 一個音樂平台都沒登入:不送命令,指到帳號頁
  sy = await open({}, { google: { state: 'ok' }, spotify: { state: 'missing' }, apple: { state: 'missing' }, youtube: { state: 'missing' } }, data());
  script.export = noCopy;
  page.refresh();
  await settle();
  before = bodies.length;
  byText(sec(sy, 0), '讀回正本(會順便讀已登入的平台)').click();
  await settle();
  check(!sent().slice(before).some((c) => c.startsWith('pl pull')) && sec(sy, 0).textContent.includes('先到「帳號」登入任一個音樂平台'), '沒有登入任何平台:不送命令、指到帳號頁');

  // ── 進階:args 陣列、flag 在 -- 前面;只看變更 + exit 2 說 dry_note;去重的平台清單形式只送名稱、收尾說是檢查
  sy = await open({ 'pl sync --dry-run -- road trip': { events: [exit(2, 'Error: 待套用')] }, 'pl sync -- road trip': { events: [exit(2, 'Error: 待套用')] },
    'pl dedup -- apple:p.B': table(['POS', 'ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'], [['1', 'i.1', 'Song A', 'a', 'r', 'dup_id']]) });
  const adv = (k) => { let x = null; walk(sy, (c) => { if (!x && c.dataset?.adv === k) x = c; }); return x; };
  const advBox = allByClass(sy, 'sync__adv')[0];
  adv('name').value = 'road trip';
  adv('sync').click();
  await settle();
  check(sent().includes('pl sync --dry-run -- road trip') && status(advBox).includes('以上是會改的東西,還沒有寫入') && !status(advBox).includes('你按了取消'), `勾著只看變更:${status(advBox)}`);
  adv('dry').checked = false;
  adv('sync').click();
  await settle();
  check(sent().includes('pl sync -- road trip') && status(advBox).includes('你按了取消'), `沒勾只看變更、按了取消:${status(advBox)}`);
  adv('name').value = 'apple:p.B';
  adv('dry').checked = true;
  adv('provider').value = 'spotify';
  adv('dedup').click();
  await settle();
  check(sent().includes('pl dedup -- apple:p.B') && status(advBox).includes('這只是檢查') && !status(advBox).includes('已經是最新的'), `去重的平台清單形式:不帶旗標、收尾說是檢查:${JSON.stringify(sent().slice(-3))} ${status(advBox)}`);
  // 去除重複名稱留空:挑選器選完、沒有重複(沒有表)時說「沒有重複的歌」,不說「完成」
  adv('name').value = '';
  adv('provider').value = '';
  script['pl dedup --dry-run'] = { events: [{ type: 'prompt', id: 9, kind: 'select', title: '?', options: [{ key: 'road trip', value: 'a' }] }, { type: 'prompt_closed', id: 9, reason: 'answered' }, done] };
  adv('dedup').click();
  await settle();
  check(status(advBox).includes('這次沒有要拿掉的重複') && status(advBox).includes('主控台') && !status(advBox).includes('完成') && !status(advBox).includes('沒有重複的歌'),
    `挑選器選完、沒有表:不斷定沒有重複(可能有平台沒檢查),指到主控台:${status(advBox)}`);
  adv('provider').value = 'apple';
  adv('pull').click();
  await settle();
  check(sent().includes('pl pull --all --provider apple --dry-run'), `留空 = 全部、平台在名稱前:${JSON.stringify(sent().slice(-3))}`);

  // ── 路由:#/sync/apple 預選 Apple 並讀它的清單;回到這一頁帶別的平台就換過去
  sy = await open({ 'pl list --provider apple': table(LH, [['p.B', 'mix', '-', '']]) }, AUTH, data(), 'apple');
  check(radios(sy, 'sync-plat')[1].checked === true && sent().includes('pl list --provider apple'), `#/sync/apple:預選 Apple 並讀它的清單:${JSON.stringify(sent())}`);
  page.refresh('spotify');
  await settle();
  check(sent().includes('pl list --provider spotify'), '回到這一頁帶 spotify:換過去並讀');
  // export 失敗(這台沒有副本)時,預選的平台也照樣讀清單(同手點平台卡)
  await open({ export: { events: [exit(1, 'Error: 本機沒有資料')] }, 'pl list --provider apple': table(LH, [['p.B', 'mix', '-', '']]) }, AUTH, data(), 'apple');
  check(sent().includes('pl list --provider apple'), `#/sync/apple、export 失敗:照樣讀 Apple 的清單:${JSON.stringify(sent())}`);

  // ── 英文:頁面沒有中文、label 是進行式
  i18nFile = './i18n-en.json';
  const labels = [];
  try {
    await loadI18n(api);
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label); return Console.prototype.run.call(con, line, hooks, opts); };
    sy = await open({ [`pl show ${SP2} --provider spotify`]: table(TH, [[SP1, 'Song A', 'Artist', 'Album', '215000']]) });
    await pickPlat(sy, 'spotify');
    await pickList(sy, 'Workout');
    byText(sec(sy, 1), 'Check for duplicates')?.click();
    await settle();
    const texts = [];
    walk(sy, (c) => { if (c._text) texts.push(c._text); if (c.placeholder) texts.push(c.placeholder); });
    const CJK = /[　-〿㐀-鿿＀-￯]/;
    for (const x of [...texts, ...labels]) check(!CJK.test(x), `en:同步頁還有中文:「${x}」`);
    for (const x of labels) check(/^[A-Z][a-z]*ing /.test(x), `en:label 要是進行式:「${x}」`);
    check(labels.length >= 5 && texts.some((x) => x.includes('Start syncing a playlist')), `en:流程真的跑過:${labels.length}`);
  } finally {
    delete con.run;
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 8v. 合併連結(計畫 2026-10-01 §2.5、§4 情境 11,T4):連結面板的「連到 X 上已經有的清單」與同步頁的同名卡打開確認卡;
//     卡上每一句的出現條件(改名只在名字不同且不是本機、Apple 揭露在目標是 Apple 或正本連著 Apple 時);下拉裡連著別的正本的列
//     不能選;三個命令(pl link --merge → resolve → pl sync,有連著的平台沒登入時一家一家同步);第一步停下說沒合併沒連上、
//     第三步停下說已經合併並連上;搬家完成頁的「保持同步」帶目的平台、精靈遇到同名有歌的正本時指向同步頁。
await scenario('8v', async () => {
  const { initSync } = await import('./pages/sync.mjs');
  const SP1 = '4uLU6hMCjMI75M1A2tKUQC', SP2 = '7qiZfU4dY1lWllzX7mPBI3', SP3 = '37i9dQZF1DXcBWIGoYBM5M';
  const walk = (n, f) => { for (const c of n.children || []) { f(c); walk(c, f); } };
  const byText = (n, text) => { let b = null; if (n) walk(n, (c) => { if (!b && c.tagName === 'BUTTON' && c.textContent === text) b = c; }); return b; };
  const idle = () => new Promise((r) => con.idle(r));
  const settle = async () => { for (let k = 0; k < 4; k++) { await idle(); await tick(5); } };
  const out = (o) => ({ events: [{ type: 'stdout', text: JSON.stringify(o) }, done] });
  const table = (header, rows, end = done) => ({ events: [{ type: 'table', header, rows }, end] });
  const exit = (code, message = '', reason = 'done') => ({ type: 'exit', code, message, reason });
  const answered = (id) => [{ type: 'prompt', id, kind: 'confirm', title: '?' }, { type: 'prompt_closed', id, reason: 'answered' }];
  const LH = ['ID', 'NAME', 'TRACKS', 'OWNER'];
  const AUTH = { google: { state: 'ok', device_id: 'devA' }, spotify: { state: 'ok' }, apple: { state: 'ok' }, youtube: { state: 'ok', channel_id: 'UCme' } };
  const data = { 'pl__a.json': { pid: 'a', name: 'road trip', links: { spotify: SP3 }, items: [{ cid: 'c1' }, { cid: 'c1' }, { cid: 'c2' }] },
    'pl__x.json': { pid: 'x', name: 'other', links: { apple: 'p.T' }, items: [{ cid: 'c1' }] },
    'tracks.json': { tracks: { c1: { title: 'Song A' }, c2: { title: 'Song B' } } } };
  const providers = { list: ['spotify', 'apple', 'local', 'youtube'], current: 'spotify' };
  const radios = (r, name) => { const o = []; walk(r, (c) => { if (c.tagName === 'INPUT' && c.type === 'radio' && c.name === name) o.push(c); }); return o; };
  const sec = (sy, i) => allByClass(sy, 'sync__sec')[i];
  const status = (box) => allByClass(box, 'pl__flow-status').map((x) => x.textContent).join(' | ');
  const sent = () => bodies.filter((b) => b.args).map((b) => b.args.join(' '));
  const open = async (more = {}, auth = AUTH) => {
    reset();
    script = { 'auth status --json': out(auth), export: out(data), 'pl list --provider spotify': table(LH, [[SP3, 'road trip', '3', 'me']]),
      'pl list --provider apple': table(LH, [['p.W', '上班聽', '-', ''], ['p.T', 'taken', '-', '']]), ...more };
    const sy = mk();
    initSync(sy, api, con, () => {}, providers, 'spotify');
    await settle();
    radios(sy, 'sync-list')[0].l.change();
    await settle();
    return sy;
  };
  const appleRow = (sy) => allByClass(sec(sy, 1), 'pl__link').find((r) => r.dataset.platform === 'apple');

  // ── 從連結面板打開確認卡:讀 Apple 的清單、連著別的正本的列不能選、卡上照實說
  let sy = await open();
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  const card = allByClass(sec(sy, 1), 'pl__flow-card').find((c) => c.textContent.includes('把正本「road trip」連到 Apple Music 上已經有的清單'));
  const opts = card ? card.querySelector('select').children : [];
  check(card && sent().includes('pl list --provider apple') && opts.length === 2 && opts[1].disabled && opts[1].textContent.includes('連著正本「other」') && !opts[0].disabled,
    `確認卡:讀 Apple 的清單、連著別的正本的不能選:${card && card.textContent.slice(0, 60)}`);
  const facts = card ? allByClass(card, 'sync__merge-facts')[0].textContent : '';
  check(facts.includes('「上班聽」也會改名為「road trip」') && facts.includes('正本原本的 3 首一首都不動') && facts.includes('可能也會進你的 Apple Music 資料庫'), `卡上的事:改名(名字不同)、正本首數、Apple 揭露:${facts}`);
  check(!sent().some((c) => c.startsWith('pl link')), '只打開卡,還沒送命令');

  // ── 三個命令,提示畫在同步頁;做完說完成並提醒去除重複
  script['pl link --merge a apple:p.W'] = { events: [...answered(1), done] };
  script['resolve a'] = { events: [done] };
  script['pl sync a'] = { events: [...answered(2), done] };
  byText(card, '開始合併').click();
  await settle();
  check(calls.slice(calls.lastIndexOf('pl sync a')).includes('export'), `合併完成後重讀本機資料:${JSON.stringify(calls.slice(-4))}`);
  check(JSON.stringify(sent().filter((c) => /^(pl link|resolve|pl sync)/.test(c))) === JSON.stringify(['pl link --merge a apple:p.W', 'resolve a', 'pl sync a']), `合併的三個命令:${JSON.stringify(sent())}`);
  check(status(sec(sy, 1)).includes('完成:「上班聽」已經併進「road trip」並連上') && status(sec(sy, 1)).includes('去除重複') && globalThis.location.hash === '', `合併完成:${status(sec(sy, 1))}`);

  // ── 第一步停下(取消 / 上限):沒合併、沒連上;第三步停下:已經合併並連上、下一步是同步這份清單
  for (const [spec, want] of [[{ events: [exit(2, 'Error: 待套用')] }, '沒有合併、也沒有連上。'], [{ events: [exit(3, 'Error: 這次會刪掉 12 首,超過閾值')] }, '沒有合併、也沒有連上。']]) {
    sy = await open({ 'pl link --merge a apple:p.W': spec });
    byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
    await settle();
    byText(sec(sy, 1), '開始合併').click();
    await settle();
    check(status(sec(sy, 1)).includes(want) && !sent().includes('resolve a'), `第一步停下:${status(sec(sy, 1))}`);
  }
  sy = await open({ 'pl link --merge a apple:p.W': { events: [...answered(1), done] }, 'resolve a': { events: [done] }, 'pl sync a': { events: [exit(2, 'Error: 待套用')] } });
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  byText(sec(sy, 1), '開始合併').click();
  await settle();
  check(status(sec(sy, 1)).includes('已經把「上班聽」合併並連上「road trip」') && status(sec(sy, 1)).includes('capy pl unlink'), `第三步停下:照實說已經合併並連上:${status(sec(sy, 1))}`);
  // 前置檢查(寫不進去):照貼原因,不再給「開始合併」
  sy = await open({ 'pl link --merge a apple:p.W': { events: [exit(1, 'Error: apple:p.W 寫不進去(協作清單),不能合併')] } });
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  byText(sec(sy, 1), '開始合併').click();
  await settle();
  check(status(sec(sy, 1)).includes('寫不進去') && !status(sec(sy, 1)).includes('Error:') && !byText(sec(sy, 1), '開始合併'), `前置檢查擋下:照貼原因、不再給開始:${status(sec(sy, 1))}`);

  // ── 第三步:有連著的平台這台沒登入時一家一家同步(正本連著 Spotify、這台的 Spotify 過期;從 Apple 上同名的清單進合併)
  reset();
  script = { 'auth status --json': out({ ...AUTH, spotify: { state: 'expired' } }), export: out(data), 'pl list --provider apple': table(LH, [['p.W', 'road trip', '-', '']]),
    'pl show p.W --provider apple': table(['ID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'], []),
    'pl link --merge a apple:p.W': { events: [...answered(1), done] }, 'resolve a': { events: [done] }, 'pl sync a --provider apple': { events: [done] } };
  sy = mk();
  initSync(sy, api, con, () => {}, providers, 'apple');
  await settle();
  radios(sy, 'sync-list')[0].l.change();
  await settle();
  byText(sec(sy, 1), '連到正本「road trip」').click();
  await settle();
  check(sec(sy, 1).textContent.includes('「road trip」0 首(剛從 Apple Music 讀的)。'), `Apple 的曲數是 -:確認卡讀 pl show 補上首數:${sec(sy, 1).textContent.slice(0, 160)}`);
  byText(sec(sy, 1), '開始合併').click();
  await settle();
  check(status(sec(sy, 1)).includes('這台有登入的平台也同步了') && status(sec(sy, 1)).includes('沒有同步的平台:Spotify。'), `只同步了一部分:照實說、列出沒同步的平台:${status(sec(sy, 1))}`);
  check(sent().includes('pl sync a --provider apple') && !sent().includes('pl sync a') && !sent().includes('pl sync a --provider spotify'), `第三步:Spotify 過期,只同步 Apple:${JSON.stringify(sent())}`);

  // ── 同步頁的同名卡:連到正本(預選那一份)
  const d2 = { ...data, 'pl__w.json': { pid: 'w', name: '上班聽', links: { spotify: SP1 }, items: [{ cid: 'c1' }] } };
  reset();
  script = { 'auth status --json': out(AUTH), export: out(d2), 'pl list --provider apple': table(LH, [['p.Z', 'zzz', '1', ''], ['p.W', '上班聽', '-', '']]), 'pl show p.W --provider apple': table(['ID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'], []) };
  sy = mk();
  initSync(sy, api, con, () => {}, providers, 'apple');
  await settle();
  radios(sy, 'sync-list')[1].l.change();
  await settle();
  const sameCard = allByClass(sec(sy, 1), 'pl__flow-card')[0];
  check(sameCard.dataset.state === 'same_name' && sameCard.textContent.includes('要讓兩份一起同步') && byText(sameCard, '連到正本「上班聽」'), `同名卡:給連到正本:${sameCard.textContent.slice(0, 80)}`);
  byText(sameCard, '連到正本「上班聽」').click();
  await settle();
  const mc = allByClass(sec(sy, 1), 'pl__flow-card').find((c) => c.querySelector('select'));
  check(mc && mc.querySelector('select').value === 'p.W' && !allByClass(mc, 'sync__merge-facts')[0].textContent.includes('也會改名'), '同名卡打開的確認卡預選那一份、名字一樣就不說改名');

  // ── 確認卡的首數:TRACKS 是數字就直接用、不讀 pl show;改名照精確比較(只差大小寫也會改);目標不是 Apple 但正本連著 Apple 也揭露
  const openApple = async (more = {}) => {
    reset();
    script = { 'auth status --json': out(AUTH), export: out(data), 'pl list --provider apple': table(LH, [['p.W', '上班聽', '-', ''], ['p.T', 'taken', '-', '']]),
      'pl list --provider spotify': table(LH, [[SP3, 'road trip', '3', 'me'], [SP2, 'Other', '7', 'me']]), ...more };
    const s = mk();
    initSync(s, api, con, () => {}, providers, 'apple');
    await settle();
    radios(s, 'sync-list')[1].l.change(); // taken:連著正本「other」
    await settle();
    return s;
  };
  const mergeCard = (s) => allByClass(sec(s, 1), 'pl__flow-card').find((c) => c.querySelector('select'));
  const factsOf = (c) => (c ? allByClass(c, 'sync__merge-facts')[0].textContent : '');
  const linkRow = (s, p) => allByClass(sec(s, 1), 'pl__link').find((r) => r.dataset.platform === p);
  sy = await openApple();
  byText(linkRow(sy, 'spotify'), '連到 Spotify 上已經有的清單').click();
  await settle();
  let mcx = mergeCard(sy);
  check(mcx && mcx.querySelector('select').value === SP2 && mcx.textContent.includes('「Other」7 首(剛從 Spotify 讀的)。') && !sent().some((c) => c.startsWith('pl show ' + SP2)),
    `TRACKS 是數字:直接用、不讀 pl show:${mcx && mcx.textContent.slice(0, 160)}`);
  check(factsOf(mcx).includes('「Other」也會改名為「other」') && factsOf(mcx).includes('可能也會進你的 Apple Music 資料庫'), `只差大小寫也改名、正本連著 Apple 就揭露:${factsOf(mcx)}`);
  sy = await open({ 'pl list --provider youtube': table(LH, [['UCme/PLy', 'road trip', '2', 'me']]) });
  byText(linkRow(sy, 'youtube'), '連到 YouTube Music 上已經有的清單').click();
  await settle();
  mcx = mergeCard(sy);
  check(mcx && !factsOf(mcx).includes('Apple Music') && !factsOf(mcx).includes('也會改名'), `目標 YouTube、正本沒連 Apple:不揭露、同名不改名:${factsOf(mcx)}`);

  // ── 平台上沒有清單:說「沒有清單」,不說「每一份都連著別的正本」
  sy = await open({ 'pl list --provider apple': table(LH, []) });
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  check(sec(sy, 1).textContent.includes('Apple Music 上沒有清單。') && !sec(sy, 1).textContent.includes('每一份都已經連著'), `平台上沒有清單:${sec(sy, 1).textContent.slice(0, 120)}`);

  // ── 第一步被中止:可能停在寫入途中,不說「沒有合併、也沒有連上」
  sy = await open({ 'pl link --merge a apple:p.W': { events: [exit(130, '', 'cancelled')] } });
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  byText(sec(sy, 1), '開始合併').click();
  await settle();
  check(status(sec(sy, 1)).includes('已中止') && !status(sec(sy, 1)).includes('沒有合併'), `第一步中止:不斷定沒寫:${status(sec(sy, 1))}`);

  // ── 讀清單排隊:預覽(pl show)還在跑時按「連到…」,排到才讀,不被序列槽擋成「讀不到」
  const [gq, relq] = gate();
  reset();
  script = { 'auth status --json': out(AUTH), export: out(data), 'pl list --provider spotify': table(LH, [[SP3, 'road trip', '3', 'me']]),
    'pl list --provider apple': table(LH, [['p.W', '上班聽', '-', '']]), ['pl show ' + SP3 + ' --provider spotify']: { gate: gq, events: [done] } };
  sy = mk();
  initSync(sy, api, con, () => {}, providers, 'spotify');
  await settle();
  radios(sy, 'sync-list')[0].l.change();
  await tick(20);
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await tick(20);
  check(!calls.includes('pl list --provider apple') && sec(sy, 1).textContent.includes('正在讀 Apple Music 上的清單'), `預覽還在跑:先排隊、說在讀:${JSON.stringify(calls)}`);
  relq();
  await settle();
  check(calls.includes('pl list --provider apple') && mergeCard(sy) && !sec(sy, 1).textContent.includes('讀不到'), `排到之後照讀、畫確認卡:${sec(sy, 1).textContent.slice(0, 120)}`);
  // 讀的時候換了清單:讀完不畫在新清單的區塊裡
  const [gl, rell] = gate();
  sy = await open({ 'pl list --provider spotify': table(LH, [[SP3, 'road trip', '3', 'me'], [SP1, 'zzz', '1', 'me']]),
    'pl list --provider apple': { gate: gl, events: [{ type: 'table', header: LH, rows: [['p.W', '上班聽', '-', '']] }, done] } });
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await tick(20);
  radios(sy, 'sync-list')[1].l.change();
  rell();
  await settle();
  check(!mergeCard(sy) && !sec(sy, 1).textContent.includes('正在讀 Apple Music 上的清單'), `讀的時候換了清單:不畫在新的區塊:${sec(sy, 1).textContent.slice(0, 120)}`);

  // ── 寫入流程收尾後,這裡自己讀的平台清單作廢:下一張卡重讀(剛合併、剛建的那份要看得到,#138 review 第 1 點)
  sy = await open({ export: out({ ...data, 'pl__z.json': { pid: 'z', name: 'zzz', links: { spotify: SP1 }, items: [{ cid: 'c1' }] } }),
    'pl list --provider spotify': table(LH, [[SP3, 'road trip', '3', 'me'], [SP1, 'zzz', '1', 'me']]),
    'pl link --merge a apple:p.W': { events: [...answered(1), done] }, 'resolve a': { events: [done] }, 'pl sync a': { events: [done] } });
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  byText(sec(sy, 1), '開始合併').click();
  await settle();
  radios(sy, 'sync-list')[1].l.change(); // zzz:連著另一份正本
  await settle();
  byText(appleRow(sy), '連到 Apple Music 上已經有的清單').click();
  await settle();
  check(calls.filter((c) => c === 'pl list --provider apple').length === 2 && mergeCard(sy), `合併之後再開確認卡:重讀 Apple 的清單:${JSON.stringify(calls.filter((c) => c.startsWith('pl list')))}`);

  // ── 在 X 建一份:那個平台有同名、有歌的清單(只差大小寫也算)→ 不給「開始」,給「連到那一份」並預選它;不送 pl link --new-only
  sy = await open({ 'pl list --provider apple': table(LH, [['p.W', '上班聽', '-', ''], ['p.R', 'Road Trip', '-', '']]) });
  byText(appleRow(sy), '在 Apple Music 建一份').click();
  await settle();
  const dupCard = allByClass(sec(sy, 1), 'pl__flow-card').find((c) => c.textContent.includes('capy 不會再建一份同名的'));
  check(dupCard && dupCard.textContent.includes('Apple Music 上已經有叫「road trip」的清單') && !byText(dupCard, '開始') && byText(dupCard, '連到 Apple Music 上已經有的清單'),
    `建一份撞同名:改給連到那一份:${dupCard && dupCard.textContent.slice(0, 120)}`);
  byText(dupCard, '連到 Apple Music 上已經有的清單').click();
  await settle();
  mcx = mergeCard(sy);
  check(mcx && mcx.querySelector('select').value === 'p.R' && factsOf(mcx).includes('「Road Trip」也會改名為「road trip」') && !sent().some((c) => c.startsWith('pl link')),
    `撞同名打開的確認卡預選那一份、還沒送命令:${JSON.stringify(sent())}`);

  // ── 搬家:完成頁的「保持同步」帶目的平台;同名、有歌的正本 → 指向同步頁
  const { initMove } = await import('./pages/move.mjs');
  globalThis.document.createElementNS ||= (_, tag) => mk(tag);
  const lists = (rows) => (h) => { h.onTable(LH, rows); h.onExit(0, '', 'done'); };
  let mig = null;
  const plan = {
    'auth status --json': (h) => { h.onStdout(JSON.stringify({ spotify: { state: 'ok', client_id: 'set' }, google: { state: 'ok', client: 'builtin' }, apple: { state: 'ok', developer_token: 'ok', user_token: 'ok' } })); h.onExit(0, '', 'done'); },
    'pl list --provider spotify': lists([['p1', 'Road trip', '2', 'me']]),
    'pl list --provider apple': lists([['q1', 'road trip', '5', '']]),
    export: (h) => { h.onStdout(JSON.stringify({ 'pl__r.json': { pid: 'r', name: 'Road Trip', links: { spotify: 'p1' }, items: [{ cid: 'c1' }] } })); h.onExit(0, '', 'done'); },
    'migrate p1 --from spotify --to apple:q1': (h) => { mig = h; },
  };
  const fcon = { idle: (fn) => fn(), run(_line, hooks, o) { plan[(o.args || []).join(' ')]?.(hooks); return Promise.resolve(); } };
  const mv = mk();
  initMove(mv, api, fcon, () => {}, { list: ['spotify', 'apple'] });
  radios(mv, 'wiz-to')[1].l.change();
  radios(mv, 'wiz-from')[0].l.change();
  byText(mv, t('webui.move.next.playlist')).click();
  radios(mv, 'wiz-src')[0].l.change();
  check(mv.textContent.includes('搬家加進它會被擋下') && (() => { let ok = false; walk(mv, (c) => { if (c.tagName === 'A' && c.href === '#/sync/apple') ok = true; }); return ok; })(), `精靈:同名、有歌的正本 → 指向同步頁:${mv.textContent.slice(0, 40)}`);
  // 同名、有歌的正本:不預選「加進它」,下拉停在「請選一份」、下一步不能按;人挑了才走得下去
  const dsel = mv.querySelector('select');
  check(dsel && dsel.value === '' && dsel.children[0].textContent === '請選一份' && byText(mv, t('webui.move.next.confirm')).disabled, `精靈:同名有歌的正本不預選目的地:${dsel && dsel.value}`);
  // 先點「建新的」再點回「加進既有的」:一樣停在「請選一份」,不替人選到會被擋下的那份(#138 review 第 2 點)
  radios(mv, 'wiz-dst')[0].l.change();
  radios(mv, 'wiz-dst')[1].l.change();
  check(mv.querySelector('select')?.value === '' && byText(mv, t('webui.move.next.confirm')).disabled, `精靈:切回加進既有的仍停在請選一份:${mv.querySelector('select')?.value}`);
  const dsel2 = mv.querySelector('select');
  dsel2.value = 'q1';
  dsel2.l.change();
  byText(mv, t('webui.move.next.confirm')).click();
  byText(mv, t('webui.move.start')).click();
  mig?.onTable(['DIR', 'ACTION', 'PROVIDER', 'PLAYLIST', 'POS', 'CID', 'PROVIDER_ID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'], [['migrate', 'add', 'spotify', 'Road trip', '0', 'a', SP1, 'Song A', 'a', 'r', 'push']]);
  mig?.onExit(0, '', 'done');
  let keep = null;
  walk(mv, (c) => { if (c.tagName === 'A' && c.textContent === '讓兩邊之後保持同步 →') keep = c; });
  check(keep && keep.href === '#/sync/apple', `搬家完成:保持同步帶目的平台:${keep && keep.href}`);
  // 正本已經連著那一份(migrate 會沿用)、或連著目的平台的另一份(同步頁也接不起來):不算,照常預選同名的那一份
  for (const [links, why] of [[{ spotify: 'p1', apple: 'q1' }, '已經連著那一份'], [{ spotify: 'p1', apple: 'q9' }, '連著目的平台的另一份']]) {
    plan.export = (h) => { h.onStdout(JSON.stringify({ 'pl__r.json': { pid: 'r', name: 'Road Trip', links, items: [{ cid: 'c1' }] } })); h.onExit(0, '', 'done'); };
    const m = mk();
    initMove(m, api, fcon, () => {}, { list: ['spotify', 'apple'] });
    radios(m, 'wiz-to')[1].l.change();
    radios(m, 'wiz-from')[0].l.change();
    byText(m, t('webui.move.next.playlist')).click();
    radios(m, 'wiz-src')[0].l.change();
    check(m.querySelector('select')?.value === 'q1' && !m.textContent.includes('搬家加進它會被擋下') && m.textContent.includes('所以預設加進它'), `精靈:正本${why}:照常預選、不指向同步頁`);
  }
  // 精靈讀 export 是背景判斷:讀不到(第一次同步前的常態)不出提示,目的地照同名預選
  reset();
  script = { 'auth status --json': out(AUTH), export: { events: [exit(1, 'Error: 還沒有本機資料')] },
    'pl list --provider spotify': table(LH, [['p1', 'Road trip', '2', 'me']]), 'pl list --provider apple': table(LH, [['q1', 'road trip', '5', '']]) };
  const mv2 = mk();
  initMove(mv2, api, con, () => {}, { list: ['spotify', 'apple'] });
  await settle();
  radios(mv2, 'wiz-to')[1].l.change();
  radios(mv2, 'wiz-from')[0].l.change();
  byText(mv2, t('webui.move.next.playlist')).click();
  await settle();
  radios(mv2, 'wiz-src')[0].l.change();
  await settle();
  check(calls.includes('export') && !notices.some((n) => n.includes('還沒有本機資料')) && mv2.querySelector('select')?.value === 'q1', `精靈讀不到 export:不出提示、照同名預選:${JSON.stringify(notices)}`);

  // ── 英文:合併的 label 是進行式、卡上沒有中文
  i18nFile = './i18n-en.json';
  const labels = [];
  try {
    await loadI18n(api);
    con.run = (line, hooks, opts = {}) => { labels.push(opts.label); return Console.prototype.run.call(con, line, hooks, opts); };
    sy = await open({ 'pl link --merge a apple:p.W': { events: [...answered(1), done] }, 'resolve a': { events: [done] }, 'pl sync a': { events: [done] } });
    byText(appleRow(sy), 'Link to an existing playlist on Apple Music').click();
    await settle();
    byText(sec(sy, 1), 'Start merging').click();
    await settle();
    const texts = [];
    walk(sec(sy, 1), (c) => { if (c._text) texts.push(c._text); });
    const CJK = /[　-〿㐀-鿿＀-￯]/;
    for (const x of [...texts.filter((x) => !x.includes('上班聽')), ...labels.filter((x) => !x.includes('上班聽'))]) check(!CJK.test(x), `en:合併流程還有中文:「${x}」`);
    for (const x of labels) check(/^[A-Z][a-z]*ing /.test(x), `en:label 要是進行式:「${x}」`);
    check(labels.some((x) => x.startsWith('Merging ')) && labels.some((x) => x.startsWith('Matching ')), `en:合併與找對應的 label:${JSON.stringify(labels)}`);
  } finally {
    delete con.run;
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

// 8m. 搜尋頁的 Spotify 列連回 Spotify(計畫 2026-09-24 §1.7 S7;Spotify 的設計規範:顯示 Spotify 的曲名、歌手就要連回 Spotify、
//     帶 Spotify 的名稱):每一列有「在 Spotify 上聽」,連到 open.spotify.com/track/<id>、開新分頁、noopener,報讀說得出是哪一首;
//     local file(spotify:local:…)沒有 Spotify 上的頁面,不給連結;Apple 列沒有這個連結。播放鈕照舊是那一列的第一個按鈕。
await scenario('8m', async () => {
  const { initSearch } = await import('./pages/search.mjs');
  const paint = async (prov, rows) => {
    reset();
    script = { [`search k --provider ${prov} --limit 10`]: { events: [{ type: 'table', header: ['ID', 'TITLE'], rows }, done] } };
    const r = mk();
    initSearch(r, api, con, () => {}, { list: ['spotify', 'apple', 'youtube'], current: prov });
    r.querySelector('input').value = 'k';
    r.querySelector('.btn--primary').click();
    await new Promise((res) => con.idle(res));
    return r.querySelector('.tbl-wrap').querySelectorAll('tbody tr').map((tr) => ({ link: tr.querySelector('a'), first: tr.querySelector('.btn--ghost') }));
  };
  const sp = await paint('spotify', [['4uLU6hMCjMI75M1A2tKUQC', 'Never Gonna'], ['spotify:local:a:b:c:1', 'Local']]);
  const a = sp[0].link;
  check(a && a.href === 'https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC' && a.target === '_blank' && a.rel === 'noopener noreferrer',
    `Spotify 列要連回 Spotify、開新分頁:${a && JSON.stringify({ href: a.href, target: a.target, rel: a.rel })}`);
  check(a && a.textContent === '在 Spotify 上聽' && a.getAttribute('aria-label') === '在 Spotify 上聽「Never Gonna」', `連結的字與報讀:${a && a.textContent} ${a && a.getAttribute('aria-label')}`);
  check(sp[0].first && sp[0].first.tagName === 'BUTTON' && sp[0].first.textContent === '播放', `播放鈕照舊是第一個:${sp[0].first && sp[0].first.tagName}`);
  check(sp[1].link === null, 'local file 沒有 Spotify 上的頁面:不給連結');
  const ap = await paint('apple', [['700050031', 'Radioactivity']]);
  check(ap[0].link === null, 'Apple 列沒有 Spotify 連結');
  const yt = (await paint('youtube', [['vid-9', 'Tune']]))[0].link;
  check(yt && yt.textContent === '在 YouTube Music 開啟' && yt.getAttribute('aria-label') === '在 YouTube Music 開啟: Tune' && yt.href === 'https://music.youtube.com/watch?v=vid-9' && yt.target === '_blank',
    `搜尋頁的 YouTube 連結照舊用 youtubeLink 的預設字:${yt && [yt.textContent, yt.getAttribute('aria-label'), yt.href]}`);

  // 報讀名稱要以看得到的字開頭(WCAG 2.5.3 Label in Name):用語音控制說「Listen on Spotify」的人才點得到。英文是正式預設。
  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const en = (await paint('spotify', [['4uLU6hMCjMI75M1A2tKUQC', 'Never Gonna']]))[0].link;
    check(en && en.textContent === 'Listen on Spotify' && en.getAttribute('aria-label').startsWith(en.textContent) && en.getAttribute('aria-label').includes('Never Gonna'),
      `en:報讀名稱要以看得到的字開頭:${en && en.textContent} / ${en && en.getAttribute('aria-label')}`);
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
  if (typeof CustomEvent === 'function') check(globalThis.document.events?.at(-1) === 'capy:now', `render 後要廣播 capy:now(歌曲 wiki 頁聽它):${globalThis.document.events}`);
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
    providers: { spotify: { tracks: [{ id: 'sp1', title: 'x', artists: ['a'], url: 'https://open.spotify.com/track/sp1' }] }, apple: { tracks: [] },
      local: { tracks: [{ id: 'l1', title: 'z', artists: ['c'], url: 'javascript:alert(1)' }] } }, // 只給 https:同 player.js
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
  check(zh.rows.join('|') === '✓ 已登入|⚠ 已過期|· 未登入|⚠ 讀取 keychain 失敗', `四列的狀態看 state(YouTube Music 沒有 youtube 段 = 未登入):${zh.rows}`);
  check(stateOf('apple', parseStatus('spotify:\n  refresh token: keychain 存在\n').apple).text === '未登入', '讀不懂(不是 JSON)就是未登入,不回頭解析文字');
  check(zh.acct.includes('Google Drive(保管你的清單)') && zh.acct.includes('重新連接') && zh.acct.includes('client ID 已設定') && !zh.acct.includes('client_id'), `帳號頁的 zh-TW:${zh.acct}`);
  check(zh.running === '檢查中…' && zh.doctor.includes('還沒檢查過。按「開始檢查」。'), `診斷頁的 zh-TW:${zh.running} ${zh.doctor}`);
  for (const s of ['ISRC 查詢', 'ISRC 是每首歌的國際編號。輸入一個,看它在三個平台上分別是哪一首。', '查詢', '台灣', '年份(推測)', '在 Spotify 開啟', '這個平台沒有符合的曲目', '(不可得) · 95 分 · isrc · 已釘選', '含這首的清單(1)']) {
    check(zh.isrc.includes(s), `ISRC 頁的 zh-TW 少了「${s}」:${zh.isrc}`);
  }
  check(!zh.isrc.includes('在 本機曲庫 開啟'), `不是 https 的網址不給連結:${zh.isrc}`);

  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const en = await draw();
    const all = [en.acct, en.doctor, en.running, en.isrc].join('\n');
    check(!/[\p{Script=Han}　-〿＀-￯]/u.test(all), `英文畫出來不可以有中文字:${all}`);
    check(en.rows.join('|') === "✓ Logged in|⚠ Expired|· Not logged in|⚠ Couldn't read the keychain", `英文的四列狀態:${en.rows}`);
    check(JSON.stringify(en.labels) === JSON.stringify(['Checking account connections', 'Checking config, logins and connections']) && /^Switching /.test(t('webui.lang.switching')),
      `英文的 label 是進行式:${JSON.stringify(en.labels)} / ${t('webui.lang.switching')}`);
    check(en.running === 'Checking…' && en.isrc.includes('ISRC lookup') && en.isrc.includes('Look up') && en.isrc.includes('Taiwan') && en.isrc.includes('Open in Spotify') && en.isrc.includes('(unavailable) · confidence 95 · isrc · pinned'),
      `診斷與 ISRC 頁的英文:${en.running} ${en.isrc}`);
  } finally {
    i18nFile = './i18n.json';
    await loadI18n(api);
  }
});

// 14b. 帳號頁的細節欄(i18n T3 自審):auth status --json 的事實畫成給人看、跟著語系的句子,沒有的事實不畫(整欄都沒有就是 —);
//      JSON 的欄名與列舉值(client_id: / missing / developer_token …)不上畫面。到期時間用這台電腦的時區
//      (預期值用同一台機器的 Date 算,不靠 TZ:CI 的 Windows 不一定吃 TZ 環境變數)。四列的順序是 Spotify、Apple Music、YouTube Music、Google Drive;
//      YouTube Music 的細節是登入時記下的帳號名與 handle(決策 60),channel_id 不上畫面。
await scenario('14b', async () => {
  const { initAccount } = await import('./pages/account.mjs');
  const fixtures = [
    { spotify: { state: 'ok', client_id: 'set' },
      google: { state: 'ok', client: 'builtin', access_token_expiry: '2026-09-24T05:00:00Z', email: 'me@example.com', device_id: 'dev1' },
      apple: { state: 'ok', developer_token: 'ok', developer_token_expiry: '2026-10-01T04:30:00Z', user_token: 'ok', storefront: 'tw' },
      youtube: { state: 'ok', account: 'Someone', handle: '@someone', channel_id: 'UC1' } },
    { spotify: { state: 'keychain_error', client_id: 'malformed' }, google: { state: 'missing', client: 'none' },
      apple: { state: 'expired', developer_token: 'expired', developer_token_expiry: '2026-01-01T00:00:00Z', user_token: 'missing' } },
    { spotify: { state: 'missing', client_id: 'missing' }, google: { state: 'keychain_error', client: 'config' },
      apple: { state: 'keychain_error', developer_token: 'keychain_error', user_token: 'keychain_error' }, youtube: { state: 'keychain_error' } },
  ];
  const p2 = (n) => String(n).padStart(2, '0');
  const local = (iso) => { const d = new Date(iso); return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`; };
  const [valid, expired] = [local('2026-10-01T04:30:00Z'), local('2026-01-01T00:00:00Z')];
  const want = {
    'zh-TW': [['不知道登入何時失效(用舊版的 capy 登入的;Spotify 的登入六個月失效,按「重新連接」之後就會顯示日期) · client ID 已設定', `developer token 有效至 ${valid} · 商店地區:台灣`, 'Someone (@someone)', 'me@example.com'],
      ['client ID 格式不對', `developer token 已於 ${expired} 過期`, '—', '—'], ['—', '—', '—', '—']],
    en: [['Login expiry unknown (you logged in with an older version of capy; Spotify logins expire after six months, and the date shows up after you press "Reconnect") · client ID set', `developer token valid until ${valid} · store region: Taiwan`, 'Someone (@someone)', 'me@example.com'],
      ['client ID has the wrong format', `developer token expired on ${expired}`, '—', '—'], ['—', '—', '—', '—']],
  };
  const raw = ['client_id', 'developer_token', 'user_token', 'access_token', 'device_id', 'dev1', 'storefront', 'missing', 'keychain_error', 'builtin', 'malformed', 'T04:30', ': ok', ': set', 'channel_id', 'UC1'];
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

// 14c. 帳號頁的 Spotify 登入到期(決策 56):門檻與天數同 auth status 的文字版——剩的時間在 10 天以內就提醒(比時間長度,
//      不比天數),天數無條件進位,過了說大概已失效;已登入卻沒有到期欄位 = 不知道;沒登入不說。要處理的那一列標成 ⚠。
await scenario('14c', async () => {
  const { initAccount, spotifyRenewal } = await import('./pages/account.mjs');
  const DAY = 24 * 60 * 60 * 1000;
  const now = Date.UTC(2026, 8, 28, 12, 0, 0);
  const at = (ms) => new Date(now + ms).toISOString();
  const p2 = (n) => String(n).padStart(2, '0');
  const day = (iso) => { const d = new Date(iso); return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`; };
  const cases = [
    [150 * DAY, `登入約 ${day(at(150 * DAY))} 失效(剩 150 天)`, false],
    [10 * DAY + 60 * 60 * 1000, `登入約 ${day(at(10 * DAY + 3600000))} 失效(剩 11 天)`, false], // 10 天又 1 小時:進位成 11 天、還不提醒
    [10 * DAY, `登入約 ${day(at(10 * DAY))} 失效(剩 10 天):請在那之前按「重新連接」`, true],
    [-DAY, `登入大概已在 ${day(at(-DAY))} 左右失效:請按「重新連接」`, true],
  ];
  for (const [ms, text, warn] of cases) {
    const r = spotifyRenewal({ state: 'ok', refresh_token_expiry: at(ms) }, now);
    check(r && r.text === text && r.warn === warn, `剩 ${ms / DAY} 天:${JSON.stringify(r)}`);
  }
  const unknown = spotifyRenewal({ state: 'ok' }, now);
  check(unknown && unknown.text.startsWith('不知道登入何時失效') && unknown.warn === false, `已登入、沒有到期欄位 = 不知道:${JSON.stringify(unknown)}`);
  check(spotifyRenewal({ state: 'missing', refresh_token_expiry: at(-DAY) }, now) === null && spotifyRenewal({ state: 'keychain_error' }, now) === null && spotifyRenewal(undefined, now) === null,
    '沒登入(或 keychain 讀不到)不說到期');
  // 日期用這台電腦的時區:UTC 午夜前後各一個,離 UTC 半小時以上的時區至少有一個跟 UTC 的日期不同(UTC 的機器上兩者一樣,驗不出來)
  for (const iso of ['2026-11-01T23:30:00Z', '2026-11-02T00:30:00Z']) {
    const r = spotifyRenewal({ state: 'ok', refresh_token_expiry: iso }, now);
    check(r && r.text.startsWith(`登入約 ${day(iso)} 失效`), `日期是本機時區的:${iso} → ${r && r.text}`);
  }

  // 畫出來:快到期的那一列標成 ⚠、細節欄有日期與按鈕名;遠的照舊 ✓。(頁面用真的時鐘:到期時間從現在往後算)
  const draw = (expiry) => {
    const r = mk();
    const st = { spotify: { state: 'ok', client_id: 'set', refresh_token_expiry: expiry }, google: { state: 'missing', client: 'none' }, apple: { state: 'missing', developer_token: 'missing', user_token: 'missing' } };
    initAccount(r, null, { run: (_c, h) => { h.onStdout(JSON.stringify(st)); h.onExit(0, ''); }, idle: (fn) => fn() }, () => {});
    const row = allByClass(r, 'acct')[0];
    const b = allByClass(row, 'btn')[0];
    return { state: row.dataset.state, mark: allByClass(row, 'acct__state')[0].textContent, detail: allByClass(row, 'acct__detail')[0].textContent, button: b.textContent, ghost: b.classList.contains('btn--ghost') };
  };
  // 那一句叫人按的「重新連接」要真的是那一列按鈕的字;要處理的那一列按鈕不做成 ghost
  const soon = draw(new Date(Date.now() + 3 * DAY).toISOString());
  check(soon.state === 'warn' && soon.mark === '⚠ 已登入' && soon.detail.includes('(剩 3 天):請在那之前按「重新連接」') && soon.button === '重新連接' && !soon.ghost, `快到期:${JSON.stringify(soon)}`);
  const gone = draw(new Date(Date.now() - DAY).toISOString());
  check(gone.state === 'warn' && gone.mark === '⚠ 已過期' && gone.detail.includes('左右失效:請按「重新連接」') && gone.button === '重新連接', `大概已失效:狀態不說「已登入」:${JSON.stringify(gone)}`);
  const far = draw(new Date(Date.now() + 150 * DAY).toISOString());
  check(far.state === 'ok' && far.mark === '✓ 已登入' && far.detail.startsWith('登入約 ') && far.detail.includes('(剩 150 天)') && far.button === '重新連接' && far.ghost, `還早:${JSON.stringify(far)}`);

  i18nFile = './i18n-en.json';
  try {
    await loadI18n(api);
    const en = [spotifyRenewal({ state: 'ok', refresh_token_expiry: at(DAY / 2) }, now), spotifyRenewal({ state: 'ok', refresh_token_expiry: at(150 * DAY) }, now), spotifyRenewal({ state: 'ok', refresh_token_expiry: at(-DAY) }, now)];
    check(en[0].text === `Login expires around ${day(at(DAY / 2))} (1 day left): press "Reconnect" before then` && en[1].text.endsWith('(150 days left)') && en[2].text.startsWith('Login probably expired around'),
      `英文(單複數):${JSON.stringify(en)}`);
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

// 16. wiki setup 的自訂標頭欄(決策 59):multiline + secret → textarea(不是 input)、遮字標記、一行提示;
//     Enter 不送出、Ctrl / ⌘+Enter 送出,多行的值原樣進 answer body;非 multiline 的 secret 欄照舊是 password input。
await scenario('16', async () => {
  i18nFile = './i18n.json'; // 上一組把目錄清空了(目錄讀不到的情境):這裡要畫的是 zh-TW 的提示
  await loadI18n(api);
  const before = answers.length;
  const [g, release] = gate();
  script = {
    w: {
      first: [{ type: 'prompt', id: 1, kind: 'form', title: 'AI', fields: [
        { name: 'base_url', label: 'Base URL', value: 'https://x/v1' },
        { name: 'api_key', label: 'Key', secret: true },
        { name: 'headers', label: 'Headers', secret: true, multiline: true },
      ] }],
      gate: g,
      events: [{ type: 'prompt_closed', id: 1, reason: 'answered' }, done],
    },
    onAnswer: release,
  };
  const p = con.run('w');
  await tick(20);
  const block = root.children[root.children.length - 1];
  const form = allByClass(block, 'prompt')[0];
  const inputs = allByClass(form, 'prompt__input');
  check(inputs.length === 3 && inputs[0].tagName === 'INPUT' && inputs[1].tagName === 'INPUT' && inputs[1].type === 'password' && inputs[2].tagName === 'TEXTAREA',
    `三個欄位:input、password input、textarea:${inputs.map((i) => i.tagName + ':' + (i.type || '')).join(',')}`);
  check('secret' in inputs[2].dataset && inputs[2].rows === 3, 'multiline 的 secret 欄要標 data-secret(CSS 遮字)、三行高');
  check(find(form, '.prompt__hint')?.textContent === '一行一個;Ctrl+Enter(Mac 是 ⌘+Enter)送出', `多行欄的提示:${find(form, '.prompt__hint')?.textContent}`);
  inputs[2].value = 'CF-Access-Client-Id: a\nCF-Access-Client-Secret: b';
  inputs[1].value = 'k';
  inputs[2].l.keydown({ key: 'Enter', preventDefault() {} });
  await tick(20);
  check(answers.length === before, 'textarea 裡的 Enter 是換行,不送出');
  inputs[2].l.keydown({ key: 'Enter', ctrlKey: true, preventDefault() {} });
  await p;
  const sent = answers[answers.length - 1];
  check(answers.length === before + 1 && sent.value.headers === 'CF-Access-Client-Id: a\nCF-Access-Client-Secret: b' && sent.value.api_key === 'k' && sent.value.base_url === 'https://x/v1',
    `Ctrl+Enter 送出、多行原樣:${JSON.stringify(sent?.value)}`);
});

// 17. 歌曲 wiki 頁的渲染器(決策 59):stdout 事件切在行中間也要湊成整行;"## " 標題、"- " 清單、**粗體**、空行分段;
//     連結只認 https(javascript: / http: 不成連結);全部是 createElement / textContent。
await scenario('17', async () => {
  const { lineSplitter, wikiRenderer } = await import('./pages/wiki.mjs');
  const lines = [];
  const sp = lineSplitter((l) => lines.push(l));
  sp.push('## Ba'); sp.push('sics\nThe so'); sp.push('ng\n\n- one\n'); sp.push('tail'); sp.end();
  check(JSON.stringify(lines) === JSON.stringify(['## Basics', 'The song', '', '- one', 'tail']), `切在行中間也要湊成整行:${JSON.stringify(lines)}`);
  const out = mk();
  const r = wikiRenderer(out);
  for (const l of ['## 基本資料', '1997 年,**五月天** 的歌。', '- 一', '- 二', '', '- 三', 'MV: https://www.youtube.com/results?search_query=x。 javascript:alert(1) http://plain']) r.line(l);
  const kinds = out.children.map((c) => c.tagName);
  check(JSON.stringify(kinds) === JSON.stringify(['H3', 'P', 'UL', 'UL', 'P']), `節點形狀(空行結束清單):${JSON.stringify(kinds)}`);
  check(out.children[0].textContent === '基本資料', `標題去掉 ## :${out.children[0].textContent}`);
  const strong = find(out.children[1], 'strong');
  check(strong?.textContent === '五月天' && out.children[1].textContent === '1997 年,五月天 的歌。', `粗體:${out.children[1].textContent}`);
  check(out.children[2].children.length === 2 && out.children[3].children.length === 1, '清單項數');
  const links = allByClass(out, 'wiki__link');
  check(links.length === 1 && links[0].href === 'https://www.youtube.com/results?search_query=x' && links[0].target === '_blank' && links[0].rel === 'noopener noreferrer',
    `只有 https 成連結、句尾的全形句號不算進網址:${links.map((a) => a.href)}`);
  check(out.children[4].textContent === 'MV: https://www.youtube.com/results?search_query=x。 javascript:alert(1) http://plain', `其餘照原文:${out.children[4].textContent}`);
});
// 18. 歌曲 wiki 頁的命令(決策 59):「介紹這首歌」帶播放列當下的平台;「再問一次」對「查這首」重新讀面板的平台
//     (面板換了平台就帶新的,不帶舊的),對手打的歌名照原樣加 --refresh;手打的歌名走 --title / --artist(有空白就加引號)。
await scenario('18', async () => {
  const { initWiki } = await import('./pages/wiki.mjs');
  const root = mk('section');
  const player = { last: { provider: 'apple', track: { title: '派對動物', artists: ['五月天'] } } };
  script = {};
  initWiki(root, api, con, (x) => notices.push(x), { list: ['spotify', 'apple'], current: 'spotify' }, player);
  const buttons = () => allByClass(root, 'btn');
  const before = calls.length;
  buttons()[0].click(); // 介紹這首歌
  await tick(30);
  check(calls[before] === 'wiki --provider apple', `查這首帶面板上的平台:${calls[before]}`);
  player.last = { provider: 'spotify', track: { title: 'Yellow', artists: ['Coldplay'] } };
  const refresh = buttons().find((b) => b.textContent === '再問一次(不用快取的回答)');
  check(!!refresh, '做完要長出「再問一次」');
  refresh?.click();
  await tick(30);
  check(calls[before + 1] === 'wiki --refresh --provider spotify', `再問一次重新讀面板的平台:${calls[before + 1]}`);
  const inputs = allByClass(root, 'in');
  inputs[0].value = '"Heroes"'; inputs[1].value = 'David Bowie';
  buttons()[1].click(); // 查詢:走 args 陣列,雙引號與空白原樣到達(#115 review 第 1 點)
  await tick(30);
  check(JSON.stringify(bodies.at(-1).args) === JSON.stringify(['wiki', '--title', '"Heroes"', '--artist', 'David Bowie']) && bodies.at(-1).line === undefined,
    `手打的歌名走 args、原樣:${JSON.stringify(bodies.at(-1))}`);
  buttons().find((b) => b.textContent === '再問一次(不用快取的回答)')?.click();
  await tick(30);
  check(JSON.stringify(bodies.at(-1).args) === JSON.stringify(['wiki', '--refresh', '--title', '"Heroes"', '--artist', 'David Bowie']), `手打的再問一次照原樣:${JSON.stringify(bodies.at(-1).args)}`);
  check(JSON.stringify(bodies.at(-3).args) === JSON.stringify(['wiki', '--refresh', '--provider', 'spotify']) && JSON.stringify(bodies.at(-4).args) === JSON.stringify(['wiki', '--provider', 'apple']),
    `查這首與它的再問一次也走 args:${JSON.stringify(bodies.slice(-4).map((b) => b.args))}`);
  check(root.hidden === false && allByClass(root, 'wiki__now-line')[0]?.textContent === 'Spotify · Yellow — Coldplay', `正在播那一行跟著面板:${allByClass(root, 'wiki__now-line')[0]?.textContent}`);
});
flush();
if (failures.length) {
  say(`${failures.length} 條不成立`);
  process.exit(1);
}
say('ok');
