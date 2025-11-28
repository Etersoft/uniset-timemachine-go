import { test, expect } from '@playwright/test';

test('table filter and add-to-chart from Sensors tab', async ({ page }) => {
  await page.goto('/ui/');

  const applyRange = async () => {
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });
  };
  await applyRange();

  // Переключаемся на вкладку "Датчики".
  await page.getByRole('button', { name: 'Датчики' }).click();

  const rows = page.locator('#tableBody tr');
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, { timeout: 15_000 });
  const total = await rows.count();

  // Берём имя первого датчика и фильтруем по нему.
  const firstName = await rows.first().locator('td').nth(1).textContent();
  await page.fill('#tableFilter', firstName || '');
  await page.waitForTimeout(300);
  const filteredCount = await rows.count();
  expect(filteredCount).toBeGreaterThan(0);
  expect(filteredCount).toBeLessThanOrEqual(total);

  // Сброс фильтра.
  await page.fill('#tableFilter', '');
  await page.waitForTimeout(300);
  await expect(rows).toHaveCount(total);

  // Добавляем первый датчик на график.
  await rows.first().locator('button[data-chart-add]').click();

  // Проверяем легенду графиков.
  await page.getByRole('button', { name: 'Графики' }).click();
  const legendRows = page.locator('#chartLegendBody tr');
  await page.waitForFunction(() => document.querySelectorAll('#chartLegendBody tr').length > 0, { timeout: 10_000 });
});
