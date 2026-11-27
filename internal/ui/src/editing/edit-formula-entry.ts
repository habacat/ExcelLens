/**
 * Interactive worksheet formula editor: pointer/arrow cell-range pointing,
 * cross-sheet formula editing, F4 absolute reference cycling, F2 point-key toggle,
 * local formula undo/redo journal, and live reference highlight areas.
 */
import type { EditAddress } from '../contracts/edit.ts';
import { parseRef } from '../core/cell-address.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireSelect } from '../core/dom.ts';
import type { DragPointKind, DragPointResult } from './edit-reference-view.ts';
import {
  WorksheetCompletion,
  type FormulaCompletionHost,
} from './edit-formula-completion.ts';
import {
  FORMULA_REF_PALETTE,
  cycleAbsolute,
  formatReference,
  formulaContext,
  parseFormula,
  type FormulaPoint,
  type ParsedFormulaRef,
} from './formula-entry-model.ts';

const HANDLES = '.grid-cell[data-ref],.column-head[data-col],.row-head,.pinned-row-head';
const CONTROLS = '.column-filter,.auto-filter-button,.column-resize-grip,.row-resize-grip';

export interface FormulaPickState {
  readonly from: number;
  end: number;
  text: string;
  readonly anchor: FormulaPoint;
  readonly point: FormulaPoint;
  readonly sheet: string;
}

export interface FormulaHistorySnapshot {
  readonly value: string;
  readonly start: number;
  readonly end: number;
  readonly pick: FormulaPickState | null;
}

export interface FormulaHistoryJournal {
  readonly undo: FormulaHistorySnapshot[];
  readonly redo: FormulaHistorySnapshot[];
}

export interface ReferenceHighlightArea {
  readonly row1: number;
  readonly col1: number;
  readonly row2: number;
  readonly col2: number;
  readonly color: string;
}

export interface FormulaEntryDraft {
  readonly address: EditAddress;
  readonly ref: string;
  text: string;
  typed?: boolean;
  field: HTMLTextAreaElement;
  pointKeys: boolean;
  formulaHistory?: FormulaHistoryJournal;
}

export interface FormulaEntrySelectedCell {
  readonly row: number;
  readonly col: number;
  readonly ref?: string;
  readonly visual?: number;
}

export interface FormulaEntryHost extends FormulaCompletionHost {
  readonly input: HTMLTextAreaElement;
  readonly formula: HTMLTextAreaElement;
  readonly region: HTMLElement;
  readonly composing: boolean;
  readonly moving: boolean;
  readonly stale: boolean;
  readonly draft: FormulaEntryDraft | null;
  readonly selected: FormulaEntrySelectedCell | null;
  readonly owner: FormulaCompletionHost['owner'] & {
    readonly pending: unknown;
    readonly data:
      | (NonNullable<FormulaCompletionHost['owner']['data']> & {
          readonly encoding?: string;
        })
      | null;
    context(): { readonly sheet: string | null };
  };
  readonly view: {
    ready(): boolean;
    paintReferences(): void;
    bounds(ref: string): {
      readonly rect: {
        readonly left: number;
        readonly right: number;
        readonly top: number;
        readonly bottom: number;
      };
    } | null;
    dragPoint(x: number, y: number, kind: DragPointKind, scroll?: boolean): DragPointResult;
    referenceStep(
      base: FormulaEntrySelectedCell,
      key: string,
    ): FormulaPoint | null | Promise<FormulaPoint | null>;
  };
  focus(): void;
  status(message: string, error?: boolean): void;
  schedulePosition(): void;
}

interface PointerDragGesture {
  readonly id: number;
  readonly before: FormulaHistorySnapshot;
  readonly anchor: FormulaPoint;
  x: number;
  y: number;
}

interface PointerEndEventLike {
  readonly pointerId: number;
  readonly clientX?: number;
  readonly clientY?: number;
}

