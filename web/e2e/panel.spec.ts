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
  await shot(page, '09-settings');
  await page.getByTestId('nav-usage').click();
  await expect(page.getByRole('heading', { name: /请求记录|Request log/ })).toBeVisible();
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
