(() => {
  'use strict';
  const el = id => document.getElementById('editGrid' + id);
  const model = window.ExcelLensEditModel;
  const keyOf = ({id, binding}) => ({id, binding});
  const textOf = cell => (cell?.hasFormula ? cell.formula : cell?.raw) ?? '';
  const kindOf = cell => cell?.hasFormula ? 'formula' : ({s:'text', inlineStr:'text', str:'text', text:'text', b:'boolean', d:'date', e:'error'}[cell?.type] || (cell?.raw ? 'number' : 'blank'));

  class SheetEditor {
    constructor(owner, bridge) {
      this.owner = owner; this.view = bridge;
      this.ranges = new window.ExcelLensGridRangeEditor(this);
      this.input = el('Input'); this.formula = el('Formula');
      this.measure = el('Measure');
      this.queue = [];
      this.region = document.querySelector('.grid-region');
      this.choices = new window.ExcelLensGridValidationList(this);
      this.statusNode = el('Status');
      for (const field of [this.input, this.formula]) {
        field.addEventListener('beforeinput', event => {
          if (!this.active || this.draft || this.moving) return;
          if (!this.begin(field === this.input, field)) event.preventDefault();
        });
        field.addEventListener('compositionstart', () => {
          if (!this.draft && !this.moving) this.begin(field === this.input, field);
          this.composing = true;
        });
        field.addEventListener('compositionend', () => { this.composing = false; });
        field.addEventListener('input', () => {
          if (!this.draft) return;
          this.draft.text = field.value;
          this.draft.typed = true;
          (field === this.input ? this.formula : this.input).value = field.value;
          this.schedulePosition();
        });
      }
      this.formula.addEventListener('focus', () => {
        if (this.active && !this.draft && !this.moving) this.begin(false, this.formula);
      });
      el('Kind').addEventListener('change', () => {
        if (!this.draft) this.begin(false, this.input);
        this.focus();
      });
      el('Undo').onclick = () => this.history('undo');
      el('Redo').onclick = () => this.history('redo');
      el('Save').onclick = () => this.act(() => owner.save());
      el('Exit').onclick = () => this.act(() => owner.exitGrid());
      el('More').onclick = () => this.act(() => owner.advanced());
      el('Range').onclick = () => this.act(() => owner.advanced('Range'));
      el('Refresh').onclick = () => this.reload();
      el('Retry').onclick = () => this.act(null);
      this.region.addEventListener('pointerdown', e => {
        if (!this.active || !this.blocked || !e.target.closest('.grid-cell')) return;
        // Resolve the clicked source coordinate on click, after the previous
        // draft has been confirmed. The reader's drag must not change it first.
        e.stopImmediatePropagation(); e.preventDefault();
      }, true);
      this.region.addEventListener('click', e => {
        const node = e.target.closest('.grid-cell');
        if (!this.active || !node) return;
        e.preventDefault(); e.stopImmediatePropagation();
        if (this.view.dragged()) { this.focus(); return; }
        if (this.view.ready()) this.act(() => {
          this.view.selectPoint(node,e);
          if (e.detail > 1) this.begin(false, this.input);
          this.focus();
        });
      }, true);
      this.region.addEventListener('dblclick', e => {
        if (this.active && e.target.closest('.grid-cell')) {
          e.preventDefault(); e.stopImmediatePropagation();
          if (!this.draft && !this.moving) this.begin(false, this.input);
        }
      }, true);
      this.region.addEventListener('contextmenu', e => {
        if (this.active && !e.target.matches('textarea') && (this.blocked || !e.target.closest('.grid-cell,.pinned-cell'))) { e.stopImmediatePropagation(); e.preventDefault(); }
      }, true);
      document.getElementById('grid').addEventListener('scroll', () => this.schedulePosition(), {passive:true});
      new ResizeObserver(() => this.schedulePosition()).observe(this.region);
      document.addEventListener('keydown', e => this.key(e), true);
      this.input.addEventListener('paste', e => this.paste(e));
      this.input.addEventListener('copy', e => {
        if (this.active && !this.draft) {
          e.preventDefault();
          if (!this.moving) this.view.copy();
        }
      });
      this.input.addEventListener('cut', e => {
        if (!this.active || this.draft) return;
        // System clipboard consumers cannot acknowledge a workbook move. Never
        // erase the source on cut, including when writing the clipboard fails.
        e.preventDefault();
        if (!this.moving) this.copyForCut();
      });
    }

    get active() { return !!this.book && this.book === this.owner.context().book; }
    get selected() { return this.view.selected(); }
    get shape() { return this.owner.data?.sheets.find(s => s.name === this.owner.context().sheet); }
    get blocked() { return !!(this.draft || this.moving || this.owner.pending || this.owner.abort); }
    get dirty() { return !!this.draft && (this.draft.text !== this.draft.initial || this.draft.typed && this.draft.text.startsWith('=') || el('Kind').value !== 'auto'); }

    async enter(data, signal) {
      this.book = this.owner.context().book;
      this.patches = new Map();
      try { await this.sync(undefined, signal); }
      catch (error) { this.leave(); throw error; }
      document.body.classList.add('editing-copy');
      for (const id of ['Bar','Input','Formula']) el(id).hidden = false;
      el('Kind').hidden = !!data.encoding;
      el('Kind').querySelector('[value="formula"]').disabled = !!data.encoding;
      this.view.redraw();
      this.selection(); this.controls(); this.focus();
    }

    leave() {
      this.choices.reset();
      this.ranges.copied = null;
      this.ranges.menu.hidden = true;
      this.book = ''; this.patches = null; this.calculated = null; this.stale = false;
      if (!this.rebinding) this.queue.length = 0;
      this.cancelDraft(); this.selecting = false;
      document.body.classList.remove('editing-copy');
      for (const id of ['Bar','Input','Formula','Kind']) el(id).hidden = true;
      this.view.redraw();
      document.getElementById('grid').focus({preventScroll:true});
    }

    async sync(changes, signal) {
      const doc = this.owner.data.document, book = this.book;
      const cells = changes?.map(({sheet,row,col}) => ({sheet,row,col}));
      const result = await this.owner.post('grid', {document:keyOf(doc), revision:doc.revision, book, cells, mode:'projection'}, signal);
      const current = this.owner.data?.document;
      if (this.book !== book || this.owner.context().book !== book || current?.id !== doc.id || current.binding !== doc.binding || current.revision !== doc.revision) throw Error('文件或修订已切换，请刷新修订');
      if (!cells) this.patches.clear();
      for (const [sheet, values] of Object.entries(result.cells)) {
        let items = this.patches.get(sheet);
        if (!items) this.patches.set(sheet, items = new Map());
        for (const [ref, cell] of Object.entries(values)) {
          if (cell) items.set(ref, cell); else items.delete(ref);
        }
      }
      this.affected = new Set(Object.keys(result.affected || {}));
      this.calculated = new Map(Object.entries(result.calculated || {}).map(([sheet, items]) => [sheet, new Map(Object.entries(items))]));
      this.generated = new Map();
      for (const area of result.generated || []) {
        const sheet = area.start.sheet;
        if (!this.generated.has(sheet)) this.generated.set(sheet, []);
        this.generated.get(sheet).push(area);
      }
      this.choices.setRules(result.rules);
      this.stale = false; this.owner.invalidated = false;
      if (document.body.classList.contains('editing-copy')) this.view.redraw();
    }

    // Both painters ask by original row and column, including a missing source
    // cell that now has a value. Their immutable tile payloads stay reusable.
    project(sheet, ref, base) {
      if (!this.active) return base;
      base = this.patches?.get(sheet)?.get(ref) || base;
      const calculated = this.calculated?.get(sheet)?.get(ref);
      if (calculated) return {...base, ...calculated, cacheMissing:!!calculated.cacheMissing, richText:null, valueColor:calculated.valueColor || '', conditional:null};
      if (!base) return base;
      const areas = this.generated?.get(sheet);
      if (areas) {
        const row = Number(ref.slice(ref.search(/[0-9]/))), col = base.col;
        const area = areas.find(a => row >= a.start.row && row <= a.endRow && col >= a.start.col && col <= a.endCol &&
          (a.pivot || row !== a.start.row || col !== a.start.col));
        if (area) return {...base, raw:'', display:area.pivot ? '待刷新' : '待重算', richText:null, valueColor:'', conditional:null};
      }
      if (base.hasFormula && this.affected?.has(sheet)) {
        return {...base, raw:'', display:'待重算', cacheMissing:true, richText:null, valueColor:'', conditional:null};
      }
      return base;
    }

    projectRow(sheet, row, firstCol, lastCol) {
      const source = new Map((row.cells || []).map(cell => [cell.col,cell]));
      const cells = [];
      for (let col=firstCol; col<=lastCol; col++) {
        const ref = this.owner.colName(col) + row.row;
        const cell = this.project(sheet, ref, source.get(col));
        if (cell) cells.push(cell);
      }
      return cells;
    }

    status(message, error = false) {
      this.statusNode.textContent = message || this.summary();
      this.statusNode.classList.toggle('is-error', error);
    }
    summary() {
      const doc = this.owner.data?.document;
      return this.shape?.readOnly || (doc ? '修订 ' + doc.revision + ' · ' + doc.changed + ' 格修改' + (doc.structuresChanged ? ' · ' + doc.structuresChanged + ' 项结构操作' : '') : '');
    }
    controls() {
      if (!this.active) return;
      const doc = this.owner.data.document, busy = !!(this.owner.abort || this.moving);
      el('Undo').disabled = busy || !doc.undo || !!this.owner.pending || this.stale;
      el('Redo').disabled = busy || !doc.redo || !!this.owner.pending || this.stale;
      el('Save').disabled = busy || !!this.owner.pending || this.stale;
      el('More').disabled = el('Save').disabled || !this.view.ready();
      el('Range').hidden = !!this.owner.data.encoding;
      el('Range').disabled = el('More').disabled || this.view.filtered();
      el('Exit').disabled = busy || !!this.owner.pending;
      el('Refresh').hidden = !this.stale;
      el('Retry').hidden = !this.owner.pending;
      el('Retry').disabled = el('Refresh').disabled = busy;
      const output = this.owner.data.artifacts.find(a => a.revision === doc.revision);
      const link = el('Download'); link.hidden = !output;
      if (output) { link.href = output.downloadUrl; link.download = output.name; }
    }

    focus() {
      if (this.active && !document.querySelector('dialog:modal')) this.input.focus({preventScroll:true});
    }
    selection() {
      if (!this.active || this.draft) return;
      const cell = this.selected, raw = textOf(cell);
      el('More').textContent = this.owner.tools.pivotAt(cell) ? '透视布局' : '更多编辑';
      this.formula.value = model.encode(raw, raw.includes('\r'));
      this.input.value = '';
      el('Kind').value = 'auto';
      if (!this.stale && !this.owner.pending) this.status('');
      this.choices.selection();
      this.schedulePosition();
    }
    schedulePosition() {
      if (this.active && (this.draft || !this.choices.button.hidden || this.choices.tree?.root) && !this.frame) {
        this.frame = requestAnimationFrame(() => { this.frame = 0; this.position(); this.choices.position(); });
      }
    }
    position() {
      const draft = this.draft;
      if (!draft) return;
      const found = this.view.bounds(draft.ref);
      this.input.style.opacity = found ? '' : '0';
      this.input.style.pointerEvents = found ? '' : 'none';
      if (!found) return;
      const {rect,node} = found, view = rect.viewport;
      const region = this.region.getBoundingClientRect(), style = getComputedStyle(node);
      const cellWidth = rect.right-rect.left, cellHeight = rect.bottom-rect.top;
      const font = [style.fontStyle,style.fontWeight,style.fontSize+'/1.25',style.fontFamily].join(' ');
      const formula = draft.text.startsWith('=');
      const align = formula ? 'left' : ({center:'center','flex-end':'right','flex-start':'left'})[style.justifyContent] || style.textAlign;
      const wrapped = !formula && ['pre-wrap','normal'].includes(style.whiteSpace);
      const limitWidth = wrapped ? cellWidth : view.right-view.left;
      // Let the browser shape the exact font, tabs, CJK and explicit breaks.
      // A final zero-width space gives an empty last line its caret height.
      Object.assign(this.measure.style,{font,minWidth:cellWidth+'px',maxWidth:limitWidth+'px',maxHeight:(view.bottom-view.top)+'px'});
      if (this.measuredText !== draft.text) {
        this.measure.textContent = draft.text+'\u200b';
        this.measuredText = draft.text;
      }
      const measured = this.measure.getBoundingClientRect();
      const width = Math.min(limitWidth,Math.max(cellWidth,Math.ceil(measured.width)));
      const height = Math.min(view.bottom-view.top,Math.max(cellHeight,Math.ceil(measured.height)));
      const extra = Math.max(0,height-measured.height);
      const factor = align==='center' ? 0.5 : align==='right' || align==='end' ? 1 : 0;
      const left = Math.max(view.left,Math.min(view.right-width,rect.left-(width-cellWidth)*factor));
      const top = Math.max(view.top,Math.min(view.bottom-height,rect.top));
      Object.assign(this.input.style, {
        left:(left-region.left)+'px',top:(top-region.top)+'px',width:width+'px',height:height+'px',
        font,textAlign:align,paddingTop:(style.alignItems==='flex-end' ? extra : style.alignItems==='center' ? extra/2 : 0)+'px'
      });
    }
    begin(replace, field) {
      if (this.draft) return true;
      const cell = this.selected, shape = this.shape, doc = this.owner.data?.document;
      try {
        if (!this.active || !cell || !this.view.ready() || this.stale || this.owner.pending) throw Error('请先核对当前修订');
        if (this.ranges.copied?.cut) this.ranges.cancelCut();
        const address = model.target(cell.ref, shape);
        if (shape.readOnly) throw Error(shape.readOnly);
        const raw = textOf(cell), escaped = raw.includes('\r');
        const initial = model.encode(raw, escaped);
        const d = {address, ref:cell.ref, revision:doc.revision, initial, escaped, text:replace ? '' : initial, kind:kindOf(cell)};
        this.draft = d;
        this.choices.dismiss(); this.choices.position();
        this.input.value = this.formula.value = d.text;
        this.input.classList.add('writing');
        this.position();
        if (field === this.input) {
          this.focus(); this.input.setSelectionRange(d.text.length, d.text.length);
        }
        const request = {document:keyOf(doc), revision:doc.revision, sheet:address.sheet, ref:d.ref};
        d.ready = this.owner.post('read', request).then(source => {
          if (this.draft !== d) return false;
          if (source.readOnly || this.owner.data.encoding && source.value.kind === 'missing') {
            this.cancelDraft(); this.status(source.readOnly || '该记录缺少此列', true); return false;
          }
          const unchanged = !replace && d.text === d.initial;
          d.kind = source.value.kind;
          const actual = source.value.raw;
          d.escaped = actual.includes('\r');
          d.initial = model.encode(actual, d.escaped);
          if (unchanged && d.text !== d.initial) {
            this.input.value = this.formula.value = d.text = d.initial;
            this.schedulePosition();
          }
          return true;
        }).catch(error => {
          if (this.draft === d) {
            this.stale = true; this.controls(); this.status(error.message, true);
          }
          return false;
        });
        if (escaped) this.status('格内回车显示为 \\r，反斜线显示为 \\\\');
        return true;
      } catch (error) { this.status(error.message, true); return false; }
    }
    cancelDraft() {
      this.draft = null; this.composing = false;
      this.measuredText = null; this.measure.textContent = '';
      this.input.classList.remove('writing'); this.input.removeAttribute('style');
      this.selection();
    }

    async finish() {
      const d = this.draft;
      if (this.owner.pending) {
        if (!await this.owner.commit(undefined,undefined,{direct:true})) return false;
        this.cancelDraft(); return true;
      }
      if (!d) return !this.stale;
      if (!this.dirty) { this.cancelDraft(); return true; }
      if (!await d.ready || this.draft !== d || this.stale) return false;
      const value = model.gridValue(model.decode(d.text, d.escaped), el('Kind').value, d.kind, this.owner.data.encoding, this.owner.limits);
      const initial = model.decode(d.initial, d.escaped);
      if (d.kind !== value.kind || initial !== value.raw || d.typed && value.kind === 'formula') {
        const saved = await this.owner.commit('change', [{...d.address, value}], {direct:true,inferNumberFormat:true,expandTable:!this.view.filtered()});
        if (!saved) {
          if (this.owner.data.document.revision !== d.revision) this.cancelDraft();
          return false;
        }
      }
      this.cancelDraft(); return true;
    }

    async act(next, confirmDraft = true, label = '') {
      if (this.moving || this.composing) return false;
      this.moving = true; this.controls();
      const activity = label && globalThis.ExcelLensActivity?.begin(label,{priority:0});
      let ok = false;
      try {
        if (confirmDraft && !await this.finish()) return false;
        if (next && await next() === false) return false;
        ok = true; return true;
      } catch (error) {
        if (error.status === 409) this.stale = true;
        this.status(error.message, true); return false;
      }
      finally {
        if (activity) activity.end();
        this.moving = false; this.controls();
        if (!ok) this.queue.length = 0;
        if (ok && this.queue.length) this.drain();
      }
    }
    async reload() {
      if (this.moving || this.owner.pending) return;
      const d = this.draft, kind = el('Kind').value;
      await this.owner.run('正在刷新修订…', async signal => {
        this.owner.data = await this.owner.post('state', {document:keyOf(this.owner.data.document)}, signal);
        const {sheet,ref} = this.owner.context();
        if (await this.owner.bindGridBase(this.owner.data,signal,sheet,ref)) {
          await this.enter(this.owner.data,signal);
          return;
        }
        await this.sync(undefined, signal);
        this.cancelDraft();
        if (d && this.begin(false, this.input)) {
          await this.draft.ready;
          if (this.draft) {
            this.input.value = this.formula.value = this.draft.text = d.text; el('Kind').value = kind;
            this.status('已刷新修订，请核对输入后按 Enter');
          }
        } else this.selection();
      });
      this.focus();
    }
    history(action) {
      return this.act(async () => {
        const ok = await this.owner.commit(action, undefined, {direct:true});
        if (ok) { this.selection(); this.focus(); }
        return ok;
      });
    }
    async copyForCut() {
      if (this.owner.data.encoding) { this.status('整块剪切需要 Excel 工作簿副本',true); return; }
      if (await this.view.copy(false,true)) this.status('选择粘贴位置，Esc 取消剪切');
    }
    applySelectionEntry() {
      const draft = this.draft;
      return this.act(async () => {
        if (!draft) return true;
        if (!await draft.ready || this.draft !== draft || this.stale) return false;
        const value = model.gridValue(model.decode(draft.text,draft.escaped),el('Kind').value,draft.kind,this.owner.data.encoding,this.owner.limits);
        const saved = await this.ranges.fill('fill-entry',{...draft.address,value});
        if (saved) { this.cancelDraft(); this.focus(); }
        return saved;
      }, false,'正在填充选区');
    }
    paste(e) {
      if (!this.active || this.draft || !e.clipboardData) return;
      const raw = e.clipboardData.getData('text/plain');
      const typed = this.ranges.typedClipboard(raw,e.clipboardData.getData('text/html'));
      e.preventDefault();
      if (this.moving) { this.queue.push({paste:raw,typed}); return; }
      this.pasteText(raw,typed);
    }
    pasteText(raw,typed) {
      if (!this.selected || !raw && !typed) return;
      return this.act(async () => {
        const ok = await this.ranges.paste(raw,typed);
        this.focus(); return ok;
      },true,'正在粘贴选区');
    }

    drain() {
      while (this.active && !this.moving && this.queue.length) {
        const task = this.queue.shift();
        if ('paste' in task) this.pasteText(task.paste,task.typed);
        else if ('text' in task) {
          if (!this.draft && !this.begin(true, this.input)) { this.queue.length = 0; return; }
          this.draft.text += task.text; this.input.value = this.formula.value = this.draft.text;
          this.schedulePosition();
        } else this.step(task.key, task.shift);
      }
    }
    step(key, shift) {
      const delta = {Tab:[0,shift?-1:1], Enter:[shift?-1:1,0], ArrowUp:[-1,0], ArrowDown:[1,0], ArrowLeft:[0,-1], ArrowRight:[0,1]}[key];
      const entry = key === 'Enter' && !shift && this.dirty ? this.draft.address : null;
      return this.act(async () => {
        if (!(['Tab','Enter'].includes(key) && await this.ranges.cycle(key,shift))) {
          // Appending below a totals row places the value one row above the
          // input address. Continue below the relocated total for the next entry.
          const cell = this.selected;
          if (entry && entry.sheet === this.shape?.name && cell?.col === entry.col && cell.row + 1 === entry.row) delta[0]++;
          await this.view.move(...delta,shift && key.startsWith('Arrow'));
        }
        this.focus();
      });
    }
    key(e) {
      if (!(this.active || this.rebinding && this.moving) || document.querySelector('dialog:modal')) return;
      const field = e.target, isInput = field === this.input, isFormula = field === this.formula;
      const inGrid = isInput || isFormula || field === document.getElementById('grid');
      if (this.composing || e.isComposing || e.keyCode === 229) return;
      const take = () => { e.preventDefault(); e.stopImmediatePropagation(); };
      const ctrl = e.ctrlKey || e.metaKey, key = e.key, lower = key.toLowerCase();
      if (ctrl && !e.altKey && lower === 's') { take(); this.act(() => this.owner.save()); return; }
      if (!inGrid) return;
      if (!this.draft && (key==='ContextMenu' || e.shiftKey && key==='F10')) {
        take();
        const rect=this.selected && this.view.bounds(this.selected.ref)?.rect;
        if (rect) this.ranges.showMenu(rect.left,rect.bottom);
        return;
      }
      if (!this.draft && !ctrl && e.altKey && key === 'ArrowDown') { take(); this.choices.open(); return; }
      if (this.moving) {
        if (!ctrl && !e.altKey && (key.length === 1 || ['Tab','Enter','ArrowUp','ArrowDown','ArrowLeft','ArrowRight'].includes(key))) {
          take(); this.queue.push(key.length === 1 ? {text:key} : {key,shift:e.shiftKey});
        } else if (!(ctrl && lower === 'v')) take();
        return;
      }
      if (key === 'Escape') { take(); if (!this.owner.pending) { this.cancelDraft(); this.ranges.cancelCut(); } this.focus(); return; }
      if (ctrl && !e.altKey && !e.shiftKey && !this.draft && lower === 'c') { take(); this.view.copy(); return; }
      if (ctrl && !e.altKey && !e.shiftKey && !this.draft && lower === 'x') { take(); this.copyForCut(); return; }
      if (ctrl && !e.altKey && !e.shiftKey && !this.draft && ['d','r'].includes(lower)) {
        take(); this.act(() => this.ranges.fill(lower === 'd' ? 'fill-down' : 'fill-right'),true,'正在填充选区'); return;
      }
      if (ctrl && !e.altKey && !e.shiftKey && key === 'Enter') { take(); this.applySelectionEntry(); return; }
      if (ctrl && !e.altKey && !this.draft && ['z','y'].includes(lower)) { take(); this.history(lower === 'y' || e.shiftKey ? 'redo' : 'undo'); return; }
      if (key === 'Enter' && e.altKey && this.draft) {
        take(); field.setRangeText('\n', field.selectionStart, field.selectionEnd, 'end'); field.dispatchEvent(new Event('input')); return;
      }
      if (!ctrl && !e.altKey && (key === 'Enter' || key === 'Tab' || !this.draft && key.startsWith('Arrow'))) {
        take(); this.step(key, e.shiftKey); return;
      }
      if (this.draft) return;
      if (key === 'F2') { take(); this.begin(false, this.input); return; }
      if (key === 'Delete') { take(); this.act(() => this.ranges.clear(),true,'正在清除选区'); return; }
      if (key === 'Backspace') { take(); this.begin(true,this.input); return; }
      if (!ctrl && !e.altKey && key.length === 1 && !isFormula) {
        take();
        if (this.begin(true, this.input)) {
          this.draft.text = key; this.input.value = this.formula.value = key;
          this.input.setSelectionRange(key.length,key.length); this.schedulePosition();
        }
      }
    }
  }
  window.ExcelLensSheetEditor = SheetEditor;
})();
