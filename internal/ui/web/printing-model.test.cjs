const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs'),path=require('node:path');
const context={window:{}};vm.runInNewContext(fs.readFileSync(path.join(__dirname,'printing-model.js'),'utf8'),context);const m=context.window.ExcelLensPrintingModel;
const limits={pages:20,rows:100,columns:20,cells:500,areas:4,parts:50,bytes:65536,requestBytes:8192,notices:32,domPixels:8000000,unitsPerPoint:1000000000,firstPageMin:-32768,firstPageMax:4294967295};
const source={maxRows:65536,maxColumns:256},binding={workbookKey:'content',book:'path-one',sheet:'明细'},id='print-job';
const plain=x=>JSON.parse(JSON.stringify(x)),input=()=>({defaults:true,paper:'source',width:'210',height:'297',orientation:'',scaleMode:'source',percent:'100',fitWidth:'1',fitHeight:'0',area:'',repeatRows:'',repeatColumns:'',marginMode:'source',left:'20',right:'20',top:'20',bottom:'20',header:'8',footer:'8',pageOrder:'',firstPage:'',rowHeight:'',columnWidth:''});
function fixture(){
  const s={id,...binding,sourceComplete:true,sourceProperties:{},sourceNotices:[],summary:{sourceId:'a'.repeat(64),sheet:binding.sheet,pages:1,paper:{width:'100000000000',height:'150000000000'},margins:{left:'0',right:'0',top:'0',bottom:'0',header:'0',footer:'0'},headings:{width:'0',height:'0'},scale:{numerator:'1',denominator:'2',percent:'50'},areas:[{row:1,col:1,endRow:1,endCol:1}],firstPageNumber:1,origins:{paper:'source'},notices:[]}};
  const p={id,...binding,page:0,content:{page:{number:1,area:1,offsetX:'0',offsetY:'0',content:{width:'15000000000',height:'7500000000'},body:{width:'15000000000',height:'7500000000'},rows:[{first:1,last:1,offset:'0',length:'15000000000',repeated:false}],columns:[{first:1,last:1,offset:'0',length:'30000000000',repeated:false}]},rows:[{number:1,part:0,start:'0',size:'15000000000',clipStart:'0',clipSize:'15000000000',repeated:false}],columns:[{number:1,part:0,start:'0',size:'30000000000',clipStart:'0',clipSize:'30000000000',repeated:false}],cells:[{row:1,col:1,ref:'A1',style:0,cell:{ref:'A1',col:1,raw:'001',display:'001',hasFormula:false,style:0,type:'inlineStr'},box:{x:'0',y:'0',width:'30000000000',height:'15000000000'},clip:{x:'0',y:'0',width:'30000000000',height:'15000000000'}}],styles:{0:{}},notices:[]}};
  return {s,p};
}
test('source settings stay absent until explicit overrides; source coordinates and empty title lists are distinct',()=>{
  assert.deepEqual(plain(m.options(input(),source,limits)),{usePreviewDefaults:true});
  const i={...input(),paper:'custom',width:'215.9',height:'279.4',orientation:'landscape',scaleMode:'fit',fitWidth:'1',fitHeight:'0',area:'$A$1:$B$4，C8',repeatRows:'1:2,5',repeatColumns:'$A:$B',marginMode:'custom',left:'0',clearRepeatColumns:true,firstPage:'-1',rowHeight:'15.25',columnWidth:'12.5'};
  const out=plain(m.options(i,source,limits));
  assert.deepEqual(out.paper,{width:'215.9mm',height:'279.4mm'});assert.deepEqual(out.fit,{width:1,height:0});assert.deepEqual(out.repeatColumns,[]);
  assert.deepEqual(out.repeatRows,[{first:1,last:2},{first:5,last:5}]);assert.deepEqual(out.areas,[{row:1,col:1,endRow:4,endCol:2},{row:8,col:3,endRow:8,endCol:3}]);
  assert.equal(out.margins.left,'0mm');assert.equal(out.firstPageNumber,-1);assert.equal(out.missingRowHeight,'15.25');assert.equal(out.missingColumnWidth,'12.5');
});
test('user ranges are bounded by the supplied source registry and do not silently reverse or truncate',()=>{
  for(const area of ['A0','A1:IV65537','IW1','B2:A1','A1,,B2','Other!A1','A1:B2:C3'])assert.throws(()=>m.options({...input(),area},source,limits));
  for(const override of [{percent:'0',scaleMode:'percent'},{percent:'401',scaleMode:'percent'},{paper:'custom',width:'NaN'},{scaleMode:'fit',fitWidth:'0',fitHeight:'0'},{repeatRows:'2:1'},{repeatColumns:'IW'},{firstPage:'4294967296'}])assert.throws(()=>m.options({...input(),...override},source,limits));
  assert.equal(m.options({...input(),area:'IW1'},{maxRows:1000000,maxColumns:300},limits).areas[0].col,257);
});
test('an accepted page retains source text/type and exact nanopts while projecting only final CSS geometry',()=>{
  const {s,p}=fixture(),before=JSON.stringify(p);m.summary(s,binding,id,limits,source);m.page(p,binding,id,0,s,limits,source);
  const projection=m.project(s,p,limits);assert.equal(projection.cells[0].width,20);assert.equal(projection.cells[0].height,10);assert.equal(projection.width,133.333333);
  assert.equal(JSON.stringify(p),before);assert.equal(p.content.cells[0].cell.raw,'001');assert.equal(m.units('9007199254740993'),9007199254740993n);
  const first=m.units('9007199254740990000'),last=first+2250000000n;assert.equal(m.pixels(last-first,limits),3);
});
test('source/path/task and physical coverage mismatches fail before adoption',()=>{
  for(const change of [p=>p.book='another-path',p=>p.id='another-job',p=>p.content.cells[0].cell.ref='B1',p=>p.content.cells[0].clip.width='30000000001',p=>p.content.rows[0].clipStart='1',p=>p.content.rows[0].number=65537,p=>p.content.page.number=2,p=>p.content.columns[0].part=2]){const {s,p}=fixture();change(p);assert.throws(()=>m.page(p,binding,id,0,s,limits,source));}
  for(const change of [s=>s.summary.scale.denominator='0',s=>s.summary.scale.numerator='9',s=>s.sourceComplete='yes',s=>s.summary.sourceId='not-an-identity',s=>s.summary.paper.width='1e9']){const {s}=fixture();change(s);assert.throws(()=>m.summary(s,binding,id,limits,source));}
});
test('oversized source boxes stay intact; an unrenderable browser layout is an explicit error',()=>{
  const {s,p}=fixture();p.content.cells[0].box.height='9223372036854775807';p.content.rows[0].size='9223372036854775807';
  m.page(p,binding,id,0,s,limits,source);assert.throws(()=>m.project(s,p,limits),/浏览器预览容量/);assert.equal(p.content.cells[0].box.height,'9223372036854775807');
  for(const value of ['01','NaN','-0','9223372036854775808',1,1n])assert.throws(()=>m.units(value));
});
test('an empty preview has no invented A1 page or fabricated scale',()=>{
  const {s}=fixture();Object.assign(s.summary,{pages:0,areas:null,scale:{numerator:'',denominator:'',percent:''},paper:{width:'0',height:'0'}});assert.equal(m.summary(s,binding,id,limits,source),s);assert.throws(()=>m.page(fixture().p,binding,id,0,s,limits,source));
});
module.exports={fixture,limits,source,binding,id,input};
