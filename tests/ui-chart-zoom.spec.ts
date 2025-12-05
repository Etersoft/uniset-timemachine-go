import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test.describe('Chart zoom functionality', () => {
  test('drag zoom shows reset button, reset restores range without collapsing', async ({ page }) => {
    await gotoWithSession(page);

    // Переключаемся на вкладку графиков
    await page.click('[data-tab="charts"]');
    await expect(page.locator('#panel-charts')).toBeVisible();

    // Получаем список датчиков и добавляем первый на график
    const sensorsResp = await page.request.get('/api/v2/sensors');
    const sensorsData = await sensorsResp.json();
    const sensors = sensorsData.sensors || [];
    const sensorNames = sensors.map((s: any) => s.name).filter((n: any) => typeof n === 'string' && n.length > 0);
    expect(sensorNames.length).toBeGreaterThan(0);

    const firstSensor = sensorNames[0];
    const chartInput = page.locator('#chartSensors');
    await chartInput.fill(firstSensor);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(500);

    // Устанавливаем диапазон воспроизведения
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });
    await page.waitForTimeout(500);

    // Проверяем что график виден
    const chartCanvas = page.locator('#chartCanvas');
    await expect(chartCanvas).toBeVisible();

    // Проверяем что кнопка сброса zoom скрыта
    const zoomResetBtn = page.locator('#chartZoomReset');
    await expect(zoomResetBtn).toBeHidden();

    // Проверяем что секция аналоговых графиков развёрнута
    const chartContent = page.locator('#chartContentAnalog');
    await expect(chartContent).toBeVisible();

    // Выполняем drag zoom на графике (выделяем область)
    const box = await chartCanvas.boundingBox();
    expect(box).not.toBeNull();
    if (box) {
      const startX = box.x + box.width * 0.2;
      const endX = box.x + box.width * 0.6;
      const y = box.y + box.height / 2;

      await page.mouse.move(startX, y);
      await page.mouse.down();
      await page.mouse.move(endX, y, { steps: 10 });
      await page.mouse.up();
    }

    await page.waitForTimeout(300);

    // После zoom кнопка сброса должна появиться
    await expect(zoomResetBtn).toBeVisible();

    // Секция должна остаться развёрнутой
    await expect(chartContent).toBeVisible();

    // Кликаем на кнопку сброса zoom
    await zoomResetBtn.click();
    await page.waitForTimeout(300);

    // Кнопка должна скрыться
    await expect(zoomResetBtn).toBeHidden();

    // Секция должна остаться развёрнутой (не свернуться при клике)
    await expect(chartContent).toBeVisible();

    // График должен остаться видимым
    await expect(chartCanvas).toBeVisible();
  });

  test('zoom reset button in header does not trigger section collapse', async ({ page }) => {
    await gotoWithSession(page);

    await page.click('[data-tab="charts"]');
    await expect(page.locator('#panel-charts')).toBeVisible();

    // Добавляем датчик
    const sensorsResp = await page.request.get('/api/v2/sensors');
    const sensorsData = await sensorsResp.json();
    const sensors = sensorsData.sensors || [];
    const sensorNames = sensors.map((s: any) => s.name).filter((n: any) => typeof n === 'string' && n.length > 0);
    if (sensorNames.length === 0) {
      test.skip();
      return;
    }

    const chartInput = page.locator('#chartSensors');
    await chartInput.fill(sensorNames[0]);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(500);

    // Устанавливаем диапазон
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    await page.request.post('/api/v2/job/range', {
      data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
    });
    await page.waitForTimeout(500);

    const chartCanvas = page.locator('#chartCanvas');
    const zoomResetBtn = page.locator('#chartZoomReset');
    const chartContent = page.locator('#chartContentAnalog');

    // Делаем zoom
    const box = await chartCanvas.boundingBox();
    if (box) {
      await page.mouse.move(box.x + box.width * 0.3, box.y + box.height / 2);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width * 0.7, box.y + box.height / 2, { steps: 5 });
      await page.mouse.up();
    }
    await page.waitForTimeout(300);

    // Кнопка видна
    await expect(zoomResetBtn).toBeVisible();

    // Запоминаем высоту контента до клика
    const heightBefore = await chartContent.evaluate(el => el.offsetHeight);

    // Кликаем на кнопку сброса
    await zoomResetBtn.click();
    await page.waitForTimeout(300);

    // Контент должен остаться видимым с той же высотой (не свернуться)
    await expect(chartContent).toBeVisible();
    const heightAfter = await chartContent.evaluate(el => el.offsetHeight);
    expect(heightAfter).toBe(heightBefore);
  });
});
