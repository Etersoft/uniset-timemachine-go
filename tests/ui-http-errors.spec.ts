import { test, expect } from '@playwright/test';

test('UI logs HTTP errors on refresh failure', async ({ page }) => {
  // Блокируем первый запрос к /api/v2/job с 500, чтобы init сработал в error-путь.
  await page.route('**/api/v2/job', (route) => {
    route.fulfill({ status: 500, body: 'boom' });
  });

  await page.goto('/ui/');

  const logEntries = page.locator('#log .log-entry');
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll('#log .log-entry')).some((el) => el.textContent?.includes('Init:')),
    { timeout: 15_000 },
  );
  const texts = await logEntries.allTextContents();
  expect(texts.join(' ')).toContain('Init:');
});
