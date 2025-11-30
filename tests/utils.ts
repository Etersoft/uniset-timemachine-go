import { Page } from '@playwright/test';

// Инициализирует сессию управления, чтобы все запросы были с нужным X-TM-Session.
const DEFAULT_TOKEN = 'playwright-session';

export async function ensureSession(page: Page, token: string = DEFAULT_TOKEN): Promise<string> {
  // Убрали вызов GET /api/v2/session - он делает токен контроллером автоматически!
  // Вместо этого просто настраиваем токен для будущих запросов.
  // Сам вызов /api/v2/session произойдёт при загрузке UI через initSession().

  await page.context().setExtraHTTPHeaders({
    'X-TM-Session': token,
  });
  // Проставляем токен в localStorage до загрузки UI.
  await page.context().addInitScript((tok) => {
    try {
      window.localStorage.setItem('tm_session', tok as string);
    } catch {
      /* ignore */
    }
  }, token);
  return token;
}

export async function claimControl(page: Page, retries = 10, delayMs = 700, token?: string): Promise<void> {
  for (let i = 0; i < retries; i += 1) {
    const resp = await page.request.post('/api/v2/session/claim', {
      headers: token ? { 'X-TM-Session': token } : undefined,
    });
    if (resp.ok()) {
      // После успешного claim принудительно вызываем refresh() в UI
      // чтобы не ждать следующего polling цикла (1500ms)
      await page.evaluate(() => {
        if (typeof (window as any).refresh === 'function') {
          return (window as any).refresh(false);
        }
      }).catch(() => { /* ignore if refresh doesn't exist yet */ });

      // Небольшая задержка чтобы refresh успел выполниться
      await page.waitForTimeout(300);
      return;
    }
    await page.waitForTimeout(delayMs);
  }
  throw new Error('cannot claim control session');
}

export async function gotoWithSession(page: Page, url = '/ui/', autoClaim = true, token?: string): Promise<string> {
  const tok = await ensureSession(page, token);
  if (autoClaim) {
    await claimControl(page, undefined, undefined, tok);
  }
  await page.goto(url);
  return tok;
}

export async function ensureSessionOnly(page: Page, token?: string): Promise<string> {
  return ensureSession(page, token);
}

// Сбрасывает все активные сессии на сервере для чистоты теста
export async function clearAllSessions(page: Page): Promise<void> {
  // Вызываем API сброса через POST /api/v2/session/clear-all (если есть)
  // Или просто ждём timeout. Для простоты - просто логаутим известные токены
  try {
    // Пытаемся сбросить несколько типичных токенов из тестов
    const testTokens = [
      'session-1', 'session-2', 'session-timeout-1', 'session-timeout-2', 'session-refresh-test', 'playwright-session'
    ];
    for (const tok of testTokens) {
      await page.request.post(`/api/v2/session/logout?force=1&session=${encodeURIComponent(tok)}`).catch(() => { /* ignore errors */ });
    }
    // Дополнительно сбрасываем без токена (force) на случай неизвестного контроллера.
    await page.request.post('/api/v2/session/logout?force=1').catch(() => {});
  } catch {
    /* ignore */
  }
}
