'use strict';
let automationData=null,automationKey='',automationPipelineKey='',automationEditing=null,automationDraft=null,automationSerial=0;
const autoState={pending:'等待目录读取',submitted:'已提交',queued:'排队中',running:'生成中',completed:'生成完成',partial:'部分失败',failed:'失败',cancelled:'已取消',interrupted:'已中断'};
const autoDelivery={pending:'等待发送 / 自动重试',sent:'已投递',failed:'发送失败',paused:'已暂停',cancelled:'已取消',not_requested:'未发送'};
$('view-settings').querySelector('.heading').insertAdjacentHTML('afterend','<nav class="automation-settings-nav" aria-label="服务设置分区"><button data-auto-section="automation-settings">Telegram 联动</button><button data-auto-section="integration-form">媒体库与通知</button><button data-auto-section="settings-form">播放入口</button><button data-auto-section="password-form">登录密码</button></nav>');
$('integration-form').insertAdjacentHTML('afterend',
 '<section id="automation-settings" class="panel form-panel automation-settings" aria-labelledby="automation-title">'+
 '<div class="section-heading"><div><h2 id="automation-title">Telegram 定向生成</h2><p class="hint">在 Hermes 私聊中发送目录命令，按对应规则生成 STRM，并接收结果通知。</p></div><button id="automation-reload" type="button">刷新状态</button></div>'+
 '<div id="automation-connection" class="automation-connection" aria-live="polite"><p class="hint">正在读取联动配置…</p></div>'+
 '<div class="actions"><button id="automation-check" type="button">检查 Hermes 连接</button><button id="automation-test" type="button">发送测试通知</button></div>'+
 '<p id="automation-error" class="error" role="alert" hidden></p>'+
 '<section class="generation-options"><div class="section-heading"><div><h3>目录命令</h3><p class="hint">例如 <code>/strm 国产剧</code>。子目录可继续写在名称后，如 <code>/strm 国产剧/具体剧名</code>。</p></div><button id="automation-add" type="button">新增目录命令</button></div>'+
 '<label class="automation-search">筛选目录命令<input id="automation-search" type="search" placeholder="名称、云端路径或输出目录"></label><div id="automation-targets" aria-busy="true"><p class="hint">正在读取目录命令…</p></div>'+
 '<form id="automation-editor" class="automation-editor" hidden><h3 id="automation-editor-title">新增目录命令</h3><div class="form-grid"><label>命令中的目录名称<input id="automation-alias" required maxlength="40" placeholder="例如：国产剧"></label><label>对应目录规则<select id="automation-rule" required></select></label><label class="automation-source-field">完整云端目录<div class="automation-path-input"><input id="automation-path" required maxlength="2048" placeholder="/media/剧集/国产剧"><button id="automation-pick" type="button">选择目录</button></div></label><label class="checkbox"><input id="automation-enabled" type="checkbox" checked>启用此目录命令</label></div><p id="automation-output" class="hint"></p><button id="automation-edit-rule" class="text" type="button">编辑对应规则的输出位置与通知</button><p id="automation-editor-error" class="error" role="alert" hidden></p><div class="form-actions"><button type="submit" class="primary">保存目录命令</button><button id="automation-editor-cancel" type="button">取消</button><button id="automation-remove" type="button" class="text">移除目录命令</button><span id="automation-editor-result" role="status"></span></div></form>'+
 '<p class="hint automation-help">机器人凭据与私聊权限由 Hermes 管理。目录配置在这里保存后立即生效；修改配置不会启动生成任务。<code>/strm</code> 查看列表，<code>/strm 状态</code> 查看任务。</p></section>'+
 '<section class="generation-options"><div class="section-heading"><h3>最近联动任务</h3><button id="automation-task-refresh" class="text" type="button">刷新任务</button></div><p class="hint">查看目录请求、生成结果与通知状态。目录读取、生成任务和通知失败分别重试。</p><div id="automation-pipeline" aria-live="polite"><p class="hint">正在读取任务…</p></div></section></section>');
$('automation-targets').before($('automation-editor'));
$('view-settings').querySelector('.automation-settings-nav').after($('automation-settings'),$('integration-form'),$('settings-form'),$('password-form'));

