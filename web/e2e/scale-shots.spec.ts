import { expect, test, type Page } from '@playwright/test';

// Large-scale screenshots against a LOCAL synthetic Home only (SCALE_ENV=1).
const SHOTS = process.env.SCREENSHOT_DIR || '/tmp';
const KEY = process.env.MANAGEMENT_KEY || 'testkey';
test.skip(process.env.SCALE_ENV !== '1', 'synthetic scale env only');
test.describe.configure({ mode: 'serial' });

async function login(page: Page) {
  await page.goto('/');
  await page.getByTestId('login-key').fill(KEY);
  await page.getByTestId('login-submit').click();
  await expect(page.getByTestId('nav-dashboard')).toBeVisible();
}
const shot = async (page: Page, name: string, full = false) => {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: full });
};

test('scale screenshots', async ({ page }) => {
  await login(page);
  await page.getByTestId('nav-credentials').click();
  await expect(page.getByTestId('credentials-table')).toBeVisible();
  await page.getByTestId('cred-filter-domain').selectOption('corp-a.example');
  await page.getByTestId('cred-filter-tag').selectOption('vip');
  await page.getByTestId('select-all-filtered').click();
  await expect(page.getByTestId('bulk-actions')).toBeVisible();
  await shot(page, '19-credentials-bulk');
  await page.getByTestId('bulk-add-group').click();
  await page.getByTestId('bulk-group').selectOption({ index: 3 });
  await shot(page, '19-credentials-bulk-group-dialog');
  await page.keyboard.press('Escape');

  await page.getByTestId('nav-groups').click();
  await expect(page.getByTestId('credential-group-table')).toBeVisible();
  await shot(page, '19-groups-summary');
  await page.locator('[data-testid^="cg-members-"]').first().click();
  await expect(page.getByTestId('member-table')).toBeVisible();
  await shot(page, '19-group-members-drawer');
  await page.getByTestId('open-picker').click();
  await page.getByTestId('cred-filter-provider').selectOption('antigravity');
  await page.getByTestId('cred-filter-status').selectOption('ok');
  await page.locator('.modal').getByTestId('select-page').check();
  await shot(page, '19-credential-picker');
  await page.keyboard.press('Escape');
  await page.getByTestId('open-rule').click();
  await expect(page.getByTestId('rule-preview')).toContainText(/匹配/);
  await shot(page, '19-group-rule');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');

  await page.getByTestId('tab-model-groups').click();
  await page.locator('[data-testid^="mg-models-"]').first().click();
  await page.getByTestId('mg-tab-add').click();
  await shot(page, '19-model-picker');
  await page.keyboard.press('Escape');

  await page.getByTestId('open-import-export').click();
  await shot(page, '19-import-export');
  await page.keyboard.press('Escape');

  await page.getByTestId('nav-accessKeys').click();
  await expect(page.getByTestId('access-keys-table')).toBeVisible();
  await page.getByTestId('key-scope').selectOption('restricted');
  await page.getByTestId('select-page').check();
  await shot(page, '19-access-keys-bulk');
  await page.getByTestId('bulk-bind-keys').click();
  await shot(page, '19-access-keys-bulk-dialog');
  await page.keyboard.press('Escape');

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/#/credentials');
  await shot(page, '19-credentials-narrow');
});
