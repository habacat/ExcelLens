const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){
  class Element{
    constructor(){this.children=[];this.hidden=false;this.open=false;this.checked=true;this.handlers={};this.textContent='';}
    append(...children){this.children.push(...children);}replaceChildren(...children){this.children=children;}
    addEventListener(name,fn){this.handlers[name]=fn;}showModal(){this.open=true;}close(){this.open=false;}
  }
  const nodes=new Map(),node=id=>{if(!nodes.has(id))nodes.set(id,new Element());return nodes.get(id);};
  const ctx={window:{},document:{getElementById:node,createElement:()=>new Element()}};vm.runInNewContext(fs.readFileSync(__dirname+'/cell-address.js','utf8'),ctx);vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-issues.js','utf8'),ctx);
  let book='one';const navigations=[],calls=[],messages=[],exports=[],saved=[];
  const panel=new ctx.window.ExcelLensAnalysisIssues({identity:()=>book,api:async(path,opts)=>{calls.push({path,body:JSON.parse(opts.body)});return {id:'new',rows:10000,total:15000,truncated:true};},toast:(...args)=>messages.push(args),navigate:(...args)=>navigations.push(args),askName:async()=> '核对表',onMaterialized:async data=>saved.push(data),startExport:(...args)=>exports.push(args)});
  const snapshot={book,job:'job',query:{source:'wb:数据'},result:{invalidNumbers:15000,issuesTruncated:true,issues:[{row:103,sourceColumn:28,sourceField:'C28',field:'D_x',fieldName:'转换金额',kind:'number',original:'0001',input:'0001',value:'0001',reason:'源值为文本'}]}};
  panel.setSnapshot(snapshot);return {panel,node,snapshot,navigations,calls,messages,exports,saved,setBook:value=>book=value};
}
test('issue samples retain source coordinates, show incomplete counts and do not start a full scan',()=>{
  const h=setup();h.panel.open();assert.equal(h.node('analysisIssuesDialog').open,true);assert.match(h.node('analysisIssuesSummary').textContent,/15,000.*1.*样例/);assert.equal(h.calls.length,0);
  const jump=h.node('analysisIssuesTable').children[1].children[0].children[0].children[0];assert.equal(jump.textContent,'AB103');jump.onclick();assert.equal(h.navigations[0][2],'AB103');assert.equal(h.node('analysisIssuesDialog').open,false);
});
test('complete issue export uses the shared export center with an explicit CSV text choice',()=>{
  const h=setup();h.panel.open();h.node('analysisIssuesCSVGuard').checked=false;h.node('analysisIssuesExport').handlers.click();assert.deepEqual(h.exports,[['job',false]]);assert.equal(h.calls.length,0);assert.equal(h.node('analysisIssuesDialog').open,false);
});
test('bounded issue extraction reports truncation and never installs a different workbook result',async()=>{
  const h=setup();await h.panel.extract();assert.equal(h.calls[0].body.jobId,'job');assert.equal(h.calls[0].body.limit,10000);assert.equal(h.saved[0].id,'new');assert.match(h.messages[0][0],/容量上限/);
  let release;h.panel.askName=()=>new Promise(resolve=>release=resolve);const waiting=h.panel.extract();h.setBook('two');release('new');await waiting;assert.equal(h.calls.length,1);
});
