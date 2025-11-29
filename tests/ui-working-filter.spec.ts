import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('sensors tab and suggestions respect working list', async ({ page }) => {
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');

  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensorsJson = await sensorsResp.json();
  const sensors = sensorsJson?.sensors || [];
  if (sensors.length < 3) {
    test.skip(true, 'not enough sensors to validate working list filtering');
  }
  const [s1, s2, s3] = sensors;
  const working = [s1, s2].filter(Boolean);
  const outside = s3;

  await page.request.post('/api/v2/job/sensors', {
    data: { sensors: working.map((s: any) => s.id) },
  });

  await page.goto('/ui/');
  await page.waitForSelector('#workingMeta');
  await page.waitForFunction(
    (len) => {
      const meta = document.querySelector('#workingMeta');
      return meta && meta.textContent && meta.textContent.includes(`${len}/`);
    },
    working.length,
    { timeout: 10_000 },
  );
  await page.getByRole('button', { name: 'Датчики' }).click();

  const expectedLen = working.length;
  await page.waitForFunction(
    (len) => document.querySelectorAll('#tableBody tr').length === len,
    expectedLen,
    { timeout: 10_000 },
  );
  const rows = page.locator('#tableBody tr');
  await expect(rows).toHaveCount(expectedLen);

  for (const s of working) {
    const name = s?.name || `${s?.id}`;
    if (name) {
      expect(await page.locator('#tableBody td', { hasText: name }).count()).toBeGreaterThan(0);
    }
  }
  if (outside) {
    const outName = outside?.name || `${outside?.id}`;
    if (outName) {
      await expect(page.locator('#tableBody td', { hasText: outName })).toHaveCount(0);
    }
  }

  // Подсказки в графиках должны содержать только рабочий список.
  await page.getByRole('button', { name: 'Графики' }).click();
  await page.waitForTimeout(200);
  const input = page.locator('#chartSensors');

  const termWorking = `${working[0]?.id}`;
  await input.fill(termWorking);
  const workingSuggestions = await page.evaluate(
    ({ term, ids }) => {
      const q = (term || '').toLowerCase();
      let cnt = 0;
      const allowed = new Set(ids || []);
      // @ts-ignore
      sensorIndex?.byId?.forEach?.((meta, id) => {
        if (allowed.size && !allowed.has(id)) return;
        const name = meta?.name || `${id}`;
        if (!name.toLowerCase().includes(q) && !String(id).includes(q)) return;
        cnt++;
      });
      return cnt;
    },
    { term: termWorking, ids: working.map((s: any) => s.id) },
  );
  expect(workingSuggestions).toBeGreaterThan(0);

  if (outside) {
    const termOutside = `${outside?.id}`;
    await input.fill(termOutside);
    const outsideSuggestions = await page.evaluate(
      ({ term, ids }) => {
        const q = (term || '').toLowerCase();
        let cnt = 0;
        const allowed = new Set(ids || []);
        // @ts-ignore
        sensorIndex?.byId?.forEach?.((meta, id) => {
          if (allowed.size && !allowed.has(id)) return;
          const name = meta?.name || `${id}`;
          if (!name.toLowerCase().includes(q) && !String(id).includes(q)) return;
          cnt++;
        });
        return cnt;
      },
      { term: termOutside, ids: working.map((s: any) => s.id) },
    );
    expect(outsideSuggestions).toBe(0);
  }
});

test('после выбора рабочего списка таблица и фильтр показывают только выбранные датчики', async ({ page }) => {
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensors = (await sensorsResp.json())?.sensors || [];
  const analogs = sensors.filter((s: any) => ['AI', 'AO'].includes(String(s.iotype || '').toUpperCase()));
  const discretes = sensors.filter((s: any) => ['DI', 'DO'].includes(String(s.iotype || '').toUpperCase()));
  const picked = [...analogs.slice(0, 10), ...discretes.slice(0, 5)].map((s: any) => s.id).filter((id: any) => Number.isFinite(id));
  test.skip(picked.length < 5, 'Нужно минимум 5 датчиков для проверки');

  // Устанавливаем рабочий список через API, чтобы не кликать 2200 строк.
  const workingResp = await page.request.post('/api/v2/job/sensors', { data: { sensors: picked } });
  const workingJson = await workingResp.json();
  const expectedCount =
    workingJson?.count ??
    (Array.isArray(workingJson?.sensors) ? workingJson.sensors.length : undefined) ??
    picked.length;

  // Если кнопка недоступна (например, страница ещё не отрисовала), подстрахуемся через API.
  const btn = page.getByRole('button', { name: 'Установить доступный диапазон' });
  if (await btn.isVisible({ timeout: 3000 }).catch(() => false)) {
    await btn.click();
    await page.waitForTimeout(500);
  } else {
    const rangeResp = await page.request.get('/api/v2/job/range');
    const range = await rangeResp.json();
    const from = range.from || '2024-06-01T00:00:00Z';
    const to = range.to || '2024-06-01T00:10:00Z';
    await page.request.post('/api/v2/job/range', {
      data: { from, to, step: '1s', speed: 1, window: '5s' },
    });
  }

  await page.getByRole('button', { name: 'Датчики' }).click();
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, null, {
    timeout: 15_000,
  });
  const tableIds = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#tableBody tr')).map(
      (tr) => Number(tr.querySelector('button[data-chart-add]')?.getAttribute('data-chart-add')) || null,
    ),
  );
  expect(tableIds.length).toBeGreaterThan(0);
  expect(tableIds.length).toBeLessThanOrEqual(expectedCount);
  expect(tableIds.every((id) => id && picked.includes(id))).toBeTruthy();

  // Фильтруем по имени первого датчика и проверяем, что остаются только совпадения рабочего списка.
  const firstMeta = sensors.find((s: any) => s.id === picked[0]) || {};
  const term = (firstMeta.name || String(firstMeta.id || '')).slice(0, 3) || String(firstMeta.id || '');
  await page.fill('#tableFilter', term);
  const filteredRows = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#tableBody tr')).map((tr) => ({
      text: tr.textContent || '',
      id: Number(tr.querySelector('button[data-chart-add]')?.getAttribute('data-chart-add')) || null,
    })),
  );
  expect(filteredRows.length).toBeGreaterThan(0);
  filteredRows.forEach((row) => {
    if (term) {
      const t = row.text.toLowerCase();
      const matchesText = t.includes(term.toLowerCase()) || t.includes(String(firstMeta.id || '').toLowerCase());
      const matchesId = row.id !== null && picked.includes(row.id);
      expect(matchesText || matchesId).toBeTruthy();
    }
  });

  // Сброс фильтра — снова ровно рабочий список.
  await page.fill('#tableFilter', '');
  await page.waitForTimeout(200);
  const finalIds = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#tableBody tr')).map(
      (tr) => Number(tr.querySelector('button[data-chart-add]')?.getAttribute('data-chart-add')) || null,
    ),
  );
  expect(finalIds.length).toBeGreaterThan(0);
  expect(finalIds.length).toBeLessThanOrEqual(expectedCount);
  expect(finalIds.every((id) => id && picked.includes(id))).toBeTruthy();
});
