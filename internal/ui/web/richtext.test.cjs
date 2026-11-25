const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const context={window:{},clearTimeout,AbortController};for(const name of ['richtext','clipboard','freeze'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+name+'.js','utf8'),context);
context.window.ExcelLensClipboard.configure({rows:10,columns:10,cells:100,textBytes:10000,htmlBytes:20000});
const rich=context.window.ExcelLensRichText,limits={cellRuns:4096,viewRuns:8192};
const cell={type:'inlineStr',raw:'中😀<&\n尾',display:'中😀<&\n尾',richText:{runs:[{end:3,font:{bold:false,underline:'none',strike:false}},{end:7,font:{color:'#1255AA',vertical:'subscript',name:'";><img src=x onerror=1>'}}]}};
test('HTML copy and source text share exact UTF16 spans, inherit styles and never execute values or font names',()=>{
  const base={bold:true,underline:true,strike:true,fontName:'宋体',fontSize:11};
  const value=context.window.ExcelLensClipboard.build({startCol:1,endCol:1,rows:[{cells:[{...cell,col:1}]}]},[base],true,'',limits);
  assert.equal(value.text,context.window.ExcelLensClipboard.tsvCell(cell.raw));assert(!value.html.includes('<img'));assert(!value.html.includes('<&'));
  assert.match(value.html,/font-weight:400/);assert.match(value.html,/text-decoration-line:none/);assert.match(value.html,/vertical-align:sub/);
  assert.match(value.html,/<br style="mso-data-placement:same-cell">/);
  const td=value.html.slice(value.html.indexOf('<td'),value.html.indexOf('><span'));assert(!td.includes('text-decoration:underline'));
  const override=rich.html(cell,base,{limits,fontOverride:'Arial'});assert(override.html.includes('Arial'));assert(!override.html.includes('onerror'));
});
test('malformed runs, surrogate splits, display transformations and unknown font fields retain complete text and a cause',()=>{
  for(const runs of [[{end:2,font:{}}],[{end:8,font:{}}],[{end:3,font:{}},{end:3,font:{}}],[{end:7,font:{color:'url(x)'}}],[{end:7,font:{size:Infinity}}],[{end:7,font:{blink:true}}]]){
    const result=rich.inspect({...cell,richText:{runs}},limits);assert.equal(result.text,cell.raw);assert.equal(result.runs.length,0);assert.match(result.notice,/无效|覆盖/);
  }
  const changed=rich.inspect({...cell,display:'formatted'},limits);assert.equal(changed.text,'formatted');assert.match(changed.notice,/不同/);
  const partial=rich.inspect({...cell,richText:{runs:[{end:3,font:{bold:true}}],notice:'部分样式未还原'}},limits);const parts=[];rich.eachPart(partial,text=>parts.push(text));assert.equal(parts.join(''),cell.raw);
  assert.match(rich.inspect({...cell,richText:{runs:[{end:7,font:{underline:'singleAccounting'}}]}},limits).notice,/会计下划线/);
});
test('style equality distinguishes missing and explicit false; retained metadata is included in cache cost',()=>{
  assert(rich.same(cell.richText,JSON.parse(JSON.stringify(cell.richText))));const changed=JSON.parse(JSON.stringify(cell.richText));delete changed.runs[0].font.bold;assert(!rich.same(cell.richText,changed));
  assert(rich.estimate(cell.richText)>500);assert.equal(rich.estimate(null),0);
});
test('clipboard rejects entire oversized UTF8 source or escaped HTML before publishing any clipboard payload',()=>{
  const clip=context.window.ExcelLensClipboard,payload={startCol:1,endCol:1,rows:[{cells:[{...cell,col:1}]}]};
  assert.throws(()=>clip.build(payload,[],true,'',limits,{textBytes:20,htmlBytes:10000}),/文字超过/);
  assert.throws(()=>clip.build(payload,[],true,'',limits,{textBytes:10000,htmlBytes:150}),/HTML超过/);
  const value=clip.build(payload,[],true,'',limits,{textBytes:10000,htmlBytes:10000});assert.equal(value.text,clip.tsvCell(cell.raw));
});
test('frozen cache uses measured bytes, retains its budget across an oversized tile, rejects late aborted fetch and resets accounting',async()=>{
  const Frozen=context.window.ExcelLensFrozenPanes,cache=new Frozen({cacheBudget:100,estimateBytes:tile=>tile.bytes,state:{frozenCellMap:new Map()}});
  cache.cachePart('a',{bytes:80});cache.cachePart('b',{bytes:80});assert.equal(cache.cache.size,1);assert.equal(cache.cacheBytes,80);
  cache.cachePart('b',{bytes:20});cache.cachePart('c',{bytes:30});assert.equal(cache.cacheBytes,50);
  cache.cachePart('giant',{bytes:200});assert.equal(cache.cache.size,2);assert.equal(cache.cacheBytes,50);
  let deliver,signal;
  cache.o.api=(_,options)=>{signal=options.signal;return new Promise(ok=>{deliver=ok;});};
  const pending=cache.fetchPart('left',new URLSearchParams(),{});
  context.document={getElementById:()=>({style:{},replaceChildren(){}})};
  cache.reset();assert.ok(signal.aborted);deliver({bytes:50});await pending;
  assert.equal(cache.cache.size,0);assert.equal(cache.cacheBytes,0);assert.equal(cache.cacheSizes.size,0);
});
