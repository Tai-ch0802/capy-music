// move.js:#/move —— 搬家精靈(首頁,決策 45–48)。三步:選路線 → 選清單 → 確認並搬家。
// 頁面只組一條 `capy migrate <來源清單 ID> --from X --to Y[:<目標 ID>]`(走 args 陣列,不經 splitArgs),
// 預覽表、逐筆裁決、最終確認都是 CLI 自己送來的:**絕不代加 --yes / --force,也不自動回答確認**(決策 46)。
// 提示畫在精靈裡(promptHost),不把人丟進主控台。上面的路線示意是裝飾,不是進度(決策 47)。
import { el, btn, providerName, emptyState } from './common.js';
import { parseStatus, stateOf } from './account.js';
import { renderTable } from '../table.js';
import { stages } from '../console.js';
import { t } from '../i18n.js';

// 首頁的每一句主張都要查得到出處(決策 48):MIT LICENSE、憑證只進鑰匙圈、migrate 只新增不刪來源、順序不動(決策 38)。
// 使用者看得到的字一律是函式、用到時才算(i18n.js 開頭的載入順序鐵則)。
const factTexts = () => [t('webui.move.fact.free'), t('webui.move.fact.open_source'), t('webui.move.fact.local'), t('webui.move.fact.no_delete')];
const READ_ONLY = [];                    // 不能當目的地的平台(目前沒有;Apple 自決策 49 起可寫)
const CAN_CREATE = ['spotify', 'apple']; // 能新建清單的平台;其餘(local)只能加進既有的
const NO_LOGIN = ['local'];
// 「逐筆裁決」那一則靠提示事件的 key 認(confirmWrite 帶的 i18n key;TestWebMigrateReviewPromptCarriesKey 釘住),
// 不比對跟著語系變的標題;「這一首推得過去」看 REASON_CODE 欄(見 tally)。

const SVG = 'http://www.w3.org/2000/svg';
const svg = (tag, attrs) => {
  const x = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) x.setAttribute(k, v);
  return x;
};

// 水豚(2026-09-20 重畫,跟終端機那隻 ASCII 水豚同一個構圖;長相的依據寫在 tui_capybara.go):
// 側面、朝右(朝著目的地)。頭像一塊圓角的磚、頭頂線跟背是同一條線(沒有脖子)、口鼻前端又鈍又方;眼睛緊貼著長在
// 頭後上方的小圓耳;鼻孔在最前上角;桶狀的身體、渾圓的屁股、沒有尾巴、腿短;嘴邊叼一根草。
// 舊版是正面圓臉 + 頭頂兩耳 + 橢圓鼻配兩個鼻孔——那是豬。單色線條、幾個基本形狀,不是手刻的長路徑。
// 尺寸是照螢幕上的大小訂的(review #71:10.5rem 寬、縮放 0.75):臉上的眼睛與鼻孔同一個量級、耳朵不可以比眼睛搶眼——
// 不然頭頂那顆圈會被讀成眼睛,而會眨的是另一顆。耳朵先畫、身體後畫:身體的底色蓋掉耳朵的下半,只剩線上的一個小凸起。
function capybara() {
  const s = svg('svg', { viewBox: '0 0 224 124', class: 'capy-svg', role: 'img', 'aria-label': t('webui.move.capybara') });
  s.append(
    svg('rect', { class: 'capy-svg__line', x: 30, y: 88, width: 20, height: 28, rx: 7 }),  // 後腿
    svg('rect', { class: 'capy-svg__line', x: 112, y: 88, width: 20, height: 28, rx: 7 }), // 前腿
    svg('ellipse', { class: 'capy-svg__line capy-svg__ear', cx: 128, cy: 29, rx: 5, ry: 7 }), // 耳朵:小、圓、在頭的後上方
    // 屁股 → 背與頭頂一條線平過去 → 又鈍又方的口鼻 → 下巴收回胸口(頭比身體淺)→ 肚子(Z 拉回屁股)
    svg('path', { class: 'capy-svg__line', d: 'M32 100 C10 100 6 74 14 58 C22 38 44 30 74 30 C110 30 140 32 172 32 Q194 32 194 54 L194 68 Q194 84 178 84 L160 84 C148 84 140 92 132 100 Z' }),
    svg('circle', { class: 'capy-svg__eye', cx: 150, cy: 48, r: 5 }),                      // 眼睛:高、貼著耳朵
    svg('ellipse', { class: 'capy-svg__dot', cx: 184, cy: 43, rx: 3.4, ry: 2.6 }),         // 鼻孔:最前上角
    svg('path', { class: 'capy-svg__stroke', d: 'M176 69 L187 68' }),                      // 嘴:貼在口鼻的內側,不碰輪廓
    svg('path', { class: 'capy-svg__stroke', d: 'M200 67 L220 61 M210 64 L215 57' }),      // 叼著的草:從口鼻的外面才開始
  );
  return s;
}