export class WorksheetFormulaEntry {
  public readonly grid: FormulaEntryHost;
  public refs: ParsedFormulaRef[] = [];
  public sequence = 0;
  public readonly completion: WorksheetCompletion;
  public pick: FormulaPickState | null = null;
  public handledClick = false;
  public pendingStep: Promise<boolean | undefined> | null = null;
  private gesture: PointerDragGesture | null = null;
  private frame = 0;

  constructor(editor: FormulaEntryHost) {
    this.grid = editor;
    this.completion = new WorksheetCompletion(editor, (field, from, to, value, offerNext) => {
      this.record(field);
      this.replace(field, from, to, value);
      this.changed(field, false, offerNext);
    });
    const region = editor.region;
    region.addEventListener('pointermove', (e: PointerEvent) => this.move(e));
    region.addEventListener('pointerup', (e: PointerEvent) => this.end(e, false));
    region.addEventListener('pointercancel', (e: PointerEvent) => this.end(e, true));
    region.addEventListener('lostpointercapture', (e: PointerEvent) => this.end(e, true));
    for (const field of [editor.input, editor.formula]) {
      field.addEventListener('blur', () => this.completion.hide());
      field.addEventListener('click', () => this.caret(field));
      field.addEventListener('keyup', (e: KeyboardEvent) => {
        if (['Home', 'End', 'ArrowLeft', 'ArrowRight'].includes(e.key) && !this.pick) {
          this.caret(field);
        }
      });
    }
  }

  get allowsFormula(): boolean {
    const g = this.grid;
    const kind = requireSelect('editGridKind').value;
    return Boolean(g.draft) && !g.owner.data?.encoding && (kind === 'auto' || kind === 'formula');
  }

  get enabled(): boolean {
    return this.allowsFormula && Boolean(this.grid.draft?.text.startsWith('='));
  }

  get field(): HTMLTextAreaElement {
    return this.grid.draft?.field ?? this.grid.input;
  }

  snapshot(f: HTMLTextAreaElement): FormulaHistorySnapshot {
    const pick = this.currentPick(f);
    return {
      value: f.value,
      start: f.selectionStart,
      end: f.selectionEnd,
      pick: pick ? { ...pick } : null,
    };
  }

  record(f: HTMLTextAreaElement, before: FormulaHistorySnapshot = this.snapshot(f)): void {
    const d = this.grid.draft;
    if (!d) return;
    const journal = (d.formulaHistory ??= { undo: [], redo: [] });
    if (journal.undo.at(-1)?.value !== before.value) {
      journal.undo.push(before);
      if (journal.undo.length > 64) journal.undo.shift();
    }
    journal.redo.length = 0;
  }

  history(f: HTMLTextAreaElement, redo: boolean): boolean {
    const journal = this.grid.draft?.formulaHistory;
    if (!journal) return false;
    const from = redo ? journal.redo : journal.undo;
    const before = from.pop();
    if (before) {
      (redo ? journal.undo : journal.redo).push(this.snapshot(f));
      this.replace(f, 0, f.value.length, before.value);
      f.setSelectionRange(before.start, before.end);
      this.pick = before.pick;
      this.changed(f, true);
    }
    return true;
  }

  beforeInput(e: InputEvent, f: HTMLTextAreaElement): void {
    if (this.grid.composing || !this.grid.draft) return;
    if (e.inputType === 'historyUndo' || e.inputType === 'historyRedo') {
      if (this.history(f, e.inputType === 'historyRedo')) e.preventDefault();
      return;
    }
    if (
      this.enabled ||
      this.grid.draft.formulaHistory ||
      (this.allowsFormula && f.selectionStart === 0 && e.data?.startsWith('='))
    ) {
      this.record(f);
    }
  }

  canPoint(): boolean {
    if (!this.enabled || this.grid.composing) return false;
    const f = this.field;
    const a = f.selectionStart;
    const b = f.selectionEnd;
    const context = formulaContext(f.value, a);
    if (!context) return false;
    return a !== b || context.operand || Boolean(this.currentPick(f));
  }

