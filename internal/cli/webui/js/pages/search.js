// search.js:#/search —— 關鍵字搜尋(走 capy search)+ 直達播放(/api/player,不掛 data-run)+ 一鍵收藏 +
// 最近點播與今日聆聽膠囊(GET /api/library:嚴守決策 42 / 48 #9 1F——非播放脈絡卡片零封面圖、禁內嵌官方標誌、Spotify 必附外連)。
import { el, quote, btn, field, input, select, providerOptions, providerName, emptyState, pageHead } from './common.js';
import { renderTable, spotifyLink, youtubeLink } from '../table.js';
import { postPlayerAction } from '../player.js';
import { collectActionButton } from '../collect.js';
import { t } from '../i18n.js';

function sameLocalDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

function formatClock(unixSec) {
  const d = new Date((unixSec || 0) * 1000);
  const p2 = (n) => String(n).padStart(2, '0');
  return `${p2(d.getHours())}:${p2(d.getMinutes())}`;
}

function makeDirectPlayBtn(prov, payload, title) {
  const apple = prov === 'apple';
  const b = el('button', 'btn btn--ghost', apple ? t('webui.search.open_music') : t('webui.search.play'));
  b.type = 'button';
  b.setAttribute('aria-label', apple ? t('webui.search.open_music_btn_label', { title }) : t('webui.search.play_btn_label', { title }));
  b.addEventListener('click', () => postPlayerAction(payload.api, payload.notice, payload.body));
  return b;
}

