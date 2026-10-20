/* Browser-local appearance preferences; online wallpapers load directly as images. */
(() => {
  'use strict';
  const palettes = [
    {id:'cream',name:'米白黑黄',tag:'原版配色',mood:'温暖 · 轻松',swatches:['#f4f3e8','#30332b','#ffdc61'],description:'柔和米白配炭黑，以奶油黄点亮主要操作。'},
    {id:'forest',name:'雾白松绿',tag:'品牌延续',mood:'自然 · 安定',swatches:['#f0f5ef','#233d32','#bce2bb'],description:'雾白与浅鼠尾草绿，让原有青绿品牌自然延续。'},
    {id:'blue',name:'云雾冰蓝',tag:'理性清晰',mood:'清爽 · 专注',swatches:['#f0f5fa','#233b51','#b8d9f2'],description:'冷白搭配柔和冰蓝，适合信息与任务密集的工作台。'},
    {id:'clay',name:'燕麦陶橙',tag:'温暖细腻',mood:'柔和 · 有温度',swatches:['#f6f0e8','#48382e','#efbc94'],description:'燕麦色、陶土橙与深咖色，让浅色界面更有材质感。'},
    {id:'lilac',name:'雾紫石墨',tag:'安静精致',mood:'克制 · 细腻',swatches:['#f3f1f8','#353247','#cfc3ed'],description:'淡雾紫与石墨灰组合，用少量紫色建立操作重点。'},
    {id:'midnight',name:'午夜琥珀',tag:'深色选项',mood:'沉静 · 聚焦',swatches:['#22262c','#15191e','#e4be73'],description:'深石墨工作面配琥珀色主操作，适合偏爱暗色的使用习惯。'},
  ];
  const glassPalettes = [
    {id:'cream',name:'通透玻璃',mood:'中性 · 通透',swatches:['#ffffff','#ffffff60','#303943'],description:'统一的中性透明材质，让壁纸透过所有内容面板。'},
  ];
  const glassPresets = [
    {id:'dark-clear',name:'深壁纸·浅字',desc:'通透高级',glassText:'light',blur:20,shellOpacity:6,opacity:16,recordsOpacity:18,dim:28,shade:0},
    {id:'light-frost',name:'浅磨砂·深字',desc:'温润白底',glassText:'dark',blur:20,shellOpacity:15,opacity:48,recordsOpacity:35,dim:0,shade:20},
    {id:'high-contrast',name:'高对比·阅读',desc:'清晰护眼',glassText:'dark',blur:26,shellOpacity:20,opacity:62,recordsOpacity:45,dim:0,shade:30},
  ];
  const root=document.documentElement;
  const storageKey='yolink-appearance-v1';
  const ids=new Set(palettes.map(p=>p.id));
  const defaults={palette:'cream',style:'original',blur:14,opacity:35,shellOpacity:8,dim:0,wallpaper:'',name:'',shade:30,wallBlur:0,fit:'cover',glassText:'dark',recordsOpacity:24};
  function wallpaperURL(value){
    if(typeof value!=='string'||value.length>4096)return '';
    try{const url=new URL(value.trim());return url.protocol==='https:'&&!url.username&&!url.password?url.href:'';}catch{return '';}
  }
  let saved={};
  try{saved=JSON.parse(localStorage.getItem(storageKey)||'{}')||{};}catch{/* Private browsing or invalid saved data. */}
  const clamp=(v,min,max,fallback)=>Number.isFinite(Number(v))?Math.min(max,Math.max(min,Number(v))):fallback;
  const savedStyle=saved.style==='glass'||(!saved.style&&saved.palette==='cream-glass')?'glass':'original';
  const originalPalette=ids.has(saved.originalPalette)?saved.originalPalette:ids.has(saved.palette)?saved.palette:'cream';
  const glassPalette='cream';
  const prefs={...defaults,originalPalette,glassPalette,palette:savedStyle==='glass'?glassPalette:originalPalette,
    style:savedStyle,recordsOpacity:clamp(saved.recordsOpacity,0,70,24),glassText:saved.glassText==='light'?'light':'dark',shellOpacity:clamp(saved.shellOpacity,0,90,8),
    blur:clamp(saved.blur,4,40,14),opacity:clamp(saved.opacity,0,90,35),dim:clamp(saved.dim,0,80,0),
    wallpaper:typeof saved.wallpaper==='string'&&/^data:image\/(jpeg|png|webp);base64,/.test(saved.wallpaper)?saved.wallpaper:'',
    name:typeof saved.name==='string'?saved.name.slice(0,180):'',shade:clamp(saved.shade,0,80,30),
    wallBlur:clamp(saved.wallBlur,0,20,0),fit:saved.fit==='contain'?'contain':'cover'};
  let panel,uploadBusy=false,adaptationReady=false,adaptationTimer=0;
  const restoredURL=wallpaperURL(saved.wallpaper);
  let rememberedURL=restoredURL;
  const availablePalettes=()=>prefs.style==='glass'?glassPalettes:palettes;
  const current=()=>availablePalettes().find(p=>p.id===prefs.palette);
  function apply(){
    root.dataset.palette=prefs.palette;
    root.dataset.style=prefs.style;
    root.dataset.glassText=prefs.glassText;
    root.toggleAttribute('data-wallpaper',!!prefs.wallpaper);
    root.style.setProperty('--glass-blur',prefs.blur+'px');
    root.style.setProperty('--glass-fill-alpha',String(prefs.opacity/100));
    root.style.setProperty('--glass-dim',String(prefs.dim/100));
    root.style.setProperty('--user-wallpaper',prefs.wallpaper?`url(${JSON.stringify(prefs.wallpaper)})`:'none');
    root.style.setProperty('--wallpaper-shade',String(prefs.shade/100));
    root.style.setProperty('--wallpaper-blur',prefs.wallBlur+'px');
    root.style.setProperty('--wallpaper-fit',prefs.fit);
    root.style.setProperty('--glass-shell-alpha',String(prefs.shellOpacity/100));
    root.style.setProperty('--glass-record-alpha',String(prefs.recordsOpacity/100));
    if(adaptationReady)adaptGlassText();
  }
  apply();
  let lastTrigger=null;
  function scheduleAdaptation(mutations){
    if(!adaptationReady||adaptationTimer||prefs.style!=='glass')return;
    if(Array.isArray(mutations)&&mutations.every(m=>{const el=m.target?.nodeType===1?m.target:m.target?.parentElement;return el&&el.closest?.('#bw-task-elapsed,#dash-updated');}))return;
    adaptationTimer=setTimeout(()=>{adaptationTimer=0;if(!document.hidden)adaptGlassText();},100);
  }
  function adaptGlassText(){
    const light=prefs.glassText==='light';
    const bgSample=light?[[20,40,48]]:[[230,238,244]];
    const helper=typeof window!=='undefined'?window.YoLinkGlassColor:null;
    const colors=helper?{body:helper.choose(bgSample,'body').css,good:helper.choose(bgSample,'good').css,danger:helper.choose(bgSample,'danger').css}:(light?{body:'#f4f6fb',good:'#b5f5d0',danger:'#ffc7cf'}:{body:'#17212b',good:'#184d2f',danger:'#8b1a2c'});
    root.style.setProperty('--glass-fg-body',colors.body);
    root.style.setProperty('--glass-fg-good',colors.good);
    root.style.setProperty('--glass-fg-danger',colors.danger);
    document.querySelectorAll('[data-glass-adapted]').forEach(el=>{el.removeAttribute('data-glass-adapted');el.removeAttribute('data-glass-role');});
    if(prefs.style!=='glass')return;
    // One saved foreground for the whole workspace; scrolling never changes it.
    document.querySelectorAll('h1,h2,h3,p,small,label,summary,button,a,code,time,dt,dd,th,td,span,strong,input,select,textarea,svg').forEach(el=>{
      if(el.closest('.wallpaper-preview,.style-sample,.palette-swatches,.sidebar'))return;
      const text=Array.from(el.childNodes).some(n=>n.nodeType===3&&n.textContent.trim());
      if(!text&&!el.matches('button,input,select,textarea,svg'))return;
      const primary=el.closest('button.primary');
      const role=primary?'primary':el.closest('.error,.danger')?'danger':el.closest('.good')?'good':'body';
      el.setAttribute('data-glass-adapted',light||primary?'light':'dark');
      el.setAttribute('data-glass-role',role);
    });
  }
  function message(text,error=false){
    const target=panel?.querySelector('.skin-status');
    if(target){target.textContent=text;target.classList.toggle('error',error);}
  }
  function persist(){
    try{localStorage.setItem(storageKey,JSON.stringify({...prefs,wallpaper:prefs.wallpaper||rememberedURL}));message('已保存到当前浏览器。');return true;}
    catch{message('当前浏览器无法保存设置，本次仍可预览。可尝试选择更小的图片。',true);return false;}
  }
  function choices(){
    return availablePalettes().map(p=>`<button type="button" class="palette-choice ${p.id===prefs.palette?'selected':''}" data-palette-choice="${p.id}" aria-pressed="${p.id===prefs.palette}"><span class="palette-swatches" aria-hidden="true">${p.swatches.map(color=>`<i data-swatch="${color}"></i>`).join('')}</span><span class="palette-name">${p.name}</span><span class="palette-check" aria-hidden="true">✓</span></button>`).join('');
  }
  function syncPresets(){
    document.querySelectorAll('[data-glass-preset]').forEach(btn=>{
      const p=glassPresets.find(x=>x.id===btn.dataset.glassPreset);
      const active=Boolean(p&&prefs.glassText===p.glassText&&prefs.blur===p.blur&&prefs.shellOpacity===p.shellOpacity&&prefs.opacity===p.opacity&&prefs.recordsOpacity===p.recordsOpacity&&prefs.dim===p.dim);
      btn.classList.toggle('selected',active);btn.setAttribute('aria-pressed',String(active));
    });
  }
  function sliders(){
    const presets=`<div class="glass-presets-group"><span class="glass-presets-label">一键推荐配方</span><div class="glass-presets" role="group" aria-label="毛玻璃推荐配方">${glassPresets.map(p=>`<button type="button" class="glass-preset" data-glass-preset="${p.id}" aria-pressed="false"><strong>${p.name}</strong><small>${p.desc}</small></button>`).join('')}</div></div>`;
    return `<div class="glass-controls" ${prefs.style==='glass'?'':'hidden'} aria-label="毛玻璃材质调整">${presets}<label>文字颜色<select data-pref="glassText" aria-label="玻璃文字颜色"><option value="dark">深色文字</option><option value="light">浅色文字</option></select></label><label>模糊半径<input type="range" data-pref="blur" min="4" max="40" value="${prefs.blur}"><output>${prefs.blur}px</output></label><label>工作台底色<input type="range" data-pref="shellOpacity" min="0" max="90" value="${prefs.shellOpacity}"><output>${prefs.shellOpacity}%</output></label><label>面板覆盖度<input type="range" data-pref="opacity" min="0" max="90" value="${prefs.opacity}"><output>${prefs.opacity}%</output></label><label>列表阅读底色<input type="range" data-pref="recordsOpacity" min="0" max="70" value="${prefs.recordsOpacity}"><output>${prefs.recordsOpacity}%</output></label><label>背景暗化<input type="range" data-pref="dim" min="0" max="80" value="${prefs.dim}"><output>${prefs.dim}%</output></label><p class="glass-readability-hint">工作台底色、面板覆盖度与列表阅读底色独立调节；设为 0% 可去掉对应底色。</p><p class="glass-readability-hint">文字颜色固定保存，不随壁纸或滚动变色。浅色壁纸选深色文字，深色壁纸选浅色文字；背景复杂时可调整面板覆盖度。</p><button type="button" data-glass-reset>恢复默认材质</button></div>`;
  }
  function styles(){
    return `<div class="skin-styles" role="group" aria-label="界面风格">${[['original','原版','米白黑黄的原版工作台'],['glass','毛玻璃','通透玻璃 · 统一透明材质']].map(([id,name,description])=>`<button type="button" class="skin-style ${prefs.style===id?'selected':''}" data-style-choice="${id}" aria-pressed="${prefs.style===id}"><span class="style-sample style-sample-${id}" aria-hidden="true"><i></i><i></i><i></i></span><strong>${name}</strong><small>${description}</small></button>`).join('')}</div>`;
  }
  function sync(){
    if(panel)panel.querySelector('.skin-palettes').closest('section').hidden=prefs.style==='glass';
    document.querySelectorAll("[data-swatch]").forEach(swatch=>swatch.style.background=swatch.dataset.swatch);
    document.querySelectorAll('[data-palette-choice]').forEach(button=>{
      const active=button.dataset.paletteChoice===prefs.palette;
      button.classList.toggle('selected',active);button.setAttribute('aria-pressed',String(active));
    });
    document.querySelectorAll('[data-style-choice]').forEach(button=>{
      const active=button.dataset.styleChoice===prefs.style;
      button.classList.toggle('selected',active);button.setAttribute('aria-pressed',String(active));
    });
    document.querySelectorAll('.glass-controls').forEach(el=>el.hidden=prefs.style!=='glass');
    document.querySelectorAll('.palette-caption').forEach(el=>el.innerHTML=`<strong>${current().mood}</strong><span>${current().description}</span>`);
    document.querySelectorAll('[data-pref]').forEach(input=>{
      input.value=prefs[input.dataset.pref];
      if(input.nextElementSibling?.tagName==='OUTPUT')input.nextElementSibling.textContent=input.value+(['blur','wallBlur'].includes(input.dataset.pref)?'px':'%');
    });
    syncPresets();
    if(!panel)return;
    panel.querySelector('.wallpaper-name').textContent=prefs.wallpaper?prefs.name:'默认背景';
    panel.querySelector('[data-wallpaper-reset]').disabled=!(prefs.wallpaper||rememberedURL)||uploadBusy;
    panel.querySelector('[data-wallpaper-upload]').disabled=uploadBusy;
    panel.querySelector('[data-wallpaper-upload]').textContent=uploadBusy?'正在处理…':prefs.wallpaper?'更换图片':'选择图片';
    panel.querySelector('[data-wallpaper-online]').disabled=uploadBusy;
    panel.querySelector('[data-wallpaper-online]').textContent=uploadBusy?'正在加载…':'使用在线壁纸';
    panel.querySelector('#wallpaper-url').disabled=uploadBusy;
    panel.querySelector('.wallpaper-online').setAttribute('aria-busy',String(uploadBusy));
    panel.querySelector('.wallpaper-options').disabled=!prefs.wallpaper||uploadBusy;
  }
  function updateAppearanceMeta(){
    const meta=document.querySelector('meta[name="theme-color"]');
    if(meta)meta.content=getComputedStyle(root).getPropertyValue('--body').trim();
  }
  function select(id){
    if(!ids.has(id))return;
    prefs.palette=id;prefs[prefs.style+'Palette']=id;apply();sync();persist();
    updateAppearanceMeta();
  }
  function close(restoreFocus=false){
    panel.hidden=true;
    document.querySelectorAll('[data-skin-toggle]').forEach(el=>el.setAttribute('aria-expanded','false'));
    if(restoreFocus)(lastTrigger||document.querySelector('[data-skin-toggle]'))?.focus?.();
  }
  document.addEventListener('DOMContentLoaded',()=>{
    updateAppearanceMeta();
    panel=document.createElement('aside');panel.id='skin-panel';panel.className='skin-panel';panel.hidden=true;
    panel.setAttribute('role','dialog');panel.setAttribute('aria-modal','false');
    panel.setAttribute('aria-labelledby','skin-heading');
    panel.innerHTML=`<header class="skin-heading"><div><h2 id="skin-heading">皮肤管理</h2><p>配色、材质，还有你喜欢的风景。</p></div><button type="button" data-skin-close aria-label="关闭皮肤管理"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button></header><section class="skin-section"><h3>界面风格</h3>${styles()}${sliders()}</section><section class="skin-section"><h3>原版配色</h3><div class="skin-palettes" role="group" aria-label="皮肤配色">${choices()}</div></section><section class="skin-section"><div class="wallpaper-heading"><h3>自选壁纸</h3><span>仅保存在此浏览器</span></div><div class="wallpaper-preview" role="img" aria-label="当前壁纸预览"><span class="wallpaper-name"></span></div><div class="wallpaper-actions"><button type="button" class="primary" data-wallpaper-upload>选择图片</button><button type="button" class="secondary" data-wallpaper-reset>恢复默认背景</button><input id="wallpaper-file" type="file" accept="image/jpeg,image/png,image/webp" aria-label="选择壁纸图片" hidden></div><p class="wallpaper-help">支持 JPG、PNG、WebP，最大 20 MB。图片在本地处理，不上传服务器。</p><fieldset class="wallpaper-options"><legend class="sr-only">壁纸显示设置</legend><label>显示方式<select data-pref="fit" aria-label="壁纸显示方式"><option value="cover">铺满屏幕</option><option value="contain">完整显示</option></select></label><label>壁纸模糊<input type="range" data-pref="wallBlur" min="0" max="20" value="${prefs.wallBlur}"><output>${prefs.wallBlur}px</output></label><label>柔和遮罩<input type="range" data-pref="shade" min="0" max="80" value="${prefs.shade}"><output>${prefs.shade}%</output></label></fieldset><p class="wallpaper-help">与毛玻璃搭配，可让壁纸透过工作台。</p></section><p class="skin-status" role="status" aria-live="polite">设置自动保存到当前浏览器。</p>`;
    panel.querySelector('.wallpaper-options').insertAdjacentHTML('beforebegin',`<form class="wallpaper-online" aria-busy="false" novalidate><label for="wallpaper-url">在线图片网址</label><input id="wallpaper-url" type="url" inputmode="url" autocomplete="off" spellcheck="false" maxlength="4096" placeholder="https://example.com/wallpaper.jpg" aria-describedby="wallpaper-online-help wallpaper-online-status"><button type="submit" data-wallpaper-online>使用在线壁纸</button><p id="wallpaper-online-help" class="wallpaper-help">使用 HTTPS 图片直链，不是网页地址。浏览器会直接访问图片站点；只保存网址，刷新时重新加载。请使用可信链接。</p><p id="wallpaper-online-status" class="wallpaper-online-status" role="status" aria-live="polite"></p></form>`);
    document.body.append(panel);sync();
    if(restoredURL){panel.querySelector('#wallpaper-url').value=restoredURL;useOnlineWallpaper(restoredURL,true);}
    adaptationReady=true;
    new MutationObserver(scheduleAdaptation).observe(document.body,{childList:true,subtree:true,characterData:true,attributes:true,attributeFilter:['hidden','open']});
    document.addEventListener('visibilitychange',scheduleAdaptation);
    scheduleAdaptation();
  });
  document.addEventListener('click',event=>{
    const trigger=event.target.closest('[data-skin-toggle]');
    if(trigger){lastTrigger=trigger;panel.hidden=!panel.hidden;trigger.setAttribute('aria-expanded',String(!panel.hidden));if(!panel.hidden){sync();panel.querySelector('[data-skin-close]').focus();}else{trigger.focus();}return;}
    if(event.target.closest('[data-skin-close]')){close(true);return;}
    const choice=event.target.closest('[data-palette-choice]');if(choice)select(choice.dataset.paletteChoice);
    const styleChoice=event.target.closest('[data-style-choice]');
    if(styleChoice&&['original','glass'].includes(styleChoice.dataset.styleChoice)){
      prefs.style=styleChoice.dataset.styleChoice;prefs.palette=prefs[prefs.style+'Palette'];
      panel.querySelector('.skin-palettes').innerHTML=choices();apply();sync();persist();updateAppearanceMeta();
    }
    const presetBtn=event.target.closest('[data-glass-preset]');
    if(presetBtn){
      const p=glassPresets.find(x=>x.id===presetBtn.dataset.glassPreset);
      if(p){Object.assign(prefs,{glassText:p.glassText,blur:p.blur,shellOpacity:p.shellOpacity,opacity:p.opacity,recordsOpacity:p.recordsOpacity,dim:p.dim,shade:p.shade});apply();sync();persist();message(`已应用「${p.name}」毛玻璃方案。`);}
      return;
    }
    if(event.target.closest('[data-glass-reset]')){prefs.blur=14;prefs.opacity=35;prefs.shellOpacity=8;prefs.recordsOpacity=24;prefs.dim=0;apply();sync();persist();}
    if(event.target.closest('[data-wallpaper-upload]')){panel.querySelector('#wallpaper-file').click();}
    if(event.target.closest('[data-wallpaper-reset]')&&!uploadBusy){prefs.wallpaper='';rememberedURL='';prefs.name='';prefs.shade=30;prefs.wallBlur=0;prefs.fit='cover';panel.querySelector('#wallpaper-url').value='';onlineMessage('');apply();sync();persist();}
    if(panel&&!panel.hidden&&!panel.contains(event.target))close();
  });
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&panel&&!panel.hidden){close(true);}});
  document.addEventListener('input',event=>{
    const input=event.target,key=input.dataset.pref;if(!key)return;
    prefs[key]=['fit','glassText'].includes(key)?input.value:Number(input.value);apply();syncPresets();
    document.querySelectorAll(`[data-pref="${key}"]`).forEach(el=>{
      el.value=input.value;
      if(el.nextElementSibling?.tagName==='OUTPUT')el.nextElementSibling.textContent=input.value+(['blur','wallBlur'].includes(key)?'px':'%');
    });
  });
  async function encode(file){
    if(!['image/jpeg','image/png','image/webp'].includes(file.type))throw Error('请选择 JPG、PNG 或 WebP 图片。');
    if(file.size>20*1024*1024)throw Error('图片超过 20 MB，请缩小后重试。');
    const image=new Image();
    const source=await new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result);reader.onerror=()=>reject(Error('无法读取图片，请重试。'));reader.readAsDataURL(file);});
    {
      await new Promise((resolve,reject)=>{image.onload=resolve;image.onerror=()=>reject(Error('无法读取这张图片，请换一张重试。'));image.src=source;});
      const scale=Math.min(1,2560/Math.max(image.naturalWidth,image.naturalHeight));
      const canvas=document.createElement('canvas');canvas.width=Math.max(1,Math.round(image.naturalWidth*scale));canvas.height=Math.max(1,Math.round(image.naturalHeight*scale));
      const ctx=canvas.getContext('2d');if(!ctx)throw Error('浏览器无法处理图片，请换一个浏览器重试。');
      ctx.fillStyle='#eee8d7';ctx.fillRect(0,0,canvas.width,canvas.height);ctx.drawImage(image,0,0,canvas.width,canvas.height);
      let data=canvas.toDataURL('image/jpeg',.86);
      if(data.length>2200000)data=canvas.toDataURL('image/jpeg',.64);
      return data;
    }
  }
  function onlineMessage(text,error=false){
    const status=panel.querySelector('#wallpaper-online-status');
    status.textContent=text;status.classList.toggle('error',error);
    panel.querySelector('#wallpaper-url').setAttribute('aria-invalid',String(error));
  }
  function loadOnlineImage(url){
    return new Promise((resolve,reject)=>{
      const image=new Image();
      const finish=error=>{clearTimeout(timer);image.onload=null;image.onerror=null;if(error){image.src='';reject(error);}else resolve();};
      const timer=setTimeout(()=>finish(Error('图片加载超时，请检查网络后重试，或改用本地图片。')),15000);
      image.referrerPolicy='no-referrer';
      image.onload=()=>finish(image.naturalWidth&&image.naturalHeight?null:Error('链接未返回有效图片，请使用图片直链。'));
      image.onerror=()=>finish(Error('在线图片加载失败，请检查直链、网络或防盗链限制，也可改用本地图片。'));
      image.src=url;
    });
  }
  async function useOnlineWallpaper(value,restoring=false){
    if(uploadBusy)return;
    const url=wallpaperURL(value);
    if(!url){onlineMessage('请输入完整的 HTTPS 图片直链，网址不能包含用户名或密码。',true);return;}
    uploadBusy=true;sync();onlineMessage('正在加载在线图片…');
    try{
      await loadOnlineImage(url);
      prefs.wallpaper=url;rememberedURL=url;prefs.name='在线壁纸 · '+new URL(url).hostname;
      panel.querySelector('#wallpaper-url').value=url;apply();sync();
      const stored=persist();
      onlineMessage(stored?'在线壁纸已应用，网址已保存到当前浏览器。':'在线壁纸已预览，但浏览器无法保存网址。',!stored);
    }catch(error){
      const text=error.message+(restoring?' 当前使用默认背景，可重新加载或选择本地图片。':' 原壁纸已保留。');
      onlineMessage(text,true);message(text,true);
    }finally{uploadBusy=false;sync();}
  }
  document.addEventListener('submit',event=>{
    if(!event.target.matches('.wallpaper-online'))return;
    event.preventDefault();useOnlineWallpaper(panel.querySelector('#wallpaper-url').value);
  });
  document.addEventListener('change',async event=>{
    if(event.target.matches('[data-pref]'))persist();
    if(event.target.id!=='wallpaper-file')return;
    const file=event.target.files?.[0];event.target.value='';if(!file||uploadBusy)return;
    uploadBusy=true;sync();message('正在本地处理图片…');
    try{const data=await encode(file);prefs.wallpaper=data;rememberedURL='';prefs.name=file.name;panel.querySelector('#wallpaper-url').value='';onlineMessage('');apply();sync();persist();}
    catch(error){message(error.message||'图片处理失败，请换一张重试。',true);}
    finally{uploadBusy=false;sync();}
  });
})();
