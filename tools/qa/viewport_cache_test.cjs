// Regression for the 1.1.1 bounded viewport cache. Uses the exact functions
// from app.js with an isolated internal-state fixture, not an approximate copy.
'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const ctx={window:{}};
for (const name of ['richtext.js','viewport-cache.js']) {
 vm.runInNewContext(fs.readFileSync(__dirname+'/../../internal/ui/web/'+name,'utf8'),ctx);
}
const {TileCache,estimateViewportBytes}=ctx.window.ExcelLensViewport;
const budget=24*1024*1024, cache=new TileCache(budget);
// Finding a covering tile also counts as use. A second window must not evict
// the tile we just reused under a different request key.
const small=new TileCache(budget,2);
const request=(row)=>({displayStart:row,displayEnd:row+1,startCol:1,cols:1,visibleColStart:1,visibleColEnd:1});
const payload={rows:[],startCol:1,colCount:1};
const reused=small.store('first',payload,request(1));
small.store('second',payload,request(20));
assert.equal(small.find('inside',{...request(1),displayEnd:1}),reused);
small.store('third',payload,request(40));
assert.equal(small.find('first',request(1)),reused);
assert.equal(small.find('second',request(20)),null);
const tile=n=>({rows:Array.from({length:95},(_,i)=>({number:i+1,cells:
 Array.from({length:40},(_,col)=>({ref:`${col}:${i}`,col:col+1,raw:'A'.repeat(n),display:'B'.repeat(n),formula:'C'.repeat(n)}))}))});
const big=tile(800);
assert(estimateViewportBytes(big)>9*1024*1024);
for(let i=0;i<15;i++){
 cache.store('tile-'+i,big,{displayStart:i*95,startCol:1,cols:40});
 assert(cache.size<=8);
 assert(cache.bytes<=budget);
}
const oldBytes=cache.bytes;
cache.store('tile-14',tile(1),{displayStart:14*95,startCol:1,cols:40});
assert(cache.bytes<oldBytes,'replaced tile bytes were not subtracted');
let sum=0;for(const x of cache.entries.values())sum+=x.bytes;
assert.equal(sum,cache.bytes,'cache accounting inconsistent');
const giant=tile(3000);
cache.store('giant',giant,{displayStart:0,startCol:1,cols:40});
assert.equal(cache.size,1,'only newest oversized tile should remain');
assert(giant.rows[0].cells[0].raw==='A'.repeat(3000),'cache mutated source data');
console.log('PASS bounded viewport cache / replacement / giant tile / payload fidelity');
