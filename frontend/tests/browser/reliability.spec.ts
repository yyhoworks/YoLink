import { test, expect, type Page } from '@playwright/test';

const now=Date.now();
test.beforeEach(async({page})=>{
 await page.route('**/api/state',async route=>{
  const response=await route.fetch(),data=await response.json();
  data.rules=[{id:'fixture-rule',name:'合成电影规则',account:'fixture',folder:'root',source_path:'/合成数据',output:'电影',enabled:false,interval:0,filter:{preset:'video'}}];
  await route.fulfill({json:data});
 });
});
const job=(id:string,eligible=true)=>({id,rule:'fixture-rule',source_path:'/合成数据/'+id,status:'failed',eligible,reason:eligible?'':'旧任务缺少规则快照，不能安全恢复，请重新生成'});
function health(overrides:Record<string,unknown>={}){
 return {account:'fixture',revision:3,recovery_ready:true,recovery_reason:'',operations:[
  {operation:'list',state:'healthy',checked:now,last_success:now,failures:0,note:'目录读取成功'},
  {operation:'resolve',state:'rate_limit',checked:now-120000,last_success:0,failures:2,next_retry:now-60000,note:'上次请求受限，建议等待时间已过'},
  {operation:'quota',state:'unsupported',checked:now,last_success:0,failures:0,note:'当前渠道不支持容量查询'},
 ],affected:[job('job-1'),job('legacy',false)],has_more:false,next_before:0,...overrides};
}
const report={id:'0123456789abcdef0123456789abcdef',created:now,media:'movie',stale:false,boundary:'服务器端检查不证明播放器解码或拖动成功。',steps:[
 {id:'index',label:'媒体索引',status:'passed',note:'已登记',millis:1},
 {id:'output',label:'本地 STRM',status:'failed',note:'本地内容已变化；没有覆盖文件',action:'核对用户修改',millis:1},
 {id:'account',label:'网盘账号',status:'passed',note:'已启用',millis:1},
 {id:'resolve',label:'原文件地址解析',status:'passed',note:'已取得地址',millis:10},
 {id:'probe',label:'有限取流检查',status:'passed',note:'已读取 1024 字节',millis:15},
]};
async function inventory(page:Page){
 await page.route('**/api/outputs?*',route=>route.fulfill({json:{items:[{media:'movie',path:'电影/合成电影.strm',name:'<合成电影 & 长文件名>.mkv',account:'fixture',status:'modified',url:'/s/synthetic',created:now,checked:now}],total:1,page:1,pages:1}}));
 await page.goto('/#inventory');await page.getByRole('button',{name:'播放诊断',exact:true}).click();
}

for(const width of [1440,390]){
 test(`health pagination, explicit recovery and diagnostic boundaries at ${width}px`,async({page},testInfo)=>{
  await page.setViewportSize({width,height:1000});
  const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
  let recovered=false;
  await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health({affected:recovered?[]:new URL(route.request().url()).searchParams.has('before')?[job('job-2')]:[job('job-1'),job('legacy',false)],has_more:!recovered&&!new URL(route.request().url()).searchParams.has('before'),next_before:100})}));
  const writes:unknown[]=[];
  await page.route('**/api/accounts/fixture/recover',route=>{writes.push(route.request().postDataJSON());recovered=true;return route.fulfill({status:202,json:{started:[{id:'new-job'}],deferred:[{id:'job-2',reason:'当前规则已有任务运行'}]}});});
  await page.goto('/#accounts');await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
  await expect(page.locator('#account-health')).toBeVisible();
  await expect(page.locator('#health-operations')).toContainText('请求受限');
  await expect(page.locator('#health-operations')).toContainText('不支持');
  await expect(page.locator('[data-health-job="legacy"]')).toBeDisabled();
  await page.locator('[data-health-job="job-1"]').check();
  await page.locator('#health-more').click();await page.locator('[data-health-job="job-2"]').check();
  await expect(page.locator('[data-health-job="job-1"]')).toBeChecked();
  await page.locator('#health-review').click();await expect(page.locator('#health-confirm-list')).toContainText('job-2');
  expect(writes).toHaveLength(0);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath('health-recovery.png'),fullPage:true});
  await page.locator('#health-confirm').click();
  await expect(page.locator('#health-recovery-result')).toContainText('已加入恢复队列 1 项，暂未恢复 1 项');
  await expect(page.locator('#health-recovery-result')).toContainText('当前规则已有任务运行');
  expect(writes).toEqual([{revision:3,jobs:['job-1','job-2'],confirm:true}]);
  await expect(page.locator('#health-review')).toBeDisabled();

  await page.route('**/api/media/movie/diagnose',route=>{expect(route.request().postDataJSON()).toEqual({output:'电影/合成电影.strm',probe:true});return route.fulfill({json:report});});
  await page.route('**/api/diagnostics/*/export',route=>route.fulfill({status:410,json:{error:'报告不存在或已过期'}}));
  await inventory(page);await page.locator('#diagnostic-probe').check();await page.locator('#diagnostic-run').click();
  await expect(page.locator('#diagnostic-steps>li')).toHaveCount(5);
  await expect(page.locator('#diagnostic-summary')).toContainText('发现需要处理的问题');
  await expect(page.locator('#diagnostic-summary')).toContainText('不证明播放器解码');
  await expect(page.locator('#diagnostic-steps')).toContainText('没有覆盖文件');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await expect(page.locator('#toast')).toBeHidden();await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath('diagnostic-results.png'),fullPage:true});
  await page.locator('#diagnostic-export').click();await expect(page.locator('#diagnostic-error')).toContainText('报告不存在或已过期');
  expect(errors).toEqual([]);
 });
}

