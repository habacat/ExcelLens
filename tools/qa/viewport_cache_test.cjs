// Regression for the 1.1.1 bounded viewport cache. Uses the exact functions
// from app.js with an isolated internal-state fixture, not an approximate copy.
'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(__dirname+'/../../internal/ui/web/app.js','utf8');
const start=source.indexOf('  function estimateViewportBytes(data) {');
const end=source.indexOf('  function cachedViewport(request) {',start);
assert(start>0&&end>start,'cache implementation not found');
const excerpt=source.slice(start,end)+ '\nthis.testCache={cacheViewport,estimateViewportBytes};';
const ctx={
 window:{},
 S:{viewportCache:new Map(),viewportCacheBytes:0},
 VIEWPORT_CACHE_BUDGET:24*1024*1024,
 viewportBounds:(data,request)=>({...request,columns:request.selectedCols||null})
};
vm.runInNewContext(fs.readFileSync(__dirname+'/../../internal/ui/web/richtext.js','utf8'),ctx);
vm.runInNewContext(excerpt,ctx);
const {cacheViewport,estimateViewportBytes}=ctx.testCache;
const tile=n=>({rows:Array.from({length:95},(_,i)=>({number:i+1,cells:
 Array.from({length:40},(_,col)=>({ref:`${col}:${i}`,col:col+1,raw:'A'.repeat(n),display:'B'.repeat(n),formula:'C'.repeat(n)}))}))});
const big=tile(800);
assert(estimateViewportBytes(big)>9*1024*1024);
for(let i=0;i<15;i++){
 cacheViewport('tile-'+i,big,{displayStart:i*95,startCol:1,cols:40});
 assert(ctx.S.viewportCache.size<=8);
 assert(ctx.S.viewportCacheBytes<=ctx.VIEWPORT_CACHE_BUDGET);
}
const oldBytes=ctx.S.viewportCacheBytes;
cacheViewport('tile-14',tile(1),{displayStart:14*95,startCol:1,cols:40});
assert(ctx.S.viewportCacheBytes<oldBytes,'replaced tile bytes were not subtracted');
let sum=0;for(const x of ctx.S.viewportCache.values())sum+=x.bytes;
assert.equal(sum,ctx.S.viewportCacheBytes,'cache accounting inconsistent');
const giant=tile(3000);
cacheViewport('giant',giant,{displayStart:0,startCol:1,cols:40});
assert.equal(ctx.S.viewportCache.size,1,'only newest oversized tile should remain');
assert(giant.rows[0].cells[0].raw==='A'.repeat(3000),'cache mutated source data');
console.log('PASS bounded viewport cache / replacement / giant tile / payload fidelity');
