/* Account management and a read-only browser, using live API data. */
'use strict';
let cloudCardsKey='',fileAccount='',fileTrail=[],fileEntries=[],fileReady=false,fileLoading=false,fileError='',fileSerial=0,fileRequest=null,filePage=1,fileMode='list',fileSelected=null;
const cloudTitles={'139':'中国移动云盘','139old':'中国移动云盘',quark:'夸克网盘',quarktv:'夸克网盘',ali:'阿里云盘','115':'115 网盘'};
const cloudIcons={'139':'cloud','139old':'cloud',quark:'cloud',quarktv:'cloud',ali:'circle-dot','115':'cloud'};
const cloudCapabilities=a=>['文件浏览','STRM 生成',a.provider==='quark'?(a.allow_proxy?'服务器中转':'中转未开启'):a.provider==='115'&&a.allow_proxy?'服务器中转':'原文件 302'];
function fileSize(n){if(!Number.isFinite(Number(n))||Number(n)<0)return '—';n=Number(n);if(n===0)return '0 B';const u=['B','KB','MB','GB','TB','PB'],i=Math.min(Math.floor(Math.log(n)/Math.log(1024)),u.length-1);return (n/1024**i).toLocaleString('zh-CN',{maximumFractionDigits:i<2?0:2})+' '+u[i]}
function fileTime(value){if(!value)return '—';let v=value;if(/^\d+(\.\d+)?$/.test(String(v))){v=Number(v);if(v<1e12)v*=1000}const d=new Date(v);return Number.isNaN(d.getTime())?String(value):d.toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'})}
function cloudReport(id){return state.account_reports?.[id]||{}}
function renderCloudManagement(){
 const signature=JSON.stringify([state.accounts,state.account_reports,state.rules.map(r=>[r.id,r.account]),state.jobs.filter(genActive).map(j=>j.rule)]);
 if(signature!==cloudCardsKey){cloudCardsKey=signature;
 const open=new Set([...$('account-list').querySelectorAll('details[open]')].map(el=>el.dataset.cloudMore));
 $('account-list').innerHTML=state.accounts.length?state.accounts.map(a=>{
  const r=cloudReport(a.id),q=r.quota,count=state.rules.filter(x=>x.account===a.id).length,active=state.jobs.some(j=>genActive(j)&&ruleBy(j.rule)?.account===a.id);
  const label=a.disabled?'已停用':r.checked?(r.reachable?'连接正常':'连接异常'):'已配置 · 待测试';
  const tone=a.disabled?'muted':r.checked?(r.reachable?'good':'error'):'muted';
  const percent=q?Math.max(0,q.used/q.total*100):0;
  return `<article class="cloud-card ${a.disabled?'cloud-disabled':''}"><div class="cloud-card-head"><span class="cloud-provider provider-${esc(a.provider)}"><i data-lucide="${cloudIcons[a.provider]||'cloud'}"></i></span><div class="cloud-card-title"><h2>${esc(a.name)}</h2><p>${esc(cloudTitles[a.provider]||'网盘')} · ${esc(names[a.provider]?.split(' · ')[1]||'')}</p><span class="cloud-status ${tone}"><span aria-hidden="true"></span>${esc(label)}</span></div><label class="cloud-switch" title="停用后暂停此网盘的浏览、生成和播放"><input type="checkbox" role="switch" aria-label="启用 ${esc(a.name)}" data-cloud-enabled="${esc(a.id)}" ${a.disabled?'':'checked'} ${active?'disabled':''}><span aria-hidden="true"></span></label></div>
   <div class="cloud-capacity"><div><span>容量</span><strong>${q?`${fileSize(q.used)} / ${fileSize(q.total)}`:r.quota_error?'暂不可用':'待读取'}</strong></div>${q?`<progress max="100" value="${Math.min(100,percent)}" aria-label="${esc(a.name)} 容量使用 ${percent.toFixed(1)}%"></progress><p>${percent.toFixed(1)}% 已使用 · ${esc(fileTime(q.checked))} 更新</p>`:r.quota_error?'':'<div class="capacity-unavailable">点击刷新容量读取网盘返回的数据</div>'}${r.quota_error?`<p class="cloud-report-error">${esc(r.quota_error)}${q?'；上方为上次成功读取的数据':''}</p>`:''}</div>
   <div class="cloud-capabilities">${cloudCapabilities(a).map(t=>`<span>${esc(t)}</span>`).join('')}</div>
   <button class="text" data-account-health="${esc(a.id)}">健康与恢复</button>
   <div class="cloud-main-actions"><button data-cloud-browse="${esc(a.id)}" ${a.disabled?'disabled':''}><i data-lucide="folder-open"></i>浏览文件</button><button data-cloud-rule="${esc(a.id)}" ${a.disabled?'disabled':''}><i data-lucide="folder-sync"></i>生成配置${count?' · '+count:''}</button></div>
   <div class="cloud-card-footer"><button data-cloud-check="${esc(a.id)}" ${a.disabled?'disabled':''}><i data-lucide="plug-zap"></i>连接测试</button><button data-cloud-quota="${esc(a.id)}" ${a.disabled?'disabled':''}><i data-lucide="refresh-cw"></i>刷新容量</button><button data-edit="${esc(a.id)}"><i data-lucide="pencil"></i>编辑</button><details class="cloud-more" data-cloud-more="${esc(a.id)}" ${open.has(a.id)?'open':''}><summary aria-label="${esc(a.name)} 更多操作" title="更多操作"><i data-lucide="ellipsis"></i></summary><div><button data-cloud-remove="${esc(a.id)}" class="error">移除账号</button><p>关联生成规则或播放索引的账号可停用，不能直接移除。</p></div></details></div>
   ${r.checked?`<p class="cloud-last-check ${r.reachable?'':'error'}">${r.reachable?'根目录读取成功 · '+r.entries+' 项':esc(r.error||'连接失败')}<span>上次测试 ${esc(fileTime(r.checked))}</span></p>`:''}</article>`
 }).join(''):'<div class="panel empty cloud-empty"><i data-lucide="cloud"></i><h2>添加你的第一个网盘</h2><p>连接移动、夸克、阿里或 115，浏览文件并配置 STRM 生成。</p><button id="cloud-empty-add" class="primary">添加网盘</button></div>';
 }
 const enabled=state.accounts.filter(a=>!a.disabled);genSetOptions($('files-account'),enabled.map(a=>({id:a.id,label:a.name+' · '+cloudTitles[a.provider]})),fileAccount);
 if(fileAccount&&!enabled.some(a=>a.id===fileAccount)){fileSerial++;fileRequest?.abort();fileAccount='';fileTrail=[];fileEntries=[];fileReady=false;fileLoading=false;fileSelected=null;fileError='';renderCloudFiles()}
 $('cloud-count').textContent=state.accounts.length+' 个网盘 · '+enabled.length+' 个已启用';icons();
}
function openAccountForm(id=''){
 resetAccount();nav('accounts');$('account-form').hidden=false;
 if(id){const a=accountBy(id);if(!a)return;$('account-id').value=a.id;$('provider').value=a.provider;$('provider').disabled=true;$('account-name').value=a.name;$('account-root').value=a.root;$('account-drive').value=a.drive;$('account-drive').disabled=true;$('allow-proxy').checked=a.allow_proxy;$('account-form-title').textContent='编辑 '+a.name;providerFields()}
 $('account-form').scrollIntoView({block:'start',behavior:'smooth'});$('account-name').focus({preventScroll:true});
}
function filesCurrent(){return fileTrail.at(-1)}
async function openCloudFiles(id){
 const a=accountBy(id);if(!a||a.disabled){nav('accounts');toast('先添加并启用一个网盘');return}
 fileAccount=id;fileTrail=[{id:a.root,name:'根目录',path:'/'}];$('files-search').value='';filePage=1;fileSelected=null;
 nav('files',false);$('files-account').value=id;await loadCloudFiles();
}
function ensureCloudFiles(){if(!fileAccount){const a=state.accounts.find(a=>!a.disabled);if(a)void openCloudFiles(a.id);else renderCloudFiles()}}
async function loadCloudFiles(){
 const a=accountBy(fileAccount),folder=filesCurrent();if(!a||!folder)return;
 const serial=++fileSerial;fileRequest?.abort();fileRequest=new AbortController();fileLoading=true;fileReady=false;fileError='';fileEntries=[];fileSelected=null;filePage=1;renderCloudFiles();
 try{const result=await api('/api/browse?account='+encodeURIComponent(a.id)+'&folder='+encodeURIComponent(folder.id),undefined,{signal:fileRequest.signal});if(serial!==fileSerial)return;fileEntries=result.entries;fileReady=true}
 catch(e){if(serial!==fileSerial||e.name==='AbortError')return;fileError=e.message}
 finally{if(serial===fileSerial){fileLoading=false;renderCloudFiles()}}
}
function fileEpoch(value){if(!value)return 0;if(/^\d+(\.\d+)?$/.test(String(value))){const n=Number(value);return n<1e12?n*1000:n}const n=Date.parse(value);return Number.isFinite(n)?n:0}
function filesFiltered(){
 const query=$('files-search').value.trim().toLocaleLowerCase(),sort=$('files-sort').value;
 return fileEntries.filter(x=>x.name.toLocaleLowerCase().includes(query)).sort((a,b)=>a.dir!==b.dir?(a.dir?-1:1):sort==='size'?b.size-a.size:sort==='modified'?fileEpoch(b.modified)-fileEpoch(a.modified):a.name.localeCompare(b.name,'zh-CN',{numeric:true}));
}
function renderCloudFiles(){
 const a=accountBy(fileAccount),folder=filesCurrent();$('files-refresh').disabled=!a||fileLoading;$('files-up').disabled=fileTrail.length<2||fileLoading;$('files-use').disabled=!fileReady||fileLoading||!a||a.disabled;
 $('files-path').innerHTML=fileTrail.map((x,i)=>`<button data-files-trail="${i}" ${fileLoading?'disabled':''}>${esc(x.name)}</button>${i<fileTrail.length-1?'<i data-lucide="chevron-right"></i>':''}`).join('');
 $('files-current-path').textContent=folder?.path||'/';$('files-copy-path').disabled=!folder;
 $('files-list-mode').setAttribute('aria-pressed',String(fileMode==='list'));$('files-grid-mode').setAttribute('aria-pressed',String(fileMode==='grid'));
 $('files-status').textContent=fileLoading?'正在读取当前目录…':fileError?'目录读取失败':fileReady?`${fileEntries.filter(x=>x.dir).length} 个文件夹 · ${fileEntries.filter(x=>!x.dir).length} 个文件`:'选择一个网盘开始浏览';
 $('files-error').hidden=!fileError;$('files-error-copy').textContent=fileError;
 const area=$('files-content');area.className=fileMode==='grid'?'files-grid':'';
 if(fileLoading)area.innerHTML='<div class="empty files-empty" role="status"><i data-lucide="loader-circle" class="file-spinner"></i><p>正在读取网盘目录…</p></div>';
 else if(fileError)area.innerHTML='<div class="empty files-empty"><i data-lucide="cloud-off"></i><h3>暂时无法读取文件</h3><p>可重新读取，或在网盘管理中检查登录凭据。</p><button data-files-retry>重新读取</button></div>';
 else if(!a)area.innerHTML='<div class="empty files-empty"><i data-lucide="folder-open"></i><h3>选择要浏览的网盘</h3><p>添加并启用账号后，这里就能查看目录和文件。</p><button data-nav="accounts">前往网盘管理</button></div>';
 else{
  const filtered=filesFiltered(),pages=Math.max(1,Math.ceil(filtered.length/50));filePage=Math.min(filePage,pages);const shown=filtered.slice((filePage-1)*50,filePage*50);
  const title=x=>`<button class="file-name-button" data-files-entry="${esc(x.id)}"><i data-lucide="${x.dir?'folder':mediaExtensionsForUI(x.name)?'file-video':'file'}"></i><span>${esc(x.name)}</span>${x.dir?'<i data-lucide="chevron-right"></i>':''}</button>`;
  area.innerHTML=!filtered.length?`<div class="empty files-empty"><i data-lucide="${fileEntries.length?'search':'folder-open'}"></i><h3>${fileEntries.length?'当前目录没有匹配项':'这个目录还是空的'}</h3><p>${fileEntries.length?'搜索只筛选当前目录，试试其他关键词。':'可以返回上一级，或将当前目录用于生成配置。'}</p></div>`:fileMode==='grid'?shown.map(x=>`<article class="file-tile">${title(x)}<p>${x.dir?'文件夹':fileSize(x.size)}</p><small>${esc(fileTime(x.modified))}</small></article>`).join(''):`<div class="table-scroll"><table class="files-table"><thead><tr><th>名称</th><th>大小</th><th>修改时间</th><th>操作</th></tr></thead><tbody>${shown.map(x=>`<tr><td>${title(x)}</td><td>${x.dir?'—':fileSize(x.size)}</td><td>${esc(fileTime(x.modified))}</td><td><button class="text" data-files-entry="${esc(x.id)}">${x.dir?'打开':'详情'}</button></td></tr>`).join('')}</tbody></table></div>`;
  $('files-page').textContent=`${filtered.length} 项 · ${filePage} / ${pages} 页`;$('files-prev').disabled=filePage<=1;$('files-next').disabled=filePage>=pages;
 }
 $('files-pagination').hidden=!fileReady||fileLoading||!!fileError;
 $('files-detail').hidden=!fileSelected;
 if(fileSelected){$('files-detail-name').textContent=fileSelected.name;$('files-detail-path').textContent=(folder.path==='/'?'':folder.path)+'/'+fileSelected.name;$('files-detail-size').textContent=fileSize(fileSelected.size);$('files-detail-time').textContent=fileTime(fileSelected.modified)}icons();
}
function mediaExtensionsForUI(name){return /\.(mkv|mp4|m4v|avi|mov|ts|m2ts|webm|iso|flac|m4a|mp3|wav|ape|aac|ogg|opus|dsf|dff)$/i.test(name)}
function filesIntoGeneration(){
 const a=accountBy(fileAccount),folder=filesCurrent();if(!a||!folder||!fileReady)return;
 generationEditRule(null,a.id);genDraft.folder=folder.id;genDraft.source_path=folder.path;genDraft.output=folder.path==='/'?(a.name.replace(/[\\/:*?"<>|]/g,'_')):folder.name.replace(/[\\/:*?"<>|]/g,'_');
 $('gen-rule-source').value=genDraft.source_path;$('gen-rule-output').value=genOutputPath(genDraft.output);toast('目录已带入，确认输出位置后保存');
}
document.addEventListener('click',event=>{
 const b=event.target.closest('button');if(!b)return;
 if(b.id==='cloud-add'||b.id==='cloud-empty-add')openAccountForm();
 if(b.dataset.edit)openAccountForm(b.dataset.edit);
 if(b.dataset.cloudBrowse)action(b,()=>openCloudFiles(b.dataset.cloudBrowse));
 if(b.dataset.cloudRule){generationEditRule(null,b.dataset.cloudRule);void generationPickFolder('rule').catch(e=>toast(e.message))}
 if(b.dataset.cloudCheck)action(b,async()=>{const r=await api('/api/accounts/'+b.dataset.cloudCheck+'/check',{});await refresh();toast(r.reachable?'连接正常，根目录读取成功':'连接失败：'+r.error)});
 if(b.dataset.cloudQuota)action(b,async()=>{const r=await api('/api/accounts/'+b.dataset.cloudQuota+'/quota',{});await refresh();toast(r.quota_error||'容量已更新')});
 if(b.dataset.cloudRemove)action(b,async()=>{const a=accountBy(b.dataset.cloudRemove);if(!confirm('移除“'+a.name+'”的本地账号配置？云端文件和已生成文件会保留。'))return;await api('/api/accounts/'+a.id+'/remove',{});await refresh();toast('账号配置已移除')});
 if(b.dataset.filesTrail!==undefined){fileTrail=fileTrail.slice(0,Number(b.dataset.filesTrail)+1);$('files-search').value='';void loadCloudFiles()}
 if(b.dataset.filesEntry){const x=fileEntries.find(x=>x.id===b.dataset.filesEntry);if(!x||!fileReady)return;if(x.dir){const parent=filesCurrent();fileTrail.push({id:x.id,name:x.name,path:(parent.path==='/'?'':parent.path)+'/'+x.name});$('files-search').value='';void loadCloudFiles()}else{fileSelected=x;renderCloudFiles();$('files-detail').scrollIntoView({block:'nearest',behavior:'smooth'})}}
 if(b.hasAttribute('data-files-retry'))void loadCloudFiles();
});
document.addEventListener('change',event=>{const el=event.target;if(el.dataset.cloudEnabled){const previous=!el.checked;el.disabled=true;action(el,async()=>{try{await api('/api/accounts/'+el.dataset.cloudEnabled+'/enabled',{enabled:el.checked});await refresh();toast(el.checked?'网盘已启用':'网盘已停用，已有文件保留')}catch(e){el.checked=previous;throw e}})}});
document.addEventListener('DOMContentLoaded',()=>{
 $('cloud-cancel-edit').addEventListener('click',()=>{$('account-form').hidden=true;resetAccount()});
 $('files-account').addEventListener('change',event=>void openCloudFiles(event.target.value));
 $('files-refresh').addEventListener('click',()=>void loadCloudFiles());$('files-search').addEventListener('input',()=>{filePage=1;renderCloudFiles()});$('files-sort').addEventListener('change',()=>{filePage=1;renderCloudFiles()});
 $('files-up').addEventListener('click',()=>{if(fileTrail.length<2)return;fileTrail.pop();$('files-search').value='';void loadCloudFiles()});
 $('files-use').addEventListener('click',filesIntoGeneration);
 $('files-copy-path').addEventListener('click',event=>action(event.currentTarget,async()=>{await copy(filesCurrent().path);toast('当前目录路径已复制')}));
 $('files-prev').addEventListener('click',()=>{filePage--;renderCloudFiles()});$('files-next').addEventListener('click',()=>{filePage++;renderCloudFiles()});
 $('files-list-mode').addEventListener('click',()=>{fileMode='list';renderCloudFiles()});$('files-grid-mode').addEventListener('click',()=>{fileMode='grid';renderCloudFiles()});
 $('files-detail-close').addEventListener('click',()=>{fileSelected=null;renderCloudFiles()});
});
