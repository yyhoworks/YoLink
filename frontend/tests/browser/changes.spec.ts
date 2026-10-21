import {test,expect,type Page} from '@playwright/test';

const id='123456789abcdef0123456789abcdef01',revision='a'.repeat(64),run='b'.repeat(32);
const rule={id:'rule',name:'影视目录',account:'fixture',folder:'root',source_path:'/media',output:'test/media',interval:1800,enabled:false,incremental:true,filter:{preset:'video'}};
async function fixture(page:Page){
 await page.route('**/api/state',async route=>{
  const response=await route.fetch(),data=await response.json();
  await route.fulfill({json:{...data,rules:[rule],settings:{public_base:'https://yolink.example.test',output_root:'/strm'}}});
 });
 await page.route('**/api/changes?*',route=>route.fulfill({json:{runs:[{id:run,rule:'rule',scope:'/media/电影/很长的目录名称',status:'complete',created:1792548000,completed:1792548010,full:true}],run,run_status:'complete',items:[{remote:'movie',kind:'moved',before:'/media/旧目录/movie.mkv',after:'/media/电影/很长的目录名称/movie.mkv'},{remote:'missing',kind:'source_absent',before:'/media/以前的电影.mkv',after:''}],total:2,page:1,pages:1,last_full:1792548010,plans:[{id,state:'applying',created:1792548000}]}}));
 await page.route('**/api/outputs?*',route=>route.fulfill({json:{items:[{path:'test/media/movie.strm',output:'/strm/test/media/movie.strm',media:'media',name:'电影 <&>.mkv',source:'/media/电影/movie.mkv',account:'fixture',rule:'rule',status:'missing',note:'本地 STRM 缺失'}],total:1,page:1,pages:1}}));
}
for(const width of [1440,390])test(`changes review, preview and uncertain repair at ${width}px`,async({page},testInfo)=>{
 await page.setViewportSize({width,height:1000});await fixture(page);
 const errors:string[]=[],writes:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 let state='preview';let dropped=false;
 const summary=()=>({id,revision,total:1,ready:1,done:state==='complete'?1:0,conflicts:0,state,items:[{path:'test/media/movie.strm',ready:true,state:state==='complete'?'recovered':state==='applying'?'intent':'preview',note:state==='complete'?'中断后重新核验归属与内容，已完成登记':'将按登记内容重建此缺失 STRM'}]});
 await page.route('**/api/changes/preview',route=>{writes.push('preview');return route.fulfill({json:summary()})});
 await page.route('**/api/changes/'+id,route=>route.fulfill({json:summary()}));
 await page.route('**/api/changes/'+id+'/apply',async route=>{
  writes.push('apply');expect(route.request().postDataJSON()).toEqual({revision,confirm:'修复缺失'});
  if(!dropped){dropped=true;state='applying';await route.abort('connectionreset')}else{state='complete';await route.fulfill({json:summary()})}
 });
 await page.goto('/#maintenance');await expect(page.locator('#changes-list')).toContainText('移动或改名');await expect(page.locator('#changes-list')).toContainText('完整扫描中未见来源');expect(writes).toEqual([]);
 await page.screenshot({path:testInfo.outputPath(`changes-${width}.png`),fullPage:true});
 await page.evaluate(()=>{location.hash='inventory'});
 await page.locator('[data-output-select]').check();await page.locator('#inventory-repair').click();
 await expect(page.locator('#change-repair-panel')).toBeVisible();expect(writes).toEqual(['preview']);await expect(page.locator('#change-repair-apply')).toBeDisabled();
 await page.evaluate(()=>scrollTo(0,0));
 await page.screenshot({path:testInfo.outputPath(`repair-preview-${width}.png`),fullPage:true});
 await page.locator('#change-repair-confirm').check();await page.locator('#change-repair-apply').click();
 await expect(page.locator('#change-repair-error')).toContainText('已读取逐项状态');await expect(page.locator('#change-repair-apply')).toBeDisabled();
 await page.locator('#change-repair-confirm').check();await page.locator('#change-repair-apply').click();
 await expect(page.locator('#change-repair-panel')).toContainText('已核验完成 1 项');expect(writes).toEqual(['preview','apply','apply']);
 if(width===390){
  await expect(page.locator('#toast')).toBeHidden({timeout:6000});
  await page.evaluate(()=>scrollTo(0,Math.max(0,document.querySelector('#change-repair-panel')!.getBoundingClientRect().top+scrollY-105)));
  await page.screenshot({path:testInfo.outputPath('repair-result-390-viewport.png')});
 }
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);expect(errors).toEqual([]);
});

