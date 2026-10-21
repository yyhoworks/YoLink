'use strict';
let channelsData=null,clientsData=null,channelDraft=null,clientDraft=null,connectionLoading=false,channelListKey='',clientListKey='';
const channelTypes={hermes:'Hermes',webhook:'通用 Webhook',telegram:'Telegram Bot'};
const channelEvents={success:'生成完成',failure:'生成失败 / 需要处理'};
channelSettings.innerHTML='<p class="settings-intro">将生成结果发送到一个或多个接收端。连接名称由你定义，不会改变机器人名称；目录规则仍需开启「任务通知」。</p><div class="connection-toolbar"><h3>已保存的连接</h3><button id="channel-add" type="button">'+wbIcon('plus')+'新增连接</button></div><p id="channel-list-error" class="notice error" role="alert" hidden></p><div id="channel-list"><p class="hint">正在读取通知通道…</p></div><p id="channel-operation-result" role="status"></p><div id="channel-confirm" class="connection-confirm" hidden><p id="channel-confirm-copy"></p><div class="actions"><button id="channel-confirm-apply" type="button">删除连接</button><button id="channel-confirm-cancel" type="button">取消</button></div></div><form id="channel-editor" class="connection-editor" hidden><div class="section-heading"><h3 id="channel-editor-title">新增通知连接</h3><button id="channel-editor-close" type="button">取消</button></div><label>连接名称<input id="channel-name" maxlength="60" required placeholder="例如：Hermes 私聊、我的 Telegram"></label><small class="hint">仅用于 YoLink 内识别连接，不会修改 Telegram 机器人的名称。</small><label>连接类型<select id="channel-type"><option value="hermes">Hermes</option><option value="webhook">通用 Webhook</option><option value="telegram">Telegram Bot</option></select></label><div id="channel-webhook-fields"><label>通知接收地址<input id="channel-webhook" type="password" autocomplete="new-password" placeholder="填写接收端的完整 HTTP / HTTPS 地址"></label><p id="channel-address-state" class="hint"></p><label>通知签名<select id="channel-secret-mode"><option value="keep">保留当前设置</option><option value="replace">设置 / 替换密钥</option><option value="clear">清除密钥，不使用签名</option></select></label><p id="channel-signature-state" class="hint"></p><label id="channel-secret-field" hidden>新的签名密钥<input id="channel-secret" type="password" autocomplete="new-password"></label><p id="channel-webhook-help" class="hint"></p></div><div id="channel-telegram-fields" hidden><label>Bot Token<input id="channel-bot-token" type="password" autocomplete="new-password" placeholder="从 BotFather 获取；已有 Token 留空保留"></label><label>Chat ID<input id="channel-chat-id" type="password" autocomplete="new-password" placeholder="私聊数字 ID、群组 ID 或 @频道用户名"></label><p id="channel-telegram-state" class="hint"></p><p class="hint">私聊请先向机器人发送 /start；群组或频道需允许机器人发消息。此连接只发送通知，不接管消息接收、/strm 命令或机器人菜单。</p></div><fieldset class="channel-events"><legend>发送哪些结果</legend><label class="checkbox"><input id="channel-success" type="checkbox" checked>生成完成</label><label class="checkbox"><input id="channel-failure" type="checkbox" checked>生成失败 / 需要处理</label></fieldset><label class="checkbox"><input id="channel-enabled" type="checkbox" checked>启用此连接</label><p class="hint">停用后暂停尚未发送的通知，重新启用后继续。更换接收参数会取消旧投递，避免旧消息发给新接收端。</p><p id="channel-editor-error" class="error" role="alert" hidden></p><div class="form-actions"><button type="submit" class="primary">保存通知连接</button><span id="channel-editor-result" role="status"></span></div></form><details class="connection-help"><summary>通知与目录命令如何配合</summary><p>通知是 YoLink 向外发送任务结果；/strm 是 Hermes 或其他程序向 YoLink 提交生成请求。停用或删除通知连接，不会移除目录命令。新增 Telegram 通知连接也不会自动增加 /strm 命令。</p><p>通用 Webhook 发送 YoLink 的 JSON 格式。其他平台的专用机器人接口需要相应适配，不能直接假定兼容。</p></details>';

