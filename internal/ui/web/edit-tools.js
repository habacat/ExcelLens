(() => {
  'use strict';
  const el = suffix => document.getElementById('editCopy' + suffix);

  const rangeKinds = new Set(['mergeCells', 'unmergeCells', 'fixedArray', 'spillFormula', 'clearArray']);
  const arrayFormulaKinds = new Set(['fixedArray', 'spillFormula']);

  class EditTools {
    constructor(editor) {
      this.editor = editor;
      this.calculatedItems = new window.ExcelLensPivotCalculatedItems(item => this.commit({kind:'pivotCalculatedItem', pivot:el('Pivot').value, calculatedItem:item}));
      this.valueFilters = new window.ExcelLensPivotValues(filter => this.commit({kind:'pivotValueFilter', pivot:el('Pivot').value, valueFilter:filter}));
      this.pivotLayout = new window.ExcelLensPivotLayout(editor);
      this.selected = 'Copy';
      this.tabs = [...el('Tools').querySelectorAll('[role="tab"]')];
      this.options = new Map(this.tabs.map(tab => {
        const key = tab.id.slice('editCopyTab'.length);
        const option = new Option(tab.textContent, key);
        el('ToolSelect').add(option);
        tab.addEventListener('click', () => this.select(key));
        tab.addEventListener('keydown', event => this.navigate(event, tab));
        return [key, {tab, option, pane:document.getElementById(tab.getAttribute('aria-controls'))}];
      }));
      el('ToolSelect').addEventListener('change', () => this.select(el('ToolSelect').value));
      el('StructureKind').addEventListener('change', () => {
        this.structureFields(editor.cell);
        editor.controls();
      });
      for (const id of ['NewName', 'At', 'Count', 'Before', 'Range', 'ArrayFormula', 'ReplaceContents']) el(id).addEventListener('input', () => {
        if (id === 'Range') el('ReplaceContents').checked = false;
        editor.controls();
      });
      el('ApplyStructure').addEventListener('click', () => this.commit(this.structureInput()));
      el('Pivot').addEventListener('change', () => {
        this.pivotFields();
        editor.controls();
      });
      el('PivotFilter').addEventListener('change', () => editor.controls());
      el('PivotItem').addEventListener('input', () => editor.controls());
      el('PivotRefresh').addEventListener('click', () => this.commit({kind:'refreshPivot', pivot:el('Pivot').value}));
      el('PivotApply').addEventListener('click', () => this.commit({
        kind:'configurePivot', pivot:el('Pivot').value, axes:this.pivotLayout.axes,
        data:el('PivotData').value, aggregate:el('PivotAggregate').value, filterMode:el('PivotFilter').value,
        item:el('PivotFilter').value === 'item' ? el('PivotItem').value : ''
      }));
      el('Charts').addEventListener('click', () => this.commit({kind:'refreshCharts'}));
    }

    async commit(change) {
      if (!change) return;
      const returnToGrid = !!change.range || this.contextualPivot && ['configurePivot', 'refreshPivot', 'pivotValueFilter', 'pivotCalculatedItem'].includes(change.kind);
      const saved = await this.editor.commitStructure({...change, sheet:el('Sheet').value});
      if (saved && returnToGrid) this.editor.close();
    }

    pivotAt(cell) {
      if (!cell) return null;
      const sheet = this.editor.context().sheet;
      return this.editor.data?.pivots?.find(p => p.sheet === sheet && p.areas?.some(area =>
        cell.row >= area.start.row && cell.row <= area.endRow && cell.col >= area.start.col && cell.col <= area.endCol));
    }

    openPivot(pivot) {
      this.selected = 'Objects';
      el('Pivot').value = pivot.name;
      this.pivotFields();
    }

    select(key) {
      this.selected = key;
      this.editor.clearPreview();
      this.editor.controls();
    }

    navigate(event, tab) {
      const visible = this.tabs.filter(item => !item.hidden);
      const index = visible.indexOf(tab), count = visible.length;
      const next = {ArrowRight:(index + 1) % count, ArrowLeft:(index + count - 1) % count, Home:0, End:count - 1}[event.key];
      if (next === undefined) return;
      event.preventDefault();
      visible[next].click();
      visible[next].focus();
    }

    controls(blocked) {
      const editor = this.editor;
      const excel = !!editor.data && !editor.data.encoding;
      const structure = excel && editor.grid.active && !!editor.shape;
      const objects = structure && (!!this.pivots?.length || this.charts);
      const available = {Copy:editor.editable, Paste:editor.editable, Fill:editor.editable,
        Rules:editor.editable && excel && !editor.data.structured, Structure:structure, Objects:objects};
      const active = available[this.selected] ? this.selected : Object.keys(available).find(key => available[key]);
      el('Batch').hidden = !active;
      for (const [key, {tab, option, pane}] of this.options) {
        tab.hidden = option.hidden = option.disabled = !available[key];
        tab.disabled = blocked;
        const selected = key === active;
        tab.setAttribute('aria-selected', String(selected));
        tab.tabIndex = selected ? 0 : -1;
        pane.hidden = !selected;
      }
      el('ToolSelect').value = active || '';
      el('ToolSelect').disabled = blocked;
      if (active === 'Objects' && !blocked) this.resolvePivotLayout();
      for (const id of ['StructureKind','NewName','At','Count','Before','Range','ArrayFormula','ReplaceContents','Pivot','PivotFields','PivotData','PivotAggregate','PivotFilter','PivotItem','PivotRefresh','PivotApply','Charts']) {
        el(id).disabled = blocked || editor.invalidated;
      }
      el('PivotFilter').parentElement.hidden = !this.pivotLayout.axes?.rows.length;
      el('PivotItem').hidden = el('PivotFilter').parentElement.hidden || el('PivotFilter').value !== 'item';
      if (!el('PivotReason').hidden) el('PivotRefresh').disabled = true;
      if (active === 'Objects' && this.meta?.key === this.pivotKey() && (this.meta.pending || this.meta.error)) {
        el('PivotFields').disabled = el('PivotRefresh').disabled = true;
      }
      const applyingLayout = active === 'Objects' && !el('Work').hidden && !el('PivotFields').hidden;
      el('PivotApply').hidden = !applyingLayout;
      el('PivotApply').disabled = el('PivotFields').disabled || !el('PivotItem').hidden && !el('PivotItem').value;
      el('Save').classList.toggle('primary-button', !applyingLayout);
      el('Save').classList.toggle('mini-button', applyingLayout);
      el('ApplyStructure').disabled = blocked || editor.invalidated || !this.structureInput();
    }

    bindCell(cell) {
      if (this.meta?.error) this.meta=null;
      const sheet = el('Sheet').value;
      if (cell && this.chooseRangeKind) {
        this.chooseRangeKind = false;
        if (cell.array) el('StructureKind').value = cell.array.dynamic ? 'spillFormula' : 'fixedArray';
      }
      this.structureFields(cell);
      this.pivots = (this.editor.data?.pivots || []).filter(pivot => pivot.sheet === sheet);
      this.charts = this.editor.data?.chartSheets?.includes(sheet);
      el('Charts').hidden = !this.charts;
      el('PivotTools').hidden = !this.pivots.length;
      const previous = el('Pivot').value;
      el('Pivot').replaceChildren(...this.pivots.map(p => new Option(p.name, p.name)));
      if (this.pivots.some(p => p.name === previous)) el('Pivot').value = previous;
      this.pivotFields();
    }

    pivotFields() {
      const saved = this.pivots.find(p => p.name === el('Pivot').value);
      const meta = this.meta?.key === this.pivotKey() ? this.meta : null;
      const info = meta?.info || saved;
      el('PivotReason').textContent = meta?.error || info?.refreshReason || '';
      el('PivotReason').hidden = !el('PivotReason').textContent;
      el('PivotFields').hidden = !info?.data || !!info.refreshReason;
      this.pivotLayout.bind(el('PivotFields').hidden ? null : info);
      this.valueFilters.bind(el('PivotFields').hidden ? null : info);
      this.calculatedItems.bind(el('PivotFields').hidden ? null : info);
      if (el('PivotFields').hidden) return;
      el('PivotData').replaceChildren(...info.fields.map(field => new Option(field, field)));
      el('PivotData').value = info.data;
      el('PivotAggregate').value = info.aggregate;
      el('PivotFilter').value = 'keep';
      el('PivotItem').value = '';
      el('PivotItem').hidden = true;
    }

    pivotKey() {
      const doc = this.editor.data?.document;
      return JSON.stringify([doc?.id, doc?.revision, el('Sheet').value, el('Pivot').value]);
    }

    async resolvePivotLayout() {
      const saved = this.pivots?.find(p => p.name === el('Pivot').value);
      const key = this.pivotKey();
      if (!saved?.axes || saved.refreshReason || this.meta?.key === key) return;
      this.meta?.controller.abort();
      const meta = {key, pending:true, controller:new AbortController()};
      this.meta = meta;
      const doc = this.editor.data.document;
      try {
        meta.info = await this.editor.post('pivot-layout', {document:{id:doc.id,binding:doc.binding}, revision:doc.revision, sheet:saved.sheet, pivot:saved.name}, meta.controller.signal);
      } catch (error) { meta.error = error.message; }
      if (this.meta !== meta || this.pivotKey() !== key) return;
      meta.pending = false;
      this.pivotFields();
      this.editor.controls();
    }

    structureFields(cell) {
      const kind = el('StructureKind').value;
      const rename = kind === 'renameSheet', rows = kind.endsWith('Rows');
      const range = rangeKinds.has(kind);
      el('RangeField').hidden = !range;
      el('ArrayFormulaField').hidden = !arrayFormulaKinds.has(kind);
      el('ArrayFormula').value = cell?.array?.formula || (cell?.value?.kind === 'formula' ? cell.value.raw : '');
      el('MergeConsent').hidden = kind !== 'mergeCells' && kind !== 'fixedArray';
      el('ConsentLabel').textContent = kind === 'fixedArray' ? '替换选区内已有内容' : '若有多个值，仅保留首个有内容的格';
      el('ReplaceContents').checked = false;
      const selected = this.editor.grid.active && this.editor.context().sheet === el('Sheet').value ? this.editor.grid.view.range() : cell?.ref || '';
      const fullArray = cell?.array?.range;
      el('Range').value = fullArray && (!selected.includes(':') || selected === fullArray) && (arrayFormulaKinds.has(kind) || kind === 'clearArray')
        ? (kind === 'spillFormula' ? fullArray.split(':')[0] : fullArray) : selected;
      el('NewNameField').hidden = !rename;
      el('AtField').hidden = el('CountField').hidden = rename||range;
      el('BeforeField').hidden = !kind.startsWith('move');
      el('AtLabel').textContent = rows ? '起始行号' : '起始列号';
      el('At').value = cell ? (rows ? cell.address.row : cell.address.col) : '';
      el('NewName').value = el('Sheet').value;
      el('ApplyStructure').textContent = el('StructureKind').selectedOptions[0].textContent;
    }

    structureInput() {
      const sheet = el('Sheet').value, kind = el('StructureKind').value;
      if (!sheet) return null;
      if (rangeKinds.has(kind)) {
        const range = el('Range').value.trim().toUpperCase(), parts = range.split(':');
        if (parts.length > 2) return null;
        try {
          const [a, b = a] = parts.map(ref => window.ExcelLensEditModel.target(ref, this.editor.shape));
          if (a.row > b.row || a.col > b.col || kind === 'mergeCells' && a.row === b.row && a.col === b.col) return null;
        } catch { return null; }
        const result = {kind, range};
        if (arrayFormulaKinds.has(kind)) {
          result.formula = el('ArrayFormula').value.trim();
          if (!result.formula.startsWith('=') || result.formula.length < 2) return null;
        }
        if (!el('MergeConsent').hidden && el('ReplaceContents').checked) result.replaceContents = true;
        return result;
      }
      if (kind === 'renameSheet') {
        const name = el('NewName').value;
        return name.trim() && name !== sheet ? {kind, name} : null;
      }
      const at = el('At').valueAsNumber, count = el('Count').valueAsNumber;
      const limit = kind.endsWith('Rows') ? this.editor.shape?.rows : this.editor.shape?.cols;
      if (!limit || !Number.isInteger(at) || !Number.isInteger(count) || at < 1 || count < 1 || at + count - 1 > limit) return null;
      const result = {kind, at, count};
      if (kind.startsWith('move')) {
        const before = el('Before').valueAsNumber;
        if (!Number.isInteger(before) || before < 1 || before > limit + 1 || (before >= at && before <= at + count)) return null;
        result.before = before;
      }
      return result;
    }
  }

  window.ExcelLensEditTools = EditTools;
})();
