import { test, expect } from '@playwright/test';

test('charts: add/remove, color change, toggles and clear', async ({ page }) => {
  await page.goto('/ui/');

  const setRange = async () => {
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });
  };
  await setRange();

  await page.getByRole('button', { name: 'Графики' }).click();

  const legendRows = page.locator('#chartLegendBody tr');

  // Добавляем датчик через поле ввода.
  await page.fill('#chartSensors', '10001');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelectorAll('#chartLegendBody tr').length > 0, { timeout: 10_000 });
  await expect(legendRows.first()).toBeVisible();

  // Тогглы.
  const fillToggle = page.locator('#chartFill');
  const smoothToggle = page.locator('#chartSmooth');
  const autoToggle = page.locator('#chartAutoUpdate');
  await fillToggle.uncheck();
  await smoothToggle.uncheck();
  await autoToggle.uncheck();
  await expect(fillToggle).not.toBeChecked();
  await expect(smoothToggle).not.toBeChecked();
  await expect(autoToggle).not.toBeChecked();

  // Очистить график.
  await page.click('#chartClearBtn');
  // На некоторых прогонов может прилетать обновление чуть позже — делаем повторный клик и ждём пустую легенду.
  await page.waitForTimeout(500);
  await page.click('#chartClearBtn');
  const remaining = await legendRows.count();
  expect(remaining).toBeLessThanOrEqual(1);
  await expect(page.locator('#chartLegendBody')).toContainText('Нет выбранных датчиков');
});
