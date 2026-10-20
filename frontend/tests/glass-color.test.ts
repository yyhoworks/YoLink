import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import { describe, expect, it } from 'vitest';

type RGB=number[];
type Result={color:RGB;css:string;score:number;limited:boolean;light:boolean};
const context={window:{} as {YoLinkGlassColor:{choose:(samples:RGB[],role?:string,previous?:string)=>Result;contrast:(a:RGB,b:RGB)=>number;blend:(a:RGB,b:RGB,alpha:number)=>RGB}}};
runInNewContext(readFileSync(new URL('../../web/glass-color.js',import.meta.url),'utf8'),context);
const color=context.window.YoLinkGlassColor;
describe('local glass foreground contrast',()=>{
  it('selects light text on the dark path background from the reported screenshot',()=>{
    const result=color.choose([[46,106,121]],'secondary');
    expect(result.light).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(4.5);
  });
  it('selects dark text on bright glass instead of making every label white',()=>{
    const result=color.choose([[210,224,230]],'secondary');
    expect(result.light).toBe(false);
    expect(result.score).toBeGreaterThanOrEqual(4.5);
  });
  it('accounts for stacked transparent fills',()=>{
    const outer=color.blend([255,255,255],[30,100,110],.08);
    const panel=color.blend([255,255,255],outer,.35);
    expect(color.choose([panel]).score).toBeGreaterThanOrEqual(4.5);
    expect(color.blend([255,255,255],outer,0)).toEqual(outer);
  });
  it('flags busy black-and-white backgrounds instead of claiming compliance',()=>{
    const result=color.choose([[0,0,0],[0,0,0],[255,255,255],[255,255,255]]);
    expect(result.limited).toBe(true);
  });
  it('keeps the previous readable foreground through small score changes',()=>{
    const initial=color.choose([[58,62,66]]);
    const next=color.choose([[60,64,68]],'body',initial.css);
    expect(next.css).toBe(initial.css);
  });
  it.each(['good','danger'])('keeps %s labels readable on light and dark backgrounds',role=>{
    for(const background of [[230,238,244],[20,40,48]])expect(color.choose([background],role).score).toBeGreaterThanOrEqual(4.5);
  });
});
