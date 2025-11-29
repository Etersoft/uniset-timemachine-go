import { Page } from '@playwright/test';

// Инициализирует сессию управления, чтобы все запросы были с нужным X-TM-Session.
const DEFAULT_TOKEN = 'playwright-session';

export async function ensureSession(page: Page, token: string = DEFAULT_TOKEN): Promise<string> {
  const resp = await page.request.get(`/api/v2/session?session=${encodeURIComponent(token)}`);
  const data = await resp.json();

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

export async function claimControl(page: Page, retries = 6, delayMs = 800, token?: string): Promise<void> {
  for (let i = 0; i < retries; i += 1) {
    const resp = await page.request.post('/api/v2/session/claim', {
      headers: token ? { 'X-TM-Session': token } : undefined,
    });
    if (resp.ok()) return;
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
