/**
 * Copy-Special and Paste-Special dialog controllers.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { ToastFn } from '../contracts/common.ts';
import type { ClipboardLimits } from '../contracts/formats.ts';
import type { ClipboardPayload } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
  requireTextArea,
} from '../core/dom.ts';
import type { PasteCellChange } from './clipboard.ts';
import {
  buildPastePlan,
  buildSpecialSelection,
  type CopySpecialFormat,
  type CopySpecialMode,
  type CopySpecialOptions,
  type CopySpecialResult,
  type PastePlanResult,
} from './clipboard-special.ts';
import type { MergeCopyAnchor } from './merges.ts';

function setStatus(id: string, text: string, isError = false): void {
  const node = requireEl(id);
  node.textContent = text;
  node.setAttribute('role', isError ? 'alert' : 'status');
  node.classList.toggle('has-error', isError);
}

export interface CopySpecialSnapshot {
  readonly label: string;
  readonly headerRow: number;
  readonly maxRow: number;
  readonly styled: boolean;
  readonly sheet: string;
  readonly mergeAt?: (row: number, col: number) => MergeCopyAnchor | null;
  current(): boolean;
  read(
    options: { readonly headerRow?: number; readonly richText?: boolean; readonly conditional?: boolean },
    signal?: AbortSignal,
  ): Promise<ClipboardPayload>;
}

export interface CopySpecialControllerOptions {
  readonly capture: () => CopySpecialSnapshot;
  readonly copy: (content: CopySpecialResult) => Promise<boolean | undefined>;
  readonly toast: ToastFn;
  readonly limits: ClipboardLimits;
}

export class CopySpecialController {
  private readonly capture: () => CopySpecialSnapshot;
  private readonly copy: (content: CopySpecialResult) => Promise<boolean | undefined>;
  private readonly toast: ToastFn;
  private readonly limits: ClipboardLimits;
  private serial = 0;
  private readonly dialog: HTMLDialogElement;
  private abort: AbortController | null = null;
  private payload: ClipboardPayload | null = null;
  private result: CopySpecialResult | null = null;
  private snapshot: CopySpecialSnapshot | null = null;

  constructor(options: CopySpecialControllerOptions) {
    this.capture = options.capture;
    this.copy = options.copy;
    this.toast = options.toast;
    this.limits = options.limits;
    this.dialog = requireDialog('copySpecialDialog');

    requireButton('copySpecialClose').onclick = (): void => this.dialog.close();
    requireButton('copySpecialReload').onclick = (): void => {
      void this.load();
    };
    requireEl('copySpecialForm').onsubmit = (event: SubmitEvent): void => {
      event.preventDefault();
      void this.write();
    };
    for (const id of ['copySpecialFormat', 'copySpecialMode'] as const) {
      requireSelect(id).onchange = (): void => this.render();
    }
    requireInput('copySpecialHeader').onchange = (): void => {
      void this.load();
    };
    requireInput('copySpecialHeaderRow').onchange = (): void => {
      void this.load();
    };
    this.dialog.addEventListener('close', () => {
      if (this.dialog.open) return;
      ++this.serial;
      this.abort?.abort();
      this.payload = null;
      this.result = null;
      this.snapshot = null;
    });
  }

  options(): CopySpecialOptions {
    const format = requireSelect('copySpecialFormat').value as CopySpecialFormat;
    const mode = requireSelect('copySpecialMode').value as CopySpecialMode;
    return {
      format,
      mode,
      includeHeader: requireInput('copySpecialHeader').checked,
      styled: this.snapshot?.styled,
      sheet: this.snapshot?.sheet,
      mergeAt: this.snapshot?.mergeAt,
    };
  }

  open(): void {
    try {
      this.snapshot = this.capture();
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'warning');
      return;
    }
    requireEl('copySpecialSource').textContent = this.snapshot.label;
    const headerRowInput = requireInput('copySpecialHeaderRow');
    headerRowInput.value = String(this.snapshot.headerRow);
    headerRowInput.max = String(this.snapshot.maxRow);
    requireEl('copySpecialLimits').textContent =
      localize('ui.clipboardPanels.upRowsColumnsCellsIncluding', { rows: this.limits.rows, columns: this.limits.columns, cells: this.limits.cells.toLocaleString(interfaceLocale()) });
    this.dialog.showModal();
    void this.load();
  }

  async load(): Promise<void> {
    if (!this.snapshot) return;
    const serial = ++this.serial;
    this.abort?.abort();
    const controller = new AbortController();
    this.abort = controller;
    this.payload = null;
    this.result = null;
    requireButton('copySpecialWrite').disabled = true;
    requireTextArea('copySpecialPreview').value = '';
    const included = requireInput('copySpecialHeader').checked;
    const headerRowInput = requireInput('copySpecialHeaderRow');
    const headerRow = included ? Number(headerRowInput.value) : 0;
    headerRowInput.disabled = !included;
    if (included && (!Number.isSafeInteger(headerRow) || headerRow < 1 || headerRow > this.snapshot.maxRow)) {
      setStatus('copySpecialStatus', localize('ui.clipboardPanels.provideValidSourceHeaderRow'), true);
      return;
    }
    setStatus('copySpecialStatus', localize('ui.clipboardPanels.readingSelection'));
    try {
      const payload = await this.snapshot.read(
        { headerRow, richText: false, conditional: this.snapshot.styled },
        controller.signal,
      );
      if (serial !== this.serial || !this.dialog.open) return;
      if (!this.snapshot.current()) throw new Error(localize('ui.common.sourceOrElectoralDistrictChanged'));
      this.payload = payload;
      this.render();
    } catch (error: unknown) {
      const isAbort = error instanceof Error && error.name === 'AbortError';
      if (serial === this.serial && (!isAbort || !this.snapshot.current())) {
        setStatus('copySpecialStatus', errorMessage(error), true);
      }
    } finally {
      if (this.abort === controller) this.abort = null;
    }
  }

  render(): void {
    requireSelect('copySpecialMode').disabled = requireSelect('copySpecialFormat').value === 'json';
    if (!this.payload || !this.snapshot) return;
    this.result = null;
    requireButton('copySpecialWrite').disabled = true;
    try {
      if (!this.snapshot.current()) throw new Error(localize('ui.common.sourceOrElectoralDistrictChanged'));
      const result = buildSpecialSelection(this.payload, this.options(), this.limits);
      this.result = result;
      requireTextArea('copySpecialPreview').value = result.text.slice(0, 10000);
      const notes = [
        localize('ui.clipboardPanels.rowDataColumn', { rows: result.rows, columns: result.columns }),
        result.text.length > 10000 ? localize('ui.clipboardPanels.previewOnlyShowsFirstCharacters') : '',
        ...result.notices,
      ].filter(Boolean);
      setStatus('copySpecialStatus', notes.join('；'));
      requireButton('copySpecialWrite').disabled = false;
    } catch (error: unknown) {
      requireTextArea('copySpecialPreview').value = '';
      setStatus('copySpecialStatus', errorMessage(error), true);
    }
  }

  private isWriteReady(): boolean {
    return Boolean(this.result && this.snapshot?.current());
  }

  async write(): Promise<void> {
    if (!this.result || !this.snapshot?.current()) {
      setStatus('copySpecialStatus', localize('ui.common.sourceOrElectoralDistrictChanged'), true);
      return;
    }
    const serial = this.serial;
    requireButton('copySpecialWrite').disabled = true;
    try {
      const copied = await this.copy(this.result);
      if (serial !== this.serial || !this.dialog.open) return;
      if (copied !== false) this.dialog.close();
      else setStatus('copySpecialStatus', localize('ui.clipboardPanels.copyNotCompletedTryAgain'), true);
    } catch (error: unknown) {
      if (serial === this.serial) setStatus('copySpecialStatus', errorMessage(error), true);
    } finally {
      if (serial === this.serial && this.dialog.open) {
        requireButton('copySpecialWrite').disabled = !this.isWriteReady();
      }
    }
  }
}

export interface PasteSpecialTargetContext {
  readonly id: string;
  readonly label: string;
  readonly row: number;
  readonly col: number;
  readonly columns: number;
  current(): boolean;
}

export interface PasteSpecialControllerOptions {
  readonly capture: (previous?: PasteSpecialTargetContext) => PasteSpecialTargetContext;
  readonly commit: (
    context: PasteSpecialTargetContext,
    changes: readonly PasteCellChange[],
  ) => Promise<void>;
  readonly toast: ToastFn;
  readonly refresh: (previous: PasteSpecialTargetContext) => Promise<void>;
}

export class PasteSpecialController {
  private readonly capture: (previous?: PasteSpecialTargetContext) => PasteSpecialTargetContext;
  private readonly commit: (
    context: PasteSpecialTargetContext,
    changes: readonly PasteCellChange[],
  ) => Promise<void>;
  private readonly toast: ToastFn;
  private readonly refresh: (previous: PasteSpecialTargetContext) => Promise<void>;
  private serial = 0;
  private textIntent = 0;
  public readonly dialog: HTMLDialogElement;
  private busy = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private context: PasteSpecialTargetContext | null = null;
  private plan: PastePlanResult | null = null;

  constructor(options: PasteSpecialControllerOptions) {
    this.capture = options.capture;
    this.commit = options.commit;
    this.toast = options.toast;
    this.refresh = options.refresh;
    this.dialog = requireDialog('pasteSpecialDialog');

    requireButton('pasteSpecialClose').onclick = (): void => this.dialog.close();
    requireButton('pasteSpecialRead').onclick = (): void => {
      void this.readClipboard();
    };
    requireButton('pasteSpecialReload').onclick = (): void => {
      void this.reload();
    };
    requireEl('pasteSpecialForm').onsubmit = (event: SubmitEvent): void => {
      event.preventDefault();
      void this.apply();
    };
    requireTextArea('pasteSpecialText').oninput = (): void => {
      this.textIntent++;
      this.plan = null;
      requireButton('pasteSpecialApply').disabled = true;
      if (this.timer !== undefined) clearTimeout(this.timer);
      this.timer = setTimeout(() => this.preview(), 120);
    };
    for (const id of ['pasteSpecialFormat', 'pasteSpecialMode'] as const) {
      requireSelect(id).onchange = (): void => this.preview();
    }
    for (const id of [
      'pasteSpecialHeader',
      'pasteSpecialSkipFirst',
      'pasteSpecialTranspose',
      'pasteSpecialSkipBlank',
    ] as const) {
      requireInput(id).onchange = (): void => this.preview();
    }
    this.dialog.addEventListener('cancel', (event: Event) => {
      if (this.busy) event.preventDefault();
    });
    this.dialog.addEventListener('close', () => {
      if (this.dialog.open) return;
      ++this.serial;
      if (this.timer !== undefined) clearTimeout(this.timer);
      this.context = null;
      this.plan = null;
    });
  }

  open(context?: PasteSpecialTargetContext): void {
    if (this.busy) return;
    try {
      this.context = context ?? this.capture();
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'warning');
      return;
    }
    ++this.serial;
    requireEl('pasteSpecialTarget').textContent = this.context.label;
    this.dialog.showModal();
    this.preview();
    requireTextArea('pasteSpecialText').focus();
  }

  options() {
    const format = requireSelect('pasteSpecialFormat').value as 'tsv' | 'json';
    const mode = requireSelect('pasteSpecialMode').value as CopySpecialMode;
    return {
      format,
      mode,
      withHeader: requireInput('pasteSpecialHeader').checked,
      skipFirstRow: format === 'tsv' && requireInput('pasteSpecialSkipFirst').checked,
      transpose: requireInput('pasteSpecialTranspose').checked,
      skipBlank: requireInput('pasteSpecialSkipBlank').checked,
      startRow: this.context?.row,
      startCol: this.context?.col,
      columns: this.context?.columns,
    };
  }

  preview(): void {
    if (!this.context || !this.dialog.open || this.busy) return;
    this.plan = null;
    requireButton('pasteSpecialApply').disabled = true;
    requireEl('pasteSpecialPreview').replaceChildren();
    const isJson = requireSelect('pasteSpecialFormat').value === 'json';
    requireSelect('pasteSpecialMode').disabled = !isJson;
    requireInput('pasteSpecialHeader').disabled = !isJson;
    requireInput('pasteSpecialSkipFirst').disabled = isJson;
    try {
      if (!this.context.current()) {
        throw new Error(localize('ui.clipboardPanels.targetTableLocationOrVersion'));
      }
      const text = requireTextArea('pasteSpecialText').value;
      if (!text) {
        setStatus('pasteSpecialStatus', localize('ui.clipboardPanels.pasteTextIntoUpperText'));
        return;
      }
      const plan = buildPastePlan(text, this.options());
      this.plan = plan;
      const body = document.createElement('tbody');
      for (const row of plan.preview) {
        const tr = document.createElement('tr');
        for (const value of row) {
          const td = document.createElement('td');
          const chars = Array.from(value);
          td.textContent = chars.slice(0, 160).join('') + (chars.length > 160 ? '…' : '');
          td.title = value;
          tr.append(td);
        }
        body.append(tr);
      }
      requireEl('pasteSpecialPreview').append(body);
      setStatus(
        'pasteSpecialStatus',
        localize('ui.clipboardPanels.rowColumnWillWriteCell', { rows: plan.rows, columns: plan.columns, count: plan.changes.length.toLocaleString(interfaceLocale()) }) +
          (plan.skipped ? ` ${localize('ui.clipboardPanels.skippingBlankItems', { skipped: plan.skipped })}` : '') +
          localize('ui.clipboardPanels.previewUpRowsColumns'),
      );
      requireButton('pasteSpecialApply').disabled = !plan.changes.length;
    } catch (error: unknown) {
      setStatus('pasteSpecialStatus', errorMessage(error), true);
    }
  }

  async readClipboard(): Promise<void> {
    const serial = this.serial;
    const intent = ++this.textIntent;
    try {
      if (typeof navigator.clipboard.readText !== 'function') {
        throw new Error(localize('ui.clipboardPanels.browserDoesNotSupportClipboard'));
      }
      const text = await navigator.clipboard.readText();
      if (serial !== this.serial || intent !== this.textIntent || !this.dialog.open) return;
      requireTextArea('pasteSpecialText').value = text;
      this.preview();
    } catch (error: unknown) {
      if (serial === this.serial && intent === this.textIntent) {
        setStatus(
          'pasteSpecialStatus',
          localize('ui.clipboardPanels.readingFailedCanAlsoPaste', { detail: errorMessage(error) }),
          true,
        );
      }
    }
  }

  async reload(): Promise<void> {
    if (this.busy || !this.context) return;
    const serial = this.serial;
    const previous = this.context;
    try {
      await this.refresh(previous);
      if (serial !== this.serial || !this.dialog.open) return;
      this.context = this.capture(previous);
      requireEl('pasteSpecialTarget').textContent = this.context.label;
      this.preview();
    } catch (error: unknown) {
      if (serial === this.serial) setStatus('pasteSpecialStatus', errorMessage(error), true);
    }
  }

  async apply(): Promise<void> {
    if (this.busy) return;
    this.preview();
    if (!this.plan?.changes.length || !this.context?.current()) return;
    const serial = this.serial;
    const context = this.context;
    const changes = this.plan.changes;
    this.busy = true;
    const fieldsEl = requireEl('pasteSpecialFields');
    if (fieldsEl instanceof HTMLFieldSetElement) fieldsEl.disabled = true;
    requireButton('pasteSpecialClose').disabled = true;
    requireButton('pasteSpecialApply').disabled = true;
    setStatus('pasteSpecialStatus', localize('ui.clipboardPanels.savingEntireBatches'));
    try {
      await this.commit(context, changes);
      if (serial === this.serial) {
        this.dialog.close();
        this.toast(localize('ui.clipboardPanels.pastedCellsCanBeUndone', { count: changes.length.toLocaleString(interfaceLocale()) }), 'success');
      }
    } catch (error: unknown) {
      if (serial === this.serial) setStatus('pasteSpecialStatus', errorMessage(error), true);
    } finally {
      this.busy = false;
      if (fieldsEl instanceof HTMLFieldSetElement) fieldsEl.disabled = false;
      requireButton('pasteSpecialClose').disabled = false;
      if (this.dialog.open) requireButton('pasteSpecialApply').disabled = true;
    }
  }
}
