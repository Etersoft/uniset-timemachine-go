import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// Тест для создания скриншотов документации
// Запуск: docker-compose --profile tests run --rm --entrypoint "" playwright \
//   npx playwright test doc-screenshots.spec.ts -c tests/playwright.config.ts --reporter=list

const screenshotsDir = '/workspace/docs/screenshots';

test.describe('Documentation Screenshots', () => {
  test.beforeAll(async () => {
    // Создаём директорию для скриншотов
    if (!fs.existsSync(screenshotsDir)) {
      fs.mkdirSync(screenshotsDir, { recursive: true });
    }
  });

  test('main interface overview', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');

    // Ждём загрузки данных
    await page.waitForTimeout(1000);

    // Общий вид интерфейса
    await page.screenshot({
      path: path.join(screenshotsDir, '01-main-overview.png'),
      fullPage: false
    });
  });

  test('control panel', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // Панель управления (верхняя часть)
    const controlPanel = page.locator('.card').first();
    await controlPanel.screenshot({
      path: path.join(screenshotsDir, '02-control-panel.png')
    });
  });

  test('range dialog', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // Открываем диалог диапазона через JavaScript
    await page.evaluate(() => {
      const dialog = document.getElementById('rangeDialog') as HTMLDialogElement;
      if (dialog) dialog.showModal();
    });
    await page.waitForTimeout(300);

    const dialog = page.locator('#rangeDialog');
    if (await dialog.isVisible()) {
      await dialog.screenshot({
        path: path.join(screenshotsDir, '03-range-dialog.png')
      });

      // Закрываем диалог
      await page.keyboard.press('Escape');
    }
  });

  test('sensors table tab', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');

    // Переходим на вкладку таблицы
    const tableTab = page.locator('[data-tab="table"]');
    await tableTab.click();
    await page.waitForTimeout(500);

    // Скриншот таблицы датчиков
    await page.screenshot({
      path: path.join(screenshotsDir, '04-sensors-table.png'),
      fullPage: false
    });
  });

  test('table with filter', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');

    // Переходим на вкладку таблицы
    const tableTab = page.locator('[data-tab="table"]');
    await tableTab.click();
    await page.waitForTimeout(300);

    // Вводим фильтр
    const filterInput = page.locator('#tableFilter');
    await filterInput.fill('Input');
    await page.waitForTimeout(300);

    await page.screenshot({
      path: path.join(screenshotsDir, '05-table-filtered.png'),
      fullPage: false
    });
  });

  test('charts tab empty', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');

    // Переходим на вкладку графиков
    const chartsTab = page.locator('[data-tab="charts"]');
    await chartsTab.click();
    await page.waitForTimeout(300);

    await page.screenshot({
      path: path.join(screenshotsDir, '06-charts-empty.png'),
      fullPage: false
    });
  });

  test('charts with sensors', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');

    // Переходим на вкладку графиков
    const chartsTab = page.locator('[data-tab="charts"]');
    await chartsTab.click();
    await page.waitForTimeout(300);

    // Добавляем датчик через поле ввода
    const sensorInput = page.locator('#chartSensors');
    await sensorInput.fill('Input1');
    await page.waitForTimeout(500);

    // Кликаем на первую подсказку если есть
    const suggestion = page.locator('.chart-suggest-box .suggest-item').first();
    if (await suggestion.isVisible()) {
      await suggestion.click();
      await page.waitForTimeout(300);
    }

    await page.screenshot({
      path: path.join(screenshotsDir, '07-charts-with-sensor.png'),
      fullPage: false
    });
  });

  test('working sensors dialog', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // Открываем диалог рабочих датчиков через JavaScript
    await page.evaluate(() => {
      const dialog = document.getElementById('sensorsDialog') as HTMLDialogElement;
      if (dialog) dialog.showModal();
    });
    await page.waitForTimeout(300);

    const dialog = page.locator('#sensorsDialog');
    if (await dialog.isVisible()) {
      await dialog.screenshot({
        path: path.join(screenshotsDir, '08-working-sensors-dialog.png')
      });

      await page.keyboard.press('Escape');
    }
  });

  test('log panel', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForLoadState('networkidle');

    // Вкладки логов нет - делаем скриншот панели управления с логами
    // Логи отображаются в #logBody
    await page.screenshot({
      path: path.join(screenshotsDir, '09-control-with-log.png'),
      fullPage: false
    });
  });
});
