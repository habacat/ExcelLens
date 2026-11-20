(() => {
  'use strict';
  const model = window.ExcelLensFormulaInput;
  const inspector = window.ExcelLensFormulaInspector;
  const handles = '.grid-cell[data-ref],.column-head[data-col],.row-head,.pinned-row-head';
  const controls = '.column-filter,.auto-filter-button,.column-resize-grip,.row-resize-grip';

  class WorksheetFormulaEntry {
    constructor(editor) {
      this.grid = editor;
      this.refs = [];
      this.sequence = 0;
      this.completion = new window.ExcelLensWorksheetCompletion(editor, (field, from, to, value, offerNext) => {
        this.record(field);
        this.replace(field, from, to, value);
        this.changed(field, false, offerNext);
      });
      const region = editor.region;
      region.addEventListener('pointermove', e => this.move(e));
      region.addEventListener('pointerup', e => this.end(e, false));
      region.addEventListener('pointercancel', e => this.end(e, true));
      region.addEventListener('lostpointercapture', e => this.end(e, true));
      for (const field of [editor.input, editor.formula]) {
        field.addEventListener('blur', () => this.completion.hide());
        field.addEventListener('click', () => this.caret(field));
        field.addEventListener('keyup', e => {
          if (['Home','End','ArrowLeft','ArrowRight'].includes(e.key) && !this.pick) this.caret(field);
        });
      }
    }
    get allowsFormula() {
      const g = this.grid, kind = document.getElementById('editGridKind').value;
      return !!g.draft && !g.owner.data?.encoding && (kind === 'auto' || kind === 'formula');
    }
    get enabled() { return this.allowsFormula && this.grid.draft.text.startsWith('='); }
    get field() { return this.grid.draft?.field || this.grid.input; }
    snapshot(f) {
      const pick = this.currentPick(f);
      return {value:f.value,start:f.selectionStart,end:f.selectionEnd,pick:pick && {...pick}};
    }
    record(f, before = this.snapshot(f)) {
      const d = this.grid.draft;
      const journal = d.formulaHistory ??= {undo:[],redo:[]};
      if (journal.undo.at(-1)?.value !== before.value) {
        journal.undo.push(before);
        if (journal.undo.length > 64) journal.undo.shift();
      }
      journal.redo.length = 0;
    }
    history(f, redo) {
      const journal = this.grid.draft?.formulaHistory;
      if (!journal) return false;
      const from = redo ? journal.redo : journal.undo;
      const before = from.pop();
      if (before) {
        (redo ? journal.undo : journal.redo).push(this.snapshot(f));
        this.replace(f,0,f.value.length,before.value);
        f.setSelectionRange(before.start,before.end);
        this.pick = before.pick;
        this.changed(f,true);
      }
      return true;
    }
    beforeInput(e, f) {
      if (this.grid.composing || !this.grid.draft) return;
      if (e.inputType === 'historyUndo' || e.inputType === 'historyRedo') {
        if (this.history(f,e.inputType === 'historyRedo')) e.preventDefault();
        return;
      }
      if (this.enabled || this.grid.draft.formulaHistory || this.allowsFormula && f.selectionStart === 0 && e.data?.startsWith('=')) this.record(f);
    }
    canPoint() {
      if (!this.enabled || this.grid.composing) return false;
      const f = this.field, a = f.selectionStart, b = f.selectionEnd, context = model.context(f.value, a);
      if (!context) return false;
      return a !== b || context.operand || !!this.currentPick(f);
    }
    currentPick(field) {
      const p = this.pick;
      return p && field.selectionStart === field.selectionEnd && field.selectionEnd === p.end &&
        field.value.slice(p.from, p.end) === p.text ? p : null;
    }
    replace(field, from, to, value) {
      field.setRangeText(value, from, to, 'end');
      const g = this.grid;
      g.draft.text = field.value;
      g.draft.typed = true;
      g.draft.field = field;
      (field === g.input ? g.formula : g.input).value = field.value;
      g.schedulePosition();
    }
    changed(field, keepPick = false, offerNext = true) {
      if (!keepPick) { ++this.sequence; this.pick = null; }
      const draft = this.grid.draft;
      if (draft) draft.field = field;
      const hadRefs = this.refs.length;
      this.refs = this.enabled ? inspector.parseFormula(draft.text, draft.address.sheet) : [];
      if (hadRefs || this.refs.length) this.grid.view.paintReferences();
      if (offerNext && this.enabled && !this.grid.composing && !this.gesture) this.completion.update(field, draft.text);
      else this.completion.hide();
    }
    caret(field) {
      if (!this.enabled) return;
      if (!this.currentPick(field)) this.pick = null;
      this.grid.draft.field = field;
      this.completion.update(field, field.value);
    }
    areas(sheet) {
      return this.refs.filter(r => !r.external && r.sheet.toLowerCase() === sheet.toLowerCase()).map(r => ({
        row1:r.start.row, col1:r.start.col, row2:r.end.row, col2:r.end.col,
        color:inspector.PALETTE[r.colorIndex]
      }));
    }
    switchDraft(name) {
      const g = this.grid;
      if (g.moving || g.stale || g.owner.pending || !this.canPoint()) return null;
      if (name !== g.owner.context().sheet) {
        ++this.sequence;
        const f = this.field, a = f.selectionStart, b = f.selectionEnd;
        g.formula.focus({preventScroll:true});
        g.formula.setSelectionRange(a,b);
        this.completion.hide();
      }
      return g.draft;
    }
    focusAfterSwitch() {
      const g = this.grid, d = g.draft;
      if (!this.enabled) { g.focus(); return; }
      const last = this.field;
      const start = last.selectionStart, end = last.selectionEnd;
      const foreign = g.owner.context().sheet !== d.address.sheet;
      const next = foreign ? g.formula : last;
      next.focus({preventScroll:true});
      next.setSelectionRange(start,end);
      d.field = next;
      g.status(foreign ? '正在编辑 ' + d.address.sheet + '!' + d.ref : '');
      g.schedulePosition();
      this.changed(next,true);
    }
    reset() {
      ++this.sequence;
      this.stop();
      this.pick = null;
      this.refs = [];
      this.completion.hide();
      this.grid.view.paintReferences();
    }
    point(node) {
      if (!node) return null;
      if (node.dataset.ref) {
        const p = window.ExcelLensAddress.parseRef(node.dataset.ref, Infinity, Infinity);
        return p && {...p, kind:'cell', visual:Number(node.dataset.visualIndex) || p.row};
      }
      if (node.classList.contains('column-head')) return {kind:'column', col:Number(node.dataset.col)};
      const row = Number(node.closest('[data-row]')?.dataset.row || node.textContent);
      return row > 0 ? {kind:'row',row} : null;
    }
    hit(x, y) {
      for (const element of document.elementsFromPoint(x,y)) {
        if (!this.grid.region.contains(element)) continue;
        const node = element.closest(handles);
        if (node) return this.point(node);
      }
      return null;
    }
    begin(event) {
      this.handledClick = false;
      const g = this.grid;
      if (event.button !== 0 || this.gesture || !this.canPoint() || g.moving || g.stale || g.owner.pending || !g.view.ready() || event.target.closest(controls)) return false;
      if (event.target === g.input) {
        const r = g.view.bounds(g.draft.ref)?.rect;
        if (r && event.clientX >= r.left && event.clientX < r.right && event.clientY >= r.top && event.clientY < r.bottom) return false;
      }
      const point = this.point(event.target.closest(handles)) || (event.target === g.input && this.hit(event.clientX,event.clientY));
      if (!point) return false;
      ++this.sequence;
      const f = this.field, previous = this.currentPick(f), sheet = g.owner.context().sheet;
      this.gesture = {id:event.pointerId, before:this.snapshot(f),
        anchor:event.shiftKey && previous?.sheet === sheet && point.kind === previous.anchor.kind ? previous.anchor : point,
        x:event.clientX, y:event.clientY};
      this.completion.hide();
      this.writePoint(this.gesture.anchor, point, (event.ctrlKey || event.metaKey) && !!previous);
      this.handledClick = true;
      g.region.setPointerCapture(event.pointerId);
      return true;
    }
    writePoint(anchor, point, append = false) {
      const g = this.grid, f = this.field, sheet = g.owner.context().sheet;
      const prev = this.currentPick(f);
      let from = prev && !append ? prev.from : Math.max(1, f.selectionStart);
      const to = Math.max(from, f.selectionEnd);
      const ref = model.reference(anchor, point, sheet, g.draft.address.sheet, append ? '' : prev?.text);
      if (!append && prev?.text === ref && prev.sheet === sheet) return;
      if (!this.gesture) this.record(f);
      this.replace(f, from, to, (append ? ',' : '') + ref);
      if (append) from++;
      this.pick = {from, end:f.selectionEnd, text:ref, anchor, point, sheet};
      g.draft.pointKeys = true;
      this.changed(f,true);
    }
    move(e) {
      const drag = this.gesture;
      if (!drag || e.pointerId !== drag.id) return;
      e.preventDefault();
      drag.x = e.clientX; drag.y = e.clientY;
      if (!this.frame) this.frame = requestAnimationFrame(() => this.track());
    }
    track() {
      this.frame = 0;
      const drag = this.gesture;
      if (!drag) return;
      const pos = this.grid.view.dragPoint(drag.x, drag.y, drag.anchor.kind);
      const p = this.hit(pos.x,pos.y);
      if (p && p.kind === drag.anchor.kind) this.writePoint(drag.anchor,p);
      if (pos.scrolling) this.frame = requestAnimationFrame(() => this.track());
    }
    stop() {
      const id = this.gesture?.id;
      this.gesture = null;
      cancelAnimationFrame(this.frame); this.frame = 0;
      if (id != null && this.grid.region.hasPointerCapture(id)) this.grid.region.releasePointerCapture(id);
    }
    end(e, cancel) {
      const drag = this.gesture;
      if (!drag || e.pointerId !== drag.id) return;
      const f = this.field;
      if (!cancel) {
        const pos = this.grid.view.dragPoint(e.clientX,e.clientY,drag.anchor.kind,false);
        const p = this.hit(pos.x,pos.y);
        if (p && p.kind === drag.anchor.kind) this.writePoint(drag.anchor,p);
        if (drag.before.value !== f.value) this.record(f,drag.before);
      } else {
        this.replace(f, 0, f.value.length, drag.before.value);
        f.setSelectionRange(drag.before.start,drag.before.end);
        this.pick = drag.before.pick;
      }
      this.stop();
      f.focus({preventScroll:true});
      this.changed(f,true);
    }
    key(e) {
      if (this.grid.draft && (e.ctrlKey || e.metaKey) && !e.altKey && ['z','y'].includes(e.key.toLowerCase())) {
        return this.history(this.field, e.key.toLowerCase() === 'y' || e.shiftKey);
      }
      if (!this.enabled) return false;
      if (this.gesture && e.key === 'Escape') { this.end({pointerId:this.gesture.id},true); return true; }
      if (this.completion.key(e)) return true;
      const g = this.grid, f = this.field;
      if (e.key === 'F4' && !e.ctrlKey && !e.metaKey && !e.altKey) {
        const p = this.currentPick(f);
        const replacement = model.absolute(f.value, p?.from ?? f.selectionStart, f.selectionEnd, g.draft.address.sheet);
        if (replacement) {
          const {from,until,value} = replacement;
          this.record(f);
          this.replace(f,from,until,value);
          if (p) {
            p.end += value.length - (until - from);
            p.text = f.value.slice(p.from,p.end);
            f.setSelectionRange(p.end,p.end);
          }
          this.changed(f,!!p);
        }
        return true;
      }
      if (e.key === 'F2') { g.draft.pointKeys = !g.draft.pointKeys; this.pick = null; return true; }
      if (!g.draft.pointKeys || !this.canPoint() || !e.key.startsWith('Arrow') || e.ctrlKey || e.metaKey || e.altKey || !g.view.ready()) return false;
      this.step(e.key,e.shiftKey);
      return true;
    }
    step(key, extend) {
      const g = this.grid, d = g.draft, sheet = g.owner.context().sheet, sequence = this.sequence;
      const current = () => g.draft === d && g.owner.context().sheet === sheet && this.sequence === sequence;
      const move = () => {
        if (!current()) return;
        const previous = this.currentPick(this.field);
        const local = previous?.sheet === sheet && previous.point.kind === 'cell';
        const base = local ? previous.point : g.selected;
        if (!base) return;
        const anchor = local ? previous.anchor : {...base,kind:'cell'};
        const apply = point => { if (point && current()) this.writePoint(extend ? anchor : point,point); };
        const point = g.view.referenceStep(base,key);
        if (point?.then) return point.then(apply);
        apply(point);
      };
      const task = this.pendingStep ? this.pendingStep.then(move) : move();
      if (task) {
        const handled = task.catch(error => { if (current()) g.status(error.message,true); return false; });
        this.pendingStep = handled;
        handled.then(() => { if (this.pendingStep === handled) this.pendingStep = null; });
      }
    }
  }
  window.ExcelLensWorksheetFormulaEntry = WorksheetFormulaEntry;
})();
