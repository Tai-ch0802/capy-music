// collect.js:一鍵收進正本清單對話框(#now 的 + 鈕、快捷鍵 a、搜尋頁的最近點播 / 今日聆聽膠囊 / 搜尋結果列共用)。
// 清單挑選器直接讀唯讀直達端點 GET /api/library(零序列槽競爭);實際寫入走 capy pl add,由 CLI 印變更表並以 confirmWrite 確認,
// 頁面絕不代加 --yes 或 --force(決策 46)。
import { el, providerName, emptyState } from './pages/common.js';
import { changeTable } from './pages/sync.js';
import { t } from './i18n.js';

let activeCollector = null;

export function openCollect(target) {
  return activeCollector ? activeCollector.open(target) : null;
}

export function collectActionButton(target) {
  const title = (target && target.title) || (target && target.id) || '';
  const b = el('button', 'btn btn--ghost', t('webui.collect.btn_short'));
  b.type = 'button';
  b.setAttribute('aria-label', t('webui.collect.button_label', { title }));
  b.addEventListener('click', () => openCollect(target));
  return b;
}

export function createCollector(dlg, api, con, notice) {
  let running = false;
  let current = null;

  const top = el('div', 'collect__top');
  const head = el('div');
  const titleEl = el('h3', 'collect__title');
  const songEl = el('p', 'collect__song');
  head.append(titleEl, songEl);
  const closeBtn = el('button', 'btn btn--ghost btn--icon', '✕');
  closeBtn.type = 'button';
  closeBtn.addEventListener('click', () => dlg.close());
  top.append(head, closeBtn);

  const leadEl = el('p', 'page__note');
  const optWrap = el('label', 'collect__opt');
  const pushChk = el('input');
  pushChk.type = 'checkbox';
  const pushText = el('span');
  optWrap.append(pushChk, pushText);

  const listEl = el('div', 'collect__list');
  const statusEl = el('p', 'page__note collect__status');
  statusEl.setAttribute('role', 'status');
  const footEl = el('div', 'form-row collect__foot');
  const tableEl = el('div', 'collect__table');
  const promptsEl = el('div', 'collect__prompts');
  promptsEl.dataset.globalHost = '';

  dlg.replaceChildren(top, leadEl, optWrap, listEl, statusEl, footEl, tableEl, promptsEl);
  dlg.addEventListener('close', () => { if (running) con.stop(); });

  function setStatus(text, warn = false) {
    statusEl.className = warn ? 'page__warn collect__status' : 'page__note collect__status';
    statusEl.textContent = text || '';
  }

  function setFoot(...nodes) {
    footEl.replaceChildren(...nodes);
    if (dlg.open && !dlg.contains(document.activeElement)) (nodes[0] || closeBtn).focus();
  }

  function parsePlaylists(lib) {
    if (Array.isArray(lib && lib.playlists) && lib.playlists.length) return lib.playlists;
    const files = (lib && lib.files) || {};
    return Object.keys(files)
      .filter((k) => k.startsWith('pl__'))
      .sort()
      .map((k) => {
        const pl = files[k] || {};
        return { pid: pl.pid, name: pl.name || pl.pid, count: (pl.items || []).length, links: pl.links || {} };
      });
  }

  async function open(target) {
    if (!target || (!target.now && (!target.provider || !target.id))) {
      notice(t('webui.collect.nothing_playing'));
      return;
    }
    current = target;
    titleEl.textContent = t('webui.collect.title');
    closeBtn.setAttribute('aria-label', t('webui.collect.close'));
    leadEl.textContent = t('webui.collect.lead');
    pushText.textContent = t('webui.collect.push_opt');
    const who = [target.title, (target.artists || []).join(', '), target.provider ? providerName(target.provider) : ''].filter(Boolean).join(' · ');
    songEl.textContent = who;
    tableEl.replaceChildren();
    promptsEl.replaceChildren();
    setFoot();
    setStatus('');
    listEl.replaceChildren();
    if (!dlg.open) dlg.showModal();

    let lib = null;
    try {
      const r = await api.fetch('/api/library');
      if (r.ok) lib = await r.json();
    } catch (_) { /* ignore, render empty fallback */ }

    const pls = parsePlaylists(lib);
    if (!pls.length) {
      listEl.appendChild(emptyState(t('webui.collect.empty')));
      return;
    }
    const inSet = new Set(target.inPlaylists || []);
    for (const pl of pls) {
      const row = el('button', 'pl__item');
      row.type = 'button';
      const already = inSet.has(pl.pid);
      row.appendChild(el('span', 'pl__name', pl.name || pl.pid));
      if (already) row.appendChild(el('span', 'chip', '✓ ' + t('webui.collect.in_playlist')));
      row.appendChild(el('span', 'pl__count num', String(pl.count || 0)));
      const chips = el('span', 'pl__chips');
      for (const [p, id] of Object.entries(pl.links || {})) {
        if (typeof id === 'string' && id) chips.appendChild(el('span', 'chip', providerName(p)));
      }
      row.appendChild(chips);
      row.addEventListener('click', () => runAdd(pl));
      listEl.appendChild(row);
    }
  }

  function buildArgs(pl) {
    const flags = [];
    if (current.now) {
      flags.push('--now');
      if (current.provider) flags.push('--provider', current.provider);
    }
    if (pushChk.checked) flags.push('--push');
    if (current.now) return ['pl', 'add', ...flags, '--', pl.pid];
    return ['pl', 'add', ...flags, '--', pl.pid, `${current.provider}:${current.id}`];
  }

  function runAdd(pl) {
    if (con.running) {
      document.dispatchEvent(new Event('capy:busy'));
      setStatus(t('webui.shell.busy_hint'), true);
      return;
    }
    tableEl.replaceChildren();
    promptsEl.replaceChildren();
    setStatus('');
    const stopBtn = el('button', 'btn', t('webui.console.stop'));
    stopBtn.type = 'button';
    stopBtn.addEventListener('click', (ev) => {
      if (ev.detail > 1) return;
      stopBtn.textContent = t('webui.console.stopping');
      con.stop();
    });
    setFoot(stopBtn);

    let answered = false;
    let closedBy = '';
    let stderrBuf = '';
    running = true;
    const name = pl.name || pl.pid;
    con.run('', {
      onTable: (h, r) => { tableEl.replaceChildren(changeTable(h, r)); },
      onStderr: (s) => { stderrBuf += s; },
      onPromptClosed: (ev) => { closedBy = ev.reason; if (ev.reason === 'answered') answered = true; },
      onExit: (code, msg, reason) => {
        running = false;
        setFoot();
        if (reason === 'cancelled' && code !== 0) {
          setStatus(t('webui.playlists.flow.stopped'));
          return;
        }
        if (code === 0 && answered) {
          setStatus(t('webui.collect.done', { name }));
          if (typeof CustomEvent === 'function' && typeof document.dispatchEvent === 'function') {
            document.dispatchEvent(new CustomEvent('capy:player-settled'));
          }
          return;
        }
        if (code === 0) {
          const noteLine = stderrBuf.trim().split('\n').pop();
          setStatus(noteLine || t('webui.collect.already', { name }));
          return;
        }
        if (code === 2) { setStatus(t('webui.playlists.flow.cancelled')); return; }
        if (code === 1 && closedBy === 'dismissed') { setStatus(t('webui.playlists.flow.dismissed')); return; }
        if (code === 1 && closedBy === 'timeout') { setStatus(t('webui.playlists.flow.timeout')); return; }
        setStatus((msg || '').replace(/^Error: /, '') || t('webui.move.failed'), true);
      },
    }, { args: buildArgs(pl), label: t('webui.collect.label.add', { name }), promptHost: promptsEl });
  }

  const inst = { open, dialog: dlg };
  activeCollector = inst;
  return inst;
}
