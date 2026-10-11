import { chromium } from '@playwright/test';
const b = await chromium.launch();
for (const [w,h,tag] of [[1440,900,'d'],[390,844,'m']]) {
 const p = await b.newPage({ viewport: { width: w, height: h } });
 await p.goto('https://gai.vkm2.com/'); await p.getByTestId('login-key').fill(process.env.MANAGEMENT_KEY); await p.getByTestId('login-submit').click();
 await p.waitForTimeout(1500);
 for (const n of ['credentials','usage','settings']) { await p.goto('https://gai.vkm2.com/#/'+n).catch(()=>{}); 
   const nav = p.getByTestId('nav-'+n); if (await nav.isVisible().catch(()=>false)) await nav.click(); await p.waitForTimeout(1500);
   await p.screenshot({ path: `/tmp/rv/${n}-${tag}.png`, fullPage: true }); }
 await p.close(); }
await b.close();
