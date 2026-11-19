const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const read=name=>fs.readFileSync(__dirname+'/'+name,'utf8');
function setup(){
  const measured=[];
  class Element{
    constructor(tag='div',text=''){this.tagName=tag.toUpperCase();this.nodeType=tag==='#text'?3:1;this.data=text;this.dataset={};this.childNodes=[];this.attrs={};const props=new Map();this.style={setProperty:(k,v)=>props.set(k,v),getPropertyValue:k=>props.get(k)||'',removeProperty:k=>props.delete(k)};this.classes=new Set();this.classList={add:(...v)=>v.forEach(x=>this.classes.add(x)),remove:(...v)=>v.forEach(x=>this.classes.delete(x)),contains:x=>this.classes.has(x)};}
    set className(v){this.classes=new Set(v.split(' '));}get className(){return [...this.classes].join(' ');}
    set textContent(v){if(this.nodeType===3){this.data=String(v);return;}this.childNodes=[];if(v!=='')this.appendChild(new Element('#text',String(v)));}
    get textContent(){return this.nodeType===3?this.data:this.childNodes.map(x=>x.textContent).join('');}
    appendChild(n){this.childNodes.push(n);n.parent=this;return n;}prepend(n){this.childNodes.unshift(n);n.parent=this;}
    replaceWith(n){const at=this.parent.childNodes.indexOf(this);this.parent.childNodes.splice(at,1,n);n.parent=this.parent;}
    setAttribute(k,v){this.attrs[k]=v;}getContext(){return {measureText:s=>{measured.push(s.length);return {width:s.length*10};}};}
  }
  const document={createElement:tag=>new Element(tag),createTextNode:s=>new Element('#text',s)};
  const sandbox={window:{},document};vm.runInNewContext(read('conditional.js'),sandbox);vm.runInNewContext(read('richtext.js'),sandbox);vm.runInNewContext(read('cellview.js'),sandbox);vm.runInNewContext(read('cell-address.js'),sandbox);vm.runInNewContext(read('wire.js'),sandbox);
  const state={showFormulas:false,showStyles:true,styles:[{}],zoom:100,indexReady:true,source:{format:'xlsx'},frozenCellMap:new Map()};
  const options={state,applyStyle:(node,c)=>{node.appliedStyle=c.style||0;},colName:c=>String.fromCharCode(c+64),scale:()=>1,fontStack:s=>s,merge:()=>null,richLimits:{cellRuns:4096,viewRuns:8192}};
  const presenter=new sandbox.window.ExcelLensCellView.Presenter(options);
  const node=()=>{const n=document.createElement('div');n.className='grid-cell';n.dataset={row:'2',col:'1',ref:'A2'};n.style.width='100px';return n;};
  const layouts=[1,2,3,4].map(col=>({col,width:100,left:(col-1)*100,letter:String.fromCharCode(col+64)}));
  return {sandbox,document,state,presenter,options,node,layouts,measured};
}
const long='审计财务说明'.repeat(8);

