const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

test('file selection uses injected extensions and resolved source capabilities', () => {
  const context = {window: {}};
  vm.runInNewContext(fs.readFileSync(__dirname + '/formats.js', 'utf8'), context);
  const xlsx = {maxRows: 1048576, maxColumns:16384, styled: true};
  const txt = {maxRows: 1000000000, maxColumns:16384, styled: false};
  const formats = context.window.ExcelLensFileFormats.create({extensions: ['.xlsx', '.txt', '.gz'], sources: {xlsx, txt},largeDataRows:5000000,queryTempBytes:8*1024**3});
  assert.equal(formats.maxRows,1000000000);assert.equal(formats.maxColumns,16384);
  assert.equal(formats.largeDataRows,5000000);assert.equal(formats.queryTempBytes,8*1024**3);
  assert.equal(formats.supports('账簿.XLSX'), true);
  assert.equal(formats.supports('凭证明细.tar.gz'), true);
  assert.equal(formats.supports('xlsb'), false);
  assert.equal(formats.supports('账簿.xlsb'), false);
  assert.equal(formats.source('txt'), txt);
  assert.equal(formats.source(undefined), xlsx);
});
