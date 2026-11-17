const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/app.js','utf8');
const start=source.indexOf('  function searchKey('),end=source.indexOf('  function buildSearchHits(',start),ctx={};
vm.runInNewContext(source.slice(start,end)+';this.key=searchKey;',ctx);
test('advanced regex caches preserve syntax case while ordinary search remains case-insensitive',()=>{
  assert.notEqual(ctx.key('advanced','current','A regex "\\D"','Data'),ctx.key('advanced','current','A regex "\\d"','Data'));
  assert.notEqual(ctx.key('advanced','all','A regex "ABC"','Data'),ctx.key('advanced','all','A regex "abc"','Other'));
  assert.equal(ctx.key('formula','current',' SUM(A1) ','Data'),ctx.key('formula','current','sum(a1)','Data'));
  assert.equal(ctx.key('value','all','Hello','Data'),ctx.key('value','all','hello','Other'));
});
