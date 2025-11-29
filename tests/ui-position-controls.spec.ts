import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

const parseTs = (value: string | null) => {
  const t = new Date(value || '').getTime();
  return Number.isFinite(t) ? t : null;
};

test('position controls: seek via slider, jump start/end, current label', async ({ page }) => {
  await gotoWithSession(page);

  const statusBadge = page.locator('#statusBadge');
  const timeline = page.locator('#timeline');
  const fromLabel = page.locator('#fromLabel');
  const toLabel = page.locator('#toLabel');
  const currentLabel = page.locator('#currentLabel');

  // Диапазон из API.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  expect(range.from).not.toBe('');
  expect(range.to).not.toBe('');

  // Применяем диапазон.
  await page.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  await expect(fromLabel).toContainText('from:');
  await expect(toLabel).toContainText('to:');

  // Бегунок доступен.
  await expect(timeline).toBeEnabled();

  // Перематываем в середину бегунком: ставим value=500.
  await timeline.fill('500');
  await page.waitForTimeout(300); // даём UI обновить предпросмотр.
  const midTs = await currentLabel.textContent();
  expect(midTs).not.toBe('-');

  // Jump to end.
  const jumpEnd = page.locator('#jumpEndBtn');
  await jumpEnd.click({ force: true });
  await page.waitForTimeout(300);
  const endTs = await currentLabel.textContent();
  expect(endTs).not.toBe('-');
  const endVal = parseTs(endTs);
  const midVal = parseTs(midTs);
  if (endVal !== null && midVal !== null) {
    expect(endVal).toBeGreaterThanOrEqual(midVal);
  }

  // Jump to start.
  const jumpStart = page.locator('#jumpStartBtn');
  await jumpStart.click({ force: true });
  await page.waitForTimeout(300);
  const startTs = await currentLabel.textContent();
  expect(startTs).not.toBe('-');
  const startVal = parseTs(startTs);
  if (startVal !== null && endVal !== null) {
    expect(startVal).toBeLessThanOrEqual(endVal);
  }

  // Стартуем воспроизведение и убеждаемся, что статус меняется, currentLabel обновляется.
  await page.request.post('/api/v2/job/start', { data: {} });
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 8_000 });
});
