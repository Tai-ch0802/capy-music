// theater.js:沉浸式「水豚溫泉劇場 / 黑膠內頁模式」(z 鍵或點底部播放列封面)。
// 左欄:大封面(無封面如 Apple Music 時顯示純靜態黑膠水豚 SVG,零額外網路請求)+ 側面動態泡湯水豚(單眼、ear cx < eye cx < nose cx、
// eye r >= ear rx、開放路徑 fill: none)+ 直達播放控制(/api/player,不佔 runMu)。
// 右欄:零網路請求顯示 /api/now 帶來的本機已快取歌曲介紹(含 MV 連結與不可關閉的免責聲明);未快取時只有使用者親手按按鈕才跑 capy wiki。
import { el, btn, providerName } from './pages/common.js';
import { mmss } from './table.js';
import { lineSplitter, wikiRenderer } from './pages/wiki.js';
import { t } from './i18n.js';

const SVG = 'http://www.w3.org/2000/svg';
function svgEl(tag, attrs) {
  const x = document.createElementNS ? document.createElementNS(SVG, tag) : document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) x.setAttribute(k, String(v));
  return x;
}

// vinylCapybaraSVG:純靜態黑膠水豚佔位圖(Apple Music 等無 artwork_url 時使用;零網路請求)。
export function vinylCapybaraSVG() {
  const s = svgEl('svg', { viewBox: '0 0 64 64', class: 'vinyl-svg', role: 'img', 'aria-label': t('webui.player.art_placeholder') });
  s.append(
    svgEl('circle', { class: 'vinyl-svg__disc', cx: 32, cy: 32, r: 29 }),
    svgEl('circle', { class: 'vinyl-svg__groove', cx: 32, cy: 32, r: 23 }),
    svgEl('circle', { class: 'vinyl-svg__groove', cx: 32, cy: 32, r: 18 }),
    svgEl('ellipse', { class: 'vinyl-svg__ear', cx: 26, cy: 23, rx: 3, ry: 4 }),
    svgEl('circle', { class: 'vinyl-svg__label', cx: 32, cy: 32, r: 11 }),
    svgEl('circle', { class: 'vinyl-svg__hole', cx: 32, cy: 32, r: 2.5 }),
  );
  return s;
}

// onsenCapybaraSVG:側面泡在溫暖柚子湯裡的水豚(嚴守單眼側面比例:ear cx 128 < eye cx 150 < nose cx 184;eye r 5 >= ear rx 5;
// 嘴與蒸氣皆為開放路徑 fill: none)。定時器啟動時頭頂浮出一顆金黃柚子(data-yuzu="true")。
export function onsenCapybaraSVG() {
  const s = svgEl('svg', { viewBox: '0 0 224 136', class: 'onsen-svg', role: 'img', 'aria-label': t('webui.theater.capybara_aria') });
  s.append(
    svgEl('path', { class: 'onsen-svg__steam', d: 'M66 28 Q62 18 68 10' }),
    svgEl('path', { class: 'onsen-svg__steam', d: 'M96 24 Q92 14 98 6' }),
    svgEl('circle', { class: 'onsen-svg__yuzu', cx: 146, cy: 26, r: 7 }),
    svgEl('ellipse', { class: 'onsen-svg__line onsen-svg__ear', cx: 128, cy: 33, rx: 5, ry: 7 }),
    svgEl('path', { class: 'onsen-svg__line', d: 'M34 98 C16 98 14 74 22 58 C30 40 50 34 78 34 C112 34 140 36 172 36 Q194 36 194 58 L194 72 Q194 88 178 88 L160 88 C148 88 140 94 132 98 Z' }),
    svgEl('circle', { class: 'onsen-svg__eye', cx: 150, cy: 52, r: 5 }),
    svgEl('ellipse', { class: 'onsen-svg__dot', cx: 184, cy: 47, rx: 3.4, ry: 2.6 }),
    svgEl('path', { class: 'onsen-svg__stroke', d: 'M176 73 L187 72' }),
    svgEl('rect', { class: 'onsen-svg__tub', x: 10, y: 92, width: 204, height: 34, rx: 10 }),
    svgEl('rect', { class: 'onsen-svg__water', x: 18, y: 88, width: 188, height: 12, rx: 6 }),
  );
  return s;
}

