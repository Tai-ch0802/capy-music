// isrc.js:#/isrc[/<ISRC>] —— 打 GET /api/isrc/{isrc}(決策 42)。四段拆解、各平台卡片、本機鏡像的 canonical。
import { t } from '../i18n.js';

const el = (tag, cls, text) => {
  const x = document.createElement(tag);
  if (cls) x.className = cls;
  if (text != null) x.textContent = text;
  return x;
};

// 非地理前綴不丟給 Intl.DisplayNames:它對結構合法但未指派的代碼會原樣回 QM,看起來像壞掉。
function countryLabel(code, geographic) {
  if (!geographic) return code === 'ZZ' ? t('webui.isrc.country.zz') : t('webui.isrc.country.non_geo');
  try {
    // 國名跟著語系:zh-TW 與原本寫死的 zh-Hant 是同一份 CLDR 國名。
    return new Intl.DisplayNames([document.documentElement.lang || 'en'], { type: 'region' }).of(code) || code;
  } catch (_) {
    return code;
  }
}

function mmss(ms) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

function partsBar(parts) {
  const bar = el('div', 'isrc__parts');
  const cells = parts
    ? [
      { v: parts.country, k: countryLabel(parts.country, parts.geographic), w: '2ch' },
      { v: parts.registrant, k: t('webui.isrc.part.registrant'), w: '3ch' },
      { v: `${parts.year} → ${parts.year_full}`, k: t('webui.isrc.part.year'), w: '7ch' },
      { v: parts.designation, k: t('webui.isrc.part.designation'), w: '5ch' },
    ]
    : [{ v: '—', k: t('webui.isrc.part.unparsable'), w: '100%' }];
  for (const c of cells) {
    const cell = el('div', 'isrc__part');
    cell.style.minWidth = c.w;
    cell.appendChild(el('div', 'isrc__part-v', c.v));
    cell.appendChild(el('div', 'isrc__part-k', c.k));
    bar.appendChild(cell);
  }
  return bar;
}

function providerCard(id, data) {
  const card = el('section', 'card');
  card.appendChild(el('h3', 'card__title', id));
  if (data.error) {
    card.appendChild(el('p', 'card__error', data.error));
    return card;
  }
  if (!data.tracks.length) {
    card.appendChild(el('p', 'card__muted', t('webui.isrc.no_match')));
    return card;
  }
  for (const tr of data.tracks) { // 不叫 t:會遮住 i18n 的 t()
    const row = el('div', 'track');
    // 封面只與「在這個平台開啟」的連結並列(ARCHITECTURE §8:cover art 只准用在播放脈絡或連回該平台的脈絡)。
    if (tr.artwork_url) {
      const img = el('img', 'track__art');
      img.src = tr.artwork_url;
      img.alt = '';
      img.loading = 'lazy';
      row.appendChild(img);
    }
    const meta = el('div', 'track__meta');
    meta.appendChild(el('div', 'track__title', tr.title));
    meta.appendChild(el('div', 'track__sub', `${(tr.artists || []).join(', ')}${tr.album ? ' · ' + tr.album : ''}`));
    const facts = el('div', 'track__facts');
    facts.appendChild(el('span', 'mono', tr.id));
    if (tr.duration_ms) facts.appendChild(el('span', 'mono', mmss(tr.duration_ms)));
    if (tr.release_date) facts.appendChild(el('span', 'mono', tr.release_date));
    if (tr.popularity) facts.appendChild(el('span', 'mono', `popularity ${tr.popularity}`));
    for (const g of tr.genres || []) facts.appendChild(el('span', 'chip', g));
    meta.appendChild(facts);
    if (tr.url) {
      const platform = id === 'apple' ? 'Apple Music' : id;
      const a = el('a', 'track__link', t('webui.isrc.open_on', { platform }));
      a.href = tr.url;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      meta.appendChild(a);
    }
    if (tr.preview_url) {
      const audio = document.createElement('audio');
      audio.controls = true;
      audio.preload = 'none';
      audio.src = tr.preview_url;
      audio.className = 'track__preview';
      meta.appendChild(audio);
    }
    row.appendChild(meta);
    card.appendChild(row);
  }
  return card;
}

