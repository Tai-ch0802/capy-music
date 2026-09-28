// wiki.js:#/wiki —— 歌曲 wiki(決策 59)。頁面只跑命令(決策 45):「查這首」跑 capy wiki --provider <播放列上的平台>,
// 手打歌名跑 capy wiki --title … --artist …,「設定 AI」跑 capy wiki setup(表單就地出現,promptHost)。
// 命令的 stdout 是逐行的 markdown-lite,這裡邊到邊畫;AI 回應期間佔著序列槽,播放列的按鈕會被擋、中止可按(計畫 Q71)。
import { el, quote, btn, field, input, providerName, emptyState, pageHead } from './common.js';
import { t } from '../i18n.js';

// lineSplitter:stdout 事件可能切在行中間——湊到 \n 才算一行,end() 把沒有換行的尾巴也交出去。
export function lineSplitter(onLine) {
  let buf = '';
  return {
    push(text) {
      buf += text;
      let i;
      while ((i = buf.indexOf('\n')) >= 0) {
        onLine(buf.slice(0, i));
        buf = buf.slice(i + 1);
      }
    },
    end() {
      if (buf) onLine(buf);
      buf = '';
    },
  };
}

// 連結在句尾時,全形的逗號、句號、頓號、分號、冒號、驚嘆號、問號、右括號、右引號不算進網址(寫成 \u 跳脫:程式碼裡不放中文字面)。
const URL_RE = /https:\/\/[^\s<>"')\]\uFF0C\u3002\u3001\uFF1B\uFF1A\uFF01\uFF1F\uFF09\u300D\u300F]+/g;

// inline:**粗體** 與 https:// 連結;其餘都是文字節點。只認 https(javascript: 之類進不了 href),沒有 innerHTML。
function inline(parent, text) {
  const parts = text.split(/(\*\*[^*]+\*\*)/);
  for (const part of parts) {
    if (!part) continue;
    if (part.startsWith('**') && part.endsWith('**') && part.length > 4) {
      parent.appendChild(el('strong', null, part.slice(2, -2)));
      continue;
    }
    let last = 0;
    for (const m of part.matchAll(URL_RE)) {
      if (m.index > last) parent.appendChild(document.createTextNode(part.slice(last, m.index)));
      const a = el('a', 'wiki__link', m[0]);
      a.href = m[0];
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      parent.appendChild(a);
      last = m.index + m[0].length;
    }
    if (last < part.length) parent.appendChild(document.createTextNode(part.slice(last)));
  }
}

// wikiRenderer:一行一個節點——"## " 是標題、"- " / "* " 是清單項、空行結束清單、其餘各自一段(AI 的輸出本來就一段一行)。
export function wikiRenderer(out) {
  let list = null;
  return {
    line(raw) {
      const s = raw.replace(/\r$/, '');
      if (!s.trim()) { list = null; return; }
      if (s.startsWith('## ')) {
        list = null;
        out.appendChild(el('h3', 'wiki__h', s.slice(3).trim()));
        return;
      }
      if (/^[-*] /.test(s)) {
        if (!list) { list = el('ul', 'wiki__list'); out.appendChild(list); }
        const li = el('li');
        inline(li, s.slice(2));
        list.appendChild(li);
        return;
      }
      list = null;
      const p = el('p', 'wiki__p');
      inline(p, s);
      out.appendChild(p);
    },
  };
}

export function initWiki(root, api, con, notice, providers, player) {
  pageHead(root, t('webui.wiki.title'), t('webui.wiki.lead'));
  const nowLine = el('span', 'wiki__now-line', t('webui.wiki.now_none'));
  const askNow = btn(t('webui.wiki.ask_now'), 'btn--primary', () => ask(''));
  const now = el('div', 'wiki__now');
  now.append(el('span', 'wiki__now-label', t('webui.wiki.now_label')), nowLine, askNow);

  const title = input('sans', t('webui.wiki.title_placeholder'));
  const artist = input('sans', t('webui.wiki.artist_placeholder'));
  const bar = el('div', 'form-row');
  const askBtn = btn(t('webui.wiki.ask'), '', () => {
    const q = title.value.trim();
    if (!q) { title.focus(); return; }
    const a = artist.value.trim();
    ask(`--title ${quote(q)}${a ? ` --artist ${quote(a)}` : ''}`, q);
  });
  const setupBtn = btn(t('webui.wiki.setup'), 'btn--ghost', () => {
    prompts.replaceChildren();
    con.run('', {}, { args: ['wiki', 'setup'], label: t('webui.wiki.label.setup'), promptHost: prompts });
  });
  bar.append(field(t('webui.wiki.title_field'), title, true), field(t('webui.wiki.artist_field'), artist), askBtn, setupBtn);
  const prompts = el('div', 'wiki__prompts'); // wiki setup 的表單就地回答(promptHost),不把人丟進主控台
  const status = el('p', 'wiki__status');
  status.setAttribute('role', 'status');
  const out = el('div', 'wiki__out');
  const actions = el('div', 'wiki__actions');
  root.append(now, bar, prompts, status, out, actions);
  out.appendChild(emptyState(t('webui.wiki.empty', { button: t('webui.wiki.ask_now') })));

  // 播放列上的那首(player.js 每一輪 render 完會廣播 capy:now;這裡只讀 player.last,不另外打 /api/now、不開計時器)。
  const refreshNow = () => {
    if (root.hidden) return; // 頁面藏著就不畫(主控台對 hidden 頁也是這樣)
    const d = player && player.last;
    const tr = d && d.track;
    nowLine.textContent = tr ? `${providerName(d.provider)} · ${tr.title} — ${(tr.artists || []).join(', ')}` : t('webui.wiki.now_none');
  };
  refreshNow();
  document.addEventListener('capy:now', refreshNow);

  // lastManual:上一次是手打的歌名(--title …)就記著;是「查這首」就是空字串——「再問一次」對手打的照原樣重跑,
  // 對「查這首」則**重新讀播放列上的平台**:按下去的當下面板可能已經換了平台,帶舊的平台會問到另一首歌。
  let lastManual = '';
  function ask(argText, songLabel, refresh) {
    // 「查這首」:帶播放列上的平台(同 player.js 的控制鈕);還沒有任何狀態就不帶,跟終端機一樣先問預設平台再問其他家。
    let args = argText;
    if (!argText) {
      refreshNow(); // 問的就是面板上這首:那一行也同步到當下的面板,不等下一個 2.5 秒
      const p = player && player.last && player.last.provider;
      if (p) args = `--provider ${p}`;
      songLabel = (player && player.last && player.last.track && player.last.track.title) || '';
    }
    lastManual = argText;
    run(`wiki ${refresh ? '--refresh ' : ''}${args}`.trim(), songLabel);
  }
  for (const inp of [title, artist]) {
    inp.addEventListener('keydown', (ev) => {
      if (ev.key !== 'Enter' || ev.isComposing || ev.keyCode === 229) return;
      ev.preventDefault();
      askBtn.click();
    });
  }

  function run(line, songLabel) {
    out.replaceChildren();
    actions.replaceChildren();
    status.textContent = t('webui.wiki.running');
    const r = wikiRenderer(out);
    const split = lineSplitter((l) => r.line(l));
    con.run(line, {
      onStdout: (s) => split.push(s),
      onExit: (code, msg) => {
        split.end();
        status.textContent = '';
        if (code !== 0 && !out.firstChild) out.appendChild(emptyState(msg || t('webui.wiki.failed')));
        if (code === 0) {
          actions.appendChild(btn(t('webui.wiki.refresh'), 'btn--ghost', () => ask(lastManual, songLabel, true)));
        }
      },
    }, { label: songLabel ? t('webui.wiki.label.ask', { song: songLabel }) : t('webui.wiki.label.ask_now') });
  }
}
