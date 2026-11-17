// player.js:dock 的三區現代播放面板。每 2.5 秒打 GET /api/now(輪詢不串流),document.hidden 時停;
// 播放中每秒在本地平滑推進進度條(document.hidden 或拖曳進度條時暫停);
// 所有播放控制、音量、裝置切換與柚子湯定時器皆走直達端點(/api/player、/api/player/devices、/api/timer),
// 100% 不經 Console.run、不搶 runMu 單一序列槽——背景在跑 sync 或 wiki 時照樣秒速切歌與調音量。
import { t } from './i18n.js';
import { el, providerName } from './pages/common.js';
import { openCollect } from './collect.js';
import { vinylCapybaraSVG } from './theater.js';

const POLL_MS = 2500;
const STALE_DEAD_MS = 30000;

function mmss(ms) {
  const s = Math.max(0, Math.floor((ms || 0) / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

export async function postPlayerAction(api, notice, payload) {
  let r;
  try {
    r = await api.fetch('/api/player', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  } catch (e) {
    notice(e.message);
    return null;
  }
  let data = {};
  try { data = await r.json(); } catch (_) { /* ignore non-JSON */ }
  if (!r.ok) {
    notice(data.error || ('HTTP ' + r.status));
    return null;
  }
  if (data.opened_not_playing && data.message) notice(data.message);
  if (typeof CustomEvent === 'function' && typeof document.dispatchEvent === 'function') {
    document.dispatchEvent(new CustomEvent('capy:player-settled', { detail: data }));
  }
  return data;
}

export class Player {
  constructor(root, api, notice, con) {
    this.root = root;
    this.api = api;
    this.notice = notice;
    this.con = con;
    this.timer = null;
    this.interpTimer = null;
    this.fails = 0;
    this.last = null;
    this.lastSyncAt = 0;
    this.seeking = false;
    this.volDragging = false;
    this.preMuteVol = 50;
    this.onOpenTheater = null;
    this.mediaKey = '';

    const q = (id) => root.querySelector('#' + id);
    this.line = q('now-line');
    this.artBtn = q('now-art-btn');
    this.artImg = q('now-art');
    this.artFallback = q('now-art-fallback');
    this.provEl = q('now-prov');
    this.titleEl = q('now-title');
    this.subEl = q('now-sub');
    this.collectBtn = q('now-collect');
    this.shuffleBtn = q('now-shuffle');
    this.toggleBtn = q('now-toggle');
    this.repeatBtn = q('now-repeat');
    this.posEl = q('now-pos');
    this.seekIn = q('now-seek');
    this.durEl = q('now-dur');
    this.volWrap = q('now-vol-wrap');
    this.muteBtn = q('now-mute');
    this.volIn = q('now-vol');
    this.devWrap = q('now-dev-wrap');
    this.devBtn = q('now-dev-btn');
    this.devLabel = q('now-dev-label');
    this.devPop = q('now-dev-pop');
    this.timerWrap = q('now-timer-wrap');
    this.timerBtn = q('now-timer-btn');
    this.timerLabel = q('now-timer-label');
    this.timerPop = q('now-timer-pop');

    if (this.artFallback && !this.artFallback.firstChild) {
      try { this.artFallback.appendChild(vinylCapybaraSVG()); } catch (_) { /* before i18n in bare mocks */ }
    }
    if (this.artBtn) {
      this.artBtn.addEventListener('click', () => { if (this.onOpenTheater) this.onOpenTheater(); });
    }
    if (this.collectBtn) {
      this.collectBtn.addEventListener('click', () => this.openCollectNow());
    }

    this.root.querySelectorAll('[data-act]').forEach((b) => {
      b.addEventListener('click', () => this.handleButtonAct(b.dataset.act));
    });
    this.root.querySelectorAll('[data-cmd]').forEach((b) => {
      b.addEventListener('click', () => this.control(b.dataset.cmd));
    });

    if (this.seekIn) {
      this.seekIn.addEventListener('input', () => {
        this.seeking = true;
        if (this.posEl) this.posEl.textContent = mmss(Number(this.seekIn.value) || 0);
      });
      this.seekIn.addEventListener('change', () => {
        const pos = Number(this.seekIn.value) || 0;
        this.seeking = false;
        this.seekTo(pos);
      });
    }

    if (this.volIn) {
      this.volIn.addEventListener('input', () => {
        this.volDragging = true;
      });
      this.volIn.addEventListener('change', () => {
        const pct = Number(this.volIn.value) || 0;
        this.volDragging = false;
        this.setVolume(pct);
      });
    }

    if (this.devBtn && this.devPop) {
      this.devBtn.addEventListener('click', () => this.toggleDevicesPopover());
    }
    if (this.timerBtn && this.timerPop) {
      this.timerBtn.addEventListener('click', () => this.toggleTimerPopover());
    }

    document.addEventListener('click', (ev) => {
      const target = ev && ev.target;
      if (!target) return;
      if (this.devPop && !this.devPop.hidden && this.devWrap && !this.devWrap.contains(target)) {
        this.closePopovers();
      } else if (this.timerPop && !this.timerPop.hidden && this.timerWrap && !this.timerWrap.contains(target)) {
        this.closePopovers();
      }
    });
    document.addEventListener('capy:player-settled', () => { if (this.timer) this.tick(); });
    document.addEventListener('visibilitychange', () => (document.hidden ? this.stop() : this.start()));
    this.bindMediaSession();
  }

  start() {
    if (this.timer) return;
    this.tick();
    this.timer = setInterval(() => this.tick(), POLL_MS);
    this.interpTimer = setInterval(() => this.stepProgress(), 1000);
  }

  stop() {
    clearInterval(this.timer);
    clearInterval(this.interpTimer);
    this.timer = null;
    this.interpTimer = null;
  }

  closePopovers() {
    if (this.devPop) { this.devPop.hidden = true; this.devBtn?.setAttribute('aria-expanded', 'false'); }
    if (this.timerPop) { this.timerPop.hidden = true; this.timerBtn?.setAttribute('aria-expanded', 'false'); }
  }

  currentPositionMS() {
    const d = this.last;
    if (!d || !d.track) return 0;
    let pos = Math.max(0, d.position_ms || 0);
    if (d.playing && this.lastSyncAt > 0) {
      pos += Math.max(0, Date.now() - this.lastSyncAt);
    }
    if (d.track.duration_ms > 0) pos = Math.min(d.track.duration_ms, pos);
    return pos;
  }

  stepProgress() {
    if (typeof document !== 'undefined' && document.hidden) return;
    const d = this.last;
    if (!d || !d.playing || !d.track) return;
    const dur = Math.max(0, d.track.duration_ms || 0);
    const pos = this.currentPositionMS();
    if (!this.seeking) {
      if (this.posEl) this.posEl.textContent = mmss(pos);
      if (this.seekIn && dur > 0) this.seekIn.value = String(pos);
    }
    if (typeof CustomEvent === 'function' && typeof document.dispatchEvent === 'function') {
      document.dispatchEvent(new CustomEvent('capy:progress', { detail: { pos, dur } }));
    }
  }

  async tick() {
    let d;
    try {
      const r = await this.api.fetch('/api/now');
      if (!r.ok) throw new Error('HTTP ' + r.status);
      d = await r.json();
    } catch (_) {
      if (++this.fails >= 5) this.disconnected();
      return;
    }
    this.fails = 0;
    if (d.stale && (d.stale_ms || 0) > STALE_DEAD_MS) { this.disconnected(); return; }
    this.root.dataset.nowConnected = 'true';
    this.render(d);
  }

  disconnected() {
    this.root.dataset.nowConnected = 'false';
    const msg = t('webui.player.disconnected');
    if (this.line) this.line.textContent = msg;
    if (this.titleEl) this.titleEl.replaceChildren(document.createTextNode(msg));
    if (this.subEl) this.subEl.textContent = '';
  }

  render(d) {
    this.last = d;
    this.lastSyncAt = Date.now();
    if (this.artFallback) {
      if (!this.artFallback.firstChild) this.artFallback.appendChild(vinylCapybaraSVG());
      else this.artFallback.firstChild.setAttribute('aria-label', t('webui.player.art_placeholder'));
    }
    if (typeof CustomEvent === 'function' && typeof document.dispatchEvent === 'function') {
      document.dispatchEvent(new CustomEvent('capy:now', { detail: d }));
    }
    this.root.dataset.playing = String(!!d.playing);
    this.root.dataset.stale = d.stale ? 'true' : '';

    this.renderControls(d);
    this.renderTimerSummary(d.timer || null);
    this.syncMediaSession(d);

    const name = providerName(d.provider);
    const age = d.stale ? ' · ' + t('webui.player.age', { count: Math.round((d.stale_ms || 0) / 1000) }) : '';

    if (d.error) {
      const msg = t('webui.player.error', { provider: name, error: d.error });
      this.plain(msg);
      this.renderVisualEmpty(name, d.error);
      return;
    }
    if (!d.track) {
      const msg = d.playing ? `${name} · ▶${age}` : t('webui.player.idle', { provider: name });
      this.plain(msg);
      this.renderVisualEmpty(name, d.playing ? `▶${age}` : t('webui.player.idle', { provider: name }));
      return;
    }

    const tr = d.track;
    const url = /^https:\/\//.test(tr.url || '') ? tr.url : '';
    const dev = d.device ? ` · ${d.device.name}${d.device.volume_known ? ` · 🔊 ${d.device.volume_pct}` : ''}` : '';
    this.trackLine(`${name} · ${d.playing ? '▶' : '⏸'} `, tr.title, url,
      ` — ${(tr.artists || []).join(', ')} · ${mmss(d.position_ms)} / ${mmss(tr.duration_ms)}${dev}${age}`);
    this.renderVisualTrack(d, tr, name, url, age);
  }

  renderVisualEmpty(provName, message) {
    if (this.artImg) this.artImg.hidden = true;
    if (this.artFallback) this.artFallback.hidden = false;
    if (this.provEl) {
      this.provEl.textContent = provName || '';
      this.provEl.hidden = !provName;
    }
    if (this.titleEl) {
      this.titleParts = null;
      this.titleEl.replaceChildren(document.createTextNode(message));
    }
    if (this.subEl) this.subEl.textContent = '';
    if (this.collectBtn) this.collectBtn.hidden = true;
    if (this.seekIn) {
      this.seekIn.disabled = true;
      this.seekIn.max = '0';
      this.seekIn.value = '0';
    }
    if (this.posEl) this.posEl.textContent = '0:00';
    if (this.durEl) this.durEl.textContent = '0:00';
  }

  renderVisualTrack(d, tr, provName, url, age) {
    if (this.artImg && this.artFallback) {
      if (tr.artwork_url && /^https:\/\//.test(tr.artwork_url)) {
        if (this.artImg.src !== tr.artwork_url) this.artImg.src = tr.artwork_url;
        this.artImg.hidden = false;
        this.artFallback.hidden = true;
      } else {
        this.artImg.hidden = true;
        this.artFallback.hidden = false;
      }
    }
    if (this.provEl) {
      this.provEl.textContent = provName;
      this.provEl.hidden = false;
    }
    if (this.titleEl) {
      let node = this.titleParts;
      if (!node || (node.tagName === 'A') !== !!url) {
        node = document.createElement(url ? 'a' : 'span');
        if (url) {
          node.target = '_blank';
          node.rel = 'noopener noreferrer';
        }
        this.titleParts = node;
        this.titleEl.replaceChildren(node);
      }
      if (url) node.href = url;
      node.textContent = tr.title;
    }
    if (this.subEl) {
      const sub = [(tr.artists || []).join(', '), tr.album].filter(Boolean).join(' · ');
      this.subEl.textContent = sub + age;
    }
    if (this.collectBtn) {
      const inPls = d.in_playlists || [];
      const collected = inPls.length > 0;
      this.collectBtn.hidden = false;
      this.collectBtn.dataset.collected = collected ? 'true' : '';
      this.collectBtn.textContent = collected ? '✓' : '+';
      const label = collected
        ? t('webui.collect.collected_label', { count: inPls.length, title: tr.title })
        : t('webui.collect.button_label', { title: tr.title });
      this.collectBtn.setAttribute('aria-label', label);
      this.collectBtn.title = label;
    }
    const dur = Math.max(0, tr.duration_ms || 0);
    const pos = Math.min(dur || Infinity, Math.max(0, d.position_ms || 0));
    if (this.seekIn) {
      this.seekIn.disabled = dur <= 0;
      this.seekIn.max = String(dur);
      if (!this.seeking) this.seekIn.value = String(pos);
    }
    if (this.posEl && !this.seeking) this.posEl.textContent = mmss(pos);
    if (this.durEl) this.durEl.textContent = mmss(dur);
  }

  renderControls(d) {
    if (this.toggleBtn) {
      this.toggleBtn.textContent = d.playing ? '⏸' : '▶';
      this.toggleBtn.setAttribute('aria-label', d.playing ? t('webui.player.pause') : t('webui.player.play'));
    }
    if (this.shuffleBtn) {
      this.shuffleBtn.dataset.active = d.shuffle ? 'true' : '';
      this.shuffleBtn.setAttribute('aria-pressed', String(!!d.shuffle));
    }
    if (this.repeatBtn) {
      const rep = d.repeat || 'off';
      this.repeatBtn.dataset.active = rep === 'context' || rep === 'track' ? 'true' : '';
      this.repeatBtn.textContent = rep === 'track' ? '🔂' : '🔁';
      const repLabel = rep === 'track'
        ? t('webui.player.repeat_track')
        : rep === 'context' ? t('webui.player.repeat_context') : t('webui.player.repeat_off');
      this.repeatBtn.setAttribute('aria-label', repLabel);
      this.repeatBtn.title = repLabel;
    }
    const dev = d.device;
    if (this.volIn && this.muteBtn) {
      const known = !!(dev && dev.volume_known);
      this.volIn.disabled = !known;
      if (known) {
        const pct = Math.min(100, Math.max(0, dev.volume_pct || 0));
        if (pct > 0) this.preMuteVol = pct;
        if (!this.volDragging) this.volIn.value = String(pct);
        this.muteBtn.disabled = false;
        this.muteBtn.textContent = pct === 0 ? '🔇' : '🔊';
        this.muteBtn.setAttribute('aria-label', pct === 0 ? t('webui.player.unmute') : t('webui.player.mute'));
      } else {
        if (!this.volDragging) this.volIn.value = '0';
        this.muteBtn.disabled = true;
        this.muteBtn.textContent = '🔊';
        this.muteBtn.setAttribute('aria-label', t('webui.player.mute'));
      }
    }
    if (this.devLabel) {
      this.devLabel.textContent = (dev && dev.name) || '';
    }
  }

  renderTimerSummary(tm) {
    const active = !!(tm && tm.mode && tm.mode !== 'off');
    if (this.timerBtn) this.timerBtn.dataset.active = active ? 'true' : '';
    if (this.timerLabel) {
      if (!active) {
        this.timerLabel.textContent = t('webui.timer.short');
      } else if (tm.mode === 'end_of_track') {
        this.timerLabel.textContent = mmss((tm.remaining_sec || 0) * 1000);
      } else {
        this.timerLabel.textContent = mmss((tm.remaining_sec || 0) * 1000);
      }
    }
    if (this.timerPop && !this.timerPop.hidden) {
      this.renderTimerPopover(tm);
    }
  }

  plain(text) {
    if (!this.line) return;
    this.line.textContent = text;
    this.parts = null;
  }

  trackLine(head, title, url, tail) {
    if (!this.line) return;
    let p = this.parts;
    if (!p || (p.title.tagName === 'A') !== !!url) {
      const node = document.createElement(url ? 'a' : 'span');
      if (url) {
        node.target = '_blank';
        node.rel = 'noopener noreferrer';
      }
      p = this.parts = { head: document.createElement('span'), title: node, tail: document.createElement('span') };
      this.line.replaceChildren(p.head, p.title, p.tail);
    }
    if (url) p.title.href = url;
    p.head.textContent = head;
    p.title.textContent = title;
    p.tail.textContent = tail;
  }

  handleButtonAct(act) {
    switch (act) {
      case 'toggle': this.togglePlay(); break;
      case 'prev': this.act({ action: 'prev' }); break;
      case 'next': this.act({ action: 'next' }); break;
      case 'shuffle': this.toggleShuffle(); break;
      case 'repeat': this.cycleRepeat(); break;
      case 'mute': this.toggleMute(); break;
      default: break;
    }
  }

  togglePlay() {
    const playing = this.root.dataset.playing === 'true';
    return this.act({ action: playing ? 'pause' : 'play' });
  }

  toggleShuffle() {
    const cur = !!(this.last && this.last.shuffle);
    return this.act({ action: 'shuffle', on: !cur });
  }

  cycleRepeat() {
    const cur = (this.last && this.last.repeat) || 'off';
    const next = cur === 'off' ? 'context' : cur === 'context' ? 'track' : 'off';
    return this.act({ action: 'repeat', repeat: next });
  }

  toggleMute() {
    const dev = this.last && this.last.device;
    if (!dev || !dev.volume_known) return Promise.resolve(null);
    const cur = dev.volume_pct || 0;
    if (cur > 0) {
      this.preMuteVol = cur;
      return this.setVolume(0);
    }
    return this.setVolume(this.preMuteVol > 0 ? this.preMuteVol : 50);
  }

  seekTo(posMS) {
    const clamped = Math.max(0, Math.round(posMS));
    if (this.last) {
      this.last.position_ms = clamped;
      this.lastSyncAt = Date.now();
    }
    return this.act({ action: 'seek', position_ms: clamped });
  }

  seekBy(sec) {
    const d = this.last;
    if (!d || !d.track) return;
    const posMS = Math.max(0, this.currentPositionMS() + sec * 1000);
    this.seekTo(posMS);
  }

  setVolume(pct) {
    const clamped = Math.min(100, Math.max(0, Math.round(pct)));
    if (this.last && this.last.device && this.last.device.volume_known) {
      this.last.device.volume_pct = clamped;
    }
    return this.act({ action: 'volume', volume_pct: clamped });
  }

  volBy(delta) {
    const dev = this.last && this.last.device;
    if (!dev || !dev.volume_known) return;
    this.setVolume(dev.volume_pct + delta);
  }

  openCollectNow() {
    const d = this.last;
    const tr = d && d.track;
    if (!tr) {
      this.notice(t('webui.collect.nothing_playing'));
      return;
    }
    openCollect({
      now: true,
      provider: d.provider,
      title: tr.title,
      artists: tr.artists || [],
      inPlaylists: d.in_playlists || [],
    });
  }

  // control:相容舊字串呼叫('play' / 'pause' / 'next' / 'prev' / 'seek <sec>' / 'vol <pct>'),一律轉發至直達 /api/player。
  async control(cmd) {
    const parts = String(cmd || '').trim().split(/\s+/);
    const verb = parts[0];
    if (verb === 'seek') return this.act({ action: 'seek', position_ms: Math.max(0, Number(parts[1]) || 0) * 1000 });
    if (verb === 'vol') return this.act({ action: 'volume', volume_pct: Math.min(100, Math.max(0, Number(parts[1]) || 0)) });
    return this.act({ action: verb });
  }

  async act(payload) {
    const body = { ...payload };
    if (!body.provider && this.last && this.last.provider) {
      body.provider = this.last.provider;
    }
    return postPlayerAction(this.api, this.notice, body);
  }

  async toggleDevicesPopover() {
    if (!this.devPop) return;
    const willOpen = this.devPop.hidden;
    this.closePopovers();
    if (!willOpen) return;
    this.devPop.hidden = false;
    this.devBtn?.setAttribute('aria-expanded', 'true');
    this.devPop.replaceChildren(
      el('p', 'dock__pop-title', t('webui.player.devices')),
      el('p', 'dock__pop-note', t('webui.player.devices_loading')),
    );
    const prov = (this.last && this.last.provider) || '';
    const q = prov ? `?provider=${encodeURIComponent(prov)}` : '';
    let data;
    try {
      const r = await this.api.fetch('/api/player/devices' + q);
      data = await r.json();
    } catch (e) {
      this.devPop.replaceChildren(el('p', 'card__error', e.message));
      return;
    }
    if (this.devPop.hidden) return;
    const pName = providerName((data && data.provider) || prov);
    this.devPop.replaceChildren(el('p', 'dock__pop-title', t('webui.player.devices')));
    if (data && data.error) {
      this.devPop.appendChild(el('p', 'dock__pop-note', data.error));
      return;
    }
    const list = (data && data.devices) || [];
    if (!list.length) {
      this.devPop.appendChild(el('p', 'dock__pop-note', t('webui.player.devices_empty', { provider: pName })));
      return;
    }
    const canTransfer = !!(data && data.supported_transfer);
    if (!canTransfer) {
      this.devPop.appendChild(el('p', 'dock__pop-note', t('webui.player.devices_readonly', { provider: pName })));
    }
    for (const dev of list) {
      const item = el('button', 'btn dock__pop-item');
      item.type = 'button';
      item.disabled = !canTransfer || !dev.id;
      item.dataset.active = dev.active ? 'true' : '';
      item.appendChild(el('span', null, dev.name || dev.id));
      if (dev.active) item.appendChild(el('span', 'chip', t('webui.player.device_active')));
      item.addEventListener('click', async () => {
        this.closePopovers();
        await this.act({ action: 'transfer', device_id: dev.id, provider: data.provider || prov });
      });
      this.devPop.appendChild(item);
    }
  }

  toggleTimerPopover() {
    if (!this.timerPop) return;
    const willOpen = this.timerPop.hidden;
    this.closePopovers();
    if (!willOpen) return;
    this.timerPop.hidden = false;
    this.timerBtn?.setAttribute('aria-expanded', 'true');
    this.renderTimerPopover((this.last && this.last.timer) || null);
  }

  renderTimerPopover(tm) {
    if (!this.timerPop) return;
    this.timerPop.replaceChildren(el('p', 'dock__pop-title', t('webui.timer.title')));
    const active = !!(tm && tm.mode && tm.mode !== 'off');
    if (active) {
      const time = mmss((tm.remaining_sec || 0) * 1000);
      let statusText = '';
      if (tm.mode === 'end_of_track') statusText = t('webui.timer.active_eot');
      else if (tm.mode === 'pomodoro' && tm.phase === 'break') statusText = t('webui.timer.active_break', { time, cycles: tm.cycles || 1 });
      else if (tm.mode === 'pomodoro') statusText = t('webui.timer.active_focus', { time });
      else statusText = t('webui.timer.active_countdown', { time });
      this.timerPop.appendChild(el('p', 'dock__pop-status', statusText));
      if (tm.fading) this.timerPop.appendChild(el('p', 'dock__pop-note', t('webui.timer.fading')));
      if (tm.total_sec > 0) {
        const bar = el('progress', 'bar');
        bar.max = tm.total_sec;
        bar.value = Math.max(0, tm.total_sec - (tm.remaining_sec || 0));
        bar.setAttribute('aria-label', t('webui.timer.progress'));
        this.timerPop.appendChild(bar);
      }
    }
    const presets = [
      { label: t('webui.timer.end_of_track'), body: { mode: 'end_of_track' }, match: tm && tm.mode === 'end_of_track' },
      { label: t('webui.timer.min_15'), body: { mode: 'countdown', minutes: 15 }, match: tm && tm.mode === 'countdown' && tm.total_sec === 900 },
      { label: t('webui.timer.min_30'), body: { mode: 'countdown', minutes: 30 }, match: tm && tm.mode === 'countdown' && tm.total_sec === 1800 },
      { label: t('webui.timer.min_45'), body: { mode: 'countdown', minutes: 45 }, match: tm && tm.mode === 'countdown' && tm.total_sec === 2700 },
      { label: t('webui.timer.min_60'), body: { mode: 'countdown', minutes: 60 }, match: tm && tm.mode === 'countdown' && tm.total_sec === 3600 },
      { label: t('webui.timer.pomodoro'), body: { mode: 'pomodoro', focus_minutes: 25, break_minutes: 5 }, match: tm && tm.mode === 'pomodoro' },
    ];
    for (const p of presets) {
      const b = el('button', 'btn dock__pop-item', p.label);
      b.type = 'button';
      b.dataset.active = p.match ? 'true' : '';
      b.addEventListener('click', () => this.setTimer(p.body));
      this.timerPop.appendChild(b);
    }
    if (active) {
      const offBtn = el('button', 'btn btn--ghost dock__pop-item', t('webui.timer.off'));
      offBtn.type = 'button';
      offBtn.addEventListener('click', () => this.setTimer({ mode: 'off' }));
      this.timerPop.appendChild(offBtn);
    }
    this.timerPop.appendChild(el('p', 'dock__pop-note', t('webui.timer.note')));
  }

  async setTimer(spec) {
    const prov = (this.last && this.last.provider) || '';
    const body = prov && spec.mode !== 'off' ? { ...spec, provider: prov } : spec;
    try {
      const r = await this.api.fetch('/api/timer', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const snap = await r.json();
      if (!r.ok) {
        this.notice(snap.error || ('HTTP ' + r.status));
        return;
      }
      if (this.last) {
        this.last.timer = snap && snap.mode !== 'off' ? snap : undefined;
        if (typeof CustomEvent === 'function' && typeof document.dispatchEvent === 'function') {
          document.dispatchEvent(new CustomEvent('capy:now', { detail: this.last }));
        }
      }
      this.renderTimerSummary(snap);
      this.closePopovers();
    } catch (e) {
      this.notice(e.message);
    }
  }

  bindMediaSession() {
    const ms = globalThis.navigator && globalThis.navigator.mediaSession;
    if (!ms || typeof ms.setActionHandler !== 'function') return;
    const handlers = [
      ['play', () => this.act({ action: 'play' })],
      ['pause', () => this.act({ action: 'pause' })],
      ['previoustrack', () => this.act({ action: 'prev' })],
      ['nexttrack', () => this.act({ action: 'next' })],
      ['seekto', (details) => {
        if (details && typeof details.seekTime === 'number') this.seekTo(details.seekTime * 1000);
      }],
    ];
    for (const [name, fn] of handlers) {
      try { ms.setActionHandler(name, fn); } catch (_) { /* unsupported handler on some browsers */ }
    }
  }

  syncMediaSession(d) {
    const ms = globalThis.navigator && globalThis.navigator.mediaSession;
    if (!ms) return;
    const tr = d && d.track;
    if (!tr) {
      ms.playbackState = 'none';
      return;
    }
    ms.playbackState = d.playing ? 'playing' : 'paused';
    const key = `${tr.title}|${(tr.artists || []).join(',')}|${tr.album || ''}|${tr.artwork_url || ''}`;
    if (key !== this.mediaKey && typeof globalThis.MediaMetadata === 'function') {
      this.mediaKey = key;
      const artwork = tr.artwork_url && /^https:\/\//.test(tr.artwork_url) ? [{ src: tr.artwork_url }] : [];
      try {
        ms.metadata = new globalThis.MediaMetadata({
          title: tr.title || '',
          artist: (tr.artists || []).join(', '),
          album: tr.album || '',
          artwork,
        });
      } catch (_) { /* ignore */ }
    }
  }
}
