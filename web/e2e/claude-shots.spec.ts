import { test } from '@playwright/test';
// Manual screenshot run against the local mock Home (PANEL_SHOTS=1); skipped in CI.
test.skip(!process.env.PANEL_SHOTS, 'set PANEL_SHOTS=1 to capture panel screenshots');
if (process.env.CHROME_PATH) test.use({ launchOptions: { executablePath: process.env.CHROME_PATH } });
const PREFIX = process.env.SHOT_PREFIX || 'panel-claude-before';
test('claude quota', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/');
  await page.getByTestId('login-key').fill('testkey');
  await page.getByTestId('login-submit').click();
  await page.goto('/#/credentials');
  await page.getByTestId('credentials-table').waitFor();
  await page.waitForTimeout(1200);
  await page.screenshot({ path: `/workspace/${PREFIX}.png` });
  const t = page.getByTestId('col-toggle');
  if (process.env.SHOT_MENU && await t.count()) {
    await t.click();
    await page.screenshot({ path: `/workspace/${PREFIX}-menu.png` });
    await page.getByTestId('toggle-claude-quota').click();
    await page.mouse.click(5, 5);
    await page.waitForTimeout(300);
    await page.screenshot({ path: `/workspace/${PREFIX}-hidden.png` });
    await page.getByTestId('col-toggle').click();
    await page.getByTestId('toggle-claude-quota').click();
  }
});
