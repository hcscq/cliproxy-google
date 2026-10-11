import { expect, test, type Page } from '@playwright/test';

// Smoke test against a running Home (SQLite locally/CI, or the real deployment).
//   PANEL_URL       base URL of Home (default http://127.0.0.1:18327)
//   MANAGEMENT_KEY  management key
//   E2E_LOCAL=1     also exercise destructive/negative paths that are only safe on a scratch Home
//   SCREENSHOT_DIR  if set, save full-page screenshots there
const KEY = process.env.MANAGEMENT_KEY || 'testkey';
const LOCAL = process.env.E2E_LOCAL === '1';
const SHOTS = process.env.SCREENSHOT_DIR;

async function shot(page: Page, name: string) {
  if (!SHOTS) return;
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
}

async function login(page: Page) {
  await page.goto('/');
  await page.getByTestId('login-key').fill(KEY);
  await page.getByTestId('login-submit').click();
  await expect(page.getByTestId('nav-dashboard')).toBeVisible();
}

test.describe.configure({ mode: 'serial' });

test('login page renders and rejects a wrong key', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('login-key')).toBeVisible();
  await shot(page, '01-login');
  test.skip(!LOCAL, 'wrong-key attempt only on scratch Home (counts toward the IP ban)');
  await page.getByTestId('login-key').fill('definitely-wrong');
  await page.getByTestId('login-submit').click();
  await expect(page.locator('.alert.err')).toBeVisible();
});

test('dashboard, nodes, logs and settings load', async ({ page }) => {
  await login(page);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/总览|Overview/);
  await expect(page.getByText(/Home 副本|Home replicas/).first()).toBeVisible();
  await shot(page, '02-dashboard');
  await page.getByTestId('nav-nodes').click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/节点|Nodes/);
  await shot(page, '08-nodes');
  await page.getByTestId('nav-logs').click();
  await expect(page.getByTestId('log-view')).toBeVisible();
  await page.getByTestId('nav-settings').click();
  await expect(page.getByText('routing.strategy')).toBeVisible();
  await expect(page.getByText('routing.sticky-sessions.enabled')).toBeVisible();
  await expect(page.getByText('routing.sticky-sessions.key-sources')).toBeVisible();
  await shot(page, '09-settings');
  await page.getByTestId('nav-usage').click();
  await expect(page.getByRole('heading', { name: /请求记录|Request log/ })).toBeVisible();
  await expect(page.getByTestId('cache-stats')).toContainText(/粘性命中率|Sticky hit rate/);
  await expect(page.getByTestId('cache-stats')).toContainText(/缓存命中率|Cache hit rate/);
  await shot(page, '07-usage');
});

test('credentials page and Antigravity OAuth start', async ({ page }) => {
  await login(page);
  await page.getByTestId('nav-credentials').click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/凭据|Credentials/);
  if (process.env.EXPECT_CREDENTIAL) {
    await expect(page.getByTestId('credentials-table')).toContainText(process.env.EXPECT_CREDENTIAL);
  }
  await shot(page, '03-credentials');
  await page.getByTestId('add-credential').click();
  await page.getByTestId('oauth-start').click();
  const url = page.getByTestId('oauth-url');
  await expect(url).toHaveValue(/^https:\/\/accounts\.google\.com\/o\/oauth2\/v2\/auth\?.*state=/);
  await shot(page, '04-oauth-start');
  // Cancel the pending session so nothing is left behind.
  await page.getByRole('button', { name: /取消本次登录|Cancel this login/ }).click();
});

test('models list', async ({ page }) => {
  await login(page);
  await page.getByTestId('nav-models').click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/模型|Models/);
  // Default tab: model health view (empty on a fresh Home is fine).
  await expect(page.getByTestId('model-health')).toBeVisible();
  await page.getByTestId('models-tab-available').click();
  if (process.env.EXPECT_MODEL) await expect(page.getByTestId('models-table')).toContainText(process.env.EXPECT_MODEL);
  await shot(page, '05-models');
  await page.getByTestId('models-tab-excluded').click();
  await expect(page.getByTestId('excluded-antigravity')).toBeVisible();
});