export function initSearch(root, api, con, notice, providers) {
  pageHead(root, t('webui.search.title'), t('webui.search.lead'));
  const q = input('sans', t('webui.search.placeholder'));
  const prov = select(providerOptions(providers.list), providers.current);
  const limit = el('input', 'in');
  limit.type = 'number';
  limit.min = '1';
  limit.value = '10';
  const bar = el('div', 'form-row');
  bar.append(field(t('webui.search.keywords'), q, true), field(t('webui.common.platform'), prov), field(t('webui.search.limit'), limit));
  const out = el('div', 'page__out');
  const capsule = el('section', 'capsule');

  const go = () => {
    const text = q.value.trim();
    if (!text) { q.focus(); return; }
    out.replaceChildren();
    const n = Math.max(1, Number(limit.value) || 10);
    const p = prov.value;
    con.run(`search ${quote(text)} --provider ${p} --limit ${n}`, {
      onTable: (header, rows) => out.replaceChildren(rows.length
        ? resultTable(header, rows, p, api, notice)
        : emptyState(t('webui.search.not_found', { platform: providerName(p), query: text }))),
      onExit: (code, msg) => { if (code !== 0 && !out.firstChild) out.appendChild(emptyState(msg || t('webui.search.failed'))); },
    }, { label: t('webui.search.label', { platform: providerName(p), query: text }) });
  };
  const goBtn = btn(t('webui.search.go'), 'btn--primary', go);
  bar.appendChild(goBtn);
  q.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
    ev.preventDefault();
    goBtn.click();
  });

  root.append(bar, out, capsule);
  out.appendChild(emptyState(t('webui.search.empty', { button: t('webui.search.go') })));
  q.focus();

  function clearHistory() {
    con.run('', {
      onExit: (code) => { if (code === 0) loadCapsule(); },
    }, { args: ['history', 'clear'], label: t('webui.search.clear_history_label') });
  }

  let capSeq = 0;
  async function loadCapsule() {
    const mine = ++capSeq;
    let lib = null;
    try {
      const r = await api.fetch('/api/library');
      if (r.ok) lib = await r.json();
    } catch (_) { /* optional enhancement when api mock has no /api/library */ }
    if (mine !== capSeq || !lib) return;
    renderCapsule(lib);
  }

  function renderCapsule(lib) {
    capsule.replaceChildren();
    const recent = Array.isArray(lib.recent) ? lib.recent : [];
    const journal = Array.isArray(lib.play_journal) ? lib.play_journal : [];

    if (recent.length > 0) {
      const sec = el('div', 'capsule__sec');
      const head = el('div', 'capsule__head');
      head.appendChild(el('h3', 'capsule__title', t('webui.search.recent_title')));
      sec.appendChild(head);
      const grid = el('div', 'capsule__grid');
      for (const item of recent.slice(0, 8)) {
        const card = el('div', 'capsule__card');
        const main = el('div', 'capsule__card-main');
        const title = item.label || item.id;
        main.appendChild(el('span', 'capsule__card-title', title));
        const meta = [providerName(item.provider), item.detail].filter(Boolean).join(' · ');
        if (meta) main.appendChild(el('span', 'capsule__card-meta', meta));
        const acts = el('div', 'capsule__acts');
        if (item.provider !== 'youtube' && item.id) {
          if (item.type === 'track') {
            acts.appendChild(makeDirectPlayBtn(item.provider, { api, notice, body: { action: 'play', provider: item.provider, id: item.id } }, title));
          } else if (item.type === 'playlist' && item.provider === 'spotify') {
            acts.appendChild(makeDirectPlayBtn(item.provider, { api, notice, body: { action: 'play', provider: item.provider, playlist_id: item.id } }, title));
          }
        }
        if (item.type === 'track' && item.id) {
          acts.appendChild(collectActionButton({ provider: item.provider, id: item.id, title }));
        }
        const sp = item.provider === 'spotify' && (item.type === 'playlist' || item.type === 'track')
          ? spotifyLink(item.type === 'playlist' ? 'playlist' : 'track', item.id, title)
          : null;
        if (sp) acts.appendChild(sp);
        if (item.provider === 'youtube' && item.type === 'track' && item.id) {
          acts.appendChild(youtubeLink(item.id, title));
        }
        card.append(main, acts);
        grid.appendChild(card);
      }
      sec.appendChild(grid);
      capsule.appendChild(sec);
    }

    const now = new Date();
    const todayItems = journal.filter((e) => sameLocalDay(new Date((e.played_at || 0) * 1000), now));
    const jSec = el('div', 'capsule__sec');
    const jHead = el('div', 'capsule__head');
    const jTitles = el('div');
    jTitles.append(
      el('h3', 'capsule__title', t('webui.search.journal_title')),
      el('p', 'capsule__sub', t('webui.search.journal_sub')),
    );
    jHead.appendChild(jTitles);
    if (recent.length > 0 || journal.length > 0) {
      jHead.appendChild(btn(t('webui.search.clear_history'), 'btn--ghost', clearHistory));
    }
    jSec.appendChild(jHead);

    if (!todayItems.length) {
      jSec.appendChild(el('p', 'page__note', t('webui.search.journal_empty')));
    } else {
      const ul = el('ul', 'capsule__jlist');
      for (const e of todayItems) {
        const li = el('li', 'capsule__jrow');
        const main = el('div', 'capsule__jmain');
        main.append(
          el('span', 'capsule__jtime', formatClock(e.played_at)),
          el('span', 'chip', providerName(e.provider)),
          el('span', 'capsule__jtitle', e.title || e.provider_id || ''),
        );
        const by = [(e.artists || []).join(', '), e.album].filter(Boolean).join(' · ');
        if (by) main.appendChild(el('span', 'capsule__jby', by));

        const acts = el('div', 'capsule__acts');
        const title = e.title || e.provider_id || '';
        if (e.provider !== 'youtube' && e.provider_id) {
          acts.appendChild(makeDirectPlayBtn(e.provider, { api, notice, body: { action: 'play', provider: e.provider, id: e.provider_id } }, title));
        }
        if (e.provider_id) {
          acts.appendChild(collectActionButton({ provider: e.provider, id: e.provider_id, title, artists: e.artists || [] }));
        }
        const sp = e.provider === 'spotify' && e.provider_id ? spotifyLink('track', e.provider_id, title) : null;
        if (sp) acts.appendChild(sp);
        if (e.provider === 'youtube' && e.provider_id) acts.appendChild(youtubeLink(e.provider_id, title));
        li.append(main, acts);
        ul.appendChild(li);
      }
      jSec.appendChild(ul);
    }
    capsule.appendChild(jSec);
  }

  document.addEventListener('capy:player-settled', () => loadCapsule());
  loadCapsule();

  return { refresh: loadCapsule, focusSearch: () => q.focus() };
}

function resultTable(header, rows, prov, api, notice) {
  const wrap = el('div', 'tbl-wrap');
  const tbl = renderTable(header, rows);
  const head = tbl.querySelector('thead tr');
  head.appendChild(el('th', null, ''));
  const artistIdx = header.indexOf('ARTISTS');
  [...tbl.querySelectorAll('tbody tr')].forEach((tr, i) => {
    const td = el('td', 'row-actions');
    const id = rows[i][0];
    const title = rows[i][1] || id;
    const artists = artistIdx >= 0 && rows[i][artistIdx] ? String(rows[i][artistIdx]).split(',').map((x) => x.trim()).filter(Boolean) : [];
    if (prov !== 'youtube') {
      td.appendChild(makeDirectPlayBtn(prov, { api, notice, body: { action: 'play', provider: prov, id } }, title));
    }
    if (id) {
      td.appendChild(collectActionButton({ provider: prov, id, title, artists }));
    }
    const link = (prov === 'spotify' && spotifyLink('track', id, title)) || (prov === 'youtube' && youtubeLink(id, title));
    if (link) td.appendChild(link);
    tr.appendChild(td);
  });
  wrap.appendChild(tbl);
  return wrap;
}
