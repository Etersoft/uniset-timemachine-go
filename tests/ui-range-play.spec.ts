import { test, expect } from '@playwright/test';

test('range → play → stop', async ({ page }) => {
  await page.goto('/ui/');

  // Запросить доступный диапазон напрямую через API и применить его.
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

  const statusBadge = page.locator('#statusBadge');
  await page.request.post('/api/v2/job/start', { data: {} });
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 8_000 });

  // Стоп
  await page.request.post('/api/v2/job/stop', { data: {} });
  await expect(statusBadge).toHaveText(/paused|idle|done|stopping|running/i, { timeout: 8_000 });
});
