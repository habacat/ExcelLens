(() => {
  'use strict';
  const el = suffix => document.getElementById(`editCopy${suffix}`);
  const model = window.ExcelLensEditModel;
  const keyOf = ({id, binding}) => ({id, binding});
  const equals = (a, b) => a.kind === b.kind && a.raw === b.raw;
  const ruleInputs = ['RuleStart', 'RuleEnd', 'RuleType', 'RuleOperator', 'RuleFirst', 'RuleSecond', 'RuleList', 'RuleReference', 'AllowBlank'];

  class EditCopy {
    constructor({context, api, openDialog, adopt, canOpen, limits, colName, fmtBytes, grid}) {
      Object.assign(this, {context, api, openDialog, adopt, canOpen, limits, colName, fmtBytes});
      this.extensions = new Set(limits.extensions);
      this.grid = new window.ExcelLensSheetEditor(this, grid);
      el('Open').addEventListener('click', () => this.show());
      el('Close').addEventListener('click', () => this.close());
      el('Dialog').addEventListener('cancel', event => { event.preventDefault(); this.close(); });
      el('Cancel').addEventListener('click', () => this.abort?.abort());
      el('ListRefresh').addEventListener('click', () => this.list());
      el('History').addEventListener('click', () => this.list());
      el('Refresh').addEventListener('click', () => this.refresh());
      el('AddressForm').addEventListener('submit', event => { event.preventDefault(); this.read(); });
      el('Sheet').addEventListener('change', () => { this.selectFirst(); this.read(); });
      el('Address').addEventListener('input', () => { this.clearPreview(); this.controls(); });
      el('Value').addEventListener('input', () => this.controls());
      el('Kind').addEventListener('change', () => this.controls());
      el('Value').addEventListener('keydown', event => {
        if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && !event.isComposing && event.keyCode !== 229) {
          event.preventDefault(); this.applyCell();
        }
      });
      el('DiscardInput').addEventListener('click', () => this.setCell(this.cell));
      el('Apply').addEventListener('click', () => this.applyCell());
      el('Undo').addEventListener('click', () => this.commit('undo'));
      el('Redo').addEventListener('click', () => this.commit('redo'));
      this.tools = new window.ExcelLensEditTools(this);
      el('Copy').addEventListener('click', () => this.copy());
      el('BatchText').addEventListener('input', () => { this.pasteRaw = null; this.clearPreview(); });
      el('BatchText').addEventListener('paste', event => {
        const raw = event.clipboardData?.getData('text/plain');
        if (!raw || raw.length > limits.requestBytes) return;
        event.preventDefault(); el('BatchText').value = raw; this.pasteRaw = raw; this.clearPreview();
      });
      for (const id of ['SkipBlank', 'FillEnd', 'FillStep', 'FillMode', 'PasteMode']) el(id).addEventListener('input', () => { this.clearPreview(); this.controls(); });
      el('FillMode').addEventListener('change', () => this.fillControls());
      el('PreviewPaste').addEventListener('click', () => this.preview('paste'));
      el('PreviewFill').addEventListener('click', () => this.preview('fill'));
      el('ApplyBatch').addEventListener('click', () => {
        const plan = this.previewed;
        if (plan) this.commit(plan.rule ? 'rule' : 'change', plan.changes, {proof:plan.proof,rule:plan.rule});
      });
      el('PreviewRule').addEventListener('click', () => this.previewRule());
      for (const id of ruleInputs) el(id).addEventListener('input', () => { this.clearPreview(); this.ruleControls(); });
      el('ReadChoices').addEventListener('click', () => this.readChoices());
      el('Choice').addEventListener('input', () => this.controls());
      el('ApplyChoice').addEventListener('click', () => {
        const raw = el('Choice').value;
        if (raw && this.editable && !this.dirty && !equals(this.cell.value, {kind: 'text', raw})) this.commit('change', [{...this.cell.address, value: {kind: 'text', raw}}]);
      });
      el('Save').addEventListener('click', () => this.save());
      window.addEventListener('beforeunload', event => {
        if (this.grid.dirty || this.pending || el('Dialog').open && this.dirty) { event.preventDefault(); event.returnValue = ''; }
      });
    }

    availability(active) {
      const {fileName, source} = this.context();
      const dot = fileName.lastIndexOf('.');
      el('Open').hidden = !active || dot < 0 || !this.extensions.has(fileName.slice(dot).toLowerCase()) || !!source?.incomplete || !!source?.archiveMember;
    }

    post(command, body, signal) {
      return this.api(`/api/edits/${command}`, {method: 'POST', signal, headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    }

    status(message, error = false) {
      el('Status').textContent = message;
      el('Status').classList.toggle('is-error', error);
      if (this.grid.active || this.starting) this.grid.status(message, error);
    }

    async run(message, action, cancellable = true) {
      if (this.abort) return false;
      const controller = new AbortController();
      this.abort = controller;
      const activity = globalThis.ExcelLensActivity?.begin(message, {signal:controller.signal});
      this.status(message); this.controls(); el('Cancel').hidden = !cancellable;
      try { await action(controller.signal); return true; }
      catch (error) {
        if (error?.name === 'AbortError') this.status('已请求取消。可刷新核对保存状态。');
        else this.status(error.message || String(error), true);
        if (error.status === 409 && this.grid.active) this.grid.stale = true;
        return false;
      } finally {
        activity?.end();
        el('Cancel').hidden = true; this.abort = null; this.controls();
      }
    }

    close() {
      if (this.abort) { this.status('请等待当前操作完成'); return; }
      if (this.dirty || this.pending) { this.status('请先应用或放弃当前输入', true); return; }
      el('Dialog').close();
      if (this.grid.active) { this.grid.selection(); this.grid.focus(); }
    }

    async show() {
      const {book, sheet, ref} = this.context();
      if (!book || this.abort || this.grid.active || el('Dialog').open) return;
      this.starting = true;
      el('Open').disabled = true; el('Open').textContent = '正在检查…';
      const ok = await this.run('正在检查编辑源…', async signal => {
        const data = await this.post('start', {book}, signal);
        if (this.context().book !== book || document.body.classList.contains('home-view')) throw Error('当前文件已切换');
        if (!await this.bindGridBase(data, signal, sheet, ref)) await this.activate(data, sheet, ref, signal, false);
        await this.grid.enter(data, signal);
      });
      this.starting = false;
      el('Open').disabled = false; el('Open').textContent = '编辑副本';
      if (!ok && this.context().book === book && !document.body.classList.contains('home-view')) {
        el('Title').textContent = '编辑副本';
        el('Work').hidden = true; el('ListView').hidden = false;
        this.openDialog('editCopyDialog');
      }
    }

    advanced(tool) {
      if (!this.grid.view.ready()) return false;
      const table=tool==='Table';
      const range = tool === 'Range';
      const pivot = !table && !range && this.tools.pivotAt(this.grid.selected);
      this.tools.contextualPivot = !!pivot;
      el('Title').textContent = table ? '调整表格范围' : range ? '区域编辑' : pivot ? '透视布局' : '更多编辑';
      this.openDialog('editCopyDialog');
      el('Work').hidden = false; el('ListView').hidden = true;
      const {sheet, ref} = this.context();
      el('Sheet').value = sheet; el('Address').value = ref || '';
      this.cell = null; this.clearPreview(); this.setCell(null);
      if (pivot) this.tools.openPivot(pivot);
      if (table) { this.tools.selected='Structure'; el('StructureKind').value='resizeTable'; }
      if (range) {
        this.tools.selected = 'Structure';
        this.tools.chooseRangeKind = true;
        el('StructureKind').value = this.grid.view.merged() ? 'unmergeCells' : 'mergeCells';
      }
      return this.run('正在读取选区…', signal=>this.fetchCell(signal)).then(ok=>{
        if (ok && table && el('Dialog').open) { el('Range').focus(); el('Range').select(); }
        return ok;
      });
    }

    list() {
      if (this.abort || this.dirty || this.pending) return;
      el('Work').hidden = true;
      el('ListView').hidden = false;
      el('Title').textContent = '已保存的编辑';
      this.run('正在读取编辑副本…', async signal => {
        const {items} = await this.api('/api/edits', {signal});
        el('List').replaceChildren();
        for (const item of items) {
          const line = this.item(item.name || '编辑副本', item.error || (item.busy ? '正在使用' : `修订 ${item.revision}`));
          if (!item.error) line.append(this.button('继续编辑', () => this.resume(item)));
          el('List').append(line);
        }
        this.status(items.length ? '' : '暂无已保存的编辑');
      });
    }

    resume(item) {
      this.run('正在恢复编辑修订…', async signal => {
        const data = await this.post('state', {document: keyOf(item)}, signal);
        const {sheet, ref} = this.context();
        if (!await this.bindGridBase(data, signal, sheet, ref)) await this.activate(data, sheet, ref, signal, false);
        await this.grid.enter(data, signal);
        el('Dialog').close(); this.grid.focus();
      });
    }

    async activate(data, preferredSheet, preferredRef, signal, read = true) {
      if (this.data?.document.id !== data.document.id) this.tools.selected = 'Copy';
      this.data = data; this.cell = null; this.pending = null; this.invalidated = false;
      this.clearPreview(); this.pasteRaw = null; el('BatchText').value = '';
      el('CopyEnd').value = ''; el('PasteMode').value = 'plain';
      for (const option of el('PasteMode').options) if (['formulas', 'styles'].includes(option.value)) option.disabled = !!data.encoding;
      el('Kind').querySelector('[value="formula"]').disabled = !!data.encoding;
      el('FillMode').value = 'constant'; el('FillEnd').value = ''; el('FillStep').value = '1';
      el('FillMode').querySelector('[value="formula-fill"]').disabled = !!data.encoding;
      this.fillControls();
      el('Work').hidden = false; el('ListView').hidden = true; el('CellPanel').hidden = true;
      el('Name').textContent = data.document.name;
      el('Sheet').replaceChildren(...data.sheets.map(sheet => new Option(sheet.name, sheet.name)));
      if (data.sheets.some(s => s.name === preferredSheet && s.rows && s.cols)) el('Sheet').value = preferredSheet;
      else el('Sheet').value = (data.sheets.find(s => s.rows && s.cols && !s.readOnly) || data.sheets.find(s => s.rows && s.cols) || data.sheets[0])?.name || '';
      const shape = this.shape;
      try { model.target(preferredRef || '', shape); el('Address').value = preferredRef; }
      catch { this.selectFirst(); }
      el('TextOptions').hidden = !data.encoding;
      el('Encoding').options[0].textContent = data.encoding ? `源编码（${data.encoding}）` : '源编码';
      el('Encoding').value = 'source'; el('Protect').checked = true;
      this.renderState();
      if (read) await this.fetchCell(signal);
    }

    get shape() { return this.data?.sheets.find(s => s.name === el('Sheet').value); }
    get loaded() { return !!this.cell && !this.invalidated && this.cell.address.sheet === el('Sheet').value && this.cell.ref === el('Address').value.trim().toUpperCase(); }
    get editable() { return this.loaded && !this.cell.readOnly && !(this.data.encoding && this.cell.value.kind === 'missing'); }
    get dirty() { return this.loaded && (el('Value').value !== this.initialText || el('Kind').value !== this.initialKind); }

    selectFirst() {
      const {firstCol, firstRow} = this.shape || {};
      el('Address').value = firstCol && firstRow ? this.colName(firstCol) + firstRow : '';
    }

    controls() {
      this.grid.controls();
      const busy = !!this.abort, dirty = this.dirty, blocked = busy || dirty || !!this.pending;
      const doc = this.data?.document;
      el('Close').disabled = busy;
      for (const id of ['Sheet', 'Address', 'Read', 'History']) el(id).disabled = blocked;
      el('Refresh').disabled = busy;
      el('Undo').disabled = blocked || !doc?.undo || this.invalidated;
      el('Redo').disabled = blocked || !doc?.redo || this.invalidated;
      el('Value').disabled = busy || !!this.pending || !this.editable || el('Kind').value === 'blank';
      el('Kind').disabled = busy || !!this.pending || !this.editable;
      el('Apply').textContent = this.pending ? '核对提交' : '应用修改';
      el('Apply').hidden = !!this.cell?.readOnly && !this.pending;
      el('Apply').disabled = busy || !this.pending && (!this.editable || !dirty);
      el('DiscardInput').hidden = !dirty || !!this.pending;
      el('DiscardInput').disabled = busy;
      el('Save').disabled = blocked || !doc || this.invalidated;
      for (const button of el('Outputs').querySelectorAll('button')) button.disabled = blocked;
      for (const id of ['CopyEnd', 'Copy', 'PasteMode', 'BatchText', 'PreviewPaste', 'FillEnd', 'FillMode', 'FillStep', 'PreviewFill', 'ApplyBatch']) el(id).disabled = blocked || !this.editable;
      el('SkipBlank').disabled = blocked || !this.editable || !['plain', 'values'].includes(el('PasteMode').value);
      this.tools.controls(blocked);
      for (const id of [...ruleInputs, 'PreviewRule', 'ReadChoices', 'Choice']) el(id).disabled = blocked || !this.editable;
      el('ApplyChoice').disabled = blocked || !this.editable || !el('Choice').value || this.cell.value.kind === 'text' && this.cell.value.raw === el('Choice').value;
    }

    async fetchCell(signal) {
      const shape = this.shape;
      if (!shape?.firstRow || !shape.firstCol) { this.cell = null; el('CellPanel').hidden = true; this.status('此工作表没有可编辑的单元格'); return; }
      if (this.grid.active && !el('Address').value) {
        this.setCell(null);
        this.status('当前工作表没有选中的单元格');
        return;
      }
      const doc = this.data.document;
      model.target(el('Address').value, shape);
      const cell = await this.post('read', {document: keyOf(doc), revision: doc.revision, sheet: shape.name, ref: el('Address').value.trim().toUpperCase()}, signal);
      this.invalidated = false;
      this.setCell(cell);
      this.status(this.data.encoding && cell.value.kind === 'missing' ? '该记录缺少此列' : '');
    }

    read() {
      if (this.dirty || this.pending) return;
      this.clearPreview();
      this.run('正在读取单元格…', signal => this.fetchCell(signal));
    }

    setCell(cell) {
      this.cell = cell;
      el('CellPanel').hidden = !cell;
      this.tools.bindCell(cell);
      el('OriginSheet').textContent = cell?.address.sheet || el('Sheet').value;
      el('OriginRef').textContent = cell?.ref || el('Address').value;
      el('OriginRef').hidden = !el('OriginRef').textContent;
      if (!cell) return;
      el('ValueLabel').textContent = `${cell.ref} 的内容`;
      el('Address').value = cell.ref;
      this.escaped = cell.value.raw.includes('\r');
      this.initialText = model.encode(cell.value.raw, this.escaped);
      const kind = cell.value.kind;
      this.initialKind = ['number', 'boolean', 'formula'].includes(kind) ? kind : 'text';
      el('Value').value = this.initialText;
      el('Kind').value = this.initialKind;
      el('Kind').hidden = !!this.data.encoding;
      el('TextHint').hidden = !this.escaped;
      let state = {missing: '源格缺失', blank: '空字段', error: '错误值'}[kind] || '';
      if (kind === 'formula') {
        if (cell.formulaState === 'pending') state = '待重算';
        else if (cell.formulaState === 'cycle') state = '循环引用';
        else if (cell.formulaState === 'missing') state = '缺少保存结果';
        else {
          const value = cell.calculatedResult || cell.savedResult;
          if (value) state = `${cell.calculatedResult ? '计算结果' : '原保存结果'} ${[...value.raw].slice(0, 48).join('') || '空文本'}`;
        }
      }
      el('CellState').textContent = [cell.readOnly ? '只读（' + cell.readOnly + '）' : '', cell.generatedStatus || state, cell.base.style !== cell.value.style ? '格式已修改' : ''].filter(Boolean).join(' · ');
      el('Base').hidden = equals(cell.base, cell.value);
      el('Base').open = false; el('BaseText').textContent = cell.base.raw || (cell.base.kind === 'missing' ? '源格缺失' : '空白');
      this.setRule(cell);
      this.controls();
    }

    refresh() {
      if (!this.data) return;
      if (this.pending) { this.commit(); return; }
      const draft = this.dirty ? {raw: el('Value').value, kind: el('Kind').value, escaped: this.escaped} : null;
      const ruleInput = this.loaded && !this.data.encoding ? this.ruleInput() : null;
      const ruleDraft = ruleInput && JSON.stringify(ruleInput) !== this.initialRule ? ruleInput : null;
      this.clearPreview();
      this.run('正在读取最新修订…', async signal => {
        const data = await this.post('state', {document: keyOf(this.data.document)}, signal);
        this.data = data; this.renderState();
        if (this.grid.active) {
          const {sheet,ref} = this.context();
          if (await this.bindGridBase(data,signal,sheet,ref)) await this.grid.enter(data,signal);
          else await this.grid.sync(undefined, signal);
        }
        await this.fetchCell(signal);
        if (draft && this.cell) {
          this.escaped = draft.escaped;
          this.initialText = model.encode(this.cell.value.raw, this.escaped);
          el('Value').value = draft.raw; el('Kind').value = draft.kind; el('TextHint').hidden = !this.escaped;
          this.status('已刷新修订，当前输入已保留');
        }
        if (ruleDraft && this.cell) {
          ruleInputs.forEach((id, index) => { el(id)[id === 'AllowBlank' ? 'checked' : 'value'] = ruleDraft[index]; });
          this.ruleControls();
          this.status('已刷新修订，当前输入已保留');
        }
      });
    }

    applyCell() {
      if (this.abort) return;
      if (this.pending) { this.commit(); return; }
      if (!this.editable || !this.dirty) return;
      try {
        const value = model.value(el('Kind').value, model.decode(el('Value').value, this.escaped), this.limits);
        if (equals(value, this.cell.value)) { this.setCell(this.cell); return; }
        this.commit('change', [{...this.cell.address, value}]);
      } catch (error) { this.status(error.message, true); }
    }

    commit(action, changes, {proof,rule,direct=false,structure,inferNumberFormat=false,entryAnchor,expandTable=false} = {}) {
      if (this.abort || !this.data || !this.pending && (!action || action === 'change' && !changes?.length)) return;
      const doc = this.data.document;
      const request = this.pending || {document: keyOf(doc), proof, direct, operation: {action, changes, rule, structure, entryAnchor, expandTable:expandTable||undefined, inferNumberFormat:inferNumberFormat || undefined, revision: doc.revision, request: crypto.randomUUID().replaceAll('-', '')}};
      this.pending = request;
      return this.run('正在保存编辑…', async signal => {
        let result;
        try { result = await this.post('commit', request, signal); }
        catch (error) {
          if (error.status >= 400 && error.status < 500 && error.status !== 408) this.pending = null;
          throw error;
        }
        this.pending = null; this.clearPreview();
        this.data.document = result.document;
        this.renderState();
        if (direct && result.committed === request.operation.revision && result.document.revision === result.committed) {
          this.grid.status(''); return;
        }
        try {
          if (this.grid.active && result.book !== this.context().book) {
            const {sheet,ref} = this.context(), names = this.data.sheets.map(s => s.name);
            const next = await this.post('state', {document:keyOf(result.document)}, signal);
            const active = next.sheets.some(s => s.name === sheet) ? sheet : next.sheets[names.indexOf(sheet)]?.name;
            await this.bindGridBase(next, signal, active, this.tools.entryRefAfterResize(sheet,ref,request.operation,next));
            await this.grid.enter(next, signal);
          } else if (this.grid.active) {
            // A replay can return an earlier receipt after another page has
            // advanced the document. Reload the full overlay in that case.
            const delta = !result.reloadGrid && request.operation.action === 'change' && result.committed === result.document.revision ? request.operation.changes : undefined;
            await this.grid.sync(delta, signal);
          }
          if (!direct) await this.fetchCell(signal);
        } catch (error) {
          this.invalidated = this.grid.stale = true;
          throw Error(`已保存修订 ${result.committed}。${error.message}，请刷新修订`);
        }
        this.status(`已保存修订 ${result.committed}`);
      });
    }

    commitStructure(structure) {
      return this.commit('structure', undefined, {structure});
    }

    async bindGridBase(data, signal, preferredSheet, ref) {
      if (data.book === this.context().book) return false;
      const base = await this.post('base', {document:keyOf(data.document), revision:data.document.revision, book:this.context().book}, signal);
      const index = base.sheets.findIndex(s => s.name === preferredSheet);
      if (index >= 0) base.activeTab = index;
      this.grid.rebinding = this.data?.document.id === data.document.id && this.data.document.binding === data.document.binding;
      try {
        await this.adopt(base, {revisionRef:ref});
        await this.activate(data, this.context().sheet, ref, signal, false);
      } finally {
        this.grid.rebinding = false;
      }
      return true;
    }

    exitGrid() {
      if (this.data.originalBook === this.context().book) { this.grid.leave(); return true; }
      return this.run('正在打开原工作簿…', async signal => {
        const base = await this.post('base', {document:keyOf(this.data.document), book:this.context().book, mode:'original'}, signal);
        await this.adopt(base);
      });
    }

    clearPreview() { this.previewed = null; el('BatchPreview').hidden = true; }

    editTarget() {
      const doc = this.data.document;
      return {document: keyOf(doc), revision: doc.revision, sheet: this.cell.address.sheet, ref: this.cell.ref};
    }

    copy() {
      if (!this.editable || this.dirty || this.abort || this.pending) return;
      this.run('正在读取复制区域…', async signal => {
        const clip = await this.post('copy', {...this.editTarget(), end: el('CopyEnd').value.trim().toUpperCase()}, signal);
        const text = JSON.stringify(clip);
        try {
          await navigator.clipboard.writeText(text);
          this.status(`已复制 ${clip.rows * clip.cols} 格`);
        } catch {
          this.clearPreview(); this.pasteRaw = text; el('BatchText').value = text;
          el('PasteMode').value = 'values';
          this.tools.select('Paste');
          this.status('剪贴板不可用，已将数据放入粘贴框');
        }
      });
    }

    setRule(cell) {
      const {rule, choices = []} = cell;
      const start = rule ? this.colName(rule.start.col) + rule.start.row : cell.ref;
      const end = rule ? this.colName(rule.endCol) + rule.endRow : cell.ref;
      this.setChoices(choices);
      el('RuleStart').value = start; el('RuleEnd').value = end;
      el('RuleType').value = rule?.reference ? 'list-reference' : rule?.type || 'list';
      el('RuleOperator').value = rule?.operator || 'between';
      el('RuleFirst').value = rule && rule.type !== 'list' ? rule.first : '';
      el('RuleSecond').value = rule?.second || '';
      el('RuleList').value = choices.join('\n');
      el('RuleReference').value = rule?.reference ? rule.first : '';
      el('AllowBlank').checked = rule?.allowBlank ?? true;
      this.initialRule = JSON.stringify(this.ruleInput());
      this.ruleControls();
    }

    setChoices(choices) {
      const reference = !!this.cell.rule?.reference;
      el('RulePick').hidden = !reference && choices.length === 0;
      el('ReadChoices').hidden = !reference;
      for (const id of ['ChoiceLabel', 'Choice', 'ApplyChoice']) el(id).hidden = choices.length === 0;
      el('Choice').replaceChildren(new Option('请选择', ''), ...choices.map(value => new Option(value, value)));
      el('Choice').value = this.cell.value.kind === 'text' && choices.includes(this.cell.value.raw) ? this.cell.value.raw : '';
    }

    readChoices() {
      if (!this.editable || this.dirty || this.abort || this.pending || !this.cell.rule?.reference) return;
      const target = this.editTarget();
      this.setChoices([]);
      this.run('正在读取列表选项…', async signal => {
        const result = await this.post('choices', target, signal);
        if (result.revision !== target.revision) throw Error('编辑修订已变化，请刷新后重试');
        this.setChoices(result.choices);
        this.status(`已读取 ${result.choices.length} 项`);
      });
    }

    ruleInput() {
      return ruleInputs.map(id => el(id)[id === 'AllowBlank' ? 'checked' : 'value']);
    }

    ruleControls() {
      const type = el('RuleType').value;
      el('RuleList').hidden = type !== 'list';
      el('RuleReferenceField').hidden = type !== 'list-reference';
      el('RuleNumeric').hidden = type === 'list' || type === 'list-reference';
      el('RuleSecond').hidden = !['between', 'notBetween'].includes(el('RuleOperator').value);
      const placeholder = el('RuleType').value === 'date' ? 'YYYY-MM-DD 或序号' : '条件值';
      el('RuleFirst').placeholder = el('RuleSecond').placeholder = placeholder;
    }

    fillControls() {
      const mode = el('FillMode').value;
      const unit = {number: '步长', date: '步长（天）', time: '步长（秒）'}[mode];
      el('FillStep').hidden = el('StepLabel').hidden = !unit;
      if (unit) el('StepLabel').textContent = unit;
    }

    previewRule() {
      if (!this.editable || this.dirty || this.abort || this.pending || this.data.encoding) return;
      this.clearPreview();
      try {
        const a = model.target(el('RuleStart').value, this.shape), b = model.target(el('RuleEnd').value, this.shape);
        const start = {...a, row: Math.min(a.row, b.row), col: Math.min(a.col, b.col)};
        const choice = el('RuleType').value, reference = choice === 'list-reference';
        const type = reference ? 'list' : choice, list = type === 'list';
        const rule = {start, endRow: Math.max(a.row, b.row), endCol: Math.max(a.col, b.col), type,
          operator: list ? '' : el('RuleOperator').value,
          first: reference ? el('RuleReference').value.trim() : list ? el('RuleList').value : el('RuleFirst').value.trim(),
          reference,
          second: list || el('RuleSecond').hidden ? '' : el('RuleSecond').value.trim(), allowBlank: el('AllowBlank').checked};
        this.run('正在核对规则范围…', async signal => {
          const plan = await this.post('preview', {...this.editTarget(), mode: 'rule', rule}, signal);
          this.showPreview(plan, 'rule');
        });
      } catch (error) { this.status(error.message, true); }
    }

    showPreview(plan, mode) {
      this.previewed = plan;
      const {changes, last, rule} = plan, first = rule?.start || this.cell.address;
      const ref = target => this.colName(target.col) + target.row;
      const tail = plan.lastValue ?? '';
      const count = rule ? (last.row-first.row+1)*(last.col-first.col+1) : changes.length;
      const sameFormat = mode === 'fill' && changes.some(change => change.value.style != null);
      el('BatchSummary').textContent = `${ref(first)} → ${ref(last)}，${count} 格${mode === 'fill' && tail ? `，末值 ${[...tail].slice(0, 48).join('')}` : ''}${sameFormat ? '，沿用起点格式' : ''}`;
      el('ApplyBatch').textContent = rule ? '应用规则' : '应用此批';
      const parent = rule ? el('Rules') : el(mode === 'fill' ? 'PaneFill' : 'PanePaste');
      parent.append(el('BatchPreview'));
      el('BatchPreview').hidden = false;
      this.status('');
    }

    preview(mode) {
      if (!this.editable || this.dirty || this.abort || this.pending) return;
      this.clearPreview();
      const first = this.cell.address, shape = this.shape;
      try {
        const raw = this.pasteRaw ?? el('BatchText').value;
        const pasteMode = el('PasteMode').value;
        const fillMode = el('FillMode').value;
        if (mode === 'fill' && ['date', 'time', 'formula-fill'].includes(fillMode)) {
          const last = model.target(el('FillEnd').value, shape);
          this.run('正在核对填充区域…', async signal => {
            const plan = await this.post('preview', {...this.editTarget(), mode: fillMode, end: this.colName(last.col) + last.row, step: el('FillStep').value.trim()}, signal);
            this.showPreview(plan, mode);
          });
          return;
        }
        if (mode === 'paste' && pasteMode !== 'plain') {
          if (new TextEncoder().encode(raw).length + 1024 > this.limits.requestBytes) throw Error('粘贴内容超出容量，请缩小范围');
          let clipboard;
          try { clipboard = JSON.parse(raw); } catch { throw Error('请粘贴有类型数据'); }
          this.run('正在核对粘贴区域…', async signal => {
            const plan = await this.post('preview', {...this.editTarget(), mode: pasteMode, clipboard, skipBlank: el('SkipBlank').checked}, signal);
            this.showPreview(plan, mode);
          });
          return;
        }
        const plan = mode === 'paste'
          ? model.paste(raw, first, shape, el('SkipBlank').checked, this.limits, window.ExcelLensClipboard.parse)
          : model.fill(first, model.target(el('FillEnd').value, shape), this.cell.value, el('FillStep').value, fillMode === 'number', !!this.data.encoding, this.limits);
        this.run('正在核对目标区域…', async signal => {
          const checked = await this.post('preview', {...this.editTarget(), mode: 'changes', end: this.colName(plan.last.col) + plan.last.row, operation: {changes: plan.changes}}, signal);
          this.showPreview(checked, mode);
        });
      } catch (error) { this.status(error.message, true); }
    }

    item(title, detail) {
      const line = document.createElement('div'), text = document.createElement('div');
      const strong = document.createElement('strong'), small = document.createElement('small');
      line.className = 'edit-copy-item'; strong.textContent = title; small.textContent = detail;
      text.append(strong, small); line.append(text);
      return line;
    }

    button(title, action) {
      const button = document.createElement('button');
      button.type = 'button'; button.className = 'mini-button'; button.textContent = title;
      button.addEventListener('click', action);
      return button;
    }

    renderState() {
      const doc = this.data.document;
      el('Revision').textContent = `修订 ${doc.revision} · ${doc.changed} 格修改${doc.structuresChanged ? ` · ${doc.structuresChanged} 项结构操作` : ''}${doc.rulesChanged ? ` · ${doc.rulesChanged} 条规则` : ''}`;
      this.grid.controls();
      el('Outputs').replaceChildren();
      for (const artifact of this.data.artifacts) {
        const detail = [`修订 ${artifact.revision}`, this.fmtBytes(artifact.bytes), artifact.encoding, artifact.protected ? `${artifact.protected} 格添加保护前缀` : null,
          artifact.nativeRecalc ? '公式将在 Excel 中重算' : artifact.formulasPending ? `${artifact.formulasPending} 个公式待重算` : null,
          artifact.pivotCachesPending ? `${artifact.pivotCachesPending} 个透视缓存待刷新` : null,
          artifact.pivotCachesAfterCalc ? '请在 Excel 中先重算再刷新' : null].filter(Boolean).join(' · ');
        const item = this.item(artifact.name, detail);
        const link = document.createElement('a');
        link.className = 'mini-button'; link.href = artifact.downloadUrl; link.textContent = '下载'; link.download = artifact.name;
        item.append(link, this.button('打开副本', () => this.openFile(artifact)));
        el('Outputs').append(item);
      }
    }

    save() {
      if (this.dirty || this.pending || !this.data) return;
      const doc = this.data.document;
      const options = this.data.encoding ? {encoding: el('Encoding').value, protect: el('Protect').checked} : {};
      return this.run('正在生成并核对副本…', async signal => {
        const artifact = await this.post('save', {document: keyOf(doc), revision: doc.revision, options}, signal);
        this.data.artifacts = [artifact, ...this.data.artifacts.filter(a => a.key !== artifact.key)];
        this.renderState(); this.status(`已生成修订 ${artifact.revision}${artifact.pivotCachesAfterCalc ? '，请在 Excel 中重算后刷新透视表' : ''}`);
      });
    }

    openFile(artifact) {
      if (this.dirty || this.pending || !this.canOpen('打开副本')) return;
      const {book} = this.context(), doc = this.data.document;
      this.run('正在打开生成的副本…', async signal => {
        const data = await this.post('open', {document: keyOf(doc), artifact: artifact.key, book}, signal);
        if (this.grid.active) this.grid.leave();
        el('Dialog').close();
        await this.adopt(data);
      }, false);
    }
  }

  window.ExcelLensEditCopy = EditCopy;
})();