  currentPick(field: HTMLTextAreaElement): FormulaPickState | null {
    const p = this.pick;
    return p &&
      field.selectionStart === field.selectionEnd &&
      field.selectionEnd === p.end &&
      field.value.slice(p.from, p.end) === p.text
      ? p
      : null;
  }

  replace(field: HTMLTextAreaElement, from: number, to: number, value: string): void {
    field.setRangeText(value, from, to, 'end');
    const g = this.grid;
    if (g.draft) {
      g.draft.text = field.value;
      g.draft.typed = true;
      g.draft.field = field;
    }
    (field === g.input ? g.formula : g.input).value = field.value;
    g.schedulePosition();
  }

  changed(field: HTMLTextAreaElement, keepPick = false, offerNext = true): void {
    if (!keepPick) {
      ++this.sequence;
      this.pick = null;
    }
    const draft = this.grid.draft;
    if (draft) draft.field = field;
    const hadRefs = this.refs.length;
    this.refs =
      this.enabled && draft ? parseFormula(draft.text, draft.address.sheet) : [];
    if (hadRefs || this.refs.length) this.grid.view.paintReferences();
    if (offerNext && this.enabled && draft && !this.grid.composing && !this.gesture) {
      void this.completion.update(field, draft.text);
    } else {
      this.completion.hide();
    }
  }

  caret(field: HTMLTextAreaElement): void {
    if (!this.enabled || !this.grid.draft) return;
    if (!this.currentPick(field)) this.pick = null;
    this.grid.draft.field = field;
    void this.completion.update(field, field.value);
  }

  areas(sheet: string): ReferenceHighlightArea[] {
    return this.refs
      .filter((r) => !r.external && r.sheet.toLowerCase() === sheet.toLowerCase())
      .map((r) => ({
        row1: r.start.row,
        col1: r.start.col,
        row2: r.end.row,
        col2: r.end.col,
        color: FORMULA_REF_PALETTE[r.colorIndex] ?? FORMULA_REF_PALETTE[0],
      }));
  }

  switchDraft(name: string): FormulaEntryDraft | null {
    const g = this.grid;
    if (g.moving || g.stale || g.owner.pending || !this.canPoint()) return null;
    if (name !== g.owner.context().sheet) {
      ++this.sequence;
      const f = this.field;
      const a = f.selectionStart;
      const b = f.selectionEnd;
      g.formula.focus({ preventScroll: true });
      g.formula.setSelectionRange(a, b);
      this.completion.hide();
    }
    return g.draft;
  }

  focusAfterSwitch(): void {
    const g = this.grid;
    const d = g.draft;
    if (!this.enabled || !d) {
      g.focus();
      return;
    }
    const last = this.field;
    const start = last.selectionStart;
    const end = last.selectionEnd;
    const foreign = g.owner.context().sheet !== d.address.sheet;
    const next = foreign ? g.formula : last;
    next.focus({ preventScroll: true });
    next.setSelectionRange(start, end);
    d.field = next;
    g.status(foreign ? `正在编辑 ${d.address.sheet}!${d.ref}` : '');
    g.schedulePosition();
    this.changed(next, true);
  }

  reset(): void {
    ++this.sequence;
    this.stop();
    this.pick = null;
    this.refs = [];
    this.completion.hide();
    this.grid.view.paintReferences();
  }

  point(node: Element | null | undefined): FormulaPoint | null {
    if (!(node instanceof HTMLElement)) return null;
    if (node.dataset.ref) {
      const p = parseRef(node.dataset.ref, Infinity, Infinity);
      return p ? { ...p, kind: 'cell', visual: Number(node.dataset.visualIndex) || p.row } : null;
    }
    if (node.classList.contains('column-head')) {
      return { kind: 'column', col: Number(node.dataset.col) };
    }
    const rowEl = node.closest('[data-row]');
    const rowAttr = rowEl instanceof HTMLElement ? rowEl.dataset.row : undefined;
    const row = Number(rowAttr || node.textContent);
    return row > 0 ? { kind: 'row', row } : null;
  }

