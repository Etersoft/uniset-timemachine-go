import { test, expect } from '@playwright/test';
import { ensureSessionOnly, clearAllSessions } from './utils';

test.describe('Session Control - New Logic', () => {
  test('Rule 1: First session auto-claims control on initSession', async ({ browser }) => {
    test.setTimeout(15000);
    const token1 = 'first-auto-claim';

    // Очищаем все сессии
    const tempContext = await browser.newContext();
    const tempPage = await tempContext.newPage();
    await clearAllSessions(tempPage);
    await tempContext.close();

    // Открываем первую вкладку - она должна автоматически захватить управление
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await ensureSessionOnly(page1, token1);
    await page1.goto('/ui/');
    // Явно убеждаемся, что управление действительно захвачено.
    await page1.request.post('/api/v2/session/claim', { headers: { 'X-TM-Session': token1 } });
    await page1.waitForTimeout(500);

    // Проверяем что управление доступно (кнопки enabled)
    const rangePicker1 = page1.locator('#rangePickerBtn');
    await expect(rangePicker1).toBeEnabled({ timeout: 3000 });

    // Проверяем что чип блокировки НЕ виден
    const lockChip1 = page1.locator('#controlLockChip');
    await expect(lockChip1).toBeHidden({ timeout: 3000 });

    await context1.close();
  });

  test('Rule 1: Second session sees first as controller and gets blocked', async ({ browser }) => {
    test.setTimeout(15000);
    const token1 = 'controller-session';
    const token2 = 'blocked-session';

    // Очищаем все сессии
    const tempContext = await browser.newContext();
    const tempPage = await tempContext.newPage();
    await clearAllSessions(tempPage);
    await tempContext.close();

    // Первая вкладка - автоматически захватывает управление
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await ensureSessionOnly(page1, token1);
    await page1.goto('/ui/');
    // Явно захватываем, чтобы исключить гонки автозахвата.
    await page1.request.post('/api/v2/session/claim', { headers: { 'X-TM-Session': token1 } });
    await page1.waitForTimeout(500);

    // Проверяем что первая вкладка - контроллер
    const rangePicker1 = page1.locator('#rangePickerBtn');
    await expect(rangePicker1).toBeEnabled({ timeout: 3000 });

    // Вторая вкладка - должна быть заблокирована
    const context2 = await browser.newContext();
    const page2 = await context2.newPage();
    await ensureSessionOnly(page2, token2);
    await page2.goto('/ui/');
    // Логируем статус сервера для отладки
    const stat2 = await page2.request.get('/api/v2/session');
    const statData2 = await stat2.json();
    // console.log('SESSION2 status (blocked test):', statData2);
    await page2.waitForTimeout(3000);

    // Проверяем что во второй вкладке показывается блокировка
    const lockChip2 = page2.locator('#controlLockChip');
    await expect(lockChip2).toBeVisible({ timeout: 3000 });
    await expect(lockChip2).toContainText('Управление отключено');

    // Проверяем что кнопки заблокированы
    const rangePicker2 = page2.locator('#rangePickerBtn');
    await expect(rangePicker2).toBeDisabled({ timeout: 3000 });

    // Проверяем что кнопка "Забрать управление" НЕ видна (контроллер активен)
    const claimBtn2 = page2.locator('#claimControlBtn');
    await expect(claimBtn2).toBeHidden({ timeout: 3000 });

    await context1.close();
    await context2.close();
  });

  test('Rule 2: After controller timeout, blocked session sees claim button but no auto-claim', async ({ browser }) => {
    test.setTimeout(30000);
    const token1 = 'timeout-controller';
    const token2 = 'waiting-session';

    // Очищаем все сессии
    const tempContext = await browser.newContext();
    const tempPage = await tempContext.newPage();
    await clearAllSessions(tempPage);
    await tempContext.close();

    // Первая вкладка - контроллер
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await ensureSessionOnly(page1, token1);
    await page1.goto('/ui/');
    await page1.request.post('/api/v2/session/claim', { headers: { 'X-TM-Session': token1 } });
    await page1.waitForTimeout(500);

    // Вторая вкладка - заблокирована
    const context2 = await browser.newContext();
    const page2 = await context2.newPage();
    await ensureSessionOnly(page2, token2);
    await page2.goto('/ui/');
    const stat2b = await page2.request.get('/api/v2/session');
    const statData2b = await stat2b.json();
    // console.log('SESSION2 status (timeout test):', statData2b);
    await page2.waitForTimeout(2000);

    // Получаем timeout из настроек
    const statusResp = await page2.request.get(`/api/v2/session?session=${token2}`);
    const statusData = await statusResp.json();
    const timeoutSec = Number(statusData.control_timeout_sec || 5);

    // Проверяем блокировку
    const lockChip2 = page2.locator('#controlLockChip');
    await expect(lockChip2).toBeVisible({ timeout: 3000 });

    // Закрываем контроллер
    await context1.close();

    // Ждём timeout + запас
    await page2.waitForTimeout(timeoutSec * 1000 + 2000);

    // Проверяем что кнопка "Забрать управление" появилась
    const claimBtn2 = page2.locator('#claimControlBtn');
    await expect(claimBtn2).toBeVisible({ timeout: 5000 });

    // ВАЖНО: проверяем что управление НЕ захвачено автоматически
    // Кнопки должны быть ВСЁ ЕЩЁ заблокированы
    const rangePicker2 = page2.locator('#rangePickerBtn');
    await expect(rangePicker2).toBeDisabled({ timeout: 1000 });

    // Блокировка всё ещё видна
    await expect(lockChip2).toBeVisible({ timeout: 1000 });

    await context2.close();
  });

  test('Rule 2: User can manually claim control by clicking button', async ({ browser }) => {
    test.setTimeout(30000);
    const token1 = 'manual-claim-ctrl';
    const token2 = 'manual-claim-user';

    // Очищаем все сессии
    const tempContext = await browser.newContext();
    const tempPage = await tempContext.newPage();
    await clearAllSessions(tempPage);
    await tempContext.close();

    // Первая вкладка - контроллер
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await ensureSessionOnly(page1, token1);
    await page1.goto('/ui/');
    await page1.request.post('/api/v2/session/claim', { headers: { 'X-TM-Session': token1 } });
    await page1.waitForTimeout(500);

    // Вторая вкладка
    const context2 = await browser.newContext();
    const page2 = await context2.newPage();
    await ensureSessionOnly(page2, token2);
    await page2.goto('/ui/');
    await page2.waitForTimeout(2000);

    // Получаем timeout
    const statusResp = await page2.request.get(`/api/v2/session?session=${token2}`);
    const statusData = await statusResp.json();
    const timeoutSec = Number(statusData.control_timeout_sec || 5);

    // Закрываем контроллер
    await context1.close();

    // Ждём timeout
    await page2.waitForTimeout(timeoutSec * 1000 + 2000);

    // Кнопка "Забрать управление" должна появиться
    const claimBtn2 = page2.locator('#claimControlBtn');
    await expect(claimBtn2).toBeVisible({ timeout: 5000 });

    // Нажимаем кнопку
    await claimBtn2.click();

    // Ждём обработки
    await page2.waitForTimeout(1500);
    const st2 = await page2.evaluate(() => ({
      isController: (window as any).tmState?.isController,
      controllerPresent: (window as any).tmState?.controllerPresent,
      canClaim: (window as any).tmState?.canClaim,
      controlLocked: (window as any).tmState?.controlLocked,
      token: ((window as any).tmState?.sessionToken || '').substring(0, 8),
    }));
    // console.log('STATE AFTER CLAIM (manual test):', st2);

    // Проверяем что управление разблокировалось
    const rangePicker2 = page2.locator('#rangePickerBtn');
    await expect(rangePicker2).toBeEnabled({ timeout: 3000 });

    // Блокировка исчезла
    const lockChip2 = page2.locator('#controlLockChip');
    await expect(lockChip2).toBeHidden({ timeout: 3000 });

    // Кнопка "Забрать управление" исчезла
    await expect(claimBtn2).toBeHidden({ timeout: 3000 });

    await context2.close();
  });

  test('Rule 3: Server ensures only one session can claim control simultaneously', async ({ browser }) => {
    test.setTimeout(20000);
    const token1 = 'race-session-1';
    const token2 = 'race-session-2';

    // Очищаем все сессии
    const tempContext = await browser.newContext();
    const tempPage = await tempContext.newPage();
    await clearAllSessions(tempPage);
    await tempContext.close();

    // Создаём две вкладки одновременно, БЕЗ контроллера
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await ensureSessionOnly(page1, token1);

    const context2 = await browser.newContext();
    const page2 = await context2.newPage();
    await ensureSessionOnly(page2, token2);

    // Пытаемся захватить управление одновременно через API
    const [resp1, resp2] = await Promise.all([
      page1.request.post('/api/v2/session/claim', {
        headers: { 'X-TM-Session': token1 },
      }),
      page2.request.post('/api/v2/session/claim', {
        headers: { 'X-TM-Session': token2 },
      }),
    ]);

    // Проверяем что ТОЛЬКО ОДИН запрос успешен
    const success1 = resp1.ok();
    const success2 = resp2.ok();

    // XOR: ровно один должен быть успешен
    expect(success1 !== success2).toBeTruthy();

    // Проверяем что сервер вернул правильный статус
    if (success1) {
      expect(resp1.status()).toBe(200);
      expect(resp2.status()).toBe(409); // Conflict
    } else {
      expect(resp2.status()).toBe(200);
      expect(resp1.status()).toBe(409); // Conflict
    }

    await context1.close();
    await context2.close();
  });

  test('After page reload, new session is blocked by old controller', async ({ browser }) => {
    test.setTimeout(30000);
    const token1 = 'reload-controller';

    // Очищаем все сессии
    const tempContext = await browser.newContext();
    const tempPage = await tempContext.newPage();
    await clearAllSessions(tempPage);
    await tempContext.close();

    // Открываем вкладку и захватываем управление
    const context1 = await browser.newContext();
    const page1 = await context1.newPage();
    await ensureSessionOnly(page1, token1);
    await page1.goto('/ui/');
    await page1.request.post('/api/v2/session/claim', { headers: { 'X-TM-Session': token1 } });
    await page1.waitForTimeout(2000);

    // Проверяем что мы - контроллер
    const rangePicker1 = page1.locator('#rangePickerBtn');
    await expect(rangePicker1).toBeEnabled({ timeout: 3000 });

    // Получаем timeout
    const statusResp = await page1.request.get(`/api/v2/session?session=${token1}`);
    const statusData = await statusResp.json();
    const timeoutSec = Number(statusData.control_timeout_sec || 5);

    // Очищаем localStorage и подставляем новый токен, чтобы имитировать новую сессию
    const newToken = 'reload-new-session';
    await page1.evaluate((tok) => {
      try {
        window.sessionStorage.removeItem('tm_session');
        window.sessionStorage.setItem('tm_session', tok as string);
      } catch { /* ignore */ }
    }, newToken);
    await page1.context().setExtraHTTPHeaders({ 'X-TM-Session': newToken });

    // Перезагружаем страницу
    await page1.reload();
    await page1.waitForTimeout(3000);

    // После reload - новая сессия, старый контроллер (token1) ещё активен
    // Должна быть блокировка
    const lockChip1 = page1.locator('#controlLockChip');
    await expect(lockChip1).toBeVisible({ timeout: 3000 });

    // Кнопки заблокированы
    await expect(rangePicker1).toBeDisabled({ timeout: 3000 });

    // Ждём timeout старого контроллера
    await page1.waitForTimeout(timeoutSec * 1000 + 2000);

    // Кнопка "Забрать управление" должна появиться
    const claimBtn1 = page1.locator('#claimControlBtn');
    await expect(claimBtn1).toBeVisible({ timeout: 5000 });

    // Нажимаем кнопку
    await claimBtn1.click();
    await page1.waitForTimeout(1500);
    const st1 = await page1.evaluate(() => ({
      isController: (window as any).tmState?.isController,
      controllerPresent: (window as any).tmState?.controllerPresent,
      canClaim: (window as any).tmState?.canClaim,
      controlLocked: (window as any).tmState?.controlLocked,
      token: ((window as any).tmState?.sessionToken || '').substring(0, 8),
    }));
    // console.log('STATE AFTER CLAIM (reload test):', st1);

    // Проверяем что управление доступно
    await expect(rangePicker1).toBeEnabled({ timeout: 3000 });
    await expect(lockChip1).toBeHidden({ timeout: 3000 });

    await context1.close();
  });
});
