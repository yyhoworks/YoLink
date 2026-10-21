import {test,expect,type Page} from '@playwright/test';

const id='0123456789abcdef0123456789abcdef',revision='a'.repeat(64),generation='b'.repeat(32);
const candidate={id,revision,created:1792548000,validation:{schema:4,accounts:2,rules:3,media:10,outputs:8},payload_bytes:1048576,files:12};
const empty={supported:true,restricted:false,runtime:{},pending:null,upload:null};
async function openExport(page:Page){
 await page.locator('#backup-export-details summary').click();
 await page.locator('#backup-export-admin').fill('synthetic-admin');
 await page.locator('#backup-export-password').fill('synthetic-backup-passphrase');
 await page.locator('#backup-export-repeat').fill('synthetic-backup-passphrase');
}
async function confirm(page:Page,action:string,word:string){
 await page.locator(`[data-backup-action="${action}"]`).click();
 await page.locator('#backup-action-admin').fill('synthetic-admin');
 if(word)await page.locator('#backup-confirm-word').fill(word);
 await page.locator('#backup-confirm-submit').click();
}

for(const width of [1440,390])test(`backup export, candidate confirmation and recovery hold at ${width}px`,async({page},testInfo)=>{
 await page.setViewportSize({width,height:1000});
 const errors:string[]=[],writes:{url:string,body:Record<string,unknown>}[]=[];
 page.on('pageerror',e=>errors.push(e.message));
 let status:Record<string,unknown>={...empty};
 await page.route('**/api/restore/status',route=>route.fulfill({json:status}));
 await page.route('**/api/backups/export',route=>{
  writes.push({url:route.request().url(),body:route.request().postDataJSON()});
  return route.fulfill({contentType:'application/octet-stream',body:'synthetic-encrypted-archive'});
 });
 await page.route('**/api/restore/confirm',route=>{
  writes.push({url:route.request().url(),body:route.request().postDataJSON()});
  status={...empty,pending:{id,state:'pending',generation}};
  return route.fulfill({json:{ok:true,restart_required:true}});
 });
 await page.goto('/#settings');
 await expect(page.locator('#backup-status')).toContainText('没有等待处理');expect(writes).toHaveLength(0);
 await openExport(page);await page.locator('#backup-export-repeat').fill('different');
 await page.locator('#backup-export').click();await expect(page.locator('#backup-error')).toContainText('不一致');expect(writes).toHaveLength(0);
 await page.locator('#backup-export-repeat').fill('synthetic-backup-passphrase');
 const download=page.waitForEvent('download');await page.locator('#backup-export').click();
 expect((await download).suggestedFilename()).toMatch(/^yolink-backup-.*\.age$/);
 await expect(page.locator('#backup-export-admin')).toHaveValue('');
 status={...empty,upload:candidate};await page.locator('#backup-refresh').click();
 await expect(page.locator('#backup-candidate')).toContainText('2 个账号、3 条规则');
 await page.locator('[data-backup-action="confirm"]').click();
 await expect(page.locator('#backup-confirm-effects')).toContainText('备份中的管理员密码');
 await page.locator('#backup-action-admin').fill('synthetic-admin');await page.locator('#backup-confirm-word').fill('wrong');
 await page.locator('#backup-confirm-submit').click();expect(writes).toHaveLength(1);
 await page.locator('#backup-confirm-word').fill('RESTORE');await page.locator('#backup-confirm-submit').click();
 await expect(page.locator('#backup-status')).toContainText('等待重启执行恢复');
 expect(writes[1].body).toEqual({id,revision,confirm:'RESTORE',admin_password:'synthetic-admin'});
 status={...empty,restricted:true,runtime:{id,state:'installed',generation},pending:{id,state:'installed',generation}};
 await page.locator('#backup-refresh').click();await expect(page.locator('#backup-hold')).toBeVisible();
 await page.locator('[data-backup-action="review"]').click();await expect(page.locator('#backup-confirm-effects')).toContainText('还需重启');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.locator('#backup-export-details').evaluate(el=>(el as HTMLDetailsElement).open=false);
 await page.locator('#backup-import-details').evaluate(el=>(el as HTMLDetailsElement).open=true);
 await page.evaluate(()=>document.fonts.ready);
 await page.screenshot({path:testInfo.outputPath(`backup-${width}.png`),fullPage:true});
 expect(errors).toEqual([]);
});

