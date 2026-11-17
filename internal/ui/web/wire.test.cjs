const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('fs'),vm=require('vm');
const sandbox={window:{}};
vm.runInNewContext(fs.readFileSync(__dirname+'/wire.js','utf8'),sandbox);
const unpack=rows=>JSON.parse(JSON.stringify(sandbox.window.ExcelLensWire.unpack(rows)));

test('compact viewport reconstructs exact identifiers, display and metadata',()=>{
  const rows=unpack([{n:7,s:2,c:[
    {c:1,v:'00012345678901234567',t:'inlineStr'},
    {c:2,v:'1.2300001',d:'',s:8,o:'#ff0000'},
    {c:3,v:'',f:'=A1+1',k:7},
    {c:4,v:'A&B',d:'链接',r:'D9',h:{kind:'external',url:'https://example.com/a?b=1&c=2'}},
    {c:16384,v:'🙂跨行\n原文',t:'inlineStr',k:8,x:{runs:[{end:2,font:{bold:false,underline:'none'}},{end:7,font:{color:'#1255AA'}}],notice:'部分样式待核对'}}
  ]}]);
  assert.equal(rows[0].cells[0].raw,'00012345678901234567');assert.equal(rows[0].cells[0].ref,'A7');
  assert.equal(rows[0].cells[1].display,'');assert.equal(rows[0].cells[1].valueColor,'#ff0000');
  assert.equal(rows[0].cells[2].hasFormula,true);assert.equal(rows[0].cells[2].cacheMissing,true);assert.equal(rows[0].cells[2].sharedFormula,true);
  assert.equal(rows[0].cells[3].ref,'D9');assert.equal(rows[0].cells[3].hyperlink.url,'https://example.com/a?b=1&c=2');
  assert.equal(rows[0].cells[4].ref,'XFD7');assert.equal(rows[0].cells[4].raw,'🙂跨行\n原文');
  assert.equal(rows[0].cells[4].richText.runs[0].font.bold,false);assert.equal(rows[0].cells[4].richText.runs[0].font.underline,'none');assert.equal(rows[0].cells[4].richText.runs[1].font.color,'#1255AA');assert.equal(rows[0].cells[4].richText.notice,'部分样式待核对');
  assert.equal(rows[0].cells[4].style,0);assert.equal(rows[0].style,2);
});
