/**
 * Editable analysis scratch sheets controller: paged table editing, draft
 * staging, serialized revision-checked saves, undo/redo, and paste-special.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  AnalysisCellChange,
  AnalysisSheetPage,
  AnalysisWorkspaceResponse,
} from '../contracts/analysis.ts';
import type { ApiClient, HttpError, PostClient, ToastFn } from '../contracts/common.ts';
import { columnName } from '../core/cell-address.ts';
import { errorMessage, icon } from '../core/display-format.ts';
import { requireButton, requireEl } from '../core/dom.ts';
import {
  clipboardBatches,
  pasteChangesFromTSV,
  validateClipboardChanges,
} from '../grid/clipboard.ts';
import type {
  PasteSpecialController,
  PasteSpecialTargetContext,
} from '../grid/clipboard-panels.ts';
import type { AnalysisDraftEntry, AnalysisDrafts } from './drafts.ts';
import type { AnalysisResultCache } from './results.ts';
import {
  decodeAnalysisSheetPage,
  decodeAnalysisSheetPageWire,
  decodeSheetBatchResult,
  decodeSheetCreateResponse,
  decodeSheetRevisionResponse,
} from '../core/http-decoders.generated.ts';

/** @httpResponse AnalysisSheetPageWire */
export type AnalysisSheetPageWire = Omit<AnalysisSheetPage, 'rows'> & {
          readonly rows: readonly (readonly string[])[] | null;
        };

export interface MutableAnalysisSheetPage {
  readonly id: string;
  readonly name: string;
  columns: string[];
  rows: string[][];
  readonly offset: number;
  readonly total: number;
  readonly revision: number;
}

export interface SheetUndoEntry {
  readonly before: readonly AnalysisCellChange[];
  readonly after: readonly AnalysisCellChange[];
}

export interface SheetUndoHistory {
  undo: SheetUndoEntry[];
  redo: SheetUndoEntry[];
}

export interface ActiveSheetCell {
  readonly id: string;
  readonly page: number;
  readonly row: number;
  readonly col: number;
}

export interface AnalysisPasteTargetContext extends PasteSpecialTargetContext {
  readonly book: string;
  readonly pathBook: string;
  readonly page: number;
  readonly revision: number | undefined;
}

/** @httpResponse SheetBatchResult */
export interface SheetBatchResult {
  readonly revision: number;
  readonly previous: readonly AnalysisCellChange[];
}

/** @httpResponse SheetCreateResponse */
export interface SheetCreateResponse {
  readonly id: string;
}

/** @httpResponse SheetRevisionResponse */
export interface SheetRevisionResponse {
  readonly revision: number;
}

export interface CommitBatchOptions {
  readonly track?: boolean;
  readonly refresh?: boolean;
  readonly guard?: () => boolean;
}

export interface AnalysisSheetsState {
  opened: boolean;
  tab: string;
  fingerprint: string;
  book: string;
  currentBook?: string;
  workspace: Pick<AnalysisWorkspaceResponse, 'sheets'> | null;
  loadedSheet: string | null;
  sheetPage: number;
  sheetLoadSeq: number;
  sheetAbortController: AbortController | null;
  sheetRevisions: Map<string, number>;
  undoHistory: Map<string, SheetUndoHistory>;
  gridData: MutableAnalysisSheetPage | null;
  sheetCell: ActiveSheetCell | null;
  reportCache: AnalysisResultCache;
}

export interface AnalysisSheetsDeps {
  readonly state: AnalysisSheetsState;
  readonly drafts: AnalysisDrafts;
  readonly api: ApiClient;
  readonly post: PostClient;
  readonly toast: ToastFn;
  readonly askText: (title: string, initial?: string, hint?: string) => Promise<string | null>;
  readonly setText: (id: string, text: string) => void;
  readonly fmtNum: (n: unknown) => string;
  readonly setTab: (tab: 'pivot' | 'dashboard' | 'sheets' | 'templates') => Promise<void> | void;
  readonly refreshWorkspace: () => Promise<boolean | undefined>;
  readonly open: () => Promise<void>;
}

