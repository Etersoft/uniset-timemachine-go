import { test, expect } from '@playwright/test';

test('AI/AO идут на основной график, DI/DO — на дискретный; unknown — на основной', async ({ page }) => {
  await page.request.post('/api/v2/job/reset');
  const sensorsResp = await page.request.get('/api/v2/sensors');
  const sensorsJson = await sensorsResp.json();
  const list: Array<{ id: number; name?: string; iotype?: string; textname?: string }> = sensorsJson?.sensors || [];

  const pickLabel = (s: { id: number; name?: string }) => (s.name ? `${s.name} (${s.id})` : `${s.id}`);

  const analog = list.find((s) => ['AI', 'AO'].includes((s.iotype || '').toUpperCase()));
  const discrete = list.find((s) => ['DI', 'DO'].includes((s.iotype || '').toUpperCase()));
  const unknown = list.find((s) => !s.iotype);

  test.skip(!analog || !discrete, 'Нужны хотя бы один AI/AO и один DI/DO в конфиге');

  const workingIds = [analog?.id, discrete?.id, unknown?.id].filter((v): v is number => Number.isFinite(v));
  if (workingIds.length) {
    await page.request.post('/api/v2/job/sensors', { data: { sensors: workingIds } });
  }

  const analogLabel = pickLabel(analog!);
  const discreteLabel = pickLabel(discrete!);
  const unknownLabel = unknown ? pickLabel(unknown) : null;

  await page.goto('/ui/');
  await page.getByRole('button', { name: 'Датчики' }).click();
  await page.waitForFunction(() => document.querySelectorAll('#tableBody tr').length > 0, { timeout: 8_000 });

  const addBtn = (id: number) => page.locator(`#tableBody button[data-chart-add="${id}"]`);
  await expect(addBtn(analog!.id)).toBeVisible({ timeout: 4_000 });
  await addBtn(analog!.id).click();
  await expect(addBtn(discrete!.id)).toBeVisible({ timeout: 4_000 });
  await addBtn(discrete!.id).click();
  if (unknown) {
    if (await addBtn(unknown.id).isVisible()) {
      await addBtn(unknown.id).click();
    }
  }

  await page.getByRole('button', { name: 'Графики' }).click();
  await page.waitForTimeout(1000);

  const types = await page.evaluate(
    ({ analogId, discreteId, unknownId }) => ({
      analog: (window as any).getSensorType?.(analogId),
      discrete: (window as any).getSensorType?.(discreteId),
      unknown: unknownId ? (window as any).getSensorType?.(unknownId) : null,
    }),
    { analogId: analog!.id, discreteId: discrete!.id, unknownId: unknown?.id ?? null },
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
