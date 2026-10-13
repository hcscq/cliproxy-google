import { chromium } from 'playwright';
const b = await chromium.launch({executablePath:'/usr/bin/google-chrome'}); const p = await b.newPage({ viewport: { width: 1440, height: 800 } });
await p.goto('https://gai.vkm2.com/');
await p.evaluate((v) => { localStorage.setItem('panel.managementKey',v); sessionStorage.setItem('panel.managementKey',v); }, process.env.MK);
await p.goto('https://gai.vkm2.com/#/credentials'); await p.reload(); await p.waitForSelector('table',{timeout:20000}); await p.waitForTimeout(4000);
await p.screenshot({ path: '/workspace/panel-credentials-columns.png' }); await b.close();
