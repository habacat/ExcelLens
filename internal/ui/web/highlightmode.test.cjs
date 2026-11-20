const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(saved={},denied=false){
  const storage={...saved},events={};let focused=null;
  const element=()=>({hidden:true,checked:false,attrs:{},handlers:{},style:{setProperty(k,v){this[k]=v;}},setAttribute(k,v){this.attrs[k]=v;},addEventListener(k,f){this.handlers[k]=f;},contains(n){return n===this;},focus(){focused=this;}});
  const root=element(),toggle=element(),button=element(),panel=element(),close=element(),inputs=['blue','green','amber','purple','rose'].map(value=>({...element(),value,name:'crosshairColor'}));
  panel.querySelectorAll=()=>inputs;panel.querySelector=q=>q==='[data-highlight-close]'?close:inputs.find(i=>i.checked);panel.contains=n=>n===panel||n===close||inputs.includes(n);
  const context={window:{},document:{documentElement:root,addEventListener:(k,f)=>events[k]=f},localStorage:{getItem(k){if(denied)throw Error('blocked');return storage[k]??null;},setItem(k,v){if(denied)throw Error('blocked');storage[k]=v;}}};
  vm.runInNewContext(fs.readFileSync(__dirname+'/highlightmode.js','utf8'),context);
  let changed=0;const mode=new context.window.ExcelLensHighlightMode({toggle,button,panel,onChange:()=>changed++});return {mode,root,toggle,button,panel,inputs,storage,events,get changed(){return changed;},get focused(){return focused;}};
}
test('crosshair is enabled by default and toggling it preserves the chosen color',()=>{
  const h=setup();assert.equal(h.toggle.checked,true);assert.equal(h.mode.color,'green');
  for(const [key,color]of [['green','#109968'],['amber','#b77a19'],['purple','#8654b6'],['rose','#b95179']]){h.mode.apply(true,key);assert.equal(h.root.style['--crosshair-color'],color);assert.equal(h.root.style['--selection-color'],color);assert.equal(h.inputs.filter(i=>i.checked).length,1);}
  assert.equal(h.changed,4);
  h.toggle.checked=false;h.toggle.handlers.change();assert.equal(h.root.attrs['data-crosshair-enabled'],'false');assert.equal(h.mode.color,'rose');assert.equal(h.storage['ExcelLens.crosshairEnabled'],'false');
  h.toggle.checked=true;h.toggle.handlers.change();assert.equal(h.root.attrs['data-crosshair-enabled'],'true');assert.equal(h.mode.color,'rose');
});
test('saved crosshair settings restore without altering the page theme and tolerate storage denial',()=>{
  const h=setup({'ExcelLens.crosshairEnabled':'false','ExcelLens.crosshairColor':'purple'});assert.equal(h.toggle.checked,false);assert.equal(h.mode.color,'purple');assert.equal(h.root.attrs['data-reading-preset'],undefined);
  const invalid=setup({'ExcelLens.crosshairColor':'__proto__'});assert.equal(invalid.mode.color,'green');
  const denied=setup({},true);denied.mode.apply(false,'amber');assert.equal(denied.mode.color,'amber');assert.equal(denied.toggle.checked,false);
});
test('color popup supports keyboard and outside closing while retaining grid click focus',()=>{
  const h=setup();h.button.handlers.click();assert.equal(h.panel.hidden,false);assert.equal(h.focused,h.inputs[1]);
  h.events.pointerdown({target:{}});assert.equal(h.panel.hidden,true);assert.equal(h.focused,h.inputs[1]);
  h.button.handlers.click();h.panel.handlers.keydown({key:'Escape',preventDefault(){},stopPropagation(){}});assert.equal(h.panel.hidden,true);assert.equal(h.focused,h.button);
});
