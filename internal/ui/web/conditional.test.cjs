const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const h={window:{}};for(const n of ['cell-address','conditional','richtext','clipboard','wire'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+n+'.js','utf8'),h);
h.window.ExcelLensClipboard.configure({rows:10,columns:10,cells:100,textBytes:10000,htmlBytes:20000});
test('conditional display and clipboard keep exact source text, false font flags and known/pending effects',()=>{
  const cell={col:1,raw:'9007199254740993.31',display:'source display',style:0,valueColor:'#008800',conditional:{style:{font:{bold:false}},visual:{bold:false,color:'#FF0033',background:'#FFFF00'},display:'derived display',valueColor:'',notice:'一条规则尚未求值'}},before=JSON.stringify(cell);
  const payload={startCol:1,endCol:2,rows:[{presentationNotice:'显示容量不足',cells:[cell,{col:2,raw:'',display:'',presentationOnly:true,conditional:{style:{font:{}},visual:{background:'#FFFF00'}}}]}]};
  const clip=h.window.ExcelLensClipboard.build(payload,[{bold:true,color:'#008800'}],true,'',{cellRuns:4096},{textBytes:10000,htmlBytes:20000});
  assert.equal(clip.text,'derived display\t');assert.match(clip.html,/color:#FF0033/);assert(!clip.html.includes('font-weight:bold'));assert.match(clip.html,/background-color:#FFFF00/);assert(clip.notices.includes('显示容量不足'));assert(clip.notices.some(n=>n.includes('尚未求值')));
  assert.equal(h.window.ExcelLensClipboard.build(payload,[],false).text,'source display\t');assert.equal(JSON.stringify(cell),before);
  assert(h.window.ExcelLensConditional.estimate(cell.conditional)>500);assert(h.window.ExcelLensConditional.same(cell.conditional,JSON.parse(JSON.stringify(cell.conditional))));assert(!h.window.ExcelLensConditional.same(cell.conditional,{...cell.conditional,style:{font:{}}}));
});
test('compact transport retains sparse presentation markers and a row budget notice',()=>{
  const cf={style:{font:{bold:false,underline:'none'}},visual:{color:'#1255AA'},notice:'未读取的规则',pending:true};
  const row=h.window.ExcelLensWire.unpack([{n:9,p:'显示超限',c:[{c:5,v:'',k:16,q:cf}]}])[0];
  assert.equal(row.number,9);assert.equal(row.presentationNotice,'显示超限');assert.equal(row.cells[0].ref,'E9');assert.equal(row.cells[0].raw,'');assert.equal(row.cells[0].presentationOnly,true);assert.equal(row.cells[0].conditional,cf);assert.equal(row.cells[0].conditional.pending,true);
});
test('pending conditional marker toggles pending class cleanly and clears on recycle',()=>{
  const classes=new Set();
  const node={title:'',classList:{add:c=>classes.add(c),remove:c=>classes.delete(c),toggle:(c,on)=>on?classes.add(c):classes.delete(c)}};
  h.window.ExcelLensConditional.notice(node,{conditional:{pending:true,notice:'条件格式统计计算中'}},null,true);
  assert.equal(classes.has('cell-conditional-pending'),true);
  assert.equal(classes.has('cell-presentation-notice'),true);
  assert.match(node.title,/计算中/);
  h.window.ExcelLensConditional.notice(node,{conditional:{pending:true,notice:'条件格式统计计算中'}},null,false);
  assert.equal(classes.has('cell-conditional-pending'),false);
  assert.equal(classes.has('cell-presentation-notice'),false);
  h.window.ExcelLensConditional.notice(node,{conditional:{matched:1}},null,true);
  assert.equal(classes.has('cell-conditional-pending'),false);
});

test('late conditional start is cancelled by its own ID before the next start', async () => {
  const calls = [];
  let finishFirst;
  const queue = h.window.ExcelLensConditional.createStatsRequests((action, body) => {
    calls.push([action, body.sheet || body.id]);
    if (calls.length === 1) return new Promise(resolve => { finishFirst = resolve; });
    return Promise.resolve({ jobId: 'second' });
  });
  const first = queue.start('old sheet');
  const cancel = queue.cancel(null);
  const retry = queue.start('new sheet');
  await Promise.resolve();
  finishFirst({ jobId: 'first' });
  await Promise.all([first, cancel, retry]);
  assert.deepEqual(calls, [['start', 'old sheet'], ['cancel', 'first'], ['start', 'new sheet']]);
});