// The A settings surface keeps its existing form; clarify what its address does.
// Guarded the same way as workbench.js: if the settings markup changes, skip
// just that step instead of aborting the whole script while it loads.
const connStep=fn=>{try{fn()}catch{/* 设置页结构已变化，跳过这一步界面整理 */}};
connStep(()=>{const title=$('settings-form')?.querySelector('h2');if(title)title.textContent='STRM 链接地址';});
connStep(()=>{const label=$('public-base')?.closest('label');if(label?.childNodes[0])label.childNodes[0].textContent='YoLink 服务地址';});
connStep(()=>{
 const accessHints=$('settings-form')?.querySelectorAll(':scope > p.hint')||[];
 if(accessHints[0])accessHints[0].textContent='新生成的 STRM 会使用这个 YoLink 地址。Emby 8096 用于访问媒体库；这里的 8796 用于解析播放链接，两个地址各有用途。';
 if(accessHints[1])accessHints[1].textContent='Emby 和需要直接访问 STRM 的播放器应能访问此地址。YoLink 返回 302 不代表 Emby 客户端已直接向网盘取流；实际取流方式由 Emby、客户端和播放配置决定。';
 if(accessHints[2])accessHints[2].textContent='保存只影响后续生成使用的地址，不会自动改写已有 STRM。已有链接需要更新时，请在迁移维护中先预览。';
});
connStep(()=>{const outputLabel=$('output-root')?.closest('label');if(outputLabel)outputLabel.hidden=true;});
connStep(()=>$('settings-form')?.insertAdjacentHTML('beforeend','<section class="connection-output"><h3>当前规则输出位置</h3><p class="hint">这里显示规则实际使用的容器内目录。NAS 对应位置由 Docker 挂载决定。</p><div id="connection-output-rules"></div></section>'));
connStep(()=>{const submit=$('settings-form')?.querySelector('button[type=submit],button.primary');if(submit)submit.textContent='保存 STRM 链接地址';});

// Keep legacy elements and their event handlers available inside a disclosure.
const legacyEventDetails=document.createElement('details');legacyEventDetails.className='connection-help';legacyEventDetails.innerHTML='<summary>兼容凭据与原有请求工具</summary>';
if(advancedSettings){
 for(const node of [...advancedSettings.children])legacyEventDetails.append(node);
 connStep(()=>{const legacyTitle=legacyEventDetails.querySelector('h2');if(legacyTitle)legacyTitle.textContent='兼容接入凭据';});
 connStep(()=>{const legacyIntro=legacyEventDetails.querySelector('p');if(legacyIntro)legacyIntro.textContent='旧版 Hermes 或脚本可以继续使用这组凭据。新接入建议创建独立应用，便于限制目录规则和单独撤销。';});
 connStep(()=>{const legacyCopy=legacyEventDetails.querySelector('#copy-event');if(legacyCopy)legacyCopy.textContent='复制兼容接入凭据';});
 advancedSettings.innerHTML='<p class="settings-intro">整理工具或 Hermes 告诉 YoLink 哪个云端目录有新内容，YoLink 再按规则生成 STRM。通知通道负责向外发送结果，两者分别管理。</p><ol class="connection-flow"><li>工具完成云端入库</li><li>提交云端路径</li><li>YoLink 生成 STRM</li><li>按规则刷新媒体库与通知</li></ol><div class="connection-toolbar"><h3>接入应用</h3><button id="client-add" type="button">'+wbIcon('plus')+'新增应用</button></div><p id="client-list-error" class="notice error" role="alert" hidden></p><div id="client-list"><p class="hint">正在读取接入应用…</p></div><p id="client-operation-result" role="status"></p><div id="client-confirm" class="connection-confirm" hidden><p id="client-confirm-copy"></p><div class="actions"><button id="client-confirm-apply" type="button">确认撤销</button><button id="client-confirm-cancel" type="button">取消</button></div></div><form id="client-editor" class="connection-editor" hidden><div class="section-heading"><h3 id="client-editor-title">新增接入应用</h3><button id="client-editor-close" type="button">取消</button></div><label>应用名称<input id="client-name" maxlength="60" required placeholder="例如：整理工具、我的生成脚本"></label><fieldset class="client-rule-options"><legend>允许使用的目录规则</legend><div id="client-rules"></div></fieldset><label class="checkbox"><input id="client-enabled" type="checkbox" checked>允许此应用调用</label><p class="hint">每个应用使用独立凭据，只能提交所选规则内的任务，并读取这些规则的目录命令及任务状态。凭据不能登录管理页面。</p><p id="client-editor-error" class="error" role="alert" hidden></p><div class="form-actions"><button class="primary" type="submit">保存接入应用</button></div></form><section id="client-issued" class="connection-issued" hidden><h3>保存新的接入凭据</h3><p id="client-issued-copy" class="hint"></p><label>接入凭据（仅本次显示）<input id="client-issued-token" type="password" readonly autocomplete="off"></label><div class="actions"><button id="client-copy-token" type="button">复制新凭据</button><button id="client-dismiss-token" type="button">我已保存</button></div></section><details class="connection-help"><summary>生成调用示例</summary><p>填写网盘里的完整源路径，例如 /media/电影/某部电影/影片.mkv；这里不是 NAS 上的 MVStrm 输出目录。</p><label>目录规则<select id="client-example-rule"></select></label><label>路径类型<select id="client-example-kind"><option value="directory">云端目录</option><option value="file">云端文件（扫描其所在目录）</option></select></label><label>完整云端路径<input id="client-example-path" placeholder="/media/电影/某部电影"></label><pre id="client-example-code"></pre><button id="client-copy-example" type="button">复制完整调用示例</button><p class="hint">把示例中的 &lt;接入凭据&gt; 替换为对应应用的凭据。调用会提交真实生成任务；重复目录请求会合并，定时全量扫描继续补漏。</p></details><details class="connection-help"><summary>最近调用与结果</summary><div id="client-calls"></div></details>';
 advancedSettings.append(legacyEventDetails);
}

