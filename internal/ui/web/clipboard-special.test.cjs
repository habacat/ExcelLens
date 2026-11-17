const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){const context={window:{}};for(const name of ['clipboard','clipboard-special'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+name+'.js','utf8'),context);context.window.ExcelLensClipboard.configure({rows:6,columns:4,cells:24,textBytes:100000,htmlBytes:100000});return {...context.window.ExcelLensClipboardSpecial,clip:context.window.ExcelLensClipboard};}
const plain=value=>JSON.parse(JSON.stringify(value));
const cell=(col,raw,extra={})=>({col,raw,display:raw,type:'inlineStr',hasFormula:false,...extra});
function fixture(){return {startCol:2,endCol:4,header:{row:1,cells:[cell(2,'金额'),cell(3,'金额'),cell(4,'')]},rows:[{row:9,cells:[cell(2,'9007199254740993.31',{display:'9,007,199,254,740,993.31',type:'n'}),cell(3,'001'),cell(4,'<img>|q\tline\n尾')]},{row:4,cells:[cell(2,'2',{type:'n',hasFormula:true,formula:'=B3+1',display:'2.00'}),cell(3,'  ')]}]};}

test('source-order metadata, duplicate/empty headers and exact decimals survive structured JSON without numeric coercion',()=>{
  const h=setup(),payload=fixture(),before=JSON.stringify(payload),result=h.selection(payload,{format:'json',includeHeader:true,sheet:'原表'}),doc=JSON.parse(result.text);
  assert.equal(doc.sheet,'原表');assert.deepEqual(doc.columns.map(c=>c.header),['金额','金额','']);assert.deepEqual(doc.rows.map(r=>r.row),[9,4]);assert.equal(doc.rows[0].cells[0].raw,'9007199254740993.31');assert.equal(doc.rows[0].cells[0].type,'n');assert.equal(doc.rows[0].cells[1].raw,'001');assert.equal(doc.rows[1].cells[2].present,false);assert.equal(doc.rows[1].cells[0].formula,'=B3+1');assert.equal(doc.header.cells[0].ref,'B1');assert.equal(JSON.stringify(payload),before);
  const plan=h.pastePlan(result.text,{format:'json',startRow:2,startCol:1,columns:4});assert.deepEqual(plain(plan.changes).map(c=>c.value),['9007199254740993.31','001','<img>|q\tline\n尾','2','  ','']);
});

test('explicit display/raw/formula modes share safe TSV while Markdown escapes source HTML and delimiters',()=>{
  const h=setup(),payload=fixture();
  const raw=h.selection(payload,{format:'tsv',mode:'raw',includeHeader:true});assert.deepEqual(plain(h.clip.parse(raw.text))[0],['金额','金额','']);assert.equal(h.clip.parse(raw.text)[1][0],'9007199254740993.31');
  assert.equal(h.clip.parse(h.selection(payload,{mode:'display'}).text)[0][0],'9,007,199,254,740,993.31');
  assert.equal(h.clip.parse(h.selection(payload,{mode:'formula'}).text)[1][0],'=B3+1');
  const md=h.selection(payload,{format:'markdown'}).text;assert(md.startsWith('| B | C | D |'));assert(md.includes('&lt;img&gt;\\|q&#9;line<br>尾'));assert(md.includes('&#32;&#32;'));assert(!md.includes('<img>'));
  payload.rows.unshift(payload.header);assert.equal(h.clip.parse(h.selection(payload,{includeHeader:true}).text).length,3,'selected header must not be duplicated');
});

