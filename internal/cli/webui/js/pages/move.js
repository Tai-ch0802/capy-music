// move.js:#/move —— 搬家精靈(首頁,決策 45–48)。三步:選路線 → 選清單 → 確認並搬家。
// 頁面只組一條 `capy migrate <來源清單 ID> --from X --to Y[:<目標 ID>]`(走 args 陣列,不經 splitArgs),
// 預覽表、逐筆裁決、最終確認都是 CLI 自己送來的:**絕不代加 --yes / --force,也不自動回答確認**(決策 46)。
// 提示畫在精靈裡(promptHost),不把人丟進主控台。上面的路線示意是裝飾,不是進度(決策 47)。
import { el, btn, providerName, emptyState } from './common.js';
import { parseStatus, stateOf } from './account.js';
import { renderTable } from '../table.js';
import { STAGES } from '../console.js';

// 首頁的每一句主張都要查得到出處(決策 48):MIT LICENSE、憑證只進鑰匙圈、migrate 只新增不刪來源、順序不動(決策 38)。
const FACTS = ['免費', '開源(MIT)', '在你自己的電腦上執行', '不刪來源,只新增'];
const READ_ONLY = ['apple'];    // 目前只讀,不能當目的地(gate R-8)
const CAN_CREATE = ['spotify']; // 只有它能新建清單;其餘只能加進既有的
const NO_LOGIN = ['local'];
// 下面兩個字面是 migrate.go 的原文,精靈靠它們認出「逐筆裁決」那一則與「這一首推得過去」;
// TestWebMoveWizardKeysOnMigrateWording 兩邊一起釘,CLI 改字測試就紅。
const REVIEW_MARK = '現在逐筆裁決?';
const PUSHABLE_MARK = '推到 ';

const SVG = 'http://www.w3.org/2000/svg';
const svg = (tag, attrs) => {
  const x = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) x.setAttribute(k, v);
  return x;
};

// 水豚:圓角矩形與圓組成的單色線條(Q40);跟終端機那隻 ASCII 水豚同一個構圖:方頭、兩耳、圓眼、鼻子、叼一根草。
function capybara() {
  const s = svg('svg', { viewBox: '0 0 128 88', class: 'capy-svg', role: 'img', 'aria-label': '水豚' });
  s.append(
    svg('rect', { class: 'capy-svg__line', x: 16, y: 4, width: 20, height: 18, rx: 8 }),
    svg('rect', { class: 'capy-svg__line', x: 84, y: 4, width: 20, height: 18, rx: 8 }),
    svg('rect', { class: 'capy-svg__head', x: 8, y: 14, width: 104, height: 68, rx: 24 }),
    svg('circle', { class: 'capy-svg__eye', cx: 38, cy: 42, r: 5 }),
    svg('circle', { class: 'capy-svg__eye', cx: 82, cy: 42, r: 5 }),
    svg('rect', { class: 'capy-svg__line', x: 40, y: 54, width: 40, height: 20, rx: 10 }),
    svg('circle', { class: 'capy-svg__dot', cx: 53, cy: 64, r: 2 }),
    svg('circle', { class: 'capy-svg__dot', cx: 67, cy: 64, r: 2 }),
    svg('line', { class: 'capy-svg__line', x1: 80, y1: 68, x2: 124, y2: 60 }),
  );
  return s;
}

