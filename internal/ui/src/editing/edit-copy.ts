/**
 * Editable Copy controller: manages edit document lifecycle, cell/batch/rule/structure
 * commits, revision refresh, what-if goal-seek/data-table trials, generated file saving,
 * and coordination with the direct in-grid `SheetEditor` and `EditTools`.
 */
import { t as localize } from '../core/i18n.ts';
import type { ApiClient, HttpError } from '../contracts/common.ts';
import type {
  EditAddress,
  EditArtifact,
  EditBatchPreviewResponse,
  EditChange,
  EditCommitResponse,
  EditInfo,
  EditKey,
  EditOpenWorkbookResponse,
  EditOperation,
  EditOperationAction,
  EditReadResponse,
  EditRule,
  EditSheetMeta,
  EditStateResponse,
  EditStructure,
  EditValue,
  EditValueKind,
} from '../contracts/edit.ts';
import type { EditLimits } from '../contracts/formats.ts';
import type { SourceInfo } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { columnNumber } from '../core/cell-address.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import {
  byId,
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
  requireTextArea,
} from '../core/dom.ts';
import { parseClipboardTSV } from '../grid/clipboard.ts';
import { EditModel } from './edit-copy-model.ts';
import { SheetEditor, type SheetEditorBridge } from './edit-grid.ts';
import { EditTools } from './edit-tools.ts';
import { displayFormula } from './formula-entry-model.ts';
import {
  decodeEditArtifact,
  decodeEditBatchPreviewResponse,
  decodeEditChoicesResponse,
  decodeEditClipboard,
  decodeEditCommitResponse,
  decodeEditListResponse,
  decodeEditReadResponse,
  decodeEditStateResponse,
  decodeWhatIfResult,
} from '../core/http-decoders.generated.ts';

import { decodeOpenedWorkbook } from '../core/workbook-response.ts';

const RULE_INPUTS = [
  'RuleStart',
  'RuleEnd',
  'RuleType',
  'RuleOperator',
  'RuleFirst',
  'RuleSecond',
  'RuleList',
  'RuleReference',
  'AllowBlank',
] as const;

type RuleInputId = (typeof RULE_INPUTS)[number];

interface PendingCommitRequest {
  readonly document: EditKey;
  readonly proof?: string;
  readonly direct?: boolean;
  readonly operation: EditOperation;
}

interface EditListItem extends EditInfo {
  readonly busy?: boolean;
  readonly error?: string;
}

/** @httpResponse EditListResponse */
export interface EditListResponse {
  readonly items: readonly EditListItem[];
}

/** @httpResponse EditChoicesResponse */
export interface EditChoicesResponse {
  readonly revision: string;
  readonly choices: readonly string[];
}

export interface EditCopyContext {
  readonly book: string;
  readonly fileName: string;
  readonly source: SourceInfo | null;
  readonly sheet: string | null;
  readonly ref?: string;
}

export interface EditCopyCommitOptions {
  readonly proof?: string;
  readonly rule?: EditRule;
  readonly direct?: boolean;
  readonly structure?: EditStructure;
  readonly inferNumberFormat?: boolean;
  readonly entryAnchor?: EditAddress;
  readonly expandTable?: boolean;
}

export interface EditCopyOptions {
  readonly context: () => EditCopyContext;
  readonly api: ApiClient;
  readonly openDialog: (id: string) => void;
  readonly adopt: (
    data: EditOpenWorkbookResponse,
    options?: { readonly revisionRef?: string },
  ) => Promise<void> | void;
  readonly canOpen: (action?: string) => boolean;
  readonly limits: EditLimits;
  readonly colName: (col: number) => string;
  readonly fmtBytes: (bytes: number) => string;
  readonly grid: SheetEditorBridge;
  readonly session?: { readonly cacheCleared: boolean };
}

function el(suffix: string): HTMLElement {
  return requireEl(`editCopy${suffix}`);
}

function inputEl(suffix: string): HTMLInputElement {
  return requireInput(`editCopy${suffix}`);
}

function selectEl(suffix: string): HTMLSelectElement {
  return requireSelect(`editCopy${suffix}`);
}

function buttonEl(suffix: string): HTMLButtonElement {
  return requireButton(`editCopy${suffix}`);
}

function textAreaEl(suffix: string): HTMLTextAreaElement {
  return requireTextArea(`editCopy${suffix}`);
}

function dialogEl(): HTMLDialogElement {
  return requireDialog('editCopyDialog');
}

function detailsEl(suffix: string): HTMLDetailsElement {
  const node = el(suffix);
  if (!(node instanceof HTMLDetailsElement)) {
    throw new Error(`Expected HTMLDetailsElement for #editCopy${suffix}`);
  }
  return node;
}

function keyOf({ id, binding }: EditKey): EditKey {
  return { id, binding };
}

function equalsValue(a: EditValue, b: EditValue): boolean {
  return a.kind === b.kind && a.raw === b.raw;
}

function httpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function parseEditValueKind(value: string): EditValueKind {
  if (
    value === 'blank' ||
    value === 'text' ||
    value === 'number' ||
    value === 'boolean' ||
    value === 'formula' ||
    value === 'date' ||
    value === 'error' ||
    value === 'missing'
  ) {
    return value;
  }
  return 'text';
}

function setControlDisabled(node: HTMLElement, disabled: boolean): void {
  if (
    node instanceof HTMLButtonElement ||
    node instanceof HTMLInputElement ||
    node instanceof HTMLSelectElement ||
    node instanceof HTMLTextAreaElement
  ) {
    node.disabled = disabled;
  }
}

export class EditCopy {
  public readonly context: () => EditCopyContext;
  public readonly api: ApiClient;
  private readonly openDialog: (id: string) => void;
  private readonly adopt: EditCopyOptions['adopt'];
  private readonly canOpen: (action?: string) => boolean;
  public readonly limits: EditLimits;
  public readonly colName: (col: number) => string;
  private readonly fmtBytes: (bytes: number) => string;
  private readonly session?: { readonly cacheCleared: boolean };
  private readonly extensions: Set<string>;
  public readonly grid: SheetEditor;
  public readonly tools: EditTools;

  public data: EditStateResponse | null = null;
  public cell: EditReadResponse | null = null;
  public pending: PendingCommitRequest | null = null;
  public abort: AbortController | null = null;
  public invalidated = false;

  private starting = false;
  private escaped = false;
  private initialText = '';
  private initialKind: EditValueKind = 'text';
  private initialRule = '';
  private pasteRaw: string | null = null;
  private previewed: EditBatchPreviewResponse | null = null;

