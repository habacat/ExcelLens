// Presentation of aggregate cells only. Never infer numeric source types or
// convert exact amounts to binary floating point, including for rounding.
(() => {
  'use strict';
  const defaults=Object.freeze({groupThousands:true,scientific:true,scientificExponent:12});
  const valid=value=>value&&typeof value.groupThousands==='boolean'&&typeof value.scientific==='boolean'&&[9,12,15].includes(value.scientificExponent);
  let options={...defaults};
  // Bound work on unexpected input. Out-of-budget representations retain the
  // server display and exact tooltip instead of approximating or inventing zero.
  const maxText=8192,maxExponent=4096;
  function rational(raw){
    if(!raw||raw.length>maxText)return null;
    let n,d;
    if(raw.includes('/')){
      const parts=raw.split('/');if(parts.length!==2||!parts.every(p=>/^[+-]?\d+$/.test(p)))return null;
      n=BigInt(parts[0]);d=BigInt(parts[1]);if(d===0n)return null;
      if(d<0n){n=-n;d=-d;}
    }else{
      const match=/^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:[eE]([+-]?\d{1,5}))?$/.exec(raw);if(!match)return null;
      const fraction=match[3]??match[4]??'',exponent=Number(match[5]||0)-fraction.length;
      if(Math.abs(exponent)>maxExponent)return null;
      n=BigInt((match[1]||'')+(match[2]||'0')+fraction);d=1n;
      if(exponent>=0)n*=10n**BigInt(exponent);else d=10n**BigInt(-exponent);
    }
    const negative=n<0n;if(negative)n=-n;
    return {n,d,negative};
  }
  function scientific(raw,threshold,multiplier=1n){
    const ratio=rational(raw);if(!ratio||ratio.n===0n)return null;
    const {d,negative}=ratio,n=ratio.n*multiplier;
    let exponent=n.toString().length-d.toString().length;
    if(exponent<threshold)return null;
    // Digit lengths give an upper bound of at most one power of ten.
    if(n<d*10n**BigInt(exponent))exponent--;
    if(exponent<threshold)return null;
    // Three significant digits, rounded once from the exact rational value.
    const denominator=d*10n**BigInt(exponent-2);
    let rounded=n/denominator;
    if((n%denominator)*2n>=denominator)rounded++;
    if(rounded>=1000n){rounded/=10n;exponent++;}
    const digits=rounded.toString().padStart(3,'0');
    return (negative?'-':'')+digits[0]+'.'+digits.slice(1)+'e+'+exponent;
  }
  function group(display){
    const match=/^([+-]?)(\d+)(\.\d+)?$/.exec(display);if(!match||match[2].length<4)return display;
    const digits=match[2],first=digits.length%3||3;
    return match[1]+[digits.slice(0,first),...(digits.slice(first).match(/\d{3}/g)||[])].join(',')+(match[3]||'');
  }
  function format(cell,settings=options){
	cell=cell?.comparison??cell;
    const display=String(cell?.display??'');if(!display)return '—';
	if(cell?.status)return display;
	const percent=cell?.percent===true,suffix=percent?'%':'',plain=percent&&display.endsWith('%')?display.slice(0,-1):display;
    if(!valid(settings))settings=defaults;
    if(settings.scientific){const value=scientific(String(cell?.exact??plain),settings.scientificExponent,percent?100n:1n);if(value!==null)return value+suffix;}
    return (settings.groupThousands&&plain.length<=maxText?group(plain):plain)+suffix;
  }
  window.ExcelLensAnalysisNumbers={defaults,valid,format,get:()=>({...options}),set(value){if(!valid(value))throw Error('分析显示设置无效');options={...value};}};
})();