// 表的欄位:DIR ACTION PROVIDER PLAYLIST POS CID PROVIDER_ID TITLE ARTISTS REASON(TestWebMoveWizardKeysOnMigrateWording 釘住欄名)。
// 兩條路的列長得不一樣(migrate.go;review #68):「加進既有清單」接進去的每一首都是 migrate 列、ACTION 永遠是 add,
// 推不出去只寫在 REASON;「新建清單」時正本既有的曲目是 push 列(add / skip),正本已連著來源時甚至一列 migrate 都沒有。
// 所以兩種列都吃、以 CID 去重:沒搬到 = ACTION 是 skip,或 migrate 列的 REASON 不以「推到 」開頭;其餘都算搬了。
export function tally(h, rows) {
  if (!h || !rows) return { moved: 0, missed: [] };
  const [dir, action, cid, title, artists, reason] = ['DIR', 'ACTION', 'CID', 'TITLE', 'ARTISTS', 'REASON'].map((k) => h.indexOf(k));
  const songs = new Map();
  for (const r of rows) {
    if (r[dir] !== 'migrate' && r[dir] !== 'push') continue;
    const miss = r[action] === 'skip' || (r[dir] === 'migrate' && !String(r[reason] || '').startsWith(PUSHABLE_MARK));
    const seen = songs.get(r[cid]);
    if (!seen) songs.set(r[cid], { title: r[title], artists: r[artists], reason: r[reason], miss });
    else if (miss && !seen.miss) Object.assign(seen, { miss, reason: r[reason] });
  }
  const missed = [...songs.values()].filter((x) => x.miss);
  return { moved: songs.size - missed.length, missed };
}

