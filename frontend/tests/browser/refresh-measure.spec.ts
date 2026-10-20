import {test,expect} from '@playwright/test';

for(const route of ['accounts','dashboard','settings']){
 test(`measure idle refresh work on ${route}`,async({page},testInfo)=>{
  const calls:Record<string,number>={};
  page.on('request',request=>{const path=new URL(request.url()).pathname;if(path.startsWith('/api/'))calls[path]=(calls[path]||0)+1;});
  await page.clock.install({time:new Date('2026-10-20T12:00:00Z')});
  await page.goto('/#'+route);
  await expect(page.locator('#app')).toBeVisible();
  await expect(page.locator('#view-'+route)).toBeVisible();
  await page.waitForTimeout(150);
  await page.evaluate(()=>{
   const root=document.querySelector('#view-home')!;
   root.setAttribute('data-measured-mutations','0');
   new MutationObserver(records=>root.setAttribute('data-measured-mutations',String(Number(root.getAttribute('data-measured-mutations'))+records.length))).observe(root,{childList:true,subtree:true,characterData:true});
  });
  for(const path of Object.keys(calls))calls[path]=0;
  for(let i=0;i<15;i++){await page.clock.runFor(4000);await page.waitForTimeout(40);}
  const metrics={route,elapsedVirtualMs:60000,calls,hiddenGenerationMutations:Number(await page.locator('#view-home').getAttribute('data-measured-mutations'))};
  console.log('REFRESH_MEASURE',JSON.stringify(metrics));
  await testInfo.attach('refresh-measure.json',{body:JSON.stringify(metrics,null,2),contentType:'application/json'});
  if(process.env.YOLINK_REFRESH_BASELINE!=='1'){
   expect(metrics.hiddenGenerationMutations).toBe(0);
   expect(calls['/api/state']).toBeLessThanOrEqual(2);
   if(route==='settings')for(const path of ['/api/integrations','/api/automation','/api/notification-channels','/api/event-clients'])expect(calls[path]||0).toBe(0);
  }
 });
}
