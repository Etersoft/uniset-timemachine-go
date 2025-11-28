import { test, expect } from '@playwright/test';

test('indicators reflect running state', async ({ page }) => {
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

  // Подтягиваем доступный диапазон и применяем его в скрытых полях.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  await setValue('#from', range.from);
  await setValue('#to', range.to);

  const statusBadge = page.locator('#statusBadge');
  const chipStatus = page.locator('#chipStatus');
  const statStep = page.locator('#statStep');
  const statTs = page.locator('#statTs');
  const statUpdates = page.locator('#statUpdates');
  const pollingChip = page.locator('#pollingChip');
  const wsChip = page.locator('#wsSpeedChip');

  // Подготовка диапазона через API, чтобы play стало доступно.
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  await page.waitForTimeout(1500);
  await expect(page.locator('#playPauseBtn')).toBeEnabled({ timeout: 5_000 });

  await page.click('#playPauseBtn');
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 8_000 });

  await expect(chipStatus).not.toBeEmpty();
  await expect(statStep).not.toHaveText('-');
  await expect(statTs).not.toHaveText('-');
  await expect(statUpdates).not.toHaveText('-');
  await expect(pollingChip).toContainText('polling');

  // Стоп и проверка, что статус сбросился в допустимое значение.
  await page.request.post('/api/v2/job/stop', { data: {} });
  await expect(statusBadge).toHaveText(/paused|idle|done|stopping|running/i, { timeout: 10_000 });

  // WS индикатор обновлён.
  const wsText = await wsChip.textContent();
  expect(wsText || '').toMatch(/ws /);
});
