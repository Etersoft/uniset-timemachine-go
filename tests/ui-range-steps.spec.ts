import { test, expect } from '@playwright/test';

// Range -> FWD -> FWD -> Play -> Pause -> Backward -> Backward -> Play -> Stop
// -> To Begin -> Play -> Stop -> To End -> Bwd -> Bwd -> Play -> Stop (or finish)
test('range → step fwd/bwd and play/stop flow', async ({ page }) => {
  await page.goto('/ui/');

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

  const waitStatus = async (re: RegExp, timeout = 15_000) => {
    await expect(statusBadge).toHaveText(re, { timeout });
  };

  // Шаг вперёд x2 (apply=true).
  await page.request.post('/api/v2/job/step/forward', { data: { apply: true } });
  await page.request.post('/api/v2/job/step/forward', { data: { apply: true } });

  // Play.
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);

  // Pause.
  await page.request.post('/api/v2/job/pause', { data: {} });
  await waitStatus(/paused|stopping|done/i);

  // Backward x2.
  await page.request.post('/api/v2/job/step/backward', { data: { apply: true } });
  await page.request.post('/api/v2/job/step/backward', { data: { apply: true } });

  // Play → Stop.
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);
  await page.request.post('/api/v2/job/stop', { data: {} });
  await waitStatus(/paused|idle|done|stopping/i);

  // To Begin → Play → Stop.
  await page.request.post('/api/v2/job/seek', { data: { ts: range.from, apply: true } });
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);
  await page.request.post('/api/v2/job/stop', { data: {} });
  await waitStatus(/paused|idle|done|stopping/i);

  // To End.
  await page.request.post('/api/v2/job/seek', { data: { ts: range.to, apply: true } });

  // Bwd x2.
  await page.request.post('/api/v2/job/step/backward', { data: { apply: true } });
  await page.request.post('/api/v2/job/step/backward', { data: { apply: true } });

  // Play → Stop (or finish).
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);
  await page.request.post('/api/v2/job/stop', { data: {} });
  await waitStatus(/paused|idle|done|stopping/i);
});
