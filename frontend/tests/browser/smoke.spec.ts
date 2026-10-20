import { readFile } from 'node:fs/promises';
import { extname, resolve } from 'node:path';
import { test, expect } from '@playwright/test';

for (const width of [1440, 390]) {
 for (const reducedMotion of ['reduce', 'no-preference'] as const) {
  test(`classic app routes and shared bindings at ${width}px (${reducedMotion})`, async ({ page, request, context }, testInfo) => {
    const errors: string[] = [];
    const writes: string[] = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', msg => { if (['error', 'warning'].includes(msg.type())) errors.push(msg.text()); });
    page.on('requestfailed', req => { if (req.failure()?.errorText !== 'net::ERR_ABORTED') errors.push(`${req.url()}: ${req.failure()?.errorText}`); });
    page.on('response', res => { if (res.status() >= 400) errors.push(`${res.status()} ${res.url()}`); });
    page.on('request', req => { if (!['GET', 'HEAD'].includes(req.method())) writes.push(req.url()); });
    await page.emulateMedia({ reducedMotion });
    await page.setViewportSize({ width, height: 1000 });
    const bundle = await request.get('/app.js?v=0.10.0');
    expect(bundle.status()).toBe(200);
    await page.goto('/');
    await expect(page).toHaveURL(/\/#dashboard$/);
    await expect(page).toHaveTitle('YoLink · 仪表盘');
    await expect(page.locator('#view-dashboard h1')).toHaveText('仪表盘');
    await expect(page.locator('#dash-content')).toHaveAttribute('aria-busy', 'false');
    await expect(page.locator('#dash-content')).toContainText('<Fixture & cloud>');
    await expect(page.locator('#global-error')).toBeHidden();
    await expect(page.locator('#dash-error')).toBeHidden();
    await page.evaluate(() => document.fonts.ready);
    const dashboard = await page.screenshot({ path: testInfo.outputPath('dashboard.png'), fullPage: true, animations: 'disabled' });
    await testInfo.attach('dashboard', { body: dashboard, contentType: 'image/png' });

    // The records entry is in More on mobile. Use the actual visible navigation.
    if (width === 390) await page.locator('button[data-nav="more"]:visible').first().click();
    await page.locator('button[data-nav="records"]:visible').first().click();
    await expect(page).toHaveURL(/#records$/);
    await expect(page).toHaveTitle('YoLink · 运行记录');
    await expect(page.locator('#records-status')).toHaveText('1 条记录');
    await expect(page.locator('#records-table-panel')).toContainText('<Fixture & movie>.mkv');
    await expect(page.locator('#records-error')).toBeHidden();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.evaluate(() => document.fonts.ready);
    const records = await page.screenshot({ path: testInfo.outputPath('records.png'), fullPage: true, animations: 'disabled' });
    await testInfo.attach('records', { body: records, contentType: 'image/png' });

    // Optional migration baseline: run the saved pre-refactor script against the
    // same HTML/CSS/fixtures and compare pixels. No baseline is shipped at runtime.
    if (process.env.YOLINK_BASELINE_WEB || process.env.YOLINK_BASELINE_APP) {
      const baseline = await context.newPage();
      await baseline.setViewportSize({ width, height: 1000 });
      await baseline.emulateMedia({ reducedMotion });
      if (process.env.YOLINK_BASELINE_WEB) {
        const root = process.env.YOLINK_BASELINE_WEB;
        const types: Record<string, string> = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html',
          '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2' };
        await baseline.route(url => url.origin === 'http://127.0.0.1:18797' && !url.pathname.startsWith('/api/'), async route => {
          const path = new URL(route.request().url()).pathname;
          const file = resolve(root, '.' + (path === '/' ? '/index.html' : path));
          await route.fulfill({ body: await readFile(file), contentType: types[extname(file)] ?? 'application/octet-stream' });
        });
      } else {
        const original = await readFile(process.env.YOLINK_BASELINE_APP!, 'utf8');
        await baseline.route('**/app.js?*', route => route.fulfill({ body: original, contentType: 'text/javascript' }));
      }
      await baseline.goto('/');
      await expect(baseline.locator('#dash-content')).toHaveAttribute('aria-busy', 'false');
      await baseline.evaluate(() => document.fonts.ready);
      expect(dashboard.equals(await baseline.screenshot({ path: testInfo.outputPath('baseline-dashboard.png'), fullPage: true, animations: 'disabled' }))).toBe(true);
      if (width === 390) await baseline.locator('button[data-nav="more"]:visible').first().click();
      await baseline.locator('button[data-nav="records"]:visible').first().click();
      await expect(baseline.locator('#records-status')).toHaveText('1 条记录');
      await baseline.evaluate(() => document.fonts.ready);
      expect(records.equals(await baseline.screenshot({ path: testInfo.outputPath('baseline-records.png'), fullPage: true, animations: 'disabled' }))).toBe(true);
      await baseline.close();
    }

    await page.locator('button[data-nav="dashboard"]:visible').first().click();
    await expect(page).toHaveURL(/#dashboard$/);
    await expect(page.locator('#view-dashboard')).toBeVisible();
    await page.goBack();
    await expect(page.locator('#view-records')).toBeVisible();
    await page.goForward();
    await expect(page.locator('#view-dashboard')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await expect(page.locator('vite-error-overlay')).toHaveCount(0);
    expect(errors).toEqual([]);
    expect(writes).toEqual([]);

    // This talks only to the loopback fixture. No upstream backend exists.
    for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
      const blocked = await request.fetch('/api/settings', { method, data: {} });
      expect(blocked.status()).toBe(405);
    }
    const missing = await request.get('/api/not-a-fixture');
    expect(missing.status()).toBe(404);
  });
 }
}
