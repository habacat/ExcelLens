(() => {
  'use strict';
  const inputModel = window.ExcelLensFormulaInput;

  class WorksheetCompletion {
    constructor(editor, accept) {
      this.editor=editor;this.api = editor.owner.api;
      this.accept = accept;
      this.popup = document.getElementById('editFormulaPopup');
      this.list = document.getElementById('editFormulaSuggestions');
      this.hint = document.getElementById('editFormulaHint');
      this.items = [];
      this.generation = 0;
      this.popup.addEventListener('pointerdown', event => event.preventDefault());
      this.list.addEventListener('click', event => {
        const option = event.target.closest('[role="option"]');
        if (option) this.choose(Number(option.dataset.index));
      });
    }

    hide() {
      ++this.generation;
      this.popup.hidden = this.list.hidden = this.hint.hidden = true;
      this.field?.removeAttribute('aria-activedescendant');
      this.field?.setAttribute('aria-expanded', 'false');
      this.field = null;
      this.items = [];
    }

    context(text,caret) {
      const bracket=/([\p{L}_][\p{L}\p{N}_.]*)?\[(@?)([^\[\]]*)$/u.exec(text.slice(0,caret));
      if(bracket && inputModel.context(text,bracket.index))return {table:bracket[1]||'',prefix:bracket[3],start:caret-bracket[3].length,end:caret};
      return inputModel.context(text,caret);
    }
    async symbols() {
      const g=this.editor,d=g.owner.data.document,key=[d.id,d.binding,g.owner.data.book].join('|');
      if(key!==this.symbolKey){
        this.symbolKey=key;
        this.symbolsReady=g.owner.post('formula-symbols',{document:{id:d.id,binding:d.binding},revision:d.revision}).catch(()=>{if(this.symbolKey===key)this.symbolKey=null;return [];});
      }
      return (await this.symbolsReady)||[];
    }
    symbolChoices(items,at) {
      const g=this.editor,addr=g.draft.address;
      const tables=items.filter(x=>x.kind==='table');
      if(at.table!==undefined){
        const table=tables.find(t=>{
          if(at.table)return t.name.toUpperCase()===at.table.toUpperCase();
          if(t.scope!==addr.sheet)return false;
          const [a,b]=t.ref.split(':').map(v=>window.ExcelLensAddress.parseRef(v,Infinity,Infinity));
          return b&&addr.row>=a.row&&addr.row<=b.row&&addr.col>=a.col&&addr.col<=b.col;
        });
        return (table?.columns||[]).map(name=>({name,kind:'column',signature:table.name+' · 表列'}));
      }
      const found=new Map();
      for(const x of items)if(x.kind==='table'||!x.scope||x.scope===addr.sheet){
        if(!x.scope&&found.has(x.name.toUpperCase()))continue;
        found.set(x.name.toUpperCase(),{...x,signature:x.kind==='table'?'Excel 表格':(x.scope||'工作簿')+' · 定义名称'});
      }
      return [...found.values()];
    }
    async update(field, text) {
      const ticket = ++this.generation;
      const at = this.context(text, field.selectionStart);
      if (!at || (!at.prefix && !at.fn && at.table===undefined) || field.selectionStart !== field.selectionEnd) { this.hide(); return; }
      if (!this.catalog) {
        this.loading ??= this.api('/api/calculator/functions?scope=worksheet').then(data => {
          this.catalog = data.items;
        }).finally(() => { this.loading = null; });
        try { await this.loading; } catch { if (ticket === this.generation) this.hide(); return; }
      }
      const symbols=await this.symbols();
      if (ticket !== this.generation || field !== document.activeElement || !this.editor.draft) return;
      const prefix = at.prefix.toUpperCase();
      const choices=this.symbolChoices(symbols,at);
      const matches = at.table!==undefined ? choices.filter(x=>x.name.toUpperCase().startsWith(prefix)) :
        prefix ? [...this.catalog.filter(f => f.name.startsWith(prefix)),...choices.filter(x=>x.name.toUpperCase().startsWith(prefix))] : [];
      const fn = at.fn && this.catalog.find(f => f.name === at.fn);
      if (!matches.length && !fn) { this.hide(); return; }
      if (this.field && this.field !== field) this.hide();
      this.field = field;
      this.items = matches;
      this.index = 0;
      this.list.replaceChildren(...matches.map((item, i) => {
        const option = document.createElement('div');
        option.setAttribute('role','option');
        option.id = 'editFormulaOption' + i;
        option.dataset.index = String(i);
        option.textContent = item.name;
        return option;
      }));
      this.list.hidden = !matches.length;
      this.hint.hidden = !fn && !matches.length;
      if (fn && !matches.length) {
        const args = fn.signature.slice(fn.name.length + 1, -1).split(/,\s*/);
        const active = Math.min(at.arg, args.length - 1);
        this.hint.replaceChildren(fn.name + '(');
        args.forEach((arg, i) => {
          if (i) this.hint.append(', ');
          const span = document.createElement(i === active ? 'strong' : 'span');
          span.textContent = arg;
          this.hint.append(span);
        });
        this.hint.append(')');
      }
      this.popup.hidden = false;
      this.popup.classList.toggle('hint-only', !matches.length);
      this.list.scrollTop = 0;
      this.highlight();
      this.position();
    }

    highlight() {
      const option = this.list.children[this.index];
      this.list.querySelector('[aria-selected="true"]')?.setAttribute('aria-selected','false');
      this.field.setAttribute('aria-expanded', String(!!option));
      if (!option) { this.field.removeAttribute('aria-activedescendant'); return; }
      this.hint.textContent = this.items[this.index].signature;
      option.setAttribute('aria-selected','true');
      this.field.setAttribute('aria-activedescendant', option.id);
      if (option.offsetTop < this.list.scrollTop) this.list.scrollTop = option.offsetTop;
      else if (option.offsetTop + option.offsetHeight > this.list.scrollTop + this.list.clientHeight)
        this.list.scrollTop = option.offsetTop + option.offsetHeight - this.list.clientHeight;
    }

    position() {
      if (this.popup.hidden || !this.field) return;
      const anchor = this.field.style.opacity === '0' ? document.getElementById('editGridFormula') : this.field;
      const r = anchor.getBoundingClientRect(), box = this.popup;
      const gap = 3, margin = 5;
      const below = innerHeight - r.bottom - margin - gap, above = r.top - margin - gap;
      box.style.maxHeight = Math.max(25, Math.max(below,above)) + 'px';
      const top = below >= box.offsetHeight || below >= above ? r.bottom + gap : r.top - box.offsetHeight - gap;
      box.style.top = Math.max(margin, top) + 'px';
      box.style.left = Math.max(margin, Math.min(r.left, innerWidth - box.offsetWidth - margin)) + 'px';
    }

    choose(i) {
      const option = this.items[i], field = this.field;
      if (!option || !field) return;
      const cursor = field.selectionStart, at = this.context(field.value, cursor);
      if (!at) return;
      if(option.kind){
        const suffix=field.value.slice(cursor);
        const end=cursor+(option.kind==='column' ? (/^[^\[\],()+*/]*\]/.exec(suffix)?.[0].length||0) :
          (/^[\p{L}\p{N}_.]*/u.exec(suffix)[0].length));
        this.hide();
        this.accept(field,at.start,end,option.kind==='column'?option.name.replace(/['#@\[\]]/g,"'$&")+']':option.name+(option.kind==='table'?'[':''));
        return;
      }
      const tail = field.value.slice(cursor).match(/^[A-Za-z0-9_.]*/)[0];
      const end = cursor + tail.length, hasParen = field.value[end] === '(';
      this.hide();
      this.accept(field, at.start, end + (hasParen ? 1 : 0), option.name + '(');
    }

    key(event) {
      if (this.list.hidden || this.popup.hidden || !this.items.length || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return false;
      if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
        this.index = (this.index + (event.key === 'ArrowUp' ? -1 : 1) + this.items.length) % this.items.length;
        this.highlight();
      } else if (event.key === 'Tab' || event.key === 'Enter') this.choose(this.index);
      else if (event.key === 'Escape') this.hide();
      else return false;
      return true;
    }
  }
  window.ExcelLensWorksheetCompletion = WorksheetCompletion;
})();
