/* Contextual health/recovery and registered-file diagnostics. No background probes. */
'use strict';
const reliabilityHealthNames={unknown:'尚未检查',healthy:'正常',disabled:'已停用',unsupported:'不支持',credentials:'凭据失效',rate_limit:'请求受限',network:'网络异常',permission:'权限受限',source:'来源异常',protocol:'接口异常',storage:'保存失败',conflict:'状态冲突',unknown_error:'检查失败'};
const reliabilityStepNames={passed:'通过',failed:'失败',warning:'需核实',skipped:'未执行',cancelled:'已取消'};
let healthContext=null,diagnosticContext=null;

function reliabilityPanel(id,title,body){
 const panel=document.createElement('section');panel.id=id;panel.className='panel reliability-panel';panel.hidden=true;
 panel.setAttribute('aria-labelledby',id+'-title');
 panel.innerHTML='<div class="reliability-heading"><h2 id="'+id+'-title" tabindex="-1">'+title+'</h2><button type="button" data-reliability-close="'+id+'" aria-label="关闭'+title+'">关闭</button></div>'+body;
 return panel;
}
function reliabilityError(id,message){const el=$(id);el.textContent=message||'';el.hidden=!message;}
function reliabilityFocus(panel){panel.hidden=false;panel.querySelector('h2').focus({preventScroll:true});panel.scrollIntoView({block:'start',behavior:'instant'});}
function reliabilityLeave(next){
 if(next!=='accounts'&&healthContext){healthContext.request?.abort();healthContext=null;$('account-health').hidden=true;}
 if(next!=='inventory'&&diagnosticContext){diagnosticContext.request?.abort();diagnosticContext=null;$('media-diagnostic').hidden=true;}
}
function closeReliability(id){
 const ctx=id==='account-health'?healthContext:diagnosticContext;
 if(id==='account-health'){ctx?.request?.abort();healthContext=null;}else{ctx?.request?.abort();diagnosticContext=null;}
 $(id).hidden=true;
 let button=ctx?.trigger;
 if(!button?.isConnected)button=[...document.querySelectorAll(id==='account-health'?'[data-account-health]':'[data-media-diagnose]')].find(el=>id==='account-health'?el.dataset.accountHealth===ctx?.account:el.dataset.mediaDiagnose===ctx?.media&&el.dataset.diagnoseOutput===ctx?.output);
 if(button?.isConnected)button.focus();else{const heading=$('view-'+view)?.querySelector('h1');if(heading){heading.setAttribute('tabindex','-1');heading.focus();}}
}
function healthSelectionChanged(){
 const ctx=healthContext;if(!ctx)return;
 $('health-confirmation').hidden=true;
 $('health-selected').textContent='已选择 '+ctx.selected.size+' 项 · 每次最多恢复 50 项';
 $('health-review').disabled=ctx.loading||ctx.recovering||!ctx.data?.recovery_ready||!ctx.selected.size;
}
function healthJobHTML(job){
 const title=ruleBy(job.rule)?.name||'规则不可用';
 return '<li><label class="reliability-job"><input type="checkbox" data-health-job="'+esc(job.id)+'" '+(!job.eligible?'disabled':'')+'><span><strong>'+esc(title)+'</strong><span class="reliability-path">'+esc(job.source_path||'规则根目录')+'</span><small>'+esc(statusNames[job.status]||job.status)+(job.reason?' · '+esc(job.reason):' · 可按原任务范围重试')+'</small></span></label></li>';
}
function renderHealthOperations(data){
 $('health-operations').innerHTML=data.operations.map(h=>'<li><div><strong>'+esc({list:'目录读取',resolve:'播放解析',quota:'容量查询'}[h.operation]||h.operation)+'</strong><span class="reliability-state '+(h.state==='healthy'?'good':['unknown','disabled','unsupported'].includes(h.state)?'muted':'error')+'">'+esc(reliabilityHealthNames[h.state]||'检查异常')+'</span></div><p>'+esc(h.note||'尚无检查结果，不代表账号异常')+'</p><small>检查：'+esc(fileTime(h.checked))+' · 上次成功：'+esc(fileTime(h.last_success))+(h.failures?' · 连续失败 '+Number(h.failures)+' 次':'')+'</small>'+(h.next_retry?'<p>建议重试时间：'+esc(fileTime(h.next_retry))+'</p>':'')+'</li>').join('');
 $('health-readiness').textContent=data.recovery_ready?'账号当前满足恢复条件；提交时会再次检查账号和规则版本。':data.recovery_reason;
}
async function loadAccountHealth(more=false){
 const ctx=healthContext;if(!ctx||ctx.recovering||ctx.loading)return;
 ctx.loading=true;ctx.request=new AbortController();
 $('health-refresh').disabled=true;$('health-more').disabled=true;$('health-test').disabled=true;
 healthSelectionChanged();reliabilityError('health-error','');
 $('health-loading').textContent=more?'正在读取更多任务…':'正在读取账号状态…';
 try{
  const suffix=more?'?before='+encodeURIComponent(ctx.data.next_before):'';
  const data=await api('/api/accounts/'+encodeURIComponent(ctx.account)+'/health'+suffix,undefined,{signal:ctx.request.signal});
  if(healthContext!==ctx)return;
  if(data.account!==ctx.account)throw new Error('账号状态不匹配，请刷新后重试');
  if(more&&data.revision!==ctx.data.revision){ctx.data=null;ctx.jobs=[];ctx.selected.clear();$('health-jobs').replaceChildren();$('health-operations').replaceChildren();$('health-more').hidden=true;throw new Error('账号配置已改变，请刷新健康状态后重新选择任务');}
  ctx.data=data;
  if(!more){ctx.jobs=[];ctx.selected.clear();$('health-jobs').replaceChildren();}
  const existing=new Set(ctx.jobs.map(j=>j.id));const added=data.affected.filter(j=>!existing.has(j.id));
  ctx.jobs.push(...added);$('health-jobs').insertAdjacentHTML('beforeend',added.map(healthJobHTML).join(''));
  $('health-empty').hidden=ctx.jobs.length>0;
  $('health-more').hidden=!data.has_more;
  renderHealthOperations(data);
  $('health-loading').textContent='已读取 '+ctx.jobs.length+' 个受影响任务 · '+fileTime(Date.now());
 }catch(error){if(healthContext===ctx&&error.name!=='AbortError'){reliabilityError('health-error',error.message);ctx.data=null;$('health-loading').textContent='读取未完成，请刷新重试';$('health-more').hidden=true;}}
 finally{if(healthContext===ctx){ctx.loading=false;ctx.request=null;$('health-refresh').disabled=false;$('health-more').disabled=false;$('health-test').disabled=!!accountBy(ctx.account)?.disabled;healthSelectionChanged();}}
}
function openAccountHealth(account,trigger){
 healthContext?.request?.abort();
 healthContext={account,trigger,data:null,jobs:[],selected:new Set(),loading:false,recovering:false,request:null};
 $('account-health-title').textContent=(accountBy(account)?.name||'网盘账号')+' · 健康与恢复';
 for(const id of ['health-operations','health-jobs','health-recovery-result'])$(id).replaceChildren();
 $('health-readiness').textContent='';$('health-empty').hidden=true;$('health-more').hidden=true;$('health-confirm').disabled=false;
 reliabilityFocus($('account-health'));void loadAccountHealth();
}
function reviewHealthRecovery(){
 const ctx=healthContext;if(!ctx||$('health-review').disabled)return;
 $('health-confirm-list').innerHTML=ctx.jobs.filter(j=>ctx.selected.has(j.id)).map(j=>'<li>'+esc(ruleBy(j.rule)?.name||j.rule)+' · '+esc(j.source_path||'规则根目录')+'</li>').join('');
 $('health-confirmation').hidden=false;$('health-confirm').focus();
}
async function recoverHealthJobs(){
 const ctx=healthContext;if(!ctx||ctx.recovering||!ctx.data?.recovery_ready||!ctx.selected.size)return;
 const jobs=[...ctx.selected];ctx.recovering=true;
 $('health-confirm').disabled=true;$('health-review').disabled=true;$('health-refresh').disabled=true;$('health-more').disabled=true;$('health-test').disabled=true;
 $('health-jobs').querySelectorAll('input').forEach(el=>el.disabled=true);
 reliabilityError('health-error','');$('health-recovery-result').textContent='正在提交恢复请求，请勿重复提交…';
 let submissionError='';
 try{
  // Never cancel/retry this write when navigating: the server may already have queued work.
  const result=await api('/api/accounts/'+encodeURIComponent(ctx.account)+'/recover',{revision:ctx.data.revision,jobs,confirm:true});
  const summary='已加入恢复队列 '+result.started.length+' 项，暂未恢复 '+result.deferred.length+' 项。';
  toast(summary);
  if(healthContext!==ctx)return;
  $('health-recovery-result').innerHTML='<p>'+esc(summary)+'</p>'+result.deferred.map(j=>'<p>'+esc(ctx.jobs.find(v=>v.id===j.id)?.source_path||j.id)+'：'+esc(j.reason)+'</p>').join('')+'<button type="button" data-nav="tasks">查看生成任务</button>';
 }catch(error){submissionError=error.message+'。请求可能已被服务接收，请刷新任务状态后再决定是否重试。';if(healthContext!==ctx)toast('恢复请求未确认，请到生成任务核对结果');}
 finally{
  ctx.recovering=false;
  if(healthContext===ctx){$('health-confirm').disabled=false;ctx.selected.clear();$('health-confirmation').hidden=true;await loadAccountHealth();if(healthContext===ctx&&submissionError){$('health-recovery-result').textContent='恢复结果尚未确认。';reliabilityError('health-error',submissionError);}}
 }
}
function openMediaDiagnostic(media,output,trigger){
 diagnosticContext?.request?.abort();
 diagnosticContext={media,output,trigger,request:null,report:null};
 const name=inventoryData.items.find(v=>v.media===media&&v.path===output)?.name||'所选文件';
 $('media-diagnostic-title').textContent=name+' · 播放诊断';
 $('diagnostic-probe').checked=false;$('diagnostic-probe').disabled=false;$('diagnostic-run').disabled=false;$('diagnostic-cancel').hidden=true;$('diagnostic-export').hidden=true;$('diagnostic-export').disabled=false;
 $('diagnostic-steps').replaceChildren();$('diagnostic-summary').textContent='检查登记索引、本地 STRM、账号和链接解析；不会修复或覆盖文件。';
 reliabilityError('diagnostic-error','');reliabilityFocus($('media-diagnostic'));
}
async function runMediaDiagnostic(){
 const ctx=diagnosticContext;if(!ctx||ctx.request)return;
 const request=new AbortController();ctx.request=request;ctx.report=null;
 $('diagnostic-run').disabled=true;$('diagnostic-probe').disabled=true;$('diagnostic-cancel').hidden=false;$('diagnostic-export').hidden=true;
 $('diagnostic-steps').replaceChildren();reliabilityError('diagnostic-error','');
 $('diagnostic-summary').textContent='正在检查，最长约 45 秒。可以取消；结果返回后将逐项展示。';
 try{
  const report=await api('/api/media/'+encodeURIComponent(ctx.media)+'/diagnose',{output:ctx.output,probe:$('diagnostic-probe').checked},{signal:request.signal,timeoutMs:50000});
  if(diagnosticContext!==ctx||ctx.request!==request)return;
  ctx.report=report;
  const failed=report.steps.some(step=>step.status==='failed'),incomplete=report.steps.some(step=>step.status==='cancelled');
  $('diagnostic-summary').textContent=(report.stale?'账号配置已变化，请重新诊断。':failed?'发现需要处理的问题。':incomplete?'诊断未完成。':'本次检查已完成。')+' '+report.boundary;
  $('diagnostic-steps').innerHTML=report.steps.map(step=>'<li><div><strong>'+esc(step.label)+'</strong><span class="reliability-state '+(step.status==='passed'?'good':step.status==='failed'?'error':'muted')+'">'+esc(reliabilityStepNames[step.status]||'未知结果')+'</span></div><p>'+esc(step.note)+'</p>'+(step.action?'<p>建议：'+esc(step.action)+'</p>':'')+'<small>用时 '+Number(step.millis)+' ms</small></li>').join('');
  $('diagnostic-export').hidden=false;
 }catch(error){if(diagnosticContext===ctx&&ctx.request===request){$('diagnostic-summary').textContent=error.name==='AbortError'?'已取消诊断，没有完整结果。':'诊断未完成，可重新检查。';if(error.name!=='AbortError')reliabilityError('diagnostic-error',error.message);}}
 finally{if(diagnosticContext===ctx&&ctx.request===request){ctx.request=null;$('diagnostic-run').disabled=false;$('diagnostic-probe').disabled=false;$('diagnostic-cancel').hidden=true;}}
}
async function exportMediaDiagnostic(){
 const ctx=diagnosticContext;if(!ctx?.report)return;
 const snapshot=ctx.report;
 const button=$('diagnostic-export');button.disabled=true;
 try{
  const report=await api('/api/diagnostics/'+encodeURIComponent(snapshot.id)+'/export');
  if(diagnosticContext!==ctx||ctx.report!==snapshot)return;
  const url=URL.createObjectURL(new Blob([JSON.stringify(report,null,2)],{type:'application/json'}));
  const link=document.createElement('a');link.href=url;link.download='yolink-diagnostic-'+snapshot.id+'.json';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
 }catch(error){if(diagnosticContext===ctx&&ctx.report===snapshot)reliabilityError('diagnostic-error',error.message);}
 finally{if(diagnosticContext===ctx)button.disabled=false;}
}

