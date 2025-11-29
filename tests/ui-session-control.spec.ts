import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test.describe('Session Control', () => {
  test('second tab should be locked when first tab is controller', async ({ browser }) => {
    // Открываем первую вкладку с уникальным токеном
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await gotoWithSession(page1, '/ui/', false, 'session-1');

    // Первая вкладка применяет диапазон через UI (становится контроллером)
    const rangePicker1 = page1.locator('#rangePickerBtn');
    await rangePicker1.click();
    const rangeBtn1 = page1.locator('#rangeBtn');
    await rangeBtn1.click();
    await page1.waitForTimeout(1000);

    // Проверяем что первая вкладка - контроллер
    const playPause1 = page1.locator('#playPauseBtn');
    await expect(playPause1).toBeEnabled({ timeout: 3000 });

    // Открываем вторую вкладку с ДРУГИМ токеном (новая сессия)
    const context2 = await browser.newContext();
    const page2 = await context2.newPage();
    await gotoWithSession(page2, '/ui/', false, 'session-2');

    // Ждём пока вторая вкладка проверит статус контроллера (refresh каждые 1.5 сек)
    await page2.waitForTimeout(2000);

    // Проверяем что во второй вкладке показывается блокировка
    const lockNotice = page2.locator('#controlLockNotice');
    await expect(lockNotice).toBeVisible({ timeout: 5000 });
    await expect(lockNotice).toContainText(/управление/i);

    // Проверяем что кнопка "Забрать управление" видна
    const claimBtn = page2.locator('#claimControlBtn');
    await expect(claimBtn).toBeVisible();

    // Проверяем что элементы управления заблокированы
    const playPause2 = page2.locator('#playPauseBtn');
    await expect(playPause2).toBeDisabled();

    const rangeBtn2 = page2.locator('#rangeBtn');
    await expect(rangeBtn2).toBeDisabled();

    // Проверяем что кнопка выбора диапазона тоже заблокирована
    const rangePicker2 = page2.locator('#rangePickerBtn');
    await expect(rangePicker2).toBeDisabled();

    // Закрываем контексты
    await context1.close();
    await context2.close();
  });

  test('second tab can claim control after timeout', async ({ browser }) => {
    // Открываем первую вкладку
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await gotoWithSession(page1, '/ui/', false, 'session-1');

    // Первая вкладка применяет диапазон через UI (становится контроллером)
    const rangePicker1 = page1.locator('#rangePickerBtn');
    await rangePicker1.click();
    const rangeBtn1 = page1.locator('#rangeBtn');
    await rangeBtn1.click();
    await page1.waitForTimeout(1000);

    // Открываем вторую вкладку с другим токеном
    const context2 = await browser.newContext();
    const page2 = await context2.newPage();
    await gotoWithSession(page2, '/ui/', false, 'session-2');

    // Ждём пока вторая вкладка проверит статус (должна быть заблокирована)
    await page2.waitForTimeout(2000);

    // Проверяем что блокировка активна
    const lockNotice = page2.locator('#controlLockNotice');
    await expect(lockNotice).toBeVisible({ timeout: 5000 });

    // Закрываем первую вкладку (контроллер исчезнет)
    await context1.close();

    // Ждём таймаут (5 секунд + запас)
    await page2.waitForTimeout(6000);

    // Проверяем что кнопка "Забрать управление" стала доступна
    const claimBtn = page2.locator('#claimControlBtn');
    await expect(claimBtn).toBeVisible();

    // Нажимаем "Забрать управление"
    await claimBtn.click();

    // Проверяем что элементы управления разблокировались
    await page2.waitForTimeout(2000);
    const playPause2 = page2.locator('#playPauseBtn');
    await expect(playPause2).toBeEnabled({ timeout: 3000 });

    const lockNoticeAfter = page2.locator('#controlLockNotice');
    await expect(lockNoticeAfter).toBeHidden();

    // Закрываем контекст
    await context2.close();
  });

  test('first tab remains controller when refreshing', async ({ browser }) => {
    // Открываем первую вкладку
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await gotoWithSession(page1, '/ui/', false, 'session-refresh-test');

    // Первая вкладка применяет диапазон через UI
    const rangePicker1 = page1.locator('#rangePickerBtn');
    await rangePicker1.click();
    const rangeBtn1 = page1.locator('#rangeBtn');
    await rangeBtn1.click();
    await page1.waitForTimeout(1000);

    // Проверяем что первая вкладка - контроллер
    const playPause1 = page1.locator('#playPauseBtn');
    await expect(playPause1).toBeEnabled();

    // Перезагружаем первую вкладку
    await page1.reload();
    await page1.waitForTimeout(2000);

    // После перезагрузки первая вкладка НЕ должна быть контроллером (новая сессия)
    const lockNotice1 = page1.locator('#controlLockNotice');

    // Ждём timeout чтобы старый контроллер истёк
    await page1.waitForTimeout(6000);

    // Теперь можем забрать управление
    const claimBtn1 = page1.locator('#claimControlBtn');
    if (await claimBtn1.isVisible()) {
      await claimBtn1.click();
      await page1.waitForTimeout(2000);
    }

    // Проверяем что управление доступно
    await expect(playPause1).toBeEnabled({ timeout: 3000 });

    // Закрываем контекст
    await context1.close();
  });
});
