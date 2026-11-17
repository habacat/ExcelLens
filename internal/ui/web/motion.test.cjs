const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
test('shared motion cancels replaced feedback and respects reduced motion',()=>{
  let reduced=false,cancelled=0;const calls=[],node={animate:(frames,options)=>{calls.push({frames,options});return {cancel:()=>cancelled++,finished:new Promise(()=>{})};}},context={window:{matchMedia:()=>({matches:reduced})},getComputedStyle:()=>({transform:'matrix(1,0,0,1,-50,0)'})};
  vm.runInNewContext(fs.readFileSync(__dirname+'/motion.js','utf8'),context);const motion=context.window.ExcelLensMotion;
  motion.enter(node,{distance:3});motion.enter(node);assert.equal(cancelled,1);assert.equal(calls.length,2);assert.match(calls[0].frames[0].transform,/matrix.*translateY/);assert.equal(calls[0].options.duration,160);
  reduced=true;motion.enter(node);assert.equal(calls.length,2);motion.enter({});
});
test('exit completion is cancelled by a newer reveal and reduced motion stops existing animation',async()=>{
  let reduced=false,finish,cancelled=0;const node={animate:()=>({cancel(){cancelled++;},finished:new Promise(r=>finish=r)})},context={window:{matchMedia:()=>({matches:reduced})},getComputedStyle:()=>({transform:'none'})};
  vm.runInNewContext(fs.readFileSync(__dirname+'/motion.js','utf8'),context);const m=context.window.ExcelLensMotion;
  const leaving=m.exit(node);const oldFinish=finish;m.enter(node);oldFinish();assert.equal(await leaving,false);assert.equal(cancelled,1);
  reduced=true;m.enter(node);assert.equal(cancelled,2);assert.equal(await m.exit(node),true);
});
