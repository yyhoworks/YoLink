'use strict';
let dashData=null,dashKey='',dashDays=7,dashChart='created',consoleBusy=false,inventoryData={items:[],page:1,pages:0},inventorySerial=0,integrationLoaded=false;
let recordsContext={},lastNoticeLoad=0,inventoryMedia='',integrationData=null;
const selectedRules=new Set(),selectedOutputs=new Set();
const filterNames={all:'全部媒体',video:'仅影视',audio:'仅音乐',custom:'自定义'};
const healthNames={unchecked:'未体检',healthy:'检查通过',missing:'本地缺失',modified:'内容已变化',outdated:'入口需更新',source_missing:'云端缺失',source_unknown:'来源待核实',unreadable:'无法读取',index_error:'索引异常'};
const failureNames={credentials:'凭据失效',rate_limit:'请求受限',permission:'输出权限',space:'存储空间',conflict:'文件冲突',network:'网络异常',source:'来源目录',other:'执行异常'};
function msDate(v){return v?new Date(v).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}):'时间未记录'}
function consoleSection(title,body,action){return '<section class="panel console-section"><div class="section-heading"><h2>'+title+'</h2>'+(action||'')+'</div>'+body+'</section>'}
function consoleRow(title,body,action){return '<div class="console-row"><div><strong>'+title+'</strong><p class="hint">'+body+'</p></div>'+(action||'')+'</div>'}
function metric(label,value,detail,attrs){return '<button class="console-metric" '+attrs+'><span>'+label+'</span><strong>'+esc(value)+'</strong><small>'+detail+'</small></button>'}
async function refreshConsole(){
 if(consoleBusy)return;consoleBusy=true;
 try{
  if(view==='dashboard'){
   try{dashData=await api('/api/dashboard?days='+dashDays);renderDashboard();$('dash-error').hidden=true}
   catch(e){$('dash-updated').textContent=dashData?'上次更新于 '+msDate(dashData.time)+' · 读取失败':'读取失败，可重试';$('dash-error').textContent=e.message+(dashData?'。保留上次结果，请刷新重试。':'。请点击刷新重试。');$('dash-error').hidden=false;if(!dashData)$('dash-content').innerHTML='<div class="panel console-loading"><h2>暂时无法读取运行数据</h2><p class="hint">请检查服务状态，并点击上方刷新重试。</p></div>'}finally{$('dash-content').setAttribute('aria-busy','false')}
  }
  if(view==='notifications'||Date.now()-lastNoticeLoad>15000){await loadNotifications();lastNoticeLoad=Date.now()}
  if(view==='settings'&&integrationLoaded)await loadIntegrations();
 }finally{consoleBusy=false}
}
function consoleNav(next,context={}){
 if(next==='dashboard')refreshConsole();
 if(next==='inventory'){inventoryMedia=context.media||'';if(inventoryMedia)['search','account','rule','status'].forEach(k=>$('inventory-'+k).value='');loadInventory()}
 if(next==='notifications')loadNotifications().catch(e=>toast(e.message));
 if(next==='settings'&&!integrationLoaded)loadIntegrations().catch(e=>toast(e.message));
}
function renderDashboard(){
 const d=dashData;if(!d)return;
 $('dash-updated').textContent='更新于 '+msDate(d.time);
 const key=JSON.stringify([d.overview,d.stats,d.accounts,d.reports,d.plans,d.jobs,d.recent,d.errors,d.trend,d.write_error,dashChart]);
 if(key===dashKey)return;dashKey=key;
 const s=d.stats,o=d.overview,enabled=d.accounts.filter(a=>!a.disabled),bad=enabled.filter(a=>d.reports[a.id]?.checked&&!d.reports[a.id].reachable),unknown=enabled.filter(a=>!d.reports[a.id]?.checked);
 const ratio=s.resolve_total?(s.resolved/s.resolve_total*100).toFixed(1)+'%':'—';
 let html='<div class="dashboard-overviews">';
 html+=consoleSection('STRM 生成',metric('STRM 索引总数',o.outputs,'按本服务登记统计','data-nav="inventory"')+metric('今日新增',s.today_created,'实际新增文件','data-dashboard-record="generation" data-created-today="true"'),' <button class="text" data-nav="home">生成管理</button>');
 html+=consoleSection('网盘',metric('启用账号',enabled.length+' / '+d.accounts.length,'全部账号','data-nav="accounts"')+metric('最近检测异常',bad.length,unknown.length+' 个未检测','data-nav="accounts"'),'<button class="text" data-nav="accounts">网盘管理</button>');
 html+=consoleSection('302 直连',metric('入口请求 / 24h',s.requests,'媒体 GET 请求','data-dashboard-record="playback"')+metric('地址解析成功率',ratio,s.resolve_total+' 次请求解析（含缓存）','data-nav="playback"'),'<button class="text" data-nav="playback">直连工具</button>');
 html+='</div>';
 if(!d.accounts.length||!state.rules.length||!state.settings.public_base){
  const target=!d.accounts.length?'accounts':!state.rules.length?'config':'settings',text=!d.accounts.length?'连接第一个网盘':!state.rules.length?'添加目录规则':'设置播放入口';
  html+='<div class="generation-banner"><i data-lucide="sparkles"></i><div><strong>开始搭建媒体入口</strong><p>连接网盘 → 选择目录 → 预览映射 → 首次生成</p></div><button data-nav="'+target+'">'+text+'</button></div>';
 }
 const active=d.jobs.filter(genActive),failures=state.failures||[];
 let tasks='<p class="console-summary">'+o.running+' 个运行 · '+o.queued+' 个排队 · '+o.failed+' 个失败任务待处理</p>';
 tasks+='<div class="task-pills"><div class="task-pill"><span>正在运行</span><strong>'+o.running+'</strong></div><div class="task-pill"><span>等待队列</span><strong>'+o.queued+'</strong></div><div class="task-pill"><span>待处理异常</span><strong class="'+(o.failed?'warn':'good')+'">'+o.failed+'</strong></div></div>';
 if(active.length)tasks+=active.slice(0,4).map((j,i)=>consoleRow(esc(genRuleName(j.rule)),esc(statusNames[j.status])+' · '+esc(j.current_path||j.source_path)+'<br>'+esc(genResult(j))+(j.resumes?' · 已从断点恢复 '+j.resumes+' 次':''),'<button data-nav="tasks">查看任务</button>')).join('');
 else tasks+='<p class="hint">当前没有生成任务。定时计划和手动生成可随时触发。</p>';
 tasks+=failures.slice(0,3).map(j=>consoleRow('<span class="error">'+esc(failureNames[j.error_kind]||'生成失败')+'</span> · '+esc(genRuleName(j.rule)),esc(j.message),'<button data-nav="tasks">处理失败</button>')).join('');
 tasks+=bad.map(a=>consoleRow('<span class="error">网盘检测失败</span> · '+esc(a.name),esc(d.reports[a.id].error||'请检查凭据和网络'),'<button data-nav="accounts">检查账号</button>')).join('');
 if(d.write_error)tasks+='<p class="error">'+esc(d.write_error)+'</p>';
 const maxValue=Math.max(1,...d.trend.map(v=>v[dashChart]));
 const bars=d.trend.map(v=>'<button class="trend-day" data-trend-day="'+v.day+'" title="'+v.day+'：'+(v.covered?v[dashChart]:'未采集')+'" aria-label="'+v.day+'，'+(v.covered?v[dashChart]:'未采集')+(dashChart==='created'?' 个新增 STRM':' 次请求')+'"><span class="trend-count">'+(v.covered?v[dashChart]:'—')+'</span><span class="trend-column"><span style="height:'+(v.covered&&v[dashChart]?Math.max(6,Math.round(v[dashChart]/maxValue*78)):4)+'px"></span></span><small>'+v.day.slice(5)+'</small></button>').join('');
 html+='<div class="dash-middle-grid">'+consoleSection('当前任务与待处理事项',tasks,'<button class="text" data-nav="notifications">查看通知</button>')+consoleSection('运行趋势','<div class="trend-controls"><div class="actions"><button data-chart="created" aria-pressed="'+(dashChart==='created')+'">新增 STRM</button><button data-chart="requests" aria-pressed="'+(dashChart==='requests')+'">入口请求</button></div><label class="inline-label">时间范围<select id="dash-days"><option value="7" '+(dashDays===7?'selected':'')+'>近 7 天</option><option value="30" '+(dashDays===30?'selected':'')+'>近 30 天</option></select></label></div><div class="trend-chart">'+bars+'</div><p class="hint">统计自 '+esc(new Date(d.metrics_since).toLocaleDateString('zh-CN'))+' 启用，之前日期无完整数据。请求统计不代表观看次数。</p>')+'</div>';
 const clouds=d.accounts.length?d.accounts.map(a=>{const p=d.reports[a.id],status=a.disabled?'已停用':!p?.checked?'未检测':p.reachable?'最近检测可用':'最近检测失败';const quota=p?.quota;return consoleRow(esc(a.name)+' <span class="'+(p?.reachable?'good':'muted')+'">'+status+'</span>',esc(names[a.provider]||a.provider)+' · '+(p?.checked?msDate(p.checked*1000):'尚未测试')+(quota?'<br>已用 '+consoleBytes(quota.used)+' / '+consoleBytes(quota.total):'<br>容量未读取或该通道不支持'),'<button data-nav="accounts">管理</button>')}).join(''):'<p class="hint">还没有网盘账号。</p>';
 let direct='<dl class="console-stats"><div><dt>平均解析耗时</dt><dd>'+(s.resolve_total?Math.round(s.resolve_ms/s.resolve_total)+' ms':'—')+'</dd></div><div><dt>上游请求耗时</dt><dd>'+(s.upstream_count?Math.round(s.upstream_ms/s.upstream_count)+' ms':'—')+'</dd></div><div><dt>缓存 / 并发复用</dt><dd>'+s.cache+' 次</dd></div><div><dt>解析失败</dt><dd>'+(s.resolve_total-s.resolved)+' 次</dd></div></dl>';
 direct+=d.errors.length?d.errors.slice(0,3).map(v=>consoleRow(esc(v.name),'<span class="error">'+esc(v.error)+'</span> · '+msDate(v.time),'<button data-dashboard-record="direct">记录</button>')).join(''):'<p class="hint">暂无已记录的解析错误。</p>';
 direct+='<p class="hint">最近 24 小时；HEAD 和自检单独留在记录中心。返回 302 表示跳转已发送，实际播放效果需播放器验证。</p>';
 html+='<div class="console-two">'+consoleSection('网盘状态与容量',clouds)+consoleSection('302 解析情况',direct,'<button class="text" data-nav="playback">取流自检</button>')+'</div>';
 const plans=d.plans.length?d.plans.slice().sort((a,b)=>(a.next||Infinity)-(b.next||Infinity)).slice(0,5).map(p=>{const busy=active.some(j=>j.rule===p.id),disabled=accountBy(p.account)?.disabled;return consoleRow(esc(p.name),disabled?'网盘已停用':busy?'已有任务运行或排队':p.enabled?'预计 '+msDate(Math.max(Date.now(),p.next*1000))+' 检查':'仅手动执行','<button data-gen-generate="'+esc(p.id)+'" '+(disabled||busy?'disabled':'')+'>运行</button>')}).join(''):'<p class="hint">添加目录规则后，这里显示执行计划。</p>';
 const recent=d.recent.length?d.recent.map(v=>consoleRow(esc(v.name),esc(v.path)+' · '+msDate(v.created),'<button data-inventory-media="'+esc(v.media)+'">查看</button>')).join(''):'<p class="hint">升级后实际新增的 STRM 会显示在这里。</p>';
 html+='<div class="console-two">'+consoleSection('执行计划',plans,'<button class="text" data-nav="config">目录规则</button>')+consoleSection('最近生成文件',recent,'<button class="text" data-nav="inventory">全部文件</button>')+'</div>';
 const focus=document.activeElement?.id;$('dash-content').innerHTML=html;$('dash-content').setAttribute('aria-busy','false');if(focus&&$(focus))$(focus).focus({preventScroll:true});icons();
}
function consoleBytes(v){if(v>=1099511627776)return(v/1099511627776).toFixed(2)+' TB';if(v>=1073741824)return(v/1073741824).toFixed(1)+' GB';return(v/1048576).toFixed(1)+' MB'}
let ruleCardsKey='';
function renderRuleConsole(){
 const select=$('rule-account-filter');if(!select)return;
 const opened=new Set([...$('gen-rule-list').querySelectorAll('details[data-rule-detail][open]')].map(el=>el.dataset.ruleDetail));
 const moreOpened=new Set([...$('gen-rule-list').querySelectorAll('details[data-rule-more][open]')].map(el=>el.dataset.ruleMore));
 genSetOptions(select,[{id:'',label:'全部网盘'},...state.accounts.map(a=>({id:a.id,label:a.name}))],select.value);
 const text=$('rule-search').value.trim().toLowerCase(),account=select.value,status=$('rule-status-filter').value;
 const rules=state.rules.filter(r=>{const last=state.rule_latest?.[r.id]||state.jobs.find(j=>j.rule===r.id&&!j.dry_run);return(!text||[r.name,r.source_path,r.output].join(' ').toLowerCase().includes(text))&&(!account||r.account===account)&&(!status||status==='auto'&&r.enabled||status==='manual'&&!r.enabled||status==='failed'&&last&&genFailed(last))});
 $('rule-result-count').textContent=rules.length+' 条规则 · 已选择 '+selectedRules.size+' 条';
 const cardKey=JSON.stringify([rules,state.accounts,state.rule_latest,state.jobs,[...selectedRules]]);
 if(cardKey===ruleCardsKey)return;
 ruleCardsKey=cardKey;
 $('gen-rule-list').innerHTML=rules.length?rules.map(r=>{
  const a=accountBy(r.account),last=state.rule_latest?.[r.id]||state.jobs.find(j=>j.rule===r.id&&!j.dry_run),active=state.jobs.find(j=>j.rule===r.id&&genActive(j)),busy=!!active;
  const next=r.enabled?'预计 '+msDate(Math.max(Date.now(),(r.last_run+r.interval)*1000))+' 检查':'按需手动运行';
  const failed=last&&genFailed(last),tone=a?.disabled?'muted':busy?'good':failed?'warning':'muted';
  const statusLabel=!a?'账号不可用':a.disabled?'网盘已停用':busy?(active.status==='queued'?'等待执行':'正在生成'):failed?'需要处理':r.enabled?'自动生成已开启':'手动生成';
  const result=busy?genResult(active):last?'上次'+(statusNames[last.status]||last.status)+' · '+genResult(last):'尚未执行，先预览目录差异';
  return `<article class="generation-rule rule-card">
   <div class="rule-card-heading"><input type="checkbox" data-rule-select="${esc(r.id)}" ${selectedRules.has(r.id)?'checked':''} aria-label="选择规则 ${esc(r.name)}"><i data-lucide="folder-sync" aria-hidden="true"></i><div><h3>${esc(r.name)}</h3><p>${esc(a?.name||'账号不可用')}</p></div><button class="text" data-gen-edit="${esc(r.id)}" aria-label="编辑 ${esc(r.name)}" title="编辑规则"><i data-lucide="pencil" aria-hidden="true"></i></button></div>
   <div class="rule-card-body"><div class="rule-state-row"><span class="rule-state ${tone}">${esc(statusLabel)}</span><label class="checkbox auto-checkbox"><input type="checkbox" data-gen-toggle="${esc(r.id)}" ${r.enabled?'checked':''}>自动生成</label></div>
   <div><p class="rule-run-result">${esc(result)}</p><p class="rule-schedule">${esc(a?.disabled?'启用网盘后可继续生成':busy?'当前任务结束后可再次运行':next)}</p></div>
   <details class="rule-details" data-rule-detail="${esc(r.id)}" ${opened.has(r.id)?'open':''}><summary>目录与生成选项<i data-lucide="chevron-down" aria-hidden="true"></i></summary><div class="mapping-paths"><div><span>网盘目录</span><code>${esc(r.source_path)}</code></div><div><span>STRM 输出目录</span><code>${esc(genOutputPath(r.output))}</code></div></div><p class="rule-option-summary">${esc(filterNames[r.filter?.preset||'all'])} · 字幕 ${r.copy_subtitles?'开':'关'} · 非媒体 ${r.copy_non_media?'开':'关'}${r.refresh_library?' · 刷新媒体库':''}</p><button class="text" data-rule-check="${esc(r.id)}"><i data-lucide="list-checks" aria-hidden="true"></i>检查配置</button></details>
   <div class="mapping-bottom"><div class="actions"><button class="primary" data-gen-generate="${esc(r.id)}" ${!a||a.disabled||busy?'disabled':''}><i data-lucide="play" aria-hidden="true"></i>${busy?'已在队列':'运行生成'}</button><button data-gen-preview="${esc(r.id)}" ${busy||!a||a.disabled?'disabled':''}>差异预览</button><button class="text" data-rule-records="${esc(r.id)}">记录</button></div><details class="rule-more" data-rule-more="${esc(r.id)}" ${moreOpened.has(r.id)?'open':''}><summary>更多操作</summary><div><button class="text" data-gen-trial="${esc(r.id)}" ${busy||!a||a.disabled?'disabled':''}>试生成 10 个</button><button class="text" data-playlist-rule="${esc(r.id)}">下载 M3U</button><button class="text" data-rule-copy="${esc(r.id)}">复制规则</button><button class="text error" data-gen-remove="${esc(r.id)}">移除规则</button></div></details></div>
   </div></article>`;
 }).join(''):'<div class="empty"><h3>'+(state.rules.length?'没有匹配的规则':'添加第一个目录规则')+'</h3><p>选择网盘目录与输出位置，先预览再生成。</p><button id="gen-empty-add">新增规则</button></div>';
 icons();
}
function loadRuleExtras(r){
 const f=r.filter||{};$('filter-preset').value=f.preset||'all';$('filter-extensions').value=f.extensions||'';$('filter-dirs').value=f.exclude_dirs||'';$('filter-words').value=f.exclude_words||'';$('filter-pattern').value=f.pattern||'';$('filter-size').value=(f.min_bytes||0)/1048576;$('rule-refresh-library').checked=!!r.refresh_library;$('rule-notify').checked=!!r.notify;$('filter-test-result').textContent='';updateFilterFields();$('filter-options').open=!!(f.exclude_dirs||f.exclude_words||f.pattern||f.min_bytes);$('rule-after-options').open=!!(r.refresh_library||r.notify);updateAfterSummary();
}
function updateFilterFields(){
 const custom=$('filter-preset').value==='custom';$('filter-extensions-field').hidden=!custom;$('filter-extensions').required=custom;
 const count=[$('filter-dirs').value.trim(),$('filter-words').value.trim(),$('filter-pattern').value.trim(),Number($('filter-size').value)>0].filter(Boolean).length;
 $('filter-summary').textContent=count?'已设置 '+count+' 项':'未设置';
}
function updateAfterSummary(){$('rule-after-summary').textContent=[$('rule-refresh-library').checked?'刷新媒体库':'',$('rule-notify').checked?'Webhook 通知':''].filter(Boolean).join(' · ')||'未启用'}
$('filter-preset').addEventListener('change',updateFilterFields);
for(const id of ['filter-dirs','filter-words','filter-pattern','filter-size'])$(id).addEventListener('input',updateFilterFields);
for(const id of ['rule-refresh-library','rule-notify'])$(id).addEventListener('change',updateAfterSummary);
function readRuleExtras(){return{filter:{preset:$('filter-preset').value,extensions:$('filter-extensions').value.trim(),exclude_dirs:$('filter-dirs').value.trim(),exclude_words:$('filter-words').value.trim(),pattern:$('filter-pattern').value.trim(),min_bytes:Math.round(Number($('filter-size').value)*1048576)},refresh_library:$('rule-refresh-library').checked,notify:$('rule-notify').checked}}
function jobExtraDetails(j){
 let html='';
 if(j.resumes)html+='<p class="hint">已从持久断点恢复 '+j.resumes+' 次；成功文件保持不变。</p>';
 if(j.error_kind){const target=j.error_kind==='credentials'?'accounts':j.error_kind==='conflict'?'inventory':j.error_kind==='source'?'config':'settings';html+='<p class="hint">'+esc(failureNames[j.error_kind]||'执行异常')+' · <button class="text" data-nav="'+target+'">前往处理</button></p>'}
 if(j.retry_of)html+='<p class="hint">'+(j.retry_dirs?.length?'本次重扫 '+j.retry_dirs.length+' 个失败或未完成目录':'原任务没有目录断点，本次重扫原范围')+'</p>';
 if(j.dry_run&&j.preview?.length){const labels={new:'待新增',same:'已有相同',conflict:'内容冲突',filtered:'被过滤'};html+='<div class="table-scroll"><table class="preview-table"><thead><tr><th>源文件</th><th>输出位置</th><th>预览结果</th></tr></thead><tbody>'+j.preview.map(v=>'<tr><td><code>'+esc(v.source)+'</code></td><td><code>'+esc(v.output||'—')+'</code></td><td>'+labels[v.outcome]+'</td></tr>').join('')+'</tbody></table></div><p class="hint">最多展示 100 条映射；预览不下载字幕和非媒体文件。</p>'}
 return html;
}
function outputRow(v){
 const source=esc(v.source),output=esc(v.output);
 return '<tr><td><label class="output-select"><input type="checkbox" data-output-select="'+esc(v.path)+'" aria-label="选择 '+esc(v.name)+'"></label></td><td><strong>'+esc(v.name)+'</strong><small>'+esc(accountBy(v.account)?.name||'未知账号')+' · '+esc(ruleBy(v.rule)?.name||'旧索引，规则未记录')+'</small><code class="inventory-source-desktop">'+source+'</code><details class="inventory-mobile-detail"><summary>源与输出路径</summary><dl><div><dt>网盘路径</dt><dd><code>'+source+'</code></dd></div><div><dt>STRM 输出</dt><dd><code>'+output+'</code></dd></div></dl></details></td><td><code>'+output+'</code></td><td><strong class="'+(v.status==='healthy'?'good':v.status==='unchecked'?'muted':'error')+'">'+esc(healthNames[v.status]||v.status)+'</strong><small>'+esc(v.note||'尚未检查本地文件')+'</small><small>'+msDate(v.checked)+'</small></td><td><small>生成时间：'+msDate(v.created)+'</small><div class="inventory-actions"><button class="text" data-output-trace="'+esc(v.media)+'">链路</button><button class="text" data-copy-url="'+esc(v.url)+'">复制入口</button><button class="text" data-playlist-media="'+esc(v.media)+'">M3U</button><button class="text" data-probe="'+esc(v.media)+'">取流自检</button></div></td></tr>';
}
function updateOutputSelection(){
 const count=selectedOutputs.size;for(const id of ['inventory-check','inventory-cloud-check','inventory-repair'])$(id).disabled=count===0;
 $('inventory-all').checked=count>0&&count===inventoryData.items.length;$('inventory-all').indeterminate=count>0&&count<inventoryData.items.length;
 $('inventory-count').textContent=(inventoryData.total||0)+' 个登记的 STRM'+(count?' · 已选择 '+count+' 项':'');
}
async function loadInventory(page=1){
 const serial=++inventorySerial;
 genSetOptions($('inventory-account'),[{id:'',label:'全部网盘'},...state.accounts.map(a=>({id:a.id,label:a.name}))],$('inventory-account').value);
 genSetOptions($('inventory-rule'),[{id:'',label:'全部规则'},...state.rules.map(r=>({id:r.id,label:r.name}))],$('inventory-rule').value);
 const q=new URLSearchParams({q:$('inventory-search').value.trim(),account:$('inventory-account').value,rule:$('inventory-rule').value,status:$('inventory-status').value,media:inventoryMedia,page,limit:$('inventory-limit').value});
 $('inventory-error').hidden=true;$('inventory-body').setAttribute('aria-busy','true');
 try{
  const data=await api('/api/outputs?'+q);if(serial!==inventorySerial)return;inventoryData=data;selectedOutputs.clear();$('inventory-all').checked=false;
  $('inventory-context').hidden=!inventoryMedia;$('inventory-context').textContent=inventoryMedia?'已定位到所选文件 · 点击显示全部文件':'';$('inventory-count').textContent=data.total+' 个登记的 STRM';$('inventory-page').textContent=data.total?data.page+' / '+data.pages+' 页':'暂无文件';$('inventory-prev').disabled=data.page<=1;$('inventory-next').disabled=!data.pages||data.page>=data.pages;
  $('inventory-body').innerHTML=data.items.length?data.items.map(outputRow).join(''):'<tr><td colspan="5" class="empty">没有匹配的文件。生成 STRM 后可在这里查看与体检。</td></tr>';updateOutputSelection();
 }catch(e){$('inventory-error').textContent=e.message;$('inventory-error').hidden=false}finally{$('inventory-body').setAttribute('aria-busy','false')}
}
async function checkInventory(remote){if(!selectedOutputs.size)throw new Error('请先勾选需要体检的文件');$('inventory-count').textContent=remote?'正在核对云端来源…':'正在核对本地文件…';try{const data=await api('/api/outputs/check',{paths:[...selectedOutputs],remote});await loadInventory(inventoryData.page);toast('已完成 '+data.items.length+' 项体检')}catch(e){$('inventory-count').textContent=inventoryData.total+' 个登记的 STRM · 本次体检未完成';$('inventory-error').textContent=e.message+'。可重新体检，或刷新查看已经保存的结果。';$('inventory-error').hidden=false;throw e}}
async function loadNotifications(){
 const data=await api('/api/notifications');const unread=data.items.filter(v=>!v.read).length;$('notice-count').textContent=unread||'';$('notice-list').innerHTML=data.items.length?data.items.map(v=>consoleRow('<span class="'+(v.level==='error'?'error':'')+'">'+esc(v.title)+'</span>'+(!v.read?' <span class="notice-unread">未读</span>':''),esc(v.body)+'<br>'+msDate(v.created))).join(''):'<div class="empty">有新增、生成失败或联动异常时，通知会显示在这里。</div>';
}
function openConsoleRecords(kind,context={}){
 nav('records');const created=!!context.created;recordsContext=context;recordsSwitch(kind);$('records-search').value='';$('records-account').value='';$('records-days').value=kind==='generation'||context.date?'0':'1';$('records-event').value=created?'strm':kind==='playback'?'play':'';
 $('records-outcome').value=context.created?'created':'';delete recordsContext.created;updateRecordContext();recordsLoad();
}
function updateRecordContext(){$('record-context').hidden=!Object.keys(recordsContext).length;$('record-context').textContent=Object.keys(recordsContext).length?'已定位到指定规则、任务或日期 · 点击清除':''}
document.addEventListener('click',event=>{
 const b=event.target.closest('button');if(!b)return;

 if(b.dataset.chart){dashChart=b.dataset.chart;renderDashboard()}
 if(b.dataset.dashboardRecord)openConsoleRecords(b.dataset.dashboardRecord,b.dataset.createdToday?{created:true,date:new Date().toLocaleDateString('sv-SE',{timeZone:'Asia/Shanghai'})}:{});
 if(b.dataset.trendDay)openConsoleRecords(dashChart==='created'?'generation':'playback',{date:b.dataset.trendDay});
 if(b.dataset.inventoryMedia){nav('inventory',true,{media:b.dataset.inventoryMedia})}
 if(b.dataset.outputTrace){nav('records');recordsTrace(b.dataset.outputTrace)}
 if(b.dataset.probe&&view==='inventory')nav('playback');
 if(b.dataset.ruleRecords)openConsoleRecords('generation',{rule:b.dataset.ruleRecords});
 if(b.dataset.ruleCopy){const r=ruleBy(b.dataset.ruleCopy);generationEditRule(r.id);genDraft.id='';genDraft.enabled=false;$('gen-rule-enabled').checked=false;$('gen-rule-output').value='';$('gen-rule-name').value=r.name+' 副本';$('gen-editor-title').textContent='复制目录规则';generationScheduleSummary();$('gen-rule-output').focus();toast('副本默认仅手动，请选择新的输出目录')}
 if(b.dataset.ruleCheck)action(b,async()=>{const data=await api('/api/rules/'+b.dataset.ruleCheck+'/check',{});$('gen-check-result').hidden=false;$('gen-check-result').innerHTML='<h3>配置检查</h3>'+data.checks.map(v=>'<p class="'+(v.ok?'good':'error')+'">'+esc(v.name)+'：'+esc(v.note)+'</p>').join('')+'<p class="hint">'+esc(data.note)+'</p>';$('gen-check-result').scrollIntoView({block:'nearest'})});
 if(b.dataset.retryDelivery)action(b,async()=>{await api('/api/deliveries/'+b.dataset.retryDelivery+'/retry',{});await loadIntegrations();if(view==='records'&&recordsKind==='deliveries')await loadRecordDeliveries();toast('联动已加入重试队列')});
});
$('dash-refresh').addEventListener('click',e=>action(e.currentTarget,async()=>{await refreshConsole()}));
document.addEventListener('change',e=>{if(e.target.id==='dash-days'){dashDays=Number(e.target.value);refreshConsole()}if(e.target.dataset.ruleSelect){if(e.target.checked)selectedRules.add(e.target.dataset.ruleSelect);else selectedRules.delete(e.target.dataset.ruleSelect);$('rule-result-count').textContent='已选择 '+selectedRules.size+' 条规则'}if(e.target.dataset.outputSelect){if(e.target.checked)selectedOutputs.add(e.target.dataset.outputSelect);else selectedOutputs.delete(e.target.dataset.outputSelect);updateOutputSelection()}});
$('rule-filters').addEventListener('submit',e=>{e.preventDefault();renderRuleConsole()});
$('rule-account-filter').addEventListener('change',renderRuleConsole);$('rule-status-filter').addEventListener('change',renderRuleConsole);
$('rule-run-selected').addEventListener('click',e=>action(e.currentTarget,async()=>{if(!selectedRules.size)throw new Error('请先选择目录规则');let count=0,errors=[];for(const id of selectedRules){try{await api('/api/jobs',{rule:id});count++}catch(e){errors.push(e.message)}}await refresh();nav('tasks');toast('已提交 '+count+' 条规则'+(errors.length?'；'+errors.join('；'):''))}));
$('filter-test').addEventListener('click',e=>action(e.currentTarget,async()=>{const data=await api('/api/filters/preview',{filter:readRuleExtras().filter,names:$('filter-samples').value.split('\n').filter(Boolean)});$('filter-test-result').innerHTML=data.items.map(v=>'<p class="'+(v.included?'good':'muted')+'">'+esc(v.name)+' · '+(v.included?'纳入媒体':'排除 / 非所选媒体')+'</p>').join('')+'<p class="hint">样例仅检查路径和类型，实际文件大小在扫描时判断。</p>'}));
$('inventory-filters').addEventListener('submit',e=>{e.preventDefault();inventoryMedia='';loadInventory()});$('inventory-refresh').addEventListener('click',()=>loadInventory(inventoryData.page));$('inventory-prev').addEventListener('click',()=>loadInventory(inventoryData.page-1));$('inventory-next').addEventListener('click',()=>loadInventory(inventoryData.page+1));$('inventory-limit').addEventListener('change',()=>loadInventory());
$('inventory-all').addEventListener('change',e=>{document.querySelectorAll('[data-output-select]').forEach(input=>{input.checked=e.target.checked;if(input.checked)selectedOutputs.add(input.dataset.outputSelect);else selectedOutputs.delete(input.dataset.outputSelect)});updateOutputSelection()});
$('inventory-check').addEventListener('click',e=>action(e.currentTarget,()=>checkInventory(false)));$('inventory-cloud-check').addEventListener('click',e=>action(e.currentTarget,()=>checkInventory(true)));
$('inventory-repair').addEventListener('click',e=>action(e.currentTarget,async()=>{
 if(!selectedOutputs.size)throw new Error('请先勾选需要修复的文件');
 const result=await api('/api/outputs/repair',{paths:[...selectedOutputs]});await loadInventory(inventoryData.page);
 if(result.failed){$('inventory-error').textContent='修复 '+result.restored+' 项，保留 '+result.preserved+' 项，'+result.failed+' 项未完成。'+result.items.filter(v=>v.status==='failed').slice(0,3).map(v=>v.note).join('；');$('inventory-error').hidden=false}
 toast('已修复 '+result.restored+' 项，保留 '+result.preserved+' 项'+(result.failed?'，'+result.failed+' 项未完成':''));
}));
$('notice-read').addEventListener('click',e=>action(e.currentTarget,async()=>{await api('/api/notifications/read',{});await loadNotifications()}));
$('records-filter-form').insertAdjacentHTML('afterend','<button id="record-context" class="text" hidden></button>');$('record-context').addEventListener('click',()=>{recordsContext={};updateRecordContext();recordsLoad()});
$('settings-form').insertAdjacentHTML('afterend',
 '<form id="integration-form" class="panel form-panel integration-form"><h2>媒体库刷新与通知</h2><p class="hint">保存后，在目录规则的「生成后操作」中启用。联动发送失败可单独重试。</p>'+
 '<section class="generation-options"><h3>媒体库</h3><label class="checkbox"><input id="integration-enabled" type="checkbox">启用媒体库联动</label><div class="form-grid"><label>媒体服务器<select id="integration-type"><option value="emby">Emby</option><option value="jellyfin">Jellyfin</option></select></label><label>服务地址<input id="integration-base" type="url" placeholder="http://NAS-IP:8096"></label><label>API Key<input id="integration-key" type="password" autocomplete="new-password" placeholder="填写新密钥；留空保留已有密钥"><small id="integration-key-state" class="hint"></small></label><label class="checkbox"><input id="integration-all" type="checkbox">刷新整个媒体库</label><label>YoLink 内的输出目录前缀<input id="integration-local" placeholder="/CloudNAS/影视"></label><label>媒体服务器内对应的路径前缀<input id="integration-remote" placeholder="/media/影视"></label></div><p class="hint">按目录刷新时，两个路径前缀须指向同一份文件；例如 /CloudNAS/影视 → /media/影视。</p><button id="integration-test" type="button">测试媒体库连接</button></section>'+
 '<section class="generation-options"><h3>Webhook 通知</h3><label class="checkbox"><input id="webhook-enabled" type="checkbox">启用任务通知 Webhook</label><label>通知接收地址<input id="integration-webhook" type="password" autocomplete="new-password" placeholder="填写新地址；留空保留已有地址"><small id="integration-webhook-state" class="hint"></small></label><label>通知签名密钥（可选）<input id="integration-webhook-secret" type="password" autocomplete="new-password" placeholder="与接收端一致；留空保留已有密钥"><small id="integration-webhook-secret-state" class="hint"></small></label><p class="hint">发送 JSON 任务结果。Hermes 可直接接收通知；填写双方一致的签名密钥后，使用带时间戳的 HMAC-SHA256 签名验证。其他通知平台可通过网关转换格式。</p><button id="webhook-test" type="button">发送测试通知</button></section>'+
 '<div class="form-actions"><button type="submit" class="primary">保存联动设置</button><span id="integration-result" role="status"></span></div><section class="generation-options"><h3>最近联动发送</h3><div id="integration-deliveries"></div></section></form>');
