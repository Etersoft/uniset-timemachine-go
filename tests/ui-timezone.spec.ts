import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test.describe('UTC timezone handling', () => {
  test('range dialog labels show UTC indication', async ({ page }) => {
    await gotoWithSession(page);

    const dlg = page.locator('#rangeDialog');
    await page.locator('#rangePickerBtn').click();
    await expect(dlg).toBeVisible();

    // Check that labels indicate UTC
    const fromLabel = dlg.locator('label[for="rangeDialogFrom"]');
    const toLabel = dlg.locator('label[for="rangeDialogTo"]');
    await expect(fromLabel).toContainText('UTC');
    await expect(toLabel).toContainText('UTC');
  });

  test('datetime input interpreted as UTC, not local time', async ({ page }) => {
    await gotoWithSession(page);

    const dlg = page.locator('#rangeDialog');
    const dlgFrom = page.locator('#rangeDialogFrom');
    const dlgTo = page.locator('#rangeDialogTo');

    await page.locator('#rangePickerBtn').click();
    await expect(dlg).toBeVisible();

    // Enter time values that should be interpreted as UTC
    // datetime-local format: YYYY-MM-DDTHH:MM (without seconds for Playwright compatibility)
    const inputFrom = '2024-06-01T10:00';
    const inputTo = '2024-06-01T12:00';
    await dlgFrom.fill(inputFrom);
    await dlgTo.fill(inputTo);

    const applyBtn = dlg.getByRole('button', { name: 'Применить' });
    await applyBtn.click();
    await expect(dlg).toBeHidden();

    // Check the hidden fields contain exactly the UTC time entered (with Z suffix)
    // NOT converted from local timezone
    const appliedFrom = await page.locator('#from').inputValue();
    const appliedTo = await page.locator('#to').inputValue();

    // The entered time should be preserved as-is with Z suffix (UTC)
    // NOT shifted by local timezone offset
    expect(appliedFrom).toBe('2024-06-01T10:00:00Z');
    expect(appliedTo).toBe('2024-06-01T12:00:00Z');
  });

  test('timeline labels show UTC indication', async ({ page }) => {
    await gotoWithSession(page);

    // Check that timeline labels indicate UTC
    const fromLabel = page.locator('#fromLabel');
    const toLabel = page.locator('#toLabel');

    await expect(fromLabel).toContainText('UTC');
    await expect(toLabel).toContainText('UTC');
  });

  test('quick range presets produce UTC timestamps', async ({ page }) => {
    await gotoWithSession(page);

    const dlg = page.locator('#rangeDialog');
    const dlgFrom = page.locator('#rangeDialogFrom');
    const dlgTo = page.locator('#rangeDialogTo');

    await page.locator('#rangePickerBtn').click();
    await expect(dlg).toBeVisible();

    // Click 5 minute preset
    await dlg.locator('[data-quick-min="5"]').click();

    // Get the filled values
    const filledFrom = await dlgFrom.inputValue();
    const filledTo = await dlgTo.inputValue();

    // Values should be present
    expect(filledFrom).not.toBe('');
    expect(filledTo).not.toBe('');

    // Apply and check that hidden fields have Z suffix
    const applyBtn = dlg.getByRole('button', { name: 'Применить' });
    await applyBtn.click();
    await expect(dlg).toBeHidden();

    const appliedFrom = await page.locator('#from').inputValue();
    const appliedTo = await page.locator('#to').inputValue();

    // Both should end with Z (UTC)
    expect(appliedFrom).toMatch(/Z$/);
    expect(appliedTo).toMatch(/Z$/);

    // Verify they are valid ISO timestamps
    expect(() => new Date(appliedFrom)).not.toThrow();
    expect(() => new Date(appliedTo)).not.toThrow();
  });
});
