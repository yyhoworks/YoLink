'use strict';
let changesSerial=0,changesData=null,changeRepair=null,changeRepairUncertain=false;
const changeKinds={added:'新观测',moved:'移动或改名',metadata:'元数据变化',source_absent:'完整扫描中未见来源',local_missing:'本地 STRM 缺失',local_modified:'本地内容变化',outdated:'播放入口待核对'};
const changeStates={complete:'完整完成',partial:'部分未完成',failed:'失败',cancelled:'已取消',active:'扫描中',quarantined:'恢复后已隔离'};

$('view-maintenance').querySelector('.heading').after(Object.assign(document.createElement('section'),{id:'changes-panel',className:'panel form-panel'}));
$('changes-panel').innerHTML=`
 <div class="section-heading"><div><h2>变更核对</h2><p class="hint">查看云端观测与本地输出问题。来源缺失、移动和已有内容更新供人工核对。</p></div><button id="changes-refresh" type="button">刷新观测</button></div>
 <p class="hint">增量模式仍完整读取目录，只减少重复生成和写盘。自动同步继续沿用规则的执行计划；每隔 24 小时在下一次任务做完整输出核对。</p>
 <div class="changes-filters"><label>目录规则<select id="changes-rule"></select></label><label>扫描记录<select id="changes-run"><option value="">最近一次记录</option></select></label><button id="changes-full" type="button">完整核对此规则</button><button data-nav="inventory" type="button">查看本地文件</button></div>
 <p id="changes-error" class="error" role="alert" hidden></p><p id="changes-status" class="hint" role="status">选择规则后读取扫描记录。</p>
 <div id="changes-list" class="changes-list"></div><div class="actions changes-pages"><button id="changes-prev" disabled>上一页</button><span id="changes-page"></span><button id="changes-next" disabled>下一页</button></div>
 <details id="changes-recent"><summary>最近的修复预览与执行结果</summary><p class="hint">预览保存 24 小时。刷新或服务中断后，可打开原记录继续核验；过期后重新选择文件预览。</p><div id="changes-plans"></div></details>`;
$('inventory-error').after(Object.assign(document.createElement('section'),{id:'change-repair-panel',className:'panel form-panel',hidden:true}));
$('inventory-repair').textContent='预览修复缺失';
$('view-inventory').querySelector('.heading').insertAdjacentHTML('beforeend','<button type="button" id="inventory-changes">变更核对</button>');
$('gen-copy-subtitles').closest('.generation-options').insertAdjacentHTML('beforeend',`<label class="checkbox space-top-lg"><input id="gen-incremental" type="checkbox" aria-describedby="gen-incremental-hint">启用观测对比，减少重复生成</label><p id="gen-incremental-hint" class="hint">每次仍读取全部目录，并核验输出归属与内容；弱元数据继续按普通生成处理。已登记的缺失文件进入修复预览，不自动重建。</p>`);
$('gen-rule-form').querySelector('.form-grid').before(Object.assign(document.createElement('section'),{id:'scene-template',className:'generation-options'}));
$('scene-template').innerHTML=`<h3>场景模板</h3><p class="hint">先查看建议，再应用到草稿。账号、源目录、输出目录和执行计划由你选择。</p><div class="changes-filters"><label>使用场景<select id="scene-select"><option value="">自定义配置</option><option value="movies">电影</option><option value="series">剧集</option><option value="music">音乐</option></select></label><button id="scene-apply" type="button" disabled>应用到当前草稿</button></div><p id="scene-preview" class="hint" role="status">保存后是一条普通目录规则，可随时修改。</p><details><summary>Emby / Jellyfin 目录映射指引</summary><p class="hint">先用现有目录选择器选择 YoLink 可写的输出位置，再确认媒体服务器能够读取同一目录。两个容器中的路径名称可以不同，但必须对应同一主机目录。添加媒体库时填写媒体服务器所见的路径，播放入口填写播放器能访问的 YoLink 地址。</p><p class="hint">向导不修改 Docker 挂载或媒体库配置。保存前检查配置，首次先做范围预览或试生成。</p></details>`;
const sceneTemplates={
 movies:{preset:'video',subtitles:true,sidecars:true,note:'电影：视频媒体、匹配外挂字幕及常见 NFO / 海报。保持现有目录结构，不改名。'},
 series:{preset:'video',subtitles:true,sidecars:true,note:'剧集：视频媒体、匹配外挂字幕及常见 NFO / 海报。保留剧名 / 季目录；集号与刮削由现有整理工具处理。'},
 music:{preset:'audio',subtitles:false,sidecars:true,note:'音乐：音频媒体及常见封面 / 元数据文件，不复制外挂字幕。保持专辑目录，不修改音频标签。'}
};