  constructor(options: EditCopyOptions) {
    this.context = options.context;
    this.api = options.api;
    this.openDialog = options.openDialog;
    this.adopt = options.adopt;
    this.canOpen = options.canOpen;
    this.limits = options.limits;
    this.colName = options.colName;
    this.fmtBytes = options.fmtBytes;
    this.session = options.session;
    this.extensions = new Set(options.limits.extensions);
    this.grid = new SheetEditor(this, options.grid);
    this.tools = new EditTools(this);

    buttonEl('Open').addEventListener('click', () => {
      void this.show();
    });
    buttonEl('Close').addEventListener('click', () => this.close());
    dialogEl().addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    buttonEl('Cancel').addEventListener('click', () => this.abort?.abort());
    buttonEl('ListRefresh').addEventListener('click', () => this.list());
    buttonEl('History').addEventListener('click', () => this.list());
    buttonEl('Refresh').addEventListener('click', () => this.refresh());
    el('AddressForm').addEventListener('submit', (event: Event) => {
      event.preventDefault();
      this.read();
    });
    selectEl('Sheet').addEventListener('change', () => {
      this.selectFirst();
      this.read();
    });
    inputEl('Address').addEventListener('input', () => {
      this.clearPreview();
      this.controls();
    });
    const valueInput = textAreaEl('Value');
    valueInput.addEventListener('input', () => this.controls());
    selectEl('Kind').addEventListener('change', () => this.controls());
    valueInput.addEventListener('keydown', (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && !isComposing(event)) {
        event.preventDefault();
        this.applyCell();
      }
    });
    buttonEl('DiscardInput').addEventListener('click', () => this.setCell(this.cell));
    buttonEl('Apply').addEventListener('click', () => this.applyCell());
    buttonEl('Undo').addEventListener('click', () => {
      void this.commit('undo');
    });
    buttonEl('Redo').addEventListener('click', () => {
      void this.commit('redo');
    });
    selectEl('TrialMode').addEventListener('change', () => this.trialMode());
    el('Trial').addEventListener('input', () => {
      el('TrialOutput').replaceChildren();
      el('TrialStatus').textContent = '';
    });
    buttonEl('TrialRun').addEventListener('click', () => {
      void this.whatIf();
    });
    buttonEl('Copy').addEventListener('click', () => this.copy());
    const batchText = textAreaEl('BatchText');
    batchText.addEventListener('input', () => {
      this.pasteRaw = null;
      this.clearPreview();
    });
    batchText.addEventListener('paste', (event: ClipboardEvent) => {
      const raw = event.clipboardData?.getData('text/plain');
      if (!raw || raw.length > options.limits.requestBytes) return;
      event.preventDefault();
      batchText.value = raw;
      this.pasteRaw = raw;
      this.clearPreview();
    });
    for (const id of ['SkipBlank', 'FillEnd', 'FillStep', 'FillMode', 'PasteMode']) {
      el(id).addEventListener('input', () => {
        this.clearPreview();
        this.controls();
      });
    }
    selectEl('FillMode').addEventListener('change', () => this.fillControls());
    buttonEl('PreviewPaste').addEventListener('click', () => this.preview('paste'));
    buttonEl('PreviewFill').addEventListener('click', () => this.preview('fill'));
    buttonEl('ApplyBatch').addEventListener('click', () => {
      const plan = this.previewed;
      if (plan) {
        void this.commit(plan.rule ? 'rule' : 'change', plan.changes, {
          proof: plan.proof,
          rule: plan.rule,
        });
      }
    });
    buttonEl('PreviewRule').addEventListener('click', () => this.previewRule());
    for (const id of RULE_INPUTS) {
      el(id).addEventListener('input', () => {
        this.clearPreview();
        this.ruleControls();
      });
    }
    buttonEl('ReadChoices').addEventListener('click', () => this.readChoices());
    selectEl('Choice').addEventListener('input', () => this.controls());
    buttonEl('ApplyChoice').addEventListener('click', () => {
      const raw = selectEl('Choice').value;
      if (
        raw &&
        this.cell &&
        this.editable &&
        !this.dirty &&
        !equalsValue(this.cell.value, { kind: 'text', raw })
      ) {
        void this.commit('change', [{ ...this.cell.address, value: { kind: 'text', raw } }]);
      }
    });
    buttonEl('Save').addEventListener('click', () => {
      void this.save();
    });
    window.addEventListener('beforeunload', (event: BeforeUnloadEvent) => {
      if (this.session?.cacheCleared) return;
      if (this.grid.dirty || this.pending || (dialogEl().open && this.dirty)) {
        event.preventDefault();
      }
    });
  }

  availability(active: boolean): void {
    const { fileName, source } = this.context();
    const dot = fileName.lastIndexOf('.');
    buttonEl('Open').hidden =
      !active ||
      dot < 0 ||
      !this.extensions.has(fileName.slice(dot).toLowerCase()) ||
      Boolean(source?.incomplete) ||
      Boolean(source?.archiveMember);
  }

  post(command: string, body: unknown, signal?: AbortSignal): Promise<unknown> {
    return this.api(`/api/edits/${command}`, {
      method: 'POST',
      signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  }

  status(message: string, error = false): void {
    const node = el('Status');
    node.textContent = message;
    node.classList.toggle('is-error', error);
    if (this.grid.active || this.starting) this.grid.status(message, error);
  }

  async run(
    message: string,
    action: (signal: AbortSignal) => Promise<void>,
    cancellable = true,
  ): Promise<boolean> {
    if (this.abort) return false;
    const controller = new AbortController();
    this.abort = controller;
    const activity = beginActivity(message, { signal: controller.signal });
    this.status(message);
    this.controls();
    buttonEl('Cancel').hidden = !cancellable;
    try {
      await action(controller.signal);
      return true;
    } catch (error: unknown) {
      if (error instanceof Error && error.name === 'AbortError') {
        this.status(localize('ui.editCopy.requestCancelMadeCanRefresh'));
      } else {
        this.status(errorMessage(error), true);
      }
      if (httpStatus(error) === 409 && this.grid.active) this.grid.stale = true;
      return false;
    } finally {
      activity?.end();
      buttonEl('Cancel').hidden = true;
      this.abort = null;
      this.controls();
    }
  }

  close(): void {
    if (this.abort) {
      this.status(localize('ui.editCopy.waitCurrentOperationComplete'));
      return;
    }
    if (this.dirty || this.pending) {
      this.status(localize('ui.editCopy.applyOrAbandonCurrentInput'), true);
      return;
    }
    dialogEl().close();
    if (this.grid.active) {
      this.grid.selection();
      this.grid.focus();
    }
  }

  trialMode(): void {
    const table = selectEl('TrialMode').value === 'table';
    for (const label of el('Trial').querySelectorAll<HTMLElement>('[data-seek]')) {
      label.hidden = table;
    }
    for (const label of el('Trial').querySelectorAll<HTMLElement>('[data-trials]')) {
      label.hidden = !table;
    }
  }

  async whatIf(): Promise<void> {
    if (this.pending || this.dirty || this.abort || !this.data) {
      this.status(localize('ui.editCopy.confirmOrAbandonCurrentInput'));
      return;
    }
    const dataSnapshot = this.data;
    const doc = dataSnapshot.document;
    const mode = selectEl('TrialMode').value;
    const parseAddress = (value: string): EditAddress => {
      const parts = /^(?:(.+)!)?\$?([A-Za-z]+)\$?([1-9]\d*)$/.exec(value.trim());
      const letters = parts?.[2];
      const digits = parts?.[3];
      if (!parts || !letters || !digits) throw new Error(localize('ui.editCopy.enterValidCellAddress'));
      const sheetName = parts[1] ?? selectEl('Sheet').value;
      const sheet =
        sheetName.startsWith("'") && sheetName.endsWith("'")
          ? sheetName.slice(1, -1).replaceAll("''", "'")
          : sheetName;
      return { sheet, col: columnNumber(letters.toUpperCase()), row: Number(digits) };
    };
    const readField = (id: string): string => inputEl(`Trial${id}`).value.trim();
    const listField = (id: string): string[] =>
      readField(id)
        .split(/[，,;；\s]+/)
        .filter(Boolean);
    await this.run(localize('ui.editCopy.trialCalculation'), async (signal) => {
      el('TrialOutput').replaceChildren();
      el('TrialStatus').textContent = '';
      const secondText = mode === 'seek' ? '' : readField('Second');
      const trial: Record<string, unknown> = {
        mode,
        input: parseAddress(readField('Input')),
        target: parseAddress(readField('Target')),
      };
      if (mode === 'seek') {
        trial.goal = readField('Goal');
        trial.lower = readField('Lower');
        trial.upper = readField('Upper');
        trial.tolerance = readField('Tolerance');
      } else {
        trial.values = listField('Values');
        trial.secondValues = listField('SecondValues');
        if (secondText) trial.second = parseAddress(secondText);
      }
      const result = await this.post(
        'what-if',
        { document: keyOf(doc), revision: doc.revision, book: dataSnapshot.book, trial },
        signal,
      ).then(decodeWhatIfResult);
      el('TrialStatus').textContent =
        localize('ui.editCopy.calculations', { status: result.status, iterations: result.iterations }) +
        localize('ui.editCopy.revised', { detail: result.residual !== undefined ? ` ${localize('ui.editCopy.error', { residual: result.residual })}` : '', revision: doc.revision });
      const table = document.createElement('table');
      const tr = table.insertRow();
      const hasSecond = Boolean(secondText);
      for (const name of hasSecond ? [localize('ui.editCopy.enter'), localize('ui.editCopy.secondInput'), localize('ui.editCopy.targetResult')] : [localize('ui.editCopy.enter'), localize('ui.editCopy.targetResult')]) {
        const th = document.createElement('th');
        th.textContent = name;
        tr.append(th);
      }
      for (const point of result.points) {
        const row = table.insertRow();
        const values = hasSecond
          ? [point.input, point.second ?? '', point.error ?? point.result ?? '']
          : [point.input, point.error ?? point.result ?? ''];
        for (const value of values) row.insertCell().textContent = value;
      }
      el('TrialOutput').replaceChildren(table);
      this.status('');
    });
  }

  async show(): Promise<void> {
    const { book, sheet, ref } = this.context();
    if (!book || this.abort || this.grid.active || dialogEl().open) return;
    this.starting = true;
    const openBtn = buttonEl('Open');
    const label = byId('editCopyOpenLabel') ?? openBtn;
    openBtn.disabled = true;
    label.textContent = localize('ui.editCopy.checkingProgress');
    const ok = await this.run(localize('ui.editCopy.checkingSourceDataEditing'), async (signal) => {
      const data = await this.post('start', { book }, signal).then(decodeEditStateResponse);
      if (this.context().book !== book || document.body.classList.contains('home-view')) {
        throw new Error(localize('ui.editCopy.currentFileSwitched'));
      }
      if (!(await this.bindGridBase(data, signal, sheet, ref))) {
        await this.activate(data, sheet, ref, signal, false);
      }
      await this.grid.enter(data, signal);
    });
    this.starting = false;
    openBtn.disabled = false;
    label.textContent = localize('ui.common.editCopy');
    if (!ok && this.context().book === book && !document.body.classList.contains('home-view')) {
      el('Title').textContent = localize('ui.common.editCopy');
      el('Work').hidden = true;
      el('ListView').hidden = false;
      this.openDialog('editCopyDialog');
    }
  }

  advanced(tool?: string): Promise<boolean> | false {
    if (!this.grid.view.ready()) return false;
    const sheetAction = Boolean(tool?.endsWith('Sheet'));
    const table = tool === 'Table';
    const range = tool === 'Range';
    const pivot = !sheetAction && !table && !range ? this.tools.pivotAt(this.grid.selected) : null;
    this.tools.contextualPivot = Boolean(pivot);
    const sheetTitle =
      tool === 'renameSheet' ? localize('ui.editCopy.renameWorksheets') : tool === 'deleteSheet' ? localize('ui.editCopy.deleteWorksheet') : localize('ui.editCopy.worksheetOperations');
    el('Title').textContent = sheetAction
      ? sheetTitle
      : table
        ? localize('ui.editCopy.adjustTableRange')
        : range
          ? localize('ui.editCopy.regionalEditing')
          : pivot
            ? localize('ui.editCopy.pivotLayout')
            : localize('ui.editCopy.moreEditors');
    this.openDialog('editCopyDialog');
    el('Work').hidden = false;
    el('ListView').hidden = true;
    const { sheet, ref } = this.context();
    selectEl('Sheet').value = sheet ?? '';
    inputEl('Address').value = ref ?? '';
    this.cell = null;
    this.clearPreview();
    this.setCell(null);
    if (pivot) this.tools.openPivot(pivot);
    if (table) {
      this.tools.selected = 'Structure';
      selectEl('StructureKind').value = 'resizeTable';
    }
    if (sheetAction && tool) {
      this.tools.selected = 'Structure';
      selectEl('StructureKind').value = tool;
    }
    if (range) {
      this.tools.selected = 'Structure';
      this.tools.chooseRangeKind = true;
      selectEl('StructureKind').value = this.grid.view.merged() ? 'unmergeCells' : 'mergeCells';
    }
    return this.run(localize('ui.editCopy.readingSelection'), (signal) => this.fetchCell(signal)).then((ok) => {
      if (ok && sheetAction && !el('NewNameField').hidden) {
        const newName = inputEl('NewName');
        newName.focus();
        newName.select();
      }
      if (ok && table && dialogEl().open) {
        const rangeInput = inputEl('Range');
        rangeInput.focus();
        rangeInput.select();
      }
      return ok;
    });
  }

  list(): void {
    if (this.abort || this.dirty || this.pending) return;
    el('Work').hidden = true;
    el('ListView').hidden = false;
    el('Title').textContent = localize('ui.editCopy.savedEdits');
    void this.run(localize('ui.editCopy.readingEditCopies'), async (signal) => {
      const { items } = await this.api('/api/edits', { signal }, decodeEditListResponse);
      const listEl = el('List');
      listEl.replaceChildren();
      for (const item of items) {
        const line = this.item(
          item.name || localize('ui.common.editCopy'),
          item.error ?? (item.busy ? localize('ui.common.use') : localize('ui.editCopy.revise', { revision: item.revision })),
        );
        if (!item.error) line.append(this.button(localize('ui.editCopy.continueEditing'), () => this.resume(item)));
        listEl.append(line);
      }
      this.status(items.length ? '' : localize('ui.editCopy.noSavedEditsAvailable'));
    });
  }

  resume(item: EditKey): void {
    void this.run(localize('ui.editCopy.restoringEditingRevisions'), async (signal) => {
      const data = await this.post('state', { document: keyOf(item) }, signal).then(decodeEditStateResponse);
      const { sheet, ref } = this.context();
      if (!(await this.bindGridBase(data, signal, sheet, ref))) {
        await this.activate(data, sheet, ref, signal, false);
      }
      await this.grid.enter(data, signal);
      dialogEl().close();
      this.grid.focus();
    });
  }

  async activate(
    data: EditStateResponse,
    preferredSheet: string | null | undefined,
    preferredRef: string | undefined,
    signal: AbortSignal,
    read = true,
  ): Promise<void> {
    if (this.data?.document.id !== data.document.id) this.tools.selected = 'Copy';
    this.data = data;
    this.cell = null;
    this.pending = null;
    this.invalidated = false;
    el('Trial').hidden = Boolean(data.encoding);
    el('TrialOutput').replaceChildren();
    el('TrialStatus').textContent = '';
    this.clearPreview();
    this.pasteRaw = null;
    textAreaEl('BatchText').value = '';
    inputEl('CopyEnd').value = '';
    const pasteMode = selectEl('PasteMode');
    pasteMode.value = 'plain';
    for (const option of pasteMode.options) {
      if (option.value === 'formulas' || option.value === 'styles') {
        option.disabled = Boolean(data.encoding);
      }
    }
    const formulaOpt = selectEl('Kind').querySelector<HTMLOptionElement>('[value="formula"]');
    if (formulaOpt) formulaOpt.disabled = Boolean(data.encoding);
    const fillMode = selectEl('FillMode');
    fillMode.value = 'constant';
    inputEl('FillEnd').value = '';
    inputEl('FillStep').value = '1';
    const formulaFillOpt = fillMode.querySelector<HTMLOptionElement>('[value="formula-fill"]');
    if (formulaFillOpt) formulaFillOpt.disabled = Boolean(data.encoding);
    this.fillControls();
    el('Work').hidden = false;
    el('ListView').hidden = true;
    el('CellPanel').hidden = true;
    el('Name').textContent = data.document.name;
    const sheetSelect = selectEl('Sheet');
    sheetSelect.replaceChildren(...data.sheets.map((sheet) => new Option(sheet.name, sheet.name)));
    if (
      preferredSheet &&
      data.sheets.some((s) => s.name === preferredSheet && s.rows && s.cols)
    ) {
      sheetSelect.value = preferredSheet;
    } else {
      sheetSelect.value =
        (
          data.sheets.find((s) => s.rows && s.cols && !s.readOnly) ??
          data.sheets.find((s) => s.rows && s.cols) ??
          data.sheets[0]
        )?.name ?? '';
    }
    const shape = this.shape;
    try {
      EditModel.target(preferredRef ?? '', shape);
      inputEl('Address').value = preferredRef ?? '';
    } catch {
      this.selectFirst();
    }
    el('TextOptions').hidden = !data.encoding;
    const encodingSelect = selectEl('Encoding');
    const firstOpt = encodingSelect.options[0];
    if (firstOpt) {
      firstOpt.textContent = data.encoding ? localize('ui.editCopy.sourceCode', { encoding: data.encoding }) : localize('ui.editCopy.sourceEncoding');
    }
    encodingSelect.value = 'source';
    inputEl('Protect').checked = true;
    this.renderState();
    if (read) await this.fetchCell(signal);
  }

  get shape(): EditSheetMeta | undefined {
    const sheet = selectEl('Sheet').value;
    return this.data?.sheets.find((s) => s.name === sheet);
  }

  get loaded(): boolean {
    return Boolean(
      this.cell &&
        !this.invalidated &&
        this.cell.address.sheet === selectEl('Sheet').value &&
        this.cell.ref === inputEl('Address').value.trim().toUpperCase(),
    );
  }

  get editable(): boolean {
    return Boolean(
      this.loaded &&
        this.cell &&
        !this.cell.readOnly &&
        !(this.data?.encoding && this.cell.value.kind === 'missing'),
    );
  }

  get dirty(): boolean {
    return (
      this.loaded &&
      (textAreaEl('Value').value !== this.initialText || selectEl('Kind').value !== this.initialKind)
    );
  }

  selectFirst(): void {
    const shape = this.shape;
    inputEl('Address').value =
      shape?.firstCol && shape.firstRow ? `${this.colName(shape.firstCol)}${shape.firstRow}` : '';
  }

  controls(): void {
    this.grid.controls();
    const busy = Boolean(this.abort);
    const dirty = this.dirty;
    const blocked = busy || dirty || Boolean(this.pending);
    const doc = this.data?.document;
    buttonEl('Close').disabled = busy;
    for (const id of ['Sheet', 'Address', 'Read', 'History']) setControlDisabled(el(id), blocked);
    buttonEl('Refresh').disabled = busy;
    buttonEl('Undo').disabled = blocked || !doc?.undo || this.invalidated;
    buttonEl('Redo').disabled = blocked || !doc?.redo || this.invalidated;
    const kindSelect = selectEl('Kind');
    textAreaEl('Value').disabled =
      busy || Boolean(this.pending) || !this.editable || kindSelect.value === 'blank';
    kindSelect.disabled = busy || Boolean(this.pending) || !this.editable;
    const applyBtn = buttonEl('Apply');
    applyBtn.textContent = this.pending ? localize('ui.editCopy.reviewSubmit') : localize('ui.editCopy.applyModifications');
    applyBtn.hidden = Boolean(this.cell?.readOnly) && !this.pending;
    applyBtn.disabled = busy || (!this.pending && (!this.editable || !dirty));
    const discardBtn = buttonEl('DiscardInput');
    discardBtn.hidden = !dirty || Boolean(this.pending);
    discardBtn.disabled = busy;
    buttonEl('Save').disabled = blocked || !doc || this.invalidated;
    for (const button of el('Outputs').querySelectorAll('button')) button.disabled = blocked;
    for (const id of [
      'CopyEnd',
      'Copy',
      'PasteMode',
      'BatchText',
      'PreviewPaste',
      'FillEnd',
      'FillMode',
      'FillStep',
      'PreviewFill',
      'ApplyBatch',
    ]) {
      setControlDisabled(el(id), blocked || !this.editable);
    }
    const pasteModeValue = selectEl('PasteMode').value;
    inputEl('SkipBlank').disabled =
      blocked || !this.editable || (pasteModeValue !== 'plain' && pasteModeValue !== 'values');
    this.tools.controls(blocked);
    for (const id of [...RULE_INPUTS, 'PreviewRule', 'ReadChoices', 'Choice']) {
      setControlDisabled(el(id), blocked || !this.editable);
    }
    const choiceValue = selectEl('Choice').value;
    buttonEl('ApplyChoice').disabled =
      blocked ||
      !this.editable ||
      !choiceValue ||
      (this.cell?.value.kind === 'text' && this.cell.value.raw === choiceValue);
  }

  async fetchCell(signal: AbortSignal): Promise<void> {
    const shape = this.shape;
    if (!shape?.firstRow || !shape.firstCol) {
      this.cell = null;
      el('CellPanel').hidden = true;
      this.status(localize('ui.editCopy.thisWorksheetNoEditableCells'));
      return;
    }
    const addressInput = inputEl('Address');
    if (this.grid.active && !addressInput.value) {
      this.setCell(null);
      this.status(localize('ui.editCopy.noCellsOnCurrentWorksheet'));
      return;
    }
    if (!this.data) return;
    const doc = this.data.document;
    EditModel.target(addressInput.value, shape);
    const cell = await this.post(
      'read',
      {
        document: keyOf(doc),
        revision: doc.revision,
        sheet: shape.name,
        ref: addressInput.value.trim().toUpperCase(),
      },
      signal,
    ).then(decodeEditReadResponse);
    this.invalidated = false;
    this.setCell(cell);
    this.status(this.data.encoding && cell.value.kind === 'missing' ? localize('ui.editCopy.thisRecordLacksThisColumn') : '');
  }

  read(): void {
    if (this.dirty || this.pending) return;
    this.clearPreview();
    void this.run(localize('ui.editCopy.readingCells'), (signal) => this.fetchCell(signal));
  }

  setCell(cell: EditReadResponse | null): void {
    this.cell = cell;
    el('CellPanel').hidden = !cell;
    this.tools.bindCell(cell);
    el('OriginSheet').textContent = cell?.address.sheet ?? selectEl('Sheet').value;
    const originRef = el('OriginRef');
    originRef.textContent = cell?.ref ?? inputEl('Address').value;
    originRef.hidden = !originRef.textContent;
    if (!cell) return;
    el('ValueLabel').textContent = localize('ui.editCopy.content', { ref: cell.ref });
    inputEl('Address').value = cell.ref;
    this.escaped = cell.value.raw.includes('\r');
    this.initialText = EditModel.encode(
      cell.value.kind === 'formula' ? displayFormula(cell.value.raw) : cell.value.raw,
      this.escaped,
    );
    const kind = cell.value.kind;
    this.initialKind =
      kind === 'number' || kind === 'boolean' || kind === 'formula' ? kind : 'text';
    textAreaEl('Value').value = this.initialText;
    const kindSelect = selectEl('Kind');
    kindSelect.value = this.initialKind;
    kindSelect.hidden = Boolean(this.data?.encoding);
    el('TextHint').hidden = !this.escaped;
    let stateText =
      kind === 'missing' ? localize('ui.editCopy.originalCellEmpty') : kind === 'blank' ? localize('ui.editCopy.nullField') : kind === 'error' ? localize('ui.editCopy.errorValues') : '';
    if (kind === 'formula') {
      if (cell.formulaState === 'pending') stateText = localize('ui.common.beRecalculatedLater');
      else if (cell.formulaState === 'cycle') stateText = localize('ui.editCopy.cyclicReferences');
      else if (cell.formulaState === 'missing') stateText = localize('ui.editCopy.missingSavedResults');
      else {
        const value = cell.calculatedResult ?? cell.savedResult;
        if (value) {
          const preview = Array.from(value.raw).slice(0, 48).join('') || localize('ui.editCopy.nullText');
          stateText = `${cell.calculatedResult ? localize('ui.editCopy.calculationResult') : localize('ui.editCopy.originalSavedResult')} ${preview}`;
        }
      }
    }
    el('CellState').textContent = [
      cell.readOnly ? localize('ui.editCopy.readOnly', { readOnly: cell.readOnly }) : '',
      cell.generatedStatus ?? stateText,
      cell.base.style !== cell.value.style ? localize('ui.editCopy.formatModified') : '',
    ]
      .filter(Boolean)
      .join(' · ');
    const baseDetails = detailsEl('Base');
    baseDetails.hidden = equalsValue(cell.base, cell.value);
    baseDetails.open = false;
    el('BaseText').textContent =
      cell.base.raw || (cell.base.kind === 'missing' ? localize('ui.editCopy.originalCellEmpty') : localize('ui.common.blank'));
    this.setRule(cell);
    this.controls();
  }

  refresh(): void {
    const currentData = this.data;
    if (!currentData) return;
    if (this.pending) {
      void this.commit();
      return;
    }
    const draft = this.dirty
      ? {
          raw: textAreaEl('Value').value,
          kind: selectEl('Kind').value,
          escaped: this.escaped,
        }
      : null;
    const ruleInput = this.loaded && !currentData.encoding ? this.ruleInput() : null;
    const ruleDraft =
      ruleInput && JSON.stringify(ruleInput) !== this.initialRule ? ruleInput : null;
    this.clearPreview();
    void this.run(localize('ui.editCopy.readingLatestRevisions'), async (signal) => {
      const data = await this.post(
        'state',
        { document: keyOf(currentData.document) },
        signal,
      ).then(decodeEditStateResponse);
      this.data = data;
      this.renderState();
      if (this.grid.active) {
        const { sheet, ref } = this.context();
        if (await this.bindGridBase(data, signal, sheet, ref)) {
          await this.grid.enter(data, signal);
        } else {
          await this.grid.sync(undefined, signal);
        }
      }
      await this.fetchCell(signal);
      if (draft && this.cell) {
        this.escaped = draft.escaped;
        this.initialText = EditModel.encode(
          this.cell.value.kind === 'formula'
            ? displayFormula(this.cell.value.raw)
            : this.cell.value.raw,
          this.escaped,
        );
        textAreaEl('Value').value = draft.raw;
        selectEl('Kind').value = draft.kind;
        el('TextHint').hidden = !this.escaped;
        this.status(localize('ui.editCopy.revisionRefreshedCurrentInputRetained'));
      }
      if (ruleDraft && this.cell) {
        RULE_INPUTS.forEach((id, index) => {
          const val = ruleDraft[index];
          if (id === 'AllowBlank') {
            inputEl(id).checked = Boolean(val);
          } else {
            const node = el(id);
            if (
              node instanceof HTMLInputElement ||
              node instanceof HTMLSelectElement ||
              node instanceof HTMLTextAreaElement
            ) {
              node.value = typeof val === 'string' ? val : '';
            }
          }
        });
        this.ruleControls();
        this.status(localize('ui.editCopy.revisionRefreshedCurrentInputRetained'));
      }
    });
  }

  applyCell(): void {
    if (this.abort) return;
    if (this.pending) {
      void this.commit();
      return;
    }
    if (!this.editable || !this.dirty || !this.cell) return;
    try {
      const value = EditModel.value(
        parseEditValueKind(selectEl('Kind').value),
        EditModel.decode(textAreaEl('Value').value, this.escaped),
        this.limits,
      );
      if (equalsValue(value, this.cell.value)) {
        this.setCell(this.cell);
        return;
      }
      void this.commit('change', [{ ...this.cell.address, value }]);
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
    }
  }

  commit(
    action?: EditOperationAction,
    changes?: readonly EditChange[],
    options: EditCopyCommitOptions = {},
  ): Promise<boolean | undefined> {
    const {
      proof,
      rule,
      direct = false,
      structure,
      inferNumberFormat = false,
      entryAnchor,
      expandTable = false,
    } = options;
    if (
      this.abort ||
      !this.data ||
      (!this.pending && (!action || (action === 'change' && !changes?.length)))
    ) {
      return Promise.resolve(undefined);
    }
    const doc = this.data.document;
    const request: PendingCommitRequest =
      this.pending ??
      ({
        document: keyOf(doc),
        proof,
        direct,
        operation: {
          action: action ?? 'change',
          changes,
          rule,
          structure,
          entryAnchor,
          expandTable: expandTable || undefined,
          inferNumberFormat: inferNumberFormat || undefined,
          revision: doc.revision,
          request: crypto.randomUUID().replaceAll('-', ''),
        },
      } satisfies PendingCommitRequest);
    this.pending = request;
    return this.run(localize('ui.editCopy.savingEditsProgress'), async (signal) => {
      let result: EditCommitResponse;
      try {
        result = await this.post('commit', request, signal).then(decodeEditCommitResponse);
      } catch (error: unknown) {
        const status = httpStatus(error);
        if (status !== undefined && status >= 400 && status < 500 && status !== 408) {
          this.pending = null;
        }
        throw error;
      }
      this.pending = null;
      this.clearPreview();
      if (this.data) {
        this.data = { ...this.data, document: result.document };
      }
      this.renderState();
      if (
        direct &&
        result.committed === request.operation.revision &&
        result.document.revision === result.committed
      ) {
        this.grid.status('');
        return;
      }
      try {
        if (this.grid.active && result.book !== this.context().book) {
          const { sheet, ref } = this.context();
          const next = await this.post(
            'state',
            { document: keyOf(result.document) },
            signal,
          ).then(decodeEditStateResponse);
          const op = request.operation.structure;
          const followWorkbook = Boolean(
            op && (op.kind === 'copySheet' || op.kind === 'newSheet' || op.kind === 'deleteSheet'),
          );
          const active =
            !followWorkbook && sheet && next.sheets.some((s) => s.name === sheet)
              ? sheet
              : undefined;
          await this.bindGridBase(
            next,
            signal,
            active,
            this.tools.entryRefAfterResize(sheet, ref, request.operation, next),
          );
          await this.grid.enter(next, signal);
        } else if (this.grid.active) {
          const delta =
            !result.reloadGrid &&
            request.operation.action === 'change' &&
            result.committed === result.document.revision
              ? request.operation.changes
              : undefined;
          await this.grid.sync(delta, signal);
        }
        if (!direct) await this.fetchCell(signal);
      } catch (error: unknown) {
        this.invalidated = true;
        this.grid.stale = true;
        throw new Error(localize('ui.editCopy.savedRevisionsHaveSavedRefresh', { committed: result.committed, detail: errorMessage(error) }), {
          cause: error,
        });
      }
      this.status(localize('ui.editCopy.editsSaved', { committed: result.committed }));
    });
  }

  commitStructure(structure: EditStructure): Promise<boolean | undefined> {
    return this.commit('structure', undefined, { structure });
  }

  async bindGridBase(
    data: EditStateResponse,
    signal: AbortSignal,
    preferredSheet?: string | null,
    ref?: string,
  ): Promise<boolean> {
    if (data.book === this.context().book) return false;
    const base = await this.post(
      'base',
      {
        document: keyOf(data.document),
        revision: data.document.revision,
        book: this.context().book,
      },
      signal,
    ).then(decodeOpenedWorkbook);
    const index = preferredSheet ? base.sheets.findIndex((s) => s.name === preferredSheet) : -1;
    const adoptedBase: EditOpenWorkbookResponse =
      index >= 0 ? { ...base, activeTab: index } : base;
    this.grid.rebinding =
      this.data?.document.id === data.document.id &&
      this.data.document.binding === data.document.binding;
    try {
      await this.adopt(adoptedBase, { revisionRef: ref });
      await this.activate(data, this.context().sheet, ref, signal, false);
    } finally {
      this.grid.rebinding = false;
    }
    return true;
  }

  exitGrid(): Promise<boolean> | boolean {
    const dataSnapshot = this.data;
    if (!dataSnapshot || dataSnapshot.originalBook === this.context().book) {
      this.grid.leave();
      return true;
    }
    return this.run(localize('ui.editCopy.openingOriginalWorkbook'), async (signal) => {
      const base = await this.post(
        'base',
        {
          document: keyOf(dataSnapshot.document),
          book: this.context().book,
          mode: 'original',
        },
        signal,
      ).then(decodeOpenedWorkbook);
      await this.adopt(base);
    });
  }

  clearPreview(): void {
    this.previewed = null;
    el('BatchPreview').hidden = true;
  }

  editTarget(): {
    readonly document: EditKey;
    readonly revision: string;
    readonly sheet: string;
    readonly ref: string;
  } {
    if (!this.data || !this.cell) throw new Error(localize('ui.editCopy.cellsNotYetRead'));
    const doc = this.data.document;
    return {
      document: keyOf(doc),
      revision: doc.revision,
      sheet: this.cell.address.sheet,
      ref: this.cell.ref,
    };
  }

  copy(): void {
    if (!this.editable || this.dirty || this.abort || this.pending) return;
    void this.run(localize('ui.editCopy.readingCopyArea'), async (signal) => {
      const clip = await this.post(
        'copy',
        { ...this.editTarget(), end: inputEl('CopyEnd').value.trim().toUpperCase() },
        signal,
      ).then(decodeEditClipboard);
      const text = JSON.stringify(clip);
      try {
        await navigator.clipboard.writeText(text);
        this.status(localize('ui.editCopy.cellsCopied', { rows: clip.rows * clip.cols }));
      } catch {
        this.clearPreview();
        this.pasteRaw = text;
        textAreaEl('BatchText').value = text;
        selectEl('PasteMode').value = 'values';
        this.tools.select('Paste');
        this.status(localize('ui.editCopy.clipboardUnavailableDataPlacedPaste'));
      }
    });
  }

  setRule(cell: EditReadResponse): void {
    const rule = cell.rule;
    const choices = cell.choices ?? [];
    const start = rule ? `${this.colName(rule.start.col)}${rule.start.row}` : cell.ref;
    const end = rule ? `${this.colName(rule.endCol)}${rule.endRow}` : cell.ref;
    this.setChoices(choices);
    inputEl('RuleStart').value = start;
    inputEl('RuleEnd').value = end;
    selectEl('RuleType').value = rule?.reference ? 'list-reference' : (rule?.type ?? 'list');
    selectEl('RuleOperator').value = rule?.operator ?? 'between';
    inputEl('RuleFirst').value = rule && rule.type !== 'list' ? rule.first : '';
    inputEl('RuleSecond').value = rule?.second ?? '';
    textAreaEl('RuleList').value = choices.join('\n');
    inputEl('RuleReference').value = rule?.reference ? rule.first : '';
    inputEl('AllowBlank').checked = rule?.allowBlank ?? true;
    this.initialRule = JSON.stringify(this.ruleInput());
    this.ruleControls();
  }

  setChoices(choices: readonly string[]): void {
    const reference = Boolean(this.cell?.rule?.reference);
    el('RulePick').hidden = !reference && choices.length === 0;
    buttonEl('ReadChoices').hidden = !reference;
    for (const id of ['ChoiceLabel', 'Choice', 'ApplyChoice']) {
      el(id).hidden = choices.length === 0;
    }
    const choiceSelect = selectEl('Choice');
    choiceSelect.replaceChildren(
      new Option(localize('ui.editCopy.select'), ''),
      ...choices.map((value) => new Option(value, value)),
    );
    choiceSelect.value =
      this.cell?.value.kind === 'text' && choices.includes(this.cell.value.raw)
        ? this.cell.value.raw
        : '';
  }

  readChoices(): void {
    if (!this.editable || this.dirty || this.abort || this.pending || !this.cell?.rule?.reference) {
      return;
    }
    const target = this.editTarget();
    this.setChoices([]);
    void this.run(localize('ui.editCopy.readingListOptions'), async (signal) => {
      const result = await this.post('choices', target, signal).then(decodeEditChoicesResponse);
      if (result.revision !== target.revision) throw new Error(localize('ui.editCopy.editedRevisionChangedTryAgain'));
      this.setChoices(result.choices);
      this.status(localize('ui.editCopy.itemsRead', { count: result.choices.length }));
    });
  }

  ruleInput(): (string | boolean)[] {
    return RULE_INPUTS.map((id: RuleInputId) => {
      if (id === 'AllowBlank') return inputEl(id).checked;
      const node = el(id);
      if (
        node instanceof HTMLInputElement ||
        node instanceof HTMLSelectElement ||
        node instanceof HTMLTextAreaElement
      ) {
        return node.value;
      }
      return '';
    });
  }

  ruleControls(): void {
    const type = selectEl('RuleType').value;
    textAreaEl('RuleList').hidden = type !== 'list';
    el('RuleReferenceField').hidden = type !== 'list-reference';
    el('RuleNumeric').hidden = type === 'list' || type === 'list-reference';
    const op = selectEl('RuleOperator').value;
    const second = inputEl('RuleSecond');
    second.hidden = op !== 'between' && op !== 'notBetween';
    const placeholder = type === 'date' ? localize('ui.editCopy.yyyyMmDdOrSerial') : localize('ui.editCopy.conditionValues');
    inputEl('RuleFirst').placeholder = placeholder;
    second.placeholder = placeholder;
  }

  fillControls(): void {
    const mode = selectEl('FillMode').value;
    const unit =
      mode === 'number'
        ? localize('ui.editCopy.bucheng')
        : mode === 'date'
          ? localize('ui.editCopy.stepSizeDays')
          : mode === 'time'
            ? localize('ui.editCopy.stepSizeSeconds')
            : '';
    inputEl('FillStep').hidden = !unit;
    el('StepLabel').hidden = !unit;
    if (unit) el('StepLabel').textContent = unit;
  }

  previewRule(): void {
    if (!this.editable || this.dirty || this.abort || this.pending || this.data?.encoding) return;
    this.clearPreview();
    try {
      const a = EditModel.target(inputEl('RuleStart').value, this.shape);
      const b = EditModel.target(inputEl('RuleEnd').value, this.shape);
      const start: EditAddress = {
        ...a,
        row: Math.min(a.row, b.row),
        col: Math.min(a.col, b.col),
      };
      const choice = selectEl('RuleType').value;
      const reference = choice === 'list-reference';
      const type = reference ? 'list' : choice;
      const list = type === 'list';
      const secondEl = inputEl('RuleSecond');
      const rule: EditRule = {
        start,
        endRow: Math.max(a.row, b.row),
        endCol: Math.max(a.col, b.col),
        type,
        operator: list ? '' : selectEl('RuleOperator').value,
        first: reference
          ? inputEl('RuleReference').value.trim()
          : list
            ? textAreaEl('RuleList').value
            : inputEl('RuleFirst').value.trim(),
        reference,
        second: list || secondEl.hidden ? '' : secondEl.value.trim(),
        allowBlank: inputEl('AllowBlank').checked,
      };
      void this.run(localize('ui.editCopy.verifyingRuleRange'), async (signal) => {
        const plan = await this.post(
          'preview',
          { ...this.editTarget(), mode: 'rule', rule },
          signal,
        ).then(decodeEditBatchPreviewResponse);
        this.showPreview(plan, 'rule');
      });
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
    }
  }

  showPreview(plan: EditBatchPreviewResponse, mode: 'paste' | 'fill' | 'rule'): void {
    this.previewed = plan;
    const changes = plan.changes ?? [];
    const last = plan.last;
    const rule = plan.rule;
    const first = rule?.start ?? this.cell?.address;
    if (!first) return;
    const ref = (target: EditAddress): string => `${this.colName(target.col)}${target.row}`;
    const tail = plan.lastValue ?? '';
    const count = rule
      ? (last.row - first.row + 1) * (last.col - first.col + 1)
      : changes.length;
    const sameFormat = mode === 'fill' && changes.some((change) => change.value.style !== undefined);
    el('BatchSummary').textContent =
      localize('ui.editCopy.cell', { ref: ref(first), ref2: ref(last), count }) +
      (mode === 'fill' && tail ? localize('ui.editCopy.finalValue', { from: Array.from(tail).slice(0, 48).join('') }) : '') +
      (sameFormat ? localize('ui.editCopy.followStartingPointFormat') : '');
    buttonEl('ApplyBatch').textContent = rule ? localize('ui.editCopy.applyRules') : localize('ui.editCopy.applyThisBatch');
    const parent = rule ? el('Rules') : el(mode === 'fill' ? 'PaneFill' : 'PanePaste');
    const batchPreview = el('BatchPreview');
    parent.append(batchPreview);
    batchPreview.hidden = false;
    this.status('');
  }

  preview(mode: 'paste' | 'fill'): void {
    if (!this.editable || this.dirty || this.abort || this.pending || !this.cell || !this.shape) {
      return;
    }
    this.clearPreview();
    const first = this.cell.address;
    const shape = this.shape;
    try {
      const raw = this.pasteRaw ?? textAreaEl('BatchText').value;
      const pasteMode = selectEl('PasteMode').value;
      const fillMode = selectEl('FillMode').value;
      if (
        mode === 'fill' &&
        (fillMode === 'date' || fillMode === 'time' || fillMode === 'formula-fill')
      ) {
        const last = EditModel.target(inputEl('FillEnd').value, shape);
        void this.run(localize('ui.editCopy.verifyingFillingArea'), async (signal) => {
          const plan = await this.post(
            'preview',
            {
              ...this.editTarget(),
              mode: fillMode,
              end: `${this.colName(last.col)}${last.row}`,
              step: inputEl('FillStep').value.trim(),
            },
            signal,
          ).then(decodeEditBatchPreviewResponse);
          this.showPreview(plan, mode);
        });
        return;
      }
      if (mode === 'paste' && pasteMode !== 'plain') {
        if (new TextEncoder().encode(raw).length + 1024 > this.limits.requestBytes) {
          throw new Error(localize('ui.editCopy.pasteContentExceedsCapacityReduce'));
        }
        let clipboard: unknown;
        try {
          clipboard = JSON.parse(raw);
        } catch {
          throw new Error(localize('ui.editCopy.pasteTypeData'));
        }
        void this.run(localize('ui.editCopy.verifyingPasteArea'), async (signal) => {
          const plan = await this.post(
            'preview',
            {
              ...this.editTarget(),
              mode: pasteMode,
              clipboard,
              skipBlank: inputEl('SkipBlank').checked,
            },
            signal,
          ).then(decodeEditBatchPreviewResponse);
          this.showPreview(plan, mode);
        });
        return;
      }
      const plan =
        mode === 'paste'
          ? EditModel.paste(
              raw,
              first,
              shape,
              inputEl('SkipBlank').checked,
              this.limits,
              parseClipboardTSV,
            )
          : EditModel.fill(
              first,
              EditModel.target(inputEl('FillEnd').value, shape),
              this.cell.value,
              inputEl('FillStep').value,
              fillMode === 'number',
              Boolean(this.data?.encoding),
              this.limits,
            );
      void this.run(localize('ui.editCopy.verifyingTargetArea'), async (signal) => {
        const checked = await this.post(
          'preview',
          {
            ...this.editTarget(),
            mode: 'changes',
            end: `${this.colName(plan.last.col)}${plan.last.row}`,
            operation: { changes: plan.changes },
          },
          signal,
        ).then(decodeEditBatchPreviewResponse);
        this.showPreview(checked, mode);
      });
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
    }
  }

  private item(title: string, detail: string): HTMLDivElement {
    const line = document.createElement('div');
    const text = document.createElement('div');
    const strong = document.createElement('strong');
    const small = document.createElement('small');
    line.className = 'edit-copy-item';
    strong.textContent = title;
    small.textContent = detail;
    text.append(strong, small);
    line.append(text);
    return line;
  }

  private button(title: string, action: () => void): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'mini-button';
    button.textContent = title;
    button.addEventListener('click', action);
    return button;
  }

  renderState(): void {
    if (!this.data) return;
    const doc = this.data.document;
    el('Revision').textContent =
      localize('ui.editCopy.revisionCellModifications', { revision: doc.revision, changed: doc.changed }) +
      (doc.structuresChanged ? ` ${localize('ui.editCopy.itemStructureOperation', { structuresChanged: doc.structuresChanged })}` : '') +
      (doc.rulesChanged ? ` ${localize('ui.editCopy.rule', { rulesChanged: doc.rulesChanged })}` : '');
    this.grid.controls();
    const outputs = el('Outputs');
    outputs.replaceChildren();
    for (const artifact of this.data.artifacts) {
      const detail = [
        localize('ui.editCopy.revise', { revision: artifact.revision }),
        this.fmtBytes(artifact.bytes),
        artifact.encoding,
        artifact.protected ? localize('ui.editCopy.cellsProtectedPrefix', { protected: artifact.protected }) : null,
        artifact.nativeRecalc
          ? localize('ui.editCopy.formulasWillBeRecalculatedExcel')
          : artifact.formulasPending
            ? localize('ui.editCopy.formulasNeedBeRecalculated', { formulasPending: artifact.formulasPending })
            : null,
        artifact.pivotCachesPending ? localize('ui.editCopy.pivotTablesWaitingBeRefreshed', { pivotCachesPending: artifact.pivotCachesPending }) : null,
        artifact.pivotCachesAfterCalc ? localize('ui.editCopy.recalculateExcelThenRefreshPage') : null,
      ]
        .filter(Boolean)
        .join(' · ');
      const item = this.item(artifact.name, detail);
      const link = document.createElement('a');
      link.className = 'mini-button';
      link.href = artifact.downloadUrl;
      link.textContent = localize('ui.editCopy.download');
      link.download = artifact.name;
      item.append(
        link,
        this.button(localize('ui.editCopy.openDuplicate'), () => this.openFile(artifact)),
      );
      outputs.append(item);
    }
  }

  save(): Promise<boolean> | undefined {
    if (this.dirty || this.pending || !this.data) return undefined;
    const dataSnapshot = this.data;
    const doc = dataSnapshot.document;
    const options = dataSnapshot.encoding
      ? { encoding: selectEl('Encoding').value, protect: inputEl('Protect').checked }
      : {};
    return this.run(localize('ui.editCopy.generatingVerifyingCopies'), async (signal) => {
      const artifact = await this.post(
        'save',
        { document: keyOf(doc), revision: doc.revision, options },
        signal,
      ).then(decodeEditArtifact);
      if (this.data) {
        this.data = {
          ...this.data,
          artifacts: [artifact, ...this.data.artifacts.filter((a) => a.key !== artifact.key)],
        };
      }
      this.renderState();
      this.status(
        localize('ui.editCopy.revisionsHaveGenerated', { revision: artifact.revision, detail: artifact.pivotCachesAfterCalc ? localize('ui.editCopy.recalculateExcelRefreshPivotTable') : '' }),
      );
    });
  }

  openFile(artifact: EditArtifact): void {
    if (this.dirty || this.pending || !this.data || !this.canOpen(localize('ui.editCopy.openDuplicate'))) return;
    const { book } = this.context();
    const doc = this.data.document;
    void this.run(
      localize('ui.editCopy.openingGeneratedCopies'),
      async (signal) => {
        const data = await this.post(
          'open',
          { document: keyOf(doc), artifact: artifact.key, book },
          signal,
        ).then(decodeOpenedWorkbook);
        if (this.grid.active) this.grid.leave();
        dialogEl().close();
        await this.adopt(data);
      },
      false,
    );
  }
}