function canonicalCard(d) {
  const card = el('section', 'card');
  card.appendChild(el('h3', 'card__title', t('webui.isrc.canonical.title')));
  if (d.canonical_error) {
    card.appendChild(el('p', 'card__error', d.canonical_error));
    return card;
  }
  const c = d.canonical;
  if (!c) {
    card.appendChild(el('p', 'card__muted', t('webui.isrc.canonical.none')));
    return card;
  }
  const dl = el('dl', 'defs');
  const add = (k, v) => { dl.appendChild(el('dt', null, k)); dl.appendChild(el('dd', 'mono', v)); };
  add('cid', c.cid);
  add(t('webui.isrc.canonical.track'), `${c.title} — ${(c.artists || []).join(', ')}${c.album ? ' · ' + c.album : ''}`);
  if (c.duration_ms) add(t('webui.isrc.canonical.duration'), mmss(c.duration_ms));
  add('ISRC alias set', (c.isrc || []).join(' · ') || '—');
  for (const [prov, m] of Object.entries(c.mappings || {})) {
    const id = m.id || t('webui.isrc.canonical.unavailable');
    const score = t('webui.isrc.canonical.confidence', { confidence: m.confidence });
    add(`mapping ${prov}`, `${id} · ${score} · ${m.source}${m.pinned ? ' · ' + t('webui.isrc.canonical.pinned') : ''}`);
  }
  for (const cf of c.conflicts || []) add('conflict', `${cf.provider}:${cf.provider_id} ${cf.title || ''}`);
  card.appendChild(dl);
  const pls = el('div', 'isrc__pls');
  pls.appendChild(el('h4', 'card__sub', t('webui.isrc.canonical.playlists', { count: (c.playlists || []).length })));
  for (const p of c.playlists || []) {
    const line = el('div', 'isrc__pl');
    line.appendChild(el('span', null, p.name));
    line.appendChild(el('span', 'mono muted', `#${p.pos}`));
    for (const [prov, id] of Object.entries(p.links || {})) line.appendChild(el('span', 'chip', `${prov}:${id}`));
    pls.appendChild(line);
  }
  card.appendChild(pls);
  return card;
}

export function initISRC(root, api, initial) {
  const out = root.querySelector('#isrc-out');
  const input = root.querySelector('#isrc-input');
  const status = root.querySelector('#isrc-status');
  // 序號 + AbortController:兩次查詢重疊時,舊的那次不可以把結果接在新的後面(伺服器端每家給到 30 秒,
  // 一個沒登入的平台就足以讓第一次查詢在背景吊很久)。取消舊 fetch 也讓伺服器的 r.Context() 提早收工。
  let seq = 0;
  let inflight = null;

  async function look(raw) {
    const isrc = (raw || '').trim();
    if (!isrc) return;
    const mine = ++seq;
    if (inflight) inflight.abort();
    inflight = new AbortController();
    location.hash = '#/isrc/' + encodeURIComponent(isrc);
    status.textContent = t('webui.isrc.looking_up');
    out.replaceChildren();
    let r;
    try {
      r = await api.fetch('/api/isrc/' + encodeURIComponent(isrc), { signal: inflight.signal });
    } catch (e) {
      if (mine === seq && e.name !== 'AbortError') status.textContent = t('webui.isrc.unreachable', { err: e.message });
      return;
    }
    if (mine !== seq) return;
    if (!r.ok) {
      let msg = r.statusText;
      try { msg = (await r.json()).error || msg; } catch (_) { /* 非 JSON */ }
      if (mine === seq) status.textContent = msg;
      return;
    }
    const d = await r.json();
    if (mine !== seq) return;
    status.textContent = '';
    out.replaceChildren(partsBar(d.parts));
    for (const id of Object.keys(d.providers).sort()) out.appendChild(providerCard(id, d.providers[id]));
    out.appendChild(canonicalCard(d));
  }

  input.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
    ev.preventDefault();
    look(input.value);
  });
  root.querySelector('#isrc-go').addEventListener('click', () => look(input.value));
  if (initial) {
    input.value = initial;
    look(initial);
  }
  // show:路由每次 hashchange 都會叫;與輸入框現值相同就不重查(look 自己設 hash 造成的那次)。
  return {
    show(isrc) {
      if (!isrc || isrc === input.value.trim()) return;
      input.value = isrc;
      look(isrc);
    },
  };
}
