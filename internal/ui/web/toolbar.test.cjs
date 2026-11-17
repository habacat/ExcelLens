const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const context={window:{},document:{querySelector:()=>null},Set,Number,Math};
vm.runInNewContext(fs.readFileSync(__dirname+'/toolbar.js','utf8'),context);
const choose=context.window.ExcelLensAdaptiveToolbar.choose;
const items=[{node:'open',width:55,priority:0},{node:'search',width:55,priority:0},{node:'export',width:55,priority:1},{node:'freeze',width:90,priority:2},{node:'commands',width:55,priority:0}];
test('narrow toolbar retains named core actions and the command entry',()=>{
  const shown=choose(items,180,5);assert.deepEqual([...shown],['open','search','commands']);
});
test('available container width admits useful actions in priority order',()=>{
  assert.deepEqual([...choose(items,235,5)],['open','search','commands','export']);
  assert.deepEqual([...choose(items,330,5)],['open','search','commands','export','freeze']);
});
test('core controls remain available for wrapping even below a single-row minimum',()=>{
  assert.deepEqual([...choose(items,60,5)],['open','search','commands']);
});
test('toolbar gap accounting includes fractional text widths at the boundary',()=>{
  const list=[{node:'open',width:55.5,priority:0},{node:'commands',width:55.5,priority:0},{node:'sort',width:55.5,priority:1}];
  assert.equal(choose(list,176.49,5).size,2);assert.equal(choose(list,176.5,5).size,3);
});
