import { test, expect } from '@playwright/test';

// Проверяем, что после Stop кнопка Play остаётся доступной при заданном диапазоне.
test('play is enabled after stop when range is set', async ({ page }) => {
  await page.request.post('/api/v2/job/reset');
  await page.goto('/ui/');

  // Получаем доступный диапазон и применяем его через API.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });

  const playBtn = page.locator('#playPauseBtn');
  const stopBtn = page.locator('#stopBtn');

  // После установки диапазона play должен разблокироваться.
  await expect(playBtn).toBeEnabled({ timeout: 5_000 });

  // Запускаем и сразу останавливаем через API, чтобы не зависеть от задержек UI.
  await playBtn.click();
  await page.waitForTimeout(300);
  await page.request.post('/api/v2/job/stop', { data: {} });

  // После остановки play остаётся доступной.
  await expect(playBtn).toBeEnabled({ timeout: 5_000 });
});
