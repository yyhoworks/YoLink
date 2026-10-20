/* Contrast decisions for local glass backgrounds; no image or color leaves the browser. */
(() => {
  'use strict';
  const palettes={
    body:[[23,33,43],[244,246,251]],
    secondary:[[38,55,70],[226,232,240]],
    good:[[24,77,47],[181,245,208]],
    danger:[[139,26,44],[255,199,207]],
  };
  const luminance=c=>c.map(v=>v/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4).reduce((n,v,i)=>n+v*[.2126,.7152,.0722][i],0);
  const contrast=(a,b)=>{const x=luminance(a),y=luminance(b);return(Math.max(x,y)+.05)/(Math.min(x,y)+.05);};
  const blend=(a,b,alpha)=>a.map((v,i)=>v*alpha+b[i]*(1-alpha));
  function choose(samples,role='body',previous=''){
    const candidates=[...(palettes[role]||palettes.body)];
    if(role==='good'||role==='danger')candidates.push(...palettes.body);
    // The 15th percentile rejects isolated texture pixels without treating a
    // whole wallpaper average as the background behind every label.
    const scores=candidates.map(color=>{
      const ratios=samples.map(bg=>contrast(color,bg)).sort((a,b)=>a-b);
      return{color,score:ratios[Math.floor((ratios.length-1)*.15)],css:'rgb('+color.join(', ')+')'};
    });
    const best=scores.reduce((a,b)=>b.score>a.score?b:a);
    const old=scores.find(v=>v.css===previous);
    const selected=old&&old.score>=4.5&&best.score<old.score+.6?old:best;
    return{...selected,limited:selected.score<4.5,light:luminance(selected.color)>.5};
  }
  window.YoLinkGlassColor=Object.freeze({choose,blend,contrast,luminance});
})();
