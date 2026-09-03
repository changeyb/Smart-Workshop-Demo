import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
const tick = () => new Promise(resolve => setImmediate(resolve));

function page({storage = new Map(), storageBlocked = false, data = {}, offline = false} = {}) {
  const node = dataset => ({dataset, textContent:'', innerHTML:'', style:{}, attributes:{},
    classList:{toggle(){}}, setAttribute(k,v){this.attributes[k]=v;},
    addEventListener(type, callback){this[type]=callback;}});
  const ids = Object.fromEntries([...html.matchAll(/id="([^"]+)"/g)].map(m => [m[1], node({})]));
  const labels = [...html.matchAll(/data-i18n="([^"]+)"/g)].map(m => node({i18n:m[1]}));
  const buttons = ['en','zh'].map(lang => node({lang}));
  const document = {documentElement:{}, title:'', getElementById:id=>ids[id],
    querySelectorAll:selector=>selector === '[data-i18n]' ? labels : buttons};
  const requests = [], alerts = [], prompts = [], timers = [];
  let answer = null;
  const context = vm.createContext({document,
    localStorage:{getItem(k){if(storageBlocked) throw Error('blocked'); return storage.get(k);},
      setItem(k,v){if(storageBlocked) throw Error('blocked'); storage.set(k,v);}},
    fetch:async (url, options)=>{
      requests.push({url,options});
      if(offline) throw Error('offline');
      return {json:async()=>({code:0,data})};
    },
    prompt:(...args)=>{prompts.push(args); return answer;}, alert:message=>alerts.push(message),
    setInterval:callback=>timers.push(callback)
  });
  vm.runInContext(script, context);
  return {context, ids, labels, buttons, document, requests, alerts, prompts, timers,
    answer(value){answer=value;}, run:code=>vm.runInContext(code,context)};
}

test('switch translates static and dynamic text without fetching or duplicating polling', async()=>{
  const p = page({data:{spots:[{spot_id:'A-01',status:'OCCUPIED',over_target:true,elapsed_sec:7380}],
    persons_present:[{name:'Tan Wei Ming',role:'Senior Mechanic',identity_status:'IDENTIFIED',duration_sec:60}],
    vehicles_present:[{plate_no:'SNE1234A',duration_sec:60}],
    recent_alerts:[{behavior:'NO_HELMET',target:'STRANGER · CAM-T-1'}], cameras:[{camera_id:'CAM_001',status:'OFFLINE'}]}});
  await tick();
  assert.equal(p.document.documentElement.lang,'en');
  assert.equal(p.ids.livetext.textContent,'LIVE');
  const count = p.requests.length;
  p.buttons[1].click();
  assert.equal(p.document.documentElement.lang,'zh-CN');
  assert.equal(p.buttons[1].attributes['aria-pressed'],'true');
  assert.equal(p.ids.livetext.textContent,'实时在线');
  assert.match(p.ids.bays.innerHTML,/data-over-label="超过目标时长"/);
  assert.match(p.ids.bays.innerHTML,/2小时 03分/);
  assert.match(p.ids.staff.innerHTML,/高级维修技师/);
  assert.match(p.ids.staff.innerHTML,/Tan Wei Ming/);
  assert.match(p.ids.vehs.innerHTML,/SNE1234A/);
  assert.match(p.ids.alerts.innerHTML,/未戴安全帽/);
  assert.match(p.ids.alerts.innerHTML,/陌生人 · CAM-T-1/);
  assert.match(p.ids.cams.innerHTML,/离线/);
  assert.equal(p.requests.length,count);
  await p.timers[0]();
  assert.equal(p.ids.livetext.textContent,'实时在线');
  p.buttons[0].click();
  assert.match(p.ids.bays.innerHTML,/OVER TARGET/);
  assert.equal(p.timers.length,1);
  for(const label of p.labels) assert.notEqual(label.textContent,label.dataset.i18n);
});

test('saved language survives page recreation; unsupported value falls back to English', async()=>{
  const storage = new Map();
  const first = page({storage});
  first.buttons[1].click();
  const second = page({storage});
  await tick();
  assert.equal(second.document.documentElement.lang,'zh-CN');
  assert.match(second.ids.staff.innerHTML,/暂无在场人员/);
  assert.match(second.ids.alerts.innerHTML,/暂无告警/);
  assert.match(second.ids.vehs.innerHTML,/暂无在场车辆/);
  storage.set('workshop-language','invalid');
  assert.equal(page({storage}).document.documentElement.lang,'en');
});

test('unavailable storage and disconnection do not prevent switching', async()=>{
  const p = page({storageBlocked:true,offline:true});
  await tick();
  assert.equal(p.ids.livetext.textContent,'DISCONNECTED');
  p.buttons[1].click();
  assert.equal(p.ids.livetext.textContent,'连接已断开');
  assert.equal(p.document.documentElement.lang,'zh-CN');
});

test('Chinese override input uses unchanged API enum; cancellation and invalid input never write', async()=>{
  const p = page();
  await tick();
  p.buttons[1].click();
  p.answer('占用');
  await p.run("overrideSpot('A-01')");
  const post = p.requests.find(r=>r.options?.method==='POST');
  assert.equal(JSON.parse(post.options.body).status,'OCCUPIED');
  assert.equal(post.options.headers.Authorization,undefined);
  assert.match(p.prompts[0][0],/人工修正工位 A-01/);
  assert.equal(p.prompts[0][1],'空闲');
  p.answer(null);
  await p.run("overrideSpot('A-01')");
  p.answer('invalid');
  await p.run("overrideSpot('A-01')");
  assert.match(p.alerts[0],/状态无效/);
  assert.equal(p.requests.filter(r=>r.options?.method==='POST').length,1);
});
