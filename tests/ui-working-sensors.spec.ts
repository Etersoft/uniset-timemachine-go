import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('working sensors: select existing, reset to all, load from text', async ({ page }) => {
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');

  // Получаем справочник и полный список для проверок.
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensorsJson = await sensorsResp.json();
  const allSensors = (sensorsJson?.sensors ?? []) as Array<{ name: string }>;
  expect(allSensors.length).toBeGreaterThan(0);
  const firstNames = allSensors.slice(0, 3).map(s => s.name).filter((n) => typeof n === 'string' && n.length > 0);
  expect(firstNames.length).toBeGreaterThan(1);
  const nameSecond = allSensors[1]?.name;

  // Установим рабочий список сразу через API, чтобы диалог показывал нужные галочки.
  const namesToSelect = firstNames.slice(0, 2);
  await page.request.post('/api/v2/job/sensors', { data: { sensors: namesToSelect } });

  // Перезагружаем страницу чтобы UI подхватил новый рабочий список
  await page.reload();
  await page.waitForSelector('#workingMeta');
  // Ждём пока метка покажет что рабочий список загружен (не "загружаем...")
  await page.waitForFunction(
    () => {
      const meta = document.querySelector('#workingMeta');
      return meta && meta.textContent && !meta.textContent.includes('загружаем');
    },
    { timeout: 10_000 },
  );

  // Открываем диалог выбора датчиков.
  await page.getByRole('button', { name: 'Загрузить', exact: true }).click();
  await page.waitForSelector('#sensorsDialogBody tr');
  for (const name of namesToSelect) {
    await expect(page.locator(`input.dlg-select[data-sensor="${name}"]`)).toBeChecked();
  }
  await page.click('#sensorsApplyBtn');
  await page.waitForTimeout(400);

  const workingAfterSelect = await (await page.request.get('/api/v2/job/sensors')).json();
  const selectedServer = ((workingAfterSelect?.sensors ?? []) as string[]).sort();
  expect(selectedServer).toEqual([...namesToSelect].sort());

  // Сбрасываем на все.
  await page.getByRole('button', { name: 'Загрузить', exact: true }).click();
  await page.click('#sensorsResetBtn');
  await page.waitForTimeout(400);
  const workingAfterReset = await (await page.request.get('/api/v2/job/sensors')).json();
  const afterResetNames = (workingAfterReset?.sensors ?? []) as string[];
  expect(afterResetNames.length).toBeGreaterThan(selectedServer.length);
  expect(afterResetNames.length).toBeGreaterThanOrEqual(allSensors.length);

  // Загрузка через текстовую область (name + неизвестный).
  await page.getByRole('button', { name: 'Загрузить', exact: true }).click();
  await page.getByRole('button', { name: 'Загрузить из файла' }).click().catch(() => {}); // таб через data-sensortab
  // Переключение на вкладку файла
  await page.locator('[data-sensortab="file"]').click();
  const textPayload = `${firstNames[0]}\nunknown_sensor_name\n${nameSecond}\n`;
  await page.fill('#sensorsFileArea', textPayload);
  await page.click('#sensorsApplyBtn');
  await page.waitForTimeout(400);
  const workingAfterFile = await (await page.request.get('/api/v2/job/sensors')).json();
  const afterFileNames = new Set((workingAfterFile?.sensors ?? []) as string[]);
  expect(afterFileNames.has(firstNames[0])).toBeTruthy();
  expect(afterFileNames.has(nameSecond)).toBeTruthy();
});
