import {test,expect,type Page} from '@playwright/test';

const rule={id:'refresh-rule',name:'刷新测试规则',account:'fixture',folder:'root',source_path:'/synthetic',output:'test',interval:3600,enabled:false,filter:{preset:'video'}};
const job={id:'refresh-job',rule:rule.id,account:'fixture',folder:'root',source_path:'/synthetic',status:'running',created:1792497600,started:1792497600,seen:0,created_files:0,skipped:0,errors:0,samples:[],failures:[]};
async function setVisibility(page:Page,hidden:boolean){await page.evaluate(value=>{Object.defineProperty(document,'hidden',{configurable:true,value});document.dispatchEvent(new Event('visibilitychange'));},hidden);}
async function tick(page:Page,ms:number){await page.clock.runFor(ms);await page.waitForTimeout(70);}

test('active status polls at 4s, idle pages at 30s, hidden pages do not render',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();let calls=0;
 await page.route('**/api/state',route=>{calls++;return route.fulfill({json:{...data,rules:[rule],jobs:[{...job,seen:calls}]}});});
 await page.clock.install({time:new Date('2026-10-20T12:00:00Z')});await page.goto('/#home');
 await expect(page.locator('#gen-active')).toContainText('已发现 1');
 await page.locator('#gen-history [data-gen-cancel="refresh-job"]').focus();
 await tick(page,4100);await expect(page.locator('#gen-active')).toContainText('已发现 2');
 await expect(page.locator('#gen-history [data-gen-cancel="refresh-job"]')).toBeFocused();
 await page.locator('.sidebar [data-nav="accounts"]').click();
 const before=calls;await tick(page,29000);expect(calls).toBe(before);await tick(page,1100);expect(calls).toBe(before+1);
 await expect(page.locator('#gen-active')).toContainText('已发现 2');
 await page.locator('.sidebar [data-nav="home"]').click();await expect(page.locator('#gen-active')).toContainText('已发现 3');
});

for(const width of [1440,390])test(`settings draft survives blur and state polling at ${width}px`,async({page,request},testInfo)=>{
 await page.setViewportSize({width,height:1000});
 const data=await(await request.get('/api/state')).json();let version=0,integrations=0;
 await page.route('**/api/state',route=>route.fulfill({json:{...data,settings:{...data.settings,public_base:version++?'https://external-change.example.test':'https://saved.example.test'}}}));
 await page.route('**/api/integrations',async route=>{integrations++;await route.continue();});
 await page.clock.install();await page.goto('/#settings');
 await page.locator('[data-setting-open="access"]').click();
 await page.locator('#public-base').fill('https://unsaved.example.test');await page.locator('#settings-drawer-title').click();
 const before=integrations;await tick(page,61000);
 await expect(page.locator('#public-base')).toHaveValue('https://unsaved.example.test');expect(integrations).toBe(before);
 await page.screenshot({path:testInfo.outputPath('draft-preserved.png')});
});

test('first state request interrupted by hiding resumes bootstrap when visible',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();let calls=0;let release!:()=>void;
 const pending=new Promise<void>(resolve=>{release=resolve;});
 await page.route('**/api/state',async route=>{if(++calls===1)await pending;await route.fulfill({json:data}).catch(()=>{});});
 await page.goto('/#accounts');await expect.poll(()=>calls).toBe(1);
 await setVisibility(page,true);release();await setVisibility(page,false);
 await expect(page.locator('#app')).toBeVisible();await expect(page.locator('#view-accounts')).toBeVisible();
 expect(calls).toBe(2);
});

test('hide cancels pending state and foreground fetch is fresh; logout cannot be resurrected',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();let calls=0;let release!:()=>void;
 const pending=new Promise<void>(resolve=>{release=resolve;});
 await page.route('**/api/state',async route=>{const n=++calls;if(n===2)await pending;await route.fulfill({json:{...data,accounts:[{...data.accounts[0],name:'snapshot-'+n}]}}).catch(()=>{});});
 await page.route('**/api/logout',route=>route.fulfill({json:{ok:true}}));
 await page.clock.install();await page.goto('/#accounts');await expect(page.locator('#account-list')).toContainText('snapshot-1');
 await tick(page,30010);await expect.poll(()=>calls).toBe(2);await setVisibility(page,true);
 await tick(page,90000);expect(calls).toBe(2);await setVisibility(page,false);
 await expect(page.locator('#account-list')).toContainText('snapshot-3');
 await page.locator('#logout').click();release();await expect(page.locator('#login')).toBeVisible();
 await tick(page,90000);expect(calls).toBe(3);await expect(page.locator('#app')).toBeHidden();
});

