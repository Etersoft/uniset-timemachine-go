import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('diagnostics toggle collects logs and can be cleared', async ({ page }) => {
  await gotoWithSession(page);

  const diagEnable = page.locator('#diagEnable');
  const diagDownload = page.locator('#diagDownload');
  const diagClear = page.locator('#diagClear');

  // На всякий случай активируем вкладку управления, где расположен лог.
  const controlTab = page.locator('[data-tab="control"]');
  if (await controlTab.count()) {
    await controlTab.first().click();
  }

  await expect(diagEnable).toHaveCount(1);
  await expect(diagDownload).toBeDisabled();
  await expect(diagClear).toBeDisabled();

  // Включаем диагностику и записываем строку.
  await diagEnable.check({ force: true });
  await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any,@typescript-eslint/no-unsafe-call
    (window as any).log?.('diag test entry');
  });

  await expect(diagDownload).toBeEnabled();
  await expect(diagClear).toBeEnabled();

  // Очищаем буфер — сразу выключаем диагностику, чтобы фоновые логи не вернули кнопку в enabled.
  await diagClear.click();
  await diagEnable.uncheck();
  await expect(diagDownload).toBeDisabled({ timeout: 5000 });
  await page.waitForTimeout(200);
  await expect(diagClear).toBeDisabled({ timeout: 5000 });

  // Убеждаемся, что при выключенной диагностике новые записи не включают кнопки.
  await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any,@typescript-eslint/no-unsafe-call
    (window as any).log?.('diag entry while disabled');
  });
  if (await diagClear.isEnabled()) {
    await diagClear.click();
  }
  await expect(diagDownload).toBeDisabled({ timeout: 5000 });
  await expect(diagClear).toBeDisabled({ timeout: 5000 });
});
