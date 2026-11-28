/**
 * Floating virtualized read-only formula calculator window with grid range
 * picking, function autocomplete, recovery journal, and notes column.
 */
import { t as localize } from '../core/i18n.ts';
import type { ApiClient, SessionService, ToastFn } from '../contracts/common.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import { requireButton, requireEl } from '../core/dom.ts';
import { enterMotion, exitMotion } from '../core/motion.ts';
import { RecoveryJournal, showRecoveryDialog } from '../core/recovery.ts';
import { parseClipboardTSV } from '../grid/clipboard.ts';
import type { WorkbookState } from '../grid/state.ts';
import { VirtualScroll } from '../grid/virtual-scroll.ts';
import {
  closeFormula as defaultCloseFormula,
  formulaContext as defaultFormulaContext,
  FormulaTable,
  type CalculatorRowRecord,
  type CalculatorSourceRef,
  type CalculatorSuggestion,
  type FormulaInputContext,
} from './calculator-model.ts';
import {
  decodeCalculatorCellResult,
  decodeCalculatorFunctionsResponse,
} from '../core/http-decoders.generated.ts';

const ROW_HEIGHT = 26;
const HEADER_HEIGHT = 26;

const quoteSheet = (sheet: string): string => `'${sheet.replaceAll("'", "''")}'!`;

export interface CalculatorFunctionInfo {
  readonly name: string;
  readonly signature: string;
}

/** @httpResponse CalculatorFunctionsResponse */
export interface CalculatorFunctionsResponse {
  readonly items?: readonly CalculatorFunctionInfo[] | null;
}

interface CalculatorRowView {
  readonly tr: HTMLDivElement;
  readonly number: HTMLDivElement;
  readonly input: HTMLInputElement;
  readonly value: HTMLDivElement;
  readonly note: HTMLInputElement;
  index: number;
}

interface CalculatorJob {
  readonly index: number;
  readonly revision: number;
  readonly controller: AbortController;
  readonly key: string;
  readonly body: string;
  promise: Promise<void>;
  finish: () => void;
}

interface PickSpan {
  readonly index: number;
  readonly start: number;
  readonly end: number;
  readonly token: string;
}

interface NoteEditState {
  readonly index: number;
  readonly before: string;
}

interface NotesBounds {
  readonly width: string;
  readonly left: number;
  readonly top: number;
  expandedLeft: number;
  expandedTop: number;
}

interface LegacyExecDocument {
  execCommand?(commandId: string, showUI?: boolean, value?: string): boolean;
}

export interface CalculatorOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly grid: HTMLElement;
  readonly copy: (value: string, successMessage?: string) => Promise<boolean> | boolean | undefined;
  readonly session?: SessionService;
  readonly formulaContext?: (text: string, caret: number) => FormulaInputContext | null;
  readonly closeFormula?: (text: string) => string;
  readonly enableRecovery?: boolean;
}

export class Calculator {
  public readonly model: FormulaTable;
  public readonly recovery: RecoveryJournal<CalculatorRowRecord> | null = null;
  public editing: number | null = null;

  private readonly o: CalculatorOptions;
  private readonly getFormulaContext: (text: string, caret: number) => FormulaInputContext | null;
  private readonly closeFormulaText: (text: string) => string;
  public readonly host: HTMLElement;
  public readonly body: HTMLElement;
  public readonly scroll: HTMLElement;
  public readonly popup: HTMLElement;
  public readonly virtual = new VirtualScroll();

  public views = new Map<number, CalculatorRowView>();
  public readonly pool: CalculatorRowView[] = [];
  public readonly jobs = new Map<number, CalculatorJob>();
  private readonly waiting = new Map<number, CalculatorJob>();
  private running = 0;
  private pick: PickSpan | null = null;
  public notes = false;
  private noteEdit: NoteEditState | null = null;
  private notesBounds: NotesBounds | null = null;
  public catalog: CalculatorFunctionInfo[] = [];
  private catalogPromise: Promise<void> | null = null;
  public suggestions: CalculatorFunctionInfo[] = [];
  public suggestion = 0;
  public completionMode: '' | 'list' | 'hint' = '';
  private visibility = 0;
  private leaving = false;