document.addEventListener('DOMContentLoaded',()=>{
 const health=reliabilityPanel('account-health','健康与恢复',
  '<p class="hint">状态来自实际操作，不会因打开面板额外扫描网盘。目录正常不代表播放解析正常。</p><div class="actions"><button id="health-refresh">刷新状态</button><button id="health-test">测试目录连接</button></div><p id="health-loading" role="status"></p><p id="health-error" class="error" role="alert" hidden></p><ul id="health-operations" class="reliability-list"></ul><h3>受影响任务</h3><p id="health-readiness" class="hint"></p><p id="health-empty" hidden>没有待恢复的失败、中断或部分失败任务。</p><ul id="health-jobs" class="reliability-list"></ul><button id="health-more" hidden>加载更多任务</button><div class="reliability-toolbar"><span id="health-selected"></span><button id="health-review" disabled>预览所选恢复</button></div><div id="health-confirmation" class="reliability-confirm" hidden><h3>确认恢复这些任务？</h3><p>按各任务原范围继续执行，可能创建缺失文件并请求网盘。账号或规则已变化的任务不会恢复。</p><ul id="health-confirm-list"></ul><div class="actions"><button id="health-confirm" class="primary">确认加入恢复队列</button><button id="health-dismiss">返回选择</button></div></div><div id="health-recovery-result" role="status"></div>');
 $('account-list').before(health);
 const diagnostic=reliabilityPanel('media-diagnostic','播放诊断',
  '<p class="hint">只检查当前登记文件，不接受外部网址；不会改写 STRM。检查可能联系网盘并刷新账号凭据。</p><label class="reliability-check"><input id="diagnostic-probe" type="checkbox">额外执行有限取流检查（最多读取 1024 字节）</label><div class="actions"><button id="diagnostic-run" class="primary">开始诊断</button><button id="diagnostic-cancel" hidden>取消诊断</button><button id="diagnostic-export" hidden>导出脱敏报告</button></div><p id="diagnostic-summary" role="status"></p><p id="diagnostic-error" class="error" role="alert" hidden></p><ol id="diagnostic-steps" class="reliability-list"></ol>');
 $('inventory-filters').before(diagnostic);
 $('health-refresh').addEventListener('click',()=>void loadAccountHealth());
 $('health-more').addEventListener('click',()=>void loadAccountHealth(true));
 $('health-review').addEventListener('click',reviewHealthRecovery);
 $('health-dismiss').addEventListener('click',()=>{$('health-confirmation').hidden=true;$('health-review').focus();});
 $('health-confirm').addEventListener('click',()=>void recoverHealthJobs());
 $('health-test').addEventListener('click',async()=>{
  const ctx=healthContext;if(!ctx||ctx.loading||ctx.recovering)return;
  ctx.loading=true;ctx.request=new AbortController();$('health-test').disabled=true;$('health-refresh').disabled=true;$('health-more').disabled=true;healthSelectionChanged();
  $('health-loading').textContent='正在测试目录连接…';reliabilityError('health-error','');
  let message='';
  try{await api('/api/accounts/'+encodeURIComponent(ctx.account)+'/check',{}, {signal:ctx.request.signal});}
  catch(error){if(error.name!=='AbortError')message=error.message;}
  finally{if(healthContext===ctx){ctx.loading=false;ctx.request=null;await loadAccountHealth();if(healthContext===ctx&&message)reliabilityError('health-error',message);}}
 });
 $('diagnostic-run').addEventListener('click',()=>void runMediaDiagnostic());
 $('diagnostic-cancel').addEventListener('click',()=>diagnosticContext?.request?.abort());
 $('diagnostic-export').addEventListener('click',()=>void exportMediaDiagnostic());
});
document.addEventListener('click',event=>{
 const button=event.target.closest('button');if(!button)return;
 if(button.dataset.accountHealth)openAccountHealth(button.dataset.accountHealth,button);
 if(button.dataset.mediaDiagnose)openMediaDiagnostic(button.dataset.mediaDiagnose,button.dataset.diagnoseOutput,button);
 if(button.dataset.reliabilityClose)closeReliability(button.dataset.reliabilityClose);
});
document.addEventListener('change',event=>{
 const input=event.target,ctx=healthContext;if(!input.dataset.healthJob||!ctx)return;
 if(ctx.recovering){input.checked=ctx.selected.has(input.dataset.healthJob);return;}
 if(input.checked&&ctx.selected.size>=50){input.checked=false;reliabilityError('health-error','每次最多选择 50 项，请分批恢复。');return;}
 if(input.checked)ctx.selected.add(input.dataset.healthJob);else ctx.selected.delete(input.dataset.healthJob);
 healthSelectionChanged();
});
