// lang.js:rail 的語言選單(決策 50;計畫 §2.4)。換語言 = 走一般的命令路徑跑 `config set language <代碼>`:
// 同一個序列槽(runMu)、主控台留紀錄;exit 0 才重新載入整頁(目錄與 /api/commands 都跟著 config 換)。
// 有命令在跑時選單是停用的:它標了 data-run,Console 佔槽時把 select[data-run] 設成 disabled。
import { t, languages, currentLang } from './i18n.js';

export function languageMenu(sel, con) {
  for (const l of languages()) {
    const o = document.createElement('option');
    o.value = l.code;
    o.textContent = l.name;
    sel.appendChild(o);
  }
  sel.value = currentLang();
  sel.addEventListener('change', async () => {
    const [code] = await con.run('', {}, { args: ['config', 'set', 'language', sel.value], label: t('webui.lang.switching') });
    if (code === 0) location.reload();
    else sel.value = currentLang(); // 沒換成(被擋、失敗):選單不停在沒生效的語言上
  });
}