function notificationSummary(){
 if(!channelsData)return ['尚未读取','muted','点击管理可刷新通知连接','管理'];
 const enabled=channelsData.items.filter(c=>c.enabled);
 return [enabled.length+' 个已启用 / 共 '+channelsData.items.length+' 个',enabled.length?'good':'muted',enabled.map(c=>c.name).join('、')||'新增连接后选择通知方式','管理'];
}
function eventClientSummary(){
 if(!clientsData)return ['尚未读取','muted','外部程序调用后才会触发生成','管理'];
 const n=clientsData.items.filter(c=>c.enabled).length,last=clientsData.calls[0];
 return [n+' 个应用'+(clientsData.legacy.enabled?' · 兼容接入可用':''),n||clientsData.legacy.enabled?'good':'muted',last?'最近调用：'+last.name+' · HTTP '+last.status:'尚无调用记录；不会自行监控外部程序','管理'];
}
async function loadNotificationChannels(options={}){
 try{const data=await api('/api/notification-channels',undefined,{signal:options.signal});if(options.signal?.aborted)return;channelsData=data;$('channel-list-error').hidden=true;renderNotificationChannels();renderSettingsSummary()}
 catch(e){if(options.signal?.aborted||e.name==='AbortError')return;$('channel-list-error').textContent=e.message+'。可关闭后重新打开或刷新状态。';$('channel-list-error').hidden=false;throw e}
}
async function loadEventClients(options={}){
 try{const data=await api('/api/event-clients',undefined,{signal:options.signal});if(options.signal?.aborted)return;clientsData=data;$('client-list-error').hidden=true;renderEventClients();renderSettingsSummary()}
 catch(e){if(options.signal?.aborted||e.name==='AbortError')return;$('client-list-error').textContent=e.message+'。可关闭后重新打开或刷新状态。';$('client-list-error').hidden=false;throw e}
}
async function loadConnectionSettings(options={}){
 await Promise.allSettled([loadNotificationChannels(options),loadEventClients(options)]);
 if(!options.signal?.aborted)renderOutputRules();
}
function refreshConnectionSurface(category){
 if(category==='telegram')loadNotificationChannels().catch(()=>{});
 if(category==='advanced'){loadEventClients().catch(()=>{});updateClientExample()}
 if(category==='access')renderOutputRules();
}
function renderNotificationChannels(){
 const key=JSON.stringify(channelsData.items);if(key===channelListKey)return;channelListKey=key;
 $('channel-list').innerHTML=channelsData.items.length?channelsData.items.map(c=>'<article class="connection-row"><div><strong>'+esc(c.name)+'</strong><span class="connection-type">'+esc(channelTypes[c.type]||c.type)+'</span><p class="hint">'+(c.enabled?'已启用':'已停用')+' · '+(c.type==='telegram'?(c.has_bot_token&&c.has_chat_id?'Token 与目标会话已保存':'待填写 Token 与 Chat ID'):esc(c.host||'待填写接收地址')+' · '+(c.has_secret?'已配置签名':'不使用签名'))+'</p><p class="hint">'+c.events.map(v=>esc(channelEvents[v]||v)).join('、')+(c.tested?' · '+date(c.tested)+' 测试'+(c.test_ok?'已接受':'失败'):' · 尚未测试')+'</p></div><div class="actions"><button data-channel-edit="'+esc(c.id)+'" aria-label="编辑通知连接 '+esc(c.name)+'">编辑</button><button data-channel-test="'+esc(c.id)+'" '+(!c.enabled?'disabled':'')+' aria-label="测试通知连接 '+esc(c.name)+'">测试</button><button data-channel-toggle="'+esc(c.id)+'">'+(c.enabled?'停用':'启用')+'</button><button class="text" data-channel-remove="'+esc(c.id)+'" aria-label="删除通知连接 '+esc(c.name)+'">删除</button></div></article>').join(''):'<p class="empty">尚无通知连接。点击“新增连接”，命名后选择 Hermes、Webhook 或 Telegram Bot。</p>';icons();
}
function editNotificationChannel(id=''){
 if(!channelsData)return;$('channel-operation-result').textContent='';const c=channelsData.items.find(v=>v.id===id);channelDraft={id,revision:channelsData.revision};
 $('channel-editor-title').textContent=c?'编辑通知连接':'新增通知连接';$('channel-name').value=c?.name||'';$('channel-type').value=c?.type||'hermes';$('channel-type').disabled=!!c;
 for(const name of ['webhook','secret','bot-token','chat-id'])$('channel-'+name).value='';
 $('channel-signature-state').textContent=c?.has_secret?'当前已保存签名密钥。保留当前设置无需重复填写。':'当前不使用签名；可按接收端要求设置密钥。';$('channel-secret-mode').value='keep';$('channel-success').checked=c?c.events.includes('success'):true;$('channel-failure').checked=c?c.events.includes('failure'):true;$('channel-enabled').checked=c?c.enabled:true;
 $('channel-address-state').textContent=c?.has_webhook?'已有接收地址，填写新地址才会替换；留空保留。':'填写接收端提供的通知 URL。';
 $('channel-telegram-state').textContent=c?'已保存状态：'+(c.has_bot_token?'Token 已保存':'Token 未填写')+'，'+(c.has_chat_id?'目标会话已保存':'目标会话未填写')+'。留空保留现值。':'Bot Token 与 Chat ID 会加密保存。';
 $('channel-editor-error').hidden=true;$('channel-editor-result').textContent='';$('channel-confirm').hidden=true;$('channel-list').hidden=true;$('channel-add').closest('.connection-toolbar').hidden=true;$('channel-editor').hidden=false;syncChannelType();$('channel-editor').scrollIntoView({block:'start'});$('channel-name').focus({preventScroll:true});
}
function syncChannelType(){
 const type=$('channel-type').value;$('channel-webhook-fields').hidden=type==='telegram';$('channel-telegram-fields').hidden=type!=='telegram';$('channel-secret-field').hidden=$('channel-secret-mode').value!=='replace';
 $('channel-webhook-help').textContent=type==='hermes'?'填写 Hermes 的 YoLink 通知接收 URL；启用签名时，密钥须与接收端一致。这里只配置结果通知，目录命令单独管理。':'发送 YoLink 的 JSON 任务结果。接收端需要兼容此格式；其他平台的专用机器人 URL 不一定兼容。';
}
function channelSaveBody(c){return {id:c.id,name:c.name,type:c.type,enabled:c.enabled,events:c.events,revision:channelsData.revision}}
function closeChannelEditor(){channelDraft=null;$('channel-editor').hidden=true;$('channel-list').hidden=false;$('channel-add').closest('.connection-toolbar').hidden=false;for(const id of ['channel-webhook','channel-secret','channel-bot-token','channel-chat-id'])$(id).value=''}
function clearConnectionDrafts(){closeChannelEditor();closeClientEditor();$('client-issued-token').value='';$('client-issued').hidden=true;$('channel-confirm').hidden=true;$('client-confirm').hidden=true;channelRemoveID='';clientConfirmAction=null}
$('channel-editor').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{
 try{
  const events=['success','failure'].filter(v=>$('channel-'+v).checked),mode=$('channel-secret-mode').value;
  if(mode==='replace'&&!$('channel-secret').value.trim())throw Error('请选择新的签名密钥，或改为保留 / 清除');
  await api('/api/notification-channels',{id:channelDraft.id,revision:channelDraft.revision,name:$('channel-name').value.trim(),type:$('channel-type').value,enabled:$('channel-enabled').checked,events,webhook:$('channel-webhook').value.trim(),secret:mode==='replace'?$('channel-secret').value.trim():'',clear_secret:mode==='clear',bot_token:$('channel-bot-token').value.trim(),chat_id:$('channel-chat-id').value.trim()});
  closeChannelEditor();await loadNotificationChannels();$('channel-operation-result').className='good';$('channel-operation-result').textContent='通知连接已保存。目录命令与媒体库设置分别保留。';
 }catch(err){$('channel-editor-error').textContent=err.message+'。输入已保留。';$('channel-editor-error').hidden=false}
})});
$('channel-add').addEventListener('click',()=>editNotificationChannel());$('channel-editor-close').addEventListener('click',closeChannelEditor);
$('channel-type').addEventListener('change',syncChannelType);$('channel-secret-mode').addEventListener('change',syncChannelType);
let channelRemoveID='';$('channel-confirm-cancel').addEventListener('click',()=>{$('channel-confirm').hidden=true;channelRemoveID=''});
$('channel-confirm-apply').addEventListener('click',e=>action(e.currentTarget,async()=>{
 try{await api('/api/notification-channels/'+channelRemoveID+'/remove',{revision:channelsData.revision});$('channel-confirm').hidden=true;if(channelDraft?.id===channelRemoveID)closeChannelEditor();await loadNotificationChannels();$('channel-operation-result').textContent='连接与凭据已删除，未发送通知已取消；历史记录保留。'}catch(err){$('channel-confirm-copy').textContent=err.message+'。请刷新列表后重试。'}
}));