test('repair keeps a usable keyboard path through uncertain reads and completion',async({page})=>{
 await fixture(page);let state='preview',readFails=false,applies=0;
 const summary=()=>({id,revision,total:1,ready:1,done:state==='complete'?1:0,conflicts:0,state,items:[{path:'test/media/movie.strm',ready:true,state:state==='complete'?'recovered':state==='applying'?'intent':'preview',note:'按登记内容核验此缺失 STRM'}]});
 await page.route('**/api/changes/preview',route=>route.fulfill({json:summary()}));
 await page.route('**/api/changes/'+id,route=>readFails?route.fulfill({status:503,json:{error:'状态暂时不可用'}}):route.fulfill({json:summary()}));
 await page.route('**/api/changes/'+id+'/apply',async route=>{applies++;if(applies===1){state='applying';readFails=true;await route.abort('connectionreset')}else{state='complete';await route.fulfill({json:summary()})}});
 await page.goto('/#inventory');await expect(page.locator('[data-output-select]')).toBeVisible();
 await page.locator('[data-output-select]').focus();await page.keyboard.press('Space');
 // Reach the preview action using the browser's real sequential focus order.
 for(let i=0;i<100&&!await page.locator('#inventory-repair').evaluate(el=>el===document.activeElement);i++)await page.keyboard.press('Tab');
 await expect(page.locator('#inventory-repair')).toBeFocused();await page.keyboard.press('Enter');
 await expect(page.locator('#change-repair-title')).toBeFocused();
 await page.keyboard.press('Tab');await expect(page.locator('#change-repair-close')).toBeFocused();
 await page.keyboard.press('Tab');await expect(page.locator('#change-repair-confirm')).toBeFocused();await page.keyboard.press('Space');
 await page.keyboard.press('Tab');await expect(page.locator('#change-repair-apply')).toBeFocused();await page.keyboard.press('Enter');
 await expect(page.locator('#change-repair-error')).toContainText('请再次读取保存状态');await expect(page.locator('#change-repair-title')).toBeFocused();await expect(page.locator('#change-repair-confirm')).toBeDisabled();
 await page.keyboard.press('Tab');await page.keyboard.press('Tab');await expect(page.locator('#change-repair-recheck')).toBeFocused();
 readFails=false;await page.keyboard.press('Enter');await expect(page.locator('#change-repair-title')).toBeFocused();await expect(page.locator('#change-repair-confirm')).toBeEnabled();
 await page.keyboard.press('Tab');await page.keyboard.press('Tab');await expect(page.locator('#change-repair-confirm')).toBeFocused();await page.keyboard.press('Space');await page.keyboard.press('Tab');await page.keyboard.press('Enter');
 await expect(page.locator('#change-repair-panel')).toContainText('已核验完成 1 项');await expect(page.locator('#change-repair-title')).toBeFocused();
 await page.keyboard.press('Tab');await expect(page.locator('#change-repair-close')).toBeFocused();await page.keyboard.press('Enter');await expect(page.locator('#inventory-refresh')).toBeFocused();expect(applies).toBe(2);
});

for(const scene of ['movies','series','music'])test(`scene template ${scene} only changes an editable draft`,async({page})=>{
 await fixture(page);const writes:string[]=[];page.on('request',r=>{if(r.method()==='POST')writes.push(new URL(r.url()).pathname)});
 await page.goto('/#config');await page.locator('[data-gen-edit="rule"]').first().click();
 await page.locator('#gen-rule-output').fill('/strm/chosen-output');
 const initial=await page.locator('#filter-preset').inputValue();await page.locator('#scene-select').selectOption(scene);expect(await page.locator('#filter-preset').inputValue()).toBe(initial);
 await page.locator('#scene-apply').click();await expect(page.locator('#filter-preset')).toHaveValue(scene==='music'?'audio':'video');await expect(page.locator('#gen-rule-output')).toHaveValue('/strm/chosen-output');await expect(page.locator('#gen-rule-enabled')).not.toBeChecked();await expect(page.locator('#gen-incremental')).toBeChecked();
 expect(writes).toEqual([]);await expect(page.locator('#scene-preview')).toContainText('已应用到草稿');
});

test('changes failure preserves data and can retry',async({page})=>{
 await fixture(page);let failed=true;await page.route('**/api/changes?*',route=>failed?route.fulfill({status:503,json:{error:'读取暂时不可用'}}):route.fulfill({json:{runs:[],items:[],plans:[],page:1,pages:0,total:0,last_full:0}}));
 await page.goto('/#maintenance');await expect(page.locator('#changes-error')).toContainText('刷新观测重试');failed=false;await page.locator('#changes-refresh').click();await expect(page.locator('#changes-list')).toContainText('还没有扫描观测');
});

test('changes extension retains all palettes and tablet layout',async({page},testInfo)=>{
 await fixture(page);await page.goto('/#maintenance');await expect(page.locator('#changes-list')).toContainText('移动或改名');
 for(const [palette,name]of [['cream','米白黑黄'],['forest','雾白松绿'],['blue','云雾冰蓝'],['clay','燕麦陶橙'],['lilac','雾紫石墨'],['midnight','午夜琥珀']]){
  await page.getByRole('button',{name:'皮肤管理',exact:true}).click();await page.getByRole('button',{name,exact:true}).click();await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();await expect(page.locator('html')).toHaveAttribute('data-palette',palette);
  await page.screenshot({path:testInfo.outputPath('changes-palette-'+palette+'.png'),fullPage:true});
 }
 for(const width of [768,1280]){await page.setViewportSize({width,height:1000});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.screenshot({path:testInfo.outputPath(`changes-${width}.png`),fullPage:true})}
 await page.getByRole('button',{name:'皮肤管理',exact:true}).click();await page.locator('[data-style-choice="glass"]').click();await page.getByRole('button',{name:'关闭皮肤管理',exact:true}).click();await page.screenshot({path:testInfo.outputPath('changes-glass.png')});
});
