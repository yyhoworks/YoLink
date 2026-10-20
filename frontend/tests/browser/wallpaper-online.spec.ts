import { test, expect } from '@playwright/test';

const image=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aXioAAAAASUVORK5CYII=','base64');
const online='https://wallpaper.example.test/image.png';

for(const width of [1440,390]){
 test(`online wallpaper loads, persists and resets under CSP at ${width}px`,async({page},testInfo)=>{
  const errors:string[]=[],writes:string[]=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(!['GET','HEAD'].includes(r.method()))writes.push(r.url())});
  await page.route('https://wallpaper.example.test/**',async route=>{
   expect(route.request().headers()['referer']).toBeUndefined();
   await route.fulfill({contentType:'image/png',body:image});
  });
  await page.setViewportSize({width,height:1000});
  const response=await page.goto('/');
  expect(response?.headers()['content-security-policy']).toContain("img-src 'self' data: https:");
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.getByLabel('在线图片网址',{exact:true}).fill(online);
  await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
  await expect(page.locator('#wallpaper-online-status')).toContainText('网址已保存');
  await expect(page.locator('html')).toHaveAttribute('data-wallpaper','');
  await expect(page.locator('.wallpaper-name')).toHaveText('在线壁纸 · wallpaper.example.test');
  expect(await page.locator('body').evaluate(el=>getComputedStyle(el,'::before').backgroundImage)).toContain(online);
  await page.locator('[data-pref="fit"]').selectOption('contain');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-wallpaper','');
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await expect(page.locator('#wallpaper-url')).toHaveValue(online);
  await expect(page.locator('[data-pref="fit"]')).toHaveValue('contain');
  await page.locator('.wallpaper-online').scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('online-wallpaper.png')});
  await page.getByRole('button',{name:'恢复默认背景',exact:true}).click();
  await page.reload();
  await expect(page.locator('html')).not.toHaveAttribute('data-wallpaper','');
  expect(errors).toEqual([]);expect(writes).toEqual([]);
 });

 test(`rule save bar follows glass and original palettes at ${width}px`,async({page},testInfo)=>{
  await page.route(online,route=>route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="1440" height="1000"><rect width="1440" height="1000" fill="#20303b"/></svg>'}));
  await page.setViewportSize({width,height:1000});
  await page.goto('/#config');
  await page.locator('#gen-add-rule').click();
  await expect(page.locator('#gen-rule-form')).toBeVisible();
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.locator('#wallpaper-url').fill(online);
  await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
  await expect(page.locator('#wallpaper-online-status')).toContainText('网址已保存');
  await page.locator('[data-style-choice="glass"]').click();
  await page.locator('[data-glass-preset="dark-clear"]').click();
  await page.locator('[data-pref="glassText"]').selectOption('light');
  await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
  const bar=page.locator('#gen-rule-form>.form-actions');
  await bar.scrollIntoViewIfNeeded();
  expect(await bar.evaluate(el=>getComputedStyle(el).backgroundColor)).toBe('rgba(23, 33, 43, 0.16)');
  expect(await bar.evaluate(el=>getComputedStyle(el).backdropFilter)).toContain('blur');
  const save=bar.getByRole('button',{name:'保存目录规则',exact:true});
  await expect(save).toBeVisible();await save.focus();await expect(save).toBeFocused();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('glass-rule-save-bar.png')});
  await page.emulateMedia({contrast:'more'});
  expect(await bar.evaluate(el=>getComputedStyle(el).backgroundColor)).toBe('rgb(23, 33, 43)');
  expect(await bar.evaluate(el=>getComputedStyle(el).backdropFilter)).toBe('none');
  await page.emulateMedia({contrast:'no-preference'});
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.locator('[data-style-choice="original"]').click();
  await page.getByRole('button',{name:'午夜琥珀',exact:true}).click();
  await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
  const colors=await bar.evaluate(el=>({bar:getComputedStyle(el).backgroundColor,panel:getComputedStyle(el.parentElement!).backgroundColor}));
  expect(colors.bar).toBe(colors.panel);
  expect(colors.bar).not.toBe('rgb(255, 255, 255)');
 });
}

