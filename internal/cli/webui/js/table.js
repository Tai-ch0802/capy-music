// table.js:ui.TableWriter 送來的整張表。原子欄(ID / CID / PID / ISRC / DEVICE / REASON_CODE / _ID 結尾)不折行、不截斷,同 ui.go 的規則;
// 時長 / DURATION 是毫秒整數,轉 m:ss。
import { t } from './i18n.js';

const ATOMIC = /^(ID|CID|PID|ISRC|DEVICE|REASON_CODE)$|_ID$/;

function mmss(ms) {
  const s = Math.floor(ms / 1000); // 對齊 ui.FormatDuration 的整數除法:同一首歌在終端機與網頁要是同一個長度
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

export function renderTable(header, rows) {
  const t = document.createElement('table');
  t.className = 'tbl';
  const cols = rows.reduce((m, r) => Math.max(m, r.length), header.length); // 不用 Math.max(...spread):上萬列會 RangeError
  const thead = document.createElement('thead');
  const hr = document.createElement('tr');
  for (let i = 0; i < cols; i++) {
    const th = document.createElement('th');
    th.textContent = header[i] || '';
    hr.appendChild(th);
  }
  thead.appendChild(hr);
  t.appendChild(thead);
  const tbody = document.createElement('tbody');
  for (const r of rows) {
    const tr = document.createElement('tr');
    for (let i = 0; i < cols; i++) {
      const td = document.createElement('td');
      const h = header[i] || '';
      let v = r[i] == null ? '' : r[i];
      if (h === 'DURATION' && /^\d+$/.test(v)) { v = mmss(Number(v)); td.className = 'num'; }
      if (ATOMIC.test(h)) td.className = 'atomic';
      td.textContent = v;
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  t.appendChild(tbody);
  return t;
}

// SPOTIFY_ID:Spotify 的曲目與清單 id 是 22 碼 base62。spotify:local:…、spotify:episode:… 這類 URI 沒有頁面,空字串(釘成「沒有」)
// 也不算;Apple 的 p. / i. / 數字 id 與本機的路徑形狀不同,但呼叫端仍要先確認這一列是 Spotify 的(路徑理論上也可能湊成 22 碼)。
const SPOTIFY_ID = /^[0-9A-Za-z]{22}$/;

// spotifyLink:連回 Spotify 上的那一首或那個清單(Spotify Developer Policy II.4.b 與設計規範:顯示 Spotify 的內容就要連回去;
// 計畫 2026-09-24 §1.7 S7)。字用規範核可的「LISTEN ON SPOTIFY」,報讀名稱以它開頭、再說是哪一首(WCAG 2.5.3 Label in Name),
// 開在新分頁,同 player.js 的曲名連結。規範也要求用 Spotify 的 logo 標示來源,這一點刻意不照做:決策 48 不內嵌官方 logo。
// kind 是 'track' 或 'playlist',由呼叫端給(同一張表長得一樣也可能是清單或曲目,不從表頭猜);id 不像 Spotify 的就回 null。
export function spotifyLink(kind, id, title) {
  if (!SPOTIFY_ID.test(id)) return null; // test() 先轉字串:undefined、物件、空字串都過不了
  const a = document.createElement('a');
  a.className = 'btn btn--ghost';
  a.textContent = t('webui.spotify.listen');
  a.href = `https://open.spotify.com/${kind}/${id}`;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  a.setAttribute('aria-label', t('webui.spotify.listen_label', { title: title || id })); // 每一列的字都一樣:報讀時要聽得出是哪一首
  return a;
}

// linkColumn:在 renderTable 畫好的表最後補一欄 Spotify 連結(只在頁面上,不動命令的 TSV 與機器欄位)。
// pick(row, i) 回 { kind, id, title } 或 null。一列都連不出去就不補——Apple 或本機的表不會多出一欄空白。
export function linkColumn(tbl, rows, pick) {
  const links = rows.map((r, i) => {
    const p = pick(r, i);
    return p && spotifyLink(p.kind, p.id, p.title);
  });
  if (!links.some(Boolean)) return tbl;
  tbl.querySelector('thead tr').appendChild(document.createElement('th'));
  [...tbl.querySelectorAll('tbody tr')].forEach((tr, i) => {
    const td = document.createElement('td');
    td.className = 'row-actions';
    if (links[i]) td.appendChild(links[i]);
    tr.appendChild(td);
  });
  return tbl;
}

// byProvider:變更表(pl pull / push / sync / dedup、migrate、resolve)每一列自己帶 PROVIDER 與 PROVIDER_ID,
// PROVIDER 是 spotify 的那一列連到那一首。兩欄的位置在 pull 與 sync 的表不一樣,一律照表頭找;看 PROVIDER_ID 不看 TITLE
// (改名那一列的 TITLE 是清單名、PROVIDER_ID 是空的)。表頭沒有這兩欄就回 null。
// resolve 的 review 列:PROVIDER_ID 是還沒確認的候選、TITLE 卻是正本那一首,報讀名稱不說正本的歌名(會說成另一首),改說 id。
export function byProvider(header) {
  const [a, p, id, title] = ['ACTION', 'PROVIDER', 'PROVIDER_ID', 'TITLE'].map((k) => header.indexOf(k));
  if (p < 0 || id < 0) return null;
  return (r) => (r[p] === 'spotify' ? { kind: 'track', id: r[id], title: r[a] === 'review' ? '' : r[title] } : null);
}