test('failed writes require status reconciliation and never silently retry',async({page})=>{
 let calls=0;let upload:typeof candidate|null=candidate;
 await page.route('**/api/restore/status',route=>route.fulfill({json:{...empty,upload}}));
 await page.route('**/api/restore/cancel',route=>{calls++;return route.abort('failed');});
 await page.goto('/#settings');await expect(page.locator('#backup-candidate')).toBeVisible();
 await confirm(page,'cancel','');await expect(page.locator('#backup-error')).toContainText('不要直接重复提交');
 await expect(page.locator('#backup-import')).toBeDisabled();expect(calls).toBe(1);
 upload=null;await page.locator('#backup-refresh').click();await expect(page.locator('#backup-status')).toContainText('没有等待处理');
 await expect(page.locator('#backup-import')).toBeEnabled();expect(calls).toBe(1);
});

test('import sends credentials only in the body and preserves candidate until explicit action',async({page})=>{
 let imported=false,calls=0;
 await page.route('**/api/restore/status',route=>route.fulfill({json:{...empty,upload:imported?candidate:null}}));
 await page.route('**/api/restore/import',route=>{
  calls++;expect(route.request().url()).not.toContain('synthetic-');
  const body=route.request().postDataBuffer()!.toString();
  expect(body).toContain('name="options"');expect(body).toContain('synthetic-admin');expect(body).toContain('synthetic-ciphertext');
  imported=true;return route.fulfill({json:candidate});
 });
 await page.goto('/#settings');await page.locator('#backup-import-details summary').click();
 await page.locator('#backup-file').setInputFiles({name:'synthetic.age',mimeType:'application/octet-stream',buffer:Buffer.from('synthetic-ciphertext')});
 await page.locator('#backup-import-admin').fill('synthetic-admin');await page.locator('#backup-import-password').fill('synthetic-backup-passphrase');
 await page.locator('#backup-import').click();await expect(page.locator('#backup-candidate')).toContainText('2 个账号、3 条规则');
 await expect(page.locator('#backup-status')).toContainText('尚未替换运行数据');
 await expect(page.locator('#backup-import-admin')).toHaveValue('');expect(calls).toBe(1);
});

test('navigation clears secrets, cancels stale operations and preserves latest status',async({page})=>{
 let release!:()=>void;const waiting=new Promise<void>(resolve=>{release=resolve;});let writes=0;
 await page.route('**/api/restore/status',route=>route.fulfill({json:empty}));
 await page.route('**/api/backups/export',async route=>{writes++;await waiting;await route.fulfill({status:500,json:{error:'old request'}}).catch(()=>{});});
 await page.goto('/#settings');await openExport(page);await page.locator('#backup-export').click();
 await expect.poll(()=>writes).toBe(1);await page.locator('.sidebar [data-nav="accounts"]').click();
 await page.locator('.sidebar [data-nav="settings"]').click();release();
 await expect(page.locator('#backup-status')).toContainText('没有等待处理');await expect(page.locator('#backup-export-admin')).toHaveValue('');
 await expect(page.locator('#backup-error')).toBeHidden();await expect(page.locator('#backup-export')).toBeEnabled();
});

test('unsupported restore and expired authentication leave useful recovery actions',async({page})=>{
 await page.route('**/api/restore/status',route=>route.fulfill({json:{...empty,supported:false}}));
 await page.goto('/#settings');await expect(page.locator('#backup-export')).toBeEnabled();await expect(page.locator('#backup-import')).toBeDisabled();
 await page.locator('#backup-import-details summary').click();await expect(page.locator('#backup-platform')).toContainText('Linux');
 await page.route('**/api/restore/status',route=>route.fulfill({status:401,json:{error:'session expired'}}));
 await page.locator('#backup-refresh').click();await expect(page.locator('#login')).toBeVisible();await expect(page.locator('#app')).toBeHidden();
});

test('a consumed upload pointer never hides pending or completed recovery',async({page})=>{
 let phase='pending';
 await page.route('**/api/restore/status',route=>route.fulfill({json:{...empty,pending:{id,state:phase,generation},upload:candidate}}));
 await page.goto('/#settings');await expect(page.locator('#backup-status')).toContainText('等待重启执行恢复');
 await expect(page.locator('#backup-import')).toBeDisabled();await expect(page.locator('[data-backup-action="confirm"]')).toHaveCount(0);
 for(const state of ['installed','released_new']){
  phase=state;await page.locator('#backup-refresh').click();
  await expect(page.locator('#backup-status')).not.toContainText('尚未替换');
  await expect(page.locator('#backup-import')).toBeEnabled();await expect(page.locator('[data-backup-action="confirm"]')).toHaveCount(0);
 }
});

for(const palette of ['cream','forest','blue','clay','lilac','midnight'])test(`backup form retains ${palette} theme`,async({page},testInfo)=>{
 await page.goto('/#settings');await expect(page.locator('#backup-status')).toContainText('没有等待处理');
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();await page.locator(`[data-palette-choice="${palette}"]`).click();
 await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();await openExport(page);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:testInfo.outputPath(`backup-${palette}.png`),fullPage:true});
});