test('failed recovery keeps uncertainty warning after refreshing health',async({page})=>{
 await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health()}));
 await page.route('**/api/accounts/fixture/recover',route=>route.abort('failed'));
 await page.goto('/#accounts');await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await page.locator('[data-health-job="job-1"]').check();await page.locator('#health-review').click();await page.locator('#health-confirm').click();
 await expect(page.locator('#health-error')).toContainText('请求可能已被服务接收');
 await expect(page.locator('#health-recovery-result')).toHaveText('恢复结果尚未确认。');
 await expect(page.locator('#health-review')).toBeDisabled();
});

test('account changes while paging invalidate selection instead of merging revisions',async({page})=>{
 await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health({revision:new URL(route.request().url()).searchParams.has('before')?4:3,has_more:true,next_before:99})}));
 await page.goto('/#accounts');await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await page.locator('[data-health-job="job-1"]').check();await page.locator('#health-more').click();
 await expect(page.locator('#health-error')).toContainText('账号配置已改变');
 await expect(page.locator('#health-jobs input')).toHaveCount(0);await expect(page.locator('#health-review')).toBeDisabled();
});

test('cancel, close and reopen diagnostics reset probe and reject old responses',async({page})=>{
 let release!:()=>void;const pending=new Promise<void>(resolve=>{release=resolve;});
 await page.route('**/api/media/movie/diagnose',async route=>{await pending;await route.fulfill({json:report}).catch(()=>{});});
 await inventory(page);await page.locator('#diagnostic-run').click();await expect(page.locator('#diagnostic-probe')).toBeDisabled();
 await page.locator('#diagnostic-cancel').click();await expect(page.locator('#diagnostic-summary')).toContainText('已取消诊断');
 await page.locator('#diagnostic-run').click();
 await page.getByRole('button',{name:'关闭播放诊断',exact:true}).click();
 await page.getByRole('button',{name:'播放诊断',exact:true}).click();
 await expect(page.locator('#diagnostic-probe')).toBeEnabled();
 release();await expect(page.locator('#diagnostic-steps>li')).toHaveCount(0);
 await expect(page.locator('#diagnostic-export')).toBeHidden();
});

test('health read error is actionable and a fresh retry clears stale selection',async({page})=>{
 let fail=true;
 await page.route('**/api/accounts/fixture/health*',route=>fail?route.fulfill({status:500,json:{error:'健康状态读取失败'}}):route.fulfill({json:health({recovery_ready:false,recovery_reason:'请先测试当前账号连接'})}));
 await page.goto('/#accounts');await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await expect(page.locator('#health-error')).toContainText('健康状态读取失败');await expect(page.locator('#health-review')).toBeDisabled();
 fail=false;await page.locator('#health-refresh').click();
 await expect(page.locator('#health-error')).toBeHidden();await expect(page.locator('#health-readiness')).toContainText('请先测试当前账号连接');
 await page.locator('[data-health-job="job-1"]').check();await expect(page.locator('#health-review')).toBeDisabled();
});

test('reopening during recovery resets confirmation and old completion cannot replace the new panel',async({page})=>{
 let release!:()=>void;const pending=new Promise<void>(resolve=>{release=resolve;});
 await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health()}));
 await page.route('**/api/accounts/fixture/recover',async route=>{await pending;await route.fulfill({status:500,json:{error:'旧请求失败'}});});
 await page.goto('/#accounts');await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await page.locator('[data-health-job="job-1"]').check();await page.locator('#health-review').click();await page.locator('#health-confirm').click();
 await expect(page.locator('#health-confirm')).toBeDisabled();
 await page.getByRole('button',{name:'关闭健康与恢复',exact:true}).click();
 await expect(page.getByRole('button',{name:'健康与恢复',exact:true})).toBeFocused();
 await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await page.locator('[data-health-job="job-1"]').check();await page.locator('#health-review').click();
 await expect(page.locator('#health-confirm')).toBeEnabled();release();
 await expect(page.locator('#toast')).toContainText('恢复请求未确认');await expect(page.locator('#health-error')).toBeHidden();
});

