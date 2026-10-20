import { test, expect } from '@playwright/test';

test('dashboard retains upstream timing, escaped resolve errors and record navigation',async({page,request})=>{
 const response=await request.get('/api/dashboard');
 const data=await response.json();
 await page.route(url=>url.pathname==='/api/dashboard',route=>route.fulfill({json:{...data,stats:{...data.stats,upstream_ms:18000,upstream_count:289},errors:[{name:'<synthetic & media>.mkv',error:'<upstream failure>',time:1792382400000}]}}));
 await page.goto('/');
 await page.locator('#bw-direct-detail-summary').click();
 await expect(page.locator('#bw-direct-detail')).toContainText('62 ms');
 await expect(page.locator('#bw-direct-detail')).toContainText('<synthetic & media>.mkv');
 await expect(page.locator('#bw-direct-detail')).toContainText('<upstream failure>');
 await page.locator('#bw-direct-detail [data-dashboard-record="direct"]').click();
 await expect(page.locator('#view-records')).toBeVisible();
});

for(const width of [1440,390]){
 test(`appearance axes and local wallpaper persist under production CSP at ${width}px`,async({page})=>{
  const errors:string[]=[],writes:string[]=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('console',m=>{if(m.type()==='error')errors.push(m.text())});
  page.on('request',r=>{if(!['GET','HEAD'].includes(r.method()))writes.push(r.url())});
  await page.setViewportSize({width,height:1000});
  await page.goto('/');
  await expect(page.locator('#dash-content')).toHaveAttribute('aria-busy','false');
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await expect(page.locator('[data-style-choice="original"]')).toHaveAttribute('aria-pressed','true');
  await page.getByRole('button',{name:'云雾冰蓝',exact:true}).click();
  await page.locator('[data-style-choice="glass"]').click();
  await expect(page.locator('html')).toHaveAttribute('data-palette','cream');
  await expect(page.locator('html')).toHaveAttribute('data-style','glass');
  await expect(page.getByRole('button',{name:'米白黑黄',exact:true})).toHaveCount(0);
  await expect(page.locator('.skin-palettes')).toBeHidden();
  await expect(page.locator('html')).toHaveAttribute('data-palette','cream');
  expect(await page.locator('.bw-card').first().evaluate(el=>getComputedStyle(el).backdropFilter)).toContain('blur');
  await page.locator('[data-style-choice="original"]').click();
  await expect(page.locator('html')).toHaveAttribute('data-palette','blue');
  await expect(page.locator('.glass-controls')).toBeHidden();
  const input=page.locator('#wallpaper-file');
  await input.setInputFiles({name:'wallpaper.png',mimeType:'image/png',buffer:Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aXioAAAAASUVORK5CYII=','base64')});
  await expect(page.locator('.wallpaper-name')).toHaveText('wallpaper.png');
  await expect(page.locator('.skin-status')).toHaveText('已保存到当前浏览器。');
  await page.getByRole('slider',{name:'壁纸模糊',exact:true}).press('ArrowRight');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-style','original');
  await expect(page.locator('html')).toHaveAttribute('data-palette','blue');
  await expect(page.locator('html')).toHaveAttribute('data-wallpaper','');
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.locator('[data-style-choice="glass"]').click();
  await expect(page.locator('.skin-palettes')).toBeHidden();
  await expect(page.locator('html')).toHaveAttribute('data-palette','cream');
  await page.locator('[data-style-choice="original"]').click();
  await expect(page.getByRole('button',{name:'云雾冰蓝',exact:true})).toHaveAttribute('aria-pressed','true');
  await expect(page.locator('.wallpaper-name')).toHaveText('wallpaper.png');
  await expect(page.locator('[data-pref="wallBlur"]')).toHaveValue('1');
  await input.setInputFiles({name:'broken.png',mimeType:'image/png',buffer:Buffer.from('not an image')});
  await expect(page.locator('.skin-status')).toContainText('无法读取');
  await expect(page.locator('.wallpaper-name')).toHaveText('wallpaper.png');
  await page.getByRole('button',{name:'恢复默认背景',exact:true}).click();
  await expect(page.locator('html')).not.toHaveAttribute('data-wallpaper','');
  await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
  if(width===390)await page.locator('.sidebar [data-nav="more"]').click();
  await page.locator('button[data-nav="records"]:visible').first().click();
  await expect(page.locator('#records-status')).toHaveText('1 条记录');
  await expect(page.locator('html')).toHaveAttribute('data-palette','blue');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  expect(writes).toEqual([]);
  expect(errors).toEqual([]);
 });
}
