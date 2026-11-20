(() => {
  'use strict';
  const areaKey = r => [r.row,r.col,r.endRow,r.endCol].join(',');
  class GridValidationList {
    constructor(editor) {
      this.grid = editor;
      this.button = document.getElementById('editGridListButton');
      this.list = document.getElementById('editGridList');
      this.button.onclick = () => this.list.hidden ? this.open() : this.dismiss(true);
      this.list.addEventListener('click',event => { if (event.target.tagName === 'OPTION') this.choose(); });
      this.list.addEventListener('keydown',event => {
        if (event.key === 'Escape' || event.altKey && event.key === 'ArrowUp') this.dismiss(true);
        else if (event.key === 'Enter' || event.key === 'Tab') this.choose(event.key === 'Tab' ? (event.shiftKey ? -1 : 1) : 0);
        else return;
        event.preventDefault(); event.stopPropagation();
      });
      document.addEventListener('pointerdown',event => {
        if (!this.button.contains(event.target) && !this.list.contains(event.target)) this.dismiss();
      },true);
    }
    dismiss(focus = false) {
      this.lookup?.abort(); this.lookup = null;
      this.list.hidden = true; this.button.setAttribute('aria-expanded','false');
      if (focus) this.grid.focus();
    }
    reset() {
      this.dismiss(); this.source?.abort(); this.source = null;
      this.key = ''; this.point = ''; this.tree = null; this.button.hidden = true;
    }
    setRules(rules) {
      const stamp = JSON.stringify(rules || []);
      if (stamp === this.rulesStamp) return;
      this.rulesStamp = stamp; this.rules = rules;
      this.rebuild();
    }
    rebuild() {
      const items = new Map((this.base || []).map(r=>[areaKey(r),r]));
      for (const r of this.rules || []) {
        if (r.start.sheet !== this.sheet) continue;
        const rect = {row:r.start.row,col:r.start.col,endRow:r.endRow,endCol:r.endCol}, key = areaKey(rect);
        if (!r.list) items.delete(key);
        else if (!items.get(key)?.hidden) items.set(key,rect);
      }
      this.tree = new window.ExcelLensMergedCells();
      this.tree.load([...items.values()].filter(r=>!r.hidden));
    }
    selection() {
      const g = this.grid, cell = g.selected, shape = g.shape;
      if (!cell || !g.active || !g.view.ready() || !shape || shape.readOnly || g.owner.data.encoding) { this.reset(); return; }
      const key = JSON.stringify([g.book,shape.name]);
      if (key !== this.key) {
        this.reset(); this.key = key; this.sheet = shape.name; this.base = []; this.error = '';
        this.rebuild();
        if (shape.sourceRules) {
          const controller = this.source = new AbortController();
          this.ready = g.owner.post('list-areas',{book:g.book,sheet:shape.name},controller.signal)
            .then(areas => {
              if (this.source !== controller) return;
              this.base = areas; this.rebuild(); this.position();
            }).catch(error => { if (this.source === controller) this.error = error.message; });
        } else this.ready = Promise.resolve();
      }
      const point = JSON.stringify([key,cell.ref,g.owner.data.document.revision]);
      if (point !== this.point) { this.dismiss(); this.point = point; }
      this.position();
    }
    position() {
      const g = this.grid, cell = g.selected;
      const hit = g.active && !g.draft && !g.stale && cell && this.tree?.at(cell.row,cell.col);
      const rect = hit && g.view.bounds(cell.ref)?.rect;
      if (!rect) {
        this.button.hidden = true;
        if (!this.list.hidden) this.dismiss();
        return;
      }
      const region = g.region.getBoundingClientRect(), x = rect.right-region.left, y = rect.bottom-region.top;
      this.button.hidden = false;
      this.button.style.left = Math.max(0,Math.min(x,region.width-20))+'px';
      this.button.style.top = Math.max(0,Math.min(y-20,region.height-20))+'px';
      if (this.list.hidden) return;
      const width = Math.min(region.width,Math.max(156,rect.right-rect.left+20,this.list.scrollWidth+4));
      this.list.style.width = width+'px';
      this.list.style.left = Math.max(0,Math.min(rect.left-region.left,region.width-width))+'px';
      const above = rect.top-region.top, below = region.height-y;
      const up = below < this.list.offsetHeight && above > below;
      this.list.style.maxHeight = Math.max(0,up ? above : below)+'px';
      this.list.style.top = (up ? above-this.list.offsetHeight : y)+'px';
    }
    async open() {
      const g = this.grid;
      if (g.blocked || g.stale || !g.active) return;
      this.selection();
      const point = this.point;
      if (!point) return;
      this.dismiss();
      const lookup = this.lookup = new AbortController();
      const current = () => this.lookup === lookup && point === this.point && g.active && !g.blocked;
      const activity = globalThis.ExcelLensActivity?.begin('正在读取选项',{signal:lookup.signal});
      try {
        await this.ready;
        if (!current()) return;
        if (this.error) { const message=this.error; this.key=''; throw Error(message); }
        if (this.button.hidden) throw Error('当前格没有可选列表');
        const target = g.ranges.target();
        const result = await g.owner.post('choices',{...target,ref:g.selected.ref},lookup.signal);
        if (!current()) return;
        if (!result.choices?.length) throw Error('当前列表没有可选值');
        this.list.replaceChildren(...result.choices.map(value=>new Option(value,value)));
        this.list.size = Math.max(2,Math.min(9,result.choices.length));
        this.list.value = g.selected.raw;
        if (this.list.selectedIndex < 0) this.list.selectedIndex = 0;
        this.list.removeAttribute('style'); this.list.hidden = false;
        this.button.setAttribute('aria-expanded','true'); this.position();
        this.list.focus({preventScroll:true});
      } catch (error) {
        if (current() && !lookup.signal.aborted) {
          if (error.status === 409) { g.stale = true; g.controls(); }
          g.status(error.message,true);
        }
      } finally { activity?.end(); }
    }
    choose(step = 0) {
      if (this.list.hidden || this.list.selectedIndex < 0) return;
      const g = this.grid, cell = g.selected, raw = this.list.value;
      this.dismiss(true);
      g.act(async () => {
        const addr = {sheet:cell.sheet,row:cell.row,col:cell.col};
        if (cell.raw !== raw || cell.hasFormula || !['s','str','inlineStr','text'].includes(cell.type)) {
          if (!await g.owner.commit('change',[{...addr,value:{kind:'text',raw}}],{direct:true})) return false;
        }
        if (step) await g.view.move(0,step);
        g.focus(); return true;
      });
    }
  }
  window.ExcelLensGridValidationList = GridValidationList;
})();