function renderOutputRules(){
 $('connection-output-rules').innerHTML=state.rules.length?state.rules.map(r=>'<div class="connection-output-row"><div><strong>'+esc(r.name)+'</strong><code>'+esc(genOutputPath(r.output))+'</code></div><button class="text" data-output-rule="'+esc(r.id)+'">编辑规则</button></div>').join(''):'<p class="hint">尚无目录规则。先在 STRM 生成中选择云端来源与输出位置。</p>';
}
function renderEventClients(){
 const key=JSON.stringify(clientsData);if(key===clientListKey)return;clientListKey=key;
 const row=c=>'<article class="connection-row"><div><strong>'+esc(c.name)+'</strong><p class="hint">'+(c.enabled?'允许调用':'已停用')+' · '+(c.last_used?'最近调用 '+date(c.last_used):'尚无调用')+'</p><p class="hint">'+c.rules.map(id=>esc(ruleBy(id)?.name||'已移除的规则')).join('、')+'</p></div><div class="actions"><button data-client-edit="'+esc(c.id)+'" aria-label="编辑接入应用 '+esc(c.name)+'">编辑</button><button data-client-rotate="'+esc(c.id)+'">更换凭据</button><button class="text" data-client-remove="'+esc(c.id)+'">撤销接入</button></div></article>';
 $('client-list').innerHTML=clientsData.items.map(row).join('')+'<article class="connection-row"><div><strong>兼容接入</strong><p class="hint">'+(clientsData.legacy.enabled?'允许调用':'已停用')+' · '+(clientsData.legacy.last_used?'最近调用 '+date(clientsData.legacy.last_used):'尚无调用记录')+'</p><p class="hint">旧版 Hermes / 脚本共用的凭据，可使用现有目录规则。停用会影响仍在使用它的命令。</p></div><button id="client-legacy-toggle" type="button">'+(clientsData.legacy.enabled?'停用兼容接入':'启用兼容接入')+'</button></article>';
 $('client-calls').innerHTML=clientsData.calls.length?clientsData.calls.map(c=>'<div class="connection-call"><strong>'+esc(c.name)+'</strong><code>'+esc(c.action)+'</code><span class="'+(c.status>=400?'error':'good')+'">HTTP '+c.status+'</span><time>'+date(c.created)+'</time></div>').join(''):'<p class="hint">尚无调用记录。发送新请求后，这里显示来源、时间和 HTTP 结果。</p>';
 updateClientExample();
}
function editEventClient(id=''){
 const c=clientsData?.items.find(v=>v.id===id);clientDraft={id};$('client-editor-title').textContent=c?'编辑接入应用':'新增接入应用';$('client-name').value=c?.name||'';$('client-enabled').checked=c?c.enabled:true;
 $('client-rules').innerHTML=state.rules.length?state.rules.map(r=>'<label class="checkbox"><input type="checkbox" name="client-rule" value="'+esc(r.id)+'" '+(!c||c.rules.includes(r.id)?'checked':'')+'>'+esc(r.name)+' <small>'+esc(r.source_path)+'</small></label>').join(''):'<p class="hint">请先添加目录规则。</p>';
 $('client-editor-error').hidden=true;$('client-list').hidden=true;$('client-add').closest('.connection-toolbar').hidden=true;$('client-editor').hidden=false;$('client-editor').scrollIntoView({block:'start'});$('client-name').focus({preventScroll:true});
}
function closeClientEditor(){clientDraft=null;$('client-editor').hidden=true;$('client-list').hidden=false;$('client-add').closest('.connection-toolbar').hidden=false}
function showIssuedToken(token,name){$('client-issued-token').value=token;$('client-issued-copy').textContent=name+'：请复制到该应用。关闭后无法再次查看；更换凭据会使旧凭据失效。';$('client-issued').hidden=false;$('client-issued').scrollIntoView({block:'start'})}
$('client-add').addEventListener('click',()=>editEventClient());$('client-editor-close').addEventListener('click',closeClientEditor);
$('client-editor').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{try{
 const result=await api('/api/event-clients',{id:clientDraft.id,name:$('client-name').value.trim(),enabled:$('client-enabled').checked,rules:[...$('client-rules').querySelectorAll('input:checked')].map(el=>el.value)});closeClientEditor();if(result.token)showIssuedToken(result.token,$('client-name').value.trim());else $('client-operation-result').textContent='接入应用已保存。';await loadEventClients().catch(()=>{});
 }catch(err){$('client-editor-error').textContent=err.message+'。输入已保留。';$('client-editor-error').hidden=false}})});
