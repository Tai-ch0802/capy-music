// sync.js:#/sync —— 同步頁(計畫 docs/superpowers/plans/2026-10-01-sync-page-redesign.md)。由上到下:頁首(lead、三則常駐的事實、
// 小示意圖、「還想知道」)→ 在同步的清單(export,不連網)→ 讓一份清單開始同步(左:平台卡與那個平台的清單,按了才讀;右:選中那份的
// 引導卡、流程區、歌曲預覽)→ 進階(用正本名稱直接下指令)。分工:「我的清單」從正本看;這一頁從平台上的一份清單出發。
// 寫入一律走 CLI 自己的變更表與確認,頁面絕不代加 --yes / -y / --force(決策 46);提示都畫在這一頁的流程區(promptHost),
// reveal() 不會把人丟去主控台。三個流程區(頁首 / 右欄 / 進階)共用一個 makeFlow:有寫入命令在等提示時,開新的流程會被擋下並說明。
import { el, btn, field, input, select, providerOptions, providerName, emptyState, pageHead, radioCard } from './common.js';
import { parseStatus, stateOf } from './account.js';
import { spotifyLink, mmss } from '../table.js';
import { COLUMNS, links, indexExport, driveState, loggedOut, relogin, linkState, listState, cell, filterBox, changeTable, makeFlow, readLists, outcome, say, markStep, linkFlows } from './plflow.js';
import { t } from '../i18n.js';

const FILTER = 8; // 清單超過這麼多份才出現篩選框(同「我的清單」)

