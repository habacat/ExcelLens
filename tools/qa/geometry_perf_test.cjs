'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const sandbox={window:{}};
vm.runInNewContext(fs.readFileSync(__dirname+'/../../internal/ui/web/geometry.js','utf8'),sandbox);
const g=new sandbox.window.ExcelLensGeometry(1000000000,16384);
const changes={};
for(let i=13;i<1048576;i+=83)changes[String(i)]=10+(i%90);
g.rows.load(changes);
const pixels=Array.from({length:70000},(_,i)=>((i*389419)%25000000));
const slowAt=(axis,pos)=>{let lo=1,hi=axis.max;while(lo<hi){let mid=Math.floor((lo+hi+1)/2);if(axis.offset(mid)<=pos)lo=mid;else hi=mid-1;}return lo;};
// Cross-check random values and exact boundaries, not only common scroll positions.
for(let i=0;i<3500;i++){
 const pixel=pixels[i];
 assert.equal(g.rows.at(pixel),slowAt(g.rows,pixel),`pixel ${pixel}`);
}
for(let i=100;i<1048576;i+=787){
 const start=g.rows.offset(i),end=g.rows.offset(i+1);
 for(const p of [start,Math.max(0,end-1e-4),end])
  assert.equal(g.rows.at(p),slowAt(g.rows,p),`row ${i} pixel ${p}`);
}
let sum=0;
let t=process.hrtime.bigint();
for(let rep=0;rep<3;rep++)for(const p of pixels)sum+=g.rows.at(p);
const elapsed=Number(process.hrtime.bigint()-t)/1e6;
console.log(JSON.stringify({test:'geometry inverse lookup',pixelLookups:pixels.length*3,customSizes:Object.keys(changes).length,ms:Math.round(elapsed*100)/100,check:sum}));

// Fractional Excel view scaling is where inverse division can round a precise
// row boundary to its preceding row. Test both axes against the legacy seek.
let seed=37;
const rand=()=>{seed=(seed*1664525+1013904223)>>>0;return seed/4294967296;};
for(let batch=0;batch<24;batch++){
 const axis=batch%2?g.cols:g.rows;
 axis.setBase(12.5+rand()*13.4);
 const custom={};
 for(let i=0;i<240;i++)custom[String(Math.floor(1+rand()*axis.max))]=10+rand()*590;
 axis.load(custom);
 for(let i=0;i<240;i++){
  const cell=Math.floor(1+rand()*axis.max);
  const pixel=[axis.offset(cell),axis.offset(cell+1),axis.offset(cell)+rand()*axis.measure(cell),rand()*axis.total(axis.max),Math.max(0,axis.offset(cell+1)-1e-8)][i%5];
  assert.equal(axis.at(pixel),slowAt(axis,pixel),`fractional batch=${batch} cell=${cell} pixel=${pixel}`);
 }
}
console.log('PASS fractional-base row/column boundaries (5,760 probes)');
