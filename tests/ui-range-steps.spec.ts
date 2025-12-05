import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

// Range -> FWD -> FWD -> Play -> Pause -> Backward -> Backward -> Play -> Stop
// -> To Begin -> Play -> Stop -> To End -> Bwd -> Bwd -> Play -> Stop (or finish)
test('range → step fwd/bwd and play/stop flow', async ({ page }) => {
  test.setTimeout(20_000);
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');

  const statusBadge = page.locator('#statusBadge');

  // Подтягиваем диапазон через API и применяем его.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  await page.request.post('/api/v2/job/range', {
    data: {
      from: range.from,
      to: range.to,
      step: '1s',
      speed: 1,
      window: '5s',
    },
  });

  const waitStatus = async (re: RegExp, timeout = 5_000) => {
    await expect(statusBadge).toHaveText(re, { timeout });
  };
  const nap = (ms = 150) => page.waitForTimeout(ms);

  // Шаг вперёд (apply=true).
  await page.request.post('/api/v2/job/step/forward', { data: { apply: true } });
  await nap();

  // Play.
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused|done/i);
  await nap();

  // Pause.
  await page.request.post('/api/v2/job/pause', { data: {} });
  await waitStatus(/paused|stopping|done/i);
  await nap();

  // Backward.
  await page.request.post('/api/v2/job/step/backward', { data: { apply: true } });
  await nap();

  // Play → Stop.
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused|done/i);
  await page.request.post('/api/v2/job/stop', { data: {} });
  await waitStatus(/paused|idle|done|stopping|running/i);
  await nap();

  // To Begin → Play → Stop.
  await page.request.post('/api/v2/job/seek', { data: { ts: range.from, apply: true } });
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused|done/i);
  await page.request.post('/api/v2/job/stop', { data: {} });
  await waitStatus(/paused|idle|done|stopping|running/i);
  await nap();
});
