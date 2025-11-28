import { test, expect } from '@playwright/test';

test('websocket indicator and speed updates when playing', async ({ page }) => {
  await page.request.post('/api/v2/job/reset');
  await page.goto('/ui/');

  const setValue = async (selector: string, value: string) => {
    await page.evaluate(
      ([sel, val]) => {
        const el = document.querySelector<HTMLInputElement>(sel);
        if (el) {
          el.value = val;
          el.dispatchEvent(new Event('input', { bubbles: true }));
          el.dispatchEvent(new Event('change', { bubbles: true }));
        }
      },
      [selector, value],
    );
  };

  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  await setValue('#from', range.from);
  await setValue('#to', range.to);

  // Проставляем диапазон через API, чтобы play стало доступно.
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  await page.waitForTimeout(1500);
  await expect(page.locator('#playPauseBtn')).toBeEnabled({ timeout: 5_000 });

  const wsChip = page.locator('#wsSpeedChip');
  const statusBadge = page.locator('#statusBadge');

  // Запускаем, чтобы пошёл трафик.
  await page.click('#playPauseBtn');
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 8_000 });

  await page.waitForFunction(
    () => {
      const el = document.querySelector('#wsSpeedChip');
      return !!el && el.textContent !== null && !el.textContent.includes('—');
    },
    { timeout: 8_000 },
  );
  await expect(wsChip).toHaveClass(/ok/);

  await page.request.post('/api/v2/job/stop', { data: {} });
});