function renderChangeRepair(result){
 changeRepair=result;
 const panel=$('change-repair-panel'),pending=result.items.filter(v=>v.ready&&['preview','intent'].includes(v.state));
 const enabled=pending.length>0&&!changeRepairUncertain&&result.state!=='quarantined';
 panel.hidden=false;
 panel.innerHTML=`<div class="section-heading"><h2 id="change-repair-title" tabindex="-1">缺失 STRM 修复预览</h2><button type="button" id="change-repair-close">收起预览</button></div><p class="hint">共 ${result.total} 项，可恢复 ${result.ready} 项，已核验完成 ${result.done||0} 项，冲突 ${result.conflicts||0} 项。只按登记内容恢复缺失文件，已存在或变化的文件保留。</p><ul class="change-repair-items">${result.items.map(v=>`<li><code>${esc(v.path)}</code><p class="${v.state==='conflict'?'error':'hint'}">${esc(v.note)}</p></li>`).join('')}</ul><p id="change-repair-error" class="error" role="alert" ${changeRepairUncertain?'':'hidden'}>${changeRepairUncertain?'尚未确认执行结果，请先读取保存状态。':''}</p><label class="checkbox"><input id="change-repair-confirm" type="checkbox" ${enabled?'':'disabled'}>我已核对以上路径，仅恢复缺失的 STRM</label><div class="form-actions"><button id="change-repair-apply" class="primary" type="button" disabled>${result.state==='applying'?'继续核验本次修复':'确认修复缺失'}</button><button id="change-repair-recheck" type="button">读取保存状态</button></div>`;
 $('change-repair-confirm').addEventListener('change',e=>{$('change-repair-apply').disabled=!e.target.checked||!enabled});
 $('change-repair-close').onclick=()=>{panel.hidden=true};
 $('change-repair-recheck').onclick=e=>action(e.currentTarget,()=>readChangeRepair());
 $('change-repair-apply').onclick=e=>action(e.currentTarget,()=>applyChangeRepair());
}
async function readChangeRepair(){
 if(!changeRepair)return;
 try{const result=await api('/api/changes/'+encodeURIComponent(changeRepair.id));changeRepairUncertain=false;renderChangeRepair(result)}catch(error){changeRepairUncertain=true;renderChangeRepair(changeRepair);$('change-repair-error').textContent=error.message+'。请再次读取保存状态。';throw error}
}
async function previewChangeRepair(paths){
 const result=await api('/api/changes/preview',{paths});changeRepairUncertain=false;renderChangeRepair(result);$('change-repair-title').focus({preventScroll:true});window.scrollTo({top:Math.max(0,$('change-repair-panel').getBoundingClientRect().top+window.scrollY-112),behavior:'instant'});
}
async function applyChangeRepair(){
 const preview=changeRepair;
 if(!preview||changeRepairUncertain||!$('change-repair-confirm').checked)return;
 try{const result=await api('/api/changes/'+encodeURIComponent(preview.id)+'/apply',{revision:preview.revision,confirm:'修复缺失'});changeRepairUncertain=false;renderChangeRepair({...result,state:'complete'});await loadInventory(inventoryData.page);toast('已核验完成 '+result.done+' 项，冲突 '+result.conflicts+' 项')}
 catch(error){changeRepairUncertain=true;renderChangeRepair(preview);try{await readChangeRepair();$('change-repair-error').textContent=error.message+'。已读取逐项状态；仍待处理的项目可重新确认后继续核验。';$('change-repair-error').hidden=false}catch{/* Keep the uncertain state and require a status read. */}}
}
async function changesOpen(page=1,selectedRun=''){
 if(view!=='maintenance')return;
 const serial=++changesSerial;
 genSetOptions($('changes-rule'),[{id:'',label:'全部规则'},...state.rules.map(r=>({id:r.id,label:r.name}))],$('changes-rule').value);
 const q=new URLSearchParams({rule:$('changes-rule').value,run:selectedRun,page});
 $('changes-error').hidden=true;$('changes-list').setAttribute('aria-busy','true');$('changes-status').textContent='正在读取保存的扫描观测…';
 try{
  const result=await api('/api/changes?'+q);if(serial!==changesSerial||view!=='maintenance')return;changesData=result;
  genSetOptions($('changes-run'),[{id:'',label:'最近一次记录'},...result.runs.map(v=>({id:v.id,label:(ruleBy(v.rule)?.name||'旧规则')+' · '+date(v.created)+' · '+(changeStates[v.status]||v.status)+' · '+v.scope}))],result.run||'');
  $('changes-status').textContent=(result.run_status&&result.run_status!=='complete'?'本次未完整完成，仅展示已观察到的本地问题。':'仅完整覆盖的成功扫描会更新基线并报告来源缺失。')+' 根目录最近完整核对：'+(result.last_full?date(result.last_full):'尚未完成')+'。';
  $('changes-list').innerHTML=result.items.length?result.items.map(v=>`<article class="change-row"><strong>${esc(changeKinds[v.kind]||v.kind)}</strong><div>${v.before?'<p>此前：<code>'+esc(v.before)+'</code></p>':''}${v.after?'<p>本次：<code>'+esc(v.after)+'</code></p>':''}${v.path?'<p>本地：<code>'+esc(v.path)+'</code></p>':''}</div></article>`).join(''):'<p class="empty">'+(result.runs.length?'这条记录没有可显示的变化。未完整完成的扫描不会报告来源删除。':'还没有扫描观测。选择规则后做一次完整核对；范围预览、限量试生成和失败目录重试不会建立完整基线。')+'</p>';
  $('changes-page').textContent=result.total?result.page+' / '+result.pages+' 页，共 '+result.total+' 项':'';$('changes-prev').disabled=result.page<=1;$('changes-next').disabled=!result.pages||result.page>=result.pages;$('changes-full').disabled=!$('changes-rule').value;
  $('changes-plans').innerHTML=(result.plans||[]).length?result.plans.map(p=>`<div class="change-plan-row"><span>${esc(date(p.created))} · ${esc({preview:'待确认',applying:'待核验',complete:'已处理',quarantined:'已隔离'}[p.state]||p.state)}</span><button data-change-plan="${esc(p.id)}" type="button">查看修复记录</button></div>`).join(''):'<p class="hint">尚无修复预览。在“生成文件”勾选文件后，点击“预览修复缺失”。</p>';
 }catch(error){if(serial!==changesSerial)return;$('changes-error').textContent=error.message+'。可点击刷新观测重试，已有结果保留。';$('changes-error').hidden=false;$('changes-status').textContent='本次读取未完成。'}
 finally{if(serial===changesSerial)$('changes-list').setAttribute('aria-busy','false')}
}
function changesLeave(){changesSerial++;changeRepair=null;changeRepairUncertain=false;$('change-repair-panel').hidden=true}
$('changes-refresh').onclick=e=>action(e.currentTarget,()=>changesOpen(1,$('changes-run').value));
$('changes-rule').onchange=()=>changesOpen();$('changes-run').onchange=()=>changesOpen(1,$('changes-run').value);
$('changes-prev').onclick=()=>changesOpen((changesData?.page||1)-1,$('changes-run').value);$('changes-next').onclick=()=>changesOpen((changesData?.page||1)+1,$('changes-run').value);
$('changes-full').onclick=e=>action(e.currentTarget,async()=>{const rule=$('changes-rule').value;if(!rule)throw new Error('请选择目录规则');await api('/api/jobs',{rule,full_reconcile:true});await refresh();nav('tasks');toast('已提交完整核对任务')});
$('inventory-changes').onclick=()=>nav('maintenance');
document.addEventListener('click',event=>{const button=event.target.closest('[data-change-plan]');if(!button)return;action(button,async()=>{const result=await api('/api/changes/'+encodeURIComponent(button.dataset.changePlan));nav('inventory');changeRepairUncertain=false;renderChangeRepair(result);$('change-repair-title').focus()})});
$('scene-select').onchange=()=>{const template=sceneTemplates[$('scene-select').value];$('scene-preview').textContent=template?template.note+' 应用后启用观测对比；路径、账号与执行计划保持当前选择。':'保存后是一条普通目录规则，可随时修改。';$('scene-apply').disabled=!template};
$('scene-apply').onclick=()=>{
 const id=$('scene-select').value,template=sceneTemplates[id];if(!template||!genDraft)return;
 genDraft.template=id;genDraft.template_version=1;
 $('filter-preset').value=template.preset;$('gen-copy-subtitles').checked=template.subtitles;$('gen-copy-nonmedia').checked=template.sidecars;$('gen-incremental').checked=true;updateFilterFields();
 $('scene-preview').textContent=template.note+' 已应用到草稿，保存规则后生效；已有自定义排除条件保留。';toast('模板已应用到草稿，尚未保存');
};
if(view==='maintenance'&&!$('app').hidden)changesOpen();
