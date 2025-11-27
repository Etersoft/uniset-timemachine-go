import { test, expect } from '@playwright/test';

test('range → play → stop', async ({ page }) => {
  await page.goto('/ui/');

  // Ждём готовности UI
  await expect(page.locator('#rangeBtn')).toBeVisible();

  // Установить доступный диапазон
  await page.locator('#rangeBtn').click();
  await page.getByText('Диапазон установлен').first().waitFor({ timeout: 5_000 });

  // Нажать Play
  await page.getByRole('button', { name: 'Play' }).click();
  await expect(page.getByText('status')).toHaveText(/running|paused|pending/i, { timeout: 10_000 });

  // Стоп
  await page.getByRole('button', { name: 'Stop' }).click();
  await expect(page.getByText('status')).toHaveText(/paused|idle|done/i, { timeout: 10_000 });
});
