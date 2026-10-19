import { test } from '@playwright/test';
// Manual screenshot run against the local mock Home (PANEL_SHOTS=1); skipped in CI.
test.skip(!process.env.PANEL_SHOTS, 'set PANEL_SHOTS=1 to capture panel screenshots');
if (process.env.CHROME_PATH) test.use({ launchOptions: { executablePath: process.env.CHROME_PATH } });
const PREFIX = process.env.SHOT_PREFIX || 'panel-before';
for (const w of [1440, 1280, 390]) {
  test(`credentials ${w}`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: 900 });
    await page.goto('/');
    await page.getByTestId('login-key').fill('testkey');
    await page.getByTestId('login-submit').click();
    await page.goto('/#/credentials');
    await page.getByTestId('credentials-table').waitFor();
    await page.waitForTimeout(800);
    await page.screenshot({ path: `/workspace/${PREFIX}-${w}.png`, fullPage: false });
    if (PREFIX === 'panel-after' && w === 1280) {
      await page.getByTestId('row-menu').first().click(); await page.locator('.row-menu').waitFor();
      await page.screenshot({ path: `/workspace/${PREFIX}-menu-${w}.png` });
      await page.keyboard.press('Escape'); await page.mouse.click(5, 5);
    }
    for (const [n, h] of [['dashboard', '/#/'], ['settings', '/#/settings']]) {
      await page.goto(h); await page.waitForTimeout(800);
      await page.screenshot({ path: `/workspace/${PREFIX}-${n}-${w}.png` });
    }
  });
}
