/* Manual encrypted backup/recovery. Status reads on entry; no automatic writes. */
'use strict';
let backupContext=null,backupUncertain=false;
const backupPanel=document.createElement('section');
backupPanel.id='backup-panel';backupPanel.className='panel backup-panel';backupPanel.setAttribute('aria-labelledby','backup-title');
backupPanel.innerHTML=`
 <div class="section-heading"><h2 id="backup-title">备份与恢复</h2><button id="backup-refresh" type="button">刷新恢复状态</button></div>
 <p class="hint">手动保存或恢复 YoLink 的账号、规则、密钥和应用数据。网盘媒体与外部输出目录不包含在备份中。</p>
 <p id="backup-status" role="status">正在读取恢复状态…</p><p id="backup-candidate" hidden></p><p id="backup-error" role="alert" hidden></p>
 <div id="backup-state-actions" class="actions"></div>
 <form id="backup-confirm-form" class="backup-confirm" hidden>
  <h3 id="backup-confirm-title" tabindex="-1"></h3><p id="backup-confirm-effects"></p><p id="backup-confirm-target" class="hint"></p>
  <label>当前管理员密码<input id="backup-action-admin" type="password" autocomplete="current-password" required></label>
  <label id="backup-confirm-word-label">确认文字<input id="backup-confirm-word" autocomplete="off" spellcheck="false"></label>
  <div class="actions"><button id="backup-confirm-submit" type="submit" class="primary"></button><button id="backup-confirm-cancel" type="button">返回</button></div>
 </form>
 <details id="backup-export-details"><summary>导出加密备份</summary>
  <p>加密文件可保存到其他设备。请单独保管备份口令；遗失口令后无法解密。</p>
  <form id="backup-export-form">
   <label>当前管理员密码<input id="backup-export-admin" type="password" autocomplete="current-password" required></label>
   <label>独立备份口令<input id="backup-export-password" type="password" autocomplete="new-password" required minlength="12" maxlength="1024" aria-describedby="backup-password-hint"></label>
   <label>再次输入备份口令<input id="backup-export-repeat" type="password" autocomplete="new-password" required></label>
   <p id="backup-password-hint" class="hint">使用至少 12 字节的独立长口令。备份包含敏感凭据，请妥善保存。</p>
   <div class="actions"><button id="backup-export" class="primary" type="submit">生成并下载加密备份</button></div>
  </form>
 </details>
 <details id="backup-import-details"><summary>从加密备份恢复</summary>
  <p>先上传并校验，确认后再重启服务替换应用数据。恢复后自动工作暂停，核对完成并再次重启后才恢复运行。</p>
  <p class="hint">恢复时需同时容纳候选数据、原数据副本、写入临时文件和回退空间预留。请先在 NAS 上留足空间；空间不足时会停止操作并保留原数据或回退资料。历史恢复资料会保留，请另行规划容量。</p>
  <p id="backup-platform" class="hint"></p>
  <form id="backup-import-form">
   <label>加密备份文件<input id="backup-file" type="file" accept=".age" required></label>
   <label>备份文件的口令<input id="backup-import-password" type="password" autocomplete="off" required></label>
   <label>当前管理员密码<input id="backup-import-admin" type="password" autocomplete="current-password" required></label>
   <div class="actions"><button id="backup-import" type="submit" class="primary">上传并校验备份</button></div>
  </form>
 </details>
 <p id="backup-result" role="status"></p><button id="backup-abort" type="button" hidden>停止等待并刷新状态</button>`;
$('view-settings').querySelector('.heading').after(backupPanel);
const backupHold=document.createElement('section');backupHold.className='notice backup-hold';backupHold.id='backup-hold';backupHold.hidden=true;
backupHold.innerHTML='<span>恢复保护中：自动任务和正常播放已暂停。请核对恢复资料后完成确认。</span><button type="button" data-nav="settings">核对恢复资料</button>';
$('main').prepend(backupHold);

