import { test, expect } from '@playwright/test';
import { ensureSessionOnly, claimControl } from './utils';

// Проверяем блокировку управления для второй сессии и возможность забрать управление после таймаута.
test('second session is read-only until claim after timeout', async ({ browser }) => {
  test.setTimeout(30_000);
  const tokenA = 'lock-A';
  const tokenB = 'lock-B';

  // Сессия A: забирает управление и устанавливает диапазон.
  const contextA = await browser.newContext();
  const pageA = await contextA.newPage();
  await ensureSessionOnly(pageA, tokenA);
  await claimControl(pageA, 20, 700, tokenA);
  const rangeResp = await pageA.request.get('/api/v2/job/range');
  const range = await rangeResp.json();
  const rangeSetResp = await pageA.request.post('/api/v2/job/range', {
    data: { from: range.from, to: range.to, step: '1s', speed: 1, window: '5s' },
  });
  expect(rangeSetResp.ok()).toBeTruthy();

  // Сессия B: команды должны блокироваться.
  const contextB = await browser.newContext();
  const pageB = await contextB.newPage();
  await ensureSessionOnly(pageB, tokenB);
  await pageB.goto('/ui/');
  const statusResp = await pageB.request.get(`/api/v2/session?session=${tokenB}`);
  const statusData = await statusResp.json();
  const timeoutSec = Number(statusData.control_timeout_sec || 5);

  const stopByB = await pageB.request.post('/api/v2/job/stop');
  expect(stopByB.status()).toBe(403);
  // Индикатор блокировки в UI.
  const lockChip = pageB.locator('#controlLockChip');
  await expect(lockChip).toBeVisible({ timeout: 3_000 });
  await expect(lockChip).toContainText('Управление отключено');

  // Закрываем A, ждём таймаут и забираем управление B.
  await contextA.close();
  await pageB.waitForTimeout(timeoutSec * 1000 + 800);
  await claimControl(pageB, 12, Math.max(500, timeoutSec * 1000 / 6), tokenB);

  // Теперь stop от B проходит, от A — нет.
  const stopB2 = await pageB.request.post('/api/v2/job/stop');
  expect(stopB2.ok()).toBeTruthy();
  const stopA2 = await pageB.request.post('/api/v2/job/stop', { headers: { 'X-TM-Session': tokenA } });
  expect(stopA2.status()).toBe(403);
  // Индикатор пропадает после получения управления (после обновления UI/refresh).
  await pageB.reload();
  await expect(lockChip).toBeHidden({ timeout: 4_000 });

  // Вернём управление дефолтной сессии (best effort).
  await pageB.waitForTimeout(timeoutSec * 1000 + 500);
  await claimControl(pageB, 6, 600, 'playwright-session').catch(() => {});

  await contextB.close();
});
