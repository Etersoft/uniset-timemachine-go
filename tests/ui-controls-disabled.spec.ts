import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('player controls stay disabled until range is set', async ({ page }) => {
  // Сбрасываем состояние сервера, чтобы не было активной/отложенной задачи.
  await gotoWithSession(page, '/ui/', false, 'controls-disabled');
  await page.request.post('/api/v2/job/reset').catch(() => {});

  const playBtn = page.locator('#playPauseBtn');
  const stopBtn = page.locator('#stopBtn');
  const backBtn = page.locator('#stepBackBtn');
  const fwdBtn = page.locator('#stepFwdBtn');
  const slider = page.locator('#timeline');

  await expect(playBtn).toBeDisabled();
  await expect(stopBtn).toBeDisabled();
  await expect(backBtn).toBeDisabled();
  await expect(fwdBtn).toBeDisabled();
  await expect(slider).toBeDisabled();
});
