import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
const tick = () => new Promise(resolve => setImmediate(resolve));

function page({storage = new Map(), storageBlocked = false, dashboard = {}, offline = false, respond} = {}) {
  let document;
  const makeNode = (dataset = {}) => {
    const classes = new Set();
    const node = {dataset, textContent:'', _innerHTML:'', style:{}, attributes:{}, value:'', checked:false,
      disabled:false, hidden:false, placeholder:'', name:'', options:[],
      classList:{toggle(name,on){on ? classes.add(name) : classes.delete(name);}, add(name){classes.add(name);}, remove(name){classes.delete(name);}, contains(name){return classes.has(name);}},
      setAttribute(k,v){this.attributes[k]=v;}, addEventListener(type,callback){this[type]=callback;},
      focus(){document.activeElement=this;}, closest(){return null;}, querySelectorAll(){return [];},
      reset(){ids['override-reason'].value=''; ids['override-remark'].value=''; radios.forEach(r=>r.checked=false);}};
    Object.defineProperty(node,'innerHTML',{get(){return this._innerHTML;},set(value){
      this._innerHTML=value;
      this.options=[...String(value).matchAll(/<option value="([^"]*)"/g)].map(match=>({value:match[1]}));
    }});
    return node;
  };
  const ids = Object.fromEntries([...html.matchAll(/id="([^"]+)"/g)].map(match=>[match[1],makeNode({})]));
  const labels = [...html.matchAll(/data-i18n="([^"]+)"/g)].map(match=>makeNode({i18n:match[1]}));
  const placeholders = [...html.matchAll(/data-i18n-placeholder="([^"]+)"/g)].map(match=>makeNode({i18nPlaceholder:match[1]}));
  const ariaLabels = [...html.matchAll(/data-i18n-aria="([^"]+)"/g)].map(match=>makeNode({i18nAria:match[1]}));
  const buttons = ['en','zh'].map(lang=>makeNode({lang}));
  const radios = ['FREE','OCCUPIED','BLOCKED'].map(value=>Object.assign(makeNode({}),{name:'override-status',value}));
  ids['spot-drawer'].querySelectorAll=()=>[ids['drawer-close'],...radios,ids['override-reason'],ids['override-remark'],ids['drawer-cancel'],ids['override-submit']].filter(node=>!node.disabled);
  document={documentElement:{},title:'',activeElement:null,getElementById:id=>ids[id],
    querySelector:selector=>selector.includes('override-status') ? radios.find(r=>r.checked) || null : null,
    querySelectorAll:selector=>selector==='[data-i18n]'?labels:selector==='[data-lang]'?buttons:selector==='[data-i18n-placeholder]'?placeholders:selector==='[data-i18n-aria]'?ariaLabels:selector.includes('override-status')?radios:[],
    addEventListener(type,callback){this[type]=callback;}};
  const requests=[],timers=[];
  const context=vm.createContext({document,URLSearchParams,encodeURIComponent,Date,
    localStorage:{getItem(key){if(storageBlocked)throw Error('blocked');return storage.get(key);},setItem(key,value){if(storageBlocked)throw Error('blocked');storage.set(key,value);}},
    fetch:async(url,options)=>{requests.push({url,options});if(offline)throw Error('offline');const body=respond?await respond(url,options):{code:0,data:dashboard};return{json:async()=>body};},
    setInterval:callback=>timers.push(callback)});
  vm.runInContext(script,context);
  return{context,ids,labels,placeholders,buttons,radios,document,requests,timers,run:code=>vm.runInContext(code,context)};
}

const sampleDashboard={spots:[{spot_id:'A-01',status:'OCCUPIED',plate_no:'SNE1234A',over_target:true,elapsed_sec:7380,status_since:'2026-09-04T08:00:00Z'}],
  persons_present:[{name:'Tan Wei Ming',role:'Senior Mechanic',identity_status:'IDENTIFIED',duration_sec:60}],vehicles_present:[{plate_no:'SNE1234A',duration_sec:60}],
  recent_alerts:[{behavior:'NO_HELMET',target:'STRANGER · CAM-T-1'}],cameras:[{camera_id:'CAM_001',status:'OFFLINE'}]};

test('中英文切换同步静态、动态、占位和抽屉文案且不重复轮询',async()=>{
  const p=page({dashboard:sampleDashboard});await tick();const count=p.requests.length;
  p.buttons[1].click();
  assert.equal(p.document.documentElement.lang,'zh-CN');
  assert.equal(p.ids.livetext.textContent,'实时在线');
  assert.match(p.ids.bays.innerHTML,/超过目标时长/);
  assert.match(p.ids.bays.innerHTML,/>调整<\/button>/);
  assert.equal(p.placeholders[0].placeholder,'姓名、员工编号、身份或轨迹编号');
  assert.equal(p.labels.find(label=>label.dataset.i18n==='adjustBay').textContent,'调整工位');
  assert.equal(p.requests.length,count);
  assert.equal(p.timers.length,1);
});

test('语言持久化、非法语言回退和存储不可用均安全',async()=>{
  const storage=new Map();const first=page({storage});first.buttons[1].click();
  assert.equal(page({storage}).document.documentElement.lang,'zh-CN');
  storage.set('workshop-language','invalid');assert.equal(page({storage}).document.documentElement.lang,'en');
  const blocked=page({storageBlocked:true,offline:true});await tick();blocked.buttons[1].click();
  assert.equal(blocked.ids.livetext.textContent,'连接已断开');
});

test('车位卡仅由明确调整按钮打开抽屉并可关闭后归还焦点',async()=>{
  const p=page({dashboard:sampleDashboard});await tick();
  assert.doesNotMatch(p.ids.bays.innerHTML,/onclick=/);
  assert.match(p.ids.bays.innerHTML,/data-spot-id="A-01"/);
  const trigger=p.ids.bays;trigger.focus();
  p.run("openSpotDrawer('A-01', document.getElementById('bays'))");
  assert.equal(p.ids['spot-drawer'].hidden,false);
  assert.equal(p.ids['drawer-current'].textContent,'Occupied');
  assert.equal(p.radios[1].disabled,true);
  let prevented=false;p.document.keydown({key:'Escape',preventDefault(){prevented=true;}});
  assert.equal(prevented,true);
  assert.equal(p.ids['spot-drawer'].hidden,true);
  assert.equal(p.document.activeElement,trigger);
});

test('目标状态只提供合法原因并更新调整摘要',async()=>{
  const p=page({dashboard:sampleDashboard});await tick();p.run("openSpotDrawer('A-01')");
  p.radios[0].checked=true;p.run('updateOverrideReasonOptions()');
  assert.deepEqual(p.ids['override-reason'].options.map(o=>o.value),['','VEHICLE_LEFT','SENSOR_ERROR','OTHER']);
  assert.match(p.ids['override-summary'].textContent,/A-01.*Occupied.*Free/);
  p.buttons[1].click();
  assert.match(p.ids['override-summary'].textContent,/占用.*空闲/);
});

test('人工修正提交冻结合同且 OTHER 没有备注时不发请求',async()=>{
  const p=page({dashboard:sampleDashboard});await tick();p.run("openSpotDrawer('A-01')");
  p.radios[2].checked=true;p.run('updateOverrideReasonOptions()');p.ids['override-reason'].value='OTHER';
  await p.run('submitOverride()');
  assert.match(p.ids['override-error'].textContent,/remark/i);
  assert.equal(p.requests.filter(r=>r.options?.method==='POST').length,0);
  p.ids['override-remark'].value='Access blocked';await p.run('submitOverride()');
  const post=p.requests.find(r=>r.options?.method==='POST');const body=JSON.parse(post.options.body);
  assert.deepEqual(body,{status:'BLOCKED',reason_code:'OTHER',remark:'Access blocked',operator:'dashboard'});
  assert.equal(post.options.headers.Authorization,undefined);
});

test('人工修正接口失败在抽屉内显示且保持面板打开',async()=>{
  const p=page({dashboard:sampleDashboard,respond:(url,options)=>options?.method==='POST'?{code:400,message:'reason_code is required and must match status'}:{code:0,data:sampleDashboard}});await tick();
  p.buttons[1].click();
  p.run("openSpotDrawer('A-01')");p.radios[0].checked=true;p.run('updateOverrideReasonOptions()');p.ids['override-reason'].value='VEHICLE_LEFT';
  await p.run('submitOverride()');
  assert.equal(p.ids['override-error'].textContent,'操作失败: 修正原因必填，且必须与目标状态匹配');
  assert.equal(p.ids['spot-drawer'].hidden,false);
  assert.equal(p.ids['override-submit'].disabled,false);
});

test('后端人工修正业务错误均提供中文内联文案',()=>{
  const p=page({dashboard:sampleDashboard});p.buttons[1].click();
  assert.equal(p.run("t('reason_code is required and must match status')"),'修正原因必填，且必须与目标状态匹配');
  assert.equal(p.run("t('remark must not exceed 200 characters')"),'备注不能超过 200 个字符');
  assert.equal(p.run("t('remark is required when reason_code is OTHER')"),'选择“其他”原因时必须填写备注');
});

test('人工修正提交期间锁定控件，完成后恢复',async()=>{
  let release;
  const p=page({dashboard:sampleDashboard,respond:(url,options)=>options?.method==='POST'?new Promise(resolve=>{release=()=>resolve({code:400,message:'busy test'});}):{code:0,data:sampleDashboard}});await tick();
  p.run("openSpotDrawer('A-01')");p.radios[2].checked=true;p.run('updateOverrideReasonOptions()');p.ids['override-reason'].value='BAY_MAINTENANCE';
  const pending=p.run('submitOverride()');await tick();
  assert.equal(p.ids['override-submit'].disabled,true);assert.equal(p.ids['override-reason'].disabled,true);assert.ok(p.radios.every(radio=>radio.disabled));
  release();await pending;
  assert.equal(p.ids['override-submit'].disabled,false);assert.equal(p.ids['override-reason'].disabled,false);
});

test('历史查询发送人员类型和关键词并用 person_key 请求详情',async()=>{
  const list={code:0,data:{list:[{person_key:'obs_ZXYx',identity_status:'STRANGER',track_ids:['trk-9'],first_seen:'2026-09-04T08:00:00Z',last_seen:'2026-09-04T08:02:00Z',event_count:2,segment_count:1,alert_count:0}],filters:{cameras:[],areas:[]}}};
  const detail={code:0,data:{summary:{person_key:'obs_ZXYx',identity_status:'STRANGER',track_ids:['trk-9'],first_seen:'2026-09-04T08:00:00Z',duration_sec:120,event_count:2,alert_count:0},nodes:[]}};
  const p=page({dashboard:sampleDashboard,respond:url=>String(url).includes('/obs_ZXYx?')?detail:String(url).includes('/history/person-visits?')?list:{code:0,data:sampleDashboard}});await tick();
  p.ids['history-person-type'].value='STRANGER';p.ids['history-keyword'].value=' trk-9 ';
  await p.run('queryHistory()');
  const listRequest=p.requests.find(r=>String(r.url).includes('/history/person-visits?'));
  assert.match(listRequest.url,/identity_status=STRANGER/);assert.match(listRequest.url,/keyword=trk-9/);
  assert.ok(p.requests.some(r=>String(r.url).includes('/history/person-visits/obs_ZXYx?')));
  assert.match(p.ids['history-list'].innerHTML,/data-person-key="obs_ZXYx"/);
});

test('未知人员列表和详情仅显示身份状态与轨迹，不伪造员工字段',async()=>{
  const p=page({dashboard:sampleDashboard});await tick();
  p.run(`historyMeta={cameras:[],areas:[]};historyResults=[{person_key:'obs_1',identity_status:'UNRESOLVED',track_ids:['track-21'],employee_no:'FAKE-001',first_seen:'2026-09-04T08:00:00Z',last_seen:'2026-09-04T08:01:00Z',event_count:1,segment_count:1,alert_count:0}];selectedPersonKey='obs_1';historyDetail={summary:{person_key:'obs_1',identity_status:'UNRESOLVED',track_ids:['track-21'],employee_no:'FAKE-001',first_seen:'2026-09-04T08:00:00Z',duration_sec:60,event_count:1,alert_count:0},nodes:[]};renderHistory()`);
  assert.match(p.ids['history-list'].innerHTML,/Unresolved.*track-21/);
  assert.doesNotMatch(p.ids['history-list'].innerHTML,/FAKE-001/);
  assert.match(p.ids['history-detail'].innerHTML,/Unresolved.*track-21/);
  assert.doesNotMatch(p.ids['history-detail'].innerHTML,/FAKE-001/);
});

test('静态页面无 prompt/alert 且所有显式字号不低于 12px',()=>{
  assert.doesNotMatch(html,/\b(?:prompt|alert)\s*\(/);
  const sizes=[...html.matchAll(/(?:font-size\s*:|font\s*:[^;}]*?\s)(\d+(?:\.\d+)?)px/g)].map(match=>Number(match[1]));
  assert.ok(sizes.length>20);assert.equal(sizes.filter(size=>size<12).length,0);
  assert.match(html,/\.camchip small\{[^}]*font-size:12px/);
  assert.match(html,/\.kpi\.money \.big small\{color:#C6D1DF\}/);
});
