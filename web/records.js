'use strict';
let recordsRenderKey='',recordsKind='generation',recordsPage=1,recordsPages=0,recordsScope=null,recordGroupContext=null;
const recordTriggers={scheduled:'定时执行',ingest:'入库触发',manual:'手动执行',schedule:'定时执行',event:'入库触发',retry:'失败重试'};
const recordNames={generation:'生成记录',playback:'播放记录',direct:'诊断记录',tools:'诊断记录',deliveries:'通知投递'};
const recordOutcomes={created:'已生成',completed:'任务完成',skipped:'相同内容跳过',existing:'已有 STRM',failed:'失败',partial:'部分失败',preview:'仅预览',cancelled:'已取消',interrupted:'已中断',redirected:'已返回 302',metadata:'HEAD 元信息可读',proxied:'中转响应已发送',handled:'请求已处理',resolved:'已取得下载地址',proxy_required:'需要中转',readable:'取流检查可读'};
const recordOutput=r=>r.output?genOutputPath(r.output):'—';
const recordEvents={strm:'STRM 文件',task:'生成任务',subtitle:'外挂字幕',nonmedia:'非媒体文件',play:'播放请求',head:'HEAD 预检',check:'取流自检',get:'播放请求',resolve:'仅解析'};
const recDate=t=>t?new Date(t).toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit'}):'生成时间未记录';
const recMode=r=>r.mode==='302'?'302 直连':r.mode==='proxy'?'服务器中转':r.mode==='proxy-required'?'需允许中转':'—';
const recLabel=r=>r.outcome==='resolved'?(r.mode==='302'?'已取得 302 地址':'已取得中转地址'):(recordOutcomes[r.outcome]||r.outcome);
const recClass=r=>['failed','partial','proxy_required','interrupted'].includes(r.outcome)?'error':['created','completed','redirected','metadata','readable','proxied','resolved'].includes(r.outcome)?'good':'muted';
function recordsOpen(){recordsLeave();recordsScope=createCleanupScope();recordsPopulateAccounts();recordsSwitch(recordsKind);recordsScope.interval(()=>{if(view==='records'&&!document.hidden&&!recordsScope.pending('list'))recordsLoad(recordsPage)},10000)}
function recordsLeave(){recordsScope?.dispose();recordsScope=null;recordsCloseDetail();$('records-table-panel').removeAttribute('aria-busy')}
function recordsCloseDetail(){recordsScope?.cancel('trace');recordGroupContext=null;$('records-detail').hidden=true;$('records-detail').removeAttribute('aria-busy')}
function recordsPopulateAccounts(){const el=$('records-account'),old=el.value;el.innerHTML='<option value="">全部网盘</option>'+state.accounts.map(a=>`<option value="${esc(a.id)}">${esc(a.name)}</option>`).join('');el.value=old}
function recordsFilters(){const params=new URLSearchParams({kind:recordsKind==='tools'?'direct':recordsKind,q:$('records-search').value.trim(),account:$('records-account').value,outcome:$('records-outcome').value,event:$('records-event').value,days:$('records-days').value,page:String(recordsPage),limit:$('records-limit').value});if(recordsKind==='playback')params.set('summary','1');if(recordsKind==='tools')params.set('scope','diagnostic');Object.entries(recordsContext).forEach(([k,v])=>params.set(k,v));return params}
function recordsResetList(message){
 recordsRenderKey='';recordsPages=0;
 $('records-head').innerHTML='';$('records-body').innerHTML='<tr><td colspan="6" class="empty"></td></tr>';
 $('records-body').querySelector('td').textContent=message;
 $('records-page').textContent='';$('records-prev').disabled=true;$('records-next').disabled=true;
 $('records-write-error').hidden=true;
}
function recordsSwitch(kind){
 if(kind==='direct')kind='tools';
 recordsKind=kind;recordsPage=1;recordsCloseDetail();
 recordsResetList('正在读取'+recordNames[kind]+'…');
 document.querySelectorAll('[data-record-kind]').forEach(button=>{const selected=button.dataset.recordKind===kind;button.setAttribute('aria-selected',String(selected));button.tabIndex=selected?0:-1});
 $('records-table-panel').setAttribute('aria-labelledby','records-'+kind+'-tab');
 const outcomes=kind==='generation'?['created','skipped','failed','partial','completed','preview','existing']:kind==='playback'?['redirected','metadata','proxied','failed']:kind==='deliveries'?['pending','sent','failed','paused','cancelled']:['resolved','readable','proxy_required','failed'];
 $('records-outcome').innerHTML='<option value="">全部结果</option>'+outcomes.map(v=>`<option value="${v}">${recordOutcomes[v]}</option>`).join('');
 const events=kind==='generation'?['strm','task','subtitle','nonmedia']:kind==='playback'?['play','head']:kind==='deliveries'?[]:['resolve','check'];
 $('records-event').innerHTML='<option value="">全部环节</option>'+events.map(v=>`<option value="${v}">${recordEvents[v]}</option>`).join('');
 $('records-explanation').textContent=kind==='generation'?'查看每次任务与文件的新增、跳过、预览和失败；已有 STRM 索引也可查。':kind==='playback'?'同一媒体、来源和客户端，相邻不超过 30 秒的同类请求汇总展示。失败单独列出；点击文件名查看每次请求的解析与响应。这些请求组不等于观看次数，已返回 302 表示跳转已发出。':'只展示手动「仅解析」和「取流自检」的最终结果。普通播放的解析过程可在播放记录详情中查看。';
 if(typeof syncRecordSections==='function')syncRecordSections();
 recordsLoad();
}
async function recordsLoad(page=1){
 if(!recordsScope)return;
 if(recordsKind==='deliveries'){await loadRecordDeliveries();return}
 recordsPage=page;const request=recordsScope.latest('list');
 $('records-table-panel').setAttribute('aria-busy','true');$('records-status').textContent='正在读取'+recordNames[recordsKind]+'…';$('records-error').hidden=true;
 try{const data=await api('/api/records?'+recordsFilters(),undefined,{signal:request.signal});if(!request.current())return;
 recordsPage=data.page;recordsPages=data.pages;$('records-status').textContent=recordsKind==='playback'?`${data.total} 组 · ${data.request_total} 次请求`:`${data.total} 条记录`;$('records-page').textContent=data.total?`${data.page} / ${data.pages} 页`:'暂无记录';$('records-prev').disabled=data.page<=1;$('records-next').disabled=!data.pages||data.page>=data.pages;
 $('records-write-error').textContent=data.write_error||'';$('records-write-error').hidden=!data.write_error;recordsRender(data.items);
 }catch(err){if(err.name==='AbortError'||!request.current())return;recordsResetList(recordNames[recordsKind]+'暂时无法读取，请点击「刷新记录」重试。');$('records-error').textContent=err.message+'。可点击刷新重试。';$('records-error').hidden=false;$('records-status').textContent='读取失败'}finally{if(request.current())$('records-table-panel').removeAttribute('aria-busy');request.finish()}
}
function recordIdentity(r){return `<button class="record-name" data-record-media="${esc(r.media)}" data-record-request="${esc(r.request)}" ${r.group_id?'data-record-group="'+esc(r.group_id)+'"':''} ${r.media?'':'disabled'} title="查看请求、解析和响应详情">${esc(r.name||'未知文件')}</button><small>${r.group_id?'播放请求汇总':esc(recordEvents[r.event]||r.event)}${r.rule_name?' · '+esc(r.rule_name):''}${r.trigger?' · '+esc(recordTriggers[r.trigger]||r.trigger):''}</small>${r.group_id?'<small class="record-summary-mobile">'+r.request_count+' 次请求 · 解析 '+r.resolve_count+' 次 · 复用 '+r.reuse_count+' 次</small>':''}`}
function recordResult(r){return `<span class="${recClass(r)}">${esc(recLabel(r))}</span><small>${esc(r.kind==='generation'?(r.error||r.note):recMode(r))}${r.http?' · HTTP '+r.http:''}${r.bytes?' · '+r.bytes.toLocaleString()+' 字节':''}</small>${r.kind!=='generation'&&r.error?`<small class="record-error">${esc(r.error)}</small>`:''}`}
function recordsRender(items){
 renderCompactRecords(items);return;
}