  constructor(options: CalculatorOptions) {
    this.o = options;
    this.getFormulaContext = options.formulaContext ?? defaultFormulaContext;
    this.closeFormulaText = options.closeFormula ?? defaultCloseFormula;
    this.model = new FormulaTable();
    this.host = requireEl('calculatorWindow');
    this.body = requireEl('calculatorRows');
    this.scroll = requireEl('calculatorScroll');
    this.popup = requireEl('calculatorCompletion');

    requireButton('calculatorOpenBtn').addEventListener('click', () => this.toggle());
    requireButton('calculatorClose').addEventListener('click', () => this.hide());
    requireButton('calculatorMinimize').addEventListener('click', () => this.minimize());
    requireButton('calculatorNotes').addEventListener('click', () => this.toggleNotes());
    for (const id of ['calculatorDock', 'calculatorHomeDock']) {
      requireButton(id).addEventListener('click', () => this.open());
    }

    this.body.addEventListener('focusin', (event: FocusEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const note = target?.closest<HTMLElement>('[data-note-row]');
      if (note) {
        this.activateNote(Number(note.dataset.noteRow));
        return;
      }
      const input = target?.closest<HTMLElement>('[data-formula-row]');
      if (input) {
        this.activate(Number(input.dataset.formulaRow));
        return;
      }
      if (target?.closest('[data-result-row]')) {
        if (this.editing !== null) void this.commit(this.editing);
        this.editing = null;
        this.noteEdit = null;
        this.pick = null;
        this.hideCompletion();
      }
    });

    this.body.addEventListener('input', (event: Event) => {
      const target = event.target instanceof Element ? event.target : null;
      const note = target?.closest('[data-note-row]');
      if (note instanceof HTMLInputElement) {
        const index = Number(note.dataset.noteRow);
        if (this.noteEdit?.index !== index) this.activateNote(index);
        this.model.setNote(index, note.value);
        note.title = note.value;
        this.render();
        return;
      }
      const input = target?.closest('[data-formula-row]');
      if (!(input instanceof HTMLInputElement)) return;
      const index = Number(input.dataset.formulaRow);
      this.activate(index);
      this.edit(index, input.value);
      void this.updateCompletion();
    });

    this.body.addEventListener('paste', (event: ClipboardEvent) => this.paste(event));
    this.scroll.addEventListener(
      'scroll',
      () => {
        this.hideCompletion();
        this.render();
      },
      { passive: true },
    );
    this.scroll.addEventListener(
      'wheel',
      (event: WheelEvent) => {
        if (this.virtual.scale === 1 || !event.cancelable) return;
        event.preventDefault();
        const next = this.virtual.wheel(event, this.scroll.scrollTop, ROW_HEIGHT, this.viewport());
        this.scroll.scrollTop = next.top;
        this.render();
      },
      { passive: false },
    );
    this.popup.addEventListener('pointerdown', (event: PointerEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const item = target?.closest<HTMLElement>('[data-calc-function]');
      if (!item?.dataset.calcFunction) return;
      event.preventDefault();
      this.complete(item.dataset.calcFunction);
    });
    document.addEventListener(
      'pointerdown',
      (event: PointerEvent) => {
        const target = event.target instanceof Element ? event.target : null;
        if (this.host.hidden || this.editing === null || target?.closest('#calculatorCompletion')) {
          return;
        }
        if (
          this.picking &&
          target?.closest('.grid-cell,.column-head[data-col],.row-head,.pinned-row-head,.sheet-tab') &&
          !target.closest('.row-resize-grip,.column-resize-grip,.column-filter,.auto-filter-button')
        ) {
          event.preventDefault();
          return;
        }
        if (!target?.closest('#calculatorWindow,.grid-region,.sheet-tab')) {
          const index = this.editing;
          this.editing = null;
          void this.commit(index);
          this.hideCompletion();
        }
      },
      true,
    );

    this.bindDrag();
    window.addEventListener('resize', () => {
      this.hideCompletion();
      this.render();
      this.clamp();
    });
    if (typeof ResizeObserver !== 'undefined') {
      new ResizeObserver(() => {
        this.render();
        this.clamp();
      }).observe(this.host);
    }
    this.render();

    if (options.enableRecovery !== false) {
      const recovery = new RecoveryJournal<CalculatorRowRecord>({
        kind: 'calculator',
        api: this.o.api,
        session: this.o.session,
        onRestore: (records) => {
          this.model.restore(records);
          this.render();
          if (Object.keys(records).length) this.dock(Boolean(this.host.hidden));
        },
        prepareCopy: (records, current) => {
          let last = -2;
          for (const key of current.keys()) last = Math.max(last, Number(key));
          const offset = last + 2;
          const result: Record<string, CalculatorRowRecord> = {};
          for (const [key, value] of Object.entries(records)) {
            const index = Number(key) + offset;
            if (!Number.isSafeInteger(index) || index > Number.MAX_SAFE_INTEGER - 2) {
              throw new Error(localize('ui.calculator.appendedRowNumberExceedsRange'));
            }
            result[String(index)] = value;
          }
          return result;
        },
        onStatus: (status) => {
          const el = requireEl('calculatorSaveStatus');
          el.textContent = status.error
            ? localize('ui.calculator.restoreUnsavedData')
            : status.loading
              ? localize('ui.calculator.readingRecoveryCopy')
              : status.dirty
                ? localize('ui.calculator.restorationProgress')
                : status.count
                  ? localize('ui.common.saved')
                  : '';
          el.title = status.error || localize('ui.calculator.recoveryCopiesFormulasRemarks');
          el.classList.toggle('has-error', Boolean(status.error));
        },
      });
      this.recovery = recovery;
      this.model.onChange = (key, data): void => recovery.set(key, data);
      requireButton('calculatorRecovery').addEventListener('click', () =>
        showRecoveryDialog(recovery, {
          title: localize('ui.calculator.resumeCalculationTable'),
          explanation: localize('ui.calculator.loadedFormulasWaitingBeCalculated'),
        }),
      );
    }
  }

