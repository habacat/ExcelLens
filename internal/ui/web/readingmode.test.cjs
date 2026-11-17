const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(saved='off',denied=false,texture=null){
  let focused=null;const events={},changes=[],textures=[],storage=new Map([['ExcelLens.readingPreset',saved],['ExcelLens.gridTexture',texture]]);
  function element(){return {hidden:true,style:{},attrs:{},handlers:{},classList:{toggle(){}},setAttribute(k,v){this.attrs[k]=v;},removeAttribute(k){delete this.attrs[k];},addEventListener(k,f){this.handlers[k]=f;},contains(n){return n===this;},focus(){focused=this;}};}
  const root=element(),button=element(),panel=element(),veil=element(),close=element(),textureToggle=element(),inputs=['off','warm','green','mist','blush','ice','lavender','graphite','navy','forest','warmdark','plum','brown'].map(value=>({...element(),value,name:'readingPreset'}));
  panel.querySelectorAll=()=>inputs;panel.querySelector=q=>q==='[data-reading-close]'?close:(q==='input:checked'?[textureToggle,...inputs]:inputs).find(i=>i.checked);panel.contains=n=>n===panel||n===close||n===textureToggle||inputs.includes(n);
  const context={window:{},Event:class Event{constructor(type){this.type=type;}},document:{documentElement:root,addEventListener:(name,fn)=>events[name]=fn,dispatchEvent:event=>changes.push({type:event.type,preset:root.attrs['data-reading-preset']||'off'})},localStorage:{getItem(key){if(denied)throw Error('blocked');return storage.get(key)??null;},setItem(key,value){if(denied)throw Error('blocked');storage.set(key,value);}}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/readingmode.js','utf8'),context);const mode=new context.window.ExcelLensReadingMode({button,panel,veil,textureToggle,onTextureChange:enabled=>textures.push(enabled)});
  return {mode,root,button,panel,veil,inputs,events,changes,textures,textureToggle,get stored(){return storage.get('ExcelLens.readingPreset');},get textureStored(){return storage.get('ExcelLens.gridTexture');},get focused(){return focused;}};
}
test('light presets theme the whole document, retain the source-color film, and restore original colors',()=>{
  const h=setup();assert.equal(h.veil.hidden,true);
  for(const [value,color]of [['warm','#f4eedf'],['green','#e5f0e4'],['mist','#e9edf2'],['blush','#f4e6e3'],['ice','#e3eef7'],['lavender','#eee8f4']]){h.mode.apply(value);assert.equal(h.root.attrs['data-reading-preset'],value);assert.equal(h.root.attrs['data-reading-tone'],'light');assert.equal(h.veil.hidden,false);assert.equal(h.veil.style.backgroundColor,color);assert.equal(h.stored,value);assert.equal(h.inputs.filter(i=>i.checked).length,1);}
  h.mode.apply('off');assert.equal(h.veil.hidden,true);assert.equal(h.veil.style.backgroundColor,'');
  assert.deepEqual(h.root.attrs,{});
});
test('dark presets restore from storage, theme cached nodes through the root, and never darken text with a film',()=>{
  const h=setup('navy');assert.equal(h.mode.value,'navy');assert.equal(h.root.attrs['data-reading-tone'],'dark');
  for(const value of ['graphite','forest','navy','warmdark','plum','brown']){h.mode.apply(value);assert.equal(h.root.attrs['data-reading-preset'],value);assert.equal(h.veil.hidden,true);assert.equal(h.veil.style.backgroundColor,'');assert.equal(h.inputs.filter(i=>i.checked).length,1);assert.equal(h.stored,value);}
  h.mode.apply('warm');assert.equal(h.root.attrs['data-reading-tone'],'light');assert.equal(h.veil.hidden,false);
  h.mode.apply('off');assert.deepEqual(h.root.attrs,{});
});
test('preset popup supports focus, Escape and outside clicks without stealing a grid click',()=>{
  const h=setup('green');h.mode.open();assert.equal(h.focused,h.inputs[2]);assert.equal(h.button.attrs['aria-expanded'],'true');
  h.events.pointerdown({target:{}});assert.equal(h.panel.hidden,true);assert.equal(h.focused,h.inputs[2]);
  h.mode.open();let stopped=false;h.panel.handlers.keydown({key:'Escape',preventDefault(){},stopPropagation(){stopped=true;}});assert(stopped);assert.equal(h.focused,h.button);
});
test('invalid or unavailable browser storage falls back safely and does not prevent choosing a preset',()=>{
  const invalid=setup('__proto__');assert.equal(invalid.mode.value,'off');assert.equal(invalid.veil.hidden,true);
  const denied=setup('mist',true);assert.equal(denied.mode.value,'off');denied.mode.apply('warm');assert.equal(denied.veil.hidden,false);assert.equal(denied.mode.value,'warm');
});

test('canvas repaint notification follows the applied palette and fires only when the preset changes',()=>{
  const h=setup('green');h.changes.length=0;
  h.mode.apply('graphite');h.mode.apply('graphite');h.mode.apply('off');
  assert.deepEqual(h.changes,[{type:'excellens:readingchange',preset:'graphite'},{type:'excellens:readingchange',preset:'off'}]);
});

test('worksheet texture defaults off and changes independently of reading colors and chart notifications',()=>{
  const h=setup('warm');assert.equal(h.mode.textureEnabled,false);assert.equal(h.textureToggle.checked,false);assert.deepEqual(h.textures,[]);
  h.changes.length=0;h.textureToggle.checked=true;h.panel.handlers.change({target:h.textureToggle});
  assert.equal(h.mode.textureEnabled,true);assert.equal(h.textureStored,'on');assert.equal(h.stored,'warm');assert.deepEqual(h.textures,[true]);assert.deepEqual(h.changes,[]);
  h.mode.apply('graphite');assert.equal(h.textureToggle.checked,true);assert.equal(h.textureStored,'on');
  h.changes.length=0;h.mode.applyTexture(false);h.mode.applyTexture(false);
  assert.equal(h.textureStored,'off');assert.equal(h.stored,'graphite');assert.equal(h.textureToggle.checked,false);assert.deepEqual(h.textures,[true,false]);assert.deepEqual(h.changes,[]);
});

test('texture preference restores independently, keeps radio focus, and tolerates invalid or blocked storage',()=>{
  const h=setup('navy',false,'on');assert.equal(h.mode.textureEnabled,true);assert.equal(h.mode.value,'navy');
  h.mode.open();assert.equal(h.focused,h.inputs.find(i=>i.value==='navy'));
  h.events.pointerdown({target:h.textureToggle});assert.equal(h.panel.hidden,false);
  h.mode.applyTexture(false);const restored=setup(h.stored,false,h.textureStored);assert.equal(restored.mode.textureEnabled,false);assert.equal(restored.mode.value,'navy');
  assert.equal(setup('warm',false,'invalid').mode.textureEnabled,false);
  const denied=setup('navy',true,'on');assert.equal(denied.mode.textureEnabled,false);denied.mode.applyTexture(true);assert.equal(denied.textureToggle.checked,true);assert.deepEqual(denied.textures,[true]);
});
