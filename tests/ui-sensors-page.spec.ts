import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('table filter and add-to-chart from Sensors tab', async ({ page }) => {
  test.setTimeout(30_000); // Этот тест делает много операций: play/pause/stop, переключение вкладок
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');

  // Установим рабочий список датчиков заранее.
  const sensorsResp1 = await page.request.get('/api/v2/sensors');
  const sensorsAll = (await sensorsResp1.json())?.sensors ?? [];
  const names = sensorsAll.map((s: any) => s.name).filter((n: any) => typeof n === 'string' && n.length > 0);
  if (names.length) {
    await page.request.post('/api/v2/job/sensors', { data: { sensors: names } });
  }

  // Даже без выбора диапазона таблица должна заполниться метаданными.
  await page.getByRole('button', { name: 'Датчики' }).click();
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, { timeout: 10_000 });

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
  await page.waitForTimeout(1500);

  // Проверяем, что справочник датчиков подгружен и имя из /api/v2/sensors есть в таблице.
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensorsJson = await sensorsResp.json();
  const expectedName = sensorsJson?.sensors?.[0]?.name as string | undefined;

  const rows = page.locator('#tableBody tr');
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, { timeout: 8_000 });
  const total = await rows.count();
  expect(total).toBeGreaterThan(0);

  if (expectedName) {
    const nameCells = page.locator('#tableBody td').filter({ hasText: expectedName });
    const count = await nameCells.count();
    expect(count).toBeGreaterThan(0);
  }
  const textCellBefore = (await rows.first().locator('td').nth(4).textContent())?.trim();
  expect(textCellBefore).toBeTruthy();

  // Берём имя первого датчика и фильтруем по нему.
  const firstName = (await rows.first().locator('td').nth(1).textContent())?.trim();
  expect(firstName).toBeTruthy();
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

  // Прогоняем play/pause/stop и убеждаемся, что имя не пропадает из таблицы.
  await page.getByRole('button', { name: 'Управление' }).click();
  const playBtn = page.locator('#playPauseBtn');
  const stopBtn = page.locator('#stopBtn');
  const statusBadge = page.locator('#statusBadge');
  await expect(playBtn).toBeEnabled({ timeout: 6_000 });
  await playBtn.click();
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 8_000 });
  // Если статус успел стать done, это тоже валидно — задача могла быстро завершиться.
  await playBtn.click({ trial: true }).catch(() => {});
  if (await stopBtn.isEnabled()) {
    await stopBtn.click();
    await expect(statusBadge).toHaveText(/(idle|done)/i, { timeout: 10_000 });
  }

  await page.getByRole('button', { name: 'Датчики' }).click();
  const rowsAfter = page.locator('#tableBody tr');
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, { timeout: 10_000 });
  if (firstName) {
    const countAfter = await page.locator('#tableBody td').filter({ hasText: firstName }).count();
    expect(countAfter).toBeGreaterThan(0);
  }
  const textCellAfter = (await rowsAfter.first().locator('td').nth(4).textContent())?.trim();
  expect(textCellAfter).toBe(textCellBefore);

  // Проверяем легенду графиков.
  await page.getByRole('button', { name: 'Графики' }).click();
  const legendRows = page.locator('#chartLegendBody tr');
  await page.waitForFunction(() => document.querySelectorAll('#chartLegendBody tr').length > 0, { timeout: 10_000 });
});