export function initMove(root, api, con, notice, providers) {
  const state = {
    step: 1,
    from: providers.list.includes('apple') ? 'apple' : providers.list[0],
    to: 'spotify',
    status: null,               // auth status 解析後的三段;null = 還沒讀到
    src: null, srcLists: null,  // 選中的來源清單 { id, name, count } / 來源平台的清單
    dst: { mode: 'new', id: '' }, dstLists: null,
    listError: '', filter: '',  // filter:步驟二的過濾字串(放在 state 才活得過 render)
    closedBy: '',                  // 這一次最後一則提示是怎麼收的(prompt_closed 的 reason)
    running: false, preview: null, // 搬家那一次命令在跑 / 它送來的預覽表 { h, rows }
    progress: null,                // 伺服器送來的最新一筆進度 { stage, done, total };沒有就不畫進度條
    result: null,                  // 跑完之後 { code, msg, moved, missed }
  };

  // ── 開場 + 路線示意 ──
  const hero = el('header', 'hero hero--split');
  const intro = el('div');
  intro.appendChild(el('h1', 'hero__title', '把歌單搬過去,一首都不用重找。'));
  intro.appendChild(el('p', 'hero__sub',
    'capy 讀出你在一個平台的播放清單,在另一個平台找到同樣的歌,照原本的順序放好。它在你自己的電腦上執行,你的帳號不經過任何人的伺服器。'));
  const facts = el('ul', 'facts');
  for (const f of FACTS) facts.appendChild(el('li', 'fact', f));
  intro.appendChild(facts);
  const route = el('div', 'route');
  route.setAttribute('role', 'img');
  const routeFrom = el('div', 'route__end');
  const routeTo = el('div', 'route__end');
  const lane = el('div', 'route__lane');
  lane.setAttribute('aria-hidden', 'true');
  for (let i = 0; i < 3; i++) {
    const n = el('span', 'route__note');
    n.appendChild(el('span', 'route__chip', '♪'));
    lane.appendChild(n);
  }
  lane.appendChild(capybara());
  route.append(routeFrom, lane, routeTo);
  hero.append(intro, route);

  // ── 精靈 ──
  const panel = el('section', 'panel wiz');
  const steps = el('ol', 'wiz__steps');
  const stepEls = ['選路線', '選清單', '確認並搬家'].map((t) => {
    const li = el('li', 'wiz__step', t);
    steps.appendChild(li);
    return li;
  });
  const body = el('div', 'wiz__body');
  const prompts = el('div', 'wiz__prompts'); // 提示就地回答的地方(promptHost)
  panel.append(steps, body, prompts);

  root.append(hero, panel, how(), truths(), foot());

  // 會問人的命令都帶同一組選項:提示畫進精靈;「逐筆裁決」那一則旁邊補一句白話(原文照留)。
  // 伺服器問的話逐字照留(規格 §9),這裡只在旁邊補白話;不替使用者回答任何一則。
  const onPrompt = (ev, box) => {
    let help = '';
    if (String(ev.title || '').includes(REVIEW_MARK)) {
      help = '有幾首歌在目的地找不到完全一樣的。按「套用」可以一首一首挑;按「取消」就先搬找得到的,其餘之後可以再處理。這一步不會寫入任何東西。';
    } else if (ev.kind === 'confirm' && state.running && state.preview) {
      help = '這是最後一次確認:上面列的就是要搬的歌。按「套用」才會開始寫入;按「取消」什麼都不會改。';
    }
    if (help) box.insertBefore(el('p', 'prompt__help', help), box.firstChild);
  };

  const connected = (id) => {
    if (NO_LOGIN.includes(id)) return { mark: '✓', text: '不需要登入', kind: 'ok' };
    if (!state.status) return { mark: '·', text: '檢查中…', kind: 'muted' };
    return stateOf(id, state.status[id]);
  };

  function loadStatus() {
    let text = '';
    con.run('', {
      onStdout: (t) => { text += t; },
      onExit: () => { state.status = parseStatus(text); render(); },
    }, { args: ['auth', 'status'], label: '檢查帳號的連接狀態', promptHost: prompts });
  }

  function connect(id) {
    prompts.replaceChildren();
    con.run('', { onPrompt, onExit: () => loadStatus() },
      { args: ['auth', 'login', id], label: `連接 ${providerName(id)}`, promptHost: prompts });
  }

  // 讀一個平台的清單(pl list 的表:ID / 名稱 / 曲數 / 擁有者)。
  function loadLists(prov, done) {
    let rows = null;
    con.run('', {
      onTable: (h, r) => {
        const [id, name, count] = [h.indexOf('ID'), h.indexOf('名稱'), h.indexOf('曲數')];
        rows = r.map((x) => ({ id: x[id], name: x[name], count: x[count] }));
      },
      onExit: (code, msg) => done(code === 0 ? rows || [] : null, msg),
    }, { args: ['pl', 'list', '--provider', prov], label: `讀取 ${providerName(prov)} 上的清單`, promptHost: prompts });
  }

  function enterStep2() {
    state.step = 2; state.src = null; state.srcLists = null; state.dstLists = null; state.listError = ''; state.filter = '';
    state.dst = { mode: CAN_CREATE.includes(state.to) ? 'new' : 'existing', id: '' };
    render();
    loadLists(state.from, (src, msg) => {
      if (!src) { state.listError = msg || `讀不到 ${providerName(state.from)} 的清單`; render(); return; }
      state.srcLists = src; render();
      loadLists(state.to, (dst, msg2) => {
        if (!dst) { state.listError = msg2 || `讀不到 ${providerName(state.to)} 的清單`; render(); return; }
        state.dstLists = dst; render();
      });
    });
  }

  // 目的地已經有同名清單時 CLI 會擋(migrate.go):送出前自己比對,直接給「加進它」,不去解析那句錯誤字串。
  const sameName = () => (state.src && state.dstLists
    ? state.dstLists.find((x) => x.name.toLowerCase() === state.src.name.toLowerCase()) : null);

  function start() {
    prompts.replaceChildren();
    Object.assign(state, { result: null, preview: null, progress: null, closedBy: '', running: true });
    const target = state.dst.mode === 'new' ? state.to : `${state.to}:${state.dst.id}`;
    con.run('', {
      onPrompt,
      onPromptClosed: (ev) => { state.closedBy = ev.reason; },
      onTable: (h, rows) => { state.preview = { h, rows }; render(); },
      onProgress: (ev) => { state.progress = ev; if (!state.preview) render(); },
      onExit: (code, msg) => {
        const p = state.preview || {};
        Object.assign(state, { running: false, result: { code, msg, ...tally(p.h, p.rows) } });
        render();
      },
    }, {
      args: ['migrate', state.src.id, '--from', state.from, '--to', target],
      label: `把「${state.src.name}」搬到 ${providerName(state.to)}`,
      promptHost: prompts,
    });
    render();
  }

  function preview(h, rows) {
    const { moved, missed } = tally(h, rows);
    const box = el('div', 'wiz__preview');
    box.appendChild(el('p', 'wiz__count', `會搬 ${moved} 首` + (missed.length ? `,${missed.length} 首這次搬不過去` : '')));
    if (missed.length) box.appendChild(missedList(missed));
    const more = el('details', 'wiz__more');
    more.appendChild(el('summary', null, '看完整的變更表'));
    const wrap = el('div', 'tbl-wrap tbl-wrap--tall');
    wrap.appendChild(renderTable(h, rows));
    more.appendChild(wrap);
    box.appendChild(more);
    return box;
  }

  function missedList(missed) {
    const ul = el('ul', 'wiz__missed');
    for (const m of missed) {
      const li = el('li', null, `${m.title} — ${m.artists}`);
      li.appendChild(el('span', 'muted', ` · ${m.reason}`));
      ul.appendChild(li);
    }
    return ul;
  }

  // ── 各步的畫面 ──
  function render() {
    stepEls.forEach((li, i) => {
      li.dataset.state = i + 1 < state.step ? 'done' : i + 1 === state.step ? 'now' : 'todo';
      if (i + 1 === state.step) li.setAttribute('aria-current', 'step'); else li.removeAttribute('aria-current');
    });
    routeFrom.replaceChildren(el('span', 'route__role', '來源'), el('strong', 'route__name', providerName(state.from)));
    routeTo.replaceChildren(el('span', 'route__role', '目的地'), el('strong', 'route__name', providerName(state.to)));
    route.setAttribute('aria-label', `示意:把 ${providerName(state.from)} 的歌單搬到 ${providerName(state.to)}`);
    const a = document.activeElement;
    const group = a && body.contains(a) && a.type === 'radio' ? a.name : '';
    body.replaceChildren(...[step1, step2, step3][state.step - 1]());
    // 點了卡片 / 清單列會整段重畫:把焦點還給同一組單選裡選中的那一顆,不然它會掉回 <body>
    if (group) body.querySelector(`input[name="${group}"]:checked`)?.focus();
  }

  // 單選卡:真的 radio(方向鍵、表單語意都是原生的),藏起來,樣子由 label 扛(CSS 的 :has())。
  function radioCard(cls, name, checked, disabled, onPick, ...children) {
    const lab = el('label', cls);
    const r = el('input', 'sr-only');
    r.type = 'radio'; r.name = name; r.checked = checked; r.disabled = disabled;
    r.addEventListener('change', onPick);
    lab.append(r, ...children);
    return lab;
  }

  function pcard(id, role, disabledWhy) {
    const st = connected(id);
    const text = disabledWhy || (NO_LOGIN.includes(id) ? '不需要登入'
      : !state.status ? '檢查中…' : st.kind === 'ok' ? '已連接' : st.kind === 'warn' ? st.text : '需要連接');
    const mark = disabledWhy ? '' : st.kind === 'ok' ? '✓ ' : st.kind === 'warn' ? '⚠ ' : '· ';
    const line = el('span', 'pcard__state', mark + text);
    line.dataset.state = disabledWhy ? 'muted' : st.kind;
    return radioCard('pcard', 'wiz-' + role, !disabledWhy && state[role] === id, !!disabledWhy,
      () => { state[role] = id; render(); }, el('strong', 'pcard__name', providerName(id)), line);
  }

  function group(legend, role) {
    const fs = el('fieldset', 'pick');
    fs.appendChild(el('legend', 'pick__legend', legend));
    for (const id of providers.list) {
      fs.appendChild(pcard(id, role, role === 'to' && READ_ONLY.includes(id) ? '目前只能當來源' : ''));
    }
    return fs;
  }

  function step1() {
    const cols = el('div', 'wiz__cols');
    cols.append(group('從哪裡搬', 'from'), group('搬到哪裡', 'to'));
    const out = [el('h2', 'panel__title', '從哪裡搬到哪裡?'), cols];
    // 還沒連接的平台:各給一顆「連接」。Google Drive 是清單正本放的地方,搬家也需要它。
    const need = [...new Set([state.from, state.to, 'google'])]
      .filter((id) => !NO_LOGIN.includes(id) && state.status && connected(id).kind !== 'ok');
    for (const id of need) {
      const row = el('p', 'wiz__need');
      row.appendChild(el('span', null, id === 'google'
        ? 'capy 把清單的正本存在你自己的 Google Drive,搬家之前要先連接它。'
        : `${providerName(id)} 還沒連接。用的是你自己的帳號憑證,第一次設定要花幾分鐘。`));
      row.appendChild(btn(`連接 ${providerName(id)}`, '', () => connect(id)));
      out.push(row);
    }
    const same = state.from === state.to;
    if (same) out.push(el('p', 'page__warn', '來源和目的地要是不同的平台。'));
    const acts = el('div', 'form-row wiz__acts');
    const next = btn('下一步:選清單', 'btn--primary', enterStep2);
    next.disabled = same;
    acts.appendChild(next);
    out.push(acts);
    return out;
  }

  function step2() {
    const out = [el('h2', 'panel__title', `要搬 ${providerName(state.from)} 的哪一個清單?`)];
    const back = btn('上一步', 'btn--ghost', () => { state.step = 1; render(); });
    if (state.listError) {
      out.push(el('p', 'page__warn', state.listError));
      const acts = el('div', 'form-row wiz__acts');
      acts.append(back, btn('再試一次', '', enterStep2));
      out.push(acts);
      return out;
    }
    if (!state.srcLists) { out.push(skeleton()); return out; }
    if (!state.srcLists.length) {
      out.push(emptyState(`${providerName(state.from)} 上沒有可以搬的清單。`));
      const acts = el('div', 'form-row wiz__acts');
      acts.appendChild(back);
      out.push(acts);
      return out;
    }
    const list = el('fieldset', 'wiz__list');
    list.appendChild(el('legend', 'sr-only', '來源清單'));
    const draw = () => {
      const filter = state.filter.trim().toLowerCase();
      list.replaceChildren(list.firstChild);
      for (const p of state.srcLists.filter((x) => !filter || x.name.toLowerCase().includes(filter))) {
        list.appendChild(radioCard('pl__item', 'wiz-src', !!state.src && state.src.id === p.id, false,
          () => { state.src = p; render(); },
          el('span', 'pl__name', p.name), el('span', 'pl__count', p.count && p.count !== '-' ? `${p.count} 首` : '')));
      }
    };
    if (state.srcLists.length > 8) {
      const f = el('input', 'in wiz__filter');
      f.type = 'search'; f.placeholder = '過濾清單名稱'; f.setAttribute('aria-label', '過濾清單名稱');
      f.value = state.filter; // 挑了一個清單會整段重畫:過濾字串放在 state 才不會被洗掉(review #68)
      f.addEventListener('input', () => { state.filter = f.value; draw(); });
      out.push(f);
    }
    draw();
    out.push(list);

    // 放到哪裡:新建(只有能建清單的平台)或加進既有的。
    out.push(el('h3', 'card__sub', `放到 ${providerName(state.to)} 的哪裡?`));
    if (!state.dstLists) out.push(skeleton());
    else {
      const dup = sameName();
      if (dup && state.dst.mode === 'new') { state.dst = { mode: 'existing', id: dup.id }; }
      const where = el('div', 'wiz__where');
      const opt = (mode, text, enabled) => {
        const lab = el('label', 'wiz__opt');
        const r = el('input');
        r.type = 'radio'; r.name = 'wiz-dst'; r.checked = state.dst.mode === mode; r.disabled = !enabled;
        r.addEventListener('change', () => { state.dst = { mode, id: mode === 'existing' ? (state.dstLists[0] || {}).id || '' : '' }; render(); });
        lab.append(r, el('span', null, text));
        return lab;
      };
      const canNew = CAN_CREATE.includes(state.to) && !dup;
      where.appendChild(opt('new', CAN_CREATE.includes(state.to)
        ? '建一個同名的新清單(私人)'
        : `建一個新清單(${providerName(state.to)} 做不到,只能加進既有的)`, canNew));
      where.appendChild(opt('existing', '加進既有的清單,接在它原本的歌後面', state.dstLists.length > 0));
      if (state.dst.mode === 'existing') {
        const sel = el('select', 'in');
        sel.setAttribute('aria-label', '既有的清單');
        for (const p of state.dstLists) {
          const o = el('option', null, p.name);
          o.value = p.id;
          sel.appendChild(o);
        }
        sel.value = state.dst.id || (state.dstLists[0] || {}).id || '';
        state.dst.id = sel.value;
        sel.addEventListener('change', () => { state.dst.id = sel.value; });
        where.appendChild(sel);
      }
      out.push(where);
      if (dup) out.push(el('p', 'page__note', `${providerName(state.to)} 上已經有一個叫「${dup.name}」的清單,所以改成加進它;想放到別的清單可以在上面換。`));
    }
    const acts = el('div', 'form-row wiz__acts');
    const next = btn('下一步:確認', 'btn--primary', () => { state.step = 3; state.result = null; render(); });
    next.disabled = !state.src || !state.dstLists || (state.dst.mode === 'existing' && !state.dst.id);
    acts.append(back, next);
    out.push(acts);
    return out;
  }

  function step3() {
    const dstName = state.dst.mode === 'new'
      ? `${providerName(state.to)} 的新清單「${state.src.name}」`
      : `${providerName(state.to)} 的「${(state.dstLists.find((x) => x.id === state.dst.id) || {}).name || state.dst.id}」`;
    const out = [
      el('h2', 'panel__title', '確認並搬家'),
      el('p', 'wiz__sum', `把 ${providerName(state.from)} 的「${state.src.name}」搬到 ${dstName}。`),
    ];
    const r = state.result;
    const live = el('div', 'wiz__live');
    const acts = el('div', 'form-row wiz__acts');
    if (state.running) {
      // 進度是真的才畫(決策 47):這裡只有階段說明與預覽;做到哪裡看底部的執行狀態列,要停按那裡的「中止」。
      if (state.preview) live.appendChild(preview(state.preview.h, state.preview.rows));
      else {
        const p = state.progress;
        live.appendChild(el('p', 'wiz__stage', p ? (STAGES[p.stage] || p.stage) + (p.total > 0 ? ` ${p.done} / ${p.total}` : '…')
          : '正在準備…'));
        if (p && p.total > 0) {
          const bar = el('progress', 'bar');
          bar.max = p.total; bar.value = p.done;
          bar.setAttribute('aria-label', STAGES[p.stage] || p.stage);
          live.appendChild(bar);
        }
        live.appendChild(el('p', 'page__note', '清單越長越久。想停下來,按底部的「中止」。'));
      }
    } else if (!r) {
      out.push(el('p', 'page__note', '按下去之後會先比對、列出要搬的歌,再問你一次;你確認了才會寫入。來源的清單不會被更動,原本的順序也不會變。'));
      acts.append(btn('上一步', 'btn--ghost', () => { state.step = 2; render(); }), btn('開始搬家', 'btn--primary', start));
    } else if (r.code === 0) {
      live.appendChild(el('p', 'wiz__done', r.moved ? `搬好了:${r.moved} 首已經在 ${dstName} 裡。` : '目的地已經都有這些歌了,沒有需要搬的。'));
      if (r.missed.length) {
        live.appendChild(el('p', 'page__note', `${r.missed.length} 首在 ${providerName(state.to)} 找不到,這次沒搬:`));
        live.appendChild(missedList(r.missed));
      }
      acts.append(btn('再搬一個', 'btn--primary', () => { state.result = null; enterStep2(); }));
      const sync = el('a', 'wiz__link', '讓兩邊之後保持同步 →');
      sync.href = '#/sync';
      acts.appendChild(sync);
    } else {
      // 「取消」是 exit 2;關掉提示(✕)或等到逾時是 huh.ErrUserAborted → exit 1 + 英文的 user aborted(review #68)。
      // 三種都發生在寫入之前,都是同一種收尾;靠 prompt_closed 的 reason 分辨,不比對那句英文。
      // 「已中止」(底部的中止鈕)另外說:中止前可能已經開始寫入,但再搬一次不會重複。
      const stopped = r.msg === '已中止';
      const quit = stopped || r.code === 2 || (r.code === 1 && ['dismissed', 'timeout'].includes(state.closedBy));
      live.appendChild(el('p', quit ? 'wiz__stage' : 'page__warn',
        !quit ? (r.msg || '沒有完成。').replace(/^Error: /, '')
          : stopped ? '已中止。中止前如果已經開始寫入,可能只搬了一部分;再搬一次不會重複,已經在目的地的歌會自動略過。'
            : state.closedBy === 'timeout' ? '等太久沒有回答,這次已經取消,什麼都沒有寫入。' : '已取消,什麼都沒有寫入。'));
      if (!quit) {
        const c = el('a', 'wiz__link', '到主控台看完整的輸出 →');
        c.href = '#/console';
        live.appendChild(c);
      }
      acts.append(btn('上一步', 'btn--ghost', () => { state.step = 2; state.result = null; render(); }), btn('再試一次', 'btn--primary', start));
    }
    out.push(live, acts);
    return out;
  }

  render();
  con.idle(loadStatus); // 第一次進來時若有命令在跑,等它結束再讀
}

