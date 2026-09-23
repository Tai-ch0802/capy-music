// i18n.js:網頁的語系目錄(決策 50;計畫 docs/superpowers/plans/2026-09-23-i18n.md §2.4)。目錄由 GET /api/i18n 給:
// webui.* 的每個 key,加上 web.go 的 webSharedKeys 列的幾個共用 key;語系跟著 config 的 language。
//
// 載入順序的鐵則(Go 那邊 init 時期就 T() 的陷阱,在 JS 的樣子):ES module 的頂層在 import 的當下就執行,
// 那時 loadI18n() 還沒跑完。所以**任何模組都不可以在頂層算出使用者看得到的字**——
//   const TITLE = t('webui.x.title');          // 錯:import 時 t() 會丟例外
//   const title = () => t('webui.x.title');    // 對:用到時才查
// app.js 先 await loadI18n() 再建任何畫面;t() 在那之前被叫到就丟例外(TestWebConsoleBehaviour 在載目錄之前
// 先 import 每個模組,違反的話測試直接紅)。
//
// 用法:
//   t('webui.move.count', { count: n, name })  // 同步查;{name} 一趟替換(值裡的 {x} 不會再被換);
//                                              // 複數訊息依 count 用 Intl.PluralRules 選類別,沒有就用 other;
//                                              // 目錄沒有這個 key 就回 key 本身
//   <span data-i18n="webui.x.label"></span>    // 靜態文字:applyStatic() 填 textContent(會換掉整個內容,
//                                              // 所以放在只有文字的葉子元素上);另有 data-i18n-aria-label、
//                                              // data-i18n-placeholder、data-i18n-title 填對應的屬性
// key 一律寫字串字面、佔位符寫成 t() 的第二個參數的物件字面(i18n 的守門測試靜態檢查:key 要在 en.json、
// 佔位符名稱要跟目錄一樣)。這個檔本身不含任何使用者看得到的字。

let lang = '';
let catalog = null; // null = 還沒 loadI18n()
let loaded = false; // 最近一次 loadI18n() 真的讀到目錄
let supported = [];
let rules = null;

// loadI18n:讀目錄。失敗(連不上、token 不對)也照樣收尾:t() 之後回 key 本身,頁面至少畫得出來,
// 真正的錯誤由 app.js 的 /api/commands 說(401 印伺服器回的那一句;連不上印瀏覽器的錯誤)。
export async function loadI18n(api) {
  loaded = false;
  try {
    const r = await api.fetch('/api/i18n');
    if (!r.ok) return false;
    const d = await r.json();
    lang = d.lang || '';
    catalog = d.messages || {};
    supported = d.supported || [];
    document.documentElement.lang = lang;
    loaded = true;
    return true;
  } catch (_) {
    return false;
  } finally {
    catalog ||= {};
    try { rules = new Intl.PluralRules(lang || 'en'); } catch (_) { rules = new Intl.PluralRules('en'); }
  }
}

export function t(key, params = {}) {
  if (!catalog) throw new Error(`t('${key}') before loadI18n(): no module may compute user-facing text at import time`);
  let s = catalog[key];
  if (s == null) return key;
  if (typeof s === 'object') s = s[rules.select(Math.abs(Number(params.count)) || 0)] ?? s.other ?? key;
  return s.replace(/\{([a-z_][a-z0-9_]*)\}/g, (m, name) => (Object.hasOwn(params, name) ? String(params[name]) : m));
}

const ATTRS = { 'data-i18n-aria-label': 'aria-label', 'data-i18n-placeholder': 'placeholder', 'data-i18n-title': 'title' };

// applyStatic:把 root 底下 data-i18n* 的元素填上目前語系的字。
// 目錄沒讀到就什麼都不填:rail、底部列留空,不把 webui.rail.move 這種 key 當成字畫出來。
export function applyStatic(root = document) {
  if (!loaded) return;
  for (const el of root.querySelectorAll('[data-i18n],[data-i18n-aria-label],[data-i18n-placeholder],[data-i18n-title]')) {
    const k = el.getAttribute('data-i18n');
    if (k) el.textContent = t(k);
    for (const [from, to] of Object.entries(ATTRS)) {
      const ak = el.getAttribute(from);
      if (ak) el.setAttribute(to, t(ak));
    }
  }
}

// languages:支援的語系 [{ code, name }](name 是語系用自己的語言寫的名稱);currentLang:目前語系代碼。
export const languages = () => supported;
export const currentLang = () => lang;
