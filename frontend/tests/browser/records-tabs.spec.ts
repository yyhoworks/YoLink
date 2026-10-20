import { test, expect } from '@playwright/test';

test.beforeEach(async({page})=>{
 await page.route(url=>url.pathname==='/api/integrations',route=>route.fulfill({json:{config:{},deliveries:[{id:'delivery-qa',job:'job-qa',kind:'webhook',status:'sent',attempts:1,created:1792382400,source_path:'/synthetic/media',channel_name:'Synthetic channel',title:'Synthetic completion'}]}}));
});

for(const width of [1440,390]){
 test(`playback failure clears delivery rows, stays readable and recovers at ${width}px`,async({page},testInfo)=>{
  let fail=true;
  await page.route(url=>url.pathname==='/api/records'&&url.searchParams.get('kind')==='playback',route=>{
   if(fail)return route.fulfill({status:500,json:{error:'播放汇总读取失败'}});
   return route.fulfill({json:{items:[{id:41,group_id:41,media:'play-media',kind:'playback',event:'play',outcome:'redirected',mode:'302',name:'Recovered playback.mkv',time:1792382400000,request_count:2,resolve_count:1,reuse_count:1}],page:1,pages:1,total:1,request_total:2}});
  });
  await page.route('https://wallpaper.example.test/dark.svg',route=>route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="1440" height="1000"><rect width="1440" height="1000" fill="#20303b"/></svg>'}));
  await page.setViewportSize({width,height:1000});
  await page.goto('/#records');
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
  await page.locator('#wallpaper-url').fill('https://wallpaper.example.test/dark.svg');
  await page.getByRole('button',{name:'使用在线壁纸',exact:true}).click();
  await expect(page.locator('#wallpaper-online-status')).toContainText('网址已保存');
  await page.locator('[data-style-choice="glass"]').click();
  await page.locator('[data-glass-preset="dark-clear"]').click();
  await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
  await page.locator('#records-deliveries-tab').click();
  await expect(page.locator('#records-body .delivery-record')).not.toHaveCount(0);
  await page.locator('#records-playback-tab').click();
  await expect(page.locator('#records-playback-tab')).toHaveAttribute('aria-selected','true');
  await expect(page.locator('#records-status')).toHaveText('读取失败');
  await expect(page.locator('#records-error')).toContainText('播放汇总读取失败');
  await expect(page.locator('#records-body .delivery-record')).toHaveCount(0);
  await expect(page.locator('#records-body')).toContainText('播放记录暂时无法读取');
  for(const selector of ['#records-body tr','#records-body td.empty']){
   expect(await page.locator(selector).evaluate(el=>getComputedStyle(el).backgroundColor)).toBe('rgba(0, 0, 0, 0)');
  }
  await expect(page.locator('#records-prev')).toBeDisabled();await expect(page.locator('#records-next')).toBeDisabled();
  const colors=await page.locator('#records-error').evaluate(el=>({background:getComputedStyle(el).backgroundColor,color:getComputedStyle(el).color}));
  expect(colors.background).toBe('rgba(23, 33, 43, 0.88)');
  const ratio=await page.locator('#records-error').evaluate(el=>{
   const parse=(s:string)=>s.match(/[\d.]+/g)!.map(Number);
   const fg=parse(getComputedStyle(el).color),bg=parse(getComputedStyle(el).backgroundColor);
   // Composite over white, the worst background for the dark alert tint.
   const composed=bg.slice(0,3).map(v=>v*bg[3]+255*(1-bg[3]));
   const lum=(rgb:number[])=>rgb.slice(0,3).map(v=>v/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4).reduce((s,v,i)=>s+v*[.2126,.7152,.0722][i],0);
   return(lum(fg)+.05)/(lum(composed)+.05);
  });
  expect(ratio).toBeGreaterThanOrEqual(4.5);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('records-error-glass.png')});
  await page.locator('html').evaluate(el=>el.setAttribute('data-glass-text','dark'));
  expect(await page.locator('#records-error').evaluate(el=>getComputedStyle(el).backgroundColor)).toBe('rgba(255, 255, 255, 0.88)');
  await page.locator('html').evaluate(el=>el.setAttribute('data-glass-text','light'));
  fail=false;await page.locator('#records-refresh').click();
  await expect(page.locator('#records-error')).toBeHidden();
  await expect(page.locator('#records-status')).toHaveText('1 组 · 2 次请求');
  await expect(page.locator('#records-body')).toContainText('Recovered playback.mkv');
  await page.locator('#records-deliveries-tab').click();
  await expect(page.locator('#records-body .delivery-record')).not.toHaveCount(0);
  await page.locator('#records-playback-tab').click();
  await expect(page.locator('#records-body')).toContainText('Recovered playback.mkv');
  await expect(page.locator('#records-body .delivery-record')).toHaveCount(0);
 });
}

test('pending tab request never presents the preceding tab rows',async({page})=>{
 let release!:()=>void;
 const pending=new Promise<void>(resolve=>{release=resolve;});
 await page.route(url=>url.pathname==='/api/records'&&url.searchParams.get('kind')==='playback',async route=>{await pending;await route.fulfill({json:{items:[],page:1,pages:0,total:0,request_total:0}});});
 await page.goto('/#records');
 await page.locator('#records-deliveries-tab').click();
 await expect(page.locator('#records-body .delivery-record')).not.toHaveCount(0);
 await page.locator('#records-playback-tab').click();
 await expect(page.locator('#records-body')).toContainText('正在读取播放记录');
 await expect(page.locator('#records-body .delivery-record')).toHaveCount(0);
 release();
 await expect(page.locator('#records-status')).toHaveText('0 组 · 0 次请求');
});