test('source numeric overflow shows hashes instead of a misleading suffix and restores exact spans when widened',()=>{
  const h=setup(),cell={col:1,raw:'9007199254740993.31',display:'9,007,199,254,740,993.31',type:'n',style:0},before=JSON.stringify(cell),node=h.node();
  h.presenter.paint(node,cell,{});const original=node.childNodes[0];assert.equal(h.presenter.fitNumber(node,cell,100,14),true);assert.match(node.textContent,/^#+$/);assert.match(node.title,/9,007,199,254,740,993.31/);assert.equal(node._xlModel,cell);assert.equal(JSON.stringify(cell),before);
  const measurements=h.measured.length;h.presenter.fitNumber(node,cell,110,14);assert.equal(h.measured.length,measurements);
  const duplicate={...cell},other=h.node();h.presenter.paint(other,duplicate,{});h.presenter.fitNumber(other,duplicate,100,14);assert.equal(h.measured.length,measurements,'equal value/font remeasured across different source cells');
  assert.equal(h.presenter.fitNumber(node,cell,400,14),false);assert.equal(node.childNodes[0],original);assert.equal(node.textContent,cell.display);assert.equal(node.title,'');
});
test('width markers respect source text/boolean/formula-cache semantics and preserve numeric date rich spans',()=>{
  const h=setup(),node=h.node();
  for(const cell of [{raw:'000000000000001',display:'000000000000001',type:'inlineStr'},{raw:'1',display:'TRUE',type:'b'},{raw:'',display:'',hasFormula:true,cacheMissing:true}]){h.presenter.paint(node,cell,{});assert.equal(h.presenter.fitNumber(node,cell,10,0),false);assert.equal(node.textContent,cell.display);}
  const cell={raw:'45292',display:'2024-01-01',type:'n',conditional:{style:{font:{vertical:'superscript'}},visual:{fontSize:12}}};h.presenter.prepare([{cells:[cell]}]);h.presenter.paint(node,cell,{});const rich=node._xlRichNode;assert(h.presenter.fitNumber(node,cell,20,0));assert(h.presenter.fitNumber(node,cell,400,0)===false);assert.equal(node._xlRichNode,rich);assert.equal(node.textContent,cell.display);
  h.presenter.fitNumber(node,cell,20,0);h.presenter.paint(node,{raw:'短文',display:'短文',type:'inlineStr'},{});assert.equal(node.textContent,'短文');assert(!node.title.includes('宽度不足'));
});

test('numeric fitting uses inherited row wrapping and the full merged width without changing source values',()=>{
  const h=setup(),cell={raw:'1234567890',display:'1,234,567,890'},node=h.node();h.state.styles[1]={wrap:true};
  h.presenter.paint(node,cell,{style:1});h.presenter.overflow(node,cell,{1:cell},h.layouts,0);assert.equal(node.textContent,cell.display);assert.equal(h.measured.length,0);
  h.presenter.paint(node,{...cell,style:0},{style:1});h.presenter.overflow(node,node._xlModel,{1:cell},h.layouts,0);assert.match(node.textContent,/^#+$/);
  h.options.merge=()=>({row:2,col:1,endRow:2,endCol:3});node.style.width='300px';h.presenter.overflow(node,node._xlModel,{1:cell},h.layouts,0);assert.equal(node.textContent,cell.display);assert.equal(node._xlOverflowSpan,undefined);assert.equal(cell.raw,'1234567890');
});

test('ordinary source XF fonts retain superscripts and double underlines without a live condition',()=>{
  const h=setup(),cell={col:1,raw:'中😀',display:'中😀',style:0},node=h.node();h.state.styles[0]={fontSize:12,fontVertical:'superscript',underline:true,underlineStyle:'double'};
  h.presenter.prepare([{cells:[cell]}]);h.presenter.paint(node,cell,{});const span=node._xlRichNode.childNodes[0];assert.equal(span.textContent,cell.raw);assert.equal(span.style.getPropertyValue('vertical-align'),'super');assert.equal(span.style.getPropertyValue('text-decoration-style'),'double');assert.equal(span.style.getPropertyValue('font-size'),'10.4px');
  const explicit={...cell,richText:{runs:[{end:3,font:{vertical:'baseline',underline:'none'}}]}};h.presenter.prepare([{cells:[explicit]}]);h.presenter.paint(node,explicit,{});assert.equal(node._xlRichNode.childNodes[0].style.getPropertyValue('vertical-align'),'baseline');assert.equal(node._xlRichNode.childNodes[0].style.getPropertyValue('text-decoration-line'),'none');
});

test('conditional font properties override rich runs and derived display never replaces source evidence',()=>{
  const h=setup(),text='甲😀乙',cell={col:1,raw:text,display:text,type:'inlineStr',style:0,richText:{runs:[{end:3,font:{bold:true,color:'#112233',underline:'single'}},{end:4,font:{italic:true}}]},conditional:{style:{font:{bold:false,color:'#FF0033',underline:'none'}},visual:{bold:false,color:'#FF0033',fontSize:11},matched:1}};
  const before=JSON.stringify(cell),node=h.node();h.presenter.prepare([{cells:[cell]}]);h.presenter.paint(node,cell,{});
  assert.equal(node.textContent,text);for(const span of node._xlRichNode.childNodes){assert.equal(span.style.getPropertyValue('font-weight'),'400');assert.equal(span.style.getPropertyValue('--rich-color'),'#FF0033');assert.equal(span.style.getPropertyValue('text-decoration-line'),'none');}
  assert.equal(JSON.stringify(cell),before);assert.equal(h.presenter.style(cell).color,'#FF0033');
  const numeric={raw:'9007199254740993.31',display:'source display',conditional:{display:'derived display',style:{font:{}}}};
  assert.equal(h.presenter.value(numeric),'derived display');h.state.showStyles=false;assert.equal(h.presenter.value(numeric),'source display');h.state.showStyles=true;
  h.presenter.paint(node,cell,{presentationNotice:'条件格式读取超限'});assert.equal(node.title,'条件格式读取超限');assert(node.classList.contains('cell-presentation-notice'));
  assert(h.presenter.needsPaint(node,cell,{}));h.presenter.paint(node,{raw:'plain'},{});assert.equal(node.title,'');assert(!node.classList.contains('cell-presentation-notice'));
});

test('actual style painter clears recycled conditional fills, font flags and number-format color',()=>{
  const h=setup(),app=read('app.js'),start=app.indexOf('  function applyCellStyle('),end=app.indexOf('  // Each seam',start);
  Object.assign(h.sandbox,{S:h.state,cellPresenter:h.presenter,highlightMode:null,STYLE_ALIGN:{left:'flex-start'},STYLE_VERTICAL:{top:'flex-start'},applyDisplayFont:(node,name)=>{node.style.fontFamily=name;}});
  vm.runInNewContext(app.slice(start,end)+';this.paintStyle=applyCellStyle;',h.sandbox);h.options.applyStyle=h.sandbox.paintStyle;
  const node=h.node(),on={raw:'1',display:'1',style:0,valueColor:'#008800',conditional:{style:{font:{}},visual:{bold:true,italic:true,background:'#FFFF00',color:'#FF0033'},valueColor:''}};
  h.presenter.paint(node,on,{});assert.equal(node.style.fontWeight,'700');assert.equal(node.style.getPropertyValue('--xl-color'),'#FF0033');assert.equal(node.style.getPropertyValue('--xl-fill'),'#FFFF00');
  h.presenter.paint(node,{raw:'2',display:'2',style:0},{});assert.equal(node.style.fontWeight,'');assert.equal(node.style.fontStyle,'');assert.equal(node.style.getPropertyValue('--xl-fill'),'');assert.equal(node.style.getPropertyValue('--xl-color'),'');assert.equal(node.style.width,'100px');
});

test('plain conditional superscripts share span budget and full text survives a fallback',()=>{
  const h=setup(),cell={raw:'中😀',display:'中😀',conditional:{style:{font:{vertical:'superscript'}},visual:{fontSize:12}}},node=h.node();
  h.presenter.prepare([{cells:[cell]}]);h.presenter.paint(node,cell,{});assert.equal(node._xlRichNode.childNodes[0].style.getPropertyValue('vertical-align'),'super');assert.equal(node.textContent,cell.raw);
  h.options.richLimits={cellRuns:4096,viewRuns:1};h.presenter.prepare([{cells:[cell]}]);h.presenter.paint(node,cell,{});assert.equal(node._xlRichNode,null);assert.equal(node.textContent,cell.raw);assert.match(node.title,/容量/);
});

test('range tint remains distinct on colored fills while retaining source text contrast',()=>{
  const {sandbox}=setup(),fill=sandbox.window.ExcelLensCellView.selectionFill;
  vm.runInNewContext(read('highlightmode.js'),sandbox);
  const rgb=hex=>[0,2,4].map(i=>parseInt(hex.slice(1+i,3+i),16));
  const luminance=c=>c.reduce((n,v,i)=>{v/=255;return n+[.2126,.7152,.0722][i]*(v<=.04045?v/12.92:((v+.055)/1.055)**2.4);},0);
  const contrast=(a,b)=>(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
  for(const palette of [undefined,...Object.values(sandbox.window.ExcelLensHighlightMode.palettes)])for(const [background,foreground]of [['#ffffff','#304355'],['#2f79cf','#ffffff'],['#fff176','#304355'],['#008000','#ffffff'],['#000000','#ffffff'],['#dbe8f6','#304355'],['#e53935','#ffffff']]){
    const base=rgb(background),text=rgb(foreground),parts=fill(background,foreground,palette).match(/[\d.]+/g).map(Number),alpha=parts[3];
    const painted=base.map((c,i)=>c*(1-alpha)+parts[i]*alpha);
    assert(painted.reduce((n,c,i)=>n+Math.abs(c-base[i]),0)/3>=20,background+' did not show the range');
    const original=contrast(luminance(base),luminance(text)),selected=contrast(luminance(painted),luminance(text));
    assert(selected>=Math.min(4.5,original*.9)-.01,background+' lost text contrast');
  }
});
test('XLSX, BIFF XLS, HTML XLS, TXT and CSV use identical text overflow rules',()=>{
  const h=setup();let expected;
  for(const [format,type] of [['xlsx','s'],['xlsx','inlineStr'],['xls','inlineStr'],['xls-html','inlineStr'],['txt','inlineStr'],['csv','inlineStr'],['txt',undefined],['txt','text']]){
    h.state.source.format=format;const cell={col:1,raw:long,display:long,type},node=h.node();h.presenter.paint(node,cell,{});h.presenter.overflow(node,cell,{1:cell},h.layouts,0);
    assert(node.classList.contains('cell-overflow'),format+'/'+type);const width=node.style.getPropertyValue('--overflow-width');expected??=width;assert.equal(width,expected);assert.equal(node.style.width,'100px');assert.equal(cell.raw,long);assert.equal(node._xlOverflowSpan.textContent,long);
  }
});
test('the first actual value blocks spilling, while a style-only empty cell does not',()=>{
  const h=setup(),cell={type:'inlineStr',raw:long,display:long};
  for(const occupied of [{raw:'0',type:'n'},{raw:' '},{display:'',raw:'',hasFormula:true},{display:'x'}]){
    assert.equal(h.presenter.spillWidth(cell,{2:occupied},h.layouts,0),0);
    assert.equal(h.presenter.spillWidth(cell,{3:occupied},h.layouts,0),194);
  }
  assert(h.presenter.spillWidth(cell,{2:{raw:'',display:'',style:5}},h.layouts,0)>200);
});
test('numbers, errors and explicit wrapping/alignment follow the same rules in every format',()=>{
  const h=setup();
  for(const format of ['xlsx','xls','xls-html','txt']){
    h.state.source.format=format;
    for(const type of ['n','e','b'])assert.equal(h.presenter.spillWidth({raw:long,display:long,type},{},h.layouts,0),0);
    for(const style of [{wrap:true},{horizontal:'right'},{horizontal:'center'},{horizontal:'fill'}]){
      h.state.styles[0]=style;assert.equal(h.presenter.spillWidth({raw:long,type:'inlineStr'},{},h.layouts,0),0);
    }
    h.state.showStyles=false;assert(h.presenter.spillWidth({raw:long,type:'inlineStr'},{},h.layouts,0)>0);h.state.showStyles=true;
  }
});
test('very long TXT values spill without measuring or copying the entire string',()=>{
  const h=setup(),cell={raw:'长'.repeat(100000),type:'inlineStr'},node=h.node();h.presenter.paint(node,cell,{});h.presenter.overflow(node,cell,{},h.layouts,0);
  assert.equal(node._xlOverflowSpan.textContent.length,100000);assert.equal(node.style.getPropertyValue('--overflow-width'),'394px');assert(h.measured.every(n=>n<=2048));
});
test('hidden columns are skipped and frozen-column boundaries remain clipped',()=>{
  const h=setup(),cell={raw:long,type:'inlineStr'},columns=[h.layouts[0],h.layouts[2],h.layouts[3]];
  assert(h.presenter.spillWidth(cell,{2:{raw:'hidden'}},columns,0)>100);assert.equal(h.presenter.spillWidth(cell,{},[h.layouts[0]],0),0);
  h.options.merge=()=>({row:2,col:1,endCol:3});const node=h.node();h.presenter.paint(node,cell,{});h.presenter.overflow(node,cell,{},h.layouts,0);assert(!node._xlOverflowSpan);
});
test('full and compact transports preserve the same display value and blank semantics',()=>{
  const h=setup(),full={col:1,raw:long,display:long,type:'inlineStr'};
  const compact=h.sandbox.window.ExcelLensWire.unpack([{n:2,c:[{c:1,v:long,t:'inlineStr'}]}])[0].cells[0];
  assert.equal(h.presenter.spillWidth(full,{},h.layouts,0),h.presenter.spillWidth(compact,{},h.layouts,0));
  assert.equal(h.sandbox.window.ExcelLensCellView.value({raw:'raw only'}),'raw only');assert.equal(h.sandbox.window.ExcelLensCellView.value({raw:'value',display:''}),'');
});
test('reused cells remove obsolete overflow without removing filter controls',()=>{
  const h=setup(),cell={raw:long,type:'inlineStr'},node=h.node();h.state.autoFilter={row:2,startCol:1,endCol:4,rules:new Map()};
  h.presenter.paint(node,cell,{});h.presenter.overflow(node,cell,{},h.layouts,0);const button=node.childNodes.find(n=>n.tagName==='BUTTON');assert(button);
  h.presenter.overflow(node,cell,{2:{raw:'block'}},h.layouts,0);assert.equal(node._xlOverflowSpan,null);assert(node.childNodes.includes(button));assert.equal(node.childNodes[0].textContent,long);
});
test('the actual main and frozen cell factories share text, styles, filters and spill painting',()=>{
  const h=setup(),cell={col:1,ref:'A2',raw:long,display:long,type:'inlineStr'},row={number:2,cells:[cell]},byColumn={1:cell};
  const app=read('app.js'),a=app.indexOf('  function makeGridCell('),b=app.indexOf('  // Only resolved,',a);
  const sandbox={...h.sandbox,cellPresenter:h.presenter};vm.runInNewContext(app.slice(a,b)+';this.make=makeGridCell;',sandbox);
  const main=sandbox.make(cell,row,2,2,h.layouts[0],byColumn,h.layouts,0);h.presenter.overflow(main,cell,byColumn,h.layouts,0);
  const frozen=read('freeze.js'),start=frozen.indexOf('      const make = (data,'),end=frozen.indexOf('      const head =',start);
  Object.assign(sandbox,{s:h.state,cellByColumn:data=>data?Object.fromEntries(data.cells.map(c=>[c.col,c])):null,colWidth:()=>100,rowHeight:()=>25,topLayouts:h.layouts,pinnedLayouts:h.layouts,topPositions:new Map([[1,0]]),pinnedPositions:new Map([[1,0]]),renderer:{o:{paintCell:h.presenter.paint.bind(h.presenter),extendTextIntoBlanks:h.presenter.overflow.bind(h.presenter),colName:c=>String.fromCharCode(64+c),updateSharedBorders(){},isPrecedent:()=>false,isSearchMatch:()=>false}},fixture:row});
  sandbox.sourceHeight=()=>25;
  Object.assign(sandbox,{borderRows:new Map([[2,row]]),borderColumns:h.layouts.map(c=>c.col),nr:2,sourceTop:r=>(r-1)*25});
  const pinned=vm.runInNewContext('(function(){'+frozen.slice(start,end)+'return make(fixture,1,2,2,0,0);}).call(renderer)',sandbox);
  assert.equal(main._xlOverflowSpan.textContent,pinned._xlOverflowSpan.textContent);assert.equal(main.style.getPropertyValue('--overflow-width'),pinned.style.getPropertyValue('--overflow-width'));assert.equal(main.appliedStyle,pinned.appliedStyle);
});
test('row style inheritance and merged destinations do not diverge by source format',()=>{
  const h=setup(),cell={type:'inlineStr',raw:long},node=h.node();h.state.styles[1]={wrap:true};
  h.presenter.paint(node,cell,{style:1});h.presenter.overflow(node,cell,{},h.layouts,0);assert.equal(node.appliedStyle,1);assert(!node._xlOverflowSpan);
  h.presenter.paint(node,{...cell,style:0},{style:1});h.presenter.overflow(node,{...cell,style:0},{},h.layouts,0);assert(node._xlOverflowSpan);
  h.options.merge=(row,col)=>col===2?{row,col,endCol:3}:null;h.presenter.overflow(node,cell,{},h.layouts,0);assert.equal(node._xlOverflowSpan,null);
});

test('rich text keeps Unicode, explicit false and inherited decorations through overflow and reuse',()=>{
  const h=setup(),text='中😀'+long,cell={type:'inlineStr',raw:text,display:text,richText:{runs:[{end:3,font:{bold:false,underline:'none',strike:false}},{end:text.length,font:{bold:true,color:'#1255AA',vertical:'superscript'}}]}};
  h.state.styles[0]={bold:true,underline:true,strike:true,fontSize:11};const node=h.node();
  h.presenter.prepare([{cells:[cell]}]);h.presenter.paint(node,cell,{});const root=node._xlRichNode;
  assert.equal(root.childNodes[0].textContent,'中😀');assert.equal(root.childNodes[0].style.getPropertyValue('font-weight'),'400');
  assert.equal(root.childNodes[0].style.getPropertyValue('text-decoration-line'),'none');assert.equal(node.style.textDecoration,'none');
  assert.equal(root.childNodes[1].style.getPropertyValue('--rich-color'),'#1255AA');assert.equal(root.childNodes[1].style.getPropertyValue('vertical-align'),'super');
  h.presenter.overflow(node,cell,{},h.layouts,0);assert.equal(node.textContent,text);assert.equal(node._xlOverflowSpan.childNodes[0],root);
  h.presenter.overflow(node,cell,{},h.layouts,0);assert.equal(node.textContent,text);assert.equal(root.childNodes.length,2);
  h.presenter.overflow(node,cell,{2:{raw:'block'}},h.layouts,0);assert.equal(node._xlOverflowSpan,null);assert.equal(node.childNodes[0],root);assert.equal(node.textContent,text);
  h.presenter.paint(node,{raw:'plain',display:'plain'},{});assert.equal(node._xlRichNode,null);assert.equal(node.textContent,'plain');
});

test('format, formula, font and scale changes invalidate previously painted rich text',()=>{
  const h=setup(),cell={raw:'中😀',display:'中😀',type:'inlineStr',hasFormula:true,formula:'="中😀"',richText:{runs:[{end:3,font:{name:'宋体',size:12}}]}},node=h.node();
  h.presenter.paint(node,cell,{});assert(node._xlRichNode);assert(!h.presenter.needsPaint(node,cell));
  h.state.fontOverride='Arial';assert(h.presenter.needsPaint(node,cell));h.presenter.paint(node,cell,{});assert.equal(node._xlRichNode.childNodes[0].style.getPropertyValue('font-family'),'Arial');
  h.options.scale=()=>1.5;assert(h.presenter.needsPaint(node,cell));h.presenter.paint(node,cell,{});assert.equal(node._xlRichNode.childNodes[0].style.getPropertyValue('font-size'),'24px');
  h.state.showFormulas=true;h.presenter.paint(node,cell,{});assert.equal(node._xlRichNode,null);assert.equal(node.textContent,cell.formula);
  h.state.showStyles=false;h.state.showFormulas=false;h.presenter.paint(node,cell,{});assert.equal(node._xlRichNode,null);assert.equal(node.textContent,cell.raw);
});

test('viewport span budget falls back truthfully without truncating text and can recover on another tile',()=>{
  const h=setup(),cell=()=>({raw:'甲乙',type:'inlineStr',richText:{runs:[{end:1,font:{bold:true}},{end:2,font:{italic:true}}]}}),a=cell(),b=cell();
  h.options.richLimits.viewRuns=3;h.presenter.prepare([{cells:[a,b]}]);const node=h.node();h.presenter.paint(node,b,{});
  assert.equal(node._xlRichNode,null);assert.equal(node.textContent,b.raw);assert.match(node.title,/视口容量/);assert.equal(h.presenter.allowed(a),true);
  h.presenter.prepare([{cells:[b]}]);assert(h.presenter.needsPaint(node,b));h.presenter.paint(node,b,{});assert.equal(node.textContent,b.raw);assert(node._xlRichNode);assert.equal(node.title,'');
});