  source(): CalculatorSourceRef {
    return {
      workbookKey: this.o.state.workbookKey,
      sheet: this.o.state.sheet ?? '',
    };
  }

  get picking(): boolean {
    if (this.host.hidden || this.editing === null) return false;
    const row = this.model.get(this.editing);
    const input = this.views.get(this.editing)?.input;
    const caret = input?.selectionStart ?? null;
    if (!input || caret === null || !this.getFormulaContext(row.text, caret)) return false;
    if (caret !== input.selectionEnd) return true;
    if (
      this.pick?.index === this.editing &&
      caret === this.pick.end &&
      row.text.slice(this.pick.start, this.pick.end) === this.pick.token
    ) {
      return true;
    }
    return /[=+\-*/^&(<>,:]\s*$/.test(row.text.slice(0, caret));
  }

  toggle(): void {
    if (this.host.hidden || this.leaving) this.open();
    else this.hide();
  }

  dock(value: boolean): void {
    for (const id of ['calculatorDock', 'calculatorHomeDock']) {
      const node = requireEl(id);
      node.hidden = !value;
      if (value) enterMotion(node, { distance: 3, duration: 140 });
    }
  }

  toggleNotes(): void {
    const index = this.model.active;
    const noteFocused = document.activeElement === this.views.get(index)?.note;
    const r = this.host.getBoundingClientRect();
    this.notes = !this.notes;
    if (this.notes) {
      this.notesBounds = {
        width: this.host.style.width || `${r.width}px`,
        left: r.left,
        top: r.top,
        expandedLeft: r.left,
        expandedTop: r.top,
      };
      const width = Math.min(window.innerWidth - 16, Math.max(580, r.width + 180));
      this.host.style.width = `${width}px`;
      this.host.style.left = `${r.left + r.width - width}px`;
    } else {
      const old = this.notesBounds;
      this.host.style.width = old?.width || '440px';
      if (old) {
        this.host.style.left = `${old.left + r.left - old.expandedLeft}px`;
        this.host.style.top = `${old.top + r.top - old.expandedTop}px`;
      }
    }
    this.host.classList.toggle('has-notes', this.notes);
    requireButton('calculatorNotes').setAttribute('aria-pressed', String(this.notes));
    this.scroll.setAttribute('aria-colcount', this.notes ? '4' : '3');
    this.render();
    this.clamp();
    if (this.notes && this.notesBounds) {
      const expanded = this.host.getBoundingClientRect();
      this.notesBounds.expandedLeft = expanded.left;
      this.notesBounds.expandedTop = expanded.top;
    } else {
      this.notesBounds = null;
    }
    enterMotion(this.scroll, { duration: 120 });
    if (noteFocused && !this.notes) this.focus(index);
  }

  open(): void {
    this.visibility++;
    this.leaving = false;
    this.host.hidden = false;
    this.dock(false);
    void this.loadFunctions();
    this.render();
    this.clamp();
    this.focus(this.model.active);
    enterMotion(this.host, { distance: 5 });
  }

  private leave(minimized: boolean): void {
    if (this.editing !== null) void this.commit(this.editing);
    this.editing = null;
    this.pick = null;
    this.hideCompletion();
    this.leaving = true;
    const ticket = ++this.visibility;
    this.dock(minimized);
    const finish = (): void => {
      if (ticket !== this.visibility) return;
      const returnFocus = this.host.contains(document.activeElement);
      this.host.hidden = true;
      this.leaving = false;
      if (returnFocus && this.o.state.indexReady) {
        this.o.grid.focus({ preventScroll: true });
      }
    };
    if (typeof this.host.animate === 'function') {
      void exitMotion(this.host).then((done) => {
        if (done) finish();
      });
    } else {
      finish();
    }
  }

  hide(): void {
    this.leave(false);
  }

  minimize(): void {
    this.leave(true);
  }

  cancelJob(index: number): void {
    const job = this.jobs.get(index);
    if (!job) return;
    job.controller.abort();
    this.jobs.delete(index);
    this.waiting.delete(index);
    job.finish();
  }

  bookChanged(): void {
    this.editing = null;
    this.pick = null;
    this.hideCompletion();
    for (const [index, job] of this.jobs) {
      if (job.key && job.key !== this.o.state.workbookKey) {
        this.cancelJob(index);
        const row = this.model.ensure(index);
        row.result = null;
        row.error = localize('ui.calculator.refWorkbookSwitched');
        this.paint(index);
      }
    }
  }

  activate(index: number): void {
    if (this.editing === index && this.model.active === index) return;
    if (this.editing !== null && this.editing !== index) void this.commit(this.editing);
    this.noteEdit = null;
    this.hideCompletion();
    this.pick = null;
    this.model.active = index;
    this.model.fit();
    this.editing = index;
    for (const [i, v] of this.views) v.tr.classList.toggle('is-active', i === index);
  }

  activateNote(index: number): void {
    if (this.editing !== null) void this.commit(this.editing);
    this.editing = null;
    this.pick = null;
    this.hideCompletion();
    if (this.noteEdit?.index !== index) {
      this.noteEdit = { index, before: this.model.get(index).note };
    }
    this.model.active = index;
    this.model.fit();
    for (const [i, v] of this.views) v.tr.classList.toggle('is-active', i === index);
  }

  viewport(): number {
    return Math.max(
      ROW_HEIGHT,
      (this.scroll.clientHeight ||
        HEADER_HEIGHT + Math.min(12, this.model.rowCount) * ROW_HEIGHT) - HEADER_HEIGHT,
    );
  }

  layout(): void {
    this.scroll.style.height = `${HEADER_HEIGHT + Math.min(12, Math.max(5, this.model.rowCount)) * ROW_HEIGHT}px`;
    const height = this.virtual.configure(this.model.rowCount * ROW_HEIGHT, this.viewport());
    this.body.style.height = `${height}px`;
  }

  focus(index: number, field: 'formula' | 'note' = 'formula'): void {
    if (index < 0 || !Number.isSafeInteger(index)) return;
    this.model.ensure(index);
    this.layout();
    const y = this.virtual.logical(this.scroll.scrollTop || 0);
    const top = index * ROW_HEIGHT;
    const view = this.viewport();
    if (top < y) this.scroll.scrollTop = this.virtual.physical(top);
    else if (top + ROW_HEIGHT > y + view) {
      this.scroll.scrollTop = this.virtual.physical(top + ROW_HEIGHT - view);
    }
    if (field === 'note') this.activateNote(index);
    else this.activate(index);
    this.render();
    const rowView = this.views.get(index);
    (field === 'note' ? rowView?.note : rowView?.input)?.focus({ preventScroll: true });
  }

  edit(index: number, text: string): void {
    this.cancelJob(index);
    this.pick = null;
    try {
      this.model.edit(index, text, this.source());
    } catch (error: unknown) {
      this.model.ensure(index).error = errorMessage(error);
    }
    this.render();
  }

  private makeView(): CalculatorRowView {
    const tr = document.createElement('div');
    tr.className = 'calculator-row';
    tr.setAttribute('role', 'row');
    const number = document.createElement('div');
    number.className = 'calculator-row-number';
    number.setAttribute('role', 'rowheader');
    const cell = document.createElement('div');
    const input = document.createElement('input');
    input.type = 'text';
    input.className = 'calculator-formula';
    input.maxLength = 4096;
    input.spellcheck = false;
    input.autocomplete = 'off';
    input.setAttribute('aria-autocomplete', 'list');
    input.setAttribute('aria-controls', 'calculatorCompletion');
    cell.append(input);
    const value = document.createElement('div');
    value.className = 'calculator-value';
    value.tabIndex = 0;
    value.setAttribute('role', 'gridcell');
    const noteCell = document.createElement('div');
    noteCell.className = 'calculator-note-cell';
    const note = document.createElement('input');
    note.type = 'text';
    note.className = 'calculator-note';
    note.maxLength = 4096;
    note.autocomplete = 'off';
    noteCell.append(note);
    tr.append(number, cell, value, noteCell);
    this.body.append(tr);
    const view: CalculatorRowView = { tr, number, input, value, note, index: -1 };
    this.pool.push(view);
    return view;
  }

  render(): void {
    this.layout();
    const y = this.virtual.logical(this.scroll.scrollTop || 0);
    const first = Math.max(0, Math.floor(y / ROW_HEIGHT) - 3);
    const last = Math.min(this.model.rowCount, first + Math.ceil(this.viewport() / ROW_HEIGHT) + 7);
    const desired = new Set<number>();
    for (let i = first; i < last; i++) desired.add(i);
    if (this.editing !== null && document.activeElement === this.views.get(this.editing)?.input) {
      desired.add(this.editing);
    }
    if (this.noteEdit && document.activeElement === this.views.get(this.noteEdit.index)?.note) {
      desired.add(this.noteEdit.index);
    }
    const next = new Map<number, CalculatorRowView>();
    for (const i of desired) {
      const existing = this.views.get(i);
      if (existing) next.set(i, existing);
    }
    const used = new Set(next.values());
    const free = this.pool.filter((v) => !used.has(v));
    for (const i of desired) {
      if (!next.has(i)) {
        const v = free.pop() ?? this.makeView();
        v.index = i;
        v.input.dataset.formulaRow = String(i);
        v.value.dataset.resultRow = String(i);
        v.note.dataset.noteRow = String(i);
        v.input.id = `calculatorFormula${i}`;
        v.input.placeholder = i === 0 ? '=' : '';
        v.input.setAttribute('aria-label', localize('ui.calculator.formulaRow', { i: i + 1 }));
        v.value.setAttribute('aria-label', localize('ui.calculator.resultsRow', { i: i + 1 }));
        v.note.setAttribute('aria-label', localize('ui.calculator.notesRow', { i: i + 1 }));
        v.input.setAttribute('aria-expanded', 'false');
        v.tr.setAttribute('aria-rowindex', String(i + 1));
        v.number.textContent = String(i + 1);
        next.set(i, v);
      }
    }
    this.views = next;
    const bound = new Set(next.values());
    for (const v of this.pool) v.tr.hidden = !bound.has(v);
    for (const [i, v] of next) {
      const row = this.model.get(i);
      v.tr.hidden = false;
      v.tr.style.top = `${this.virtual.rowPosition(i * ROW_HEIGHT, this.scroll.scrollTop || 0)}px`;
      v.tr.classList.toggle('is-active', i === this.model.active);
      if (v.input.value !== row.text) v.input.value = row.text;
      if (v.note.value !== row.note) v.note.value = row.note;
      v.note.title = row.note;
      this.paint(i);
    }
    this.scroll.setAttribute('aria-rowcount', String(this.model.rowCount));
  }

  paint(index: number): void {
    const row = this.model.get(index);
    const v = this.views.get(index);
    if (!v) return;
    v.value.textContent = row.error
      ? this.errorCode(row.error)
      : (row.result?.display ?? (this.jobs.has(index) ? '…' : row.restored ? localize('ui.calculator.pendingCalculation') : ''));
    v.value.title =
      row.error || row.result?.exact || (row.restored ? localize('ui.calculator.formulasCalculatedPressingEnterRecovery') : '');
    v.value.classList.toggle('is-error', Boolean(row.error));
    v.value.classList.toggle('is-pending', row.restored && !row.result);
    v.value.classList.toggle('is-text', row.result?.kind === 'text');
    v.value.classList.toggle('is-boolean', row.result?.kind === 'boolean');
    const suggestion = row.result?.suggestions?.[0];
    if (!row.error && suggestion) {
      const text = document.createElement('span');
      text.className = 'calculator-conversion-value';
      text.textContent = v.value.textContent;
      v.value.textContent = '';
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'calculator-convert';
      button.textContent = suggestion.label;
      button.title = localize('ui.calculator.clickConvertThisRowFormula', { preview: suggestion.preview });
      button.onclick = (event: MouseEvent): void => {
        event.preventDefault();
        event.stopPropagation();
        this.applyConversion(index, suggestion);
      };
      v.value.append(text, button);
    }
  }

  applyConversion(index: number, suggestion: CalculatorSuggestion): void {
    const row = this.model.get(index);
    if (row.result?.suggestions?.[0] !== suggestion) return;
    const text = row.text.trim();
    const body = text.startsWith('=') ? text.slice(1) : `"${text.replaceAll('"', '""')}"`;
    let formula: string;
    if (suggestion.kind === 'date') formula = `=DATEVALUE(${body})`;
    else if (suggestion.kind === 'time') formula = `=TIMEVALUE(${body})`;
    else if (suggestion.kind === 'datetime') formula = `=DATEVALUE(${body})+TIMEVALUE(${body})`;
    else return;
    if (formula.length > 4096) {
      this.o.toast(localize('ui.calculator.convertedFormulaTooLongSplit'), 'warning');
      return;
    }
    this.edit(index, formula);
    void this.commit(index, true);
    this.focus(index);
  }

  errorCode(error: string): string {
    const code = /#(?:DIV\/0!|REF!|VALUE!|NUM!|NAME\?|N\/A|NULL!|CALC!|SPILL!)/.exec(error)?.[0];
    if (code) return code;
    if (/除数|除以0/.test(error)) return '#DIV/0!';
    if (/引用|工作簿|工作表|索引/.test(error)) return '#REF!';
    return '#VALUE!';
  }

  commit(index: number, force = false): Promise<void> {
    const row = this.model.ensure(index);
    let text = row.text.trim();
    if (row.restored && !force) return Promise.resolve();
    row.restored = false;
    if (text.startsWith('=')) {
      const completed = this.closeFormulaText(row.text);
      if (completed !== row.text) {
        this.cancelJob(index);
        this.model.edit(index, completed, this.source());
        const input = this.views.get(index)?.input;
        if (input) input.value = completed;
        text = completed.trim();
      }
    }
    if (!text.startsWith('=')) {
      this.cancelJob(index);
      const kind = /^[+-]?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(text)
        ? 'number'
        : 'text';
      row.result = text ? { kind, display: row.text, copy: row.text, exact: row.text } : null;
      row.committed = row.text;
      row.savedResult = row.result;
      row.committedRestored = false;
      this.paint(index);
      return Promise.resolve();
    }
    if (!force && row.text === row.committed && row.result && !row.error) {
      return Promise.resolve();
    }
    const existingJob = this.jobs.get(index);
    if (existingJob?.revision === row.revision) return existingJob.promise;
    this.cancelJob(index);
    const source = { ...(row.source ?? this.source()) };
    let resolveFinish = (): void => {
      // Initialized synchronously inside Promise executor.
    };
    const promise = new Promise<void>((resolve) => {
      resolveFinish = resolve;
    });
    const job: CalculatorJob = {
      index,
      revision: row.revision,
      controller: new AbortController(),
      key: source.workbookKey,
      body: JSON.stringify({ expression: row.text, ...source }),
      promise,
      finish: resolveFinish,
    };
    this.jobs.set(index, job);
    this.waiting.set(index, job);
    row.result = null;
    row.error = '';
    this.paint(index);
    this.pump();
    return job.promise;
  }

  private pump(): void {
    while (this.running < 4 && this.waiting.size) {
      const next = this.waiting.entries().next();
      if (next.done) break;
      const [index, job] = next.value;
      this.waiting.delete(index);
      if (job.controller.signal.aborted) {
        job.finish();
        continue;
      }
      this.running++;
      void this.run(job).finally(() => {
        this.running--;
        job.finish();
        this.pump();
      });
    }
  }

  private async run(job: CalculatorJob): Promise<void> {
    const { index, revision, controller } = job;
    const row = this.model.ensure(index);
    try {
      const result = await this.o.api('/api/calculator/formula', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: job.body,
        signal: controller.signal,
      }, decodeCalculatorCellResult);
      if (controller.signal.aborted || row.revision !== revision) return;
      row.result = result;
      row.error = '';
      row.committed = row.text;
      row.savedResult = result;
      row.committedRestored = false;
    } catch (error: unknown) {
      if (!controller.signal.aborted && row.revision === revision) {
        row.result = null;
        row.error = errorMessage(error);
      }
    } finally {
      if (this.jobs.get(index) === job) this.jobs.delete(index);
      this.paint(index);
    }
  }

