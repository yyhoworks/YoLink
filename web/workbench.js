'use strict';
let settingsCategory='',settingsSummaryKey='',settingsReturnFocus=null;
const settingsMeta={telegram:['bell','通知通道','把任务结果发送到指定接收端'],commands:['folder-sync','目录命令','/strm 分类与子目录定向生成'],library:['tv-minimal','媒体库刷新','生成新 STRM 后通知 Emby / Jellyfin'],access:['link','STRM 链接与输出','YoLink 播放地址与实际输出目录'],security:['key-round','登录安全','管理员密码与会话'],advanced:['webhook','自动生成接入','允许整理工具或 Hermes 请求生成 STRM'],maintenance:['files','迁移维护','更新已生成播放入口，或从其他项目迁移旧 STRM']};
const wbIcon=name=>'<i data-lucide="'+name+'" aria-hidden="true"></i>';

// Reuse the existing forms and their listeners. Each form lives in one place.
$('view-settings').querySelector('.automation-settings-nav').remove();
const settingsStash=document.createElement('div');settingsStash.hidden=true;settingsStash.id='settings-stash';$('view-settings').append(settingsStash);
const advancedSettings=$('event-token').closest('section.panel');
const commandSettings=$('automation-settings').querySelector('section.generation-options');
const pipelineSettings=$('automation-pipeline').closest('section');
const telegramSettings=document.createElement('section');telegramSettings.id='telegram-settings';
const channelSettings=document.createElement('section');channelSettings.id='channel-settings';settingsStash.append(channelSettings);
telegramSettings.innerHTML='<h2>Hermes 通知连接</h2><p class="hint">生成结果发送到你已授权的 Telegram 私聊。</p>';
telegramSettings.append($('automation-connection'),$('automation-check').parentElement,$('automation-error'));
for(const el of [telegramSettings,commandSettings,$('integration-form'),$('settings-form'),$('password-form'),advancedSettings])settingsStash.append(el);
$('automation-settings').remove();
$('view-settings').insertAdjacentHTML('beforeend','<div id="settings-overview" aria-live="polite"><p class="hint">正在读取连接状态…</p></div>');
$('settings-overview').before(Object.assign(document.createElement('p'),{id:'settings-overview-error',className:'notice error',hidden:true}));
$('view-settings').querySelector('.heading').insertAdjacentHTML('beforeend','<button id="settings-refresh" type="button">'+wbIcon('refresh-cw')+'刷新状态</button>');
document.body.insertAdjacentHTML('beforeend','<dialog id="settings-drawer" class="settings-drawer" aria-labelledby="settings-drawer-title"><div class="settings-drawer-heading"><h2 id="settings-drawer-title"></h2><button id="settings-drawer-close" type="button" aria-label="关闭设置">'+wbIcon('x')+'</button></div><div id="settings-drawer-error" class="notice error" role="alert" hidden></div><div id="settings-drawer-body"></div></dialog>');
$('view-settings').querySelector('.heading p').textContent='先看连接状态，需要修改时再打开对应设置。';
const integrationSections=[...$('integration-form').querySelectorAll(':scope > .generation-options')];
const webhookSection=integrationSections[1];
webhookSection.querySelector('h3').textContent='通知连接';
const replaceDetails=document.createElement('details');replaceDetails.className='settings-replace';
replaceDetails.innerHTML='<summary>修改接收地址或通知验证密钥</summary><p class="hint">只有需要替换时才填写新值。留空保存会保留原配置。</p>';
const webhookLabels=[...webhookSection.querySelectorAll(':scope > label')];
for(const label of webhookLabels.slice(1))replaceDetails.append(label);
webhookSection.insertBefore(replaceDetails,$('webhook-test'));
const savedWebhook=document.createElement('p');savedWebhook.id='settings-webhook-saved';savedWebhook.className='settings-saved-connection';replaceDetails.before(savedWebhook);
$('integration-webhook-secret').closest('label').childNodes[0].textContent='通知验证密钥（可选）';
integrationSections[2].hidden=true;
const libraryHelp=document.createElement('p');libraryHelp.className='settings-field-help';libraryHelp.textContent='路径映射：下面两条路径必须指向同一份 STRM 文件。Emby 看到的路径以其媒体库文件夹设置为准，不能只根据 NAS 物理路径推断。';$('integration-local').closest('label').before(libraryHelp);

