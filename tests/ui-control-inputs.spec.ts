import { test, expect } from '@playwright/test';

test('step / speed / cache / save-to-sm controls are applied on start', async ({ page }) => {
  await page.goto('/ui/');

  const statusBadge = page.locator('#statusBadge');
  const stepInput = page.locator('#step');
  const speedInput = page.locator('#speed');
  const windowInput = page.locator('#window');
  const saveCheckbox = page.locator('#saveToSM');
  const playBtn = page.locator('#playPauseBtn');

  // Забираем доступный диапазон и текущий save_allowed.
  const rangeResp = await page.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  const jobResp = await page.request.get('/api/v2/job');
  const job = await jobResp.json();
  const saveAllowed = !!(job.save_allowed ?? job.SaveAllowed);

  // Проставляем диапазон и параметры.
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

  await setValue('#from', range.from);
  await setValue('#to', range.to);
  await setValue('#step', '2s');
  await setValue('#speed', '2');
  await setValue('#window', '10s');

  if (saveAllowed) {
    await expect(saveCheckbox).toBeEnabled();
    await saveCheckbox.check();
  } else {
    await expect(saveCheckbox).toBeDisabled();
  }

  await playBtn.click();
  await expect(statusBadge).not.toHaveText(/failed/i, { timeout: 15_000 });
  await page.waitForTimeout(300);

  const afterJobResp = await page.request.get('/api/v2/job');
  const afterJob = await afterJobResp.json();
  const params = afterJob.params || afterJob.Params || {};

  const stepNs = params.Step ?? params.step;
  const windowNs = params.Window ?? params.window;
  const speedVal = params.Speed ?? params.speed;
  const saveOutput = params.save_output ?? params.SaveOutput;

  expect(stepNs).toBeGreaterThanOrEqual(1_000_000_000); // 1s+
  expect(stepNs).toBeLessThanOrEqual(2_000_000_000); // should reflect 2s override
  expect(windowNs).toBeGreaterThanOrEqual(5_000_000_000); // 5s+
  expect(speedVal).toBeGreaterThan(0);
  if (saveAllowed) {
    expect(saveOutput).toBe(true);
  } else {
    expect(saveOutput ?? false).toBeFalsy();
  }

  // Остановим задачу для последующих тестов.
  await page.request.post('/api/v2/job/stop', { data: {} });
});
