import { test, expect } from '@playwright/test';

const makeJob=(status:string,started:number)=>({id:'review-job',rule:'review-rule',account:'fixture',folder:'root',source_path:'/review',current_path:'/review/movie.mkv',status,started,created:started,dry_run:false,limit:0,seen:0,created_files:0,skipped:0,errors:status==='failed'?1:0,filtered:0,message:'Synthetic review fixture',samples:[],failures:[]});

test('running clock advances with unchanged API data without replacing dashboard controls',async({page,request})=>{
 const now=Date.UTC(2026,9,20,4);
 const data=await(await request.get('/api/dashboard')).json();
 const state=await(await request.get('/api/state')).json();
 const job=makeJob('running',now/1000-60);
 await page.route(url=>url.pathname==='/api/dashboard',route=>route.fulfill({json:{...data,time:now,jobs:[job],overview:{...data.overview,running:1}}}));
 await page.route(url=>url.pathname==='/api/state',route=>route.fulfill({json:{...state,jobs:[job]}}));
 await page.clock.install({time:now-1000});
 await page.clock.pauseAt(now);
 await page.goto('/');
 const clock=page.locator('#bw-task-elapsed');
 await expect(clock).toHaveText('01:00');
 const handle=await clock.elementHandle();
 await page.locator('.bw-trend-controls button').focus();
 await page.clock.fastForward(60000);
 await expect(clock).toHaveText('02:00');
 expect(await handle?.evaluate(el=>el.isConnected)).toBe(true);
 await expect(page.locator('.bw-trend-controls button')).toBeFocused();
 await page.locator('#dash-refresh').click();
 await expect(clock).toHaveText('02:00');
 await page.locator('.sidebar [data-nav="records"]').click();
 await page.clock.fastForward(60000);
 await expect(clock).toHaveText('02:00');
 await page.locator('.sidebar [data-nav="dashboard"]').click();
 await expect(clock).toHaveText('03:00');
});

for(const width of [1440,390]){
 test(`task detail action has sufficient contrast in every palette at ${width}px`,async({page,request})=>{
  const state=await(await request.get('/api/state')).json();
  const job=makeJob('failed',1792468740);
  await page.route(url=>url.pathname==='/api/state',route=>route.fulfill({json:{...state,jobs:[job],failures:[job]}}));
  await page.setViewportSize({width,height:1000});
  await page.goto('/#home');
  await expect(page.locator('#gen-failures .job-details>summary')).toBeVisible();
  for(const name of ['米白黑黄','雾白松绿','云雾冰蓝','燕麦陶橙','雾紫石墨','午夜琥珀']){
   await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
   await page.getByRole('button',{name,exact:true}).click();
   await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();
   const ratio=await page.locator('#gen-failures .job-details>summary').evaluate(el=>{
    const foreground=getComputedStyle(el).color;
    const background=getComputedStyle(el.closest('.panel')!).backgroundColor;
    const luminance=(value:string)=>value.match(/[\d.]+/g)!.slice(0,3).map(Number).map(v=>v/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4).reduce((sum,v,i)=>sum+v*[.2126,.7152,.0722][i],0);
    const values=[luminance(foreground),luminance(background)].sort((a,b)=>b-a);
    return(values[0]+.05)/(values[1]+.05);
   });
   expect(ratio,name).toBeGreaterThanOrEqual(4.5);
  }
 });
}

test('glass minimum, maximum, persistence and reset match the agreed material range',async({page})=>{
 await page.goto('/');
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 await page.locator('[data-style-choice="glass"]').click();
 for(const [key,min,max] of [['blur','4','40'],['opacity','0','90'],['dim','0','80']]){
  const slider=page.locator('[data-pref="'+key+'"]');
  await expect(slider).toHaveAttribute('min',min);
  await expect(slider).toHaveAttribute('max',max);
  await slider.press('Home');await expect(slider).toHaveValue(min);
  await slider.press('End');await expect(slider).toHaveValue(max);
 }
 expect(await page.locator('body').evaluate(el=>getComputedStyle(el,'::after').backgroundColor)).toBe('rgba(0, 0, 0, 0.8)');
 await page.reload();
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 for(const [key,value] of [['blur','40'],['opacity','90'],['dim','80']])await expect(page.locator('[data-pref="'+key+'"]')).toHaveValue(value);
 await page.getByRole('button',{name:'恢复默认材质',exact:true}).click();
 for(const [key,value] of [['blur','14'],['opacity','35'],['dim','0']])await expect(page.locator('[data-pref="'+key+'"]')).toHaveValue(value);
 await page.locator('[data-glass-preset="dark-clear"]').click();
 await expect(page.locator('[data-glass-preset="dark-clear"]')).toHaveAttribute('aria-pressed','true');
 for(const [key,value] of [['glassText','light'],['blur','20'],['shellOpacity','6'],['opacity','16'],['recordsOpacity','18'],['dim','28']])await expect(page.locator('#skin-panel [data-pref="'+key+'"]')).toHaveValue(value);
 await page.locator('[data-glass-preset="light-frost"]').click();
 await expect(page.locator('[data-glass-preset="light-frost"]')).toHaveAttribute('aria-pressed','true');
 for(const [key,value] of [['glassText','dark'],['blur','20'],['shellOpacity','15'],['opacity','48'],['recordsOpacity','35'],['dim','0']])await expect(page.locator('#skin-panel [data-pref="'+key+'"]')).toHaveValue(value);
 await page.locator('[data-style-choice="original"]').click();
 expect(await page.locator('body').evaluate(el=>getComputedStyle(el,'::after').content)).toBe('none');
});

