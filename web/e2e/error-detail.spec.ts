import { expect, test } from '@playwright/test';

// Visual/overflow regression: a failed request with a long upstream error body must be
// fully readable in the detail drawer (scrollable drawer, wrapped summary, full JSON).
const KEY = process.env.MANAGEMENT_KEY || 'testkey';

const details = Array.from({ length: 40 }, (_, i) => ({ '@type': 'type.googleapis.com/google.rpc.ErrorInfo', reason: i === 0 ? 'MODEL_CAPACITY_EXHAUSTED' : `DETAIL_${i}`, metadata: { model: 'gemini-3.1-pro-low', index: String(i) } }));
const body = JSON.stringify({ error: { code: 503, message: 'No capacity available for model gemini-3.1-pro-low on the server — ' + 'very long explanation '.repeat(30) + 'END-OF-MESSAGE', status: 'UNAVAILABLE', details: [...details, { reason: 'LAST_DETAIL_MARKER' }] } });
const event = {
  id: 'evt_e2e',
  request_id: '01a1255b-2f11-7a32-b1d1-1f48e0bc11e5',
  timestamp: new Date().toISOString(),
  failed: true,
  status: 'failed',
  status_code: 503,
  model: 'gemini-3.1-pro-low',
  endpoint: 'POST /v1/chat/completions',
  provider: 'antigravity',
  error: { message: body.slice(0, 120), reason: null, status_code: 503, upstream_status_code: 503, body_preview: body },
  tokens: { total_tokens: 0 },
  performance: { latency_ms: 289 },
  related: { request_log: { available: true, request_id: '01a1255b-2f11-7a32-b1d1-1f48e0bc11e5', home_ip: '127.0.0.1', home_port: 1 } },
};

test('long upstream error is fully visible in the request drawer', async ({ page }) => {
  await page.route('**/v8/management/request-events?*', (r) => r.fulfill({ json: { items: [event], total: 1, limit: 25, offset: 0 } }));
  await page.route('**/v8/management/request-events/evt_e2e*', (r) => r.fulfill({ json: { event, log_excerpt: [] } }));
  await page.goto('/');
  await page.getByTestId('login-key').fill(KEY);
  await page.getByTestId('login-submit').click();
  await page.getByTestId('nav-usage').click();
  await page.getByTestId('event-row').first().click();

  const drawer = page.getByTestId('drawer');
  await expect(drawer).toBeVisible();
  // Wide by default on desktop.
  expect((await drawer.boundingBox())!.width).toBeGreaterThanOrEqual(700);
  // Summary is parsed from the body and wraps instead of being cut off.
  const msg = page.getByTestId('error-message');
  await expect(msg).toContainText('END-OF-MESSAGE');
  expect(await msg.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
  // The drawer body scrolls to the end of the content.
  const bodyEl = page.getByTestId('drawer-body');
  expect(await bodyEl.evaluate((el) => getComputedStyle(el).overflowY)).toBe('auto');
  // The JSON block contains everything (own scroll) and the last detail is reachable.
  const block = page.getByTestId('error-body');
  await expect(block).toContainText('LAST_DETAIL_MARKER');
  await block.evaluate((el) => (el.scrollTop = el.scrollHeight));
  await bodyEl.evaluate((el) => (el.scrollTop = el.scrollHeight));
  const box = (await block.boundingBox())!;
  expect(box.y + box.height).toBeLessThanOrEqual(page.viewportSize()!.height + 1);
  expect(await block.evaluate((el) => el.scrollTop + el.clientHeight >= el.scrollHeight - 2)).toBe(true);
  // Full view.
  await page.getByTestId('error-body-expand').click();
  await expect(page.getByTestId('error-body-full')).toContainText('LAST_DETAIL_MARKER');
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('error-body-full')).toHaveCount(0);
  await expect(drawer).toBeVisible();
});

test('drawer is full screen on mobile', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await page.route('**/v8/management/request-events?*', (r) => r.fulfill({ json: { items: [event], total: 1, limit: 25, offset: 0 } }));
  await page.route('**/v8/management/request-events/evt_e2e*', (r) => r.fulfill({ json: { event, log_excerpt: [] } }));
  await page.goto('/#/usage');
  if (await page.getByTestId('login-key').isVisible()) {
    await page.getByTestId('login-key').fill(KEY);
    await page.getByTestId('login-submit').click();
  }
  await page.getByTestId('event-row').first().click();
  expect((await page.getByTestId('drawer').boundingBox())!.width).toBeGreaterThanOrEqual(389);
});
