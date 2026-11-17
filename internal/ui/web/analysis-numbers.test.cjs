const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function setup(){const env={window:{}};vm.runInNewContext(fs.readFileSync(__dirname+'/analysis-numbers.js','utf8'),env);return env.window.ExcelLensAnalysisNumbers;}
const cell=(exact,display=exact)=>({exact,display});

test('derived ratios and cumulative cells reuse display preferences without formatting the original aggregate',()=>{
  const numbers=setup(),ratio={exact:'9007199254740993.31',display:'9007199254740993.31',comparison:{exact:'1/3',display:'33.333%',percent:true}},before=JSON.stringify(ratio);
  assert.equal(numbers.format(ratio),'33.333%');assert.equal(numbers.format(ratio,{...numbers.defaults,scientific:false,groupThousands:false}),'33.333%');assert.equal(JSON.stringify(ratio),before);
  assert.equal(numbers.format({exact:'5',display:'5',comparison:{exact:'1234567.89',display:'1234567.89',percent:false}}),'1,234,567.89');
  assert.equal(numbers.format({comparison:{exact:'100000000000',display:'10000000000000.00%',percent:true}}),'1.00e+13%');
  for(const display of ['分母为0','无有效值','分母无有效值'])assert.equal(numbers.format({exact:'0',display:'0.00',comparison:{exact:'',display,percent:true,status:'unavailable'}}),display);
});

test('thousands grouping preserves decimal digits beyond floating point precision and changes only the display',()=>{
  const numbers=setup(),options={...numbers.defaults,scientific:false};
  for(const [input,expected] of [['9007199254741015.11','9,007,199,254,741,015.11'],['-1234567.00','-1,234,567.00'],['+1234.5000','+1,234.5000'],['123.00','123.00'],['0.00','0.00']]){
    const value=Object.freeze(cell(input));assert.equal(numbers.format(value,options),expected);assert.equal(value.exact,input);assert.equal(value.display,input);
  }
});

test('default scientific notation rounds once from exact decimals, including negatives and carry to the next exponent',()=>{
  const {format}=setup();
  for(const [exact,display,expected] of [
    ['9007199254741015.11','9007199254741015.11','9.01e+15'],
    ['1286742750677287.87','1286742750677287.87','1.29e+15'],
    ['1004999999999.999','1005000000000.00','1.00e+12'],
    ['1005000000000','1005000000000.00','1.01e+12'],
    ['-9995000000000','-9995000000000.00','-1.00e+13'],
    ['1.2345e+80','1'+'0'.repeat(80),'1.23e+80'],
    ['999999999999.999','1000000000000.00','1,000,000,000,000.00'],
    ['1000000000000','1000000000000.00','1.00e+12'],
  ])assert.equal(format(cell(exact,display)),expected,exact);
});

test('rational averages use exact integer arithmetic even with large numerator and denominator',()=>{
  const {format}=setup();
  for(const [exact,display,expected] of [
    ['201000000000000/200','1005000000000.00','1.01e+12'],
    ['200999999999999/200','1005000000000.00','1.00e+12'],
    ['-1000000000000000/-3','333333333333333.33','3.33e+14'],
    ['-1000000000000000/3','-333333333333333.33','-3.33e+14'],
    ['1'+'0'.repeat(450)+'/3'+'0'.repeat(434),'3333333333333333.33','3.33e+15'],
    ['109/30','3.63','3.63'],
  ])assert.equal(format(cell(exact,display)),expected,exact);
});

test('independent settings and threshold apply without rewriting cells; malformed and missing values stay truthful',()=>{
  const numbers=setup(),large=cell('9007199254741015.11'),normal=cell('1234567.89');
  numbers.set({...numbers.defaults,groupThousands:false});assert.equal(numbers.format(large),'9.01e+15');assert.equal(numbers.format(normal),'1234567.89');
  numbers.set({...numbers.defaults,scientific:false});assert.equal(numbers.format(large),'9,007,199,254,741,015.11');
  numbers.set({groupThousands:false,scientific:false,scientificExponent:9});assert.equal(numbers.format(large),large.display);
  for(const exponent of [9,12,15]){
    const settings={...numbers.defaults,scientificExponent:exponent};
    assert.equal(numbers.format(cell('1e'+exponent,'1'+'0'.repeat(exponent)+'.00'),settings),'1.00e+'+exponent);
    assert(!numbers.format(cell('9e'+(exponent-1),'9'+'0'.repeat(exponent-1)+'.00'),settings).includes('e+'));
  }
  for(const value of [null,{},cell('','')])assert.equal(numbers.format(value),'—');
  for(const exact of ['NaN','Infinity','1/0','1/2/3','1e99999','1'+'0'.repeat(9000)])assert.equal(numbers.format(cell(exact,'无法计算')),'无法计算');
  assert.equal(numbers.format(cell('','#VALUE!')),'#VALUE!');
  assert.throws(()=>numbers.set({scientific:true}));assert.equal(numbers.get().groupThousands,false);
  const copy=numbers.get();copy.groupThousands=true;assert.equal(numbers.get().groupThousands,false);
});