  hit(x: number, y: number): FormulaPoint | null {
    for (const element of document.elementsFromPoint(x, y)) {
      if (!this.grid.region.contains(element)) continue;
      const node = element.closest(HANDLES);
      if (node) return this.point(node);
    }
    return null;
  }

  begin(event: PointerEvent): boolean {
    this.handledClick = false;
    const g = this.grid;
    const target = event.target;
    if (
      event.button !== 0 ||
      this.gesture ||
      !this.canPoint() ||
      g.moving ||
      g.stale ||
      g.owner.pending ||
      !g.view.ready() ||
      !(target instanceof Element) ||
      target.closest(CONTROLS)
    ) {
      return false;
    }
    if (target === g.input && g.draft) {
      const r = g.view.bounds(g.draft.ref)?.rect;
      if (
        r &&
        event.clientX >= r.left &&
        event.clientX < r.right &&
        event.clientY >= r.top &&
        event.clientY < r.bottom
      ) {
        return false;
      }
    }
    const point =
      this.point(target.closest(HANDLES)) ??
      (target === g.input ? this.hit(event.clientX, event.clientY) : null);
    if (!point) return false;
    ++this.sequence;
    const f = this.field;
    const previous = this.currentPick(f);
    const sheet = g.owner.context().sheet ?? '';
    this.gesture = {
      id: event.pointerId,
      before: this.snapshot(f),
      anchor:
        event.shiftKey && previous?.sheet === sheet && point.kind === previous.anchor.kind
          ? previous.anchor
          : point,
      x: event.clientX,
      y: event.clientY,
    };
    this.completion.hide();
    this.writePoint(this.gesture.anchor, point, (event.ctrlKey || event.metaKey) && Boolean(previous));
    this.handledClick = true;
    g.region.setPointerCapture(event.pointerId);
    return true;
  }

  writePoint(anchor: FormulaPoint, point: FormulaPoint, append = false): void {
    const g = this.grid;
    const draft = g.draft;
    if (!draft) return;
    const f = this.field;
    const sheet = g.owner.context().sheet ?? '';
    const prev = this.currentPick(f);
    let from = prev && !append ? prev.from : Math.max(1, f.selectionStart);
    const to = Math.max(from, f.selectionEnd);
    const ref = formatReference(anchor, point, sheet, draft.address.sheet, append ? '' : prev?.text);
    if (!append && prev?.text === ref && prev.sheet === sheet) return;
    if (!this.gesture) this.record(f);
    this.replace(f, from, to, (append ? ',' : '') + ref);
    if (append) from++;
    this.pick = { from, end: f.selectionEnd, text: ref, anchor, point, sheet };
    draft.pointKeys = true;
    this.changed(f, true);
  }

  private move(e: PointerEvent): void {
    const drag = this.gesture;
    if (drag?.id !== e.pointerId) return;
    e.preventDefault();
    drag.x = e.clientX;
    drag.y = e.clientY;
    if (!this.frame) this.frame = requestAnimationFrame(() => this.track());
  }

  private track(): void {
    this.frame = 0;
    const drag = this.gesture;
    if (!drag) return;
    const pos = this.grid.view.dragPoint(drag.x, drag.y, drag.anchor.kind);
    const p = this.hit(pos.x, pos.y);
    if (p?.kind === drag.anchor.kind) this.writePoint(drag.anchor, p);
    if (pos.scrolling) this.frame = requestAnimationFrame(() => this.track());
  }

  private stop(): void {
    const id = this.gesture?.id;
    this.gesture = null;
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    if (id !== undefined && this.grid.region.hasPointerCapture(id)) {
      this.grid.region.releasePointerCapture(id);
    }
  }