function closeSettingsDrawer(){
 if(typeof clearConnectionDrafts==='function')clearConnectionDrafts();
 const dialog=$('settings-drawer');
 for(const node of [...$('settings-drawer-body').children])settingsStash.append(node);
 if(dialog.open)dialog.close();settingsCategory='';settingsReturnFocus?.focus({preventScroll:true});
}
async function openSettingsDrawer(category){
 if(!settingsMeta[category])return;
 if($('settings-drawer').open)closeSettingsDrawer();
 settingsCategory=category;settingsReturnFocus=document.activeElement;
 const body=$('settings-drawer-body');$('settings-drawer-title').textContent=settingsMeta[category][1];$('settings-drawer-error').hidden=true;
 if(category==='telegram')body.append(channelSettings);
 if(category==='library')body.append($('integration-form'));
 if(category==='commands')body.append(commandSettings);
 if(category==='access')body.append($('settings-form'));
 if(category==='security')body.append($('password-form'));
 if(category==='advanced')body.append(advancedSettings);
 if(category==='library'){
  $('integration-form').querySelector(':scope > h2').hidden=true;
  $('integration-form').querySelector(':scope > p').textContent=category==='telegram'?'无需重复填写已保存的地址和密钥。每条目录规则的「生成后操作」决定任务是否通知。':'保存后，再在需要的目录规则「生成后操作」中启用媒体库刷新。';
  integrationSections[0].hidden=category!=='library';webhookSection.hidden=category!=='telegram';
  $('integration-form').querySelector('.form-actions button').textContent=category==='telegram'?'保存通知连接':'保存媒体库设置';
 }
 const dialog=$('settings-drawer');dialog.showModal();dialog.scrollTop=0;icons();
 if(typeof refreshConnectionSurface==='function')refreshConnectionSurface(category);
 try{await loadIntegrations()}catch(e){$('settings-drawer-error').textContent=e.message+'。请关闭后重试，当前输入保留。';$('settings-drawer-error').hidden=false}
}
function renderSettingsSummary(){
 if(!integrationData||!automationData)return;
 const c=integrationData.config,a=automationData,conn=a.connection;
 const rows={
  telegram:typeof notificationSummary==='function'?notificationSummary():['正在读取','muted','通知通道独立管理','管理'],
  commands:[a.targets.filter(t=>t.enabled&&t.valid).length+' 个可用命令',a.targets.some(t=>t.enabled&&!t.valid)?'warn':'good',a.targets.some(t=>t.enabled&&!t.valid)?'部分命令配置需处理':'按目录规则生成，保存后立即生效','管理'],
  library:[c.enabled?'已启用':'未启用',c.enabled?'good':'warn',c.enabled?c.base:'需要媒体服务器地址、API Key 和路径映射','配置'],
  access:[state.settings.public_base?'已设置':'尚未设置',state.settings.public_base?'good':'warn',state.settings.public_base||'填写播放器能访问的地址','查看'],
  security:['管理员密码已设置','good','修改后所有管理会话退出','修改'],
  advanced:typeof eventClientSummary==='function'?eventClientSummary():['正在读取','muted','外部程序调用后才会触发生成','管理'],
  maintenance:['入口更新与旧项目迁移','good','批量替换播放地址，或将其他项目 STRM 转为 YoLink','打开']
 };
 const row=key=>{const [i,title,help]=settingsMeta[key],[label,tone,value,action]=rows[key],attr=key==='maintenance'?'data-nav="maintenance"':'data-setting-open="'+key+'"';return '<div class="settings-state-row"><span class="settings-state-icon">'+wbIcon(i)+'</span><div><strong>'+title+'</strong><p>'+help+'</p></div><div class="settings-state-value"><span class="'+tone+'">'+esc(label)+'</span><small title="'+esc(value)+'">'+esc(value)+'</small></div><button '+attr+' aria-label="'+action+' '+title+'">'+action+wbIcon('chevron-right')+'</button></div>'};
 const latest=integrationData.deliveries[0],last=latest?'最近投递：'+(latest.kind==='library'?'媒体库刷新':latest.channel_name||'任务通知')+' · '+({sent:'已投递',pending:'等待发送 / 自动重试',failed:'发送失败',paused:'已暂停',cancelled:'已取消'}[latest.status]||latest.status)+' · 尝试 '+latest.attempts+' 次':'尚无通知投递记录';
 const key=JSON.stringify([rows,last]);if(key!==settingsSummaryKey){settingsSummaryKey=key;$('settings-overview').innerHTML='<h2 class="settings-group-title">通知与媒体联动</h2><section class="panel settings-state-list">'+['telegram','commands','library'].map(row).join('')+'</section><h2 class="settings-group-title">服务基础与高级接口</h2><section class="panel settings-state-list">'+['access','security','advanced','maintenance'].map(row).join('')+'</section><div class="settings-latest"><span>'+esc(last)+'</span><button class="text" data-open-deliveries>查看投递记录 '+wbIcon('arrow-right')+'</button></div>';icons()}
 $('settings-webhook-saved').textContent=c.has_webhook?'通知接收地址已保存'+(conn.host?' · '+conn.host:'')+'。'+(c.has_webhook_secret?'通知验证密钥已保存，无需重复填写。':'尚未配置通知验证密钥。'):'尚未配置通知接收地址。';
 const first=a.pipeline[0];$('settings-latest-task').textContent=first?'最近目录请求：'+(autoState[first.status]||first.status)+' · '+first.path:'暂无目录请求。可在 Hermes 私聊中发送已启用的 /strm 命令。';
}
const taskHint=document.createElement('p');taskHint.id='settings-latest-task';taskHint.className='settings-task-hint';commandSettings.append(taskHint);
$('settings-drawer-close').addEventListener('click',closeSettingsDrawer);
$('settings-refresh').addEventListener('click',event=>action(event.currentTarget,loadIntegrations));
$('settings-drawer').addEventListener('cancel',event=>{event.preventDefault();closeSettingsDrawer()});
$('settings-drawer').addEventListener('click',event=>{const d=$('settings-drawer');if(event.target===d&&event.clientX<d.getBoundingClientRect().left)closeSettingsDrawer()});
$('automation-edit-rule').addEventListener('click',()=>{const id=$('automation-rule').value;closeSettingsDrawer();generationEditRule(id)});

