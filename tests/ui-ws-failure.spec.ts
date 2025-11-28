import { test, expect } from '@playwright/test';

test('ws indicator switches to warn on disconnect', async ({ page, context }) => {
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

  // Устанавливаем диапазон через API, чтобы play стало доступно.
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  await page.waitForTimeout(1500);
  await expect(page.locator('#playPauseBtn')).toBeEnabled({ timeout: 5_000 });

  const wsChip = page.locator('#wsSpeedChip');
  await page.click('#playPauseBtn');
  await expect(wsChip).toHaveClass(/ok/, { timeout: 8_000 });

  // Имитация обрыва сети/WS.
  await context.setOffline(true);
  await page.waitForTimeout(2000);
  await expect(wsChip).toContainText('ws —', { timeout: 10_000 });
  await context.setOffline(false);

  await page.request.post('/api/v2/job/stop', { data: {} });
});