test('state and dashboard requests do not overlap while responses are slow',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();let calls=0,inflight=0,max=0;let release!:()=>void;
 const pending=new Promise<void>(resolve=>{release=resolve;});
 await page.route('**/api/state',async route=>{calls++;inflight++;max=Math.max(max,inflight);if(calls===2)await pending;inflight--;await route.fulfill({json:{...data,jobs:[job]}});});
 await page.clock.install();await page.goto('/');await expect(page.locator('#dash-content')).toContainText('网盘');
 await tick(page,4100);await expect.poll(()=>calls).toBe(2);await tick(page,30000);expect(calls).toBe(2);expect(max).toBe(1);
 release();await tick(page,100);await tick(page,4100);await expect.poll(()=>calls).toBe(3);
});

test('partial dashboard failure backs off exponentially and explicit range remains immediate',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();let failures=true,calls=0;
 await page.route('**/api/state',route=>route.fulfill({json:{...data,jobs:[job]}}));
 await page.route('**/api/dashboard?*',async route=>{calls++;if(failures)await route.fulfill({status:503,json:{error:'合成仪表盘异常'}});else await route.continue();});
 await page.clock.install();await page.goto('/');await expect(page.locator('#dash-error')).toContainText('合成仪表盘异常');
 const before=calls;await tick(page,8100);expect(calls).toBe(before+1);
 await tick(page,8100);expect(calls).toBe(before+1);await tick(page,8100);expect(calls).toBe(before+2);
 failures=false;await page.locator('#dash-refresh').click();await expect(page.locator('#dash-error')).toBeHidden();
 const next=calls;await page.locator('#dash-days').selectOption('30');await expect.poll(()=>calls).toBe(next+1);
 await expect(page.locator('.bw-trend-value')).toContainText('近 30 天');
});

test('generation history and legacy event selector initialize on their own pages',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();
 await page.route('**/api/state',route=>route.fulfill({json:{...data,rules:[rule],ingest:[{id:'event',rule:rule.id,path:'/synthetic/new',status:'failed',error:'合成失败',created:1792497600}]}}));
 await page.goto('/#home');await expect(page.locator('#ingest-section')).toBeVisible();
 await page.locator('#ingest-summary').click();await expect(page.locator('#ingest-list')).toContainText('/synthetic/new');
 await page.locator('.sidebar [data-nav="settings"]').click();await page.locator('[data-setting-open="advanced"]').click();
 await page.getByText('兼容凭据与原有请求工具',{exact:true}).click();
 await expect(page.locator('#ingest-rule option')).toHaveText(['刷新测试规则']);
});

test('rule edit keeps selected account and fields after accounts change in background',async({page,request})=>{
 const data=await(await request.get('/api/state')).json();let changed=false;
 await page.route('**/api/state',route=>route.fulfill({json:{...data,rules:[rule],accounts:changed?[]:data.accounts}}));
 await page.clock.install();await page.goto('/#config');await page.locator('[data-gen-edit="refresh-rule"]').click();
 await page.locator('#gen-rule-form').getByText('高级选项',{exact:true}).click();
 await page.locator('#gen-rule-name').fill('保留草稿');await page.locator('#gen-rule-output').fill('/edited/output');
 changed=true;await tick(page,31000);
 await expect(page.locator('#gen-rule-name')).toHaveValue('保留草稿');await expect(page.locator('#gen-rule-output')).toHaveValue('/edited/output');
 await expect(page.locator('#gen-rule-account')).toHaveValue('fixture');
});

test('interrupted settings demand load resumes after a transient foreground state failure',async({page})=>{
 let calls=0,release!:()=>void;const pending=new Promise<void>(resolve=>{release=resolve;});
 let stateCalls=0;
 await page.route('**/api/state',async route=>{if(++stateCalls===2)await route.fulfill({status:503,json:{error:'临时状态读取失败'}});else await route.continue();});
 await page.route('**/api/integrations',async route=>{if(++calls===1)await pending;await route.fulfill({json:{config:{},deliveries:[]}}).catch(()=>{});});
 await page.clock.install();await page.goto('/#settings');await expect.poll(()=>calls).toBe(1);
 await setVisibility(page,true);release();await setVisibility(page,false);
 await expect(page.locator('#global-error')).toContainText('临时状态读取失败');
 await tick(page,61000);
 await expect(page.locator('#settings-overview')).toContainText('通知与媒体联动');expect(calls).toBe(2);
});

test('generation tab reveals convert icons immediately without rerendering hidden pages',async({page})=>{
 await page.goto('/#home');await page.locator('#gen-config-tab').click();
 const pending=(selector:string)=>page.locator(selector).evaluateAll(els=>els.filter(el=>!el.closest('[hidden]')).length);
 await expect.poll(()=>pending('#gen-config i[data-lucide]')).toBe(0);
 await page.locator('#gen-status-tab').click();await expect.poll(()=>pending('#gen-status i[data-lucide]')).toBe(0);
});