$('client-copy-token').addEventListener('click',e=>action(e.currentTarget,async()=>{await copy($('client-issued-token').value);$('client-issued-copy').textContent='新凭据已复制。请保存到对应应用。'}));
$('client-dismiss-token').addEventListener('click',()=>{$('client-issued-token').value='';$('client-issued').hidden=true});
let clientConfirmAction=null;
function confirmClient(text,label,fn){$('client-confirm-copy').textContent=text;$('client-confirm-apply').textContent=label;clientConfirmAction=fn;$('client-confirm').hidden=false;$('client-confirm').scrollIntoView({block:'center'})}
$('client-confirm-cancel').addEventListener('click',()=>{$('client-confirm').hidden=true;clientConfirmAction=null});
$('client-confirm-apply').addEventListener('click',e=>action(e.currentTarget,async()=>{try{await clientConfirmAction();$('client-confirm').hidden=true;clientConfirmAction=null;await loadEventClients()}catch(err){$('client-confirm-copy').textContent=err.message+'。请刷新后重试。'}}));
function updateClientExample(){
 const el=$('client-example-rule'),old=el.value;el.innerHTML=state.rules.map(r=>'<option value="'+esc(r.id)+'">'+esc(r.name)+'</option>').join('');if(old)el.value=old;
 const rule=el.value,source=$('client-example-path').value.trim()||ruleBy(rule)?.source_path||'/media/电影/某部电影';
 const quote=v=>"'"+v.replace(/'/g,"'\\''")+"'";
 $('client-example-code').textContent='curl -X POST '+quote((state.settings.public_base||location.origin)+'/api/events')+' \\\n  -H '+quote('Authorization: Bearer <接入凭据>')+' \\\n  -H '+quote('Content-Type: application/json')+' \\\n  --data '+quote(JSON.stringify({rule,path:source,kind:$('client-example-kind').value}));
}
for(const id of ['client-example-rule','client-example-kind','client-example-path'])$(id).addEventListener(id.endsWith('path')?'input':'change',updateClientExample);
$('client-copy-example').addEventListener('click',e=>action(e.currentTarget,async()=>{updateClientExample();await copy($('client-example-code').textContent);$('client-operation-result').textContent='调用示例已复制；请替换其中的接入凭据。'}));

document.addEventListener('click',e=>{
 const b=e.target.closest('button');if(!b)return;
 if(b.dataset.channelEdit)editNotificationChannel(b.dataset.channelEdit);
 if(b.dataset.channelTest)action(b,async()=>{const el=$('channel-operation-result');el.className='hint';el.textContent='正在向接收端发送测试通知…';el.scrollIntoView({block:'nearest'});try{const v=await api('/api/notification-channels/'+b.dataset.channelTest+'/test',{});el.className='good';el.textContent=(channelsData.items.find(c=>c.id===b.dataset.channelTest)?.name||'通知连接')+'：'+v.note}catch(err){el.className='error';el.textContent=err.message}await loadNotificationChannels();el.scrollIntoView({block:'nearest'})});
 if(b.dataset.channelToggle)action(b,async()=>{try{const c=channelsData.items.find(v=>v.id===b.dataset.channelToggle);await api('/api/notification-channels',{...channelSaveBody(c),enabled:!c.enabled});await loadNotificationChannels();$('channel-operation-result').className='good';$('channel-operation-result').textContent=c.enabled?'连接已停用，待发通知已暂停。':'连接已启用，已暂停通知继续处理。'}catch(err){$('channel-operation-result').className='error';$('channel-operation-result').textContent=err.message}});
 if(b.dataset.channelRemove){const c=channelsData.items.find(v=>v.id===b.dataset.channelRemove);if(!c)return;channelRemoveID=c.id;$('channel-confirm-copy').textContent='删除「'+c.name+'」及其保存的凭据？未发送通知将取消，历史记录和目录命令保留。';$('channel-confirm').hidden=false;$('channel-confirm').scrollIntoView({block:'center'})}
 if(b.dataset.outputRule){closeSettingsDrawer();generationEditRule(b.dataset.outputRule)}
 if(b.dataset.clientEdit)editEventClient(b.dataset.clientEdit);
 if(b.dataset.clientRotate){const c=clientsData.items.find(v=>v.id===b.dataset.clientRotate);confirmClient('为「'+c.name+'」更换凭据？旧凭据立即失效，请随后更新对应应用。','更换凭据',async()=>{const v=await api('/api/event-clients/'+c.id+'/rotate',{});showIssuedToken(v.token,c.name)})}
 if(b.dataset.clientRemove){const c=clientsData.items.find(v=>v.id===b.dataset.clientRemove);confirmClient('撤销「'+c.name+'」的接入？凭据将失效，已提交的生成任务继续执行。','撤销接入',async()=>{await api('/api/event-clients/'+c.id+'/remove',{});if(clientDraft?.id===c.id){clientDraft=null;$('client-editor').hidden=true}$('client-operation-result').textContent='应用接入已撤销，调用历史保留。'})}
 if(b.id==='client-legacy-toggle'){const enabled=clientsData.legacy.enabled;confirmClient(enabled?'停用兼容接入会阻止仍使用旧凭据的 Hermes /strm 命令和脚本提交新任务。通知连接单独管理。':'重新允许旧版 Hermes / 脚本使用兼容凭据调用？',enabled?'停用兼容接入':'启用兼容接入',async()=>{await api('/api/event-clients/legacy',{enabled:!enabled})})}
});
renderOutputRules();icons();
