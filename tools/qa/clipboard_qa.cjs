// Pure offline clipboard regression: no browser or external packages needed.
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const sandbox={window:{}};
vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../../internal/ui/web/clipboard.js'),'utf8'),sandbox);
const {build,tsvCell}=sandbox.window.ExcelLensClipboard;
assert.equal(tsvCell('line\n"quoted"'),'"line\n""quoted"""');
const content=build({startCol:1,endCol:2,rows:[
 {row:7,cells:[{col:1,display:'<script>alert(1)</script>',style:1,type:'s'},
               {col:2,display:'The\n"note"',style:0,type:'s'}]},
 {row:9,cells:[{col:1,display:'00012',style:1,type:'s'},
               {col:2,display:'123.40',style:0,type:'n'}]}
]},[{},{fontName:'Arial',background:'#ff0011',color:'#123456',bold:true,horizontal:'center'}]);
assert.equal(content.text,'<script>alert(1)</script>\t"The\n""note"""\n00012\t123.40');
assert.ok(content.html.includes('&lt;script&gt;alert(1)&lt;/script&gt;'));
assert.ok(!content.html.includes('<script>'));
assert.ok(content.html.includes('background-color:#ff0011'));
assert.ok(content.html.includes('font-weight:bold'));
assert.ok(content.html.includes('mso-number-format'));
assert.ok(content.html.includes('<br>'));
assert.equal((content.html.match(/<tr>/g)||[]).length,2);
assert.throws(()=>build({startCol:1,endCol:51,rows:[]}),/复制区域/);
console.log('ExcelLens 5.6 clipboard QA: HTML escaping, styles, tab/newline quoting, leading-zero text and caps PASS');