  captureCell(ref: string, event?: MouseEvent): boolean {
    if (!this.picking || (event && event.detail > 1)) return false;
    return this.captureRange(ref);
  }

  captureRange(start: string, end?: string): boolean {
    if (!this.picking || this.editing === null) return false;
    this.hideCompletion();
    const index = this.editing;
    const row = this.model.ensure(index);
    const input = this.views.get(index)?.input;
    const current = this.source();
    if (!input) return false;
    if (!this.o.state.indexReady) {
      row.error = localize('ui.calculator.refWorksheetIndexNotCompleted');
      this.paint(index);
      return true;
    }
    if (row.source?.workbookKey && row.source.workbookKey !== current.workbookKey) {
      row.error = localize('ui.calculator.refWorkbookSwitched');
      this.paint(index);
      return true;
    }
    if (!row.source?.workbookKey) row.source = current;
    const token =
      (current.sheet === row.source.sheet ? '' : quoteSheet(current.sheet)) +
      start +
      (end && end !== start ? `:${end}` : '');
    let a = input.selectionStart ?? row.text.length;
    let b = input.selectionEnd ?? a;
    const equal = row.text.indexOf('=');
    if (a <= equal && b > equal) a = equal + 1;
    if (
      this.pick?.index === index &&
      a === b &&
      a === this.pick.end &&
      row.text.slice(this.pick.start, this.pick.end) === this.pick.token
    ) {
      a = this.pick.start;
      b = this.pick.end;
    }
    const updated = row.text.slice(0, a) + token + row.text.slice(b);
    input.focus({ preventScroll: true });
    input.setSelectionRange(a, b);
    try {
      const legacy: LegacyExecDocument = document;
      legacy.execCommand?.('insertText', false, token);
    } catch {
      // Fallback updates value directly below.
    }
    this.edit(index, updated);
    if (input.value !== row.text) input.value = row.text;
    const caret = a + token.length;
    input.setSelectionRange(caret, caret);
    this.pick = { index, start: a, end: caret, token };
    return true;
  }

