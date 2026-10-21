/* YoLink live UI: all displayed accounts, jobs and results come from the API. */
'use strict';
import { esc, jobClass } from '../shared/presentation.ts';
import { createAPI } from '../core/api/client.ts';
import { createCleanupScope } from '../core/lifecycle/cleanup-scope.ts';
const requestAPI=createAPI({fetch:(...args)=>fetch(...args),onUnauthorized:()=>showLogin()});
const $=id=>document.getElementById(id);
const names={'139':'移动云盘 · 新个人云','139old':'移动云盘 · 旧个人云',quark:'夸克网页 · Cookie',quarktv:'夸克 TV · 原文件',ali:'阿里云盘 · OpenAPI','115':'115 网盘 · Cookie'};
const statusNames={queued:'排队中',running:'执行中',completed:'已完成',partial:'部分失败',failed:'失败',cancelled:'已取消',interrupted:'已中断'};
let state={accounts:[],rules:[],jobs:[],media:[],settings:{}},view='dashboard',polling=false,toastTimer;
const refreshLifecycle={scope:createCleanupScope(),timer:null,last:0,failures:0,epoch:0,initialized:false};
// The event token no longer travels in /api/state. It is fetched on demand
// from its dedicated admin endpoints and cached for the settings surface.
let eventTokenCache=null,eventTokenRequest=null;
function ensureEventToken(){
 if(eventTokenCache!==null)return Promise.resolve(eventTokenCache);
 if(!eventTokenRequest)eventTokenRequest=api('/api/settings/event-token').then(data=>{eventTokenCache=data&&data.event_token||'';const field=$('event-token');if(field)field.value=eventTokenCache;return eventTokenCache;}).finally(()=>{eventTokenRequest=null;});
 return eventTokenRequest;
}
const routes=new Set(['dashboard','inventory','notifications','more','home','config','accounts','files','directories','tasks','playback','maintenance','settings','records']);
let routeReady=false,activeRoute='';
function writeRoute(route,replace=false){activeRoute=route;const hash='#'+route;if(location.hash!==hash)history[replace||!routeReady?'replaceState':'pushState'](null,'',hash)}
function restoreRoute(){const route=location.hash.slice(1);if(routes.has(route)&&route!==activeRoute)nav(route,true,{}, {restore:true})}
addEventListener('popstate',restoreRoute);addEventListener('hashchange',restoreRoute);
const icons=()=>{
 if(!window.lucide)return;
 const pending=[...document.querySelectorAll('i[data-lucide]')].filter(el=>!el.closest('[hidden]'));
 if(!pending.length)return;
 for(const el of pending){el.setAttribute('data-lucide-pending',el.getAttribute('data-lucide'));el.removeAttribute('data-lucide');}
 lucide.createIcons({nameAttr:'data-lucide-pending'});
 for(const el of document.querySelectorAll('[data-lucide-pending]')){el.setAttribute('data-lucide',el.getAttribute('data-lucide-pending'));el.removeAttribute('data-lucide-pending');}
};
const toast=message=>{$('toast').textContent=message;$('toast').hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('toast').hidden=true,4500)};
async function api(path,body,options={}){return requestAPI(path,body,options)}
async function action(button,fn){button.disabled=true;button.setAttribute('aria-busy','true');try{await fn()}catch(e){toast(e.message);$('global-error').textContent=e.message;$('global-error').hidden=false}finally{button.disabled=false;button.removeAttribute('aria-busy');if(!$('app').hidden&&view==='home')preserveControlFocus(renderGeneration);if(typeof updateOutputSelection==='function'&&view==='inventory')updateOutputSelection();if(!button.isConnected) return;icons()}}
function showLogin(){refreshLifecycle.initialized=true;refreshLifecycle.epoch++;refreshLifecycle.scope.cancel('state');clearTimeout(refreshLifecycle.timer);if(typeof consoleLeave==='function')consoleLeave();if(typeof reliabilityLeave==='function')reliabilityLeave();if(typeof backupLeave==='function')backupLeave();recordsLeave();if(typeof closeSettingsDrawer==='function'&&$('settings-drawer')?.open)closeSettingsDrawer();$('login').hidden=false;$('app').hidden=true}
function showApp(){$('login').hidden=true;$('app').hidden=false}
function nav(next,loadFiles=true,context={},options={}){
 if(next==='playback'){next='records';recordsKind='tools'}
 if(typeof closeSettingsDrawer==='function'&&$('settings-drawer')?.open)closeSettingsDrawer();
 const requested=next;
 recordsLeave();if(next==='records'&&typeof recordsContext!=='undefined'){recordsContext={};if(typeof updateRecordContext==='function')updateRecordContext()}
 const generationTarget=next==='config'||next==='directories'?'config':'status';
 if(['config','directories','tasks'].includes(next))next='home';
 if(typeof reliabilityLeave==='function'&&view!==next)reliabilityLeave(next);
 const previousView=view;view=next;$('global-error').hidden=true;
 if(typeof backupLeave==='function'&&next!=='settings')backupLeave();
 if(previousView!==next&&typeof consoleLeave==='function')consoleLeave();
 if(previousView==='maintenance'&&previousView!==next&&typeof changesLeave==='function')changesLeave();
 document.querySelectorAll('.view').forEach(el=>el.hidden=el.id!=='view-'+next);
 document.querySelectorAll('.sidebar [data-nav]').forEach(el=>{const selected=el.dataset.nav===next,parent=el.dataset.nav==='more'&&['files','inventory','records','notifications','maintenance','settings'].includes(next);el.classList.toggle('active',selected||parent);if(selected)el.setAttribute('aria-current','page');else if(parent)el.setAttribute('aria-current','true');else el.removeAttribute('aria-current')});
 $('location').textContent={dashboard:'仪表盘',inventory:'生成文件',notifications:'通知',more:'更多',home:'STRM 生成',accounts:'网盘管理',files:'文件浏览',playback:'诊断工具',settings:'服务设置',maintenance:'迁移维护',records:'运行记录'}[next]||'工作台';
 if(next==='home')generationTab(requested==='tasks'||options.restore&&requested==='home'?'status':['config','directories'].includes(requested)?'config':localStorage.getItem('yolink-gen-tab')||generationTarget,{write:false});
 writeRoute(next==='home'&&genTab==='config'?'config':next,options.replace);routeReady=true;
 render();scheduleRefresh();
 if(next==='files'&&loadFiles)ensureCloudFiles();if(next==='records')recordsOpen();if(next==='maintenance'&&typeof changesOpen==='function')changesOpen();if(typeof consoleNav==='function')consoleNav(next,context);document.title='YoLink · '+$('location').textContent;if(previousView!==next)window.scrollTo({top:0,behavior:'instant'});icons();
}
function refreshDelay(){
 const live=(view==='dashboard'||view==='home'&&genTab==='status')&&state.jobs.some(genActive);
 return Math.min(60000,(live?4000:30000)*2**Math.min(refreshLifecycle.failures,3));
}
function scheduleRefresh(){
 clearTimeout(refreshLifecycle.timer);
 if($('app').hidden||document.hidden)return;
 refreshLifecycle.timer=setTimeout(async()=>{
  if(refreshLifecycle.scope.pending('state')){scheduleRefresh();return;}
  try{await refresh({background:true});}
  catch(error){if(error.name!=='AbortError'&&!$('app').hidden){$('global-error').textContent=error.message+'。保留上次数据，稍后自动重试。';$('global-error').hidden=false;}}
 },refreshDelay());
}
async function refresh(options={}){
 if(options.background&&($('app').hidden||document.hidden))return false;
 const request=refreshLifecycle.scope.latest('state'),epoch=refreshLifecycle.epoch;
 clearTimeout(refreshLifecycle.timer);polling=true;
 try{
  const data=await api('/api/state',undefined,{signal:request.signal});
  if(!request.current()||epoch!==refreshLifecycle.epoch)return false;
  state=data;refreshLifecycle.initialized=true;refreshLifecycle.last=Date.now();showApp();render();
  if(typeof consoleResume==='function')consoleResume();
  const complete=typeof refreshConsole!=='function'||await refreshConsole();
  if(!request.current()||epoch!==refreshLifecycle.epoch)return false;
  if(complete){refreshLifecycle.failures=0;$('global-error').hidden=true;}else refreshLifecycle.failures++;
  return true;
 }catch(error){if(!request.current()||epoch!==refreshLifecycle.epoch||error.name==='AbortError')return false;refreshLifecycle.failures++;throw error;}
 finally{if(request.current()){request.finish();polling=false;scheduleRefresh();}}
}
// Preserve identity, not stale HTML or field values, across list updates.
function preserveControlFocus(update){
 const focused=document.activeElement;
 const anchor=focused?.matches('summary')?focused.closest('details'):focused;
 const id=anchor?.id,scopeID=anchor?.parentElement?.closest('[id]')?.id,data=anchor?[...anchor.attributes].filter(a=>a.name.startsWith('data-')&&!['data-glass-adapted','data-glass-role'].includes(a.name)).map(a=>[a.name,a.value]):[];
 update();
 if(!focused||focused.isConnected||(!id&&!data.length))return;
 const container=scopeID?$(scopeID):document;
 const replacement=id?$(id):[...(container?.querySelectorAll(anchor.tagName)||[])].find(el=>data.every(([name,value])=>el.getAttribute(name)===value));
 const target=focused.matches('summary')?replacement?.querySelector('summary'):replacement;
 if(target&&!target.closest('[hidden]'))target.focus({preventScroll:true});
}
const accountBy=id=>state.accounts.find(a=>a.id===id);
const ruleBy=id=>state.rules.find(r=>r.id===id);
const date=t=>new Date(t*1000).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'});
function render(){
 if(typeof backupRender==='function')backupRender();
 preserveControlFocus(()=>{
  if(view==='home'){renderGeneration();renderIngestHistory();}
  if(view==='accounts'||view==='files')renderCloudManagement();
  if(view==='records'&&recordsKind==='tools'){renderPlaybackTools();renderPlaybackMedia();}
  if(view==='maintenance')renderMaintenanceTools();
  if(view==='settings'){
   if(!$('settings-drawer')?.open&&document.activeElement!==$('public-base'))$('public-base').value=state.settings.public_base||'';
   $('output-root').value=state.settings.output_root||'';
   if(eventTokenCache===null)ensureEventToken().catch(()=>{});else $('event-token').value=eventTokenCache;
   if(!$('settings-drawer')?.open)renderLegacyEventOptions();
   if(typeof renderSettingsSummary==='function')renderSettingsSummary();
  }
 });icons();
}
function renderPlaybackMedia(){
 const list=$('media-list'),rows=new Map([...list.querySelectorAll('[data-media-id]')].map(row=>[row.dataset.mediaId,row]));
 if(!state.media.length){if(!list.querySelector('.empty'))list.innerHTML='<div class="empty">生成 STRM 后，这里会列出最近 12 个媒体。</div>';return}
 list.querySelector('.empty')?.remove();
 const focused=document.activeElement,focusedID=focused?.closest('[data-media-id]')?.dataset.mediaId,actionName=['data-probe','data-resolve','data-copy-url','data-playlist-media'].find(name=>focused?.hasAttribute(name));
 let cursor=list.firstElementChild;
 for(const m of state.media){
  let row=rows.get(m.id);if(!row){row=document.createElement('div');row.className='list-row';row.dataset.mediaId=m.id}
  const account=accountBy(m.account)?.name||'',signature=JSON.stringify([m.name,m.path,m.url,account]);
  if(row.dataset.signature!==signature){row.dataset.signature=signature;row.innerHTML=`<div><strong>${esc(m.name)}</strong><p>${esc(account)} · ${esc(m.path)}</p></div><div class="actions"><button data-probe="${esc(m.id)}">取流自检</button><button data-resolve="${esc(m.id)}">仅解析</button><button data-copy-url="${esc(m.url)}">复制播放入口</button><button data-playlist-media="${esc(m.id)}">下载 M3U</button></div>`}
  if(row!==cursor)list.insertBefore(row,cursor);cursor=row.nextElementSibling;rows.delete(m.id);
 }
 for(const row of rows.values())row.remove();
 if(focusedID&&actionName&&document.activeElement!==focused){const row=[...list.children].find(el=>el.dataset.mediaId===focusedID);row?.querySelector('['+actionName+']')?.focus({preventScroll:true})}
}
const credentialSchema={
 '115':[['cookie','115 登录 Cookie','完整 Cookie，包含 UID、CID、SEID，可带 KID']],
 '139':[['authorization','Authorization','导入 Basic 后面的完整字符串']],
 '139old':[['authorization','Authorization','导入 Basic 后面的完整字符串']],
 quark:[['cookie','网页登录 Cookie','从已登录的 pan.quark.cn 请求中复制']],
 quarktv:[['access_token','TV Access Token','与夸克网页 Cookie 不通用'],['device_id','设备 ID','导入令牌所对应的设备 ID'],['refresh_token','TV Refresh Token（可选）','配置续期服务时使用'],['refresh_endpoint','HTTPS 续期接口（可选）','自行提供可信服务；默认不调用外部续期平台']],
 ali:[['access_token','OpenAPI Access Token','与网页登录令牌不通用'],['refresh_token','OpenAPI Refresh Token（可选）','自有应用的刷新令牌'],['client_id','Client ID（可选）','自己的开放平台应用 ID'],['client_secret','Client Secret（可选）','自己的开放平台应用密钥']]
};
const providerNotes={
 '115':'导入 115 登录 Cookie，默认根目录 ID 为 0。支持目录浏览、容量查询、STRM 生成和原文件 302；取链沿用播放器 User-Agent。若播放器取流受限，可勾选服务器中转（媒体流量经过服务器）。Cookie 失效后请重新导入。',
 '139':'直接连接移动云盘个人云。首版导入网页登录后的 Authorization；不提供扫码、短信登录或家庭云。若目录为空或报错，可检查是否选对新旧个人云。',
 '139old':'使用旧个人云目录接口。Authorization 与新个人云一致；默认根目录 ID 为 root。',
 quark:'网页 Cookie 通道的原文件通常要求 Cookie 和 Referer。生成 STRM 可用；播放需勾选允许中转，服务器将承担媒体流量。',
 quarktv:'使用 TV 原文件下载通道，返回 302。先导入有效 TV 令牌及对应设备 ID；续期接口为可选外部依赖，仅在填写后使用。未提供扫码授权。',
 ali:'连接阿里官方 OpenAPI。导入有效访问令牌与 drive_id；提供自己的应用凭据可自动续期。此版不借用公共授权应用，也不支持网页登录令牌。'
};
function providerFields(){const p=$('provider').value;$('credential-fields').innerHTML=credentialSchema[p].map(([key,label,placeholder])=>`<label>${esc(label)}<input data-secret="${key}" type="${['device_id','refresh_endpoint','client_id'].includes(key)?'text':'password'}" autocomplete="off" placeholder="${esc(placeholder)}"></label>`).join('');$('provider-note').textContent=providerNotes[p];$('drive-field').hidden=p!=='ali';$('account-drive').required=p==='ali';$('proxy-field').hidden=!['quark','115'].includes(p);}
function resetAccount(){$('account-form').reset();$('account-id').value='';$('provider').disabled=false;$('account-drive').disabled=false;$('account-form-title').textContent='添加网盘';$('account-result').textContent='';providerFields()}
async function copy(text){if(navigator.clipboard?.writeText){await navigator.clipboard.writeText(text);return};const area=document.createElement('textarea');area.value=text;document.body.append(area);area.select();const ok=document.execCommand('copy');area.remove();if(!ok)throw new Error('浏览器禁止复制，请在 HTTPS 或 localhost 页面使用此操作')}
document.addEventListener('click',e=>{const b=e.target.closest('button[data-nav]');if(b)nav(b.dataset.nav);const el=e.target.closest('button');if(!el)return;
 if(el.dataset.resolve)action(el,async()=>{const r=await api('/api/resolve/'+el.dataset.resolve);$('resolve-result').hidden=false;$('resolve-result').textContent=`解析方式：${r.mode==='302'?'302 直连':r.mode==='proxy'?'服务器中转':'当前禁止中转，播放将被拒绝'}；目标：${r.target_host}。${r.note}`});
 if(el.dataset.copyUrl)action(el,async()=>{if(!state.settings.public_base)throw new Error('请先设置播放入口');await copy(state.settings.public_base+el.dataset.copyUrl);toast('播放入口已复制')});
});
$('login-form').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{try{await api('/api/login',{password:$('password').value});$('password').value='';$('login-error').textContent='';$('login-notice').hidden=true;await refresh();nav(view)}catch(e){$('login-error').textContent=e.message;throw e}})});
$('logout').addEventListener('click',e=>action(e.currentTarget,async()=>{await api('/api/logout',{});showLogin()}));
$('logout-settings').addEventListener('click',e=>action(e.currentTarget,async()=>{await api('/api/logout',{});showLogin()}));
document.querySelector('.brand').addEventListener('click',e=>{e.preventDefault();nav('dashboard')});
$('provider').addEventListener('change',providerFields);$('reset-account').addEventListener('click',resetAccount);
$('account-form').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{const secrets={};document.querySelectorAll('[data-secret]').forEach(el=>{if(el.value.trim())secrets[el.dataset.secret]=el.value.trim()});const saved=await api('/api/accounts',{id:$('account-id').value,name:$('account-name').value.trim(),provider:$('provider').value,root:$('account-root').value.trim(),drive:$('account-drive').value.trim(),allow_proxy:$('allow-proxy').checked,secrets});document.querySelectorAll('[data-secret]').forEach(el=>el.value='');$('account-id').value=saved.id;$('provider').disabled=true;$('account-drive').disabled=true;$('account-form-title').textContent='编辑 '+saved.name;await refresh();$('account-form').hidden=true;toast('账号已保存，可点击连接测试或浏览文件')})});
$('settings-form').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{await api('/api/settings',{public_base:$('public-base').value.trim()});await refresh();$('settings-result').textContent='已保存，之后生成的 STRM 将使用这个地址。';toast('播放入口已保存')})});
$('copy-event').addEventListener('click',e=>action(e.currentTarget,async()=>{await copy(await ensureEventToken());toast('兼容接入凭据已复制')}));
$('rotate-event').addEventListener('click',e=>action(e.currentTarget,async()=>{const data=await api('/api/settings/event-token/rotate',{});eventTokenCache=data&&data.event_token||null;if(eventTokenCache===null)await ensureEventToken();else $('event-token').value=eventTokenCache;toast('凭据已轮换，旧凭据已失效，请同步更新 Hermes 等接入方')}));
document.addEventListener('visibilitychange',()=>{
 if(document.hidden){clearTimeout(refreshLifecycle.timer);refreshLifecycle.scope.cancel('state');if(typeof consoleLeave==='function')consoleLeave();return;}
 if(!refreshLifecycle.initialized||!$('app').hidden)refresh().then(applied=>{if(applied&&!routeReady)nav(view,true,{}, {replace:true,restore:true});if(applied&&typeof consoleResume==='function')consoleResume();}).catch(error=>{if(!refreshLifecycle.initialized)showLogin();else if(error.name!=='AbortError'){$('global-error').textContent=error.message;$('global-error').hidden=false;}});
});
document.querySelectorAll('.sidebar button').forEach(el=>{const label=(el.querySelector('.nav-long')||el).textContent.trim();el.setAttribute('aria-label',label);el.setAttribute('title',label)});
providerFields();icons();
const initial=location.hash.slice(1);if(routes.has(initial))view=initial;
refresh().then(applied=>{if(applied)nav(view,true,{}, {replace:true,restore:true})}).catch(()=>showLogin());

$('password-form').addEventListener('submit',event=>{event.preventDefault();const error=$('password-change-error');error.hidden=true;error.textContent='';
 if($('new-password').value!==$('confirm-password').value){error.textContent='两次输入的新密码不一致';error.hidden=false;$('confirm-password').focus();return}
 action(event.submitter,async()=>{try{await api('/api/password',{current_password:$('current-password').value,new_password:$('new-password').value,confirm_password:$('confirm-password').value});eventTokenCache=null;$('password-form').reset();showLogin();$('login-notice').textContent='密码已修改，请使用新密码重新登录。事件令牌已同步轮换，请更新 Hermes 等接入方的凭据。';$('login-notice').hidden=false;$('password').value='';$('password').focus()}catch(e){error.textContent=e.message;error.hidden=false}});
});
