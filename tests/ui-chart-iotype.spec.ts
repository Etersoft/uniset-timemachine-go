import { test, expect } from '@playwright/test';
import { gotoWithSession } from './utils';

test('AI/AO идут на основной график, DI/DO — на дискретный; unknown — на основной', async ({ page }) => {
  await gotoWithSession(page);
  await page.request.post('/api/v2/job/reset');
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensorsJson = await sensorsResp.json();
  const list: Array<{ name: string; iotype?: string; textname?: string }> = sensorsJson?.sensors || [];

  const pickLabel = (s: { name: string }) => s.name;

  const analog = list.find((s) => ['AI', 'AO'].includes((s.iotype || '').toUpperCase()));
  const discrete = list.find((s) => ['DI', 'DO'].includes((s.iotype || '').toUpperCase()));
  const unknown = list.find((s) => !s.iotype);

  test.skip(!analog || !discrete, 'Нужны хотя бы один AI/AO и один DI/DO в конфиге');

  const workingNames = [analog?.name, discrete?.name, unknown?.name].filter((v): v is string => typeof v === 'string' && v.length > 0);
  if (workingNames.length) {
    await page.request.post('/api/v2/job/sensors', { data: { sensors: workingNames } });
  }

  const analogLabel = pickLabel(analog!);
  const discreteLabel = pickLabel(discrete!);
  const unknownLabel = unknown ? pickLabel(unknown) : null;

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

  await page.getByRole('button', { name: 'Датчики' }).click();
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, { timeout: 8_000 });

  const addBtn = (name: string) => page.locator(`#tableBody button[data-chart-add="${name}"]`);
  await expect(addBtn(analog!.name)).toBeVisible({ timeout: 4_000 });
  await addBtn(analog!.name).click();
  await expect(addBtn(discrete!.name)).toBeVisible({ timeout: 4_000 });
  await addBtn(discrete!.name).click();
  if (unknown) {
    if (await addBtn(unknown.name).isVisible()) {
      await addBtn(unknown.name).click();
    }
  }

  await page.getByRole('button', { name: 'Графики' }).click();
  await page.waitForTimeout(1000);

  const types = await page.evaluate(
    ({ analogName, discreteName, unknownName }) => ({
      analog: (window as any).getSensorType?.(analogName),
      discrete: (window as any).getSensorType?.(discreteName),
      unknown: unknownName ? (window as any).getSensorType?.(unknownName) : null,
    }),
    { analogName: analog!.name, discreteName: discrete!.name, unknownName: unknown?.name ?? null },
  );

  const { main, step, legend, builtStep, stepExists, stepCanvasExists } = await page.evaluate(() => {
    // @ts-ignore Chart is global from chart.umd
    const mainChart = Chart.getChart(document.getElementById('chartCanvas'));
    // @ts-ignore Chart is global
    const stepCanvas = document.getElementById('chartCanvasStep');
    // @ts-ignore Chart is global
    const stepChart = Chart.getChart(stepCanvas);
    return {
      main: mainChart?.data.datasets.map((d) => d.label) || [],
      step: stepChart?.data.datasets.map((d) => d.label) || [],
      legend: Array.from(document.querySelectorAll('#chartLegendBody tr')).map((tr) =>
        tr.textContent?.trim() || '',
      ),
      builtStep: (window as any).buildStepDatasets
        ? (window as any)
            .buildStepDatasets()
            .map((d: any) => ({ label: d.label, dataLen: (d.data || []).length }))
        : [],
      stepExists: !!stepChart,
      stepCanvasExists: !!stepCanvas,
    };
  });

  test.info().annotations.push({
    type: 'debug',
    description: `types=${JSON.stringify(types)} labels main=${JSON.stringify(main)} step=${JSON.stringify(step)} legend=${JSON.stringify(legend)} builtStep=${JSON.stringify(builtStep)} stepExists=${stepExists}`,
  });

  expect(main).toContain(analogLabel);
  expect(step).not.toContain(analogLabel);
  if (!step.includes(discreteLabel)) {
    throw new Error(
      `discrete label missing: ${discreteLabel}; types=${JSON.stringify(types)} main=${JSON.stringify(main)} step=${JSON.stringify(step)} legend=${JSON.stringify(legend)} builtStep=${JSON.stringify(builtStep)} stepExists=${stepExists} stepCanvas=${stepCanvasExists}`,
    );
  }
  expect(main).not.toContain(discreteLabel);
  if (unknownLabel) {
    expect(main).toContain(unknownLabel);
  }
});
