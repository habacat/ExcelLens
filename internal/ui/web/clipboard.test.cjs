const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const ctx={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/clipboard.js','utf8'),ctx);
const clip=ctx.window.ExcelLensClipboard,plain=value=>JSON.parse(JSON.stringify(value));
test('HTML keeps the merged grid while TSV keeps a placeholder for every covered position',()=>{
  const limits={rows:12,columns:8,cells:40,textBytes:20000,htmlBytes:20000};
  vm.runInNewContext(fs.readFileSync(__dirname+'/merges.js','utf8'),ctx);
  const merges=new ctx.window.ExcelLensMergedCells();
  merges.load([{row:7,col:2,endRow:9,endCol:3},{row:7,col:4,endRow:7,endCol:5}]);
  const row=n=>({row:n,cells:[2,3,4,5,6].map(col=>({col,display:n+'-'+col}))});
  const build=(numbers,visibility)=>clip.build({startCol:2,endCol:6,rows:numbers.map(row)},[],false,'',null,limits,
    (r,c)=>merges.copyAnchor(r,c,visibility));
  const result=build([7,8,9]);
  assert.match(result.html,/<td colspan="2" rowspan="3"[^>]*>7-2<\/td>/);
  assert.match(result.html,/<td colspan="2"[^>]*>7-4<\/td>/);
  assert.equal((result.html.match(/<td\b/g)||[]).length,9);
  assert.equal(result.text,'7-2\t\t7-4\t\t7-6\n\t\t8-4\t8-5\t8-6\n\t\t9-4\t9-5\t9-6');
  assert.doesNotMatch(result.html,/8-2/);
  const hidden=build([7,9],{count:()=>1});
  assert.match(hidden.html,/colspan="2" rowspan="2"/);
  for (const rows of [[7,10,9],[7,9,8],[9,7],[7,9]]) {
    const reordered=build(rows);
    assert.doesNotMatch(reordered.html,/rowspan/);
    assert.match(reordered.html,/colspan="2"/);
    assert.ok(reordered.html.includes(rows.at(-1)+'-6'));
  }
});
test('Excel/WPS quoted TSV round trips literal tabs, CRLF, quotes, empty cells and text identifiers',()=>{
  const rows=[['000123','1,234.50','line1\r\nline2','"quoted"','\t'],['','tail','','','']];
  const text=rows.map(row=>row.map(clip.tsvCell).join('\t')).join('\r\n')+'\r\n';
  assert.deepEqual(plain(clip.parse(text)),rows);
  assert.deepEqual(plain(clip.parse('"a\nb"\t"q""s"\rx\t\r')),[['a\nb','q"s'],['x','']]);
  assert.deepEqual(plain(clip.parse('\n\n')),[[''],['']]);
});
test('paste planning preserves all values and rejects the entire payload instead of clipping rows or columns',()=>{
  assert.deepEqual(plain(clip.pasteChanges('"a\nb"\t00042\r\nx\ty\r\n',{startRow:2,startCol:1,rowLimit:10,columns:3})),[
    {row:2,col:1,value:'a\nb'},{row:2,col:2,value:'00042'},{row:3,col:1,value:'x'},{row:3,col:2,value:'y'}
  ]);
  assert.throws(()=>clip.pasteChanges('a\tb\nc\td',{startRow:0,startCol:1,columns:2}),/边界/);
  assert.throws(()=>clip.pasteChanges('a\nb',{startRow:9999,startCol:0,columns:1}),/边界/);
});
test('malformed or excessive clipboard data is refused before building any edits',()=>{
  for(const text of ['"unterminated','"closed"tail','a'.repeat(4001)])assert.throws(()=>clip.parse(text));
  assert.throws(()=>clip.parse('a\tb',{maxCols:1}),/1列/);
  assert.throws(()=>clip.parse('a\nb\nc',{maxRows:2}),/2行/);
  assert.throws(()=>clip.parse('a\tb\nc\td',{maxCells:3}),/3个单元格/);
  assert.throws(()=>clip.parse('a'.repeat(2*1024*1024+1)),/2 MiB/);
});
test('4000 Unicode characters fit a cell regardless of UTF-8 bytes or UTF-16 pairs',()=>{
  for(const symbol of ['文','😀']){
    const value=symbol.repeat(4000);assert.equal(clip.parse(value)[0][0],value);
    assert.equal(clip.pasteChanges(value,{startRow:0,startCol:0,columns:1})[0].value,value);
    assert.throws(()=>clip.parse(value+symbol),/4000字符/);assert.equal(clip.characterCount(value),4000);
    assert.equal(clip.utf8Length(value),Buffer.byteLength(value,'utf8'));
  }
});
test('paste is atomic under UTF-8 and JSON request budgets; explicit retries may form bounded batches',()=>{
  const unicode=Array(180).fill('文'.repeat(4000)).join('\n');assert(unicode.length<2*1024*1024);assert.throws(()=>clip.parse(unicode),/2 MiB/);
  const escaped=Array(90).fill('\u0001'.repeat(4000)).join('\n');assert.throws(()=>clip.pasteChanges(escaped,{startRow:0,startCol:0,columns:1}),/请求超过2 MiB/);
  const changes=Array.from({length:200},(_,row)=>({row,col:0,value:'文'.repeat(4000)})),batches=Array.from(clip.batches(changes));
  assert.equal(batches.length,2);assert.equal(batches.flat().length,200);
  for(const batch of batches){clip.validateChanges(batch);assert(Buffer.byteLength(JSON.stringify({id:'sheet',workbookKey:'a'.repeat(64),expectedRevision:1,changes:batch}),'utf8')<=2*1024*1024);}
});