test('create and delete an access key', async ({ page }) => {
  await login(page);
  await page.getByTestId('nav-accessKeys').click();
  await page.getByTestId('create-access-key').click();
  const name = `e2e-${Date.now()}`;
  await page.getByTestId('access-key-name').fill(name);
  await page.getByTestId('create-access-key-submit').click();
  const created = await page.getByTestId('created-key').textContent();
  expect(created).toMatch(/^sk-[0-9a-f]{48}$/);
  await page.getByRole('button', { name: /关闭|Close/ }).last().click();
  const row = page.locator('[data-testid^="access-key-"]', { hasText: name });
  await expect(row).toBeVisible();
  await shot(page, '06-access-keys');
  await row.locator('[data-testid^="delete-access-key-"]').click();
  await page.getByTestId('confirm-ok').click();
  await expect(row).toHaveCount(0);
});

test('credential/model groups bind to an access key and are cleaned up', async ({ page }) => {
  await login(page);
  const ts = Date.now();
  const cg = `e2e-cg-${ts}`;
  const mg = `e2e-mg-${ts}`;
  const keyName = `e2e-scope-${ts}`;

  await page.getByTestId('nav-groups').click();
  await expect(page.getByTestId('cg-warning')).toBeVisible();
  await page.getByTestId('create-credential-group').click();
  await page.getByTestId('group-name-input').fill(cg);
  await page.getByTestId('group-name-save').click();
  const cgRow = page.locator('[data-testid^="cg-"]', { hasText: cg }).first();
  await expect(cgRow).toContainText(/无成员|No members/);

  await page.getByTestId('tab-model-groups').click();
  await page.getByTestId('create-model-group').click();
  await page.getByTestId('group-name-input').fill(mg);
  await page.getByTestId('group-name-save').click();
  const mgRow = page.locator('[data-testid^="mg-"]', { hasText: mg }).first();
  await mgRow.locator('[data-testid^="mg-models-"]').click();
  await page.getByTestId('mg-tab-add').click();
  await page.getByTestId('mg-manual-input').fill('gemini-3.8-flash-tiered');
  await page.getByTestId('mg-manual-add').click();
  await page.getByTestId('mg-tab-members').click();
  await expect(page.getByTestId('mg-table')).toContainText('gemini-3.8-flash-tiered');
  await page.keyboard.press('Escape');
  await expect(mgRow).toContainText(/1 个模型|1 models/);
  await shot(page, '18-model-groups');

  await page.getByTestId('nav-accessKeys').click();
  await page.getByTestId('create-access-key').click();
  await page.getByTestId('access-key-name').fill(keyName);
  await page.getByTestId('create-access-key-submit').click();
  await page.getByRole('button', { name: /关闭|Close/ }).last().click();
  const row = page.locator('[data-testid^="access-key-"]', { hasText: keyName });
  await expect(row).toContainText(/全部凭据 · 全部模型|All credentials · all models/);
  await row.locator('[data-testid^="edit-access-key-"]').click();
  await page.getByTestId('key-credential-groups').getByText(cg).click();
  await expect(page.getByTestId('key-no-fallback-warning')).toBeVisible();
  await page.getByTestId('key-model-groups').getByText(mg).click();
  await shot(page, '18-key-scope-dialog');
  await page.getByTestId('edit-access-key-save').click();
  await expect(row).toContainText(cg);
  await expect(row).toContainText(mg);

  // deleting the groups unbinds the key (no silent lock-out)
  await page.getByTestId('nav-groups').click();
  await page.locator('[data-testid^="cg-"]', { hasText: cg }).first().locator('[data-testid^="cg-delete-"]').click();
  await expect(page.getByText(/有 1 个访问密钥绑定了该分组|1 keys are bound/).first()).toBeVisible();
  await page.getByTestId('confirm-ok').click();
  await expect(page.locator('[data-testid^="cg-"]', { hasText: cg })).toHaveCount(0);
  await page.getByTestId('tab-model-groups').click();
  await page.locator('[data-testid^="mg-"]', { hasText: mg }).first().locator('[data-testid^="mg-delete-"]').click();
  await page.getByTestId('confirm-ok').click();
  await expect(page.locator('[data-testid^="mg-"]', { hasText: mg })).toHaveCount(0);

  await page.getByTestId('nav-accessKeys').click();
  await expect(row).toContainText(/全部凭据 · 全部模型|All credentials · all models/);
  await row.locator('[data-testid^="delete-access-key-"]').click();
  await page.getByTestId('confirm-ok').click();
  await expect(row).toHaveCount(0);
});

test('model catalog panel shows refresh controls', async ({ page }) => {
  await login(page);
  await page.getByTestId('nav-models').click();
  await expect(page.getByTestId('catalog-panel')).toBeVisible();
  await expect(page.getByTestId('catalog-refresh-all')).toBeVisible();
});