async function loadIntegrations(){
 try{
 const data=await api('/api/integrations'),c=data.config;integrationData=data;
 if(!integrationLoaded){$('integration-enabled').checked=c.enabled;$('integration-type').value=c.server_type||'emby';$('integration-base').value=c.base||'';$('integration-local').value=c.local_prefix||'';$('integration-remote').value=c.remote_prefix||'';$('integration-all').checked=c.refresh_all;$('webhook-enabled').checked=c.webhook_enabled;integrationLoaded=true}
 $('integration-key-state').textContent=c.has_api_key?'已有密钥，留空将保留':'尚未配置密钥';$('integration-webhook-state').textContent=c.has_webhook?'已有地址，留空将保留':'尚未配置通知接收地址';
 $('integration-webhook-secret-state').textContent=c.has_webhook_secret?'已有签名密钥，留空将保留':'未配置签名密钥，将发送普通 JSON';
 const labels={pending:'等待发送 / 重试',sent:'已发送',failed:'发送失败'};
 $('integration-deliveries').innerHTML=data.deliveries.length?data.deliveries.map(v=>consoleRow(v.kind==='library'?'媒体库刷新':'任务通知',esc(labels[v.status])+' · 尝试 '+v.attempts+' 次'+(v.error?'<br>'+esc(v.error):''),v.status==='failed'?'<button data-retry-delivery="'+esc(v.id)+'">重试</button>':'')).join(''):'<p class="hint">启用规则的生成后操作后，这里显示发送结果。</p>';
 if(typeof loadAutomation==='function')await loadAutomation();
 if(typeof renderSettingsSummary==='function')renderSettingsSummary();
 if(typeof loadConnectionSettings==='function')await loadConnectionSettings();
 if($('settings-overview-error'))$('settings-overview-error').hidden=true;
 }catch(e){if($('settings-overview-error')){$('settings-overview-error').textContent=e.message+'。保留上次结果，可点击刷新状态重试。';$('settings-overview-error').hidden=false}throw e}
}
$('integration-form').addEventListener('submit',e=>{e.preventDefault();action(e.submitter,async()=>{
 try{
 const body={enabled:$('integration-enabled').checked,server_type:$('integration-type').value,base:$('integration-base').value.trim(),api_key:$('integration-key').value.trim(),local_prefix:$('integration-local').value.trim(),remote_prefix:$('integration-remote').value.trim(),refresh_all:$('integration-all').checked,webhook_enabled:$('webhook-enabled').checked,webhook:$('integration-webhook').value.trim(),webhook_secret:$('integration-webhook-secret').value.trim()};
 const section=typeof settingsCategory!=='undefined'&&settingsCategory==='telegram'?'webhook':'library';
 await api('/api/integrations/'+section,body);$('integration-key').value='';$('integration-webhook').value='';$('integration-webhook-secret').value='';$('integration-result').textContent=section==='webhook'?'通知连接已保存，其他设置保留。':'媒体库设置已保存，通知连接保留。';await loadIntegrations();toast('当前分类设置已保存');
 $('integration-result').className='good';
 }catch(e){$('integration-result').textContent=e.message+'。输入已保留，请修改后重新保存。';$('integration-result').className='error';throw e}
})});
function integrationTest(button,kind,message){
 let result=$(button.id+'-result');
 if(!result){result=document.createElement('p');result.id=button.id+'-result';result.setAttribute('role','status');const anchor=button.parentElement.classList.contains('actions')?button.parentElement:button;anchor.after(result)}
 result.className='hint';result.textContent=kind==='library'?'正在测试媒体库连接…':'正在发送测试通知…';
 return action(button,async()=>{
  try{await api('/api/integrations/test',{kind});result.className='good';result.textContent=message}
  catch(e){result.className='error';result.textContent=e.message+'。请检查已保存配置后重试。';throw e}
 });
}
$('integration-test').addEventListener('click',e=>integrationTest(e.currentTarget,'library','媒体库连接测试通过'));
$('webhook-test').addEventListener('click',e=>integrationTest(e.currentTarget,'webhook','测试通知已投递到接收服务，请查看 Telegram'));

$('inventory-filters').insertAdjacentHTML('afterend','<button id="inventory-context" class="text" hidden></button>');$('inventory-context').addEventListener('click',()=>{inventoryMedia='';loadInventory()});