function makeHttpError(message: string, status: number): HttpError {
  const err: HttpError = new Error(message);
  err.status = status;
  return err;
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function isAnalysisPasteTarget(
  value: PasteSpecialTargetContext | undefined,
): value is AnalysisPasteTargetContext {
  return (
    value !== undefined &&
    'book' in value &&
    typeof value.book === 'string' &&
    'pathBook' in value &&
    typeof value.pathBook === 'string' &&
    'page' in value &&
    typeof value.page === 'number'
  );
}

function closestCellTextArea(target: EventTarget | null): HTMLTextAreaElement | null {
  if (!(target instanceof Element)) return null;
  const node = target.closest('textarea[data-row][data-col]');
  return node instanceof HTMLTextAreaElement ? node : null;
}

export class AnalysisSheetsController {
  private readonly d: AnalysisSheetsDeps;
  private saveChain: Promise<void> = Promise.resolve();
  public pendingWrites = 0;
  private pasteSpecial: PasteSpecialController | null = null;

  constructor(deps: AnalysisSheetsDeps) {
    this.d = deps;
  }

  setPasteSpecial(pasteSpecial: PasteSpecialController | null): void {
    this.pasteSpecial = pasteSpecial;
  }

  flush(): Promise<void> {
    return this.saveChain;
  }

  hasUnsaved(): boolean {
    const { state, drafts } = this.d;
    return (
      this.pendingWrites > 0 ||
      drafts.entries(state.currentBook ?? state.fingerprint).length > 0
    );
  }

  updateSaveStatus(): void {
    const { state, drafts, setText } = this.d;
    const count = drafts.entries(state.fingerprint).length;
    setText(
      'analysisAutoSave',
      this.pendingWrites ? localize('ui.sheets.savingProgress') : count ? localize('ui.sheets.cellsNotSaved', { count }) : localize('ui.common.saved'),
    );
    const retry = requireButton('analysisRetrySave');
    retry.hidden = !count || this.pendingWrites > 0;
    retry.disabled = this.pendingWrites > 0;
  }

  paintDraft(
    input: HTMLTextAreaElement,
    row: number,
    col: number,
    saved: string,
  ): AnalysisDraftEntry | undefined {
    const { state, drafts } = this.d;
    const loadedSheet = state.loadedSheet;
    const draft = loadedSheet ? drafts.get(state.fingerprint, loadedSheet, row, col) : undefined;
    input.classList.toggle('is-unsaved', Boolean(draft));
    input.setAttribute('aria-invalid', draft ? 'true' : 'false');
    input.title = draft
      ? localize('ui.sheets.draftNotSavedCurrentlySaved', { saved })
      : (input.dataset.savedTitle ?? '');
    return draft;
  }

  queueWrite<T>(run: () => Promise<T>): Promise<T> {
    const { drafts, toast } = this.d;
    ++this.pendingWrites;
    this.updateSaveStatus();
    const next = this.saveChain
      .catch(() => undefined)
      .then(async (): Promise<T> => {
        try {
          return await run();
        } catch (err: unknown) {
          toast(localize('ui.sheets.saveFailed', { detail: errorMessage(err), detail2: drafts.size ? localize('ui.sheets.draftRemainsRetained') : '' }), 'error');
          throw err;
        } finally {
          --this.pendingWrites;
          this.updateSaveStatus();
        }
      });
    this.saveChain = next.then(
      () => undefined,
      () => undefined,
    );
    return next;
  }

  async createSheet(): Promise<void> {
    const { state, post, askText, refreshWorkspace, setTab, toast } = this.d;
    const name = await askText(
      localize('ui.sheets.createNewEditableWorksheet'),
      localize('ui.sheets.dataAnalysisTable', { detail: (state.workspace?.sheets.length ?? 0) + 1 }),
    );
    if (!name) return;
    try {
      const data = await post('/api/analysis/sheet/create', { name }).then(decodeSheetCreateResponse);
      state.loadedSheet = data.id;
      state.sheetPage = 0;
      await refreshWorkspace();
      void setTab('sheets');
      toast(localize('ui.sheets.newWorksheetCreated'), 'success');
    } catch (err: unknown) {
      toast(errorMessage(err), 'error');
    }
  }

  async renderSheets(): Promise<void> {
    const { state, setText, setTab } = this.d;
    if (!state.opened || state.tab !== 'sheets') return;
    const host = requireEl('analysisSheetList');
    host.replaceChildren();
    const sheets = state.workspace?.sheets ?? [];
    for (const sheet of sheets) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.textContent = sheet.name;
      btn.title = sheet.name;
      btn.className = sheet.id === state.loadedSheet ? 'is-active' : '';
      btn.onclick = (): void => {
        state.loadedSheet = sheet.id;
        state.sheetPage = 0;
        void this.renderSheets();
      };
      host.append(btn);
    }
    const firstSheet = sheets[0];
    if (!firstSheet) {
      state.sheetLoadSeq++;
      state.sheetAbortController?.abort();
      state.sheetAbortController = null;
      state.loadedSheet = null;
      state.gridData = null;
      setText('analysisSheetTitle', localize('ui.sheets.editWorksheet'));
      for (const id of [
        'analysisPasteSpecial',
        'analysisUndo',
        'analysisRedo',
        'analysisAddColumn',
        'analysisAddRows',
        'analysisDeleteSheet',
      ] as const) {
        requireButton(id).disabled = true;
      }
      requireEl('analysisSheetPagination').replaceChildren();
      const empty = document.createElement('div');
      empty.className = 'analysis-sheet-empty';
      const glyph = document.createElement('div');
      glyph.className = 'analysis-sheet-empty-icon';
      glyph.setAttribute('aria-hidden', 'true');
      glyph.innerHTML = icon('header');
      const title = document.createElement('h3');
      title.textContent = localize('ui.sheets.noWorksheetAvailable');
      const actions = document.createElement('div');
      actions.className = 'analysis-sheet-empty-actions';
      const create = document.createElement('button');
      create.type = 'button';
      create.className = 'analysis-primary-button';
      create.innerHTML = icon('plus');
      create.append(localize('ui.sheets.createNewWorksheet'));
      create.addEventListener('click', () => {
        void this.createSheet();
      });
      const pivot = document.createElement('button');
      pivot.type = 'button';
      pivot.className = 'analysis-outline-button';
      pivot.textContent = localize('ui.sheets.goPivotAnalysis');
      pivot.addEventListener('click', () => {
        void setTab('pivot');
      });
      actions.append(create, pivot);
      empty.append(glyph, title, actions);
      requireEl('analysisEditableGrid').replaceChildren(empty);
      const hint = document.createElement('div');
      hint.className = 'analysis-sheet-list-hint';
      hint.textContent = localize('ui.sheets.noEditTableAvailable');
      host.append(hint);
      return;
    }
    if (!sheets.some((s) => s.id === state.loadedSheet)) {
      state.loadedSheet = firstSheet.id;
    }
    host.querySelectorAll('button').forEach((btn, i) => {
      btn.classList.toggle('is-active', sheets[i]?.id === state.loadedSheet);
    });
    if (state.loadedSheet) {
      await this.loadSheet(state.loadedSheet, state.sheetPage);
    }
  }

  getSheetHistory(id: string): SheetUndoHistory {
    const { state } = this.d;
    let history = state.undoHistory.get(id);
    if (!history) {
      history = { undo: [], redo: [] };
      state.undoHistory.set(id, history);
    }
    return history;
  }

  updateSheetHistoryButtons(): void {
    const { state } = this.d;
    const hist = state.loadedSheet
      ? this.getSheetHistory(state.loadedSheet)
      : { undo: [], redo: [] };
    requireButton('analysisUndo').disabled = !hist.undo.length;
    requireButton('analysisRedo').disabled = !hist.redo.length;
  }

  async commitBatch(
    id: string,
    changes: readonly AnalysisCellChange[],
    options: CommitBatchOptions = {},
  ): Promise<SheetBatchResult | undefined> {
    const { track = true, refresh = false, guard } = options;
    const { state, drafts, post } = this.d;
    if (!id || !changes.length) return undefined;
    if (guard && !guard()) {
      throw makeHttpError(localize('ui.sheets.targetOrVersionChangedRe'), 409);
    }
    const book = state.fingerprint;
    const staged = drafts.stage(book, id, changes);
    const result = await this.queueWrite(async () => {
      if (book !== state.fingerprint) throw new Error(localize('ui.common.workbookSwitched'));
      if (guard && !guard()) {
        throw makeHttpError(localize('ui.sheets.targetOrVersionChangedEntire'), 409);
      }
      validateClipboardChanges(changes);
      // Resolve expected version INSIDE the serialized write queue.
      const expectedRevision = state.sheetRevisions.get(id) ?? 0;
      const saved = await post('/api/analysis/sheet/batch', {
        id,
        workbookKey: book,
        expectedRevision,
        changes,
      }).then(decodeSheetBatchResult);
      if (book === state.fingerprint) {
        state.sheetRevisions.set(id, saved.revision);
        state.reportCache.clear();
      }
      return saved;
    });
    drafts.acknowledge(staged);
    this.updateSaveStatus();
    if (book !== state.fingerprint) return result;
    if (track) {
      const hist = this.getSheetHistory(id);
      hist.undo.push({ before: result.previous, after: changes.map((c) => ({ ...c })) });
      if (hist.undo.length > 100) hist.undo.shift();
      hist.redo = [];
    }
    this.updateSheetHistoryButtons();
    if (state.loadedSheet === id) {
      for (const input of requireEl('analysisEditableGrid').querySelectorAll<HTMLTextAreaElement>(
        'textarea[data-row][data-col]',
      )) {
        const row = Number(input.dataset.row);
        const col = Number(input.dataset.col);
        this.paintDraft(input, row, col, this.editGridCellValue(row, col));
      }
    }
    if (refresh && state.loadedSheet === id) {
      await this.loadSheet(id, state.sheetPage);
    }
    return result;
  }

  async retryDrafts(): Promise<void> {
    const { state, drafts, setTab, toast } = this.d;
    if (this.pendingWrites) return;
    const book = state.fingerprint;
    const groups = new Map<string, AnalysisDraftEntry[]>();
    for (const entry of drafts.entries(book)) {
      const list = groups.get(entry.id) ?? [];
      list.push(entry);
      groups.set(entry.id, list);
    }
    for (const [id, entries] of groups) {
      try {
        for (const batch of clipboardBatches(entries)) {
          if (book !== state.fingerprint) return;
          const active = batch.filter(
            (entry) => drafts.get(book, id, entry.row, entry.col) === entry,
          );
          const firstActive = active[0];
          if (!firstActive) continue;
          try {
            await this.commitBatch(
              id,
              active.map(({ row, col, value }) => ({ row, col, value })),
              { refresh: state.loadedSheet === id },
            );
          } catch (err: unknown) {
            if (getHttpStatus(err) === 409) {
              state.undoHistory.delete(id);
              state.loadedSheet = id;
              state.sheetPage = Math.floor(firstActive.row / this.sheetPageSize(id));
              void setTab('sheets');
              await this.loadSheet(id, state.sheetPage);
              toast(localize('ui.sheets.versionConflictsDraftsHaveRetained'), 'warning');
            }
            return;
          }
        }
      } catch (err: unknown) {
        toast(errorMessage(err), 'warning');
        return;
      }
    }
    if (book === state.fingerprint && !drafts.entries(book).length) {
      toast(localize('ui.sheets.draftSaved'), 'success');
    }
  }

  async changeSheetHistory(direction: 'undo' | 'redo'): Promise<void> {
    const { state, toast } = this.d;
    const id = state.loadedSheet;
    if (!id) return;
    const hist = this.getSheetHistory(id);
    const from = direction === 'undo' ? hist.undo : hist.redo;
    const item = from.pop();
    if (!item) return;
    this.updateSheetHistoryButtons();
    try {
      await this.commitBatch(id, direction === 'undo' ? item.before : item.after, {
        track: false,
        refresh: false,
      });
      (direction === 'undo' ? hist.redo : hist.undo).push(item);
      this.updateSheetHistoryButtons();
      await this.loadSheet(id, state.sheetPage);
    } catch (err: unknown) {
      from.push(item);
      this.updateSheetHistoryButtons();
      if (getHttpStatus(err) === 409) {
        hist.undo = [];
        hist.redo = [];
        await this.loadSheet(id, state.sheetPage);
        toast(localize('ui.sheets.editedVersionChangedOperationNot'), 'warning');
      } else {
        toast(errorMessage(err), 'error');
      }
    }
  }

  async loadSheet(id: string, page: number): Promise<void> {
    const { state, api, post, setText, fmtNum, refreshWorkspace, toast } = this.d;
    if (!id) return;
    requireButton('analysisPasteSpecial').disabled = true;
    const seq = ++state.sheetLoadSeq;
    state.sheetAbortController?.abort();
    const controller = new AbortController();
    state.sheetAbortController = controller;
    try {
      // Reuse the fetch page size across loads; cap DOM nodes for wide grids.
      const pageSize = this.sheetPageSize(id);
      const raw = await api(
        `/api/analysis/sheet?${new URLSearchParams({
          id,
          offset: String(page * pageSize),
          limit: String(pageSize),
        }).toString()}`,
        { signal: controller.signal },
        decodeAnalysisSheetPageWire,
      );
      if (seq !== state.sheetLoadSeq || state.loadedSheet !== id || state.sheetPage !== page) {
        return;
      }
      const data: MutableAnalysisSheetPage = {
        id: raw.id,
        name: raw.name,
        columns: [...raw.columns],
        rows: (raw.rows ?? []).map((r) => [...r]),
        offset: raw.offset,
        total: raw.total,
        revision: raw.revision,
      };
      state.sheetRevisions.set(id, data.revision || 0);
      state.gridData = data;
      requireButton('analysisPasteSpecial').disabled = false;
      setText('analysisSheetTitle', data.name);
      requireButton('analysisAddColumn').disabled = false;
      requireButton('analysisAddRows').disabled = false;
      requireButton('analysisDeleteSheet').disabled = false;
      this.updateSheetHistoryButtons();

      const area = requireEl('analysisEditableGrid');
      area.replaceChildren();
      const table = document.createElement('table');
      const head = document.createElement('thead');
      const tr = document.createElement('tr');
      const zero = document.createElement('th');
      zero.textContent = '';
      tr.append(zero);
      data.columns.forEach((name, j) => {
        const th = document.createElement('th');
        const input = document.createElement('input');
        input.value = name;
        input.title = localize('ui.sheets.clickEditColumnNamesWill');
        input.addEventListener('change', () => {
          const cols = [...data.columns];
          const trimmed = input.value.trim();
          if (!trimmed) {
            input.value = name;
            return;
          }
          cols[j] = trimmed;
          void this.queueWrite(async () => {
            const rev = state.sheetRevisions.get(id) ?? 0;
            const result = await post('/api/analysis/sheet/columns', {
              id,
              columns: cols,
              expectedRevision: rev,
            });
            state.sheetRevisions.set(id, rev + 1);
            state.reportCache.clear();
            return result;
          })
            .then(() => {
              data.columns = cols;
              state.undoHistory.delete(id);
              this.updateSheetHistoryButtons();
              void refreshWorkspace();
            })
            .catch(async () => {
              input.value = name;
              await this.loadSheet(id, page);
            });
        });
        th.append(input);
        tr.append(th);
      });
      head.append(tr);
      table.append(head);

      const body = document.createElement('tbody');
      const rows = Math.min(
        Math.max(data.rows.length, Math.min(25, pageSize)),
        Math.min(pageSize, 10000 - data.offset),
      );
      const fragment = document.createDocumentFragment();
      for (let i = 0; i < rows; i++) {
        const row = document.createElement('tr');
        const n = document.createElement('td');
        n.textContent = String(data.offset + i + 1);
        row.append(n);
        for (let j = 0; j < data.columns.length; j++) {
          const td = document.createElement('td');
          const input = document.createElement('textarea');
          input.rows = 1;
          const saved = data.rows[i]?.[j] ?? '';
          input.value = saved;
          input.dataset.row = String(data.offset + i);
          input.dataset.col = String(j);
          input.title = localize('ui.sheets.row', { detail: data.columns[j] ?? '', offset: data.offset + i + 1 });
          input.dataset.savedTitle = input.title;
          const draft = this.paintDraft(input, data.offset + i, j, saved);
          if (draft) input.value = draft.value;
          td.append(input);
          row.append(td);
        }
        fragment.append(row);
      }
      body.append(fragment);
      table.append(body);
      area.append(table);

      const pagination = requireEl('analysisSheetPagination');
      pagination.replaceChildren();
      const prev = document.createElement('button');
      prev.type = 'button';
      prev.textContent = localize('ui.sheets.previousPage');
      prev.disabled = page === 0;
      prev.onclick = (): void => {
        void (async () => {
          await this.flush();
          state.sheetPage = page - 1;
          await this.loadSheet(id, state.sheetPage);
        })();
      };
      pagination.append(prev);
      const span = document.createElement('span');
      span.textContent = localize('ui.sheets.pageRowsSavedRowsPer', { page: page + 1, fmtNum: fmtNum(data.total), pageSize });
      pagination.append(span);
      const next = document.createElement('button');
      next.type = 'button';
      next.textContent = localize('ui.sheets.nextPage');
      const lastAllowed = Math.min(9999, Math.max(data.total, 1) + pageSize - 1);
      next.disabled = (page + 1) * pageSize > lastAllowed;
      next.onclick = (): void => {
        void (async () => {
          await this.flush();
          state.sheetPage = page + 1;
          await this.loadSheet(id, state.sheetPage);
        })();
      };
      pagination.append(next);
    } catch (e: unknown) {
      if (!isAbortError(e) && seq === state.sheetLoadSeq) {
        toast(errorMessage(e), 'error');
      }
    } finally {
      if (state.sheetAbortController === controller) {
        state.sheetAbortController = null;
      }
    }
  }

  editGridCellValue(row: number, col: number): string {
    const data = this.d.state.gridData;
    if (!data) return '';
    const index = row - data.offset;
    return data.rows[index]?.[col] ?? '';
  }

  sheetPageSize(id: string): number {
    const info = this.d.state.workspace?.sheets.find((x) => x.id === id);
    return Math.max(30, Math.min(80, Math.floor(2600 / Math.max(1, info?.columns.length || 10))));
  }

  editGridWriteLocal(row: number, col: number, value: string): void {
    const data = this.d.state.gridData;
    if (!data || row < data.offset) return;
    const index = row - data.offset;
    if (index >= 300) return;
    while (data.rows.length <= index) data.rows.push([]);
    const targetRow = data.rows[index];
    if (!targetRow) return;
    while (targetRow.length <= col) targetRow.push('');
    targetRow[col] = value;
  }

  bindSheetGrid(): void {
    const { state, drafts, toast } = this.d;
    const area = requireEl('analysisEditableGrid');
    area.addEventListener('focusin', (event: FocusEvent) => {
      const input = closestCellTextArea(event.target);
      if (input && state.loadedSheet) {
        state.sheetCell = {
          id: state.loadedSheet,
          page: state.sheetPage,
          row: Number(input.dataset.row),
          col: Number(input.dataset.col),
        };
      }
    });
    area.addEventListener('input', (event: Event) => {
      const input = closestCellTextArea(event.target);
      if (!input || !state.loadedSheet) return;
      const row = Number(input.dataset.row);
      const col = Number(input.dataset.col);
      const saved = this.editGridCellValue(row, col);
      const staged = drafts.stage(state.fingerprint, state.loadedSheet, [
        { row, col, value: input.value },
      ]);
      if (input.value === saved && !this.pendingWrites) {
        drafts.acknowledge(staged);
      }
      this.paintDraft(input, row, col, saved);
      this.updateSaveStatus();
    });
    area.addEventListener('change', (event: Event) => {
      const input = closestCellTextArea(event.target);
      if (!input || !state.loadedSheet) return;
      const row = Number(input.dataset.row);
      const col = Number(input.dataset.col);
      const previous = this.editGridCellValue(row, col);
      if (previous === input.value && !this.pendingWrites) {
        const draft = drafts.get(state.fingerprint, state.loadedSheet, row, col);
        if (draft) drafts.acknowledge([draft]);
        this.updateSaveStatus();
        return;
      }
      const id = state.loadedSheet;
      const next = input.value;
      void this.commitBatch(id, [{ row, col, value: next }])
        .then(() => {
          if (state.loadedSheet === id) this.editGridWriteLocal(row, col, next);
        })
        .catch(async (err: unknown) => {
          if (getHttpStatus(err) === 409 && state.loadedSheet === id) {
            state.undoHistory.delete(id);
            await this.loadSheet(id, state.sheetPage);
            toast(localize('ui.sheets.versionConflictDraftRetainedRechecke4119ef'), 'warning');
          }
        });
    });
    area.addEventListener('paste', (event: ClipboardEvent) => {
      const input = closestCellTextArea(event.target);
      if (!input || !state.loadedSheet) return;
      const text = event.clipboardData?.getData('text/plain') || '';
      if (!text.includes('\t') && !/[\r\n]/.test(text)) {
        const value =
          input.value.slice(0, input.selectionStart) + text + input.value.slice(input.selectionEnd);
        try {
          validateClipboardChanges([
            { row: Number(input.dataset.row), col: Number(input.dataset.col), value },
          ]);
        } catch (err: unknown) {
          event.preventDefault();
          toast(errorMessage(err), 'warning');
        }
        return;
      }
      event.preventDefault();
      const startRow = Number(input.dataset.row);
      const startCol = Number(input.dataset.col);
      const cols = state.gridData?.columns.length ?? 0;
      let changes: AnalysisCellChange[];
      try {
        changes = pasteChangesFromTSV(text, {
          startRow,
          startCol,
          rowLimit: 10000,
          columns: cols,
        });
      } catch (err: unknown) {
        toast(errorMessage(err), 'warning');
        return;
      }
      const id = state.loadedSheet;
      void this.commitBatch(id, changes, { refresh: true }).catch(async (err: unknown) => {
        if (getHttpStatus(err) === 409 && state.loadedSheet === id) {
          state.undoHistory.delete(id);
          await this.loadSheet(id, state.sheetPage);
          toast(localize('ui.sheets.versionConflictDraftRetainedRecheck'), 'warning');
        } else {
          toast(localize('ui.sheets.pastingWithoutSaving', { detail: errorMessage(err) }), 'error');
        }
      });
    });
    area.addEventListener('keydown', (event: KeyboardEvent) => {
      const input = closestCellTextArea(event.target);
      if (!input || event.isComposing) return;
      const mod = event.ctrlKey || event.metaKey;
      if (mod && event.shiftKey && !event.altKey && event.key.toLowerCase() === 'v') {
        event.preventDefault();
        event.stopPropagation();
        void this.openPasteSpecial();
        return;
      }
      if (mod && !event.altKey && ['z', 'y'].includes(event.key.toLowerCase())) {
        const row = Number(input.dataset.row);
        const col = Number(input.dataset.col);
        if (input.value !== this.editGridCellValue(row, col)) return; // native undo while typing
        event.preventDefault();
        void this.changeSheetHistory(
          event.key.toLowerCase() === 'z' && !event.shiftKey ? 'undo' : 'redo',
        );
        return;
      }
      if (mod || event.altKey) return;
      let nr = Number(input.dataset.row);
      let nc = Number(input.dataset.col);
      if (event.key === 'Enter' && !event.shiftKey) nr++;
      else if (event.key === 'Tab') nc += event.shiftKey ? -1 : 1;
      else return;
      const data = state.gridData;
      if (
        !data ||
        nr < data.offset ||
        nr >= data.offset + 300 ||
        nc < 0 ||
        nc >= data.columns.length
      ) {
        return;
      }
      event.preventDefault();
      input.blur();
      const next = area.querySelector<HTMLTextAreaElement>(
        `textarea[data-row="${nr}"][data-col="${nc}"]`,
      );
      next?.focus();
      next?.select();
    });
  }

  async addColumn(): Promise<void> {
    const { state, api, post, askText, refreshWorkspace, toast } = this.d;
    if (!state.loadedSheet) return;
    const id = state.loadedSheet;
    const book = state.fingerprint;
    const data = await api(
      `/api/analysis/sheet?${new URLSearchParams({ id, offset: '0', limit: '1' }).toString()}`,
      undefined,
      decodeAnalysisSheetPage,
    );
    if (data.columns.length >= 80) {
      toast(localize('ui.sheets.editWorksheetMaximumColumns'), 'warning');
      return;
    }
    const name = await askText(localize('ui.sheets.addNewColumn'), localize('ui.sheets.field', { count: data.columns.length + 1 }));
    if (!name) return;
    try {
      await this.queueWrite(async () => {
        if (book !== state.fingerprint) throw new Error(localize('ui.common.workbookSwitched'));
        const result = await post('/api/analysis/sheet/columns', {
          id,
          workbookKey: book,
          expectedRevision: data.revision,
          columns: [...data.columns, name],
        }).then(decodeSheetRevisionResponse);
        state.sheetRevisions.set(id, result.revision);
      });
      if (book === state.fingerprint) {
        await refreshWorkspace();
        await this.renderSheets();
      }
    } catch {
      // Error surfaced by queueWrite.
    }
  }

  async addRows(): Promise<void> {
    const { state, post, refreshWorkspace } = this.d;
    if (!state.loadedSheet) return;
    const id = state.loadedSheet;
    const book = state.fingerprint;
    try {
      await this.queueWrite(async () => {
        if (book !== state.fingerprint) throw new Error(localize('ui.common.workbookSwitched'));
        const result = await post('/api/analysis/sheet/rows', {
          id,
          workbookKey: book,
          expectedRevision: state.sheetRevisions.get(id) ?? 0,
          count: 10,
        }).then(decodeSheetRevisionResponse);
        state.sheetRevisions.set(id, result.revision);
      });
      if (book === state.fingerprint) {
        await refreshWorkspace();
        await this.renderSheets();
      }
    } catch {
      // Error surfaced by queueWrite.
    }
  }

  async deleteSheet(): Promise<void> {
    const { state, post, askText, refreshWorkspace, setTab, toast } = this.d;
    if (this.hasUnsaved()) {
      toast(localize('ui.common.thereStillAnalysisResultsThat'), 'warning');
      return;
    }
    const sheet = state.workspace?.sheets.find((s) => s.id === state.loadedSheet);
    if (!sheet) return;
    const confirmation = await askText(localize('ui.sheets.confirmDeleteEditWorksheet'), '', localize('ui.sheets.enterConfirm', { name: sheet.name }));
    if (confirmation !== sheet.name) {
      if (confirmation !== null) toast(localize('ui.sheets.nameDoesNotMatchDeletion'), 'warning');
      return;
    }
    try {
      await post('/api/analysis/delete', { kind: 'sheet', id: sheet.id });
      state.loadedSheet = null;
      await refreshWorkspace();
      void setTab('sheets');
      toast(localize('ui.sheets.selectedWorksheetDeleted'), 'success');
    } catch (e: unknown) {
      toast(errorMessage(e), 'error');
    }
  }

  private currentGridData(): MutableAnalysisSheetPage | null {
    return this.d.state.gridData;
  }

  async navigateSheet(id: string, row: number, column: number): Promise<void> {
    const { state, open, setTab } = this.d;
    if (this.hasUnsaved()) throw new Error(localize('ui.sheets.analysisTableStillUnsavedContent'));
    if (!Number.isInteger(row) || !Number.isInteger(column) || row < 1 || column < 1) {
      throw new Error(localize('ui.sheets.editingTablePositioningCoordinatesIneffective'));
    }
    await open();
    const sheet = state.workspace?.sheets.find((s) => s.id === id);
    if (!state.opened || !sheet || row > sheet.rows || column > sheet.columns.length) {
      throw new Error(localize('ui.sheets.tableOrSourceLocationChanged'));
    }
    state.loadedSheet = id;
    state.sheetPage = Math.floor((row - 1) / this.sheetPageSize(id));
    state.gridData = null;
    await setTab('sheets');
    if (this.currentGridData()?.id !== id) throw new Error(localize('ui.sheets.editingTableReadFailedTry'));
    const target = requireEl('analysisEditableGrid').querySelector<HTMLTextAreaElement>(
      `textarea[data-row="${row - 1}"][data-col="${column - 1}"]`,
    );
    if (!target) throw new Error(localize('ui.sheets.unableReadSelectedSourceLocation'));
    target.focus();
    target.scrollIntoView({ block: 'center', inline: 'nearest' });
  }

  capturePasteTarget(previous?: PasteSpecialTargetContext): AnalysisPasteTargetContext {
    const { state, drafts } = this.d;
    const data = state.gridData;
    const id = state.loadedSheet;
    if (!state.opened || state.tab !== 'sheets' || !data || !id) {
      throw new Error(localize('ui.sheets.openIndividualEditingTablesSeparately'));
    }
    if (data.id !== id) {
      throw new Error(localize('ui.sheets.openIndividualEditingTablesSeparately'));
    }
    const book = state.fingerprint;
    const pathBook = state.book;
    const page = state.sheetPage;
    const revision = state.sheetRevisions.get(id);
    if (previous) {
      if (
        !isAnalysisPasteTarget(previous) ||
        previous.id !== id ||
        previous.book !== book ||
        previous.pathBook !== pathBook ||
        previous.page !== page
      ) {
        throw new Error(localize('ui.sheets.targetTableOrPageChanged'));
      }
    } else if (drafts.entries(book).some((entry) => entry.id === id)) {
      throw new Error(localize('ui.sheets.thereStillUnsavedDraftsThis'));
    }
    const selected =
      previous ??
      (state.sheetCell?.id === id && state.sheetCell.page === page
        ? state.sheetCell
        : { row: data.offset, col: 0 });
    const row = selected.row;
    const col = selected.col;
    return {
      id,
      book,
      pathBook,
      page,
      revision,
      row,
      col,
      columns: data.columns.length,
      label: `${data.name} · ${columnName(col + 1)}${row + 1}`,
      current: (): boolean =>
        state.opened &&
        state.tab === 'sheets' &&
        state.fingerprint === book &&
        state.book === pathBook &&
        state.loadedSheet === id &&
        state.sheetPage === page &&
        state.gridData?.id === id &&
        state.sheetRevisions.get(id) === revision,
    };
  }

  async refreshPasteTarget(previous: PasteSpecialTargetContext): Promise<void> {
    const { state } = this.d;
    if (
      !isAnalysisPasteTarget(previous) ||
      state.fingerprint !== previous.book ||
      state.book !== previous.pathBook ||
      state.loadedSheet !== previous.id ||
      state.sheetPage !== previous.page
    ) {
      throw new Error(localize('ui.sheets.targetChangedCloseReSelect'));
    }
    await this.flush();
    await this.loadSheet(previous.id, previous.page);
  }

  async openPasteSpecial(): Promise<void> {
    const { state } = this.d;
    const book = state.fingerprint;
    const pathBook = state.book;
    const id = state.loadedSheet;
    const page = state.sheetPage;
    await this.flush();
    if (
      book !== state.fingerprint ||
      pathBook !== state.book ||
      id !== state.loadedSheet ||
      page !== state.sheetPage
    ) {
      return;
    }
    this.pasteSpecial?.open();
  }
}
