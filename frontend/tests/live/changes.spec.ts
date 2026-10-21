import{test,expect}from '@playwright/test';

test('real Go change history and confirmed repair converge',async({page},testInfo)=>{
 test.skip(process.env.YOLINK_TEST_FIXTURE!=='changes','requires the isolated change fixture');
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('/#maintenance');await page.locator('#login-form').getByLabel('管理员密码',{exact:true}).fill('fixture-admin-password');await page.getByRole('button',{name:'登录',exact:true}).click();
 await expect(page.locator('#changes-list')).toContainText('新观测');
 await page.locator('#changes-rule').selectOption('rule');await expect(page.locator('#changes-run')).not.toHaveValue('');
 await page.evaluate(()=>{location.hash='inventory'});
 await page.getByRole('checkbox',{name:'选择 first.mkv',exact:true}).check();await page.locator('#inventory-repair').click();await expect(page.locator('#change-repair-panel')).toContainText('可恢复 1 项');
 await expect(page.locator('#change-repair-apply')).toBeDisabled();await page.locator('#change-repair-confirm').check();await page.locator('#change-repair-apply').click();await expect(page.locator('#change-repair-panel')).toContainText('已核验完成 1 项');
 for(const width of [1440,390]){await page.setViewportSize({width,height:1000});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath(`changes-real-${width}.png`),fullPage:true})}
 await expect(page.locator('#toast')).toBeHidden({timeout:6000});await page.evaluate(()=>scrollTo(0,Math.max(0,document.querySelector('#change-repair-panel')!.getBoundingClientRect().top+scrollY-105)));await page.screenshot({path:testInfo.outputPath('repair-real-result-390-viewport.png')});
 await page.evaluate(()=>{location.hash='config'});await page.locator('[data-gen-edit="rule"]').first().click();await page.locator('#scene-select').selectOption('music');await page.locator('#scene-apply').click();await expect(page.locator('#gen-rule-enabled')).not.toBeChecked();
 await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath('template-real-390.png'),fullPage:true});await page.getByRole('button',{name:'保存目录规则',exact:true}).click();await expect(page.locator('#gen-rule-form')).toBeHidden();
 expect(errors).toEqual([]);
});
