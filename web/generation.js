/* Generation is one workspace: configure mappings once, run from status. */
'use strict';
const genEl=id=>document.getElementById(id);
let genTab='status',genDraft=null,genFolderPurpose='rule',genFolderAccount='',genTrail=[],genFolderSerial=0,genFolderReady=false,genShowAllFailures=false;
let genRulesKey='',genJobsKey='',genAccountsKey='';
const genActive=j=>['queued','running'].includes(j.status);
const genFailed=j=>['failed','partial','interrupted'].includes(j.status);

function generationTab(tab,options={}){
 genTab=tab==='config'?'config':'status';localStorage.setItem('yolink-gen-tab',genTab);
 ['status','config'].forEach(name=>{const selected=genTab===name;genEl('gen-'+name).hidden=!selected;const button=genEl('gen-'+name+'-tab');button.setAttribute('aria-selected',String(selected));button.tabIndex=selected?0:-1});
 if(options.write!==false)writeRoute(genTab==='config'?'config':'home');
 if(typeof scheduleRefresh==='function')scheduleRefresh();
 if(typeof icons==='function')icons();
}
function genOutputPath(value){const root=state.settings?.output_root||'/strm';return !value?'':value.startsWith('/')||/^[A-Za-z]:[\\/]/.test(value)?value:root.replace(/\/$/,'')+'/'+value}
function genRuleName(id){return ruleBy(id)?.name||'已移除的目录'}
function genJobName(j){return j.retry_of?'失败重试':j.dry_run?'范围预览':j.limit?'试生成 '+j.limit+' 个':j.folder===ruleBy(j.rule)?.folder?'全量生成':'指定目录生成'}
function genResult(j){return j.dry_run?`匹配 ${j.seen} · 待新增 ${j.created_files} · 相同 ${j.preview_same||0} · 冲突 ${j.preview_conflicts||0} · 过滤 ${j.filtered||0}`:`新增 ${j.created_files} · 跳过 ${j.skipped} · 失败 ${j.errors}${j.sidecars||j.sidecars_skipped?' · 字幕 '+(j.sidecars||0)+' / 跳过 '+(j.sidecars_skipped||0):''}${j.non_media_files||j.non_media_skipped?' · 非媒体 '+(j.non_media_files||0)+' / 跳过 '+(j.non_media_skipped||0):''}`}
function genSetOptions(select,items,value){
 const signature=JSON.stringify(items.map(x=>[x.id,x.label]));
 if(select.dataset.options!==signature){select.innerHTML=items.length?items.map(x=>`<option value="${esc(x.id)}">${esc(x.label)}</option>`).join(''):'<option value="">暂无可用选项</option>';select.dataset.options=signature}
 if(items.some(x=>x.id===value))select.value=value;
}
function renderGeneration(){
 const o=state.overview||{},active=state.jobs.filter(genActive),failures=state.failures||state.jobs.filter(j=>genFailed(j)&&!j.retried_by);
 const automatic=state.rules.filter(r=>r.enabled&&!accountBy(r.account)?.disabled).length;
 genEl('gen-media').textContent=o.media??0;genEl('gen-outputs').textContent=o.outputs??0;genEl('gen-auto').textContent=automatic+' / '+state.rules.length;genEl('gen-failed-count').textContent=o.failed??failures.length;
 const availableRules=state.rules.filter(r=>!accountBy(r.account)?.disabled);genEl('gen-all').disabled=availableRules.length===0;genEl('gen-scope').disabled=availableRules.length===0;
 let title,copy,setup;
 if(!state.accounts.length){title='先连接一个网盘';copy='移动云盘、夸克、阿里或 115。连接后选择要生成的目录。';setup='添加网盘'}
 else if(!state.rules.length){title='添加第一个目录规则';copy='选好网盘目录和输出位置，以后直接点击全量生成。';setup='添加目录'}
 else if(!state.settings.public_base){title='设置播放入口';copy='填写播放器可访问的服务地址，再开始生成。';setup='服务设置'}
 else if(active.length){title='正在生成 STRM';copy=`${active.filter(j=>j.status==='running').length} 个任务运行中，${active.filter(j=>j.status==='queued').length} 个任务排队。`;setup='生成配置'}
 else{title=automatic?'自动生成已开启':'准备就绪';copy=automatic?`${automatic} 个目录按各自间隔检查新增文件，也可以随时手动生成。`:`已配置 ${state.rules.length} 个目录。全量生成会扫描全部规则，已有相同内容自动跳过。`;setup='生成配置'}
 genEl('gen-banner-title').textContent=title;genEl('gen-banner-copy').textContent=copy;genEl('gen-setup').textContent=setup;
 const accountsKey=JSON.stringify(state.accounts.map(a=>[a.id,a.name,a.disabled]));
 if(genAccountsKey!==accountsKey&&genEl('gen-rule-form').hidden){genAccountsKey=accountsKey;const current=genEl('gen-rule-account').value;genSetOptions(genEl('gen-rule-account'),state.accounts.filter(a=>!a.disabled).map(a=>({id:a.id,label:a.name})),current)}
 const rulesKey=JSON.stringify([state.rules,accountsKey]);
 if(genRulesKey!==rulesKey){genRulesKey=rulesKey;renderGenerationRules()}
 const jobsKey=JSON.stringify([state.jobs,failures,genShowAllFailures]);
 if(genJobsKey!==jobsKey){genJobsKey=jobsKey;renderGenerationJobs(active,failures)}
}
function renderGenerationRules(){renderRuleConsole()}
function genJobDetails(j){
 const lines=[...(j.failures||[]),...(j.samples||[]).map(x=>'输出：'+x)];
 return `<details class="job-details" data-job-detail="${esc(j.id)}"><summary>查看详情</summary><p>${esc(j.message||'等待扫描')}</p><ul>${lines.map(x=>`<li><code>${esc(x)}</code></li>`).join('')}</ul>${jobExtraDetails(j)}<code>规则 ID：${esc(j.rule)}</code></details>`;
}
function renderGenerationJobs(active,failures){
 const open=new Set([...document.querySelectorAll('[data-job-detail][open]')].map(x=>x.dataset.jobDetail));
 genEl('gen-active-section').hidden=active.length===0;genEl('gen-active-count').textContent=active.length+' 个任务';
 genEl('gen-active').innerHTML=active.map(j=>`<article class="panel generation-task"><div class="job-top"><div><h3>${esc(genRuleName(j.rule))} · ${esc(genJobName(j))}</h3><p class="hint">${esc(j.source_path)}</p></div><button data-gen-cancel="${esc(j.id)}">${j.status==='queued'?'取消排队':'取消任务'}</button></div>${j.status==='running'?'<div class="scan-progress" aria-hidden="true"><span></span></div>':''}<div class="scan-counts"><span>${esc(statusNames[j.status])}${j.resumes?' · 断点续跑 '+j.resumes+' 次':''}</span><span>已发现 ${j.seen}</span><span>${esc(genResult(j))}</span></div><p class="current-file">${j.current_path?'当前：'+esc(j.current_path):'等待开始扫描'}</p></article>`).join('');
 genEl('gen-failure-section').hidden=failures.length===0;genEl('gen-retry-all').disabled=!failures.length;
 const visible=genShowAllFailures?failures:failures.slice(0,5);
 genEl('gen-failures').innerHTML=visible.map(j=>`<article class="generation-failure"><div class="failure-copy"><h3>${esc(genRuleName(j.rule))} · ${esc(genJobName(j))}</h3><p class="error">${esc(j.message||'生成任务未完成')}</p><p class="hint">${esc(j.source_path)} · ${j.errors} 项失败</p>${genJobDetails(j)}</div><button data-gen-retry="${esc(j.id)}" title="重新扫描本次范围，已生成的相同文件自动跳过"><i data-lucide="rotate-ccw"></i>重试</button></article>`).join('');
 genEl('gen-more-failures').hidden=failures.length<=5||genShowAllFailures;genEl('gen-more-failures').textContent='查看全部 '+failures.length+' 条失败';renderGenerationRules();
 const history=state.jobs.slice(0,20);
 genEl('gen-history').innerHTML=history.length?history.map(j=>`<tr class="job-summary-row"><td><strong>${esc(genRuleName(j.rule))}</strong><small class="job-scope" title="${esc(j.source_path)}">${esc(genJobName(j))} · ${esc(j.source_path)}</small></td><td><span class="${jobClass(j)}">${esc(statusNames[j.status]||j.status)}</span></td><td>${esc(genResult(j))}</td><td class="table-time">${date(j.created)}</td><td><div class="actions"><button class="text" data-job-expand="${esc(j.id)}" aria-expanded="${open.has(j.id)}">${open.has(j.id)?'收起':'详情'}</button>${genActive(j)?`<button data-gen-cancel="${esc(j.id)}">取消</button>`:genFailed(j)&&!j.retried_by?`<button data-gen-retry="${esc(j.id)}">重试</button>`:j.retried_by?'<span class="hint">已重试</span>':''}</div></td></tr><tr class="job-full-row" data-job-panel="${esc(j.id)}" ${open.has(j.id)?'':'hidden'}><td colspan="5">${genJobDetails(j)}</td></tr>`).join(''):'<tr><td colspan="5" class="empty">还没有生成任务。</td></tr>';
 document.querySelectorAll('[data-job-detail]').forEach(el=>{if(open.has(el.dataset.jobDetail))el.open=true});icons();
}
function generationEditRule(id=null,accountID=''){
 const available=state.accounts.filter(a=>!a.disabled);
 if(!available.length){nav('accounts');toast('先添加并启用一个网盘账号');return}
 genDraft=id?{...ruleBy(id)}:{id:'',account:accountID||available[0].id,folder:'',source_path:'',output:'',interval:1800,enabled:false};
 nav('config');genEl('gen-rule-form').hidden=false;genEl('gen-editor-title').textContent=id?'编辑目录规则':'新增目录规则';
 genSetOptions(genEl('gen-rule-account'),state.accounts.filter(a=>!a.disabled||a.id===genDraft.account).map(a=>({id:a.id,label:a.name+(a.disabled?'（已停用）':'')})),genDraft.account);
 genEl('gen-rule-source').value=genDraft.source_path;genEl('gen-rule-output').value=genOutputPath(genDraft.output);genEl('gen-rule-name').value=id?genDraft.name:'';genEl('gen-rule-enabled').checked=genDraft.enabled;genEl('gen-copy-subtitles').checked=!!genDraft.copy_subtitles;genEl('gen-copy-nonmedia').checked=!!genDraft.copy_non_media;genEl('gen-rule-interval').value=Math.round(genDraft.interval/60)||30;genEl('gen-editor-result').textContent='';generationScheduleSummary();loadRuleExtras(genDraft);
 genEl('gen-rule-form').scrollIntoView({block:'start',behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'});
}
function genCurrent(){return genTrail.at(-1)}
function genRenderTrail(){
 genEl('gen-folder-crumbs').innerHTML=genTrail.map((x,i)=>`<button data-gen-trail="${i}">${esc(x.name)}</button>${i<genTrail.length-1?'<i data-lucide="chevron-right"></i>':''}`).join('');
 genEl('gen-folder-current').textContent=genCurrent()?.path||'/';icons();
}
async function genLoadFolder(){
 const current=genCurrent();if(!current)return;
 const serial=++genFolderSerial;genFolderReady=false;genEl('gen-folder-use').disabled=true;genEl('gen-folder-error').textContent='';genEl('gen-folder-list').innerHTML='<div class="empty">正在读取目录…</div>';genRenderTrail();
 try{const result=await api('/api/browse?account='+encodeURIComponent(genFolderAccount)+'&folder='+encodeURIComponent(current.id));if(serial!==genFolderSerial)return;
 const dirs=result.entries.filter(x=>x.dir).sort((a,b)=>a.name.localeCompare(b.name,'zh-CN'));
 genEl('gen-folder-list').innerHTML=dirs.length?dirs.map(x=>`<button class="file-row" data-gen-folder="${esc(x.id)}" data-folder-name="${esc(x.name)}"><i data-lucide="folder"></i><span>${esc(x.name)}</span><i data-lucide="chevron-right"></i></button>`).join(''):'<div class="empty">没有子目录，可以选择当前目录。</div>';
 genFolderReady=true;genEl('gen-folder-use').disabled=false;icons();
 }catch(e){if(serial!==genFolderSerial)return;genEl('gen-folder-error').textContent=e.message;genEl('gen-folder-list').innerHTML='<div class="empty"><button id="gen-folder-reload">重新读取目录</button></div>'}
}
async function generationPickFolder(purpose){
 genFolderPurpose=purpose;
 if(purpose==='scope'){
  if(!state.rules.length){generationEditRule();return}
  const select=genEl('gen-scope-rule');genSetOptions(select,state.rules.filter(r=>!accountBy(r.account)?.disabled).map(r=>({id:r.id,label:r.name+' · '+(accountBy(r.account)?.name||'')})),select.value);
  const r=ruleBy(select.value);genFolderAccount=r.account;genTrail=[{id:r.folder,path:r.source_path,name:r.name}];
 }else{
  if(!genDraft)generationEditRule();if(!genDraft)return;
  genFolderAccount=genEl('gen-rule-account').value;const a=accountBy(genFolderAccount);if(!a)throw new Error('请先添加网盘账号');genTrail=[{id:a.root,path:'/',name:'根目录'}];
 }
 genEl('gen-scope-rule-field').hidden=purpose!=='scope';genEl('gen-folder-title').textContent=purpose==='scope'?'指定目录生成':'选择网盘目录';genEl('gen-folder-use').textContent=purpose==='scope'?'生成这个目录':'选择此目录';genEl('gen-folder-account').textContent=accountBy(genFolderAccount)?.name||'';
 const dialog=genEl('gen-folder-dialog');if(!dialog.open)dialog.showModal();await genLoadFolder();
}
function genCloseFolder(){genFolderSerial++;genEl('gen-folder-dialog').close()}
async function generationRun(ruleID,{dry=false,trial=false,folder='',source_path=''}={}){
 if(!ruleID)throw new Error('请先添加目录规则');await api('/api/jobs',{rule:ruleID,folder,source_path,dry_run:dry,limit:trial?10:0});await refresh();nav('tasks');toast(dry?'差异预览已开始':trial?'已开始试生成，最多 10 个媒体':'生成任务已开始');
}
async function generationSave(event){
 event.preventDefault();const button=event.submitter;await action(button,async()=>{
  if(!genDraft?.folder||!genDraft.source_path)throw new Error('请选择网盘目录');
  const interval=Number(genEl('gen-rule-interval').value);if(!Number.isInteger(interval)||interval<1||interval>10080)throw new Error('检查间隔应为 1 到 10080 分钟');
  const leaf=genDraft.source_path.split('/').filter(Boolean).at(-1)||accountBy(genDraft.account)?.name||'媒体';
  const body={...genDraft,...readRuleExtras(),account:genEl('gen-rule-account').value,name:genEl('gen-rule-name').value.trim()||leaf,output:genEl('gen-rule-output').value.trim(),copy_subtitles:genEl('gen-copy-subtitles').checked,copy_non_media:genEl('gen-copy-nonmedia').checked,enabled:genEl('gen-rule-enabled').checked,interval:interval*60};
  const saved=await api('/api/rules',body);genEl('gen-rule-form').hidden=true;genDraft=null;await refresh();
  if(button.value==='generate')await generationRun(saved.id);else{toast('目录规则已保存');nav('config')}
 });
}
document.addEventListener('click',event=>{
 const el=event.target.closest('button');if(!el)return;
 if(el.id==='gen-empty-add')generationEditRule();
 if(el.dataset.genEdit)generationEditRule(el.dataset.genEdit);
 if(el.dataset.genGenerate)action(el,()=>generationRun(el.dataset.genGenerate));
 if(el.dataset.genTrial)action(el,()=>generationRun(el.dataset.genTrial,{trial:true}));
 if(el.dataset.genPreview)action(el,()=>generationRun(el.dataset.genPreview,{dry:true}));
 if(el.dataset.genCancel)action(el,async()=>{await api('/api/jobs/'+el.dataset.genCancel+'/cancel',{});await refresh();toast('任务已取消，已生成文件保留')});
 if(el.dataset.genRetry)action(el,async()=>{await api('/api/jobs/'+el.dataset.genRetry+'/retry',{});await refresh();toast('已提交重试')});
 if(el.dataset.genRemove)action(el,async()=>{if(!window.confirm('移除此目录规则？已经生成的文件会保留。'))return;await api('/api/rules/'+el.dataset.genRemove+'/remove',{});if(genDraft?.id===el.dataset.genRemove){genEl('gen-rule-form').hidden=true;genDraft=null}await refresh();toast('目录规则已移除，生成文件保留')});
 if(el.dataset.genTrail!==undefined)action(el,async()=>{genTrail=genTrail.slice(0,Number(el.dataset.genTrail)+1);await genLoadFolder()});
 if(el.dataset.genFolder)action(el,async()=>{const current=genCurrent(),name=el.dataset.folderName;genTrail.push({id:el.dataset.genFolder,name,path:(current.path==='/'?'':current.path)+'/'+name});await genLoadFolder()});
 if(el.id==='gen-folder-reload')action(el,genLoadFolder);
});
document.addEventListener('change',event=>{
 const input=event.target;if(!input.dataset.genToggle)return;
 action(input,async()=>{const r=ruleBy(input.dataset.genToggle);try{await api('/api/rules',{...r,enabled:input.checked});await refresh();toast(input.checked?'已开启自动生成':'已关闭自动生成')}catch(e){input.checked=r.enabled;throw e}});
});
genEl('gen-status-tab').addEventListener('click',()=>generationTab('status'));
genEl('gen-config-tab').addEventListener('click',()=>generationTab('config'));
for(const [name,other] of [['status','config'],['config','status']])genEl('gen-'+name+'-tab').addEventListener('keydown',event=>{if(['ArrowLeft','ArrowRight','Home','End'].includes(event.key)){event.preventDefault();const next=event.key==='Home'?'status':event.key==='End'?'config':other;generationTab(next);genEl('gen-'+next+'-tab').focus()}});
genEl('gen-add-rule').addEventListener('click',()=>generationEditRule());
genEl('gen-setup').addEventListener('click',()=>{if(!state.accounts.length)nav('accounts');else if(!state.rules.length)generationEditRule();else if(!state.settings.public_base)nav('settings');else nav('config')});
genEl('gen-editor-close').addEventListener('click',()=>{genEl('gen-rule-form').hidden=true;genDraft=null});
genEl('gen-rule-account').addEventListener('change',()=>{if(genDraft){genDraft.account=genEl('gen-rule-account').value;genDraft.folder='';genDraft.source_path='';genEl('gen-rule-source').value=''}});
genEl('gen-pick-rule-folder').addEventListener('click',event=>action(event.currentTarget,()=>generationPickFolder('rule')));
genEl('gen-scope').addEventListener('click',event=>action(event.currentTarget,()=>generationPickFolder('scope')));
genEl('gen-scope-rule').addEventListener('change',()=>{const r=ruleBy(genEl('gen-scope-rule').value);genFolderAccount=r.account;genTrail=[{id:r.folder,path:r.source_path,name:r.name}];genEl('gen-folder-account').textContent=accountBy(r.account)?.name||'';genLoadFolder()});
genEl('gen-folder-close').addEventListener('click',genCloseFolder);genEl('gen-folder-cancel').addEventListener('click',genCloseFolder);genEl('gen-folder-dialog').addEventListener('cancel',()=>{genFolderSerial++});
genEl('gen-folder-use').addEventListener('click',event=>action(event.currentTarget,async()=>{
 if(!genFolderReady||!genCurrent())throw new Error('请先成功读取目录');
 const current=genCurrent();
 if(genFolderPurpose==='scope'){const ruleID=genEl('gen-scope-rule').value;await generationRun(ruleID,{folder:current.id,source_path:current.path});genCloseFolder()}
 else if(genFolderPurpose==='automation'){$('automation-path').value=current.path;automationOutputHint();genCloseFolder();$('automation-path').focus()}
 else{genDraft.folder=current.id;genDraft.source_path=current.path;genEl('gen-rule-source').value=current.path;if(!genEl('gen-rule-output').value.trim()){const leaf=current.path.split('/').filter(Boolean).at(-1)||accountBy(genFolderAccount)?.name||'媒体';genEl('gen-rule-output').value=genOutputPath(leaf.replace(/[\\/:*?"<>|]/g,'_'))}genCloseFolder();genEl('gen-rule-output').focus()}
}));
genEl('gen-rule-form').addEventListener('submit',generationSave);
genEl('gen-all').addEventListener('click',event=>action(event.currentTarget,async()=>{
 if(!state.rules.length)throw new Error('请先添加目录规则');let count=0;const errors=[];
 for(const rule of state.rules.filter(r=>!accountBy(r.account)?.disabled)){try{await api('/api/jobs',{rule:rule.id,dry_run:false,limit:0});count++}catch(e){errors.push(rule.name+'：'+e.message)}}
 await refresh();nav('tasks');toast(`已提交 ${count} 个目录的全量生成${errors.length?'；'+errors.join('；'):''}`);
}));
genEl('gen-refresh').addEventListener('click',event=>action(event.currentTarget,refresh));
genEl('gen-more-failures').addEventListener('click',()=>{genShowAllFailures=true;genJobsKey='';renderGeneration()});
genEl('gen-retry-all').addEventListener('click',event=>action(event.currentTarget,async()=>{const result=await api('/api/jobs/retry-failed',{});await refresh();toast(`已提交 ${result.started.length} 个重试任务${result.deferred.length?'；另有 '+result.deferred.length+' 个范围需等待当前任务结束后重试':''}`)}));

let outputTrail=[{name:'映射目录',path:''}],outputReady=false,outputSerial=0,outputTarget='gen-rule-output',outputTrigger='gen-pick-output-folder';
async function loadOutputFolder(){
 const current=outputTrail.at(-1),serial=++outputSerial;outputReady=false;genEl('output-folder-use').disabled=true;genEl('output-folder-error').textContent='';genEl('output-folder-list').innerHTML='<div class="empty">正在读取目录…</div>';
 genEl('output-folder-crumbs').innerHTML=outputTrail.map((v,i)=>`<button data-output-trail="${i}">${esc(v.name)}</button>`).join('');genEl('output-folder-current').textContent=current.path?'当前目录：'+current.path:'选择一个输出映射';
 try{const result=await api('/api/output/directories'+(current.path?'?path='+encodeURIComponent(current.path):''));if(serial!==outputSerial)return;
  const entries=current.path?result.entries:result.mounts.map(m=>({name:m.path+(m.default?'（默认输出）':''),path:m.path}));
  genEl('output-folder-list').innerHTML=entries.length?entries.map(v=>`<button class="file-row" data-output-folder="${esc(v.path)}" data-output-name="${esc(v.name)}"><i data-lucide="folder"></i><span>${esc(v.name)}</span><i data-lucide="chevron-right"></i></button>`).join(''):'<div class="empty">没有子目录，可以选择当前目录。</div>';
  outputReady=true;genEl('output-folder-use').disabled=!current.path;icons();
 }catch(e){if(serial!==outputSerial)return;genEl('output-folder-error').textContent=e.message;genEl('output-folder-list').innerHTML='<div class="empty"><button id="output-folder-retry">重新读取目录</button></div>'}
}
function closeOutputFolder(){outputSerial++;genEl('output-folder-dialog').close();genEl(outputTrigger).focus()}
async function openOutputFolder(target,trigger,title){outputTarget=target;outputTrigger=trigger;genEl('output-folder-title').textContent=title;outputTrail=[{name:'映射目录',path:''}];genEl('output-folder-dialog').showModal();await loadOutputFolder()}
genEl('gen-pick-output-folder').addEventListener('click',event=>action(event.currentTarget,()=>openOutputFolder('gen-rule-output','gen-pick-output-folder','选择 STRM 输出目录')));
genEl('output-folder-close').addEventListener('click',closeOutputFolder);genEl('output-folder-cancel').addEventListener('click',closeOutputFolder);genEl('output-folder-dialog').addEventListener('cancel',()=>{outputSerial++});
genEl('output-folder-use').addEventListener('click',()=>{if(!outputReady||!outputTrail.at(-1).path)return;const input=genEl(outputTarget);input.value=outputTrail.at(-1).path;input.dispatchEvent(new Event('input',{bubbles:true}));closeOutputFolder()});
document.addEventListener('click',event=>{const button=event.target.closest('button');if(!button)return;
 if(button.dataset.outputFolder)action(button,async()=>{outputTrail.push({name:button.dataset.outputName,path:button.dataset.outputFolder});await loadOutputFolder()});
 if(button.dataset.outputTrail!==undefined)action(button,async()=>{outputTrail=outputTrail.slice(0,Number(button.dataset.outputTrail)+1);await loadOutputFolder()});
 if(button.id==='output-folder-retry')action(button,loadOutputFolder);
});

function generationScheduleSummary(){const enabled=genEl('gen-rule-enabled').checked,minutes=Number(genEl('gen-rule-interval').value);genEl('gen-schedule-summary').textContent=(enabled?'当前每 '+minutes+' 分钟检查新增文件。':'当前仅手动执行。')+'保存目录规则后生效。自定义间隔可在高级选项中填写。';document.querySelectorAll('[data-schedule-minutes]').forEach(button=>button.setAttribute('aria-pressed',String((enabled?minutes:0)===Number(button.dataset.scheduleMinutes))))}
document.addEventListener('click',event=>{const button=event.target.closest('[data-schedule-minutes]');if(!button)return;const minutes=Number(button.dataset.scheduleMinutes);genEl('gen-rule-enabled').checked=minutes>0;if(minutes)genEl('gen-rule-interval').value=minutes;generationScheduleSummary()});
genEl('gen-rule-enabled').addEventListener('change',generationScheduleSummary);genEl('gen-rule-interval').addEventListener('input',generationScheduleSummary);