function skeleton() {
  const s = el('div', 'skeleton');
  s.setAttribute('aria-hidden', 'true');
  for (let i = 0; i < 3; i++) s.appendChild(el('span', 'skeleton__bar'));
  return s;
}

// ── 它怎麼搬的:三拍,左右交錯,每拍一個小示意(裝飾) ──
function how() {
  const sec = el('section', 'how');
  sec.appendChild(el('h2', 'how__title', '它怎麼搬的'));
  const beats = [
    ['讀', '讀出清單裡的每一首', '從來源平台把清單讀出來:歌名、歌手、專輯,還有每首歌的國際編號(ISRC)。來源的清單只讀不改。', 'read'],
    ['對', '在目的地找到同一首歌', '先用國際編號找,同一個錄音在兩個平台上是同一個編號;沒有編號的才用歌名、歌手與長度比對。沒把握的不會亂猜,會列出來問你。', 'match'],
    ['建', '照原本的順序放進去', '在目的地建一個同名的清單(或加進你指定的清單),照來源的順序放好。你先看過要搬的歌,確認了才寫入。', 'build'],
  ];
  for (const [mark, title, text, kind] of beats) {
    const b = el('div', 'beat');
    const fig = el('div', `beat__fig beat__fig--${kind}`);
    fig.setAttribute('aria-hidden', 'true');
    if (kind === 'match') {
      fig.append(el('span', 'beat__card', '♪'), el('span', 'beat__ok', '✓'), el('span', 'beat__card', '♪'));
    } else {
      for (let i = 0; i < 3; i++) fig.appendChild(el('span', 'beat__row'));
    }
    const copy = el('div', 'beat__copy');
    copy.append(el('span', 'beat__mark', mark), el('h3', 'beat__title', title), el('p', 'beat__text', text));
    b.append(fig, copy);
    sec.appendChild(b);
  }
  return sec;
}