export function initSync(root, api, con, notice, providers, arg) {
  // st:交給共用的連結面板(plflow.js)的狀態,它每次用到時才讀。platLists[p]:那個平台的清單({ rows } / { error } / { loading } /
  // { skipped });選了才讀(會連網),讀過就留著,按「重新讀取」才重讀。「在 X 建一份」用它判斷那個平台有沒有同名的清單。
  const st = { auth: {}, devices: [], platLists: {} };
  let masters = null;   // 這台電腦的正本(export);null = 分不出來(還沒讀到、這台沒有副本)
  let exportFail = '';  // '' | 'no_copy'(export 跑完、以非 0 結束 = 這台沒有本機資料)| 'error'(其他:斷線、被佔、中止…)
  let exportMsg = '';
  let track = () => ({});
  let pick = { p: providers.list.includes(arg) ? arg : '', id: '' }; // #/sync/<平台>:預選那個平台
  const previews = {}; // `${平台}\n${清單 id}` → { header, rows } / { error } / { loading }
  const fl = makeFlow(con);
  const lf = linkFlows({ flow: fl, state: st, providers, reload: load });

  pageHead(root, t('webui.sync.title'), t('webui.sync.lead'));
  const banner = el('div', 'sync__banner'); // Google Drive 本身的問題、export 失敗:整頁的前提,畫在最上面

  // ── 在同步的清單 ──
  const allBtn = btn(t('webui.sync.all'), '', () => syncPls(null, headFlow));
  const allWhy = el('p', 'page__note');
  const linkedHead = el('div', 'sync__head');
  linkedHead.append(el('h3', 'card__sub', t('webui.sync.linked.title')), el('span', 'page__note', t('webui.playlists.fresh')), allBtn);
  const linkedBody = el('div', 'sync__linked-box');
  const headFlow = el('div', 'pl__flow'); // 全部同步、每列的同步、讀回正本
  const linkedSec = el('section', 'sync__sec');
  linkedSec.append(linkedHead, allWhy, linkedBody, headFlow);

  // ── 讓一份清單開始同步 ──
  const plats = el('fieldset', 'pick sync__plats');
  const listBox = el('div', 'sync__lists');
  const left = el('div', 'pl__left');
  left.append(plats, listBox);
  const right = el('div', 'pl__right');
  const rightFlow = el('div', 'pl__flow'); // 只在換一份清單時清空;寫入後的自動重讀把它搬進新畫的右欄,收尾那句不會消失
  const cols = el('div', 'pl__cols');
  cols.append(left, right);
  const startSec = el('section', 'sync__sec');
  startSec.append(el('h3', 'card__sub', t('webui.sync.start.title')), cols);

  const advList = el('datalist');
  advList.id = 'sync-masters';
  root.append(intro(), banner, linkedSec, startSec, advanced());
  render();
  con.idle(load); // 有命令在跑就等它結束,不要撞上它

  // load:auth status --json → export,兩個都 quiet(整份 JSON 不灌進主控台);第二個在第一個的 onExit 裡送(見 plflow.js 的 step)。
  function load() {
    let status = '';
    con.run('', {
      onStdout: (s) => { status += s; },
      onExit: (code) => { if (code === 0) st.auth = parseStatus(status); readExport(); }, // 讀不到就沿用上一次讀到的
    }, { args: ['auth', 'status', '--json'], label: t('webui.move.label.status'), quiet: true });
  }

  function readExport() {
    let text = '';
    con.run('export', {
      onStdout: (s) => { text += s; },
      onExit: (code, msg, reason) => {
        if (code !== 0) {
          // 只有命令真的跑完、以非 0 結束才算「這台沒有本機資料」;斷線、被佔著、中止、逾時照說原因,畫過的畫面保留(同「我的清單」)
          exportFail = reason === 'done' ? 'no_copy' : 'error';
          exportMsg = (msg || '').replace(/^Error: /, '');
          if (exportFail === 'no_copy') masters = null;
          render();
          return;
        }
        try {
          ({ masters, track, devices: st.devices } = indexExport(JSON.parse(text)));
          exportFail = '';
        } catch (e) {
          exportFail = 'error';
          exportMsg = t('webui.playlists.bad_export', { error: e.message });
        }
        render();
        // #/sync/<平台> 預選的平台:知道登入狀態之後才讀它的清單(在這個 onExit 裡送,不插隊)
        if (pick.p && driveOK() && !loggedOut(st.auth, pick.p) && !st.platLists[pick.p]) readPlat(pick.p);
      },
    }, { label: t('webui.playlists.load_label'), quiet: true });
  }

  function render() {
    renderBanner();
    renderLinked();
    renderPlats();
    renderLists();
    renderRight();
    advList.replaceChildren(...(masters || []).map((pl) => { const o = el('option'); o.value = pl.name || pl.pid; return o; }));
  }

  // 下面幾個用函式宣告(會提升):初始的 render() 在它們之前就叫了
  function driveOK() { return driveState(st.auth) === 'ok'; }
  function names(ps) { return ps.map(providerName).join(t('webui.sync.sep')); }
  function account() { const a = el('a', 'wiz__link', t('webui.playlists.go_account')); a.href = '#/account'; return a; }

  function renderBanner() {
    banner.replaceChildren();
    const ds = driveState(st.auth);
    if (ds !== 'ok') { banner.append(emptyState(ds === 'missing' ? t('webui.sync.no_drive') : t('webui.playlists.empty_drive_error')), account()); return; }
    if (exportFail === 'error') banner.append(el('p', 'page__warn', exportMsg || t('webui.move.failed')), btn(t('webui.sync.reread'), 'btn--ghost', load));
  }

  // ── 在同步的清單(計畫 §2.1):來源是 export,零網路;每列一顆「同步」,範圍只到那一份 ──
  function renderLinked() {
    linkedBody.replaceChildren();
    allWhy.textContent = '';
    allBtn.disabled = true;
    if (!driveOK()) return; // 頁首說了
    if (exportFail === 'no_copy') { linkedBody.append(...readbackIntro()); return; }
    if (!masters) return;
    const pls = masters.filter((pl) => links(pl).length);
    if (!pls.length) { linkedBody.appendChild(emptyState(t('webui.sync.linked.none'))); return; }
    const list = el('div', 'sync__linked');
    const rows = pls.map((pl) => linkedRow(pl));
    if (pls.length > FILTER) {
      linkedBody.appendChild(filterBox(t('webui.playlists.filter_playlists'), (q) => {
        pls.forEach((pl, i) => { rows[i].hidden = !!q && !String(pl.name || pl.pid).toLowerCase().includes(q); });
      }));
    }
    list.append(...rows);
    linkedBody.appendChild(list);
    allBtn.disabled = !pls.some((pl) => scope([pl]).run.length);
    if (allBtn.disabled) allWhy.textContent = t('webui.sync.linked.none_here');
  }

  function linkedRow(pl) {
    const row = el('div', 'sync__row');
    row.dataset.pid = pl.pid;
    const a = el('a', null, pl.name || pl.pid);
    a.href = `#/playlists/${encodeURIComponent(pl.pid)}`; // 在「我的清单」打開那一份
    const n = (pl.items || []).length;
    row.append(a, el('span', 'sync__count num', t('webui.playlists.platforms.count', { count: n })),
      el('span', 'sync__to', links(pl).map(([p]) => providerName(p)).join(' · ')));
    const sc = scope([pl]);
    if (sc.run.length) row.appendChild(btn(t('webui.sync.row_sync'), 'btn--sm', () => syncPls([pl], headFlow)));
    else {
      // 不給「同步」:連著的平台這台全都沒登入 → 說是哪幾個、指到帳號頁;全是別台電腦 / 別的帳號的 → 照連結面板的說法
      const why = el('span', 'sync__why');
      if (sc.out.length) why.append(el('span', null, `${t('webui.sync.linked.logged_out', { platforms: names(sc.out) })} `), account());
      else why.textContent = sc.device ? t('webui.playlists.links.foreign_device', { device: sc.device }) : t('webui.playlists.links.foreign_account');
      row.appendChild(why);
    }
    return row;
  }

  // scope:這幾份正本這次能同步哪些平台。run = 這台有登入、是自己的連結;out = 連著、這台沒登入;foreign(別台電腦的本機清單、
  // 別的 YouTube 帳號)CLI 自己會跳過,不算。照 providers.list 排。
  function scope(pls) {
    const run = new Set();
    const out = new Set();
    let device = '';
    for (const pl of pls) {
      for (const [p] of links(pl)) {
        const s = linkState(st.auth, st.devices, p, pl);
        if (!s) continue;
        if (s.kind === 'linked') run.add(p);
        else if (s.kind === 'out') out.add(p);
        else if (s.kind === 'foreign' && s.device) device = s.device;
      }
    }
    return { run: providers.list.filter((p) => run.has(p)), out: providers.list.filter((p) => out.has(p) && !run.has(p)), device };
  }

  // syncPls:同步這幾份(pls = null 是全部同步,計畫 §2.6)。有連著的平台在這台沒登入時不送整份(observeAndDerive 會整輪 exit 1),
  // 先說明、讓人選「只同步有登入的平台」;全部同步以 exit 1 結束時多一顆「改成一個平台一個平台同步」(頁面預先判斷不到的情況,例如沒設 local_root)。
  function syncPls(pls, box) {
    if (fl.blocked()) return;
    const all = pls === null;
    const sc = scope(all ? masters.filter((pl) => links(pl).length) : pls);
    const target = all ? ['--all'] : [pls[0].pid];
    const label = all ? t('webui.sync.label.all') : t('webui.playlists.label.sync', { name: pls[0].name });
    const labelOn = (q) => (all ? t('webui.sync.label.all_on', { platform: providerName(q) }) : t('webui.sync.label.sync_on', { name: pls[0].name, platform: providerName(q) }));
    if (sc.out.length) { partial(box, sc, () => chain(box, ['pl', 'sync', ...target], sc.run, labelOn)); return; }
    const f = fl.openFlow(null, box);
    fl.step(f, ['pl', 'sync', ...target], label, (code, o) => {
      say(f, o);
      if (all && code === 1 && o.console) {
        f.status.appendChild(el('span', null, ' '));
        f.status.appendChild(btn(t('webui.sync.per_platform'), 'btn--sm', () => chain(box, ['pl', 'sync', '--all'], sc.run, labelOn)));
      }
      load();
    });
  }

  // partial:有平台沒登入時先說明,按了才逐平台跑(照實說:一次一個平台、各問一次,後面那個平台的改動要等下一次同步才回到前面的)。
  function partial(box, sc, go) {
    const card = el('div', 'pl__flow-card');
    card.append(el('p', null, t('webui.sync.partial', { platforms: names(sc.out) })));
    if (!sc.run.length) { card.appendChild(account()); box.replaceChildren(card); return; }
    card.appendChild(el('p', 'page__note', t('webui.sync.partial_note')));
    const acts = el('div', 'form-row');
    const no = el('button', 'btn btn--ghost', t('webui.playlists.create.cancel'));
    no.type = 'button';
    no.addEventListener('click', () => box.replaceChildren());
    acts.append(btn(t('webui.sync.partial_go'), '', go), no);
    card.appendChild(acts);
    box.replaceChildren(card);
  }

  // chain:逐平台(計畫 §3.5)。一個平台一個命令,下一個在上一個的 onExit 裡送;某一家以 exit 1 失敗就記下、換下一家
  // (同一份正本的其他平台照樣同步);取消、關掉提示、逾時、被上限擋下、中止就停,沒跑的照實列出。
  function chain(box, base, qs, labelOn) {
    if (fl.blocked()) return;
    const f = fl.openFlow(qs.map(providerName), box);
    const failed = [];
    let wroteAny = false;
    const next = (i) => {
      if (i === qs.length) {
        markStep(f, i);
        if (failed.length) say(f, { text: t('webui.sync.chain_failed', { list: failed.join(t('webui.sync.sep')) }), warn: true, console: true });
        else say(f, { text: wroteAny ? t('webui.playlists.flow.done') : t('webui.playlists.flow.up_to_date') });
        load();
        return;
      }
      markStep(f, i);
      fl.step(f, [...base, '--provider', qs[i]], labelOn(qs[i]), (code, o, wrote) => {
        wroteAny = wroteAny || wrote;
        if (code === 1 && o.console) { failed.push(`${providerName(qs[i])}:${o.text}`); next(i + 1); return; }
        if (code !== 0) {
          const left = qs.slice(i + 1);
          say(f, { ...o, extra: [o.extra, left.length && t('webui.sync.chain_left', { platforms: names(left) })].filter(Boolean).join(' ') });
          load();
          return;
        }
        next(i + 1);
      });
    };
    next(0);
  }

  // ── 這台沒有副本:讀回正本(計畫 §2.8)──
  function readbackIntro() {
    return [el('p', 'page__note', t('webui.sync.no_copy')), el('p', 'page__note', t('webui.sync.readback_explain')),
      btn(t('webui.sync.readback'), '', readback)];
  }

  // readback:依序 pl pull --all --provider q(auth 說已登入的音樂平台,不含本機曲庫);第一個 exit 0 就停並安靜重讀
  // (只要成功一次,commitCanonical 會把整份正本寫進這台);exit 1 換下一家;取消、上限、關掉提示、中止就停。
  function readback() {
    if (fl.blocked()) return;
    const qs = COLUMNS.filter((p) => providers.list.includes(p) && st.auth[p]?.state === 'ok' && !relogin(st.auth, p));
    if (!qs.length) { const f = fl.openFlow(null, headFlow); say(f, { text: t('webui.sync.readback_none') }); f.status.append(el('span', null, ' '), account()); return; }
    const f = fl.openFlow(null, headFlow);
    const failed = [];
    const still = t('webui.sync.readback_still');
    const next = (i) => {
      if (i === qs.length) { say(f, { text: [t('webui.sync.readback_failed', { list: failed.join(t('webui.sync.sep')) }), still].join(' '), warn: true, console: true }); return; }
      fl.step(f, ['pl', 'pull', '--all', '--provider', qs[i]], t('webui.sync.label.readback', { platform: providerName(qs[i]) }), (code, o) => {
        if (code === 0) {
          // 寫本機快取失敗只印警告、不回錯(pull.go 的 pull.warn.cache_write):重讀之後還是沒有副本就照實說
          afterLoad(() => say(f, exportFail === 'no_copy' ? { text: still, warn: true } : { text: t('webui.sync.readback_done') }));
          return;
        }
        if (code === 1 && o.console) { failed.push(`${providerName(qs[i])}:${o.text}`); next(i + 1); return; }
        say(f, { ...o, extra: [o.extra, still].filter(Boolean).join(' ') });
      });
    };
    next(0);
  }

  // afterLoad:重讀一次,讀完再叫 fn(load 的兩個命令都在序列槽裡,第二個的 onExit 之後才算讀完)。
  function afterLoad(fn) {
    let status = '';
    con.run('', {
      onStdout: (s) => { status += s; },
      onExit: (code) => {
        if (code === 0) st.auth = parseStatus(status);
        let text = '';
        con.run('export', {
          onStdout: (s) => { text += s; },
          onExit: (c, msg, reason) => {
            if (c !== 0) { if (reason === 'done') { exportFail = 'no_copy'; masters = null; } }
            else { try { ({ masters, track, devices: st.devices } = indexExport(JSON.parse(text))); exportFail = ''; } catch (_) { exportFail = 'error'; } }
            render();
            fn();
          },
        }, { label: t('webui.playlists.load_label'), quiet: true });
      },
    }, { args: ['auth', 'status', '--json'], label: t('webui.move.label.status'), quiet: true });
  }

  // ── 平台卡(計畫 §3.2):帶登入狀態;沒登入的照樣列出、選了不讀 ──
  function renderPlats() {
    plats.replaceChildren(el('legend', 'pick__legend', t('webui.sync.start.platform')));
    for (const p of providers.list) {
      const s = platState(p);
      const line = el('span', 'pcard__state', s.text);
      line.dataset.state = s.kind;
      plats.appendChild(radioCard('pcard', 'sync-plat', pick.p === p, false, () => choosePlat(p), el('strong', 'pcard__name', providerName(p)), line));
    }
    plats.appendChild(el('p', 'page__note', t('webui.sync.start.hint')));
  }

  function platState(p) {
    if (p === 'local') return { kind: 'muted', text: t('webui.move.no_login') };
    if (relogin(st.auth, p)) return { kind: 'warn', text: t('webui.sync.plat.relogin') };
    const a = st.auth[p];
    if (!a) return { kind: 'muted', text: '' }; // 讀不到 auth:不知道,不標
    if (a.state === 'ok') return { kind: 'ok', text: t('webui.move.pcard.connected') };
    if (a.state === 'missing') return { kind: 'muted', text: t('webui.move.pcard.not_connected') };
    return { kind: 'warn', text: stateOf(p, a).text };
  }

  function choosePlat(p) {
    if (pick.p === p) return;
    if (fl.blocked()) { renderPlats(); return; } // 流程區有提示在等:把選取還回去
    pick = { p, id: '' };
    rightFlow.replaceChildren();
    delete rightFlow.dataset.key;
    renderLists();
    renderRight();
    if (driveOK() && !loggedOut(st.auth, p) && !st.platLists[p]) readPlat(p);
  }

  function readPlat(p) {
    st.platLists[p] = { loading: true };
    renderLists();
    readLists(con, p, t('webui.playlists.list_label', { platform: providerName(p) }), (res, reason, code) => {
      st.platLists[p] = reason === 'cancelled' && code !== 0 ? { rows: [], skipped: true } : res;
      if (pick.p === p) { renderLists(); renderRight(); }
    });
  }

  // ── 那個平台的清單:每列的狀態只看這台的正本,不連網;Spotify 的列連回去(連結是兄弟,不包進單選)──
  function renderLists() {
    listBox.replaceChildren();
    const p = pick.p;
    if (!p || !driveOK()) return;
    const platform = providerName(p);
    if (relogin(st.auth, p)) { listBox.append(el('p', 'page__warn', t('webui.playlists.links.relogin', { platform })), account()); return; }
    if (loggedOut(st.auth, p)) { listBox.append(el('p', 'page__note', t('webui.playlists.links.logged_out', { platform })), account()); return; }
    const L = st.platLists[p];
    if (!L) return;
    const reread = btn(t('webui.sync.reread'), 'btn--ghost btn--sm', () => readPlat(p));
    if (L.loading) { listBox.appendChild(skeleton(4)); return; }
    if (L.error) {
      listBox.appendChild(el('p', 'page__warn', t('webui.playlists.platforms.error', { platform, error: L.error })));
      if (p === 'local') { const a = el('a', 'wiz__link', t('webui.move.see_console')); a.href = '#/console'; listBox.appendChild(a); } // 例如還沒設 local_root
      listBox.appendChild(reread);
      return;
    }
    if (L.skipped || !L.rows.length) {
      listBox.append(el('p', 'page__note', L.skipped ? t('webui.playlists.platforms.skipped') : t('webui.playlists.platforms.empty', { platform })), reread);
      return;
    }
    if (!masters) listBox.appendChild(el('p', 'page__note', t('webui.sync.unknown')));
    const list = el('div', 'pl__list');
    const rows = L.rows.map((r) => listRow(p, r));
    if (L.rows.length > FILTER) {
      listBox.appendChild(filterBox(t('webui.sync.filter_lists', { platform }), (q) => {
        L.rows.forEach((r, i) => { rows[i].hidden = !!q && !String(r.name).toLowerCase().includes(q); });
      }));
    }
    list.append(...rows);
    listBox.append(list, reread);
  }

  function listRow(p, r) {
    const wrap = el('div', 'sync__item');
    const ls = listState(masters, p, r);
    wrap.dataset.state = ls.kind;
    const card = radioCard('pl__item', 'sync-list', pick.id === r.id, false, () => chooseList(r),
      el('span', 'pl__name', r.name), el('span', 'pl__count num', countText(r)), el('small', null, stateText(p, ls)));
    wrap.appendChild(card);
    const sp = p === 'spotify' && spotifyLink('playlist', r.id, r.name, 'wiz__link');
    if (sp) wrap.appendChild(sp);
    return wrap;
  }

  // 首數:TRACKS 不是數字就留白(Apple 的 pl list 一律是 -);右欄等 pl show 讀回來後用列數填
  function countText(r) { return /^\d+$/.test(r.tracks || '') ? t('webui.playlists.platforms.count', { count: Number(r.tracks) }) : ''; }
  function stateText(p, ls) {
    if (ls.kind === 'linked') return t('webui.playlists.platforms.linked', { name: ls.master.name });
    if (ls.kind === 'same_name' || ls.kind === 'same_name_taken') return t('webui.sync.same_name_short', { name: ls.master.name });
    if (ls.kind === 'unlinked') return t('webui.playlists.platforms.unlinked');
    return '';
  }

  function chooseList(r) {
    if (pick.id === r.id) return;
    if (fl.blocked()) { renderLists(); return; }
    pick.id = r.id;
    renderRight();
    const key = `${pick.p}\n${r.id}`;
    if (!previews[key] || previews[key].error) readPreview(pick.p, r);
  }

  function readPreview(p, r) {
    const key = `${p}\n${r.id}`;
    previews[key] = { loading: true };
    let header = [];
    const rows = [];
    con.run('', {
      onTable: (h, rs) => { header = h; rows.push(...rs); },
      onExit: (code, msg) => {
        previews[key] = code === 0 ? { header, rows } : { error: (msg || '').replace(/^Error: /, '') || t('webui.move.failed') };
        if (`${pick.p}\n${pick.id}` === key) renderRight();
      },
    }, { args: ['pl', 'show', r.id, '--provider', p], label: t('webui.sync.label.show', { name: r.name }) });
  }

  // ── 右欄:選中那一份 ──
  function renderRight() {
    right.replaceChildren();
    const p = pick.p;
    const L = p && st.platLists[p];
    const r = L && L.rows && L.rows.find((x) => x.id === pick.id);
    if (!r) { right.appendChild(startCard()); return; }
    const key = `${p}\n${r.id}`;
    if (rightFlow.dataset.key !== key) { rightFlow.replaceChildren(); rightFlow.dataset.key = key; }
    const ls = listState(masters, p, r);
    right.append(head(p, r, ls), guide(p, r, ls), rightFlow, preview(p, r));
  }

  function startCard() {
    const card = el('div', 'pl__flow-card');
    const ol = el('ol', 'sync__start');
    for (const k of [t('webui.sync.start.step1'), t('webui.sync.start.step2'), t('webui.sync.start.step3')]) ol.appendChild(el('li', null, k));
    card.append(el('h4', 'pl__plat-name', t('webui.sync.start.pick')), ol, el('p', 'page__note', t('webui.sync.start.no_confirm')));
    return card;
  }

  function head(p, r, ls) {
    const h = el('div', 'pl__head');
    const main = el('div', 'pl__head-main');
    const v = previews[`${p}\n${r.id}`];
    const count = countText(r) || (v && v.rows ? t('webui.playlists.platforms.count', { count: v.rows.length }) : '');
    main.append(el('h3', 'card__sub', r.name), el('p', 'page__note', [providerName(p), count, stateText(p, ls)].filter(Boolean).join(' · ')));
    h.appendChild(main);
    const sp = p === 'spotify' && spotifyLink('playlist', r.id, r.name);
    if (sp) h.appendChild(sp);
    if (ls.kind === 'linked') {
      const a = el('a', 'btn btn--ghost', t('webui.sync.open_in_playlists'));
      a.href = `#/playlists/${encodeURIComponent(ls.master.pid)}`;
      h.appendChild(a);
    }
    return h;
  }

  // guide:依那份清單跟正本的關係換引導卡(計畫 §2.2–§2.4),一張卡只有一顆主要鈕。
  function guide(p, r, ls) {
    const platform = providerName(p);
    const card = el('div', 'pl__flow-card');
    card.dataset.state = ls.kind;
    const acts = el('div', 'form-row');
    if (ls.kind === 'unknown') {
      card.appendChild(el('p', null, t('webui.sync.unknown_card')));
      return card;
    }
    if (ls.kind === 'unlinked') {
      card.append(steps(0), el('p', null, t('webui.sync.adopt.explain', { button: t('webui.playlists.platforms.adopt'), name: r.name, platform })));
      acts.append(btn(t('webui.sync.dup_check'), 'btn--ghost', () => dupCheck(p, r)),
        btn(t('webui.playlists.platforms.adopt'), 'btn--primary', () => lf.adopt(p, r, rightFlow, t('webui.sync.adopt.next'))));
      card.appendChild(acts);
      return card;
    }
    if (ls.kind === 'same_name') { card.appendChild(el('p', null, t('webui.sync.same_name', { name: ls.master.name, platform }))); return card; }
    if (ls.kind === 'same_name_taken') { card.appendChild(el('p', null, t('webui.sync.same_name_taken', { name: ls.master.name, platform }))); return card; }
    return linkedGuide(ls.master);
  }

  function steps(now) {
    const ol = el('ol', 'wiz__steps');
    [t('webui.playlists.step.adopt'), t('webui.playlists.step.pull'), t('webui.sync.step.link')].forEach((s, i) => {
      const li = el('li', 'wiz__step', s);
      li.dataset.state = i < now ? 'done' : i === now ? 'now' : 'todo';
      if (i === now) li.setAttribute('aria-current', 'step');
      ol.appendChild(li);
    });
    return ol;
  }

  // linkedGuide:已經連著正本(計畫 §2.3)。可同步的連結(扣掉別台 / 別帳號、這台沒登入的)只有一個時,同步沒有第二個平台可以對:
  // 引導去建一份(只有一個可建的平台才放主要鈕),同步降成次要;兩個以上時主要鈕是「同步這份清單」,另有「去除重複」。
  function linkedGuide(pl) {
    const box = el('div', 'sync__guide');
    const card = el('div', 'pl__flow-card');
    const acts = el('div', 'form-row');
    const kinds = providers.list.map((q) => [q, linkState(st.auth, st.devices, q, pl)]).filter(([, s]) => s);
    const syncable = kinds.filter(([, s]) => s.kind === 'linked').map(([q]) => q);
    const creatable = kinds.filter(([q, s]) => s.kind === 'unlinked' && COLUMNS.includes(q)).map(([q]) => q);
    const elsewhere = kinds.some(([, s]) => s.kind === 'out' || s.kind === 'foreign');
    const syncBtn = (cls) => btn(t('webui.playlists.sync'), cls, () => syncPls([pl], rightFlow));
    card.dataset.links = String(syncable.length);
    if (!syncable.length) {
      const sc = scope([pl]);
      card.appendChild(el('p', null, sc.out.length ? t('webui.sync.linked.logged_out', { platforms: names(sc.out) }) : t('webui.playlists.links.foreign_account')));
      if (sc.out.length) card.appendChild(account());
    } else if (syncable.length === 1) {
      const platform = providerName(syncable[0]);
      card.append(steps(2), el('p', null, t('webui.sync.one_link', { platform })));
      if (creatable.length === 1) {
        acts.appendChild(btn(t('webui.playlists.links.create', { platform: providerName(creatable[0]) }), 'btn--primary', () => lf.askCreate(creatable[0], pl, rightFlow)));
      }
      acts.appendChild(syncBtn(creatable.length ? 'btn--ghost' : 'btn--primary'));
      card.append(acts, el('p', 'page__note', elsewhere ? t('webui.sync.one_link_more', { platform }) : t('webui.sync.one_link_only', { platform })));
    } else {
      acts.append(btn(t('webui.sync.dedup'), 'btn--ghost', () => dedupPl(pl)), syncBtn('btn--primary'));
      card.appendChild(acts);
    }
    box.append(card, lf.panel(pl, pl.items || [], track, rightFlow));
    if (kinds.some(([q, s]) => s.kind === 'unlinked' && COLUMNS.includes(q))) box.appendChild(el('p', 'page__note', t('webui.sync.no_merge_yet')));
    // 名字附註:連著的平台清單現在的名字跟正本不同時才說(只比對這一頁讀過清單的平台;本機曲庫不改檔名)
    for (const [q, s] of kinds) {
      if (s.kind !== 'linked' || q === 'local') continue;
      const cur = (st.platLists[q]?.rows || []).find((x) => x.id === s.id);
      if (cur && cur.name !== pl.name) box.appendChild(el('p', 'page__note', t('webui.sync.rename_note', { platform: providerName(q), current: cur.name, name: pl.name })));
    }
    return box;
  }

  // dedupPl:整理正本裡的重複(Q11)。它走 observeAndDerive:有連著的平台這台沒登入就會整輪 exit 1,所以同 syncPls 的逐平台規則。
  function dedupPl(pl) {
    if (fl.blocked()) return;
    const sc = scope([pl]);
    const labelOn = (q) => t('webui.sync.label.dedup_on', { name: pl.name, platform: providerName(q) });
    if (sc.out.length) { partial(rightFlow, sc, () => chain(rightFlow, ['pl', 'dedup', pl.pid], sc.run, labelOn)); return; }
    const f = fl.openFlow(null, rightFlow);
    fl.step(f, ['pl', 'dedup', pl.pid], t('webui.sync.dedup_label'), (code, o, wrote) => {
      say(f, code === 0 && !wrote ? { text: t('webui.sync.adv.no_dups') } : o);
      load();
    });
  }

  // dupCheck:看看這份平台清單自己有沒有重複(pl dedup <平台>:<清單>,唯讀、不帶任何旗標:帶了會被 dedup.go 退回)。
  function dupCheck(p, r) {
    if (fl.blocked()) return;
    const f = fl.openFlow(null, rightFlow);
    let n = 0;
    con.run('', {
      onTable: (h, rs) => { n = rs.length; f.table.replaceChildren(changeTable(h, rs, { spotifyReport: p === 'spotify' })); },
      onExit: (code, msg, reason) => {
        if (code !== 0) { say(f, outcome(code, msg, reason, '', false, 0)); return; }
        say(f, { text: n ? t('webui.sync.dup.found', { count: n, button: t('webui.sync.dedup') }) : t('webui.sync.dup.none') });
      },
    }, { args: ['pl', 'dedup', `${p}:${r.id}`], label: t('webui.sync.label.dup_check', { name: r.name }), promptHost: f.host });
  }

  // ── 歌曲預覽(pl show 的表;同「我的清單」的歌曲表外觀,窄的時候每首拆成多行)──
  function preview(p, r) {
    const box = el('div', 'sync__preview');
    box.appendChild(el('h4', 'card__sub', t('webui.sync.preview.title', { platform: providerName(p) })));
    const v = previews[`${p}\n${r.id}`];
    if (!v || v.loading) box.appendChild(skeleton(5));
    else if (v.error) {
      box.appendChild(el('p', 'page__warn', t('webui.sync.preview.error', { error: v.error })));
      if (p === 'apple') box.appendChild(el('p', 'page__note', t('webui.sync.preview.apple_empty')));
    } else if (!v.rows.length) box.appendChild(el('p', 'page__note', t('webui.sync.preview.empty')));
    else box.appendChild(previewTable(p, v.header, v.rows));
    return box;
  }

  function previewTable(p, header, rows) {
    const [ii, ti, ai, li, di] = ['ID', 'TITLE', 'ARTISTS', 'ALBUM', 'DURATION'].map((k) => header.indexOf(k)); // 機器欄位,不跟語系
    const sp = p === 'spotify';
    const tbl = el('table', 'tbl pl__tbl');
    tbl.setAttribute('role', 'table');
    const cg = el('colgroup');
    cg.append(el('col', 'pl__c-pos'), el('col', 'pl__c-song'), el('col', 'pl__c-dur'), ...(sp ? [el('col', 'pl__c-spotify')] : []));
    const hr = el('tr');
    hr.setAttribute('role', 'row');
    for (const text of ['#', t('webui.playlists.col.song'), t('webui.playlists.col.duration'), ...(sp ? [providerName(p)] : [])]) {
      const th = el('th', null, text);
      th.setAttribute('role', 'columnheader');
      hr.appendChild(th);
    }
    const thead = el('thead');
    thead.appendChild(hr);
    const tbody = el('tbody');
    rows.forEach((x, i) => {
      const tr = el('tr');
      tr.setAttribute('role', 'row');
      const title = x[ti] || '';
      const sub = [x[ai], x[li]].filter(Boolean).join(' · ');
      const song = cell('pl__song');
      song.appendChild(el('span', 'pl__title', title));
      if (sub) song.appendChild(el('span', 'pl__sub', sub));
      song.title = sub ? `${title}\n${sub}` : title;
      const d = x[di] || '';
      tr.append(cell('pl__pos num', String(i + 1)), song, cell('pl__dur num', /^\d+$/.test(d) && d !== '0' ? mmss(Number(d)) : ''));
      if (sp) {
        const td = cell('row-actions pl__cell');
        td.dataset.platform = providerName(p);
        const a = spotifyLink('track', x[ii], title);
        if (a) td.appendChild(a);
        tr.appendChild(td);
      }
      tbody.appendChild(tr);
    });
    tbl.append(cg, thead, tbody);
    const wrap = el('div', 'tbl-wrap tbl-wrap--tall pl__songs');
    wrap.appendChild(tbl);
    return wrap;
  }

  function skeleton(n) {
    const s = el('div', 'skeleton');
    s.setAttribute('aria-hidden', 'true');
    for (let i = 0; i < n; i++) s.appendChild(el('div', 'skeleton__bar'));
    return s;
  }

  // ── 進階:用正本名稱直接下指令(計畫 §2.7)。四顆鈕直接對應 pl pull / push / sync / dedup;改送 args 陣列、提示畫在這裡。──
  function advanced() {
    const d = el('details', 'sync__adv');
    d.append(el('summary', null, t('webui.sync.adv.summary')), el('p', 'page__note', t('webui.sync.adv.lead')));
    const name = input('sans', t('webui.sync.name_placeholder'));
    name.setAttribute('list', 'sync-masters');
    name.dataset.adv = 'name';
    // 「全部平台」的值是空字串 = 不帶 --provider(值不拿顯示文字當,顯示文字跟著語系)。
    const prov = select([{ value: '', label: t('webui.sync.all_platforms') }, ...providerOptions(providers.list)], '');
    prov.dataset.adv = 'provider';
    const dry = el('input', null);
    dry.type = 'checkbox';
    dry.checked = true; // Q10:進階是明確操作,照舊預設只看變更
    dry.dataset.adv = 'dry';
    const dryLabel = t('webui.sync.dry_run');
    const bar = el('div', 'form-row');
    bar.append(field(t('webui.sync.playlist'), name, true), advList, field(t('webui.common.platform'), prov), field(dryLabel, dry));
    const out = el('div', 'pl__flow');
    const VERBS = [
      ['pull', t('webui.sync.pull'), t('webui.sync.pull_label'), t('webui.sync.adv.pull_note')],
      ['push', t('webui.sync.push'), t('webui.sync.push_label'), t('webui.sync.adv.push_note')],
      ['sync', t('webui.sync.sync'), t('webui.sync.sync_label'), t('webui.sync.adv.sync_note')],
      ['dedup', t('webui.sync.dedup'), t('webui.sync.dedup_label'), t('webui.sync.adv.dedup_note')],
    ];
    const list = el('div', 'sync__adv-acts');
    for (const [verb, text, label, note] of VERBS) {
      const row = el('div', 'sync__adv-row');
      const b = btn(text, verb === 'sync' ? 'btn--primary' : '', () => run(verb, label));
      b.dataset.adv = verb;
      row.append(b, el('span', 'page__note', note));
      list.appendChild(row);
    }
    d.append(bar, list, el('p', 'page__note', t('webui.sync.dedup_note', { button: t('webui.sync.dedup') })), out);

    const run = (verb, label) => {
      if (fl.blocked()) return;
      const wasDry = dry.checked; // 按下去那一刻的值:命令跑到一半才改勾選,不該改變這一次的收尾
      const n = name.value.trim();
      // pl dedup <平台>:<清單> 是平台清單的唯讀報告(dedup.go 看冒號前面是不是平台):帶任何旗標都會被退回,只送名稱
      const plat = n.includes(':') ? n.slice(0, n.indexOf(':')) : '';
      if (verb === 'dedup' && providers.list.includes(plat)) { report(n, plat, label); return; }
      // flag 在 -- 前面、名稱在後面(以 - 開頭的清單名不被當成旗標);pl dedup 沒有 --all,清單留空時讓它開挑選器
      const args = ['pl', verb, ...(n ? [] : verb === 'dedup' ? [] : ['--all']), ...(prov.value ? ['--provider', prov.value] : []),
        ...(wasDry ? ['--dry-run'] : []), ...(n ? ['--', n] : [])];
      const f = fl.openFlow(null, out);
      fl.step(f, args, label, (code, o, wrote) => {
        // 只看變更 + 有變更 = exit 2,是正常結果不是取消:說這一頁上的下一步(CLI 的原文會叫人「加 --yes」,那正是這一頁不會做的事)
        if (code === 2 && wasDry) say(f, { text: t('webui.sync.dry_note', { option: dryLabel }) });
        else if (code === 0 && !wrote && !n && verb !== 'dedup' && masters && !masters.some((pl) => links(pl).length)) say(f, { text: t('webui.sync.adv.no_links') });
        else if (code === 0 && !wrote && verb === 'dedup') say(f, { text: t('webui.sync.adv.no_dups') });
        else say(f, o);
        load();
      });
    };
    // report:不管有沒有重複都 exit 0、不問確認;不進 outcome()(會說成「已經是最新的」)
    const report = (n, plat, label) => {
      const f = fl.openFlow(null, out);
      let count = 0;
      con.run('', {
        onTable: (h, rs) => { count = rs.length; f.table.replaceChildren(changeTable(h, rs, { spotifyReport: plat === 'spotify' })); },
        onExit: (code, msg, reason) => {
          if (code !== 0) say(f, outcome(code, msg, reason, '', false, 0));
          else say(f, { text: count ? t('webui.sync.adv.report', { count }) : t('webui.sync.adv.no_dups') });
        },
      }, { args: ['pl', 'dedup', '--', n], label, promptHost: f.host });
    };
    return d;
  }

  // 回到這一頁時重讀(app.js 的 route);#/sync/<平台> 換預選的平台。有命令在跑就等它結束。
  return {
    refresh: (p) => {
      if (p && providers.list.includes(p) && p !== pick.p) choosePlat(p);
      con.idle(load);
    },
  };
}