// 表的欄位:DIR ACTION PROVIDER PLAYLIST POS CID PROVIDER_ID TITLE ARTISTS REASON REASON_CODE
// (TestWebMoveWizardCapabilitiesAndHeadersMatchGo 釘住欄名)。REASON 跟著語系、只拿來顯示;判斷一律看 REASON_CODE。
// 兩條路的列長得不一樣(migrate.go;review #68):「加進既有清單」接進去的每一首都是 migrate 列、ACTION 永遠是 add,
// 推不出去只寫在原因欄;「新建清單」時正本既有的曲目是 push 列(add / skip),正本已連著來源時甚至一列 migrate 都沒有。
// 所以兩種列都吃、以 CID 去重:沒搬到 = ACTION 是 skip,或 migrate 列的 REASON_CODE 不是 push;其餘都算搬了。
export function tally(h, rows) {
  if (!h || !rows) return { moved: 0, missed: [] };
  const [dir, action, cid, title, artists, reason, code] = ['DIR', 'ACTION', 'CID', 'TITLE', 'ARTISTS', 'REASON', 'REASON_CODE'].map((k) => h.indexOf(k));
  const songs = new Map();
  for (const r of rows) {
    if (r[dir] !== 'migrate' && r[dir] !== 'push') continue;
    const miss = r[action] === 'skip' || (r[dir] === 'migrate' && r[code] !== 'push');
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
    result: null,                  // 跑完之後 { code, msg, reason, moved, missed }
  };

  // ── 開場 + 路線示意 ──
  const hero = el('header', 'hero hero--split');
  const intro = el('div');
  intro.appendChild(el('h1', 'hero__title', t('webui.move.hero.title')));
  intro.appendChild(el('p', 'hero__sub', t('webui.move.hero.sub')));
  const facts = el('ul', 'facts');
  for (const f of factTexts()) facts.appendChild(el('li', 'fact', f));
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
  const stepEls = [t('webui.move.step.route'), t('webui.move.step.playlist'), t('webui.move.step.confirm')].map((text) => {
    const li = el('li', 'wiz__step', text);
    steps.appendChild(li);
    return li;
  });
  const body = el('div', 'wiz__body');
  const prompts = el('div', 'wiz__prompts'); // 提示就地回答的地方(promptHost)
  panel.append(steps, body, prompts);

  root.append(hero, panel, how(), truths(), foot());

  // 會問人的命令都帶同一組選項:提示畫進精靈;「逐筆裁決」那一則旁邊補一句白話(原文照留)。
  // 伺服器問的話逐字照留(規格 §9),這裡只在旁邊補白話;不替使用者回答任何一則。
  // 白話裡說的按鈕名稱跟伺服器送來的確認鈕查同一個 key(changeset.confirm.*,web.go 的 webSharedKeys 送到頁面)。
  const onPrompt = (ev, box) => {
    const apply = t('changeset.confirm.apply'), cancel = t('changeset.confirm.cancel');
    let help = '';
    if (ev.key === 'migrate.confirm.review') {
      help = t('webui.move.help.review', { apply, cancel });
    } else if (ev.kind === 'confirm' && state.running && state.preview) {
      help = t('webui.move.help.final', { apply, cancel });
    }
    if (help) box.insertBefore(el('p', 'prompt__help', help), box.firstChild);
  };

  const connected = (id) => {
    if (NO_LOGIN.includes(id)) return { mark: '✓', text: t('webui.move.no_login'), kind: 'ok' };
    if (!state.status) return { mark: '·', text: t('webui.move.checking'), kind: 'muted' };
    return stateOf(id, state.status[id]);
  };

  function loadStatus() {
    let text = '';
    con.run('', {
      onStdout: (s) => { text += s; },
      onExit: () => { state.status = parseStatus(text); render(); },
    }, { args: ['auth', 'status', '--json'], label: t('webui.move.label.status'), promptHost: prompts });
  }

  function connect(id) {
    prompts.replaceChildren();
    con.run('', { onPrompt, onExit: () => loadStatus() },
      { args: ['auth', 'login', id], label: t('webui.move.connect', { provider: providerName(id) }), promptHost: prompts });
  }

  // 讀一個平台的清單(pl list 的表:ID / NAME / TRACKS / OWNER;欄名是機器欄位,不跟語系,決策 50)。
  function loadLists(prov, done) {
    let rows = null;
    con.run('', {
      onTable: (h, r) => {
        const [id, name, count] = [h.indexOf('ID'), h.indexOf('NAME'), h.indexOf('TRACKS')];
        rows = r.map((x) => ({ id: x[id], name: x[name], count: x[count] }));
      },
      onExit: (code, msg) => done(code === 0 ? rows || [] : null, msg),
    }, { args: ['pl', 'list', '--provider', prov], label: t('webui.move.label.lists', { provider: providerName(prov) }), promptHost: prompts });
  }

  function enterStep2() {
    state.step = 2; state.src = null; state.srcLists = null; state.dstLists = null; state.listError = ''; state.filter = '';
    state.dst = { mode: CAN_CREATE.includes(state.to) ? 'new' : 'existing', id: '' };
    render();
    loadLists(state.from, (src, msg) => {
      if (!src) { state.listError = msg || t('webui.move.err.lists', { provider: providerName(state.from) }); render(); return; }
      state.srcLists = src; render();
      loadLists(state.to, (dst, msg2) => {
        if (!dst) { state.listError = msg2 || t('webui.move.err.lists', { provider: providerName(state.to) }); render(); return; }
        state.dstLists = dst; defaultDst(); render();
      });
    });
  }

  // 目的地已經有同名清單時 CLI 會擋(migrate.go):送出前自己比對,直接給「加進它」,不去解析那句錯誤字串。
  // 目的地的預設:同名就「加進它」,否則能建就建新的。只在「剛挑了來源清單 / 目的地清單剛讀到」時套一次,
  // 之後使用者要換隨他——不可以每次重畫都改回來、也不可以因為同名就把「建新的」停用:CLI 的撞名判定比這裡嚴
  // (sameNamePlaylists 只算讀得到的;你追蹤的別人的同名清單不算),精靈比它嚴會把人關在沒有出路的分支裡(review #68)。
  const defaultDst = () => {
    const dup = sameName();
    state.dst = dup ? { mode: 'existing', id: dup.id } : { mode: CAN_CREATE.includes(state.to) ? 'new' : 'existing', id: '' };
  };

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
      onProgress: (ev) => { state.progress = ev; if (!state.preview) paintProgress(); },
      onExit: (code, msg, reason) => {
        const p = state.preview || {};
        Object.assign(state, { running: false, result: { code, msg, reason, ...tally(p.h, p.rows) } });
        render();
      },
    }, {
      args: ['migrate', state.src.id, '--from', state.from, '--to', target],
      label: t('webui.move.label.migrate', { name: state.src.name, provider: providerName(state.to) }),
      promptHost: prompts,
    });
    render();
  }

  function preview(h, rows) {
    const { moved, missed } = tally(h, rows);
    const box = el('div', 'wiz__preview');
    box.appendChild(el('p', 'wiz__count', t('webui.move.preview.count', { count: moved }) + (missed.length ? t('webui.move.preview.missed', { count: missed.length }) : '')));
    if (missed.length) box.appendChild(missedList(missed));
    const more = el('details', 'wiz__more');
    more.appendChild(el('summary', null, t('webui.move.preview.full_table')));
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

  // 比對階段每一首歌一個 progress 事件(三百首 = 三百個):就地改這兩個節點,不為了它重畫整個步驟(review #69)。
  // 進度條只在 total > 0 時出現,值只來自伺服器(決策 47)。
  const liveStage = el('p', 'wiz__stage');
  const liveBar = el('progress', 'bar');
  function paintProgress() {
    const p = state.progress;
    const name = p ? stages()[p.stage] || p.stage : '';
    liveStage.textContent = !p ? t('webui.move.progress.preparing') : name + (p.total > 0 ? ` ${p.done} / ${p.total}` : '…');
    liveBar.hidden = !(p && p.total > 0);
    if (!liveBar.hidden) { liveBar.max = p.total; liveBar.value = p.done; liveBar.setAttribute('aria-label', name); }
  }

  // ── 各步的畫面 ──
  function render() {
    stepEls.forEach((li, i) => {
      li.dataset.state = i + 1 < state.step ? 'done' : i + 1 === state.step ? 'now' : 'todo';
      if (i + 1 === state.step) li.setAttribute('aria-current', 'step'); else li.removeAttribute('aria-current');
    });
    routeFrom.replaceChildren(el('span', 'route__role', t('webui.move.route.from')), el('strong', 'route__name', providerName(state.from)));
    routeTo.replaceChildren(el('span', 'route__role', t('webui.move.route.to')), el('strong', 'route__name', providerName(state.to)));
    route.setAttribute('aria-label', t('webui.move.route.aria', { from: providerName(state.from), to: providerName(state.to) }));
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
    const text = disabledWhy || (NO_LOGIN.includes(id) ? t('webui.move.no_login')
      : !state.status ? t('webui.move.checking') : st.kind === 'ok' ? t('webui.move.pcard.connected') : st.kind === 'warn' ? st.text : t('webui.move.pcard.not_connected'));
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
      fs.appendChild(pcard(id, role, role === 'to' && READ_ONLY.includes(id) ? t('webui.move.source_only') : ''));
    }
    return fs;
  }

  function step1() {
    const cols = el('div', 'wiz__cols');
    cols.append(group(t('webui.move.pick.from'), 'from'), group(t('webui.move.pick.to'), 'to'));
    const out = [el('h2', 'panel__title', t('webui.move.step1.title')), cols];
    // 還沒連接的平台:各給一顆「連接」。Google Drive 是清單正本放的地方,搬家也需要它。
    const need = [...new Set([state.from, state.to, 'google'])]
      .filter((id) => !NO_LOGIN.includes(id) && state.status && connected(id).kind !== 'ok');
    for (const id of need) {
      const row = el('p', 'wiz__need');
      row.appendChild(el('span', null, id === 'google'
        ? t('webui.move.need.google')
        : t('webui.move.need.provider', { provider: providerName(id) })));
      row.appendChild(btn(t('webui.move.connect', { provider: providerName(id) }), '', () => connect(id)));
      out.push(row);
    }
    const same = state.from === state.to;
    if (same) out.push(el('p', 'page__warn', t('webui.move.same')));
    const acts = el('div', 'form-row wiz__acts');
    const next = btn(t('webui.move.next.playlist'), 'btn--primary', enterStep2);
    next.disabled = same;
    acts.appendChild(next);
    out.push(acts);
    return out;
  }

  function step2() {
    const out = [el('h2', 'panel__title', t('webui.move.step2.title', { provider: providerName(state.from) }))];
    const back = btn(t('webui.move.back'), 'btn--ghost', () => { state.step = 1; render(); });
    if (state.listError) {
      out.push(el('p', 'page__warn', state.listError));
      const acts = el('div', 'form-row wiz__acts');
      acts.append(back, btn(t('webui.move.retry'), '', enterStep2));
      out.push(acts);
      return out;
    }
    if (!state.srcLists) { out.push(skeleton()); return out; }
    if (!state.srcLists.length) {
      out.push(emptyState(t('webui.move.no_lists', { provider: providerName(state.from) })));
      const acts = el('div', 'form-row wiz__acts');
      acts.appendChild(back);
      out.push(acts);
      return out;
    }
    const list = el('fieldset', 'wiz__list');
    list.appendChild(el('legend', 'sr-only', t('webui.move.src_legend')));
    const draw = () => {
      const filter = state.filter.trim().toLowerCase();
      list.replaceChildren(list.firstChild);
      for (const p of state.srcLists.filter((x) => !filter || x.name.toLowerCase().includes(filter))) {
        list.appendChild(radioCard('pl__item', 'wiz-src', !!state.src && state.src.id === p.id, false,
          () => { state.src = p; defaultDst(); render(); },
          el('span', 'pl__name', p.name), el('span', 'pl__count', p.count && p.count !== '-' ? t('webui.move.track_count', { count: p.count }) : '')));
      }
    };
    if (state.srcLists.length > 8) {
      const f = el('input', 'in wiz__filter');
      f.type = 'search'; f.placeholder = t('webui.move.filter'); f.setAttribute('aria-label', t('webui.move.filter'));
      f.value = state.filter; // 挑了一個清單會整段重畫:過濾字串放在 state 才不會被洗掉(review #68)
      f.addEventListener('input', () => { state.filter = f.value; draw(); });
      out.push(f);
    }
    draw();
    out.push(list);

    // 放到哪裡:新建(只有能建清單的平台)或加進既有的。
    out.push(el('h3', 'card__sub', t('webui.move.where.title', { provider: providerName(state.to) })));
    if (!state.dstLists) out.push(skeleton());
    else {
      const dup = sameName();
      const where = el('div', 'wiz__where');
      const opt = (mode, text, enabled) => {
        const lab = el('label', 'wiz__opt');
        const r = el('input');
        r.type = 'radio'; r.name = 'wiz-dst'; r.checked = state.dst.mode === mode; r.disabled = !enabled;
        r.addEventListener('change', () => { state.dst = { mode, id: mode === 'existing' ? (state.dstLists[0] || {}).id || '' : '' }; render(); });
        lab.append(r, el('span', null, text));
        return lab;
      };
      const canNew = CAN_CREATE.includes(state.to);
      where.appendChild(opt('new', CAN_CREATE.includes(state.to)
        ? t('webui.move.where.new')
        : t('webui.move.where.new_unsupported', { provider: providerName(state.to) }), canNew));
      where.appendChild(opt('existing', t('webui.move.where.existing'), state.dstLists.length > 0));
      if (state.dst.mode === 'existing') {
        const sel = el('select', 'in');
        sel.setAttribute('aria-label', t('webui.move.where.existing_aria'));
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
      if (dup) out.push(el('p', 'page__note', t('webui.move.where.dup', { provider: providerName(state.to), name: dup.name })));
      // 兩條路都走不通(不能新建、也沒有既有的):說原因,不要只留兩個灰掉的選項(review #68)。
      if (!canNew && !state.dstLists.length) {
        const provider = providerName(state.to);
        out.push(el('p', 'page__warn', state.to === 'local' ? t('webui.move.where.none_local', { provider }) : t('webui.move.where.none', { provider })));
      }
    }
    const acts = el('div', 'form-row wiz__acts');
    const next = btn(t('webui.move.next.confirm'), 'btn--primary', () => { state.step = 3; state.result = null; render(); });
    next.disabled = !state.src || !state.dstLists || (state.dst.mode === 'existing' && !state.dst.id);
    acts.append(back, next);
    out.push(acts);
    return out;
  }

  function step3() {
    const dstName = state.dst.mode === 'new'
      ? t('webui.move.dst.new', { provider: providerName(state.to), name: state.src.name })
      : t('webui.move.dst.existing', { provider: providerName(state.to), name: (state.dstLists.find((x) => x.id === state.dst.id) || {}).name || state.dst.id });
    const out = [
      el('h2', 'panel__title', t('webui.move.step.confirm')),
      el('p', 'wiz__sum', t('webui.move.summary', { from: providerName(state.from), name: state.src.name, target: dstName })),
    ];
    const r = state.result;
    const live = el('div', 'wiz__live');
    const acts = el('div', 'form-row wiz__acts');
    if (state.running) {
      // 進度是真的才畫(決策 47):這裡只有階段說明與預覽;做到哪裡看底部的執行狀態列,要停按那裡的「中止」。
      if (state.preview) live.appendChild(preview(state.preview.h, state.preview.rows));
      else {
        live.append(liveStage, liveBar, el('p', 'page__note', t('webui.move.running_note', { button: t('webui.console.stop') })));
        paintProgress();
      }
    } else if (!r) {
      out.push(el('p', 'page__note', t('webui.move.before_note')));
      acts.append(btn(t('webui.move.back'), 'btn--ghost', () => { state.step = 2; render(); }), btn(t('webui.move.start'), 'btn--primary', start));
    } else if (r.code === 0) {
      live.appendChild(el('p', 'wiz__done', r.moved ? t('webui.move.done', { count: r.moved, target: dstName }) : t('webui.move.done_none')));
      if (r.missed.length) {
        live.appendChild(el('p', 'page__note', t('webui.move.missed', { count: r.missed.length, provider: providerName(state.to) })));
        live.appendChild(missedList(r.missed));
      }
      acts.append(btn(t('webui.move.again'), 'btn--primary', () => { state.result = null; enterStep2(); }));
      const sync = el('a', 'wiz__link', t('webui.move.keep_synced'));
      sync.href = '#/sync';
      acts.appendChild(sync);
    } else {
      // 「取消」是 exit 2;關掉提示(✕)或等到逾時是 huh.ErrUserAborted → exit 1 + 英文的 user aborted(review #68)。
      // 三種都發生在寫入之前,都是同一種收尾;靠 prompt_closed 的 reason 分辨,不比對那句英文。
      // 「已中止」(底部的中止鈕)另外說:中止前可能已經開始寫入,但再搬一次不會重複。認它看 exit 的 reason(機器欄位;
      // 走到這裡 code 一定不是 0,所以跟 console.js 的 isCancelled 同一個判斷),不比對跟著語系變的訊息。
      const stopped = r.reason === 'cancelled';
      const quit = stopped || r.code === 2 || (r.code === 1 && ['dismissed', 'timeout'].includes(state.closedBy));
      live.appendChild(el('p', quit ? 'wiz__stage' : 'page__warn',
        !quit ? (r.msg || t('webui.move.failed')).replace(/^Error: /, '')
          : stopped ? t('webui.move.stopped')
            : state.closedBy === 'timeout' ? t('webui.move.timed_out') : t('webui.move.cancelled')));
      if (!quit) {
        const c = el('a', 'wiz__link', t('webui.move.see_console'));
        c.href = '#/console';
        live.appendChild(c);
      }
      acts.append(btn(t('webui.move.back'), 'btn--ghost', () => { state.step = 2; state.result = null; render(); }), btn(t('webui.move.retry'), 'btn--primary', start));
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
  sec.appendChild(el('h2', 'how__title', t('webui.move.how.title')));
  const beats = [
    [t('webui.move.how.read.mark'), t('webui.move.how.read.title'), t('webui.move.how.read.text'), 'read'],
    [t('webui.move.how.match.mark'), t('webui.move.how.match.title'), t('webui.move.how.match.text'), 'match'],
    [t('webui.move.how.build.mark'), t('webui.move.how.build.title'), t('webui.move.how.build.text'), 'build'],
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
  sec.appendChild(el('h2', 'how__title', t('webui.move.truths.title')));
  const ul = el('ul', 'truths__list');
  for (const [k, v] of [
    [t('webui.move.truths.no_delete.title'), t('webui.move.truths.no_delete.text')],
    [t('webui.move.truths.order.title'), t('webui.move.truths.order.text')],
    [t('webui.move.truths.apple_library.title'), t('webui.move.truths.apple_library.text')],
    [t('webui.move.truths.setup.title'), t('webui.move.truths.setup.text')],
    [t('webui.move.truths.drive.title'), t('webui.move.truths.drive.text')],
    [t('webui.move.truths.local.title'), t('webui.move.truths.local.text')],
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
  f.appendChild(el('span', null, t('webui.move.foot.open_source')));
  const a = el('a', null, t('webui.move.foot.source'));
  a.href = 'https://github.com/Tai-ch0802/capy-music'; a.target = '_blank'; a.rel = 'noopener noreferrer';
  f.appendChild(a);
  return f;
}
