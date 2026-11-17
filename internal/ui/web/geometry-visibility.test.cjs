const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(max=1000){const ctx={window:{}};for(const name of ['geometry.js','row-visibility.js'])vm.runInNewContext(fs.readFileSync(__dirname+'/'+name,'utf8'),ctx);return {axis:new ctx.window.ExcelLensGeometry(max,10).rows,Visibility:ctx.window.ExcelLensRowVisibility};}
test('compressed hidden geometry retains source sizes and local overrides across rank, pixel and resize boundaries',()=>{
  const {axis,Visibility}=setup(100),ranges=[{min:1,max:2},{min:7,max:15},{min:30,max:80}],v=new Visibility(100,ranges);
  axis.load({4:31,10:50,90:65},[{min:20,max:40,size:17.5},{min:60,max:91,size:28.5}]);axis.setHidden(ranges);
  const check=()=>{let offset=0;for(let n=1;n<=100;n++){assert.equal(axis.offset(n),offset);const size=v.visible(n)?axis.values.get(n)??axis.sourceMeasure(n):0;assert.equal(axis.measure(n),size);if(size){for(const part of [0,0.1,size/2,size-0.001])assert.equal(axis.at(offset+part),n);offset+=size;}}assert.equal(axis.total(100),offset);};check();axis.set(10,66);check();axis.set(90,38);check();axis.setBase(26.25);check();
  const visible=[];for(let n=1;n<=100;n++)if(v.visible(n))visible.push(n);visible.forEach((n,i)=>{assert.equal(v.at(i),n);assert.equal(v.before(n),i);});
});
test('a billion-row hidden gap does not allocate row arrays and inverse lookup reaches the far visible source coordinate',()=>{
  const {axis,Visibility}=setup(1e9),ranges=[{min:2,max:999999999}],v=new Visibility(1e9,ranges);axis.setHidden(ranges);
  assert.equal(axis.hidden.length,1);assert.equal(axis.total(1e9),50);assert.equal(axis.at(25),1e9);assert.equal(v.at(1),1e9);assert.equal(v.next(2),1e9);assert.equal(v.previous(999999999),1);assert.equal(v.count(1,1e9),2);
  const all=new Visibility(1e9,[{min:1,max:1e9}]);assert.equal(all.next(1),0);assert.equal(all.at(0),0);
});

test('visibility navigation and selections agree with a dense reference including hidden anchors and empty views',()=>{
  const {Visibility}=setup();let seed=91273;
  const random=n=>{seed=(Math.imul(seed,1664525)+1013904223)>>>0;return seed%n;};
  for(let sample=0;sample<160;sample++){
    const maximum=1+random(110),hidden=Array.from({length:maximum},()=>random(3)===0),ranges=[];
    for(let i=0;i<maximum;i++)if(hidden[i]){const first=i;while(i+1<maximum&&hidden[i+1])i++;ranges.push({min:first+1,max:i+1});}
    const v=new Visibility(maximum,ranges),rows=hidden.flatMap((h,i)=>h?[]:[i+1]);
    assert.equal(v.length,rows.length);
    for(let row=1;row<=maximum;row++){
      const rank=rows.filter(n=>n<row).length;
      assert.equal(v.before(row),rank);assert.equal(v.visible(row),!hidden[row-1]);
      assert.equal(v.next(row),rows.find(n=>n>=row)||0);assert.equal(v.previous(row),rows.findLast(n=>n<=row)||0);
      for(const delta of [-2,-1,0,1,2])assert.equal(v.advance(row,delta),rows[rank+delta-(hidden[row-1]&&delta>0?1:0)]||0);
      const last=row+random(maximum-row+1);
      assert.equal(v.selectionCount({row1:last,row2:row},false),rows.filter(n=>n>=row&&n<=last).length);
    }
    const through=Math.min(30,maximum),pinned=rows.filter(n=>n<=through);
    if(pinned.length){const head=pinned.at(-1);assert.equal(v.selectionCount({row1:head,row2:maximum+1,visual1:head,visual2:7},true,through),1+7);}
  }
});

test('random fractional source heights and hidden runs preserve pixel inverse boundaries and device edges',()=>{
  let seed=77681;const random=n=>{seed=(Math.imul(seed,1664525)+1013904223)>>>0;return seed%n;};
  for(let sample=0;sample<140;sample++){
    const maximum=40+random(90),{axis}=setup(maximum),sizes=Array.from({length:maximum},()=>10+random(8001)/37),values={},runs=[],hidden=[],ranges=[];
    for(let row=1;row<=maximum;row++){runs.push({min:row,max:row,size:sizes[row-1]});if(random(4)===0)values[row]=10+random(7001)/43;if(random(3)===0)hidden.push(row);}
    for(const row of hidden){const last=ranges.at(-1);if(last&&last.max===row-1)last.max=row;else ranges.push({min:row,max:row});}
    axis.load(values,runs);axis.setHidden(ranges);let offset=0;
    for(let row=1;row<=maximum;row++){
      const size=hidden.includes(row)?0:values[row]??sizes[row-1];assert.ok(Math.abs(axis.offset(row)-offset)<1e-8);assert.equal(axis.measure(row),size);
      if(size){for(const part of [0,size/2,size-1e-5])assert.equal(axis.at(axis.offset(row)+part),row,`sample ${sample}, row ${row}, part ${part}`);offset+=size;}
      for(const ratio of [1,1.25,2]){const start=axis.paintOffset(row,1.1,ratio),end=axis.paintOffset(row+1,1.1,ratio);assert.ok(Math.abs(start+axis.paintSize(row,1.1,ratio)-end)<1e-8);}
    }
    assert.ok(Math.abs(axis.total(maximum)-offset)<1e-8);
  }
});
