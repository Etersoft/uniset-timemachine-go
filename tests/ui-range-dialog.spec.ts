import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('range dialog elements and apply flow', async ({ page }) => {
  await gotoWithSession(page);

  const dlg = page.locator('#rangeDialog');
  const dlgFrom = page.locator('#rangeDialogFrom');
  const dlgTo = page.locator('#rangeDialogTo');
  const labelValue = page.locator('#rangeLabelValue');

  await page.locator('#rangePickerBtn').click();
  await expect(dlg).toBeVisible();

  // Быстрые пресеты
  const quickBtns = dlg.locator('[data-quick-min]');
  await expect(quickBtns).toHaveCount(5);
  await expect(quickBtns.nth(0)).toHaveText('5м');
  await expect(quickBtns.nth(4)).toHaveText('3ч');

  // Пресет 5 минут заполняет поля
  await dlg.locator('[data-quick-min="5"]').click();
  const quickFrom = await dlgFrom.inputValue();
  const quickTo = await dlgTo.inputValue();
  expect(quickFrom).not.toEqual('');
  expect(quickTo).not.toEqual('');
  expect(new Date(quickTo).getTime()).toBeGreaterThan(new Date(quickFrom).getTime());

  // Кастомный диапазон
  const customFrom = '2024-01-01T00:00';
  const customTo = '2024-01-01T00:05';
  await dlgFrom.fill(customFrom);
  await dlgTo.fill(customTo);

  const applyBtn = dlg.getByRole('button', { name: 'Применить' });
  await applyBtn.click();
  await expect(dlg).toBeHidden();

  // Проверяем, что скрытые поля и метка обновились
  // Input is now interpreted as UTC directly (not local time)
  const appliedFrom = await page.locator('#from').inputValue();
  const appliedTo = await page.locator('#to').inputValue();
  // customFrom = '2024-01-01T00:00' should become '2024-01-01T00:00:00Z' (UTC, not local)
  expect(appliedFrom).toBe('2024-01-01T00:00:00Z');
  expect(appliedTo).toBe('2024-01-01T00:05:00Z');
  await expect(labelValue).not.toHaveText(/не задан/i);
});