test('online wallpaper validates URLs and keeps local image on failure',async({page})=>{
 await page.route('https://wallpaper.example.test/**',route=>route.fulfill({contentType:'text/html',body:'not an image'}));
 await page.goto('/');
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 await page.locator('#wallpaper-file').setInputFiles({name:'local.png',mimeType:'image/png',buffer:image});
 await expect(page.locator('.wallpaper-name')).toHaveText('local.png');
 for(const url of ['http://example.com/img.png','javascript:alert(1)','https://user:password@example.com/img.png','https://example.com/'.padEnd(4100,'a')]){
  // Set an overlong value programmatically to exercise validation past maxlength.
  await page.locator('#wallpaper-url').evaluate((el,value)=>{(el as HTMLInputElement).value=value;},url);
  await page.locator('.wallpaper-online').evaluate(el=>el.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));
  await expect(page.locator('#wallpaper-online-status')).toContainText('请输入完整的 HTTPS');
  await expect(page.locator('.wallpaper-name')).toHaveText('local.png');
 }
 await page.locator('#wallpaper-url').fill(online);
 await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
 await expect(page.locator('#wallpaper-online-status')).toContainText('原壁纸已保留');
 await expect(page.locator('.wallpaper-name')).toHaveText('local.png');
 expect(await page.evaluate(()=>JSON.parse(localStorage.getItem('yolink-appearance-v1')!).wallpaper)).toMatch(/^data:image/);
});

test('online wallpaper timeout preserves the previous image and unlocks controls',async({page})=>{
 await page.route(online,()=>{});
 await page.goto('/');
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 await page.locator('#wallpaper-file').setInputFiles({name:'local.png',mimeType:'image/png',buffer:image});
 await expect(page.locator('.wallpaper-name')).toHaveText('local.png');
 await page.clock.install();
 await page.locator('#wallpaper-url').fill(online);
 await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
 await expect(page.locator('.wallpaper-online')).toHaveAttribute('aria-busy','true');
 await page.clock.fastForward(15001);
 await expect(page.locator('#wallpaper-online-status')).toContainText('图片加载超时');
 await expect(page.locator('.wallpaper-name')).toHaveText('local.png');
 await expect(page.locator('[data-wallpaper-online]')).toBeEnabled();
 await expect(page.locator('[data-wallpaper-upload]')).toBeEnabled();
});

test('online wallpaper storage failure reports preview only and local upload still replaces it',async({page})=>{
 await page.route(online,route=>route.fulfill({contentType:'image/png',body:image}));
 await page.goto('/');
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 await page.evaluate(()=>{Storage.prototype.setItem=()=>{throw new DOMException('Quota exceeded','QuotaExceededError');};});
 await page.locator('#wallpaper-url').fill(online);
 await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
 await expect(page.locator('#wallpaper-online-status')).toContainText('无法保存网址');
 await expect(page.locator('html')).toHaveAttribute('data-wallpaper','');
 await page.locator('#wallpaper-file').setInputFiles({name:'replacement.png',mimeType:'image/png',buffer:image});
 await expect(page.locator('.wallpaper-name')).toHaveText('replacement.png');
 await expect(page.locator('#wallpaper-url')).toHaveValue('');
});

test('unreachable saved wallpaper falls back and can be cleared',async({page})=>{
 await page.addInitScript(url=>{if(!localStorage.getItem('seeded')){localStorage.setItem('seeded','1');localStorage.setItem('yolink-appearance-v1',JSON.stringify({wallpaper:url}));}},online);
 await page.route('https://wallpaper.example.test/**',route=>route.abort());
 await page.goto('/');
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 await expect(page.locator('#wallpaper-online-status')).toContainText('当前使用默认背景');
 await expect(page.locator('html')).not.toHaveAttribute('data-wallpaper','');
 await page.getByRole('button',{name:'恢复默认背景',exact:true}).click();
 await page.reload();
 await expect(page.locator('#wallpaper-online-status')).toBeEmpty();
 expect(await page.evaluate(()=>JSON.parse(localStorage.getItem('yolink-appearance-v1')!).wallpaper)).toBe('');
});