test('UI review fixes: header logout, deduplicated desktop more menu, skin panel geometry/focus, glass filter veil and 700px breakpoint',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});
 await page.goto('/');
 await expect(page.locator('.butter-header-actions #logout')).toBeVisible();
 await expect(page.locator('#logout-settings')).toBeHidden();
 await page.locator('.sidebar [data-nav="more"]').click();
 await expect(page.locator('#view-more .more-menu [data-nav="files"]')).toBeHidden();
 await expect(page.locator('#view-more .more-menu [data-nav="records"]')).toBeHidden();
 await expect(page.locator('#view-more .more-menu [data-nav="inventory"]')).toBeVisible();
 await expect(page.locator('#view-more .more-menu [data-nav="maintenance"]')).toBeVisible();
 await expect(page.locator('#view-maintenance')).toContainText('从其他项目迁移');
 const skinTrigger=page.locator('[data-skin-toggle]');
 await skinTrigger.click();
 const actionBottom=await page.locator('.butter-header-actions').evaluate(el=>el.getBoundingClientRect().bottom);
 const panelTop=await page.locator('#skin-panel').evaluate(el=>el.getBoundingClientRect().top);
 expect(panelTop).toBeGreaterThanOrEqual(actionBottom);
 await page.locator('[data-style-choice="glass"]').click();
 expect(await page.locator('[data-skin-toggle] span').evaluate(el=>getComputedStyle(el).color)).toBe('rgb(255, 255, 255)');
 expect(await page.locator('.records-filters').evaluate(el=>getComputedStyle(el).backdropFilter)).toContain('blur');
 await page.keyboard.press('Escape');
 await expect(page.locator('#skin-panel')).toBeHidden();
 await expect(skinTrigger).toBeFocused();
 await page.setViewportSize({width:700,height:900});
 await expect(page.locator('#view-more .more-menu [data-nav="files"]')).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.locator('#view-more .more-menu [data-nav="settings"]').click();
 await expect(page.locator('#settings-overview [data-nav="maintenance"]')).toBeVisible();
 await page.locator('#settings-overview [data-nav="maintenance"]').click();
 await expect(page.locator('#view-maintenance')).toBeVisible();
});

test('cloud-more compact menu popover remains high-contrast in glass modes and filter bars align on a single row',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});
 await page.goto('/#accounts');
 const more=page.locator('.cloud-more').first();
 await more.locator('summary').click();
 const menu=more.locator('>div');
 const removeBtn=menu.locator('button[data-cloud-remove]');
 const hint=menu.locator('p');
 await expect(menu).toBeVisible();
 await expect(removeBtn.locator('svg')).toBeVisible();
 await expect(hint).toHaveText('已关联规则或播放索引时请先停用，不可直接移除。');
 const checkContrast=async(backdropRgb:[number,number,number])=>{
  for(const target of [removeBtn,hint]){
   const ratio=await target.evaluate((el,backdrop)=>{
    const parse=(s:string)=>{const m=s.match(/[\d.]+/g)!.map(Number);return m.length===3?[...m,1]:m;};
    const fg=parse(getComputedStyle(el).color),bg=parse(getComputedStyle(el.closest('.cloud-more>div')!).backgroundColor);
    const comp=bg.slice(0,3).map((v,i)=>v*bg[3]+backdrop[i]*(1-bg[3]));
    const lum=(rgb:number[])=>rgb.slice(0,3).map(v=>v/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4).reduce((s,v,i)=>s+v*[.2126,.7152,.0722][i],0);
    const [hi,lo]=[lum(fg),lum(comp)].sort((a,b)=>b-a);
    return(hi+.05)/(lo+.05);
   },backdropRgb);
   expect(ratio).toBeGreaterThanOrEqual(4.5);
  }
 };
 await checkContrast([255,255,255]);
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();
 await page.locator('[data-style-choice="glass"]').click();
 await page.locator('[data-glass-preset="light-frost"]').click();
 await expect(removeBtn).toHaveAttribute('data-glass-adapted','dark');
 await checkContrast([0,0,0]);
 await page.locator('[data-glass-preset="dark-clear"]').click();
 await expect(removeBtn).toHaveAttribute('data-glass-adapted','light');
 await checkContrast([255,255,255]);
 await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();

 await page.locator('.sidebar [data-nav="records"]').click();
 await expect(page.getByLabel('搜索文件、路径或客户端')).toHaveAttribute('placeholder','搜索文件名、输出路径、IP、PotPlayer…');
 const recordsTops=await page.locator('#records-filter-form :is(input,select,button):visible').evaluateAll(els=>els.map(el=>Math.round(el.getBoundingClientRect().top)));
 expect(new Set(recordsTops).size).toBe(1);
 await page.locator('#records-deliveries-tab').click();
 await expect(page.getByLabel('搜索投递任务、目录或错误')).toHaveAttribute('placeholder','搜索投递任务、目录或错误…');

 await page.goto('/#inventory');
 await expect(page.getByLabel('文件或路径')).toBeVisible();
 const invTops=await page.locator('#inventory-filters :is(input,select,button):visible').evaluateAll(els=>els.map(el=>Math.round(el.getBoundingClientRect().top)));
 expect(new Set(invTops).size).toBe(1);

 await page.goto('/#config');
 await expect(page.getByLabel('搜索目录规则')).toBeVisible();
 const ruleTops=await page.locator('#rule-filters :is(input,select,button):visible').evaluateAll(els=>els.map(el=>Math.round(el.getBoundingClientRect().top)));
 expect(new Set(ruleTops).size).toBe(1);
});


