/* A displayed result and its server task are one snapshot. Bound dashboard
 * retention without serializing and duplicating large result strings. */
(() => {
  'use strict';
  function retainedBytes(value,limit){
    let size=0;const seen=new WeakSet();
    function visit(item){
      if(size>limit)return;
      if(typeof item==='string'){size+=16+item.length*2;return;}
      if(!item||typeof item!=='object'){size+=8;return;}
      if(seen.has(item))return;seen.add(item);
      size+=64;
      if(Array.isArray(item)){
        size+=item.length*8;
        for(const child of item){visit(child);if(size>limit)break;}
      }else for(const key of Object.keys(item)){size+=16+key.length*2;visit(item[key]);if(size>limit)break;}
    }
    visit(value);return size;
  }
  class ResultCache {
    constructor({maxEntries=8,maxBytes=24*1024*1024}={}){
      this.maxEntries=maxEntries;this.maxBytes=maxBytes;this.entries=new Map();this.bytes=0;
    }
    get size(){return this.entries.size;}
    key(book,query){return JSON.stringify([book,query]);}
    clear(){this.entries.clear();this.bytes=0;}
    remove(key){const old=this.entries.get(key);if(old){this.bytes-=old.bytes;this.entries.delete(key);}}
    get(book,query){
      const key=this.key(book,query),entry=this.entries.get(key);
      if(!entry)return null;
      this.entries.delete(key);this.entries.set(key,entry);return entry.snapshot;
    }
    set(book,query,{job,result,sourceFields}){
      const key=this.key(book,query),snapshot={book,query:JSON.parse(JSON.stringify(query)),job,result};
      if(Array.isArray(sourceFields))snapshot.sourceFields=sourceFields.map(field=>({...field}));
      this.remove(key);
      const bytes=retainedBytes(snapshot,this.maxBytes)+key.length*2+64;
      if(bytes>this.maxBytes||this.maxEntries<1)return snapshot;
      while(this.entries.size>=this.maxEntries||this.bytes+bytes>this.maxBytes)this.remove(this.entries.keys().next().value);
      this.entries.set(key,{snapshot,bytes});this.bytes+=bytes;return snapshot;
    }
  }
  ResultCache.retainedBytes=retainedBytes;
	// Screen and image exports must select the same completed metric. Never
	// infer from builder fields that may already belong to another source.
	ResultCache.metricCell=function(result,row,columnIndex,metricIndex,total=false){
	  if(row){
	    if(total)return row.metricTotals?.[metricIndex]||(metricIndex===0?row.total:null)||{};
	    return row.metricValues?.[columnIndex]?.[metricIndex]||(metricIndex===0?row.values?.[columnIndex]:null)||{};
	  }
	  if(total)return result.metricGrand?.[metricIndex]||(metricIndex===0?result.grand:null)||{};
	  return result.metricColumnTotals?.[columnIndex]?.[metricIndex]||(metricIndex===0?result.totals?.[columnIndex]:null)||{};
	};
	ResultCache.projectMetric=function(result,index){
	  if(!result||index===0)return result;
	  const cell=ResultCache.metricCell;
	  return {...result,grand:cell(result,null,0,index,true),totals:(result.columnKeys||[]).map((_,j)=>cell(result,null,j,index)),
	    rows:(result.rows||[]).map(row=>({...row,total:cell(result,row,0,index,true),values:(result.columnKeys||[]).map((_,j)=>cell(result,row,j,index))}))};
	};
  window.ExcelLensAnalysisResults=ResultCache;
})();
