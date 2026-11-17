/* Unsaved analysis edits stay separate from the last durable server snapshot.
 * Acknowledging an older request must never erase a newer draft. */
(() => {
  'use strict';
  class Drafts {
    constructor(){this.items=new Map();this.onChange=null;}
    key(book,id,row,col){return JSON.stringify([book,id,row,col]);}
    stage(book,id,changes){
      return changes.map(change=>{const entry={book,id,...change};entry.key=this.key(book,id,entry.row,entry.col);this.items.set(entry.key,entry);this.onChange?.(entry.key,{book,id,row:entry.row,col:entry.col,value:entry.value});return entry;});
    }
    acknowledge(entries){for(const entry of entries)if(this.items.get(entry.key)===entry){this.items.delete(entry.key);this.onChange?.(entry.key,null);}}
    restore(records){
      const items=Object.entries(records);
      for(const [key,v] of items)if(!v||typeof v.book!=='string'||typeof v.id!=='string'||!Number.isSafeInteger(v.row)||v.row<0||v.row>=10000||!Number.isSafeInteger(v.col)||v.col<0||v.col>=80||typeof v.value!=='string'||Array.from(v.value).length>4000||this.key(v.book,v.id,v.row,v.col)!==key)throw Error('分析草稿恢复内容无效');
      for(const [key,value] of items)if(!this.items.has(key))this.items.set(key,{...value,key});
    }
    get(book,id,row,col){return this.items.get(this.key(book,id,row,col));}
    entries(book){return [...this.items.values()].filter(entry=>entry.book===book);}
    get size(){return this.items.size;}
  }
  window.ExcelLensAnalysisDrafts=Drafts;
})();
