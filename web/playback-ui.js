'use strict';
let toolHistoryKey='',ingestHistoryKey='',migrationFiles=[],migrationSerial=0,toolPlans={links:null,migration:null,inplace:null},restoredPlans=false,inplaceScan=null,migrationBusy=false,migrationStop=false;
function renderPlaybackTools(){
 const playback=state.playback||{},stats=playback.cache||{};
 $('playback-cache-stats').innerHTML=`<span><strong>${stats.entries||0}</strong>有效直链</span><span><strong>${stats.hits||0}</strong>复用 / 合并</span><span><strong>${stats.misses||0}</strong>重新解析</span>`;
 const records=playback.history||[],key=JSON.stringify(records);
 if(key!==toolHistoryKey){toolHistoryKey=key;$('playback-history').innerHTML=records.length?records.map(r=>`<tr><td><strong>${esc(r.name)}</strong><small>${r.action==='check'?'取流自检':r.action==='head'?'HEAD 预检':'播放入口请求'} · ${r.mode==='302'?'302 直连':r.mode==='proxy'?'服务器中转':'待确认'}</small></td><td><span class="${r.error?'error':r.action==='check'&&r.readable?'good':'muted'}">${r.error?esc(r.error):r.action==='check'?(r.readable?'可读取'+(r.range?' · Range 正常':' · Range 未确认'):'未取得媒体'):r.status===302?'已返回 302':r.action==='head'&&r.status===200?'媒体信息可读取':'已解析中转地址'}</span></td><td>${r.cache?'复用':'新解析'}</td><td>${r.millis} ms</td><td>${date(r.time)}</td></tr>`).join(''):'<tr><td colspan="5" class="empty">播放或运行取流自检后，这里会记录结果。</td></tr>'}
 const events=state.ingest||[],eventKey=JSON.stringify(events);
 if(eventKey!==ingestHistoryKey){ingestHistoryKey=eventKey;$('ingest-section').hidden=!events.length;$('ingest-summary').textContent='最近入库触发 · '+events.length+' 条';$('ingest-list').innerHTML=events.map(e=>`<div class="ingest-row"><div><strong>${esc(ruleBy(e.rule)?.name||'已移除规则')}</strong><p>${esc(e.path)}</p><small>${e.error?esc(e.error):e.status==='pending'?'等待规则空闲后处理':e.status==='submitted'?'已提交生成任务':esc(statusNames[e.status]||e.status)}</small></div>${e.status==='failed'&&!e.job?`<button data-ingest-retry="${esc(e.id)}">重试目录读取</button>`:`<span class="hint">${date(e.created)}</span>`}</div>`).join('')}
 genSetOptions($('ingest-rule'),state.rules.filter(r=>!accountBy(r.account)?.disabled).map(r=>({id:r.id,label:r.name})), $('ingest-rule').value);
 if(!restoredPlans){restoredPlans=true;for(const mode of ['links','migration'])restoreToolPlan(mode)}
}
function showProbeResult(r){
 const area=$('probe-result');area.hidden=false;
 area.innerHTML=`<h2>${esc(r.name)} · 取流自检</h2><div class="probe-grid"><div><span>取流结果</span><strong class="${r.error?'error':'good'}">${r.readable?'可读取媒体':'未确认可用'}</strong></div><div><span>Range 响应</span><strong>${r.range?'206 · 已确认':r.status?'HTTP '+r.status:'未确认'}</strong></div><div><span>HEAD 响应</span><strong>${r.head_status?'HTTP '+r.head_status:'未取得'}</strong></div><div><span>解析与检查</span><strong>${r.millis} ms · ${r.cache?'缓存复用':'重新解析'}</strong></div></div><p class="${r.error?'error':''}">${esc(r.error||r.note)}${r.bytes?' · 已读取 '+r.bytes+' 字节':''}</p>`;
 area.scrollIntoView({block:'start',behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'});
}
function showToolPlan(mode,p){
 toolPlans[mode]=p;try{localStorage.setItem('strmlink-plan-'+mode,p.id)}catch{}
 if(mode==='inplace'){
  const area=$('migration-plan');if($('migration-mode').value!=='inplace')return;area.hidden=false;
  const pending=p.pending||0,retry=p.ready-p.applied-pending;
  area.innerHTML=`<h3>原位迁移：已更新 ${p.applied} 个 · 待核对 ${pending} 个 · 跳过 / 失败 ${p.skipped} 个</h3><p>目标目录：<code>${esc(p.directory)}</code></p><p>预览已生成新入口；覆盖时会核对云端文件。先覆盖 1 个，在 Emby 试播通过后继续。每批最多 20 个，可中途停止并继续。</p><details class="migration-diffs"><summary>查看新旧地址与路径示例</summary>${p.samples.map(v=>`<div class="migration-diff"><strong>${esc(v.path)}</strong>${v.error?`<p class="error">${esc(v.error)}</p>`:v.applied?'<p class="good">已更新</p>':'<p>等待云端核对</p>'}<dl><dt>原地址</dt><dd><code>${esc(v.before||'不可读取')}</code></dd><dt>新地址</dt><dd><code>${esc(v.after||'此项保留原内容')}</code></dd>${v.target?`<dt>网盘路径</dt><dd><code>${esc(v.target.path)}</code></dd>`:''}</dl></div>`).join('')}</details><p>备份位于 YoLink 数据目录的 strm-backups/${esc(p.id)}/。清单含完整原地址和新地址，请妥善保存。</p><label class="checkbox"><input id="migration-write-confirm" type="checkbox">我已备份旧目录，并已暂停原项目向该目录自动写入</label><div class="actions"><button data-inplace-run="one" ${!pending?'disabled':''}>先覆盖 1 个</button><button class="primary" data-inplace-run="all" ${!pending?'disabled':''}>${p.applied?'继续分批覆盖':'分批覆盖全部'}</button>${retry>0?'<button data-inplace-run="retry">重试失败项</button>':''}<button data-inplace-stop hidden>停止后续批次</button>${p.applied?'<button data-plan-rollback="inplace">从本次备份恢复</button>':''}<button data-plan-report="inplace">下载完整清单</button></div><p id="migration-progress" role="status" aria-live="polite"></p>`;
  setMigrationBusy(migrationBusy);icons();return;
 }
 const area=$(mode==='links'?'links-plan':'migration-plan');area.hidden=false;
 area.innerHTML=`<h3>预览：${p.ready} 个可处理 · ${p.skipped} 个跳过</h3><p>${mode==='links'?'仅更新本项目托管且内容未被外部修改的 STRM。应用时先保存备份，再更新入口。':'只生成独立的新副本。首次播放会按旧链接中的云端路径查找文件；映射正确仍需实际试播。'}</p><ul>${p.samples.map(v=>`<li>${esc(v.path)}${v.error?' — '+esc(v.error):v.applied?' · 已处理':''}</li>`).join('')}</ul><p>已处理 ${p.applied} / ${p.ready} 个。${p.total>p.samples.length?'这里只展示部分路径，可下载完整清单。':''}</p><div class="actions"><button class="primary" data-plan-apply="${mode}" ${p.ready===0||p.applied===p.ready?'disabled':''}>${mode==='links'?'备份并更新':'生成迁移副本'}</button>${mode==='links'&&p.applied?'<button data-plan-rollback="links">从备份恢复</button>':''}<button data-plan-report="${mode}">下载完整清单</button></div>`;icons();
}
function restoreToolPlan(mode){let id='';try{id=localStorage.getItem('strmlink-plan-'+mode)||''}catch{}if(id)api('/api/maintenance/'+id+'/report').then(p=>{if(!toolPlans[mode])showToolPlan(mode,p)}).catch(()=>{})}
function setMigrationBusy(busy){migrationBusy=busy;for(const id of ['migration-mode','migration-directory','migration-pick-directory','migration-scan','migration-preview','migration-files']){$(id).disabled=busy||(id==='migration-preview'&&($('migration-mode').value==='inplace'?!inplaceScan:!migrationFiles.length))}document.querySelectorAll('[data-inplace-run],[data-plan-rollback="inplace"]').forEach(b=>{b.disabled=busy||b.dataset.inplaceRun!=='retry'&&b.dataset.inplaceRun!==undefined&&!toolPlans.inplace?.pending});const stop=document.querySelector('[data-inplace-stop]');if(stop)stop.hidden=!busy;const check=$('migration-write-confirm');if(check)check.disabled=busy}
function invalidateMigration(){if(migrationBusy)return;$('migration-plan').hidden=true;toolPlans[$('migration-mode').value==='inplace'?'inplace':'migration']=null}
function renderMigrationMappings(storage,inplace=false){
 $('migration-mappings').innerHTML=Object.entries(storage).map(([name,count],index)=>{
  const provider=/^yun139/i.test(name)?'139':/^quark/i.test(name)?'quark':/^ali/i.test(name)?'ali':'';
  const matching=state.accounts.filter(a=>!a.disabled&&(provider==='139'?a.provider.startsWith('139'):provider==='quark'?a.provider.startsWith('quark'):provider&&a.provider===provider)),available=inplace?matching:state.accounts.filter(a=>!a.disabled);
  return `<div class="migration-mapping"><label>${esc(name||'来源未标明')} · ${count} 个<select data-migration-storage="${esc(name)}"><option value="">跳过此来源</option>${available.map(a=>`<option value="${esc(a.id)}" ${matching.length===1&&matching[0].id===a.id?'selected':''}>${esc(a.name)}</option>`).join('')}</select></label>${inplace?`${!provider?'<p class="hint">此来源暂不支持，原 STRM 保留。</p>':!matching.length?'<p class="hint">先到网盘管理添加同类型账号，再重新扫描。</p>':''}<details><summary>云端路径不同？设置前缀映射</summary><p class="hint">路径指网盘目录。相同时留空；例如旧 /media/电影 对应网盘 /电影，可填 /media → /。</p><div class="form-grid"><label>旧链接路径前缀<input data-prefix-from="${index}" placeholder="例如 /media"></label><label>实际网盘路径前缀<input data-prefix-to="${index}" placeholder="例如 /"></label></div></details>`:''}</div>`
 }).join('');
}
async function runInplace(kind){
 const p=toolPlans.inplace;if(!p||migrationBusy)return;
 if(!$('migration-write-confirm').checked)throw new Error('请先确认已备份旧目录，并暂停原项目向此目录自动写入');
 migrationStop=false;setMigrationBusy(true);
 try{
  let retry=kind==='retry';
  do{const before=toolPlans.inplace;const result=await api('/api/maintenance/'+p.id+'/apply',{confirm_directory:p.directory,limit:kind==='one'?1:20,retry});retry=false;showToolPlan('inplace',result);$('migration-progress').textContent=`已更新 ${result.applied} 个，剩余 ${result.pending} 个，跳过 / 失败 ${result.skipped} 个。`;
   if(kind==='one'||!result.pending||result.applied===before.applied&&result.pending===before.pending)break;
   await new Promise(resolve=>setTimeout(resolve,100));
  }while(!migrationStop);
  toast(migrationStop?'已停止后续批次，已更新文件保留，可继续或恢复':'本次处理已结束，请查看结果和清单');await refresh();
 }catch(e){try{showToolPlan('inplace',await api('/api/maintenance/'+p.id+'/report'))}catch{}throw e}finally{setMigrationBusy(false)}
}
async function readMigrationFiles(list){
 const serial=++migrationSerial;migrationFiles=[];$('migration-mappings').innerHTML='';$('migration-preview').disabled=true;$('migration-plan').hidden=true;
 const files=[...list].filter(f=>/\.strm$/i.test(f.name));if(files.length>50000)throw new Error('每次最多选择 50000 个 STRM');const storage=new Map();let skipped=0;
 for(let i=0;i<files.length;i++){if(serial!==migrationSerial)return;const f=files[i];if(f.size>65536){skipped++;continue};const content=await f.text();if(serial!==migrationSerial)return;migrationFiles.push({path:f.webkitRelativePath||f.name,content});try{const u=new URL(content.trim().replace(/^\uFEFF/,''));if(u.pathname==='/redirect'){const key=u.searchParams.get('storage')||'';storage.set(key,(storage.get(key)||0)+1)}}catch{}if(i%200===0)$('migration-selection').textContent=`已读取 ${i+1} / ${files.length} 个 STRM…`}
 $('migration-selection').textContent=`已读取 ${migrationFiles.length} 个 STRM${skipped?'；'+skipped+' 个超过 64KB，已跳过':''}。选择来源网盘后预览。`;
 renderMigrationMappings(Object.fromEntries(storage));$('migration-preview').disabled=!migrationFiles.length;
}
document.addEventListener('click',event=>{
 const b=event.target.closest('button');if(!b)return;
 if(b.dataset.playlistMedia||b.dataset.playlistRule)action(b,async()=>{const kind=b.dataset.playlistMedia?'media':'rules',id=b.dataset.playlistMedia||b.dataset.playlistRule;await downloadPlaylist('/api/'+kind+'/'+encodeURIComponent(id)+'/playlist');});
 if(b.dataset.probe)action(b,async()=>{const result=await api('/api/playback/'+b.dataset.probe+'/check',{});showProbeResult(result);await refresh()});
 if(b.dataset.ingestRetry)action(b,async()=>{await api('/api/ingest/'+b.dataset.ingestRetry+'/retry',{});await refresh();toast('已重新排队读取目录')});
 if(b.dataset.inplaceRun)action(b,()=>runInplace(b.dataset.inplaceRun));
 if(b.hasAttribute('data-inplace-stop')){migrationStop=true;b.disabled=true;b.textContent='当前批次完成后停止'}
 if(b.dataset.planApply)action(b,async()=>{const mode=b.dataset.planApply,p=toolPlans[mode];if(!confirm(mode==='links'?`备份并更新预览中的 ${p.ready-p.applied} 个 STRM？外部修改的文件会保留。`:`在独立目录生成 ${p.ready-p.applied} 个迁移副本？旧文件夹会保留。`))return;const result=await api('/api/maintenance/'+p.id+'/apply',{});showToolPlan(mode,result);await refresh();toast('处理完成：'+result.applied+' 个，跳过 '+result.skipped+' 个')});
 if(b.dataset.planRollback)action(b,async()=>{const mode=b.dataset.planRollback,p=toolPlans[mode];if(migrationBusy)return;if(mode!=='inplace'){if(!confirm('从此次备份恢复已更新的 STRM？后续被修改的文件会跳过。'))return;showToolPlan(mode,await api('/api/maintenance/'+p.id+'/rollback',{}));return}if(!$('migration-write-confirm').checked)throw new Error('请先确认已备份，并暂停原项目自动写入；恢复将使用此次迁移的原内容');migrationStop=false;setMigrationBusy(true);try{let result=p;do{const before=result.applied;result=await api('/api/maintenance/'+p.id+'/rollback',{});showToolPlan(mode,result);if(result.applied>=before)break}while(result.applied&&!migrationStop);toast('恢复操作完成，请查看清单');await refresh()}finally{setMigrationBusy(false)}});
 if(b.dataset.planReport)action(b,async()=>{const p=toolPlans[b.dataset.planReport],r=await api('/api/maintenance/'+p.id+'/report');const blob=new Blob([JSON.stringify(r,null,2)],{type:'application/json'}),url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download='YoLink-'+p.mode+'-'+p.id.slice(0,8)+'.json';a.click();URL.revokeObjectURL(url)});
});
document.addEventListener('DOMContentLoaded',()=>{
 $('nav-collapse').addEventListener('click',()=>{const compact=document.documentElement.classList.toggle('nav-compact');$('nav-collapse').setAttribute('aria-expanded',String(!compact));$('nav-collapse').setAttribute('aria-label',compact?'展开侧栏':'折叠侧栏')});
 $('playback-refresh').addEventListener('click',e=>action(e.currentTarget,refresh));$('playback-clear-cache').addEventListener('click',e=>action(e.currentTarget,async()=>{await api('/api/playback/cache/clear',{});await refresh();toast('直链缓存已清空，下一次播放重新解析')}));
 $('ingest-rule').addEventListener('change',()=>{const r=ruleBy($('ingest-rule').value);$('ingest-path').placeholder=(r?.source_path||'/media')+'/某部电影/电影.mkv'});
 $('ingest-copy-body').addEventListener('click',e=>action(e.currentTarget,async()=>{const rule=$('ingest-rule').value,path=$('ingest-path').value.trim();if(!rule||!path.startsWith('/'))throw new Error('先选择规则并填写完整云端文件路径');await copy(JSON.stringify({rule,path,kind:'file'},null,2));toast('入库请求体已复制')}));
 $('links-preview').addEventListener('click',e=>action(e.currentTarget,async()=>showToolPlan('links',await api('/api/maintenance/links',{}))));
 $('migration-mode').addEventListener('change',()=>{migrationSerial++;const inplace=$('migration-mode').value==='inplace';$('migration-upload').hidden=inplace;$('migration-server').hidden=!inplace;$('migration-plan').hidden=true;$('migration-mappings').innerHTML='';inplaceScan=null;migrationFiles=[];$('migration-files').value='';$('migration-preview').disabled=true;$('migration-selection').textContent=inplace?'选择 NAS 上的原目录并扫描，再映射来源网盘。':'选择文件夹后，只读取其中的 STRM 内容。';const mode=inplace?'inplace':'migration';if(toolPlans[mode])showToolPlan(mode,toolPlans[mode]);else restoreToolPlan(mode)});
 $('migration-directory').addEventListener('input',()=>{inplaceScan=null;$('migration-preview').disabled=true;$('migration-mappings').innerHTML='';invalidateMigration()});
 $('migration-pick-directory').addEventListener('click',e=>action(e.currentTarget,()=>openOutputFolder('migration-directory','migration-pick-directory','选择 NAS 上的旧 STRM 目录')));
 $('migration-mappings').addEventListener('input',invalidateMigration);
 $('migration-scan').addEventListener('click',e=>action(e.currentTarget,async()=>{invalidateMigration();inplaceScan=null;setMigrationBusy(true);try{const p=await api('/api/maintenance/migration/scan',{directory:$('migration-directory').value.trim()});inplaceScan=p;$('migration-selection').textContent=`扫描到 ${p.total} 个 STRM，其中 ${p.skipped} 个格式异常或不可读取。映射来源账号后生成预览。`;renderMigrationMappings(p.storage,true)}finally{setMigrationBusy(false)}}));
 $('migration-files').addEventListener('change',e=>{readMigrationFiles(e.target.files).catch(err=>{$('migration-selection').textContent=err.message;toast(err.message)})});
 $('migration-preview').addEventListener('click',e=>action(e.currentTarget,async()=>{const mapping={};document.querySelectorAll('[data-migration-storage]').forEach(el=>mapping[el.dataset.migrationStorage]=el.value);
 if($('migration-mode').value==='inplace'){if(!inplaceScan)throw new Error('先扫描 NAS 上的旧 STRM 目录');const prefixes={};document.querySelectorAll('[data-migration-storage]').forEach((el,i)=>{const from=document.querySelector(`[data-prefix-from="${i}"]`).value.trim(),to=document.querySelector(`[data-prefix-to="${i}"]`).value.trim();if(from||to)prefixes[el.dataset.migrationStorage]={from,to}});setMigrationBusy(true);try{showToolPlan('inplace',await api('/api/maintenance/migration/inplace',{plan_id:inplaceScan.id,mapping,prefixes}));$('migration-selection').textContent='预览完成。展开核对新旧地址，再先覆盖 1 个试播。'}finally{setMigrationBusy(false)}return}
 if(!migrationFiles.length)throw new Error('先选择旧 STRM 文件夹');let id='',last=null;
 for(let i=0;i<migrationFiles.length;){const batch=[];let bytes=0;while(i<migrationFiles.length&&batch.length<500){const file=migrationFiles[i],size=new TextEncoder().encode(JSON.stringify(file)).length;if(batch.length&&bytes+size>650000)break;batch.push(file);bytes+=size;i++}last=await api('/api/maintenance/migration',{plan_id:id,files:batch,mapping});id=last.id;$('migration-selection').textContent=`已预览 ${i} / ${migrationFiles.length} 个 STRM…`}
 if(last){showToolPlan('migration',last);$('migration-selection').textContent='预览完成。确认映射和范围后生成新副本。'}
 }));
});

async function downloadPlaylist(path){const response=await fetch(path,{credentials:'same-origin'});if(!response.ok){let message='播放列表下载失败';try{message=(await response.json()).error||message}catch{}throw new Error(message)}const blob=await response.blob(),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download='YoLink-播放列表.m3u';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),60000);toast('M3U 已下载，请用 PotPlayer 等播放器打开')}