function formatDate(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso).slice(0, 10);
  const p2 = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`;
}

export function createTheater(dlg, player, api, con, notice) {
  let wikiRunning = false;
  let shownKey = '';
  let seeking = false;

  const top = el('div', 'theater__top');
  const head = el('div', 'theater__head');
  const badge = el('span', 'theater__badge');
  const timerPill = el('span', 'theater__timer');
  timerPill.hidden = true;
  head.append(badge, timerPill);
  const closeBtn = el('button', 'btn btn--ghost btn--icon', '✕');
  closeBtn.type = 'button';
  closeBtn.addEventListener('click', () => dlg.close());
  top.append(head, closeBtn);

  const grid = el('div', 'theater__grid');

  // 左欄:封面 + 曲目 + 直達控制 + 溫泉水豚
  const stage = el('div', 'theater__stage');
  const artWrap = el('div', 'theater__art-wrap');
  const artImg = el('img', 'theater__art');
  artImg.alt = '';
  artImg.hidden = true;
  const artFallback = el('span', 'theater__art-fallback');
  artWrap.append(artImg, artFallback);

  const meta = el('div', 'theater__meta');
  const songTitle = el('div', 'theater__song');
  const songBy = el('div', 'theater__by');
  meta.append(songTitle, songBy);

  const ctl = el('div', 'theater__ctl');
  const prevBtn = el('button', 'btn btn--ghost btn--icon dock__ctl-btn', '⏮');
  prevBtn.type = 'button';
  const toggleBtn = el('button', 'btn dock__ctl-btn dock__ctl-btn--main', '▶');
  toggleBtn.type = 'button';
  const nextBtn = el('button', 'btn btn--ghost btn--icon dock__ctl-btn', '⏭');
  nextBtn.type = 'button';
  prevBtn.addEventListener('click', () => player.act({ action: 'prev' }));
  toggleBtn.addEventListener('click', () => player.togglePlay());
  nextBtn.addEventListener('click', () => player.act({ action: 'next' }));
  ctl.append(prevBtn, toggleBtn, nextBtn);

  const seekWrap = el('div', 'theater__seek');
  const posEl = el('span', 'dock__time num', '0:00');
  const seekIn = el('input', 'dock__range');
  seekIn.type = 'range';
  seekIn.min = '0';
  seekIn.max = '0';
  seekIn.value = '0';
  seekIn.step = '1000';
  seekIn.disabled = true;
  const durEl = el('span', 'dock__time num', '0:00');
  seekIn.addEventListener('input', () => {
    seeking = true;
    posEl.textContent = mmss(Number(seekIn.value) || 0);
  });
  seekIn.addEventListener('change', () => {
    const pos = Number(seekIn.value) || 0;
    seeking = false;
    player.seekTo(pos);
  });
  seekWrap.append(posEl, seekIn, durEl);

  const mascotHost = el('div', 'theater__mascot');
  stage.append(artWrap, meta, ctl, seekWrap, mascotHost);

  // 右欄:黑膠內頁 / 已快取歌曲介紹
  const notes = el('div', 'theater__notes');
  const notesHead = el('div', 'theater__notes-head');
  const notesTitle = el('h3', 'theater__notes-title');
  const notesBadge = el('span', 'theater__notes-badge');
  notesHead.append(notesTitle, notesBadge);
  const statusEl = el('p', 'page__note theater__status');
  statusEl.setAttribute('role', 'status');
  const footEl = el('div', 'form-row theater__foot');
  const promptsEl = el('div', 'theater__prompts');
  promptsEl.dataset.globalHost = '';
  const notesOut = el('div', 'wiki__out');
  const disclaimerEl = el('p', 'theater__disclaimer');
  disclaimerEl.hidden = true;
  notes.append(notesHead, statusEl, footEl, promptsEl, notesOut, disclaimerEl);

  grid.append(stage, notes);
  dlg.replaceChildren(top, grid);

  let mascotSvg = null;
  function ensureStaticTexts() {
    badge.textContent = t('webui.theater.title');
    closeBtn.setAttribute('aria-label', t('webui.theater.close'));
    prevBtn.setAttribute('aria-label', t('webui.player.prev'));
    nextBtn.setAttribute('aria-label', t('webui.player.next'));
    seekIn.setAttribute('aria-label', t('webui.player.seek'));
    notesTitle.textContent = t('webui.theater.notes_title');
    if (!artFallback.firstChild) artFallback.appendChild(vinylCapybaraSVG());
    else artFallback.firstChild.setAttribute('aria-label', t('webui.player.art_placeholder'));
    if (!mascotSvg) {
      mascotSvg = onsenCapybaraSVG();
      mascotHost.replaceChildren(mascotSvg);
    } else {
      mascotSvg.setAttribute('aria-label', t('webui.theater.capybara_aria'));
    }
  }

  dlg.addEventListener('close', () => { if (wikiRunning) con.stop(); });

  function setFoot(...bs) {
    footEl.replaceChildren(...bs);
    if (dlg.open && !dlg.contains(document.activeElement)) (bs[0] || closeBtn).focus();
  }

  function setStatus(text, warn = false) {
    statusEl.className = warn ? 'page__warn theater__status' : 'page__note theater__status';
    statusEl.textContent = text || '';
  }

  function renderCachedWiki(w, tr) {
    notesOut.replaceChildren();
    promptsEl.replaceChildren();
    setStatus('');
    if (!w) {
      notesBadge.textContent = '';
      disclaimerEl.hidden = true;
      if (tr && tr.title) {
        notesOut.appendChild(el('p', 'empty', t('webui.theater.empty')));
        setFoot(btn(t('webui.theater.ask_btn'), 'btn--primary', () => askLive(false)));
      } else {
        setFoot();
      }
      return;
    }
    notesBadge.textContent = t('webui.theater.cached_badge', { model: w.model || 'AI', date: formatDate(w.created_at) });
    const r = wikiRenderer(notesOut);
    const split = lineSplitter((l) => r.line(l));
    split.push(w.body || '');
    split.end();
    if (w.mv_url && /^https:\/\//.test(w.mv_url)) {
      const p = el('p', 'wiki__p');
      const a = el('a', 'wiki__link', t('webui.theater.mv_link'));
      a.href = w.mv_url;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      p.appendChild(a);
      notesOut.appendChild(p);
    }
    disclaimerEl.textContent = t('wiki.disclaimer') || w.disclaimer || '';
    disclaimerEl.hidden = !disclaimerEl.textContent;
    setFoot(btn(t('webui.wiki.refresh'), 'btn--ghost', () => askLive(true)));
  }

  function askLive(refresh) {
    const d = player.last;
    const tr = d && d.track;
    if (!tr || !tr.title) return;
    if (con.running) {
      document.dispatchEvent(new Event('capy:busy'));
      setStatus(t('webui.shell.busy_hint'), true);
      return;
    }
    notesOut.replaceChildren();
    promptsEl.replaceChildren();
    notesBadge.textContent = '';
    disclaimerEl.hidden = true;
    setStatus(t('webui.playlists.wiki_running'));
    const stopBtn = el('button', 'btn', t('webui.console.stop'));
    stopBtn.type = 'button';
    stopBtn.addEventListener('click', (ev) => {
      if (ev.detail > 1) return;
      stopBtn.textContent = t('webui.console.stopping');
      con.stop();
    });
    setFoot(stopBtn);

    const r = wikiRenderer(notesOut);
    const split = lineSplitter((l) => r.line(l));
    wikiRunning = true;
    const artists = (tr.artists || []).join(', ');
    const args = ['wiki', `--title=${tr.title}`, ...(artists ? [`--artist=${artists}`] : []), ...(refresh ? ['--refresh'] : [])];
    con.run('', {
      onStdout: (s) => split.push(s),
      onExit: async (code, msg, reason) => {
        wikiRunning = false;
        split.end();
        if (code === 0) {
          setStatus(t('webui.playlists.wiki_done'));
          disclaimerEl.textContent = t('wiki.disclaimer');
          disclaimerEl.hidden = false;
          setFoot(btn(t('webui.wiki.refresh'), 'btn--ghost', () => askLive(true)));
          player.tick();
          return;
        }
        const again = btn(t('webui.theater.ask_btn'), code > 0 ? 'btn--ghost' : 'btn--primary', () => askLive(false));
        if (reason === 'cancelled') { setStatus(t('webui.playlists.wiki_stopped')); setFoot(again); return; }
        setStatus((msg || '').replace(/^Error: /, '') || t('webui.playlists.wiki_failed'), true);
        setFoot(...(code > 0 ? [btn(t('webui.wiki.setup'), '', runSetup)] : []), again);
      },
    }, { args, label: t('webui.wiki.label.ask', { song: tr.title }), promptHost: promptsEl });
  }

  function runSetup() {
    if (con.running) {
      document.dispatchEvent(new Event('capy:busy'));
      return;
    }
    notesOut.replaceChildren();
    promptsEl.replaceChildren();
    setStatus('');
    setFoot();
    wikiRunning = true;
    let closedBy = '';
    con.run('', {
      onPromptClosed: (ev) => { closedBy = ev.reason; },
      onExit: (code, msg, reason) => {
        wikiRunning = false;
        if (code === 0) {
          setStatus(t('webui.playlists.wiki_ready'));
          setFoot(btn(t('webui.theater.ask_btn'), 'btn--primary', () => askLive(false)));
          return;
        }
        const unfinished = code === 1 && (reason === 'timeout' || closedBy === 'dismissed');
        if (reason === 'cancelled') setStatus(t('webui.playlists.wiki_stopped'));
        else if (unfinished) setStatus(t('webui.playlists.wiki_setup_unfinished'));
        else setStatus((msg || '').replace(/^Error: /, '') || t('webui.playlists.wiki_failed'), true);
        setFoot(btn(t('webui.wiki.setup'), '', runSetup));
      },
    }, { args: ['wiki', 'setup'], label: t('webui.wiki.label.setup'), promptHost: promptsEl });
  }

  function updateFromNow(d) {
    ensureStaticTexts();
    const tr = d && d.track;
    toggleBtn.textContent = d && d.playing ? '⏸' : '▶';
    toggleBtn.setAttribute('aria-label', d && d.playing ? t('webui.player.pause') : t('webui.player.play'));

    const tm = d && d.timer;
    const activeTimer = tm && tm.mode && tm.mode !== 'off';
    if (mascotSvg) mascotSvg.dataset.yuzu = activeTimer ? 'true' : '';
    if (activeTimer) {
      const time = mmss((tm.remaining_sec || 0) * 1000);
      if (tm.mode === 'pomodoro') {
        timerPill.textContent = tm.phase === 'break'
          ? t('webui.theater.yuzu_break', { time })
          : t('webui.theater.yuzu_focus', { time });
      } else if (tm.mode === 'end_of_track') {
        timerPill.textContent = t('webui.timer.active_eot');
      } else {
        timerPill.textContent = t('webui.theater.yuzu_sleep', { time });
      }
      timerPill.hidden = false;
    } else {
      timerPill.hidden = true;
    }

    if (!tr) {
      artImg.hidden = true;
      artFallback.hidden = false;
      songTitle.replaceChildren(document.createTextNode(t('webui.theater.idle')));
      songBy.textContent = d && d.provider ? providerName(d.provider) : '';
      seekIn.disabled = true;
      seekIn.max = '0';
      seekIn.value = '0';
      posEl.textContent = '0:00';
      durEl.textContent = '0:00';
      if (!wikiRunning && shownKey !== '__idle__') {
        shownKey = '__idle__';
        renderCachedWiki(null, null);
      }
      return;
    }

    if (tr.artwork_url && /^https:\/\//.test(tr.artwork_url)) {
      if (artImg.src !== tr.artwork_url) artImg.src = tr.artwork_url;
      artImg.hidden = false;
      artFallback.hidden = true;
    } else {
      artImg.hidden = true;
      artFallback.hidden = false;
    }

    if (tr.url && /^https:\/\//.test(tr.url)) {
      const a = el('a', null, tr.title);
      a.href = tr.url;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      songTitle.replaceChildren(a);
    } else {
      songTitle.replaceChildren(document.createTextNode(tr.title));
    }
    songBy.textContent = [providerName(d.provider), (tr.artists || []).join(', '), tr.album].filter(Boolean).join(' · ');

    const dur = Math.max(0, tr.duration_ms || 0);
    const pos = Math.min(dur || Infinity, Math.max(0, player.currentPositionMS()));
    seekIn.disabled = dur <= 0;
    seekIn.max = String(dur);
    if (!seeking) {
      seekIn.value = String(pos);
      posEl.textContent = mmss(pos);
    }
    durEl.textContent = mmss(dur);

    const key = `${d.provider || ''}|${tr.title || ''}|${(tr.artists || []).join(',')}|${d.wiki_cached ? d.wiki_cached.created_at : 'none'}`;
    if (!wikiRunning && key !== shownKey) {
      shownKey = key;
      renderCachedWiki(d.wiki_cached || null, tr);
    }
  }

  function updateProgress(posMS, durMS) {
    if (!dlg.open || seeking) return;
    seekIn.max = String(durMS);
    seekIn.value = String(posMS);
    posEl.textContent = mmss(posMS);
    durEl.textContent = mmss(durMS);
  }

  document.addEventListener('capy:now', (ev) => {
    if (dlg.open) updateFromNow(ev.detail);
  });
  document.addEventListener('capy:progress', (ev) => {
    if (dlg.open && ev.detail) updateProgress(ev.detail.pos, ev.detail.dur);
  });

  function open() {
    shownKey = '';
    updateFromNow(player.last || null);
    if (!dlg.open) dlg.showModal();
  }

  function close() {
    if (dlg.open) dlg.close();
  }

  function toggle() {
    if (dlg.open) close();
    else open();
  }

  return { open, close, toggle, dialog: dlg };
}