  paste(
    event: Pick<ClipboardEvent, 'target' | 'preventDefault'> & {
      readonly clipboardData?: Pick<DataTransfer, 'getData'> | null;
    },
  ): void {
    const target = event.target instanceof Element ? event.target : null;
    const note = target?.closest<HTMLElement>('[data-note-row]');
    const input = target?.closest<HTMLElement>('[data-formula-row]');
    if (!note && !input) return;
    const text = event.clipboardData?.getData('text/plain') ?? '';
    if (!/[\t\r\n]/.test(text)) return;
    event.preventDefault();
    let lines: string[];
    try {
      lines = parseClipboardTSV(text, {
        maxCols: 1,
        maxCells: 2000,
        maxCellLength: 4096,
      }).map((row) => row[0] ?? '');
    } catch (error: unknown) {
      this.o.toast(errorMessage(error), 'warning');
      return;
    }
    const start = Number(note ? note.dataset.noteRow : input?.dataset.formulaRow);
    if (note) {
      for (let offset = 0; offset < lines.length; offset++) {
        this.model.setNote(start + offset, lines[offset] ?? '');
      }
      this.render();
      this.focus(start + lines.length, 'note');
      return;
    }
    this.hideCompletion();
    for (let offset = 0; offset < lines.length; offset++) {
      const i = start + offset;
      this.cancelJob(i);
      try {
        this.model.edit(i, lines[offset] ?? '', this.source());
        void this.commit(i);
      } catch (error: unknown) {
        this.model.ensure(i).error = errorMessage(error);
      }
    }
    this.render();
    this.focus(start + lines.length);
  }

