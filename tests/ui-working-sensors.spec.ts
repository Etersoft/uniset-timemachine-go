import { test, expect } from '@playwright/test';

test('working sensors: select existing, reset to all, load from text', async ({ page }) => {
  await page.request.post('/api/v2/job/reset');

  // Получаем справочник и полный список для проверок.
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensorsJson = await sensorsResp.json();
  const allSensors = (sensorsJson?.sensors ?? []) as Array<{ id: number; name: string }>;
  expect(allSensors.length).toBeGreaterThan(0);
  const firstIds = allSensors.slice(0, 3).map(s => s.id).filter((id) => Number.isFinite(id));
  expect(firstIds.length).toBeGreaterThan(1);
  const nameSecond = allSensors[1]?.name || String(allSensors[1]?.id);

  // Установим рабочий список сразу через API, чтобы диалог показывал нужные галочки.
  await page.request.post('/api/v2/job/sensors', { data: { sensors: firstIds.slice(0, 2) } });

  await page.goto('/ui/');
  await page.waitForSelector('#workingMeta');

  // Открываем диалог выбора датчиков.
  await page.getByRole('button', { name: 'Загрузить' }).click();
  await page.waitForSelector('#sensorsDialogBody tr');

  // Проверяем, что отмечены нужные два датчика.
  const idsToSelect = firstIds.slice(0, 2);
  for (const id of idsToSelect) {
    await expect(page.locator(`input.dlg-select[data-sensor="${id}"]`)).toBeChecked();
  }
  await page.click('#sensorsApplyBtn');
  await page.waitForTimeout(400);

  const workingAfterSelect = await (await page.request.get('/api/v2/job/sensors')).json();
  const selectedServer = (workingAfterSelect?.sensors ?? []).map(Number).sort((a: number, b: number) => a - b);
  expect(selectedServer).toEqual(idsToSelect.sort((a, b) => a - b));

  // Сбрасываем на все.
  await page.getByRole('button', { name: 'Загрузить' }).click();
  await page.click('#sensorsResetBtn');
  await page.waitForTimeout(400);
  const workingAfterReset = await (await page.request.get('/api/v2/job/sensors')).json();
  const afterResetIds = (workingAfterReset?.sensors ?? []) as number[];
  expect(afterResetIds.length).toBeGreaterThan(selectedServer.length);
  expect(afterResetIds.length).toBeGreaterThanOrEqual(allSensors.length);

  // Загрузка через текстовую область (id + name + неизвестный).
  await page.getByRole('button', { name: 'Загрузить' }).click();
  await page.getByRole('button', { name: 'Загрузить из файла' }).click().catch(() => {}); // таб через data-sensortab
  // Переключение на вкладку файла
  await page.locator('[data-sensortab="file"]').click();
  const textPayload = `${firstIds[0]}\nunknown_sensor_name\n${nameSecond}\n`;
  await page.fill('#sensorsFileArea', textPayload);
  await page.click('#sensorsApplyBtn');
  await page.waitForTimeout(400);
  const workingAfterFile = await (await page.request.get('/api/v2/job/sensors')).json();
  const afterFileIds = new Set((workingAfterFile?.sensors ?? []).map(Number));
  expect(afterFileIds.has(firstIds[0])).toBeTruthy();
  expect(afterFileIds.has(allSensors[1].id)).toBeTruthy();
});
