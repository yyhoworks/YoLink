import {test,expect} from '@playwright/test';
import {readFile} from 'node:fs/promises';

test('real Go export downloads an encrypted archive and preserves authentication',async({page},testInfo)=>{
 const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
 await page.goto('/#settings');
 await page.locator('#login-form').getByLabel('管理员密码',{exact:true}).fill('fixture-admin-password');
 await page.getByRole('button',{name:'登录',exact:true}).click();
 await expect(page.locator('#backup-status')).toContainText('没有等待处理');
 await page.locator('#backup-export-details summary').click();
 await page.locator('#backup-export-admin').fill('fixture-admin-password');
 await page.locator('#backup-export-password').fill('synthetic-browser-backup-password');
 await page.locator('#backup-export-repeat').fill('synthetic-browser-backup-password');
 const downloaded=page.waitForEvent('download');await page.locator('#backup-export').click();
 const download=await downloaded,archive=await readFile((await download.path())!);
 expect(archive.subarray(0,22).toString()).toBe('age-encryption.org/v1\n');
 expect(archive.includes(Buffer.from('fixture-admin-password'))).toBe(false);
 expect(archive.includes(Buffer.from('fixture-secret'))).toBe(false);
 await expect(page.locator('#backup-export-admin')).toHaveValue('');
 await expect(page.locator('#backup-export')).toBeEnabled();
 await expect(page.locator('#backup-status')).toContainText('没有等待处理');
 for(const width of [1440,390]){
  await page.setViewportSize({width,height:1000});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath(`backup-real-${width}.png`),fullPage:true});
 }
 expect(errors).toEqual([]);
});