async function loadAutomation(options={}){
 const serial=++automationSerial;
 try{
  const result=await api('/api/automation',undefined,{signal:options.signal});if(serial!==automationSerial||options.signal?.aborted)return;
  automationData=result;$('automation-error').hidden=true;$('automation-targets').setAttribute('aria-busy','false');
  renderAutomationConnection();renderAutomationTargets();renderAutomationPipeline();
  if(typeof renderSettingsSummary==='function')renderSettingsSummary();
 }catch(e){if(e.name==='AbortError'||serial!==automationSerial||options.signal?.aborted)return;$('automation-error').textContent=e.message+(automationData?'。保留上次结果，可点击刷新状态重试。':'。请点击刷新状态重试。');$('automation-error').hidden=false;$('automation-targets').setAttribute('aria-busy','false');if(!automationData){$('automation-targets').innerHTML='<p class="hint">目录命令暂时无法读取。</p>';$('automation-pipeline').innerHTML='<p class="hint">任务状态暂时无法读取。</p>'}}
}
function renderAutomationConnection(){
 const c=automationData.connection;
 const text=!c.enabled?'尚未启用 Webhook 通知':!c.checked?'接收地址已配置，尚未检查连接':c.ok?'Hermes 接收接口可达':'Hermes 连接需要处理';
 $('automation-connection').innerHTML='<strong class="'+(c.checked&&!c.ok?'error':'')+'">'+esc(text)+'</strong><p class="hint">'+esc(c.host||'在上方 Webhook 通知中保存接收地址')+' · '+(c.signed?'已配置通知签名':'尚未配置通知签名')+(c.checked?' · 检查于 '+msDate(c.checked):'')+'</p>'+(c.note?'<p class="hint">'+esc(c.note)+'</p>':'');
 $('automation-check').disabled=!c.enabled;$('automation-test').disabled=!c.enabled;
}
function renderAutomationTargets(){
 if(!automationData)return;
 const query=$('automation-search').value.trim().toLowerCase();
 const rows=automationData.targets.filter(t=>[t.alias,t.path,t.output,t.rule_name].some(x=>(x||'').toLowerCase().includes(query)));
 const key=JSON.stringify(rows);if(automationKey===key)return;automationKey=key;
 $('automation-targets').innerHTML=rows.length?rows.map(t=>'<div class="automation-target"><div class="automation-target-copy"><strong>'+esc(t.alias)+'</strong> <span class="hint">'+(!t.enabled?'已停用':t.valid?'可用':'配置需处理')+'</span><p class="hint">云端：<code>'+esc(t.path)+'</code><br>输出：<code>'+esc(t.output||'请检查规则与路径')+'</code><br>规则：'+esc(t.rule_name||'已移除')+(t.enabled&&t.valid&&!t.notify?' · <span class="error">该规则未开启任务通知</span>':'')+'</p>'+(t.reason?'<p class="error">'+esc(t.reason)+'</p>':'')+'</div><div class="actions automation-target-actions"><button data-auto-copy="'+esc(t.alias)+'" '+(!t.enabled||!t.valid?'disabled':'')+' aria-label="复制 '+esc(t.alias)+' 的生成命令">复制命令</button><button data-auto-edit="'+esc(t.alias)+'" aria-label="编辑 '+esc(t.alias)+' 目录命令">编辑</button><button data-auto-toggle="'+esc(t.alias)+'" aria-label="'+(t.enabled?'停用':'启用')+' '+esc(t.alias)+' 目录命令">'+(t.enabled?'停用':'启用')+'</button></div></div>').join(''):'<p class="empty">'+(query?'没有匹配的目录命令，请调整筛选。':'还没有目录命令。点击“新增目录命令”，选择规则和云端目录。')+'</p>';
}
function renderAutomationPipeline(){
 const key=JSON.stringify(automationData.pipeline);if(key===automationPipelineKey)return;automationPipelineKey=key;
 $('automation-pipeline').innerHTML=automationData.pipeline.length?automationData.pipeline.map(v=>{
  const failed=['failed','partial','interrupted'].includes(v.status),count=v.job?'新增 '+v.created_files+' · 跳过 '+v.skipped+' · 失败 '+v.errors:'尚未开始生成';
  const retry=v.status==='failed'&&!v.job?'<button data-auto-event="'+esc(v.id)+'">重试目录读取</button>':failed&&v.job?'<button data-auto-job="'+esc(v.job)+'">重试生成任务</button>':'';
  const notifyRetry=v.notify_status==='failed'&&v.delivery?'<button data-auto-delivery="'+esc(v.delivery)+'">重试通知</button>':'';
  return '<article class="automation-run"><div class="automation-run-heading"><strong class="'+(failed?'error':'')+'">'+esc(autoState[v.status]||v.status)+'</strong><span class="hint">'+esc(date(v.created))+'</span></div><p class="automation-run-path">'+esc(v.path)+'</p><div class="automation-run-stages"><span>生成：'+esc(count)+'</span><span>通知：'+esc(autoDelivery[v.notify_status]||v.notify_status)+(v.attempts?' · 尝试 '+v.attempts+' 次':'')+'</span></div>'+(v.error||v.message?'<p class="'+(failed?'error':'hint')+'">'+esc(v.error||v.message)+'</p>':'')+(v.notify_error?'<p class="error">通知：'+esc(v.notify_error)+'</p>':'')+'<div class="actions">'+retry+notifyRetry+(v.job?'<button class="text" data-auto-record="'+esc(v.job)+'">查看生成记录</button>':'')+'</div></article>';
 }).join(''):'<p class="empty">尚无联动任务。在 Hermes 发送一个已配置的 /strm 目录命令后，这里显示进度与通知结果。</p>';
}
function automationOutputHint(){
 const r=ruleBy($('automation-rule').value),source=$('automation-path').value.trim();
 const within=r&&(source===r.source_path||source.startsWith(r.source_path.replace(/\/$/,'')+'/'));
 $('automation-output').textContent=r?(within?'实际输出：'+genOutputPath(r.output).replace(/\/$/,'')+source.slice(r.source_path.replace(/\/$/,'').length):'请从所选规则内选择云端目录。')+(!r.notify?' 该规则尚未开启任务通知，可编辑对应规则启用。':''):'请先选择已有目录规则。';
}
function automationEdit(alias=null){
 if(!automationData)return;
 if(!state.rules.length){toast('请先在 STRM 生成中添加目录规则');return}
 automationEditing=alias;automationDraft={revision:automationData.revision,items:automationRawTargets()};const t=automationData.targets.find(x=>x.alias===alias);
 $('automation-editor-title').textContent=t?'编辑目录命令':'新增目录命令';$('automation-alias').value=t?.alias||'';
 genSetOptions($('automation-rule'),state.rules.map(r=>({id:r.id,label:r.name+' · '+r.source_path})),t?.rule||state.rules[0].id);
 $('automation-path').value=t?.path||ruleBy($('automation-rule').value)?.source_path||'';$('automation-enabled').checked=t?t.enabled:true;
 $('automation-remove').hidden=!t;$('automation-editor-error').hidden=true;$('automation-editor-result').textContent='';$('automation-editor').hidden=false;automationOutputHint();$('automation-alias').focus({preventScroll:true});
 if($('settings-drawer')?.open)$('automation-editor').scrollIntoView({block:'start',behavior:'instant'});
 else window.scrollTo({top:Math.max(0,$('automation-editor').getBoundingClientRect().top+window.scrollY-112),behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'instant':'smooth'});
}
function automationRawTargets(){return automationData.targets.map(({alias,rule,path,enabled})=>({alias,rule,path,enabled}))}
async function automationSave(items,revision=automationData.revision){
 try{await api('/api/automation/targets',{revision,items});await loadAutomation()}
 catch(e){$('automation-editor-error').textContent=e.message;$('automation-editor-error').hidden=false;if($('settings-drawer-error')&&$('settings-drawer')?.open){$('settings-drawer-error').textContent=e.message;$('settings-drawer-error').hidden=false}throw e}
}
$('automation-editor').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{
 const t={alias:$('automation-alias').value.trim(),rule:$('automation-rule').value,path:$('automation-path').value.trim(),enabled:$('automation-enabled').checked};
 const items=automationDraft.items.filter(x=>x.alias!==automationEditing);
 if(items.some(x=>x.alias.toLowerCase()===t.alias.toLowerCase())){$('automation-editor-error').textContent='目录名称重复，请使用不同的名称';$('automation-editor-error').hidden=false;$('automation-alias').focus();return}
 items.push(t);await automationSave(items,automationDraft.revision);$('automation-editor').hidden=true;automationEditing=null;automationDraft=null;toast('目录命令已保存，Hermes 下次命令立即使用新配置');
})});
$('automation-editor-cancel').addEventListener('click',()=>{$('automation-editor').hidden=true;automationEditing=null;automationDraft=null;$('automation-add').focus()});
$('automation-remove').addEventListener('click',e=>action(e.currentTarget,async()=>{if(!confirm('移除目录命令“'+automationEditing+'”？媒体文件保留，命令本身不可恢复。'))return;await automationSave(automationDraft.items.filter(t=>t.alias!==automationEditing),automationDraft.revision);$('automation-editor').hidden=true;automationEditing=null;automationDraft=null;toast('目录命令已移除，媒体文件保留')}));
$('automation-add').addEventListener('click',()=>automationEdit());
$('automation-rule').addEventListener('change',()=>{$('automation-path').value=ruleBy($('automation-rule').value)?.source_path||'';automationOutputHint()});$('automation-path').addEventListener('input',automationOutputHint);
$('automation-search').addEventListener('input',renderAutomationTargets);
for(const id of ['automation-reload','automation-task-refresh'])$(id).addEventListener('click',e=>action(e.currentTarget,loadAutomation));
$('automation-check').addEventListener('click',e=>action(e.currentTarget,async()=>{const result=await api('/api/automation/check',{});await loadAutomation();toast(result.note)}));
$('automation-test').addEventListener('click',e=>integrationTest(e.currentTarget,'webhook','测试通知已投递到接收服务，请查看 Telegram'));
$('automation-pick').addEventListener('click',e=>action(e.currentTarget,async()=>{
 const r=ruleBy($('automation-rule').value);if(!r)throw new Error('请先选择目录规则');
 genFolderPurpose='automation';genFolderAccount=r.account;genTrail=[{id:r.folder,path:r.source_path,name:r.name}];$('gen-scope-rule-field').hidden=true;$('gen-folder-title').textContent='选择命令生成的云端目录';$('gen-folder-use').textContent='使用此目录';$('gen-folder-account').textContent=accountBy(r.account)?.name||'';$('gen-folder-dialog').showModal();await genLoadFolder();
}));
document.addEventListener('click',e=>{
 const b=e.target.closest('button');if(!b)return;
 if(b.dataset.autoSection){const target=$(b.dataset.autoSection);if(target){window.scrollTo({top:Math.max(0,target.getBoundingClientRect().top+window.scrollY-112),behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'instant':'smooth'});const heading=target.querySelector('h2');if(heading){heading.tabIndex=-1;heading.focus({preventScroll:true})}}}
 if(b.dataset.autoEdit)automationEdit(b.dataset.autoEdit);
 if(b.dataset.autoCopy)action(b,async()=>{await copy('/strm '+b.dataset.autoCopy);toast('生成命令已复制')});
 if(b.dataset.autoToggle)action(b,async()=>{const items=automationRawTargets(),t=items.find(x=>x.alias===b.dataset.autoToggle);if(!t)return;t.enabled=!t.enabled;await automationSave(items);toast(t.enabled?'目录命令已启用':'目录命令已停用')});
 if(b.dataset.autoEvent)action(b,async()=>{await api('/api/ingest/'+b.dataset.autoEvent+'/retry',{});await loadAutomation();toast('已重新排队读取该目录')});
 if(b.dataset.autoJob)action(b,async()=>{await api('/api/jobs/'+b.dataset.autoJob+'/retry',{});await refresh();await loadAutomation();toast('已重试本次生成范围')});
 if(b.dataset.autoDelivery)action(b,async()=>{await api('/api/deliveries/'+b.dataset.autoDelivery+'/retry',{});await loadAutomation();toast('通知已加入重试队列')});
 if(b.dataset.autoRecord)openConsoleRecords('generation',{job:b.dataset.autoRecord});
});