const backupStateNames={pending:'已确认，等待重启执行恢复',prepared:'恢复副本已准备，等待重启',installing:'恢复处理中，需重启完成保护',installed:'恢复已完成，等待核对',rollback_pending:'已请求回退，等待重启',rollback_prepared:'回退资料已准备，等待重启',rolling_back:'回退处理中，需重启完成保护',rolled_back:'已回退，等待核对',released_new:'恢复资料已核对',released_old:'回退资料已核对'};
const backupActionInfo={
 confirm:['确认恢复应用数据','RESTORE','重启时替换当前应用数据，并保留恢复前基线。请先保存当前数据的独立备份。恢复后使用备份中的管理员密码登录。网盘媒体和外部输出文件不改写。'],
 cancel:['移除待恢复候选','','仅移除尚未启用的候选资料，当前运行数据保持原样。'],
 rollback:['回退应用数据','ROLLBACK','重启后恢复本会话的恢复前数据。当前数据将完整保留；恢复后的新增配置不会合并到旧数据。外部输出文件不改写。'],
 review:['完成恢复核对','REVIEWED','请先核对网盘账号、目录规则、播放入口和生成文件。历史任务、规则计划、接入和待发通知继续暂停；完成后还需重启，随后按需逐项启用。'],
 restart:['重启 YoLink','RESTART','重启会短暂中断管理与播放连接。等待恢复或回退会在启动时执行；完成后请重新登录，并核对应用数据。']
};
function backupClearSecrets(){for(const input of backupPanel.querySelectorAll('input'))input.value='';}
function backupError(message){$('backup-error').textContent=message||'';$('backup-error').hidden=!message;}
function backupLeave(){
 const ctx=backupContext;
 if(ctx?.writing)backupUncertain=true;
 ctx?.request?.abort();backupContext=null;backupClearSecrets();$('backup-confirm-form').hidden=true;
 for(const details of backupPanel.querySelectorAll('details'))details.open=false;
}
function backupRender(){
 backupHold.hidden=!state.restore_restricted;
 if(view!=='settings'||$('app').hidden)return;
 if(!backupContext){backupContext={data:null,busy:false,writing:false,request:null,confirmation:null};backupLoadStatus();}
}
function backupControls(){
 const ctx=backupContext,data=ctx?.data;
 const blocked=!ctx||ctx.busy||!data||backupUncertain;
 for(const b of backupPanel.querySelectorAll('button'))b.disabled=!!ctx?.busy;
 $('backup-refresh').disabled=!!ctx?.busy;$('backup-abort').disabled=false;$('backup-abort').hidden=!ctx?.busy;
 $('backup-export').disabled=blocked;
 const waitingUpload=data?.upload&&data.upload.id!==data.pending?.id;
 $('backup-import').disabled=blocked||!data?.supported||!!waitingUpload||!!data?.pending&&!['installed','rolled_back','released_new','released_old'].includes(data.pending.state);
 $('backup-confirm-submit').disabled=blocked;
 for(const b of $('backup-state-actions').querySelectorAll('button'))b.disabled=blocked;
 backupPanel.setAttribute('aria-busy',ctx?.busy?'true':'false');
}
function backupShowState(){
 const ctx=backupContext;if(!ctx)return;
 const data=ctx.data,pending=data?.pending,upload=data?.upload?.id===pending?.id?null:data?.upload;
 $('backup-state-actions').replaceChildren();
 $('backup-candidate').hidden=!upload?.validation;
 if(upload?.validation){const v=upload.validation;$('backup-candidate').textContent='已校验候选：'+v.accounts+' 个账号、'+v.rules+' 条规则、'+v.outputs+' 个输出登记；'+(upload.payload_bytes/1048576).toFixed(1)+' MiB 应用数据。候选校验时间：'+new Date(upload.created*1000).toLocaleString('zh-CN')+'。';}
 if(!data){$('backup-status').textContent=backupUncertain?'上次操作结果尚未确认。请刷新状态后再操作。':'尚未取得恢复状态。';backupControls();return;}
 $('backup-platform').textContent=data.supported?'恢复在本机重启时执行。文件载荷最多 4 GiB；最多 10 万个文件与 10 万个父目录。':'当前平台可导出加密备份；恢复激活需在 Linux 部署中进行。';
 $('backup-status').textContent=upload?'有一份已校验候选等待确认；尚未替换运行数据。':pending?(backupStateNames[pending.state]||'恢复状态需核实')+(data.restricted?'。当前进程保持恢复保护。':'。'): '没有等待处理的恢复会话。';
 const add=(action,label)=>{const b=document.createElement('button');b.type='button';b.textContent=label;b.dataset.backupAction=action;$('backup-state-actions').append(b);};
 if(upload&&(!pending||upload.id!==pending.id)){add('confirm','确认恢复');add('cancel','移除候选');}
 if(pending){
  if(['pending','prepared'].includes(pending.state))add('cancel','取消等待中的恢复');
  if(['installed','released_new'].includes(pending.state))add('rollback','回退应用数据');
  if(data.restricted&&['installed','rolled_back'].includes(pending.state)&&pending.id===data.runtime?.id&&pending.generation===data.runtime?.generation)add('review','完成恢复核对');
  if(data.restricted||!['installed','rolled_back','released_new','released_old'].includes(pending.state))add('restart','重启并继续');
 }
 backupControls();
}
async function backupLoadStatus(){
 const ctx=backupContext;if(!ctx||ctx.busy)return;
 ctx.busy=true;ctx.request=new AbortController();$('backup-confirm-form').hidden=true;ctx.confirmation=null;backupControls();backupError('');
 try{const data=await api('/api/restore/status',undefined,{signal:ctx.request.signal});if(backupContext!==ctx)return;ctx.data=data;backupUncertain=false;backupHold.hidden=!data.restricted;}
 catch(e){if(backupContext!==ctx)return;ctx.data=null;backupError(e.message+'。请检查服务后刷新状态。');}
 finally{if(backupContext===ctx){ctx.busy=false;ctx.request=null;backupShowState();}}
}
function backupPrompt(action){
 const ctx=backupContext;if(!ctx?.data||ctx.busy||backupUncertain)return;
 const info=backupActionInfo[action];if(!info)return;
 const {pending,upload}=ctx.data;
 const target=action==='confirm'||action==='cancel'&&upload?upload:pending;
 if(action!=='restart'&&!target)return;
 ctx.confirmation={action,id:target?.id,revision:target?.revision,generation:target?.generation,word:info[1]};
 $('backup-action-admin').value='';$('backup-confirm-word').value='';
 $('backup-confirm-title').textContent=info[0];$('backup-confirm-effects').textContent=info[2];$('backup-confirm-submit').textContent=info[0];
 $('backup-confirm-target').textContent=target?'恢复会话：'+target.id:'重启当前服务';
 $('backup-confirm-word-label').hidden=!info[1];$('backup-confirm-word').required=!!info[1];
 $('backup-confirm-word-label').firstChild.textContent=info[1]?'输入 '+info[1]+' 确认':'';
 $('backup-confirm-form').hidden=false;$('backup-confirm-title').focus({preventScroll:true});
}
async function backupResponse(response){
 if(response.status===401)showLogin();
 let data;try{data=await response.json();}catch{throw new Error('服务响应无法读取，请刷新恢复状态核实结果');}
 if(!response.ok)throw new Error(data.error||'操作失败（HTTP '+response.status+'）');
 return data;
}
async function backupRun(kind,run){
 const ctx=backupContext;if(!ctx?.data||ctx.busy||backupUncertain)return;
 ctx.busy=true;ctx.writing=kind!=='export';ctx.request=new AbortController();backupError('');$('backup-result').textContent=kind==='export'?'正在生成加密备份，请等待下载…':kind==='import'?'正在上传并隔离校验，尚未替换运行数据…':'正在提交确认…';backupControls();
 let success=false;
 try{await run(ctx);success=true;}
 catch(e){if(backupContext===ctx){if(ctx.writing)backupUncertain=true;ctx.data=null;backupError(e.message+(ctx.writing?'。请求可能已被服务接收，请刷新恢复状态核实，不要直接重复提交。':'。请刷新状态后重试。'));$('backup-result').textContent='操作未确认完成。';}}
 finally{if(backupContext===ctx){ctx.busy=false;ctx.writing=false;ctx.request=null;backupClearSecrets();backupControls();if(success)await backupLoadStatus();}}
}
$('backup-refresh').addEventListener('click',backupLoadStatus);
$('backup-abort').addEventListener('click',()=>{const ctx=backupContext;if(!ctx)return;if(ctx.writing)backupUncertain=true;backupLeave();backupRender();$('backup-result').textContent='已停止等待；已刷新服务状态，请核实结果后继续。';});
$('backup-state-actions').addEventListener('click',e=>{const b=e.target.closest('[data-backup-action]');if(b)backupPrompt(b.dataset.backupAction);});
$('backup-confirm-cancel').addEventListener('click',()=>{$('backup-confirm-form').hidden=true;$('backup-action-admin').value='';$('backup-confirm-word').value='';if(backupContext)backupContext.confirmation=null;});
$('backup-export-form').addEventListener('submit',e=>{
 e.preventDefault();
 const admin=$('backup-export-admin').value,password=$('backup-export-password').value,repeat=$('backup-export-repeat').value;
 if(password!==repeat){backupError('两次输入的备份口令不一致。');return;}
 if(new TextEncoder().encode(password).length<12||new TextEncoder().encode(password).length>1024){backupError('备份口令需为 12–1024 字节。');return;}
 backupRun('export',async ctx=>{
  backupClearSecrets();const response=await fetch('/api/backups/export',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({admin_password:admin,backup_password:password}),signal:ctx.request.signal});
  if(!response.ok){await backupResponse(response);return;}
  const blob=await response.blob();if(backupContext!==ctx)return;
  const url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download='yolink-backup-'+new Date().toISOString().replace(/[:.]/g,'-')+'.age';a.hidden=true;document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),60000);
  $('backup-result').textContent='已发起加密备份下载，请确认文件已保存。请另行保管文件和备份口令。';
 });
});
$('backup-import-form').addEventListener('submit',e=>{
 e.preventDefault();const file=$('backup-file').files[0],admin=$('backup-import-admin').value,password=$('backup-import-password').value;if(!file)return;
 backupRun('import',async ctx=>{
  const body=new FormData();body.append('options',JSON.stringify({admin_password:admin,backup_password:password}));body.append('archive',file);backupClearSecrets();
  const response=await fetch('/api/restore/import',{method:'POST',credentials:'same-origin',body,signal:ctx.request.signal});const candidate=await backupResponse(response);
  if(backupContext!==ctx)return;
  $('backup-result').textContent='校验通过：'+(candidate.validation?.accounts||0)+' 个账号、'+(candidate.validation?.rules||0)+' 条规则。仅保存为候选，请确认后再重启执行恢复。';
 });
});
$('backup-confirm-form').addEventListener('submit',e=>{
 e.preventDefault();const ctx=backupContext,c=ctx?.confirmation;if(!c)return;
 if(c.word&&$('backup-confirm-word').value!==c.word){backupError('请输入 '+c.word+' 确认。');return;}
 const password=$('backup-action-admin').value;
 backupRun('action',async request=>{
  const body={id:c.id,revision:c.revision,generation:c.generation,confirm:c.word,admin_password:password};backupClearSecrets();
  await api('/api/restore/'+c.action,body,{signal:request.request.signal});if(backupContext!==request)return;
  $('backup-confirm-form').hidden=true;request.confirmation=null;
  $('backup-result').textContent=c.action==='restart'?'重启请求已提交，请等待服务恢复后重新登录。':c.action==='cancel'?'待恢复候选已移除，运行数据未替换。':'操作已确认。请查看最新状态，并按提示重启。';
 });
});
backupRender();