  private end(e: PointerEndEventLike, cancel: boolean): void {
    const drag = this.gesture;
    if (drag?.id !== e.pointerId) return;
    const f = this.field;
    if (!cancel) {
      const pos = this.grid.view.dragPoint(
        e.clientX ?? drag.x,
        e.clientY ?? drag.y,
        drag.anchor.kind,
        false,
      );
      const p = this.hit(pos.x, pos.y);
      if (p?.kind === drag.anchor.kind) this.writePoint(drag.anchor, p);
      if (drag.before.value !== f.value) this.record(f, drag.before);
    } else {
      this.replace(f, 0, f.value.length, drag.before.value);
      f.setSelectionRange(drag.before.start, drag.before.end);
      this.pick = drag.before.pick;
    }
    this.stop();
    f.focus({ preventScroll: true });
    this.changed(f, true);
  }

  key(e: KeyboardEvent): boolean {
    if (
      this.grid.draft &&
      (e.ctrlKey || e.metaKey) &&
      !e.altKey &&
      ['z', 'y'].includes(e.key.toLowerCase())
    ) {
      return this.history(this.field, e.key.toLowerCase() === 'y' || e.shiftKey);
    }
    if (!this.enabled || !this.grid.draft) return false;
    if (this.gesture && e.key === 'Escape') {
      this.end({ pointerId: this.gesture.id }, true);
      return true;
    }
    if (this.completion.key(e)) return true;
    const g = this.grid;
    const draft = g.draft;
    if (!draft) return false;
    const f = this.field;
    if (e.key === 'F4' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const p = this.currentPick(f);
      const replacement = cycleAbsolute(
        f.value,
        p?.from ?? f.selectionStart,
        f.selectionEnd,
        draft.address.sheet,
      );
      if (replacement) {
        const { from, until, value } = replacement;
        this.record(f);
        this.replace(f, from, until, value);
        if (p) {
          p.end += value.length - (until - from);
          p.text = f.value.slice(p.from, p.end);
          f.setSelectionRange(p.end, p.end);
        }
        this.changed(f, Boolean(p));
      }
      return true;
    }
    if (e.key === 'F2') {
      draft.pointKeys = !draft.pointKeys;
      this.pick = null;
      return true;
    }
    if (
      !draft.pointKeys ||
      !this.canPoint() ||
      !e.key.startsWith('Arrow') ||
      e.ctrlKey ||
      e.metaKey ||
      e.altKey ||
      !g.view.ready()
    ) {
      return false;
    }
    this.step(e.key, e.shiftKey);
    return true;
  }

  step(key: string, extend: boolean): void {
    const g = this.grid;
    const d = g.draft;
    const sheet = g.owner.context().sheet ?? '';
    const sequence = this.sequence;
    const current = (): boolean =>
      g.draft === d && (g.owner.context().sheet ?? '') === sheet && this.sequence === sequence;
    const move = (): Promise<undefined> | undefined => {
      if (!current()) return undefined;
      const previous = this.currentPick(this.field);
      const local = previous?.sheet === sheet && previous.point.kind === 'cell';
      const base: FormulaEntrySelectedCell | null = local
        ? previous.point
        : g.selected;
      if (!base) return undefined;
      const anchor: FormulaPoint = local
        ? previous.anchor
        : { ...base, kind: 'cell' };
      const apply = (point: FormulaPoint | null): undefined => {
        if (point && current()) this.writePoint(extend ? anchor : point, point);
        return undefined;
      };
      const point = g.view.referenceStep(base, key);
      if (point instanceof Promise) return point.then(apply);
      apply(point);
      return undefined;
    };
    const task: Promise<undefined> | undefined = this.pendingStep
      ? this.pendingStep.then(move)
      : move();
    if (task) {
      const handled = task.catch((error: unknown) => {
        if (current()) g.status(errorMessage(error), true);
        return false as const;
      });
      this.pendingStep = handled;
      void handled.then(() => {
        if (this.pendingStep === handled) this.pendingStep = null;
      });
    }
  }
}
