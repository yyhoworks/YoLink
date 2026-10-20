import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import { describe, expect, it } from 'vitest';

const script=readFileSync(new URL('../../web/appearance.js',import.meta.url),'utf8');
const palettes=['cream','forest','blue','clay','lilac','midnight'];
function restore(saved:Record<string,unknown>){
  const properties:Record<string,string>={},attributes:Record<string,boolean>={};
  const root={dataset:{} as Record<string,string>,toggleAttribute:(key:string,value:boolean)=>{attributes[key]=value;},style:{setProperty:(key:string,value:string)=>{properties[key]=value;}}};
  runInNewContext(script,{
    document:{documentElement:root,addEventListener:()=>{}},
    localStorage:{getItem:()=>JSON.stringify(saved)},
  });
  return{root,properties,attributes};
}
describe('unified glass preferences',()=>{
  it.each(palettes)('migrates old %s glass to neutral glass while preserving wallpaper and original palette',glassPalette=>{
    const {root,properties,attributes}=restore({style:'glass',glassPalette,originalPalette:'forest',wallpaper:'data:image/png;base64,AA==',opacity:27,shellOpacity:6});
    expect(root.dataset).toEqual({style:'glass',palette:'cream',glassText:'dark'});
    expect(properties['--user-wallpaper']).toContain('data:image/png;base64,AA==');
    expect(attributes['data-wallpaper']).toBe(true);
    expect(properties['--glass-fill-alpha']).toBe('0.27');
    expect(properties['--glass-shell-alpha']).toBe('0.06');
    expect(restore({style:'original',originalPalette:'forest',glassPalette}).root.dataset.palette).toBe('forest');
  });
  it('honors both transparency sliders including zero without automatic tint floors',()=>{
    for(const opacity of [0,10,35,90])for(const shellOpacity of [0,8,90])for(const dim of [0,80]){
      const {properties}=restore({style:'glass',opacity,shellOpacity,dim,wallpaper:'data:image/png;base64,AA=='});
      expect(Number(properties['--glass-fill-alpha'])).toBe(opacity/100);
      expect(Number(properties['--glass-shell-alpha'])).toBe(shellOpacity/100);
      expect(Number(properties['--glass-dim'])).toBe(dim/100);
      expect(properties['--glass-text-alpha']).toBeUndefined();
    }
  });
  it('defaults to a light outer tint and a shared translucent panel fill',()=>{
    const {properties}=restore({style:'glass'});
    expect(properties['--glass-fill-alpha']).toBe('0.35');
    expect(properties['--glass-shell-alpha']).toBe('0.08');
  });
  it('restores a fixed text preference without changing wallpaper or material opacity',()=>{
    for(const glassText of ['dark','light']){
      const {root,properties}=restore({style:'glass',glassText,opacity:10,shellOpacity:0});
      expect(root.dataset.glassText).toBe(glassText);
      expect(properties['--glass-fill-alpha']).toBe('0.1');
      expect(properties['--glass-shell-alpha']).toBe('0');
    }
    expect(restore({style:'glass',glassText:'invalid'}).root.dataset.glassText).toBe('dark');
  });
  it('restores an independent record reading veil and allows disabling it',()=>{
    for(const recordsOpacity of [0,24,70]){
      const {properties}=restore({style:'glass',recordsOpacity,opacity:10,shellOpacity:0});
      expect(Number(properties['--glass-record-alpha'])).toBe(recordsOpacity/100);
      expect(properties['--glass-fill-alpha']).toBe('0.1');
      expect(properties['--glass-shell-alpha']).toBe('0');
    }
    expect(restore({style:'glass',recordsOpacity:100}).properties['--glass-record-alpha']).toBe('0.7');
    expect(restore({style:'glass'}).properties['--glass-record-alpha']).toBe('0.24');
  });
});