test('export from a superseded report cannot download or contaminate the new diagnosis',async({page})=>{
 let release!:()=>void;const pending=new Promise<void>(resolve=>{release=resolve;});
 const downloads:string[]=[];page.on('download',d=>downloads.push(d.suggestedFilename()));
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 let generation=0;
 await page.route('**/api/media/movie/diagnose',route=>route.fulfill({json:{...report,id:++generation===1?report.id:'abcdef0123456789abcdef0123456789'}}));
 await page.route('**/api/diagnostics/*/export',async route=>{await pending;await route.fulfill({json:report});});
 await inventory(page);await page.locator('#diagnostic-run').click();await expect(page.locator('#diagnostic-export')).toBeVisible();
 await page.locator('#diagnostic-export').click();await page.locator('#diagnostic-run').click();
 await expect(page.locator('#diagnostic-steps>li')).toHaveCount(5);release();
 await expect(page.locator('#diagnostic-export')).toBeEnabled();
 expect(downloads).toEqual([]);expect(errors).toEqual([]);await expect(page.locator('#diagnostic-error')).toBeHidden();
});

test('leaving and logging out cancel context and clear sensitive detail',async({page})=>{
 await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health()}));
 await page.route('**/api/logout',route=>route.fulfill({json:{ok:true}}));
 await page.goto('/#accounts');await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await expect(page.locator('#account-health')).toBeVisible();
 await page.locator('.sidebar [data-nav="dashboard"]').click();await expect(page.locator('#account-health')).toBeHidden();
 await page.locator('.sidebar [data-nav="accounts"]').click();await expect(page.locator('#account-health')).toBeHidden();
 await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
 await page.locator('#logout').click();await expect(page.locator('#login')).toBeVisible();await expect(page.locator('#account-health')).toBeHidden();
});

for(const palette of ['cream','forest','blue','clay','lilac','midnight']){
 test(`health theme inheritance: ${palette}`,async({page},testInfo)=>{
  await page.setViewportSize({width:390,height:844});
  await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health({recovery_ready:false,recovery_reason:'等待限流结束后测试目录连接'})}));
  await page.goto('/#accounts');
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.locator('[data-palette-choice="'+palette+'"]').click();
  await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
  await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
  await expect(page.locator('#health-operations>li')).toHaveCount(3);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath('health-'+palette+'.png'),fullPage:true});
 });
}

for(const mode of ['dark','light']){
 test(`glass health states remain readable with ${mode} text`,async({page},testInfo)=>{
  await page.setViewportSize({width:1440,height:1000});
  await page.route('**/api/accounts/fixture/health*',route=>route.fulfill({json:health()}));
  await page.route('https://wallpaper.example.test/reliability.svg',route=>route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="1440" height="1000"><rect width="1440" height="1000" fill="'+(mode==='light'?'#20303b':'#e4e9ed')+'"/></svg>'}));
  await page.goto('/#accounts');
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.locator('#wallpaper-url').fill('https://wallpaper.example.test/reliability.svg');
  await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
  await expect(page.locator('#wallpaper-online-status')).toContainText('网址已保存');
  await page.locator('[data-style-choice="glass"]').click();
  await page.locator('[data-glass-preset="'+(mode==='light'?'dark-clear':'light-frost')+'"]').click();
  await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
  await page.getByRole('button',{name:'健康与恢复',exact:true}).click();
  await expect(page.locator('#health-operations')).toContainText('请求受限');
  await expect(page.locator('#health-operations .reliability-state').first()).toHaveAttribute('data-glass-adapted',mode);
  const statusColors=await page.locator('#health-operations .reliability-state').evaluateAll(els=>els.map(el=>getComputedStyle(el).color));
  expect(new Set(statusColors).size).toBe(3);
  const browse=page.locator('[data-cloud-browse]');
  await expect(browse).toHaveAttribute('data-glass-role','primary');
  const contrast=await browse.evaluate(el=>{
   const parse=(s:string)=>s.match(/[\d.]+/g)!.slice(0,3).map(Number);
   const lum=(rgb:number[])=>rgb.map(v=>v/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4).reduce((s,v,i)=>s+v*[.2126,.7152,.0722][i],0);
   const cs=getComputedStyle(el),a=lum(parse(cs.color)),b=lum(parse(cs.backgroundColor));return(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
  });
  expect(contrast).toBeGreaterThanOrEqual(4.5);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  // Full-page Chromium captures do not extend fixed wallpaper past the viewport.
  // Capture actual viewports at both ends instead of presenting that artifact as UI.
  await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath('health-glass-'+mode+'.png')});
  await page.locator('#health-review').scrollIntoViewIfNeeded();await page.screenshot({path:testInfo.outputPath('health-glass-'+mode+'-tasks.png')});
 });
}