  loadFunctions(): Promise<void> {
    this.catalogPromise ??= Promise.resolve()
      .then(() => this.o.api('/api/calculator/functions', undefined, decodeCalculatorFunctionsResponse))
      .then((data) => {
        this.catalog = data.items ? [...data.items] : [];
      })
      .catch(() => {
        this.catalogPromise = null;
      });
    return this.catalogPromise;
  }

  async updateCompletion(): Promise<void> {
    if (this.editing === null) {
      this.hideCompletion();
      return;
    }
    const index = this.editing;
    const input = this.views.get(index)?.input;
    const caret = input?.selectionStart ?? null;
    if (!input || caret === null || this.host.hidden) {
      this.hideCompletion();
      return;
    }
    const context = this.getFormulaContext(input.value, caret);
    if (!context || (!context.prefix && !context.fn)) {
      this.hideCompletion();
      return;
    }
    await this.loadFunctions();
    if (
      !this.host.isConnected ||
      this.editing !== index ||
      this.host.ownerDocument.activeElement !== input
    ) {
      return;
    }
    const nextCaret = input.selectionStart;
    if (nextCaret === null) return;
    const now = this.getFormulaContext(input.value, nextCaret);
    if (now?.prefix !== context.prefix || now.end !== context.end || now.fn !== context.fn) {
      return;
    }
    this.suggestions = context.prefix
      ? this.catalog
          .filter((fn) => fn.name.startsWith(context.prefix.toUpperCase()))
          .slice(0, 8)
      : [];
    this.suggestion = 0;
    if (!this.suggestions.length) {
      const fn = this.catalog.find((item) => item.name === context.fn);
      if (!fn) {
        this.hideCompletion();
        return;
      }
      const hint = document.createElement('div');
      hint.className = 'calculator-signature';
      hint.textContent = fn.signature;
      this.popup.replaceChildren(hint);
      this.completionMode = 'hint';
    } else {
      this.completionMode = 'list';
      this.renderSuggestions();
    }
    const entering = this.popup.hidden;
    this.popup.hidden = false;
    input.setAttribute('aria-expanded', String(this.completionMode === 'list'));
    this.placeCompletion(input);
    if (entering) enterMotion(this.popup, { distance: 3, duration: 130 });
  }

