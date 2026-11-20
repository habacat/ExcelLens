const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const context = vm.createContext({window: {}, TextEncoder});
for (const file of ['clipboard.js', 'edit-copy-model.js','edit-grid-range.js']) vm.runInContext(fs.readFileSync(path.join(__dirname, file), 'utf8'), context);
const model = context.window.ExcelLensEditModel;
const limits = {cellBytes: 1024, batchCells: 100, requestBytes: 8192};
const sheet = {name: '台账', firstRow: 1, firstCol: 1, rows: 8, cols: 4};

test('direct grid typing preserves identifiers, source text, explicit types and CSV literals', () => {
  const parse = (raw, original='blank', kind='auto', csv=false) => {
    const result = model.gridValue(raw, kind, original, csv, limits);
    return [result.kind, result.raw];
  };
  for (const text of ['0012','1234567890123456','2026-11-18']) assert.deepEqual(parse(text), ['text',text]);
  assert.deepEqual(parse('3.50','text'), ['text','3.50']);
  assert.deepEqual(parse('3.50','number'), ['number','3.50']);
  assert.deepEqual(parse('true'), ['boolean','TRUE']);
  assert.deepEqual(parse('=A1+1'), ['formula','=A1+1']);
  assert.deepEqual(parse("'=A1+1"), ['text','=A1+1']);
  assert.deepEqual(parse('=A1+1','text','auto',true), ['text','=A1+1']);
  assert.deepEqual(parse('','number'), ['blank','']);
});

test('edit input reversibly represents CR, CRLF, LF and backslashes', () => {
  const value = '\\r 原文\r\n\n 字段\r结束';
  assert.equal(model.decode(model.encode(value, true), true), value);
  assert.throws(() => model.decode('末尾\\', true), /反斜线/);
  assert.equal(model.value('text', '001234567890123456789', limits).raw, '001234567890123456789');
});

test('paste preview uses source coordinates and treats formulas as literal text', () => {
  const anchor = model.target('b2', sheet);
  const raw = '000001\t"多行\r\n原文"\r\n=SUM(B2)\t';
  const plan = model.paste(raw, anchor, sheet, true, limits, context.window.ExcelLensClipboard.parse);
  assert.equal(plan.changes.length, 3);
  assert.equal(plan.changes[1].value.raw, '多行\r\n原文');
  assert.equal(plan.changes[2].row, 3);
  assert.equal(plan.changes[2].value.kind, 'text');
  assert.equal(plan.changes[2].value.raw, '=SUM(B2)');
  assert.throws(() => model.paste(raw, model.target('D8', sheet), sheet, false, limits, context.window.ExcelLensClipboard.parse), /最多|超出/);
});

test('equal-step fill keeps exact decimal text and one source axis', () => {
  const a = model.target('B5', sheet), b = model.target('B2', sheet);
  const plan = model.fill(a, b, {kind: 'text', raw: '9007199254740993.25'}, '-0.01', true, true, limits);
  assert.equal(plan.changes[0].row, 4);
  assert.equal(plan.changes[0].value.raw, '9007199254740993.24');
  assert.equal(plan.changes[2].value.raw, '9007199254740993.22');
  assert.throws(() => model.fill(a, model.target('C2', sheet), {kind: 'text', raw: '1'}, '', false, false, limits), /同行/);
});

test('a repeated clipboard field stops expanding at the serialized request limit', () => {
  let visited = 0;
  function* repeated() {
    for (let row=1; row<=10000; row++) {
      visited++;
      yield {sheet:'预算',row,col:1,value:{kind:'text',raw:'引号"与换行\n'.repeat(28)}};
    }
  }
  assert.throws(()=>model.bounded(repeated(),{batchCells:10000,requestBytes:2048}),/容量/);
  assert.ok(visited > 0 && visited < 10);
});

test('clearing a sorted range edits only returned source rows and keeps covered merge members', async () => {
  const range = Object.create(context.window.ExcelLensGridRangeEditor.prototype);
  let committed;
  const doc = {id:'one',binding:'bound',revision:'4'};
  range.owner = {data:{document:doc},limits,commit:(_,changes)=>{committed=changes; return true;}};
  range.grid = {active:true,selected:{sheet:'台账'},shape:sheet};
  range.view = {
    ready:()=>true, filtered:()=>true, areas:()=>[{}],
    mergeAt:(r,c)=>r===5 && c<=2 ? {row:5,col:1,endRow:5,endCol:2} : null,
    visibleSelection:()=>({firstColumn:1,lastColumn:2,current:()=>true,read:async()=>({rows:[{row:5},{row:2}]})})
  };
  assert.equal(await range.clear(),true);
  assert.deepEqual(Array.from(committed,c=>[c.row,c.col,c.value.kind]),[[5,1,'blank'],[2,1,'blank'],[2,2,'blank']]);
});
