/* Cancellable feedback for controls. Grid geometry never animates. */
(() => {
  'use strict';
  const active=new WeakMap();
  const reduced=()=>window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  function stop(node){if(!node)return;const animation=active.get(node);active.delete(node);animation?.cancel();}
  function enter(node,{distance=0,duration=160}={}){
    stop(node);
    if(!node?.animate||reduced())return;
    const frames=[{opacity:0},{opacity:1}];
    if(distance){const base=getComputedStyle(node).transform;frames[0].transform=(base==='none'?'':base)+' translateY('+distance+'px)';frames[1].transform=base;}
    const animation=node.animate(frames,{duration,easing:'cubic-bezier(.2,.8,.2,1)'});active.set(node,animation);
    animation.finished.then(()=>{if(active.get(node)===animation)active.delete(node);},()=>{});
    return animation;
  }
  function exit(node,{distance=10,duration=140}={}){
    stop(node);if(!node?.animate||reduced())return Promise.resolve(true);
    const base=getComputedStyle(node).transform,animation=node.animate([
      {opacity:1,transform:base},
      {opacity:0,transform:(base==='none'?'':base)+` translateY(${distance}px) scale(.98)`}
    ],{duration,easing:'cubic-bezier(.4,0,1,1)',fill:'forwards'});
    active.set(node,animation);
    return animation.finished.then(()=>{if(active.get(node)!==animation)return false;stop(node);return true;},()=>false);
  }
  window.ExcelLensMotion=Object.freeze({enter,exit,stop});
})();
