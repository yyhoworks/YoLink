import { defineConfig } from '@playwright/test';

// Browser channel: Edge by default. Set PLAYWRIGHT_CHANNEL to another
// channel, or to an empty string to use Playwright's bundled Chromium.
const channel = process.env.PLAYWRIGHT_CHANNEL ?? 'msedge';

export default defineConfig({
  testDir: './tests/browser',
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:18797',
    browserName: 'chromium',
    ...(channel ? { channel } : {}),
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    reducedMotion: 'reduce',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'node scripts/preview.mjs',
    url: 'http://127.0.0.1:18797/',
    reuseExistingServer: false,
    timeout: 15000,
  },
});
