const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){
  class Element {
    constructor(tag='div'){this.tagName=tag;this.children=[];this.dataset={};this.attrs={};this.className='';this.value='';}
    append(...nodes){for(const n of nodes){if(n.tagName==='fragment')this.append(...n.children);else this.children.push(n);}}
    appendChild(n){this.append(n);return n;}replaceChildren(...nodes){this.children=[];this.append(...nodes);}
    setAttribute(k,v){this.attrs[k]=v;}addEventListener(){}
    querySelectorAll(selector){const cls=selector.match(/\.([\w-]+)/)?.[1],out=[];for(const n of this.children){if(n.className.split(' ').includes(cls))out.push(n);out.push(...n.querySelectorAll(selector));}return out;}
  }
  const ids=new Map(['recentList','recentSearch','recentRefreshBtn','recentSummary'].map(id=>[id,new Element()]));
  const context={window:{addEventListener(){}},document:{getElementById:id=>ids.get(id),createElement:tag=>new Element(tag),createDocumentFragment:()=>new Element('fragment')},setInterval:()=>1};
  for(const file of ['display-format.js','recent.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+file,'utf8'),context);const recent=new context.window.ExcelLensRecentWorkbooks({api:async()=>({items:[]}),open(){},toast(){}});
  return {recent,list:ids.get('recentList'),search:ids.get('recentSearch')};
}
test('recent file sizes use KB, MB or GB without inventing missing values',()=>{
  const {recent}=setup();for(const [bytes,label]of [[0,'0 KB'],[1,'0.001 KB'],[512,'0.5 KB'],[1024,'1 KB'],[1536,'1.5 KB'],[1024**2,'1 MB'],[1024**3*1.5,'1.5 GB']])assert.equal(recent.formatSize(bytes),label);
  for(const bytes of [undefined,null,NaN,-1,'1024'])assert.equal(recent.formatSize(bytes),'—');
});
test('size stays beside opening time while metadata, search and time refresh retain their roles',()=>{
  const h=setup();h.recent.items=[{id:'a',name:'<表格>.xlsx',path:'D:/资料/表格.xlsx',fileSize:2*1024**2,sheetCount:4,available:true,openedAt:new Date().toISOString()},{id:'b',name:'成员.txt',path:'temp.txt',archiveName:'资料.7z',archivePath:'D:/资料.7z',fileSize:3*1024**3,sheetCount:1,available:false,openedAt:'2020-01-01T00:00:00Z'}];h.recent.render();
  assert.deepEqual(h.list.querySelectorAll('.recent-size').map(n=>n.textContent),['2 MB','3 GB']);
  assert.equal(h.list.querySelectorAll('.recent-name')[0].textContent,'<表格>.xlsx');
  const side=h.list.querySelectorAll('.recent-side')[0];assert.deepEqual(side.children.map(n=>n.className),['recent-date','recent-size','recent-controls']);
  assert.equal(h.list.querySelectorAll('.recent-meta')[0].children[0].textContent,'4 张表');
  h.recent.refreshTimes();assert.equal(side.children[1].textContent,'2 MB');
  h.search.value='成员';h.recent.render();assert.deepEqual(h.list.querySelectorAll('.recent-size').map(n=>n.textContent),['3 GB']);assert.equal(h.list.querySelectorAll('.recent-meta')[0].children[0].textContent,'文件已移动或删除');
});
