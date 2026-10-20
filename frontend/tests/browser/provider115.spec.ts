import { test, expect } from '@playwright/test';

for(const width of [1440,390]){
 test(`115 Cookie account form saves without echoing credentials at ${width}px`,async({page,request})=>{
  const state=await(await request.get('/api/state')).json();
  const cookie='UID=115-fixture; CID=fixture-cid; SEID=fixture-seid';
  let saved:Record<string,unknown>|undefined;
  await page.route(url=>url.pathname==='/api/state',route=>route.fulfill({json:saved?{...state,accounts:[...state.accounts,{id:'115-fixture',name:'115 合成账号',provider:'115',root:'0',disabled:false,allow_proxy:false}]}:state}));
  await page.route(url=>url.pathname==='/api/accounts',async route=>{
   saved=route.request().postDataJSON();
   await route.fulfill({json:{id:'115-fixture',name:'115 合成账号',provider:'115',root:'0',disabled:false,allow_proxy:false}});
  });
  await page.setViewportSize({width,height:1000});
  await page.goto('/#accounts');
  await page.locator('#cloud-add').click();
  await page.locator('#provider').selectOption('115');
  await expect(page.locator('[data-secret="cookie"]')).toHaveAttribute('type','password');
  await expect(page.locator('#drive-field')).toBeHidden();
  await expect(page.locator('#proxy-field')).toBeVisible();
  await expect(page.locator('#allow-proxy')).not.toBeChecked();
  await expect(page.locator('#provider-note')).toContainText('原文件 302');
  await page.locator('#account-name').fill('115 合成账号');
  await page.locator('[data-secret="cookie"]').fill(cookie);
  await page.locator('#save-account').click();
  await expect(page.locator('#account-form')).toBeHidden();
  await expect(page.locator('.cloud-card').filter({hasText:'115 合成账号'})).toContainText('115 网盘');
  expect(saved).toMatchObject({provider:'115',allow_proxy:false,secrets:{cookie}});
  await expect(page.locator('[data-secret="cookie"]')).toHaveValue('');
  expect(await page.locator('body').innerText()).not.toContain('fixture-seid');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 });
}
