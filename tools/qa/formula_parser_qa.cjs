const assert=require('node:assert/strict');
const path=require('node:path');
global.window=global;
require(path.resolve(__dirname,'../../internal/ui/web/formula_inspector.js'));
const parse=window.ExcelLensFormulaInspector.parseFormula;
const labels=(formula,sheet='主表')=>parse(formula,sheet).map(x=>x.label);
assert.deepEqual(labels('=SUM($A$1,\'销售 2025\'!B2:B5,A:A,5:6)+IF(C3="A1",D4,0)'),
  ['$A$1',"'销售 2025'!B2:B5",'A:A','5:6','C3','D4']);
assert.deepEqual(labels('=Table1[Q1]+"A1"+Sheet1!B2+LOG10(B1)'),['Sheet1!B2','B1']);
assert.deepEqual(labels('="A1"&"B2""B3"&A1&$A2&A$3'),['A1','$A2','A$3']);
assert.deepEqual(labels("='O''Brien'!$C$2 + '2024 data'!B2:'2024 data'!C5"),
  ["'O''Brien'!$C$2", "'2024 data'!B2:'2024 data'!C5"]);
assert.deepEqual(labels("=SUM([Book.xlsx]Sheet1!A1,'[Book.xlsx]S'!B2,'Local'!C3)"),
  ["'[Book.xlsx]S'!B2", "'Local'!C3"]);
assert.equal(parse("='[Book.xlsx]S'!B2",'主表')[0].external,true);
assert.deepEqual(labels('=SUM(Sheet1:Sheet3!A1)'),[]);
assert.deepEqual(labels('=SUM(Z1000000,XFD1048576,XFE1,A1048577,ABCD1)'),['Z1000000','XFD1048576']);
assert.deepEqual(labels('=A1+A1').length,2);
assert.equal(parse('=A1+A1','主表')[0].colorIndex,parse('=A1+A1','主表')[1].colorIndex);
assert.deepEqual(labels('=IF(A1>0,Sheet2!A:D,1:99)'),['A1','Sheet2!A:D','1:99']);
assert.deepEqual(labels('=A1B2+B5_C6+SUM(D7)'),['D7']);
assert.deepEqual(labels('=1E+10+"C11"'),[]);
assert.deepEqual(labels('="a""B9"&A2'),['A2']);
const r=parse('=B10:C5','主表')[0];
assert.deepEqual([r.start.row,r.start.col,r.end.row,r.end.col],[5,2,10,3]);
console.log('Formula tokenization QA passed: quotes, ranges, columns, rows, external, bounds, false positives, unique colors.');