// intro:頁首的小示意圖、三則常駐的事實、「還想知道」(計畫 §3.1)。示意圖整個 aria-hidden:三則事實才是給人讀的字。
function intro() {
  const box = el('div', 'sync__intro');
  const dia = el('div', 'sync__diagram');
  dia.setAttribute('aria-hidden', 'true');
  dia.append(el('span', 'sync__box', 'Spotify'), el('span', 'sync__arrow', '<->'), el('span', 'sync__box sync__box--hub', t('webui.playlists.links.hub')),
    el('span', 'sync__arrow', '<->'), el('span', 'sync__box', 'Apple Music'));
  const facts = el('ul', 'pl__about-list');
  for (const [k, v] of [
    [t('webui.sync.fact.master.title'), t('webui.sync.fact.master.text')],
    [t('webui.sync.fact.start.title'), t('webui.sync.fact.start.text')],
    [t('webui.sync.fact.name.title'), t('webui.sync.fact.name.text')],
  ]) {
    const li = el('li');
    li.append(el('strong', null, k), el('span', null, v));
    facts.appendChild(li);
  }
  const more = el('details', 'pl__about');
  more.appendChild(el('summary', null, t('webui.sync.more.summary')));
  const ul = el('ul', 'pl__about-list');
  for (const [k, v] of [
    [t('webui.sync.more.dup.title'), t('webui.sync.more.dup.text')],
    [t('webui.sync.more.order.title'), t('webui.sync.more.order.text')],
    [t('webui.sync.more.limit.title'), t('webui.sync.more.limit.text')],
    [t('webui.sync.more.copy.title'), t('webui.sync.more.copy.text')],
    [t('webui.playlists.about.move.title'), t('webui.playlists.about.move.text')],
  ]) {
    const li = el('li');
    li.append(el('strong', null, k), el('span', null, v));
    ul.appendChild(li);
  }
  more.appendChild(ul);
  box.append(dia, facts, more);
  return box;
}