  private renderSuggestions(): void {
    const nodes = this.suggestions.map((fn, i) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.dataset.calcFunction = fn.name;
      button.id = `calculatorSuggest${i}`;
      button.setAttribute('role', 'option');
      button.setAttribute('aria-selected', String(i === this.suggestion));
      button.className = i === this.suggestion ? 'is-active' : '';
      button.textContent = fn.name;
      const signature = document.createElement('span');
      signature.textContent = fn.signature.slice(fn.name.length);
      button.append(signature);
      return button;
    });
    this.popup.replaceChildren(...nodes);
    if (this.editing !== null) {
      this.views
        .get(this.editing)
        ?.input.setAttribute('aria-activedescendant', `calculatorSuggest${this.suggestion}`);
    }
  }

  private placeCompletion(input: HTMLInputElement): void {
    const r = input.getBoundingClientRect();
    const height =
      this.completionMode === 'list' ? Math.min(174, this.suggestions.length * 28) : 30;
    const width = Math.min(340, Math.max(220, r.width || 260));
    this.popup.style.width = `${width}px`;
    this.popup.style.left = `${Math.max(8, Math.min(r.left, window.innerWidth - width - 8))}px`;
    this.popup.style.top = `${r.bottom + height + 2 < window.innerHeight ? r.bottom + 2 : Math.max(8, r.top - height - 2)}px`;
  }

  hideCompletion(): void {
    this.popup.hidden = true;
    this.completionMode = '';
    if (this.editing !== null) {
      const input = this.views.get(this.editing)?.input;
      input?.setAttribute('aria-expanded', 'false');
      input?.removeAttribute('aria-activedescendant');
    }
  }

  complete(name: string): void {
    if (this.editing === null) return;
    const index = this.editing;
    const input = this.views.get(index)?.input;
    const caretPos = input?.selectionStart ?? null;
    if (!input || caretPos === null) return;
    const context = this.getFormulaContext(input.value, caretPos);
    if (!context) return;
    const suffix = input.value.slice(context.end);
    const insert = name + (suffix.startsWith('(') ? '' : '(');
    const text = input.value.slice(0, context.start) + insert + suffix;
    this.edit(index, text);
    input.value = text;
    const caret = context.start + insert.length + (suffix.startsWith('(') ? 1 : 0);
    input.focus({ preventScroll: true });
    input.setSelectionRange(caret, caret);
    this.hideCompletion();
    void this.updateCompletion();
  }

  handleKey(event: KeyboardEvent): boolean {
    if (this.host.hidden) return false;
    const target = event.target instanceof Element ? event.target : null;
    const inside = target?.closest('#calculatorWindow');
    if (!inside) {
      if (!this.picking || !target?.closest('.grid-region')) return false;
      if (event.key === 'Enter') {
        event.preventDefault();
        this.finish(false);
        return true;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        this.cancel();
        return true;
      }
      return false;
    }
    const input = target?.closest<HTMLElement>('[data-formula-row]');
    const note = target?.closest<HTMLInputElement>('[data-note-row]');
    if (note && !isComposing(event)) {
      const index = Number(note.dataset.noteRow);
      if (
        event.key === 'Enter' ||
        event.key === 'ArrowDown' ||
        event.key === 'ArrowUp' ||
        event.key === 'Tab'
      ) {
        event.preventDefault();
        const direction = event.shiftKey || event.key === 'ArrowUp' ? -1 : 1;
        this.focus(index + direction, event.key === 'Tab' ? 'formula' : 'note');
      } else if (event.key === 'Escape' && this.noteEdit) {
        event.preventDefault();
        this.model.setNote(index, this.noteEdit.before);
        this.noteEdit = null;
        this.render();
        note.select();
      }
      return true;
    }
    if (input && !isComposing(event)) {
      if (
        !this.popup.hidden &&
        this.completionMode === 'list' &&
        ['ArrowDown', 'ArrowUp', 'Enter', 'Tab'].includes(event.key)
      ) {
        event.preventDefault();
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          this.suggestion =
            (this.suggestion + (event.key === 'ArrowDown' ? 1 : -1) + this.suggestions.length) %
            this.suggestions.length;
          this.renderSuggestions();
        } else {
          const chosen = this.suggestions[this.suggestion];
          if (chosen) this.complete(chosen.name);
        }
        return true;
      }
      if (event.key === 'Enter' || event.key === 'Tab') {
        event.preventDefault();
        this.finish(event.shiftKey);
      } else if (event.key === 'Escape') {
        event.preventDefault();
        if (!this.popup.hidden) this.hideCompletion();
        else this.cancel();
      } else if (
        (event.key === 'ArrowUp' || event.key === 'ArrowDown') &&
        !event.altKey &&
        !event.ctrlKey &&
        !event.metaKey
      ) {
        event.preventDefault();
        void this.commit(this.model.active);
        this.focus(this.model.active + (event.key === 'ArrowUp' ? -1 : 1));
      }
    }
    const output = target?.closest<HTMLElement>('[data-result-row]');
    if (output && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'c') {
      const row = this.model.get(Number(output.dataset.resultRow));
      if (row.result) {
        event.preventDefault();
        Promise.resolve(this.o.copy(row.result.copy)).catch((error: unknown) =>
          this.o.toast(errorMessage(error), 'error'),
        );
      }
    }
    return true;
  }

  finish(reverse: boolean): void {
    this.hideCompletion();
    const index = this.editing ?? this.model.active;
    void this.commit(index, true);
    this.editing = null;
    this.pick = null;
    this.focus(index + (reverse ? -1 : 1));
  }

  cancel(): void {
    const index = this.editing ?? this.model.active;
    this.cancelJob(index);
    this.model.cancel(index);
    this.editing = null;
    this.pick = null;
    this.hideCompletion();
    this.render();
    this.views.get(index)?.input.select();
  }

  clamp(): void {
    if (this.host.hidden) return;
    const r = this.host.getBoundingClientRect();
    const left = Math.max(8, Math.min(r.left, window.innerWidth - r.width - 8));
    const top = Math.max(8, Math.min(r.top, window.innerHeight - r.height - 8));
    if (this.host.style.left !== `${left}px`) this.host.style.left = `${left}px`;
    if (this.host.style.top !== `${top}px`) this.host.style.top = `${top}px`;
    this.host.style.right = 'auto';
    this.host.style.bottom = 'auto';
  }

  private bindDrag(): void {
    const handle = requireEl('calculatorDrag');
    let drag: {
      readonly id: number;
      readonly x: number;
      readonly y: number;
      readonly left: number;
      readonly top: number;
    } | null = null;
    let frame = 0;
    let pending: { readonly x: number; readonly y: number } | null = null;

    const move = (): void => {
      frame = 0;
      if (!drag || !pending) return;
      this.host.style.left = `${drag.left + pending.x - drag.x}px`;
      this.host.style.top = `${drag.top + pending.y - drag.y}px`;
      this.clamp();
    };

    handle.addEventListener('pointerdown', (event: PointerEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      if (event.button !== 0 || target?.closest('button')) return;
      this.hideCompletion();
      const r = this.host.getBoundingClientRect();
      drag = {
        id: event.pointerId,
        x: event.clientX,
        y: event.clientY,
        left: r.left,
        top: r.top,
      };
      handle.setPointerCapture(event.pointerId);
      event.preventDefault();
    });
    handle.addEventListener('pointermove', (event: PointerEvent) => {
      if (drag?.id !== event.pointerId) return;
      pending = { x: event.clientX, y: event.clientY };
      if (!frame) frame = requestAnimationFrame(move);
    });
    const end = (event: PointerEvent): void => {
      if (drag?.id === event.pointerId) {
        if (frame) {
          cancelAnimationFrame(frame);
          move();
        }
        drag = null;
        pending = null;
      }
    };
    handle.addEventListener('pointerup', end);
    handle.addEventListener('pointercancel', end);
  }
}