// ── 先說清楚的事:限制也是事實 ──
function truths() {
  const sec = el('section', 'truths');
  sec.appendChild(el('h2', 'how__title', '先說清楚的事'));
  const ul = el('ul', 'truths__list');
  for (const [k, v] of [
    ['不會刪你的東西', '來源的清單不會被更動;對目的地只新增,不移除、不重排。'],
    ['順序不會變', '你排的順序是你的記憶,capy 沒有任何路徑會打亂它。'],
    ['Apple Music 目前只能當來源', '可以從 Apple Music 搬出來,還不能搬進去。'],
    ['連接帳號要花幾分鐘', 'capy 用的是你自己的帳號憑證,存在這台電腦的鑰匙圈裡。沒有人替你代管,所以也沒有人能跟你收費。'],
    ['清單的正本在你的 Google Drive', 'capy 沒有伺服器。你隨時可以把那份資料清掉。'],
    ['硬碟裡的歌單也可以搬', '本機的 M3U 播放清單可以搬到 Spotify,不另外收費——因為本來就沒有收費。'],
  ]) {
    const li = el('li', 'truths__item');
    li.append(el('strong', null, k), el('span', null, v));
    ul.appendChild(li);
  }
  sec.appendChild(ul);
  return sec;
}

function foot() {
  const f = el('footer', 'move__foot');
  f.appendChild(el('span', null, 'capy 是開源的(MIT)。'));
  const a = el('a', null, '原始碼在 GitHub');
  a.href = 'https://github.com/Tai-ch0802/capy-music'; a.target = '_blank'; a.rel = 'noopener noreferrer';
  f.appendChild(a);
  return f;
}
