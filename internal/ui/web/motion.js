/* Cancellable feedback for controls. Grid geometry never animates. */
(() => {
  'use strict';
  const active=new WeakMap();
  const reduced=()=>window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  function stop(node){if(!node)return;const animation=active.get(node);active.delete(node);animation?.cancel();}
  function timing(style,token,fallback){
    const value=style.getPropertyValue?.(token)?.trim();
    if(!value)return fallback;
    return value.endsWith('ms')?parseFloat(value):parseFloat(value)*1000;
  }
  function enter(node,{distance=0,duration}={}){
    stop(node);
    if(!node?.animate||reduced())return;
    const style=getComputedStyle(node),frames=[{opacity:0},{opacity:1}];
    if(distance){const base=style.transform;frames[0].transform=(base==='none'?'':base)+' translateY('+distance+'px)';frames[1].transform=base;}
    const animation=node.animate(frames,{duration:duration??timing(style,'--motion-normal',160),easing:style.getPropertyValue?.('--motion-ease')?.trim()||'ease-out'});active.set(node,animation);
    animation.finished.then(()=>{if(active.get(node)===animation)active.delete(node);},()=>{});
    return animation;
  }
  function exit(node,{distance=4,duration}={}){
    stop(node);if(!node?.animate||reduced())return Promise.resolve(true);
    const style=getComputedStyle(node),base=style.transform,animation=node.animate([
      {opacity:1,transform:base},
      {opacity:0,transform:(base==='none'?'':base)+` translateY(${distance}px)`}
    ],{duration:duration??timing(style,'--motion-exit',110),easing:'ease-in',fill:'forwards'});
    active.set(node,animation);
    return animation.finished.then(()=>{if(active.get(node)!==animation)return false;stop(node);return true;},()=>false);
  }
  window.ExcelLensMotion=Object.freeze({enter,exit,stop});
})();
