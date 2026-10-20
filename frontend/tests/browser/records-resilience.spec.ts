import { test, expect, type Page, type Route } from '@playwright/test';

const listPath = (url: URL) => url.pathname === '/api/records';
const tracePath = (url: URL) => url.pathname === '/api/records/trace';
const listData = (name: string) => ({
  items: [{ id: 'qa', media: 'qa-media', name, kind: 'generation', event: 'strm', outcome: 'created', time: 1792382400000 }],
  page: 1, pages: 1, total: 1,
});
const openRecords = async (page: Page) => {
  await page.goto('/#records');
  await expect(page.locator('#view-records')).toBeVisible();
};
const flushRendering = (page: Page) => page.evaluate(() => new Promise<void>(resolve =>
  requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
const deferredRoute = () => {
  let resolve!: (route: Route) => void;
  const promise = new Promise<Route>(done => { resolve = done; });
  return { promise, resolve };
};

let pageErrors: string[];
let abortedPaths: string[];
test.beforeEach(async ({ page }) => {
  pageErrors = [];
  abortedPaths = [];
  page.on('pageerror', error => pageErrors.push(error.message));
  page.on('console', message => {
    if (message.text().startsWith('qa:abort:')) abortedPaths.push(message.text().slice('qa:abort:'.length));
  });
  // Observe signals at the browser fetch seam, without changing request behavior.
  await page.addInitScript(() => {
    const original = window.fetch;
    window.fetch = (input, options) => {
      options?.signal?.addEventListener('abort', () => console.debug('qa:abort:' + String(input)), { once: true });
      return original(input, options);
    };
  });
});
test.afterEach(() => expect(pageErrors).toEqual([]));

for (const failure of [
  { name: 'non-JSON 502', status: 502, text: '<html>upstream error</html>', expected: 'HTTP 502' },
  { name: 'invalid success JSON', status: 200, text: '<html>wrong success</html>', expected: '无效的 JSON' },
]) {
  test(`record list reports ${failure.name} and recovers on refresh`, async ({ page }) => {
    let calls = 0;
    await page.route(listPath, route => ++calls === 1
      ? route.fulfill({ status: failure.status, contentType: 'text/html', body: failure.text }) : route.continue());
    await openRecords(page);
    await expect(page.locator('#records-error')).toContainText(failure.expected);
    await expect(page.locator('#records-table-panel')).not.toHaveAttribute('aria-busy');
    await expect(page.locator('#login')).toBeHidden();
    await page.locator('#records-refresh').click();
    await expect(page.locator('#records-status')).toHaveText('1 条记录');
    await expect(page.locator('#records-error')).toBeHidden();
    expect(calls).toBe(2);
  });
}

test('empty results can be refreshed without an error or stuck pagination', async ({ page }) => {
  let calls = 0;
  await page.route(listPath, route => ++calls === 1
    ? route.fulfill({ json: { items: [], total: 0, page: 1, pages: 0 } }) : route.continue());
  await openRecords(page);
  await expect(page.locator('#records-status')).toHaveText('0 条记录');
  await expect(page.locator('#records-body')).toContainText('没有符合条件');
  await expect(page.locator('#records-prev')).toBeDisabled();
  await expect(page.locator('#records-next')).toBeDisabled();
  await expect(page.locator('#records-error')).toBeHidden();
  await page.locator('#records-refresh').click();
  await expect(page.locator('#records-status')).toHaveText('1 条记录');
});

test('a network failure leaves the list retryable', async ({ page }) => {
  let calls = 0;
  await page.route(listPath, route => ++calls === 1 ? route.abort('connectionfailed') : route.continue());
  await openRecords(page);
  await expect(page.locator('#records-error')).toContainText('网络连接失败');
  await page.locator('#records-refresh').click();
  await expect(page.locator('#records-error')).toBeHidden();
  await expect(page.locator('#records-status')).toHaveText('1 条记录');
});

test('new filters abort the pending list and late results cannot replace fresh data', async ({ page }) => {
  const old = deferredRoute();
  let calls = 0;
  await page.route(listPath, route => {
    if (++calls === 1) old.resolve(route);
    else return route.fulfill({ json: listData('fresh result') });
  });
  await openRecords(page);
  const oldRoute = await old.promise;
  await expect(page.locator('#records-table-panel')).toHaveAttribute('aria-busy', 'true');
  await page.locator('#records-search').fill('fresh');
  await page.locator('#records-filter-form button[type="submit"]').click();
  await expect(page.locator('#records-body')).toContainText('fresh result');
  await expect.poll(() => abortedPaths.filter(path => path.startsWith('/api/records?')).length).toBe(1);
  await oldRoute.fulfill({ json: listData('stale result') });
  await flushRendering(page);
  await expect(page.locator('#records-body')).toContainText('fresh result');
  await expect(page.locator('#records-body')).not.toContainText('stale result');
  await expect(page.locator('#records-error')).toBeHidden();
});

test('leaving and reentering cancels the old request and does not accumulate polling', async ({ page }) => {
  await page.clock.install();
  const old = deferredRoute();
  let calls = 0;
  await page.route(listPath, route => {
    if (++calls === 1) old.resolve(route);
    else return route.continue();
  });
  await openRecords(page);
  const oldRoute = await old.promise;
  await page.clock.runFor(20_000);
  expect(calls).toBe(1); // polling must not restart an already pending request
  await page.locator('button[data-nav="dashboard"]:visible').first().click();
  await expect.poll(() => abortedPaths.filter(path => path.startsWith('/api/records?')).length).toBe(1);
  await oldRoute.fulfill({ status: 500, body: 'late failure' });
  await page.clock.runFor(30_000);
  expect(calls).toBe(1);
  await expect(page.locator('#records-error')).toBeHidden();
  await page.locator('button[data-nav="records"]:visible').first().click();
  await expect(page.locator('#records-status')).toHaveText('1 条记录');
  expect(calls).toBe(2);
  await page.clock.runFor(10_000);
  await expect.poll(() => calls).toBe(3); // exactly one polling interval after reentry
});

test('closing details cancels the trace; reopening still works', async ({ page }) => {
  const old = deferredRoute();
  let calls = 0;
  await page.route(tracePath, route => {
    if (++calls === 1) old.resolve(route);
    else return route.continue();
  });
  await openRecords(page);
  await page.locator('[data-record-media="fixture-media"]').click();
  const oldRoute = await old.promise;
  await expect(page.locator('#records-detail')).toHaveAttribute('aria-busy', 'true');
  await page.locator('#records-detail-close').click();
  await expect.poll(() => abortedPaths.filter(path => path.startsWith('/api/records/trace?')).length).toBe(1);
  await oldRoute.fulfill({ status: 502, body: 'old detail failed' });
  await flushRendering(page);
  await expect(page.locator('#records-detail')).toBeHidden();
  await expect(page.locator('#records-trace-body')).not.toContainText('HTTP 502');
  await page.locator('[data-record-media="fixture-media"]').click();
  await expect(page.locator('#records-detail-name')).toHaveText('<Fixture & movie>.mkv');
  await expect(page.locator('#records-detail')).not.toHaveAttribute('aria-busy');
});

test('grouped playback details still render through the same trace lifecycle', async ({ page }) => {
  await page.route(listPath, route => route.fulfill({ json: {
    ...listData('grouped movie'), request_total: 1,
    items: [{ ...listData('grouped movie').items[0], group_id: 'qa-group', kind: 'playback', event: 'play', outcome: 'redirected' }],
  } }));
  await page.route(tracePath, route => route.fulfill({ json: {
    playback: [{ name: 'group detail', event: 'play', outcome: 'redirected', method: 'GET', time: 1792382400000, millis: 10 }],
    direct: [], total: 1, page: 1, pages: 1,
  } }));
  await openRecords(page);
  await page.locator('#records-playback-tab').click();
  await page.locator('[data-record-media="qa-media"]').click();
  await expect(page.locator('#records-detail-name')).toHaveText('group detail');
  await expect(page.locator('#records-trace-body')).toContainText('共 1 次原始请求');
  await expect(page.locator('#records-detail')).not.toHaveAttribute('aria-busy');
  await page.locator('#records-detail-close').click();
  await expect(page.locator('#records-detail')).toBeHidden();
});

test('notification deliveries finish loading both their list and follow-up data', async ({ page }) => {
  await openRecords(page);
  await page.locator('#records-deliveries-tab').click();
  await expect(page.locator('#records-status')).toHaveText('0 条匹配 · 最近 0 条投递');
  await expect(page.locator('#records-table-panel')).not.toHaveAttribute('aria-busy');
  await expect(page.locator('#records-body')).toContainText('暂无匹配的投递记录');
  await expect(page.locator('#automation-error')).toBeHidden();
  await expect(page.locator('#records-error')).toBeHidden();
});

test('switching record tabs cancels pending notification deliveries', async ({ page }) => {
  const pending = deferredRoute();
  await page.route(url => url.pathname === '/api/integrations', route => pending.resolve(route));
  await openRecords(page);
  await page.locator('#records-deliveries-tab').click();
  const oldRoute = await pending.promise;
  await page.locator('#records-generation-tab').click();
  await expect(page.locator('#records-status')).toHaveText('1 条记录');
  await expect.poll(() => abortedPaths.filter(path => path === '/api/integrations').length).toBe(1);
  await oldRoute.fulfill({ status: 500, body: 'stale delivery error' });
  await flushRendering(page);
  await expect(page.locator('#records-error')).toBeHidden();
  await expect(page.locator('#records-status')).toHaveText('1 条记录');
});

test('leaving during the delivery follow-up also cancels automation loading', async ({ page }) => {
  const pending = deferredRoute();
  await page.route(url => url.pathname === '/api/automation', route => pending.resolve(route));
  await openRecords(page);
  await page.locator('#records-deliveries-tab').click();
  const oldRoute = await pending.promise;
  await page.locator('button[data-nav="dashboard"]:visible').first().click();
  await expect.poll(() => abortedPaths.filter(path => path === '/api/automation').length).toBe(1);
  await oldRoute.fulfill({ status: 502, body: 'late automation error' });
  await flushRendering(page);
  await expect(page.locator('#automation-error')).toBeHidden();
  await expect(page.locator('#records-error')).toBeHidden();
});

test('a plain-text 401 restores login and stops record polling', async ({ page }) => {
  await page.clock.install();
  let calls = 0;
  await page.route(listPath, route => {
    calls++;
    return route.fulfill({ status: 401, contentType: 'text/plain', body: 'session expired' });
  });
  await page.goto('/#records');
  await expect(page.locator('#login')).toBeVisible();
  await expect(page.locator('#app')).toBeHidden();
  await expect.poll(() => calls).toBe(1);
  await page.clock.runFor(30_000);
  expect(calls).toBe(1);
  await expect(page.locator('#records-error')).toBeHidden();
});
