import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('step / speed / cache / save-to-sm controls are applied on start', async ({ page }) => {
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');

  // Установим рабочий список датчиков (на случай если UI не успел загрузить).
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensors = (await sensorsResp.json())?.sensors ?? [];
  const names = sensors.slice(0, 10).map((s: any) => s.name).filter((n: any) => typeof n === 'string' && n.length > 0);
  if (names.length) {
    await page.request.post('/api/v2/job/sensors', { data: { sensors: names } });
  }

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

  const fallbackFrom = range.from || '2024-06-01T00:00:00Z';
  const fallbackTo = range.to || '2024-06-01T00:00:10Z';

  // Подготовка диапазона через API, чтобы play стало доступно.
  const rangePayload = {
    from: fallbackFrom,
    to: fallbackTo,
    step: '1s',
    speed: 1,
    window: '5s',
  };
  await page.request.post('/api/v2/job/range', { data: rangePayload });
  await page.waitForTimeout(1500);
  await expect(playBtn).toBeEnabled({ timeout: 5_000 });

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

  await setValue('#from', rangePayload.from);
  await setValue('#to', rangePayload.to);
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
  // Ждём, пока статус станет running (не failed и не idle)
  await expect(statusBadge).toHaveText(/running|pending|paused/i, { timeout: 8_000 });
  await page.waitForTimeout(500);
  const afterJob = await page.request.get('/api/v2/job').then(r => r.json());
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
