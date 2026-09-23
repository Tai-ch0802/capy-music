// doctor.js:#/doctor —— 輸出原樣 <pre>(✅ / ❌ / 🎉 是 CLI 自己印的,不解析、不換字符;純文字契約不為 web 動)。
import { el, btn, select, field, providerOptions, emptyState, pageHead } from './common.js';
import { t } from '../i18n.js';

export function initDoctor(root, api, con, notice, providers) {
  pageHead(root, t('webui.doctor.title'), t('webui.doctor.lead'));
  // 預設平台的值是空字串:不拿顯示文字當值(計畫 §2.4)。
  const prov = select([{ value: '', label: t('webui.doctor.default_provider') }, ...providerOptions(providers.list)]);
  const bar = el('div', 'form-row');
  bar.appendChild(field(t('webui.doctor.provider'), prov));
  const out = el('pre', 'doctor__out');
  const hint = el('span', 'page__note', t('webui.doctor.hint'));

  const idle = emptyState(t('webui.doctor.idle')); // 宣告在 go() 之前:go() 會用到它,別靠「只在 click 時才呼叫」躲過 TDZ
  const go = () => {
    out.textContent = '';
    out.dataset.running = t('webui.doctor.running'); // CSS 的 ::after 用 attr(data-running) 畫這句(CSS 叫不到 t())
    const arg = prov.value ? ` --provider ${prov.value}` : '';
    idle.hidden = true;
    con.run('doctor' + arg, {
      onStdout: (s) => { out.textContent += s; },
      onExit: (code, msg) => { // refused(-1)也會走到這裡,否則「執行中…」會一直掛著
        delete out.dataset.running;
        if (code !== 0 && !out.textContent) out.textContent = msg || '';
      },
    }, { label: t('webui.doctor.checking') });
  };
  bar.append(btn(t('webui.doctor.go'), 'btn--primary', go));
  root.append(bar, hint, out);
  // 不在切到這一頁時自動跑:doctor 在 SYSTEM_DIALOG 名單裡,會彈 keychain / Music.app 對話框,
  // 由導覽動作觸發不對;而且它會佔住伺服器唯一的序列槽(review #62)。
  root.appendChild(idle);
}
