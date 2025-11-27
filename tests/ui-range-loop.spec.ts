import { test, expect } from '@playwright/test';

test('range → play/pause cycles → stop', async ({ page }) => {
  await page.goto('/ui/');

  const statusBadge = page.locator('#statusBadge');

  // Получаем доступный диапазон и применяем его через API.
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

  // Стартуем.
  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);

  // Pause → Resume → Pause → Resume.
  await page.request.post('/api/v2/job/pause', { data: {} });
  await waitStatus(/paused|stopping|done/i);

  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);

  await page.request.post('/api/v2/job/pause', { data: {} });
  await waitStatus(/paused|stopping|done/i);

  await page.request.post('/api/v2/job/start', { data: {} });
  await waitStatus(/running|pending|stopping|paused/i);

  // Stop.
  await page.request.post('/api/v2/job/stop', { data: {} });
  await waitStatus(/paused|idle|done|stopping/i);
});
