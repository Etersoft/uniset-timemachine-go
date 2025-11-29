import { test, expect } from '@playwright/test';
import { gotoWithSession, claimControl } from './utils';

test('front-end issues HTTP requests on load and start/stop', async ({ page }) => {
  await gotoWithSession(page);
  // Подстрахуемся, что управление у нас (если предыдущий тест оставил контроллер).
  await claimControl(page, 10, 700);
  await page.request.post('/api/v2/job/reset');

  const setValue = async (selector: string, value: string) => {
    await page.evaluate(
      ([sel, val]) => {
        const el = document.querySelector<HTMLInputElement>(sel);
        if (el) {
          el.value = val;
          el.dispatchEvent(new Event('input', { bubbles: true }));
          el.dispatchEvent(new Event('change', { bubbles: true }));
        }
      },
      [selector, value],
    );
  };

  // Страница сама дергает /api/v2/job — проверим статус.
  await page.waitForResponse(
    (resp) => resp.url().endsWith('/api/v2/job') && resp.status() === 200,
    { timeout: 8_000 },
  );

  // Готовим валидный диапазон.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  await setValue('#from', range.from);
  await setValue('#to', range.to);

  // Проставляем диапазон через API, чтобы play стало доступно.
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  await page.waitForTimeout(1500);
  await expect(page.locator('#playPauseBtn')).toBeEnabled({ timeout: 5_000 });

  // Ждём, пока фронт отправит range и start.
  const rangeResponsePromise = page.waitForResponse(
    (resp) => resp.url().includes('/api/v2/job/range') && resp.request().method() === 'POST',
  );
  const startResponsePromise = page.waitForResponse(
    (resp) => resp.url().includes('/api/v2/job/start') && resp.request().method() === 'POST',
  );
  await page.click('#playPauseBtn');
  const rangeResponse = await rangeResponsePromise;
  const startResponse = await startResponsePromise;
  expect(rangeResponse.ok()).toBeTruthy();
  expect(startResponse.ok()).toBeTruthy();

  const statusBadge = page.locator('#statusBadge');
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 8_000 });

  const stopResponsePromise = page.waitForResponse(
    (resp) => resp.url().includes('/api/v2/job/stop') && resp.request().method() === 'POST',
  );
  await page.click('#stopBtn');
  const stopResponse = await stopResponsePromise;
  expect(stopResponse.ok()).toBeTruthy();
});
