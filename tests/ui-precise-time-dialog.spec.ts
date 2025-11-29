import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test.describe('Precise Time Dialog', () => {
  test('double-click on timeline opens dialog with current time', async ({ page }) => {
    await gotoWithSession(page);

    const timeline = page.locator('#timeline');
    const dialog = page.locator('#preciseTimeDialog');
    const preciseDate = page.locator('#preciseDate');
    const preciseTime = page.locator('#preciseTime');
    const preciseMillis = page.locator('#preciseMillis');

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });

    // Ждём что timeline стал активен
    await expect(timeline).toBeEnabled();

    // Устанавливаем позицию timeline в середину
    await timeline.fill('500');
    await page.waitForTimeout(200);

    // Диалог должен быть скрыт
    await expect(dialog).toBeHidden();

    // Двойной клик на timeline
    await timeline.dblclick();

    // Диалог должен открыться
    await expect(dialog).toBeVisible();

    // Поля должны быть заполнены
    const dateValue = await preciseDate.inputValue();
    const timeValue = await preciseTime.inputValue();
    const millisValue = await preciseMillis.inputValue();

    expect(dateValue).toMatch(/^\d{4}-\d{2}-\d{2}$/); // YYYY-MM-DD
    expect(timeValue).toMatch(/^\d{2}:\d{2}:\d{2}$/); // HH:MM:SS
    expect(millisValue).toMatch(/^\d+$/); // число
  });

  test('cancel button closes dialog without applying changes', async ({ page }) => {
    await gotoWithSession(page);

    const timeline = page.locator('#timeline');
    const dialog = page.locator('#preciseTimeDialog');
    const preciseDate = page.locator('#preciseDate');

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });

    await expect(timeline).toBeEnabled();

    // Перехватываем HTTP запросы seek
    let seekCalled = false;
    await page.route('**/api/v2/job/seek', (route) => {
      seekCalled = true;
      route.continue();
    });

    // Открываем диалог
    await timeline.dblclick();
    await expect(dialog).toBeVisible();

    // Меняем дату на другую
    await preciseDate.fill('2024-01-01');

    // Нажимаем Отмена
    const cancelBtn = dialog.getByRole('button', { name: 'Отмена' });
    await cancelBtn.click();

    // Диалог должен закрыться
    await expect(dialog).toBeHidden();

    // Проверяем что seek НЕ был вызван
    await page.waitForTimeout(500);
    expect(seekCalled).toBe(false);
  });

  test('apply button seeks to specified time', async ({ page }) => {
    await gotoWithSession(page);

    const timeline = page.locator('#timeline');
    const dialog = page.locator('#preciseTimeDialog');
    const preciseDate = page.locator('#preciseDate');
    const preciseTime = page.locator('#preciseTime');
    const preciseMillis = page.locator('#preciseMillis');
    const currentLabel = page.locator('#currentLabel');

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    const rangeFrom = new Date(range.from);
    const rangeTo = new Date(range.to);

    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });

    await expect(timeline).toBeEnabled();

    // Открываем диалог
    await timeline.dblclick();
    await expect(dialog).toBeVisible();

    // Вычисляем время в середине диапазона
    const midTime = new Date(rangeFrom.getTime() + (rangeTo.getTime() - rangeFrom.getTime()) / 2);
    const targetDate = midTime.toISOString().split('T')[0]; // YYYY-MM-DD
    const hours = String(midTime.getUTCHours()).padStart(2, '0');
    const minutes = String(midTime.getUTCMinutes()).padStart(2, '0');
    const seconds = String(midTime.getUTCSeconds()).padStart(2, '0');
    const targetTime = `${hours}:${minutes}:${seconds}`;
    const targetMillis = String(midTime.getUTCMilliseconds());

    // Заполняем поля
    await preciseDate.fill(targetDate);
    await preciseTime.fill(targetTime);
    await preciseMillis.fill(targetMillis);

    // Нажимаем Применить
    const applyBtn = dialog.getByRole('button', { name: 'Применить' });
    await applyBtn.click();

    // Диалог должен закрыться
    await expect(dialog).toBeHidden({ timeout: 3000 });

    // Проверяем что метка времени обновилась
    await page.waitForTimeout(500);
    const newLabel = await currentLabel.textContent();
    expect(newLabel).toContain(targetDate);
    expect(newLabel).toContain(hours);
    expect(newLabel).toContain(minutes);
    expect(newLabel).toContain(seconds);
  });

  test('validation: time out of range shows error', async ({ page }) => {
    await gotoWithSession(page);

    const timeline = page.locator('#timeline');
    const dialog = page.locator('#preciseTimeDialog');
    const preciseDate = page.locator('#preciseDate');
    const preciseTime = page.locator('#preciseTime');
    const preciseMillis = page.locator('#preciseMillis');
    const errorText = page.locator('#preciseTimeError');

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();

    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });

    await expect(timeline).toBeEnabled();

    // Открываем диалог
    await timeline.dblclick();
    await expect(dialog).toBeVisible();

    // Устанавливаем дату за пределами диапазона
    await preciseDate.fill('2099-12-31');
    await preciseTime.fill('23:59:59');
    await preciseMillis.fill('999');

    // Нажимаем Применить
    const applyBtn = dialog.getByRole('button', { name: 'Применить' });
    await applyBtn.click();

    // Диалог должен остаться открытым
    await expect(dialog).toBeVisible();

    // Должно появиться сообщение об ошибке
    await expect(errorText).toContainText(/вне диапазона/i);
  });

  test('validation: empty fields show error', async ({ page }) => {
    await gotoWithSession(page);

    const timeline = page.locator('#timeline');
    const dialog = page.locator('#preciseTimeDialog');
    const preciseDate = page.locator('#preciseDate');
    const preciseTime = page.locator('#preciseTime');
    const errorText = page.locator('#preciseTimeError');

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();

    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });

    await expect(timeline).toBeEnabled();

    // Открываем диалог
    await timeline.dblclick();
    await expect(dialog).toBeVisible();

    // Очищаем обязательные поля
    await preciseDate.fill('');
    await preciseTime.fill('');

    // Нажимаем Применить
    const applyBtn = dialog.getByRole('button', { name: 'Применить' });
    await applyBtn.click();

    // Диалог должен остаться открытым
    await expect(dialog).toBeVisible();

    // Должно появиться сообщение об ошибке
    await expect(errorText).toContainText(/заполните/i);
  });

  test('slider position updates after seek via dialog', async ({ page }) => {
    await gotoWithSession(page);

    const timeline = page.locator('#timeline');
    const dialog = page.locator('#preciseTimeDialog');
    const preciseDate = page.locator('#preciseDate');
    const preciseTime = page.locator('#preciseTime');
    const preciseMillis = page.locator('#preciseMillis');

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    const rangeFrom = new Date(range.from);
    const rangeTo = new Date(range.to);

    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });

    await expect(timeline).toBeEnabled();

    // Устанавливаем начальную позицию
    await timeline.fill('200');
    await page.waitForTimeout(200);
    const initialValue = await timeline.inputValue();

    // Открываем диалог
    await timeline.dblclick();
    await expect(dialog).toBeVisible();

    // Вычисляем время ближе к концу (позиция ~800)
    const targetRatio = 0.8;
    const targetTime = new Date(rangeFrom.getTime() + (rangeTo.getTime() - rangeFrom.getTime()) * targetRatio);
    const targetDate = targetTime.toISOString().split('T')[0];
    const hours = String(targetTime.getUTCHours()).padStart(2, '0');
    const minutes = String(targetTime.getUTCMinutes()).padStart(2, '0');
    const seconds = String(targetTime.getUTCSeconds()).padStart(2, '0');
    const targetTimeStr = `${hours}:${minutes}:${seconds}`;
    const targetMillis = String(targetTime.getUTCMilliseconds());

    // Заполняем поля
    await preciseDate.fill(targetDate);
    await preciseTime.fill(targetTimeStr);
    await preciseMillis.fill(targetMillis);

    // Применяем
    const applyBtn = dialog.getByRole('button', { name: 'Применить' });
    await applyBtn.click();
    await expect(dialog).toBeHidden({ timeout: 3000 });

    // Проверяем что позиция слайдера изменилась
    await page.waitForTimeout(500);
    const newValue = await timeline.inputValue();
    const newValueNum = parseInt(newValue, 10);
    const initialValueNum = parseInt(initialValue, 10);

    // Новое значение должно быть больше начального (мы перемотали вперёд)
    expect(newValueNum).toBeGreaterThan(initialValueNum);
    // И должно быть близко к 800 (targetRatio * 1000)
    expect(newValueNum).toBeGreaterThan(700);
    expect(newValueNum).toBeLessThan(900);
  });
});