function renderCompactRecords(items){
 const key=JSON.stringify([recordsKind,items]);if(key===recordsRenderKey)return;recordsRenderKey=key;
 $('records-head').innerHTML='';
 $('records-body').innerHTML=items.length?items.map(r=>{
  const pathLine=(label,value)=>'<div class="compact-path"><span>'+label+'</span><code title="'+esc(value)+'">'+esc(value||'—')+'</code></div>';
  const source=recordsKind==='generation'?pathLine('云端',r.path)+pathLine('输出',recordOutput(r)):pathLine('云端',r.path)+'<p class="hint">'+esc(r.client||'内部检查')+' · '+esc(r.ip||'未知来源')+(recordsKind==='playback'?' · '+(r.request_count||1)+' 次请求 / 解析 '+(r.resolve_count||0)+' / 复用 '+(r.reuse_count||0):' · '+r.millis+' ms · '+esc(r.host||'未取得目标域名'))+'</p>';
  const detail='<dl><div><dt>云端路径</dt><dd><code>'+esc(r.path||'—')+'</code></dd></div>'+(r.output?'<div><dt>输出路径</dt><dd><code>'+esc(recordOutput(r))+'</code></dd></div>':'')+'<div><dt>结果</dt><dd>'+recordResult(r)+'</dd></div><div><dt>网盘 / 来源</dt><dd>'+esc(r.account_name||'未知网盘')+' · '+esc(recordTriggers[r.trigger]||r.client||r.trigger||'—')+'</dd></div>'+(r.job?'<div><dt>任务 ID</dt><dd><code>'+esc(r.job)+'</code></dd></div>':'')+'</dl>';
  return '<tr class="compact-record-row"><td colspan="6"><article class="compact-record"><div class="compact-identity"><i data-lucide="'+(r.event==='task'?'folder-sync':'file-video')+'" aria-hidden="true"></i><div>'+recordIdentity(r)+'</div></div><div class="compact-paths">'+source+'</div><div class="compact-tail"><span class="'+recClass(r)+'">'+esc(recLabel(r))+'</span><time>'+esc(recDate(r.time))+'</time></div><details class="compact-full"><summary>详情<span class="compact-inline-result '+recClass(r)+'">'+esc(recLabel(r))+'</span></summary>'+detail+'</details></article></td></tr>';
 }).join(''):'<tr><td colspan="6" class="empty">没有符合条件的'+recordNames[recordsKind]+'。调整筛选或执行对应操作后刷新查看。</td></tr>';icons();
}
const recPrecise=t=>new Date(t).toLocaleString('zh-CN',{hour12:false,month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',fractionalSecondDigits:3});
async function recordsGroupTrace(media,group,page=1){
 if(!recordsScope)return;
 const request=recordsScope.latest('trace');recordGroupContext={media,group};const area=$('records-detail');area.hidden=false;area.setAttribute('aria-busy','true');$('records-trace-body').innerHTML='<p class="hint">正在读取请求与解析详情…</p>';
 try{const data=await api('/api/records/trace?'+new URLSearchParams({media,group,page}),undefined,{signal:request.signal});if(!request.current())return;$('records-detail-name').textContent=data.playback[0]?.name||'播放请求详情';
  $('records-trace-body').innerHTML=`<p class="hint">共 ${data.total} 次原始请求 · 第 ${data.page} / ${data.pages} 页。每次请求的解析与最终响应在同一项内展示。</p>`+data.playback.map((r,i)=>{
   const direct=data.direct.filter(d=>r.request&&d.request===r.request).sort((a,b)=>a.time-b.time||a.id-b.id);
   return `<details class="record-trace-row" ${i===0?'open':''}><summary><span class="${recClass(r)}">${esc(recLabel(r))}</span><span>${esc(r.method||r.event)}</span><time>${esc(recPrecise(r.time))}</time></summary><ol class="record-request-steps"><li>收到${esc(r.method||'')}请求：${esc(r.client||'未知客户端')} · ${esc(r.ip||'未知来源')}</li>${direct.map(d=>`<li><span class="${recClass(d)}">${d.error?esc(d.error):d.cache?'复用缓存 / 并发解析结果':esc(recLabel(d))}</span> · ${d.millis} ms${d.host?' · '+esc(d.host):''}</li>`).join('')||'<li>没有关联的解析记录，或请求在解析前结束。</li>'}<li><span class="${recClass(r)}">${esc(recLabel(r))}</span>${r.http?' · HTTP '+r.http:''} · 总耗时 ${r.millis} ms${r.error?' · '+esc(r.error):''}</li></ol><dl><div><dt>云端路径</dt><dd><code>${esc(r.path||'—')}</code></dd></div><div><dt>请求 ID</dt><dd><code>${esc(r.request||'未记录')}</code></dd></div></dl></details>`
  }).join('')+`<div class="actions"><button data-trace-page="${data.page-1}" ${data.page<=1?'disabled':''}>上一页请求</button><button data-trace-page="${data.page+1}" ${data.page>=data.pages?'disabled':''}>下一页请求</button></div>`;
  area.scrollIntoView({block:'start',behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'});
 }catch(err){if(err.name!=='AbortError'&&request.current())$('records-trace-body').innerHTML=`<p class="error">${esc(err.message)}。请刷新列表重试。</p>`}finally{if(request.current())area.removeAttribute('aria-busy');request.finish()}
}
async function recordsTrace(media,request='',group=''){
 if(group)return recordsGroupTrace(media,group);
 if(!recordsScope)return;
 const pending=recordsScope.latest('trace');recordGroupContext=null;
 const area=$('records-detail');area.hidden=false;area.setAttribute('aria-busy','true');$('records-trace-body').innerHTML='<p class="hint">正在读取文件链路…</p>';
 try{const data=await api('/api/records/trace?media='+encodeURIComponent(media)+(request?'&request='+encodeURIComponent(request):''),undefined,{signal:pending.signal});if(!pending.current())return;const all=[...data.generation,...data.playback,...data.direct];$('records-detail-name').textContent=all.find(r=>r.name)?.name||'文件链路';
 $('records-trace-body').innerHTML=['generation','playback','direct'].map(kind=>`<section class="record-trace-section"><h3>${kind==='direct'?'解析与自检过程':recordNames[kind]}</h3>${data[kind].length?data[kind].map(r=>`<details class="record-trace-row ${request&&r.request===request?'record-linked':''}"><summary><span class="${recClass(r)}">${esc(recLabel(r))}</span><span>${esc(recordEvents[r.event]||r.event||'解析')}</span><time>${esc(recDate(r.time))}</time></summary><dl><div><dt>源路径</dt><dd><code>${esc(r.path||'—')}</code></dd></div>${r.output?`<div><dt>输出路径</dt><dd><code>${esc(recordOutput(r))}</code></dd></div>`:''}<div><dt>网盘 / 方式</dt><dd>${esc(r.account_name||'—')} · ${esc(recMode(r))}</dd></div>${r.host?`<div><dt>目标域名</dt><dd>${esc(r.host)}</dd></div>`:''}${r.ip?`<div><dt>连接来源 IP / UA</dt><dd>${esc(r.ip)}<br>${esc(r.user_agent||r.client)}</dd></div>`:''}${r.request?`<div><dt>关联请求</dt><dd><code>${esc(r.request)}</code></dd></div>`:''}${r.trigger?`<div><dt>执行来源</dt><dd>${esc(recordTriggers[r.trigger]||r.trigger)}</dd></div>`:''}${r.job?`<div><dt>生成任务</dt><dd><code>${esc(r.job)}</code></dd></div>`:''}<div><dt>结果</dt><dd>${esc(r.error||r.note||recLabel(r))}${r.http?' · HTTP '+r.http:''}${r.bytes?' · '+r.bytes+' 字节':''} · ${r.millis} ms</dd></div></dl></details>`).join(''):'<p class="hint">暂无该环节记录。旧版本的播放与解析历史未持久保存。</p>'}</section>`).join('');
 area.scrollIntoView({block:'start',behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'});
 }catch(err){if(err.name!=='AbortError'&&pending.current())$('records-trace-body').innerHTML=`<p class="error">${esc(err.message)}。关闭后重新点击文件重试。</p>`}finally{if(pending.current())area.removeAttribute('aria-busy');pending.finish()}
}
document.addEventListener('DOMContentLoaded',()=>{
 document.querySelectorAll('[data-record-kind]').forEach(button=>{button.addEventListener('click',()=>recordsSwitch(button.dataset.recordKind));button.addEventListener('keydown',event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const tabs=[...document.querySelectorAll('[data-record-kind]')],index=tabs.indexOf(button),next=event.key==='Home'?0:event.key==='End'?tabs.length-1:(index+(event.key==='ArrowLeft'?-1:1)+tabs.length)%tabs.length;tabs[next].focus();recordsSwitch(tabs[next].dataset.recordKind)})});
 $('records-filter-form').addEventListener('submit',event=>{event.preventDefault();recordsLoad()});for(const id of ['records-account','records-outcome','records-event','records-days','records-limit'])$(id).addEventListener('change',()=>recordsLoad());
 $('records-refresh').addEventListener('click',()=>recordsLoad(recordsPage));$('records-prev').addEventListener('click',()=>recordsLoad(recordsPage-1));$('records-next').addEventListener('click',()=>recordsLoad(recordsPage+1));$('records-detail-close').addEventListener('click',recordsCloseDetail);
 document.addEventListener('click',event=>{const tracePage=event.target.closest('[data-trace-page]');if(tracePage&&recordGroupContext){recordsGroupTrace(recordGroupContext.media,recordGroupContext.group,tracePage.dataset.tracePage);return}const ruleButton=event.target.closest('[data-view-rule-records]');if(ruleButton){const rule=ruleBy(ruleButton.dataset.viewRuleRecords);nav('records');$('records-search').value=rule?.source_path||'';$('records-account').value=rule?.account||'';recordsSwitch('generation');return}const button=event.target.closest('[data-record-media]');if(button?.dataset.recordMedia)recordsTrace(button.dataset.recordMedia,button.dataset.recordRequest,button.dataset.recordGroup)});
});
