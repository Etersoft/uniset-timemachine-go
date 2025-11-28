import { test, expect } from '@playwright/test';

test('log panel shows entries and clears on click', async ({ page }) => {
  await page.goto('/ui/');

  // Helper to set hidden inputs.
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

  // Apply available range so start is valid.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  await setValue('#from', range.from);
  await setValue('#to', range.to);

  // Устанавливаем диапазон через API, чтобы кнопка старта стала доступна.
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  await page.waitForFunction(() => {
    const el = document.querySelector<HTMLButtonElement>('#playPauseBtn');
    return !!el && !el.disabled;
  }, { timeout: 15_000 });

  const logEntries = page.locator('#log .log-entry');
  const clearBtn = page.locator('#clearLogBtn');
  const playBtn = page.locator('#playPauseBtn');

  // Ждём, пока лог что-то покажет после инициализации.
  await page.waitForFunction(() => document.querySelectorAll('#log .log-entry').length > 0, { timeout: 15_000 });

  // Очищаем.
  await clearBtn.click();
  await expect(logEntries).toHaveCount(0);

  // Действие, которое гарантированно логирует ("Старт отправлен" / ошибка).
  await playBtn.click();
  await page.waitForFunction(() => document.querySelectorAll('#log .log-entry').length > 0, { timeout: 15_000 });

  // Остановим задачу для чистоты.
  await page.request.post('/api/v2/job/stop', { data: {} });
});