test('bulk key binding, group rule preview and membership import', async ({ page }) => {
  await login(page);
  const ts = Date.now();
  const cg = `e2e-bulk-cg-${ts}`;
  const imp = `e2e-imp-${ts}`;
  await page.getByTestId('nav-groups').click();
  await page.getByTestId('create-credential-group').click();
  await page.getByTestId('group-name-input').fill(cg);
  await page.getByTestId('group-name-save').click();
  const cgRow = page.locator('[data-testid^="cg-"]', { hasText: cg }).first();
  await cgRow.locator('[data-testid^="cg-members-"]').click();
  await page.getByTestId('open-rule').click();
  await page.getByTestId('rule-email').fill('*@no-such-domain.invalid');
  await expect(page.getByTestId('rule-preview')).toContainText(/匹配 0 个凭据|0 credentials match/);
  await page.getByTestId('rule-save').click();
  await expect(page.getByTestId('open-rule')).toContainText(/编辑规则|Edit rule/);
  await page.getByTestId('open-picker').click();
  await expect(page.getByTestId('cred-filter-bar')).toBeVisible();
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  await expect(cgRow).toContainText(/规则|rule/);

  // import (dry run first, then apply) creates a model group by name
  await page.getByTestId('open-import-export').click();
  await page.getByTestId('import-data').fill(JSON.stringify({ model_groups: [{ name: imp, enabled: true, models: [{ model: 'gemini-3-flash', credential_groups: [cg] }] }] }));
  await page.getByTestId('import-dry').click();
  await expect(page.getByTestId('import-report')).toContainText(/预检|Dry run/);
  await page.getByTestId('import-apply').click();
  await expect(page.getByTestId('import-report')).toContainText(/已导入|Imported/);
  await page.keyboard.press('Escape');
  await page.getByTestId('tab-model-groups').click();
  await expect(page.locator('[data-testid^="mg-"]', { hasText: imp }).first()).toContainText(/1 个模型|1 models/);

  // two keys -> bulk bind to the credential group
  await page.getByTestId('nav-accessKeys').click();
  for (const n of ['a', 'b']) {
    await page.getByTestId('create-access-key').click();
    await page.getByTestId('access-key-name').fill(`e2e-bulk-${ts}-${n}`);
    await page.getByTestId('create-access-key-submit').click();
    await page.getByRole('button', { name: /关闭|Close/ }).last().click();
  }
  await page.getByTestId('key-q').fill(`e2e-bulk-${ts}`);
  await expect(page.locator('[data-testid^="access-key-"]', { hasText: `e2e-bulk-${ts}` })).toHaveCount(2);
  await page.getByTestId('select-all-filtered').click();
  await page.getByTestId('bulk-bind-keys').click();
  await page.getByRole('dialog').locator('.field').first().getByText(cg).click();
  await page.getByTestId('bulk-keys-apply').click();
  await expect(page.locator('[data-testid^="access-key-"]', { hasText: `e2e-bulk-${ts}` }).first()).toContainText(cg);

  // cleanup: deleting the group unbinds both keys
  await page.getByTestId('nav-groups').click();
  await page.locator('[data-testid^="cg-"]', { hasText: cg }).first().locator('[data-testid^="cg-delete-"]').click();
  await page.getByTestId('confirm-ok').click();
  await expect(page.locator('[data-testid^="cg-"]', { hasText: cg })).toHaveCount(0);
  await page.getByTestId('tab-model-groups').click();
  await page.locator('[data-testid^="mg-"]', { hasText: imp }).first().locator('[data-testid^="mg-delete-"]').click();
  await page.getByTestId('confirm-ok').click();
  await expect(page.locator('[data-testid^="mg-"]', { hasText: imp })).toHaveCount(0);
  await page.getByTestId('nav-accessKeys').click();
  await page.getByTestId('key-q').fill(`e2e-bulk-${ts}`);
  for (let i = 0; i < 2; i++) {
    const row = page.locator('[data-testid^="access-key-"]', { hasText: `e2e-bulk-${ts}` }).first();
    await expect(row).toContainText(/全部凭据 · 全部模型|All credentials · all models/);
    await row.locator('[data-testid^="delete-access-key-"]').click();
    await page.getByTestId('confirm-ok').click();
  }
  await expect(page.locator('[data-testid^="access-key-"]', { hasText: `e2e-bulk-${ts}` })).toHaveCount(0);
});