// Keep a single durable history surface, with the existing tools available inline.
const toolsPanel=document.createElement('section');toolsPanel.id='records-tools';toolsPanel.hidden=true;
for(const node of [...$('view-playback').children])if(!node.classList.contains('heading')&&!node.classList.contains('playback-history'))toolsPanel.append(node);
$('view-playback').hidden=true;
$('records-filter-form').before(toolsPanel);
$('playback-refresh').hidden=true;
for(const tab of [...document.querySelectorAll('[data-record-kind="direct"]')]){tab.dataset.recordKind='tools';tab.id='records-tools-tab';tab.textContent='诊断工具'}
$('records-tools-tab').insertAdjacentHTML('beforebegin','<button id="records-deliveries-tab" data-record-kind="deliveries" role="tab" aria-selected="false" aria-controls="records-table-panel" tabindex="-1">通知投递</button>');
const directoryHistory=document.createElement('details');directoryHistory.className='records-directory-history';directoryHistory.id='records-directory-history';directoryHistory.innerHTML='<summary>最近目录请求与失败重试</summary>';directoryHistory.append(pipelineSettings);$('records-filter-form').before(directoryHistory);directoryHistory.hidden=true;
$('view-records').querySelector('.heading p').textContent='查看文件生成、播放请求和通知投递；诊断工具也在这里。';
for(const button of document.querySelectorAll('.sidebar [data-nav="records"]')){button.setAttribute('aria-label','运行记录');button.title='运行记录';const label=button.querySelector('.nav-long');if(label)label.textContent='运行记录'}
const directLabels={pending:'等待发送',sent:'已投递',paused:'已暂停'};Object.assign(recordOutcomes,directLabels);
function syncRecordSections(){
 const delivery=recordsKind==='deliveries',tools=recordsKind==='tools';
 $('records-tools').hidden=!tools;directoryHistory.hidden=!delivery;
 for(const id of ['records-account','records-event','records-days'])$(id).closest('label').hidden=delivery;
 $('records-filter-form').classList.toggle('delivery-filter',delivery);
 $('records-search').closest('label').childNodes[0].textContent=delivery?'搜索投递任务、目录或错误':'搜索文件、路径或客户端';
 $('view-records').querySelector('.records-pagination').hidden=delivery;
 $('view-records').querySelector('.records-footer').hidden=delivery;
 if(delivery){$('records-explanation').textContent='最近 30 条持久投递结果。通知失败只重试发送，不会重新扫描或生成文件。';$('record-context').hidden=true}
 else updateRecordContext();
 if(tools)$('records-explanation').textContent='选择媒体运行仅解析或取流自检，下面保留手动诊断历史。普通播放的解析过程在播放记录详情中查看。';
}
async function loadRecordDeliveries(){
 if(!recordsScope)return;
 const request=recordsScope.latest('list');$('records-error').hidden=true;$('records-table-panel').setAttribute('aria-busy','true');
 try{
  const data=await api('/api/integrations',undefined,{signal:request.signal});if(!request.current()||recordsKind!=='deliveries')return;integrationData=data;
  const q=$('records-search').value.trim().toLowerCase(),outcome=$('records-outcome').value;
  const rows=data.deliveries.filter(d=>(!outcome||d.status===outcome)&&(!q||[d.title,d.channel_name,d.source_path,d.job,d.error,d.kind==='library'?'媒体库刷新':'任务通知'].join(' ').toLowerCase().includes(q)));
  $('records-status').textContent=rows.length+' 条匹配 · 最近 '+data.deliveries.length+' 条投递';$('records-head').innerHTML='';
  const key=JSON.stringify(['deliveries',rows]);if(recordsRenderKey!==key){recordsRenderKey=key;$('records-body').innerHTML=rows.length?rows.map(d=>'<tr class="compact-record-row"><td colspan="6"><article class="compact-record delivery-record"><div class="compact-identity">'+wbIcon(d.kind==='library'?'tv-minimal':'send')+'<div><strong>'+(d.kind==='library'?'媒体库刷新':'任务通知')+'</strong><small>'+esc((d.channel_name?d.channel_name+' · ':'')+(d.title||'生成后操作'))+'</small></div></div><div class="compact-paths"><code title="'+esc(d.source_path)+'">'+esc(d.source_path||'目录未记录')+'</code><p class="'+(d.error?'error':'hint')+'">'+'尝试 '+d.attempts+' 次'+(d.error?' · '+esc(d.error):'')+(d.status==='pending'&&d.next_at?' · 下次尝试 '+date(d.next_at):'')+'</p></div><div class="compact-tail"><span class="'+(d.status==='failed'?'error':d.status==='sent'?'good':'muted')+'">'+esc({sent:'已投递',pending:'等待发送 / 自动重试',failed:'发送失败',paused:'已暂停',cancelled:'已取消'}[d.status]||d.status)+'</span><time>'+esc(d.created?date(d.created):'时间未记录')+'</time></div><div class="compact-delivery-actions">'+(d.status==='failed'?'<button data-retry-delivery="'+esc(d.id)+'">重试投递</button>':'')+(d.job&&!d.job.startsWith('ingest:')?'<button class="text" data-auto-record="'+esc(d.job)+'">查看生成记录</button>':'')+'</div></article></td></tr>').join(''):'<tr><td class="empty" colspan="6">暂无匹配的投递记录。启用目录规则通知后，这里显示发送状态。</td></tr>';icons()}
  await loadAutomation({signal:request.signal});
 }catch(e){if(e.name!=='AbortError'&&request.current()){$('records-error').textContent=e.message+'。请刷新重试。';$('records-error').hidden=false}}
 finally{if(request.current())$('records-table-panel').removeAttribute('aria-busy');request.finish()}
}
document.addEventListener('click',event=>{
 const b=event.target.closest('button');if(!b)return;
 if(b.dataset.settingOpen)openSettingsDrawer(b.dataset.settingOpen);
 if(b.hasAttribute('data-open-deliveries')){nav('records');recordsContext={};recordsSwitch('deliveries')}
 if(b.dataset.jobExpand){const panel=[...document.querySelectorAll('[data-job-panel]')].find(el=>el.dataset.jobPanel===b.dataset.jobExpand);if(panel){panel.hidden=!panel.hidden;panel.querySelector('details').open=!panel.hidden;b.setAttribute('aria-expanded',String(!panel.hidden));b.textContent=panel.hidden?'详情':'收起'}}
});
syncRecordSections();icons();
