/* The approved Butter workspace, backed by the existing console APIs. */
'use strict';
(() => {
 const root=document.documentElement;
 const header=document.querySelector('.shell>header'),sidebar=document.querySelector('.sidebar');
 header.classList.add('butter-header');
 document.querySelector('.shell').classList.add('butter-shell');
 header.querySelector('.header-brand').after(sidebar);
 const compact=matchMedia('(max-width:760px)');
 const placeNavigation=()=>{if(compact.matches)$('app').prepend(sidebar);else header.querySelector('.header-brand').after(sidebar)};
 if(compact.addEventListener)compact.addEventListener('change',placeNavigation);else compact.addListener(placeNavigation);
 placeNavigation();
 const settings=sidebar.querySelector('[data-nav="settings"]');
 sidebar.querySelector('nav').append(settings);
 const more=sidebar.querySelector('[data-nav="more"]');
 more.classList.add('desktop-more');
 const actions=document.createElement('div');actions.className='butter-header-actions';
 actions.innerHTML='<button type="button" class="skin-trigger" data-skin-toggle aria-label="皮肤管理" aria-expanded="false" aria-controls="skin-panel"><i data-lucide="palette" aria-hidden="true"></i><span>皮肤</span></button>';
 actions.append(header.querySelector('.notification-button'));
 const logoutBtn=sidebar.querySelector('#logout');
 if(logoutBtn){logoutBtn.classList.add('header-logout');logoutBtn.setAttribute('aria-label','退出登录');logoutBtn.title='退出登录';actions.append(logoutBtn);}
 header.append(actions);
 $('view-dashboard').querySelector('.heading').classList.add('butter-dashboard-heading');
 $('view-dashboard').querySelector('h1').insertAdjacentHTML('afterend','<p class="butter-greeting">从云端目录，到每一块屏幕。</p>');
 $('view-dashboard').querySelector('.heading>div>p:not(.butter-greeting)').hidden=true;
 // A real setup action: no synthetic scan, pause, status or completion is introduced.
 const I=name=>'<i data-lucide="'+name+'" aria-hidden="true"></i>';
 const N=value=>Number(value||0).toLocaleString('zh-CN');
 const cardHead=(title,route,label='查看'+title)=>'<div class="bw-card-head"><h2>'+title+'</h2><button class="bw-arrow" data-nav="'+route+'" aria-label="'+label+'">'+I('arrow-up-right')+'</button></div>';
 const card=(title,route,body,cls='')=>'<section class="bw-card '+cls+'">'+cardHead(title,route)+body+'</section>';
 const empty=text=>'<p class="bw-empty">'+text+'</p>';
 const badge=(text,tone='')=>'<span class="bw-badge '+tone+'">'+esc(text)+'</span>';
 function elapsed(j){
  if(!j?.started)return '—';
  const seconds=Math.max(0,Math.floor(Date.now()/1000-j.started));
  return seconds>=3600?Math.floor(seconds/3600)+':'+String(Math.floor(seconds/60)%60).padStart(2,'0')+':'+String(seconds%60).padStart(2,'0'):String(Math.floor(seconds/60)).padStart(2,'0')+':'+String(seconds%60).padStart(2,'0');
 }
 function updateTaskClock(){
  if(document.hidden||$('app').hidden||$('view-dashboard').hidden)return;
  const clock=$('bw-task-elapsed');if(!clock)return;
  const value=elapsed({started:Number(clock.dataset.started)||0});
  if(clock.textContent!==value)clock.textContent=value;
 }
 // One page-owned timer updates only the clock, without rebuilding dashboard controls.
 setInterval(updateTaskClock,1000);
 document.addEventListener('visibilitychange',updateTaskClock);
 renderDashboard=function(){
  const d=dashData;if(!d)return;
  $('dash-updated').textContent='更新于 '+msDate(d.time);
  updateTaskClock();
  const key=JSON.stringify([d.overview,d.stats,d.accounts,d.reports,d.plans,d.jobs,d.recent,d.errors,d.trend,d.write_error,dashChart,dashDays,state.failures,integrationData]);
  if(key===dashKey)return;dashKey=key;
  const s=d.stats,o=d.overview,enabled=d.accounts.filter(a=>!a.disabled),active=d.jobs.filter(genActive);
  const bad=enabled.filter(a=>d.reports[a.id]?.checked&&!d.reports[a.id].reachable);
  const checked=enabled.filter(a=>d.reports[a.id]?.checked&&d.reports[a.id].reachable);
  const completed=d.jobs.filter(j=>j.status==='completed'&&!j.dry_run).length;
  const failures=state.failures||d.jobs.filter(j=>genFailed(j)&&!j.retried_by);
  const issues=[...failures.map(j=>({icon:'folder-x',title:genRuleName(j.rule),detail:j.message||'生成未完成',route:'tasks'})),...bad.map(a=>({icon:'cloud-off',title:a.name,detail:d.reports[a.id].error||'最近连接检测失败',route:'accounts'}))];
  if(d.write_error)issues.unshift({icon:'database',title:'运行记录写入异常',detail:d.write_error,route:'records'});
  const strip=[['最近完成',completed,'records','strong'],['运行中',o.running,'tasks','accent'],['等待执行',o.queued,'tasks','soft'],['待处理任务',o.failed,'tasks','outline']];
  let html='<div class="bw-overview"><div class="bw-status-strip">'+strip.map(([label,value,route,tone])=>'<div><span>'+label+'</span><button data-nav="'+route+'" class="bw-segment '+tone+'">'+N(value)+'</button></div>').join('')+'</div><div class="bw-summary"><button data-nav="inventory"><span>已生成入口</span><strong>'+N(o.outputs)+'</strong></button><button data-dashboard-record="generation" data-created-today="true"><span>今日新增</span><strong>'+N(s.today_created)+'</strong></button><button data-dashboard-record="playback"><span>请求 / 24h</span><strong>'+N(s.requests)+'</strong></button></div></div>';
  if(!d.accounts.length||!state.rules.length||!state.settings.public_base){
   const target=!d.accounts.length?'accounts':!state.rules.length?'config':'settings',label=!d.accounts.length?'连接第一个网盘':!state.rules.length?'添加目录规则':'设置播放入口';
   html+='<div class="generation-banner bw-setup">'+I('workflow')+'<div><strong>搭建你的媒体入口</strong><p>连接网盘 → 选择目录 → 预览映射 → 首次生成</p></div><button data-nav="'+target+'">'+label+'</button></div>';
  }
  const clouds='<div class="bw-cloud-total">'+I('cloud')+'<strong>'+N(checked.length)+'</strong><span>个最近检测可用<br><small>'+enabled.length+' 个启用账号 · '+(enabled.length-checked.length-bad.length)+' 个未检测</small></span></div><div class="bw-cloud-list">'+(d.accounts.length?d.accounts.map(a=>{
   const report=d.reports[a.id],text=a.disabled?'已停用':!report?.checked?'未检测':report.reachable?'最近检测可用':'最近检测失败';
   return '<button data-nav="accounts"><span class="bw-provider">'+I('cloud')+'</span><span><strong>'+esc(a.name)+'</strong><small>'+esc(names[a.provider]||a.provider)+'</small></span><span class="bw-cloud-state '+(report?.reachable&&!a.disabled?'good':'')+'">'+text+'</span></button>';
  }).join(''):empty('连接移动云盘、夸克、阿里或 115，开始管理媒体。'))+'</div><button class="text bw-link" data-nav="accounts">'+I('plus')+'管理网盘账号</button>';
  const values=d.trend.map(v=>Number(v[dashChart])||0),max=Math.max(1,...values),total=d.trend.filter(v=>v.covered).reduce((a,v)=>a+(Number(v[dashChart])||0),0);
  const bars=d.trend.map((v,index)=>'<button class="bw-bar '+(index===d.trend.length-1?'today':'')+'" data-trend-day="'+v.day+'" aria-label="'+v.day+' '+(v.covered?N(v[dashChart])+(dashChart==='created'?' 个新增入口':' 次请求'):'未采集')+'"><span class="bw-bar-track"><i data-bar-height="'+(v.covered?Math.max(3,values[index]/max*100):3)+'"></i></span><small>'+v.day.slice(5)+'</small></button>').join('');
  const trend='<div class="bw-trend-value"><strong>'+N(total)+'</strong><span>近 '+dashDays+' 天'+(dashChart==='created'?'新增 STRM':'入口请求')+'</span></div><div class="bw-chart '+(dashDays===30?'bw-chart-month':'')+'" role="group" aria-label="运行趋势">'+bars+'</div><div class="bw-trend-controls"><label class="sr-only" for="dash-days">趋势时间范围</label><select id="dash-days" aria-label="趋势时间范围"><option value="7" '+(dashDays===7?'selected':'')+'>近 7 天</option><option value="30" '+(dashDays===30?'selected':'')+'>近 30 天</option></select><button data-chart="'+(dashChart==='created'?'requests':'created')+'">'+(dashChart==='created'?'查看入口请求':'查看新增 STRM')+'</button></div><p class="hint">自 '+esc(new Date(d.metrics_since).toLocaleDateString('zh-CN'))+' 采集；较早日期无完整数据。</p>';
  const job=active.find(j=>j.status==='running')||active[0];
  const taskStarted=job?.status==='running'?Number(job.started)||0:0;
  const task=job?'<div class="bw-task-clock"><strong id="bw-task-elapsed" data-started="'+taskStarted+'">'+elapsed({started:taskStarted})+'</strong><span>'+(job.status==='queued'?'等待开始':'本次已运行')+'</span></div><strong class="bw-task-name">'+esc(genRuleName(job.rule))+'</strong><p class="hint">'+esc(statusNames[job.status]||job.status)+' · 已发现 '+N(job.seen)+' 个</p><p class="bw-task-path" title="'+esc(job.current_path||job.source_path)+'">'+esc(job.current_path||job.source_path||'等待扫描')+'</p><p class="hint">'+esc(genResult(job))+'</p><button data-nav="tasks">查看任务详情 '+I('arrow-right')+'</button>':'<div class="bw-idle">'+I('circle-check')+'<strong>当前没有任务</strong><p>手动生成或等待定时计划执行。</p></div><button data-nav="home">前往生成 '+I('arrow-right')+'</button>';
  const issueBody='<p class="bw-issue-summary">'+(issues.length?'根据已记录的失败与最近检测结果。':'暂未发现待处理事项。')+'</p><div class="bw-issue-list">'+(issues.length?issues.slice(0,3).map(v=>'<button data-nav="'+v.route+'"><span>'+I(v.icon)+'</span><span><strong>'+esc(v.title)+'</strong><small title="'+esc(v.detail)+'">'+esc(v.detail)+'</small></span>'+I('chevron-right')+'</button>').join(''):'<div class="bw-issue-clear">'+I('check')+'<p>任务和连接的异常<br>会集中显示在这里。</p></div>')+'</div><button data-nav="notifications" class="bw-issue-action">查看通知 '+I('arrow-right')+'</button><p class="bw-issue-foot">'+N(o.failed)+' 个失败任务 · '+N(bad.length)+' 个网盘检测异常</p>';
  const recentJobs=d.jobs.slice(0,4),recent=recentJobs.length?'<div class="bw-recent-head"><span>目录规则</span><span>结果</span><span>时间</span></div>'+recentJobs.map(j=>'<button class="bw-job-row" data-nav="tasks"><span class="bw-provider">'+I('folder-sync')+'</span><span><strong>'+esc(genRuleName(j.rule))+'</strong><small>'+esc(j.source_path||'')+'</small></span>'+badge(statusNames[j.status]||j.status,jobClass(j))+'<time>'+esc(date(j.created))+'</time></button>').join(''):empty('还没有执行记录。配置目录规则后，先预览再生成。');
  const config=integrationData?.config;
  const health='<div class="bw-health-row"><span>'+I('server')+'YoLink 服务</span>'+badge('已连接','good')+'</div><div class="bw-health-row"><span>'+I('monitor-play')+'媒体库联动</span><button class="text" data-nav="settings">'+(config?config.enabled?'已启用':'未启用':'查看配置')+I('chevron-right')+'</button></div><div class="bw-health-row"><span>'+I('bell-ring')+'通知连接</span><button class="text" data-nav="settings">管理连接 '+I('chevron-right')+'</button></div><div class="bw-health-row"><span>'+I('link')+'播放入口</span>'+badge(state.settings.public_base?'已设置':'未设置')+'</div><p class="hint">最近读取 '+msDate(d.time)+'</p>';
  html+='<div class="bw-grid">'+card('我的云端','accounts',clouds,'bw-cloud')+card('生成趋势','records',trend,'bw-trend')+card('当前任务','tasks',task,'bw-task')+card('待处理事项','notifications',issueBody,'bw-issues')+card('最近执行','tasks',recent,'bw-recent')+card('连接与服务','settings',health,'bw-health')+'</div>';
  const errors=d.errors.length?'<ul class="bw-resolve-errors">'+d.errors.slice(0,5).map(v=>'<li><div><strong>'+esc(v.name||'解析失败')+'</strong><p class="error">'+esc(v.error)+'</p><small>'+msDate(v.time)+'</small></div><button class="text" data-dashboard-record="direct">查看记录</button></li>').join('')+'</ul>':'<p class="hint">暂无已记录的解析错误。</p>';
  const detailOpen=$('bw-direct-detail')?.open;
  const detail='<details id="bw-direct-detail" class="bw-direct-detail" '+(detailOpen?'open':'')+'><summary id="bw-direct-detail-summary">解析耗时与最近错误 '+I('chevron-down')+'</summary><dl class="bw-direct-stats"><div><dt>上游请求平均耗时</dt><dd>'+(s.upstream_count?Math.round(s.upstream_ms/s.upstream_count)+' ms':'—')+'</dd></div><div><dt>非缓存上游请求</dt><dd>'+N(s.upstream_count)+' 次</dd></div></dl><h3>最近解析错误</h3>'+errors+'<p class="hint">这里保留最近记录的失败，不代表当前仍未恢复。</p></details>';
  const stats='<dl class="bw-direct-stats"><div><dt>解析成功率</dt><dd>'+(s.resolve_total?(s.resolved/s.resolve_total*100).toFixed(1)+'%':'—')+'</dd></div><div><dt>平均解析耗时</dt><dd>'+(s.resolve_total?Math.round(s.resolve_ms/s.resolve_total)+' ms':'—')+'</dd></div><div><dt>缓存 / 并发复用</dt><dd>'+N(s.cache)+' 次</dd></div><div><dt>解析失败</dt><dd>'+N(s.resolve_total-s.resolved)+' 次</dd></div></dl><p class="hint">最近 24 小时；302 表示跳转已发送，实际播放效果需在播放器验证。</p>'+detail;
  const plans=d.plans.length?d.plans.slice().sort((a,b)=>(a.next||Infinity)-(b.next||Infinity)).slice(0,4).map(p=>{
   const busy=active.some(j=>j.rule===p.id),disabled=accountBy(p.account)?.disabled;
   return '<div class="bw-plan-row"><div><strong>'+esc(p.name)+'</strong><small>'+esc(disabled?'网盘已停用':busy?'运行或排队中':p.enabled?'预计 '+msDate(Math.max(Date.now(),p.next*1000))+' 检查':'仅手动执行')+'</small></div><button data-gen-generate="'+esc(p.id)+'" '+(disabled||busy?'disabled':'')+'>运行</button></div>';
  }).join(''):empty('添加规则后，这里显示执行计划。');
  const outputs=d.recent.length?d.recent.slice(0,5).map(v=>'<button class="bw-output-row" data-inventory-media="'+esc(v.media)+'"><span><strong>'+esc(v.name)+'</strong><small title="'+esc(v.path)+'">'+esc(v.path)+'</small></span><time>'+msDate(v.created)+'</time>'+I('chevron-right')+'</button>').join(''):empty('实际新增的 STRM 将显示在这里。');
  html+='<div class="bw-detail-grid">'+card('302 解析情况','playback',stats)+card('执行计划','config',plans)+card('最近生成文件','inventory',outputs)+'</div>';
  const focus=document.activeElement?.id;$('dash-content').innerHTML=html;$('dash-content').setAttribute('aria-busy','false');$('dash-content').querySelectorAll('[data-bar-height]').forEach(bar=>bar.style.height=bar.dataset.barHeight+'%');if(focus&&$(focus))$(focus).focus({preventScroll:true});icons();
 };
 // Keep the existing parent navigation selection and all routed destinations.
 root.classList.remove('nav-compact');
 if(dashData){dashKey='';renderDashboard();}
 icons();
})();
