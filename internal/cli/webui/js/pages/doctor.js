// doctor.js:#/doctor —— 輸出原樣 <pre>(✅ / ❌ / 🎉 是 CLI 自己印的,不解析、不換字符;純文字契約不為 web 動)。
import { el, btn, select, field, providerOptions, emptyState, pageHead } from './common.js';

export function initDoctor(root, api, con, notice, providers) {
  pageHead(root, '診斷', '有東西不對勁時,從這裡開始:設定、登入、連線各自哪裡有問題,每一項都會告訴你下一步。');
  const prov = select([{ value: '(預設)', label: '預設平台' }, ...providerOptions(providers.list)], '(預設)');
  const bar = el('div', 'form-row');
  bar.appendChild(field('平台', prov));
  const out = el('pre', 'doctor__out');
  const hint = el('span', 'page__note', '檢查時,這台電腦可能會跳出系統對話框(鑰匙圈 / Music.app)。');

  const idle = emptyState('還沒檢查過。按「開始檢查」。'); // 宣告在 go() 之前:go() 會用到它,別靠「只在 click 時才呼叫」躲過 TDZ
  const go = () => {
    out.textContent = '';
    out.dataset.running = '';
    const arg = prov.value === '(預設)' ? '' : ` --provider ${prov.value}`;
    idle.hidden = true;
    con.run('doctor' + arg, {
      onStdout: (t) => { out.textContent += t; },
      onExit: (code, msg) => { // refused(-1)也會走到這裡,否則「執行中…」會一直掛著
        delete out.dataset.running;
        if (code !== 0 && !out.textContent) out.textContent = msg || '';
      },
    }, { label: '檢查設定、登入與連線' });
  };
  bar.append(btn('開始檢查', 'btn--primary', go));
  root.append(bar, hint, out);
  // 不在切到這一頁時自動跑:doctor 在 SYSTEM_DIALOG 名單裡,會彈 keychain / Music.app 對話框,
  // 由導覽動作觸發不對;而且它會佔住伺服器唯一的序列槽(review #62)。
  root.appendChild(idle);
}
