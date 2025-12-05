import { defineConfig, devices } from '@playwright/test';

// Тесты с playback операциями (start/stop/pause)
const playbackTests = [
  'ui-control-inputs',
  'ui-http-api',
  'ui-indicators',
  'ui-log-panel',
  'ui-play-after-stop',
  'ui-position-controls',
  'ui-precise-time-dialog',  // использует seek
  'ui-range-loop',
  'ui-range-play',
  'ui-range-steps',
  'ui-ws-failure',
  'ui-ws',
];
const playbackPattern = new RegExp(`(${playbackTests.join('|')})\\.spec\\.ts$`);

// Конфигурация для single-server режима: с группировкой
const singleServerConfig = defineConfig({
  testDir: '.',
  timeout: 10_000,
  fullyParallel: true,
  workers: 2,
  retries: 1,  // Один ретрай для flaky тестов
  use: {
    baseURL: process.env.BASE_URL || 'http://127.0.0.1:9090',
    headless: true,
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    // UI-only тесты без playback - могут идти параллельно
    {
      name: 'parallel',
      testIgnore: [
        playbackPattern,
        /ui-session.*\.spec\.ts/,
        /ui-working.*\.spec\.ts/,
      ],
      use: { ...devices['Desktop Chrome'] },
    },
    // Тесты с playback - последовательно после parallel
    {
      name: 'serial-playback',
      testMatch: playbackPattern,
      fullyParallel: false,
      dependencies: ['parallel'],
      use: { ...devices['Desktop Chrome'] },
    },
    // Тесты сессий - после playback
    {
      name: 'serial-session',
      testMatch: /ui-session.*\.spec\.ts/,
      fullyParallel: false,
      dependencies: ['serial-playback'],
      use: { ...devices['Desktop Chrome'] },
    },
    // Тесты рабочего списка - после session
    {
      name: 'serial-working',
      testMatch: /ui-working.*\.spec\.ts/,
      fullyParallel: false,
      dependencies: ['serial-session'],
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});

// Экспортируем конфигурацию
// В multi-server режиме sharding управляется через bash в docker-compose
// Каждый shard получает свой BASE_URL через окружение
export default singleServerConfig;
