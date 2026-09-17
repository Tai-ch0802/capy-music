// doctor.js:#/doctor —— 輸出原樣 <pre>(✅ / ❌ / 🎉 是 CLI 自己印的,不解析、不換字符;純文字契約不為 web 動)。
import { el, btn, select, field, pageHead } from './common.js';

export function initDoctor(root, api, con, notice, providers) {
  pageHead(root, '診斷', 'doctor');
  const prov = select(['(預設)', ...providers.list], '(預設)');
  const bar = el('div', 'form-row');
  bar.appendChild(field('平台', prov));
  const out = el('pre', 'doctor__out');
  const hint = el('span', 'page__note', '可能在這台電腦跳出系統對話框(keychain / Music.app)');

  const go = () => {
    out.textContent = '';
    out.dataset.running = '';
    const arg = prov.value === '(預設)' ? '' : ` --provider ${prov.value}`;
    con.run('doctor' + arg, {
      onStdout: (t) => { out.textContent += t; },
      onExit: () => { delete out.dataset.running; },
    });
  };
  bar.append(btn('重新檢查', 'btn--primary', go), hint);
  root.append(bar, out);
  go();
}
