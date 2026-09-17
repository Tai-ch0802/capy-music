// table.js:ui.TableWriter 送來的整張表。原子欄(ID / CID / PID / ISRC / DEVICE / _ID 結尾)不折行、不截斷,同 ui.go 的規則;
// 時長 / DURATION 是毫秒整數,轉 m:ss。
const ATOMIC = /^(ID|CID|PID|ISRC|DEVICE)$|_ID$/;

function mmss(ms) {
  const s = Math.round(ms / 1000);
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
      if ((h === '時長' || h === 'DURATION') && /^\d+$/.test(v)) { v = mmss(Number(v)); td.className = 'num'; }
      if (ATOMIC.test(h)) td.className = 'atomic';
      td.textContent = v;
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  t.appendChild(tbody);
  return t;
}