test('missing formula cache and unavailable formulas are never silently copied as a valid value or expression',()=>{
  const h=setup(),payload={startCol:1,endCol:1,rows:[{row:3,cells:[cell(1,'',{ref:'A3',hasFormula:true,formula:'=1+1',cacheMissing:true})]}]};
  for(const mode of ['raw','display'])assert.throws(()=>h.selection(payload,{mode}),/缓存/);
  assert.equal(h.selection(payload,{mode:'formula'}).text,'=1+1');const json=h.selection(payload,{format:'json'}).text;assert.equal(JSON.parse(json).rows[0].cells[0].cacheMissing,true);
  assert.throws(()=>h.pastePlan(json,{format:'json',startRow:0,startCol:0,columns:1}),/缓存/);assert.equal(h.pastePlan(json,{format:'json',mode:'formula',startRow:0,startCol:0,columns:1}).changes[0].value,'=1+1');
  payload.rows[0].cells[0].formula='';assert.throws(()=>h.selection(payload,{mode:'formula'}),/无法还原/);assert.equal(JSON.parse(h.selection(payload,{format:'json'}).text).rows[0].cells[0].formula,'');
});

test('header and escaped output share source budgets; no oversized clipboard representation is published',()=>{
  const h=setup(),payload=fixture();h.clip.configure({rows:2,columns:4,cells:8,textBytes:100000,htmlBytes:100000});assert.throws(()=>h.selection(payload,{includeHeader:true}),/上限/);
  h.clip.configure({rows:6,columns:4,cells:24,textBytes:50,htmlBytes:100000});assert.throws(()=>h.selection(payload,{format:'json'}),/容量/);assert.throws(()=>h.selection({...payload,startCol:1.5}),/上限/);
});

test('transpose, exact-empty skipping and header removal build one bounded atomic text edit without clipping',()=>{
  const h=setup();const plan=h.pastePlan('H1\tH2\tH3\n001\t\t  \nx\ty\n',{skipFirstRow:true,transpose:true,skipBlank:true,startRow:3,startCol:1,columns:4});
  assert.equal(plan.rows,3);assert.equal(plan.columns,2);assert.equal(plan.skipped,2);assert.deepEqual(plain(plan.changes),[{row:3,col:1,value:'001'},{row:3,col:2,value:'x'},{row:4,col:2,value:'y'},{row:5,col:1,value:'  '}]);
  assert.throws(()=>h.pastePlan('a\tb\nc\td',{transpose:true,skipBlank:true,startRow:9999,startCol:0,columns:2}),/边界/);assert.throws(()=>h.pastePlan('\t\t',{skipBlank:true,startRow:0,startCol:0,columns:2}),/边界/);
  assert.throws(()=>h.pastePlan('"bad',{startRow:0,startCol:0,columns:4}),/引号/);assert.throws(()=>h.pastePlan('head',{skipFirstRow:true,startRow:0,startCol:0,columns:4}),/没有/);
  assert.equal(h.pastePlan('宽表头\t多一列\n001',{skipFirstRow:true,startRow:0,startCol:0,columns:1}).changes[0].value,'001');
});

test('JSON numeric primitives are refused, not rounded; malformed types, overlong input and excess request bytes fail before edits',()=>{
  const h=setup(),target={format:'json',startRow:0,startCol:0,columns:4};
  assert.throws(()=>h.pastePlan('[[9007199254740993.31]]',target),/字符串/);assert.throws(()=>h.pastePlan(JSON.stringify(Array(2001).fill([])),target),/容量/);
  assert.deepEqual(plain(h.pastePlan('[["9007199254740993.31","001",null]]',target).changes).map(c=>c.value),['9007199254740993.31','001','']);
  const doc=JSON.parse(h.selection(fixture(),{format:'json'}).text);doc.rows[0].cells[0].raw=99;assert.throws(()=>h.pastePlan(JSON.stringify(doc),target),/类型/);
  assert.throws(()=>h.pastePlan('😀'.repeat(4001),{startRow:0,startCol:0,columns:1}),/4000/);
  const escaped=JSON.stringify(Array.from({length:90},()=>['\u0001'.repeat(4000)]));assert.throws(()=>h.pastePlan(escaped,{...target,columns:1}),/2 MiB/);
});
