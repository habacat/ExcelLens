const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function page(compositor=false) {
  let now = 0, sequence = 0;
  const timers = new Map(), elements = new Map();
  const body = {append(node) { node.parentElement = this; }};
  const attrs=new Map(),animations=[];
  const host = {hidden:true, parentElement:body, setAttribute:(k,v)=>attrs.set(k,v),removeAttribute:k=>attrs.delete(k),querySelector(selector) {
    if (!elements.has(selector)) elements.set(selector,{textContent:'',addEventListener() {},setAttribute() {}});
    return elements.get(selector);
  }};
  if(compositor)host.animate=(_frames,options)=>{
    const animation={options,cancelled:false,cancel(){this.cancelled=true;}};
    animations.push(animation);return animation;
  };
  const context = {document:{body,getElementById:()=>host,querySelector:()=>null,addEventListener() {}},performance:{now:()=>now}};
  context.window = context;
  context.matchMedia=()=>({matches:false});
  context.setTimeout = (fn,ms) => { const id=++sequence;timers.set(id,{fn,time:now+ms});return id; };
  context.clearTimeout = id => timers.delete(id);
  vm.runInNewContext(fs.readFileSync(__dirname+'/activity.js','utf8'),context);
  function advance(ms) {
    const end = now+ms;
    while (timers.size) {
      const [id,timer]=[...timers].sort((a,b)=>a[1].time-b[1].time)[0];
      if (timer.time>end) break;
      timers.delete(id);now=timer.time;timer.fn();
    }
    now=end;
  }
  return {activity:context.ExcelLensActivity,host,advance,timers,attrs,animations,label:()=>elements.get('.activity-label').textContent,
    value:()=>elements.get('.activity-value').textContent};
}

test('waiting for a paste choice hides progress and excludes time spent in the dialog',()=>{
  const p=page(), task=p.activity.begin('正在粘贴');
  p.advance(600);assert.equal(p.host.hidden,false);
  task.pause();p.advance(60000);assert.equal(p.host.hidden,true);
  task.resume();p.advance(499);assert.equal(p.host.hidden,true);
  p.advance(1);assert.equal(p.host.hidden,false);assert.match(p.value(),/^约 1[0-9]%$/);
  task.end();task.resume();assert.equal(p.host.hidden,true);
});

test('activity waits 500 ms across work phases and clears its only timer on completion',()=>{
  const p=page(), quick=p.activity.begin('打开');
  p.advance(499);assert.equal(p.host.hidden,true);
  quick.end();p.advance(1);assert.equal(p.host.hidden,true);assert.equal(p.timers.size,0);
  const work=p.activity.begin('打开');
  p.advance(310);work.stage('准备首屏',60,90);p.advance(189);assert.equal(p.host.hidden,true);
  p.advance(1);assert.equal(p.host.hidden,false);assert.equal(p.label(),'准备首屏');assert.match(p.value(),/^约 /);
  work.report(32,64,'索引','32 MB / 64 MB');assert.equal(p.value(),'32 MB / 64 MB');
  work.stage('显示单元格',95,99);p.advance(90000);assert.match(p.value(),/^约 9[5-8]%$/);
  work.end();work.end();assert.equal(p.host.hidden,true);assert.equal(p.timers.size,0);
});

test('a workbook render primes a delayed compositor layer and drops fast or superseded reveals',()=>{
  const p=page(true);
  const old=p.activity.begin('文件甲',{priority:2});
  assert.equal(p.animations[0].options.delay,500);
  assert.equal(p.attrs.get('aria-hidden'),'true');
  p.advance(300);old.end();assert.equal(p.animations[0].cancelled,true);
  const next=p.activity.begin('文件乙',{priority:2});p.advance(499);
  assert.equal(p.attrs.get('aria-hidden'),'true');
  p.advance(1);assert.equal(p.attrs.has('aria-hidden'),false);
  assert.equal(p.label(),'文件乙');next.end();assert.equal(p.host.hidden,true);
});

test('overlapping work stays with its document and an aborted or stale handle cannot hide a different job',()=>{
  const a=page(),b=page(),abort=new AbortController();
  const first=a.activity.begin('筛选'),second=a.activity.begin('打开',{priority:2,signal:abort.signal});
  a.advance(501);b.advance(1000);
  assert.equal(a.label(),'打开');assert.equal(b.host.hidden,true);
  const low=a.activity.request('/api/cell?ref=B2',{});a.advance(501);assert.equal(a.label(),'打开');
  first.job({stage:'筛选',scanned:12,total:40});
  abort.abort();assert.equal(a.label(),'筛选');assert.equal(a.value(),'30%');
  second.stage('旧结果');second.end();assert.equal(a.label(),'筛选');
  first.end();assert.equal(a.label(),'正在读取单元格');low.end();assert.equal(a.host.hidden,true);
  assert.equal(a.activity.request('/api/search/status?id=7',{}),null);
  assert.equal(b.activity.request('/api/tasks',{}),null);
});
