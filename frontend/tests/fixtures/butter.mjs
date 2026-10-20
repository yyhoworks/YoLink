// Deliberately synthetic populated data. No external service or credential is used.
import { fixtureAPI } from './api.mjs';
const now=Date.now(),seconds=Math.floor(now/1000);
const accounts=[
 {id:'demo-mobile',name:'演示 · 移动云盘',provider:'139',root:'root',disabled:false},
 {id:'demo-quark',name:'演示 · 夸克网盘',provider:'quarktv',root:'0',disabled:false},
 {id:'demo-ali',name:'演示 · 阿里云盘',provider:'ali',root:'root',drive:'demo',disabled:false},
];
const rules=accounts.map((a,i)=>({id:'demo-rule-'+i,name:['电影 · 精选片库','剧集 · 持续追更','动漫 · 每周更新'][i],account:a.id,folder:'demo-folder',source_path:['/media/电影','/media/剧集','/media/动漫'][i],output:['电影','剧集','动漫'][i],enabled:true,interval:3600,last_run:seconds-600,filter:{preset:'video'},copy_subtitles:false,copy_non_media:false}));
const job=(i,status,extra={})=>({id:'demo-job-'+i,rule:rules[i].id,account:rules[i].account,source_path:rules[i].source_path,folder:'demo-folder',status,created:seconds-i*180,started:seconds-102,finished:status==='running'?0:seconds-20,seen:1248,created_files:286,skipped:950,errors:0,message:'合成任务',current_path:rules[i].source_path+'/演示影片.mkv',dry_run:false,limit:0,samples:[],failures:[],...extra});
const jobs=[job(0,'running'),job(1,'failed',{errors:1,message:'演示：目录读取失败，请检查网盘连接。'}),job(2,'completed')];
const reports=Object.fromEntries(accounts.map((a,i)=>[a.id,{checked:seconds,reachable:true,entries:18+i,quota:i===1?undefined:{used:1099511627776,total:5497558138880,checked:seconds},quota_error:i===1?'此通道不支持容量读取':''}]));
const overview={outputs:17735,media:17735,running:1,queued:0,failed:1};
export function butterFixture(path){
 const base=fixtureAPI(path);
 if(path==='/api/state')return {...base,accounts,rules,jobs,failures:[jobs[1]],overview,account_reports:reports,settings:{public_base:'http://yolink.example.test:8796',output_root:'/strm'}};
 if(path==='/api/dashboard')return {...base,time:now,overview,accounts,reports,jobs,plans:rules.map(r=>({...r,next:seconds+300})),stats:{today_created:286,requests:512,resolved:496,resolve_total:502,resolve_ms:36400,cache:213,upstream_ms:18000,upstream_count:289},trend:[142,86,210,174,238,196,286].map((n,i)=>({day:new Date(now-(6-i)*86400000).toISOString().slice(0,10),created:n,requests:n*2,covered:true})),recent:[{media:'demo-media',name:'演示影片.mkv',path:'/strm/电影/演示影片.strm',created:now,rule:rules[0].id}],errors:[{name:'演示影片.mkv',error:'演示：上游链接已过期，请重新解析。',time:now-600000}],metrics_since:new Date(now-7*86400000).toISOString()};
 if(path==='/api/integrations')return {config:{enabled:true,type:'emby',base:'http://emby.example.test:8096',local:'/strm',remote:'/media',has_key:true},deliveries:[]};
 if(path==='/api/browse')return {entries:[{id:'demo-folder',name:'演示电影',dir:true,path:'/media/演示电影'},{id:'demo-file',name:'演示影片.mkv',size:1073741824,dir:false,path:'/media/演示影片.mkv'}]};
 if(path==='/api/records')return {items:[1,2,3].map(i=>({id:'demo-record-'+i,kind:'generation',event:'strm',outcome:i===2?'skipped':'created',name:'演示剧集 · 山海之间 - S01E0'+i+' - 2160p - WEB-DL.mkv',media:'demo-media-'+i,time:now-i*60000,path:'/media/剧集/演示剧集 · 山海之间 (2026)/Season 1/山海之间 - S01E0'+i+'.mkv',output:'/strm/剧集/演示剧集 · 山海之间 (2026)/Season 1/山海之间 - S01E0'+i+'.strm',rule_name:'演示目录',trigger:'manual'})),page:1,pages:1,total:3,write_error:''};
 if(path==='/api/notification-channels')return {items:[],channels:[],revision:'fixture'};
 if(path==='/api/event-clients')return {items:[],clients:[],calls:[],legacy:{enabled:false},revision:'fixture'};
 if(path==='/api/outputs')return {items:[],page:1,pages:0,total:0};
 return base;
}
