/**
 * Search panel UI, chunked result list, advanced syntax validation, history,
 * server-paged full search, and grid hit navigation.
 */
import { t as localize } from '../core/i18n.ts';
import type { ActivityHandle, ApiClient, ToastFn } from '../contracts/common.ts';
import type {
  SearchHit,
  SearchHistoryItem,
  SearchPageResponse,
  SearchStatusResponse,
} from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
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
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeAnalysisFieldsResponse,
  decodeSearchHistoryApiResponse,
  decodeSearchPageApiResponse,
  decodeSearchStatusApiResponse,
} from '../core/http-decoders.generated.ts';

const CHUNK_SIZE = 200;

const SNIPPETS: readonly (readonly [string, string])[] = [
  [localize('ui.searchPanel.and'), ' AND '],
  [localize('ui.searchPanel.or'), ' OR '],
  [localize('ui.searchPanel.exclude'), ' NOT '],
  [localize('ui.searchPanel.range'), ' BETWEEN 100 AND 200'],
  [localize('ui.searchPanel.multipleSelection'), ' IN ("A","B")'],
  [localize('ui.searchPanel.nullValues'), ' IS EMPTY'],
  [localize('ui.searchPanel.regularExpression'), '~"^BX[0-9]+$"'],
  [localize('ui.common.formula'), 'formula:SUMIF'],
];

/** @httpResponse SearchHistoryApiResponse */
export interface SearchHistoryApiResponse {
  readonly items?: readonly SearchHistoryItem[] | null;
}

/** @httpResponse SearchStatusApiResponse */
export interface SearchStatusApiResponse extends Omit<SearchStatusResponse, 'matches'> {
  readonly matches?: readonly SearchHit[] | null;
}

/** @httpResponse SearchPageApiResponse */
export interface SearchPageApiResponse extends Omit<SearchPageResponse, 'matches'> {
  readonly matches?: readonly SearchHit[] | null;
}

interface SearchPagesState {
  readonly id: string;
  readonly total: number;
  readonly download: string;
  readonly previous: number[];
  after: number;
  next: number | null;
  text: string;
  sheet: string;
}

export interface SearchRenderData {
  readonly matches?: readonly SearchHit[] | null;
  readonly mode?: string;
  readonly status?: string;
}

export interface SearchPanelOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly delay: (ms: number) => Promise<void>;
  readonly copyText: (value: string, successMessage?: string) => Promise<boolean> | boolean | undefined;
  readonly navigateToCell: (sheet: string, ref: string) => Promise<unknown>;
  readonly runSearch: (sheet?: string | null) => Promise<void> | void;
  readonly cancelSearch: (id: string | null, updateUI?: boolean) => Promise<void> | void;
}

export class SearchPanel {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly delay: (ms: number) => Promise<void>;
  private readonly copyText: (
    value: string,
    successMessage?: string,
  ) => Promise<boolean> | boolean | undefined;
  private readonly navigateToCell: (sheet: string, ref: string) => Promise<unknown>;
  private readonly runSearch: (sheet?: string | null) => Promise<void> | void;
  private readonly cancelSearch: (id: string | null, updateUI?: boolean) => Promise<void> | void;

  private positions = new Map<number, number>();
  private pageSerial = 0;
  private pageAbort: AbortController | null = null;
  private pages: SearchPagesState | null = null;
  private syntaxSeq = 0;
  private syntaxTimer: ReturnType<typeof setTimeout> | undefined;
  private renderedCount = 0;
  private renderedData: SearchRenderData | null = null;
  private filterTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(options: SearchPanelOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.delay = options.delay;
    this.copyText = options.copyText;
    this.navigateToCell = options.navigateToCell;
    this.runSearch = options.runSearch;
    this.cancelSearch = options.cancelSearch;
  }

  buildHits(matches: readonly SearchHit[]): void {
    this.s.searchHitMap = new Map();
    for (const match of matches) {
      const sheet = match.sheet || this.s.searchSession?.sheet || this.s.sheet || '';
      let hits = this.s.searchHitMap.get(sheet);
      if (!hits) {
        hits = new Set();
        this.s.searchHitMap.set(sheet, hits);
      }
      hits.add(match.ref);
    }
  }

  isMatch(sheet: string | null | undefined, ref: string | null | undefined): boolean {
    if (!sheet || !ref) return false;
    return this.s.searchHitMap.get(sheet)?.has(ref) ?? false;
  }

  updateNavButtons(): void {
    const count = this.s.visibleSearchIndexes.length;
    requireButton('searchPrevBtn').disabled = !count;
    requireButton('searchNextBtn').disabled = !count;
    requireButton('copySearchResultsBtn').disabled = !count;
    requireButton('saveSearchResultsBtn').disabled = this.pages ? !this.pages.total : !count;
  }

  resetWorkbook(): void {
    this.leavePages();
    if (this.syntaxTimer !== undefined) {
      clearTimeout(this.syntaxTimer);
      this.syntaxTimer = undefined;
    }
    if (this.filterTimer !== undefined) {
      clearTimeout(this.filterTimer);
      this.filterTimer = undefined;
    }
    this.renderedCount = 0;
    this.renderedData = null;
    this.positions.clear();
    this.s.searchCache.clear();
    this.s.searchHitMap.clear();
    ++this.syntaxSeq;
    this.s.searchSession = null;
    this.s.searchActivity?.end();
    ++this.s.searchRequestID;
    this.s.searchSelectedIndex = -1;
    this.s.searchJob = null;
    this.s.serverSearchHistory = [];
    this.s.visibleSearchIndexes = [];
    this.updateNavButtons();
    requireEl('searchResults').replaceChildren();
    requireInput('searchInput').value = '';
    requireInput('searchResultFilter').value = '';
    requireSelect('searchResultSheet').value = '';
    requireEl('searchHistoryPanel').hidden = true;
    requireEl('searchSummary').textContent = localize('ui.searchPanel.enterContentSearch');
  }

  async refreshHistory(): Promise<void> {
    if (!this.s.opened) return;
    const data = await this.api('/api/search/history', undefined, decodeSearchHistoryApiResponse);
    this.s.serverSearchHistory = data.items ? [...data.items] : [];
    const list = requireEl('searchHistoryList');
    list.replaceChildren();
    if (!this.s.serverSearchHistory.length) {
      const message = document.createElement('div');
      message.className = 'history-empty';
      message.textContent = localize('ui.searchPanel.noCompletedSearchYet');
      list.append(message);
      return;
    }
    const fragment = document.createDocumentFragment();
    for (const item of this.s.serverSearchHistory) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'history-item';
      button.title = localize('ui.searchPanel.items', { query: item.query, detail: item.scope === 'all' ? localize('ui.common.allWorksheets') : item.sheet, hits: item.hits });
      const query = document.createElement('span');
      query.className = 'history-query';
      const modeLabel =
        item.mode === 'value'
          ? localize('ui.searchPanel.content')
          : item.mode === 'reference'
            ? localize('ui.searchPanel.reference')
            : item.mode === 'advanced'
              ? localize('ui.searchPanel.advanced')
              : localize('ui.common.formula');
      query.textContent = `${modeLabel} · ${item.query}`;
      const meta = document.createElement('span');
      meta.className = 'history-meta';
      meta.textContent = localize('ui.searchPanel.sentences', { detail: item.scope === 'all' ? localize('ui.searchPanel.fullTable') : localize('ui.searchPanel.currentTable'), hits: item.hits });
      button.append(query, meta);
      button.addEventListener('click', () => {
        requireEl('searchHistoryPanel').hidden = true;
        requireButton('searchHistoryBtn').setAttribute('aria-expanded', 'false');
        requireSelect('searchMode').value = item.mode;
        requireSelect('searchScope').value = item.scope;
        requireInput('searchFull').checked = false;
        requireInput('searchRecoverable').checked = false;
        requireInput('searchInput').value = item.query;
        requireTextArea('searchAdvancedInput').value = item.query;
        this.syncMode();
        requireInput('searchResultFilter').value = '';
        requireSelect('searchResultSheet').value = '';
        // Restore cached results without forcing the original worksheet to be
        // decompressed; navigation happens only when a hit is clicked.
        void this.runSearch(item.scope === 'current' ? item.sheet : null);
      });
      fragment.append(button);
    }
    list.append(fragment);
  }

  buildSheetChoices(matches: readonly { readonly sheet?: string }[]): void {
    const select = requireSelect('searchResultSheet');
    const old = select.value;
    const names = new Set(
      matches.map((item) => item.sheet || this.s.searchSession?.sheet || this.s.sheet || ''),
    );
    select.replaceChildren();
    const all = document.createElement('option');
    all.value = '';
    all.textContent = localize('ui.common.allWorksheets');
    select.append(all);
    for (const sheet of this.s.sheets) {
      if (!names.has(sheet.name)) continue;
      const option = document.createElement('option');
      option.value = sheet.name;
      option.textContent = sheet.name;
      select.append(option);
    }
    select.value = names.has(old) ? old : '';
  }

  filteredIndexes(matches: readonly SearchHit[]): number[] {
    const needle = requireInput('searchResultFilter').value.trim().toLocaleLowerCase();
    const sheetOnly = requireSelect('searchResultSheet').value;
    const found: number[] = [];
    this.positions.clear();
    for (let i = 0; i < matches.length; i++) {
      const item = matches[i];
      if (!item) continue;
      const sheet = item.sheet || this.s.searchSession?.sheet || this.s.sheet || '';
      if (sheetOnly && sheet !== sheetOnly) continue;
      if (
        needle &&
        ![sheet, item.ref, item.formula ?? '', item.value ?? ''].some((text) =>
          text.toLocaleLowerCase().includes(needle),
        )
      ) {
        continue;
      }
      this.positions.set(i, found.length);
      found.push(i);
    }
    return found;
  }

  copyVisible(): void {
    const matches = this.s.searchSession?.matches ?? [];
    const rows: string[][] = [['Sheet', localize('ui.common.cell'), localize('ui.searchPanel.cacheValue'), localize('ui.common.formula')]];
    const clean = (value: string | undefined): string =>
      (value ?? '').replaceAll('\t', ' ').replaceAll('\r', ' ').replaceAll('\n', ' ');
    for (const i of this.s.visibleSearchIndexes) {
      const hit = matches[i];
      if (!hit) continue;
      rows.push([
        clean(hit.sheet || this.s.searchSession?.sheet || ''),
        clean(hit.ref),
        clean(hit.value),
        clean(hit.formula),
      ]);
    }
    void this.copyText(rows.map((row) => row.join('\t')).join('\n'));
  }

  downloadVisible(): void {
    if (this.pages) {
      const url = new URL(this.pages.download, window.location.href);
      url.searchParams.set('text', requireInput('searchResultFilter').value);
      url.searchParams.set('sheet', requireSelect('searchResultSheet').value);
      const link = document.createElement('a');
      link.href = url.href;
      link.download = localize('ui.searchPanel.excellensSearchResultsCsv');
      link.click();
      return;
    }
    const matches = this.s.searchSession?.matches ?? [];
    if (!this.s.visibleSearchIndexes.length) {
      this.toast(localize('ui.searchPanel.noSearchResultsExport'), 'warning');
      return;
    }
    const guard = (text: string | undefined): string => {
      let value = text ?? '';
      if (/^\s*[=+\-@]/.test(value)) value = `'${value}`;
      return `"${value.replaceAll('"', '""')}"`;
    };
    const rows: (string | undefined)[][] = [[localize('ui.searchPanel.worksheet'), localize('ui.common.cell'), localize('ui.searchPanel.cachedDisplayValue'), localize('ui.searchPanel.originalFormula')]];
    for (const i of this.s.visibleSearchIndexes) {
      const hit = matches[i];
      if (!hit) continue;
      rows.push([
        hit.sheet || this.s.searchSession?.sheet || this.s.sheet || '',
        hit.ref,
        hit.value,
        hit.formula,
      ]);
    }
    const csv = `\ufeff${rows.map((row) => row.map(guard).join(',')).join('\r\n')}`;
    const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = localize('ui.searchPanel.excellensSearchResultsCsv');
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 30000);
    this.toast(localize('ui.searchPanel.resultsHaveExported', { count: this.s.visibleSearchIndexes.length }), 'success');
  }

  displaySummary(message: string): void {
    const el = requireEl('searchSummary');
    el.textContent = message;
    el.hidden = !message;
    if (this.s.searchSession) this.s.searchSession.summary = message;
  }

  syncMode(): void {
    const advanced = requireSelect('searchMode').value === 'advanced';
    requireEl('searchAdvanced').hidden = !advanced;
    requireInput('searchInput').hidden = advanced;
    if (advanced) {
      // Keep the primary action aligned: the advanced expression already has
      // its own edit area; don't duplicate hints in a second input.
      const advInput = requireTextArea('searchAdvancedInput');
      const simpleInput = requireInput('searchInput');
      if (!advInput.value && simpleInput.value) advInput.value = simpleInput.value;
      void this.populateFields();
      this.validateAdvanced();
    }
  }

  validateAdvanced(): void {
    if (requireSelect('searchMode').value !== 'advanced') return;
    if (this.syntaxTimer !== undefined) {
      clearTimeout(this.syntaxTimer);
      this.syntaxTimer = undefined;
    }
    const token = ++this.syntaxSeq;
    const source = requireTextArea('searchAdvancedInput').value.trim();
    const feedback = requireEl('searchAdvancedFeedback');
    feedback.classList.remove('is-invalid');
    if (!source) {
      feedback.textContent = localize('ui.searchPanel.enterExpressionOrSelectExample');
      return;
    }
    this.syntaxTimer = setTimeout(() => {
      void (async () => {
        try {
          await this.api('/api/search/validate', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ query: source }),
          });
          if (token === this.syntaxSeq) {
            feedback.classList.remove('is-invalid');
            feedback.textContent = localize('ui.searchPanel.syntaxCorrectCombinedJudgmentWill');
          }
        } catch (e: unknown) {
          if (token === this.syntaxSeq) {
            feedback.classList.add('is-invalid');
            feedback.textContent = errorMessage(e);
          }
        }
      })();
    }, 350);
  }

  insertAdvanced(text: string, replace = false): void {
    const input = requireTextArea('searchAdvancedInput');
    const start = input.selectionStart;
    const end = input.selectionEnd;
    input.value = replace ? text : input.value.slice(0, start) + text + input.value.slice(end);
    const caret = replace ? input.value.length : start + text.length;
    input.focus();
    input.setSelectionRange(caret, caret);
    this.validateAdvanced();
  }

  async populateFields(): Promise<void> {
    const host = byId('searchAdvancedFields');
    if (!host) return;
    const sheet = this.s.sheet;
    if (!sheet || host.dataset.sheet === sheet) return;
    host.dataset.sheet = sheet;
    host.replaceChildren();
    try {
      const data = await this.api(
        `/api/analysis/fields?${new URLSearchParams({ source: `wb:${sheet}`, headerRow: '1' }).toString()}`,
        undefined,
        decodeAnalysisFieldsResponse,
      );
      if (host.dataset.sheet !== sheet) return;
      const fragment = document.createDocumentFragment();
      for (const f of data.fields.slice(0, 96)) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.textContent = f.name;
        btn.title = localize('ui.searchPanel.insertField', { id: f.id });
        btn.addEventListener('click', () => this.insertAdvanced(`[${f.name}]:`, false));
        fragment.append(btn);
      }
      host.append(fragment);
    } catch {
      host.replaceChildren();
    }
  }

  open(mode: string | null = null, query: string | null = null): void {
    const modeEl = requireSelect('searchMode');
    const scopeEl = requireSelect('searchScope');
    const inputEl = requireInput('searchInput');
    const advEl = requireTextArea('searchAdvancedInput');
    if (mode !== null) modeEl.value = mode;
    if (query !== null) {
      inputEl.value = query;
      advEl.value = query;
    }
    if (this.s.searchSession && mode === null && query === null) {
      modeEl.value = this.s.searchSession.mode;
      scopeEl.value = this.s.searchSession.scope;
      inputEl.value = this.s.searchSession.input;
      advEl.value = this.s.searchSession.input;
      this.displaySummary(this.s.searchSession.summary || localize('ui.searchPanel.searchResultsRetained'));
      if (!this.pages) {
        this.renderResults({
          matches: this.s.searchSession.matches,
          mode: this.s.searchSession.mode,
          status: this.s.searchSession.status,
        });
      }
    }
    if (!this.s.searchSession && mode === null && query === null) {
      modeEl.value = 'value';
      requireEl('searchSummary').hidden = true;
      requireEl('searchResultsPane').hidden = true;
    }
    const dialog = requireDialog('searchDialog');
    if (!dialog.open) dialog.show(); // Non-modal: grid remains clickable.
    this.syncMode();
    this.updateNavButtons();
    setTimeout(() => {
      (modeEl.value === 'advanced' ? advEl : inputEl).focus();
    }, 20);
    if (query !== null && query !== '') void this.runSearch();
  }

  async poll(
    jobID: string,
    requestID: number,
    alreadyCached: boolean,
    activity: ActivityHandle | null = beginActivity(localize('ui.common.searching'), { slot: 'searchActivity' }),
  ): Promise<void> {
    this.s.searchActivity = activity;
    try {
      while (this.s.searchJob === jobID && requestID === this.s.searchRequestID) {
        if (!alreadyCached) await this.delay(350);
        let data: SearchStatusApiResponse;
        try {
          data = await this.api(
            `/api/search/status?id=${encodeURIComponent(jobID)}`,
            undefined,
            decodeSearchStatusApiResponse,
          );
        } catch (error: unknown) {
          if (requestID !== this.s.searchRequestID) return;
          this.displaySummary(errorMessage(error));
          this.s.searchJob = null;
          if (this.s.searchSession) this.s.searchSession.status = 'error';
          requireButton('searchBtn').disabled = false;
          requireButton('cancelSearchBtn').hidden = true;
          return;
        }
        if (requestID !== this.s.searchRequestID || this.s.searchJob !== jobID) return;
        activity?.report(data.progress, 100, data.stage || localize('ui.common.searching'));
        if (data.full) {
          if (this.s.searchSession) this.s.searchSession.status = data.status;
          requireInput('searchFull').checked = true;
          const count = data.hitCount ?? 0;
          if (data.status === 'running') {
            this.displaySummary(localize('ui.searchPanel.sentencese87eb4f', { stage: data.stage || localize('ui.searchPanel.fullTextSearch'), count }));
            continue;
          }
          this.s.searchJob = null;
          requireButton('searchBtn').disabled = false;
          requireButton('cancelSearchBtn').hidden = true;
          if (data.status === 'done') {
            this.pages = {
              id: jobID,
              total: count,
              download: data.downloadUrl ?? '',
              previous: [],
              after: 0,
              next: null,
              text: '',
              sheet: '',
            };
            this.displaySummary(localize('ui.searchPanel.thereTotalMatches', { count }));
            this.buildSheetChoices(this.s.sheets.map((x) => ({ sheet: x.name })));
            await this.loadPage('reset');
          } else {
            this.displaySummary(data.error || localize('ui.searchPanel.fullSearchCanceled'));
          }
          return;
        }
        const matches = data.matches ? [...data.matches] : [];
        const session = this.s.searchSession;
        const prevMatches = session?.matches ?? [];
        const changed =
          prevMatches.length !== matches.length ||
          (prevMatches.length > 0 &&
            matches.length > 0 &&
            prevMatches.at(-1)?.ref !== matches.at(-1)?.ref);
        if (session) {
          session.matches = matches;
          session.status = data.status;
          session.truncated = data.truncated;
        }
        if (changed || data.status !== 'running') {
          this.buildHits(matches);
          this.renderResults(data);
          this.applyHighlightClasses();
        }
        if (data.status === 'running') {
          if (data.scope === 'all') {
            this.displaySummary(
              localize('ui.searchPanel.items80ed62a', { progress: (data.progress || 0).toFixed(0), stage: data.stage || localize('ui.searchPanel.searching'), count: matches.length }),
            );
          } else {
            this.displaySummary(
              localize('ui.searchPanel.rowsItems', { progress: (data.progress || 0).toFixed(0), scanned: data.scanned || 0, total: data.total || 0, count: matches.length }),
            );
          }
          continue;
        }
        if (data.status === 'done') {
          if (session) {
            this.s.searchCache.set(session.key, {
              matches,
              truncated: data.truncated,
            });
            if (this.s.searchCache.size > 32) {
              const oldest = this.s.searchCache.keys().next();
              if (!oldest.done) this.s.searchCache.delete(oldest.value);
            }
          }
          const elapsed = alreadyCached
            ? ` ${localize('ui.searchPanel.localCache')}`
            : ` ${localize('ui.searchPanel.seconds', { durationMs: ((data.durationMs || 0) / 1000).toFixed(2) })}`;
          this.displaySummary(
            localize('ui.searchPanel.itemsMatched', { count: matches.length, detail: data.truncated ? localize('ui.searchPanel.displayUpItems') : '', elapsed }),
          );
          if (!alreadyCached) {
            this.refreshHistory().catch(() => {
              // Ignore history refresh failure after successful search.
            });
          }
        } else if (data.status === 'cancelled') {
          this.displaySummary(localize('ui.searchPanel.searchCanceledFoundResultsStill'));
        } else {
          this.displaySummary(data.error || localize('ui.searchPanel.searchFailed'));
        }
        this.s.searchJob = null;
        requireButton('searchBtn').disabled = false;
        requireButton('cancelSearchBtn').hidden = true;
        return;
      }
    } finally {
      activity?.end();
    }
  }

  applyHighlightClasses(): void {
    document.querySelectorAll<HTMLElement>('.grid-region .grid-cell').forEach((node) => {
      node.classList.toggle('search-match', this.isMatch(this.s.sheet, node.dataset.ref));
    });
  }

  makeResultItem(match: SearchHit, index: number, mode: string | undefined): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `result-item${index === this.s.searchSelectedIndex ? ' active' : ''}`;
    button.dataset.matchIndex = String(index);
    button.setAttribute('role', 'option');
    button.setAttribute('aria-selected', String(index === this.s.searchSelectedIndex));
    const sheet = document.createElement('span');
    sheet.className = 'result-sheet';
    const sheetName = match.sheet || this.s.searchSession?.sheet || this.s.sheet || '';
    sheet.textContent = sheetName;
    sheet.title = sheetName;
    const ref = document.createElement('span');
    ref.className = 'result-ref';
    ref.textContent = match.ref;
    const snippet = document.createElement('span');
    snippet.className = 'result-snippet';
    const snippetText = (mode === 'value' ? match.value : match.formula) ?? '';
    snippet.textContent = snippetText;
    snippet.title = snippetText;
    button.append(sheet, ref, snippet);
    return button;
  }

  leavePages(): void {
    this.pages = null;
    ++this.pageSerial;
    this.pageAbort?.abort();
    if (this.filterTimer !== undefined) {
      clearTimeout(this.filterTimer);
      this.filterTimer = undefined;
    }
    requireEl('searchPaging').hidden = true;
    const copyBtn = requireButton('copySearchResultsBtn');
    copyBtn.title = localize('ui.searchPanel.copyResults');
    copyBtn.setAttribute('aria-label', localize('ui.searchPanel.copyResults'));
  }

  async loadPage(move: 'reset' | 'next' | 'prev'): Promise<void> {
    const state = this.pages;
    if (!state) return;
    const text = requireInput('searchResultFilter').value;
    const sheet = requireSelect('searchResultSheet').value;
    // Cursors belong to the filter that produced them. A click during the
    // input debounce must start at the first matching record of the new query.
    let activeMove = move;
    if (text !== state.text || sheet !== state.sheet) activeMove = 'reset';
    const previous = state.previous;
    let after = 0;
    if (activeMove === 'next') {
      if (state.next === null) return;
      after = state.next;
    } else if (activeMove === 'prev') {
      const prevCursor = previous.at(-1);
      if (prevCursor === undefined) return;
      after = prevCursor;
    }
    if (this.filterTimer !== undefined) {
      clearTimeout(this.filterTimer);
      this.filterTimer = undefined;
    }
    const serial = ++this.pageSerial;
    this.pageAbort?.abort();
    const controller = new AbortController();
    this.pageAbort = controller;
    const params = new URLSearchParams({ id: state.id, after: String(after), text, sheet });
    try {
      const page = await this.api(`/api/search/page?${params.toString()}`, {
        signal: controller.signal,
      }, decodeSearchPageApiResponse);
      if (serial !== this.pageSerial || state !== this.pages) return;
      if (activeMove === 'next') previous.push(state.after);
      else if (activeMove === 'prev') previous.pop();
      else previous.length = 0;
      state.text = text;
      state.sheet = sheet;
      state.after = after;
      state.next = page.more ? page.next : null;

      const matches = page.matches ? [...page.matches] : [];
      if (this.s.searchSession) {
        this.s.searchSession.matches = matches;
      }
      this.s.searchSelectedIndex = -1;
      this.s.visibleSearchIndexes = matches.map((_, i) => i);
      this.positions = new Map(matches.map((_, i) => [i, i]));
      this.renderedCount = 0;
      this.renderedData = { matches, mode: this.s.searchSession?.mode };
      const box = requireEl('searchResults');
      box.replaceChildren();
      this.appendResultChunk(matches.length);
      box.scrollTop = 0;
      this.buildHits(matches);
      this.applyHighlightClasses();
      this.updateNavButtons();
      requireEl('searchResultsPane').hidden = false;
      requireEl('searchPaging').hidden = false;
      requireEl('searchVisibleCount').hidden = true;
      requireButton('searchPagePrev').disabled = !previous.length;
      requireButton('searchPageNext').disabled = !page.more;
      requireEl('searchPageCount').textContent =
        localize('ui.searchPanel.pageRowColumn', { count: previous.length + 1, count2: matches.length, detail: page.previewClipped ? ` ${localize('ui.searchPanel.longTextPreviewShortened')}` : '' });
      const copyBtn = requireButton('copySearchResultsBtn');
      copyBtn.title = localize('ui.searchPanel.copyThisPagePreview');
      copyBtn.setAttribute('aria-label', localize('ui.searchPanel.copyThisPagePreview'));
    } catch (error: unknown) {
      if (serial === this.pageSerial && !(error instanceof Error && error.name === 'AbortError')) {
        this.toast(errorMessage(error), 'error');
      }
    }
  }

  appendResultChunk(targetCount = this.renderedCount + CHUNK_SIZE): void {
    const box = requireEl('searchResults');
    const data = this.renderedData;
    if (!data || this.renderedCount >= this.s.visibleSearchIndexes.length) return;
    const matches = data.matches ?? [];
    const end = Math.min(
      this.s.visibleSearchIndexes.length,
      Math.max(this.renderedCount, targetCount),
    );
    const fragment = document.createDocumentFragment();
    for (let i = this.renderedCount; i < end; i++) {
      const index = this.s.visibleSearchIndexes[i];
      if (index === undefined) continue;
      const match = matches[index];
      if (!match) continue;
      fragment.appendChild(this.makeResultItem(match, index, data.mode));
    }
    this.renderedCount = end;
    box.appendChild(fragment);
  }

  renderResults(data: SearchRenderData): void {
    const box = requireEl('searchResults');
    const matches = data.matches ?? [];
    requireEl('searchResultsPane').hidden = !matches.length;
    if (!matches.length) {
      this.positions.clear();
      this.s.visibleSearchIndexes = [];
      this.renderedCount = 0;
      this.renderedData = null;
      requireEl('searchVisibleCount').hidden = true;
      this.buildSheetChoices([]);
      box.textContent = data.status === 'done' ? localize('ui.searchPanel.noMatchingItemsFound') : localize('ui.common.searching39739fa');
      this.updateNavButtons();
      return;
    }
    this.buildSheetChoices(matches);
    this.s.visibleSearchIndexes = this.filteredIndexes(matches);
    const counter = requireEl('searchVisibleCount');
    counter.hidden = false;
    counter.textContent =
      this.s.visibleSearchIndexes.length === matches.length
        ? localize('ui.searchPanel.results', { count: matches.length })
        : localize('ui.searchPanel.displayResults', { count: this.s.visibleSearchIndexes.length, count2: matches.length });
    const prevScroll = box.scrollTop;
    this.renderedData = data;
    this.renderedCount = 0;
    if (!this.s.visibleSearchIndexes.length) {
      const empty = document.createElement('div');
      empty.className = 'history-empty';
      empty.textContent = localize('ui.searchPanel.noRecordsMatchingFilterConditions');
      box.replaceChildren(empty);
      this.updateNavButtons();
      return;
    }
    const selectedPos = this.positions.get(this.s.searchSelectedIndex) ?? -1;
    const initialCount = Math.max(CHUNK_SIZE, selectedPos >= 0 ? selectedPos + 20 : 0);
    box.replaceChildren();
    this.appendResultChunk(initialCount);
    box.scrollTop = prevScroll;
    this.updateNavButtons();
  }

  async goResult(index: number): Promise<void> {
    const matches = this.s.searchSession?.matches ?? [];
    if (!matches.length) return;
    const normalized = (index + matches.length) % matches.length;
    const match = matches[normalized];
    if (!match) return;
    const box = requireEl('searchResults');
    const oldPos = this.positions.get(this.s.searchSelectedIndex);
    const old = oldPos !== undefined ? box.children[oldPos] : undefined;
    if (old) {
      old.classList.remove('active');
      old.setAttribute('aria-selected', 'false');
    }
    this.s.searchSelectedIndex = normalized;
    if (this.s.searchSession) this.s.searchSession.selected = normalized;
    const pos = this.positions.get(normalized);
    if (pos !== undefined) {
      if (pos >= this.renderedCount) this.appendResultChunk(pos + 40);
      const nextNode = box.children[pos];
      if (nextNode) {
        nextNode.classList.add('active');
        nextNode.setAttribute('aria-selected', 'true');
        nextNode.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      }
    }
    // Keep panel and all search results alive while the workbook switches sheets.
    const destination = match.sheet || this.s.searchSession?.sheet || this.s.sheet || '';
    try {
      await this.navigateToCell(destination, match.ref);
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'error');
    }
  }

  step(delta: number): void {
    const visible = this.s.visibleSearchIndexes;
    if (!visible.length) {
      this.open();
      return;
    }
    const at = this.positions.get(this.s.searchSelectedIndex) ?? -1;
    const next =
      at < 0 ? (delta > 0 ? 0 : visible.length - 1) : (at + delta + visible.length) % visible.length;
    const target = visible[next];
    if (target !== undefined) void this.goResult(target);
  }

  bind(): void {
    requireButton('searchPagePrev').addEventListener('click', () => {
      void this.loadPage('prev');
    });
    requireButton('searchPageNext').addEventListener('click', () => {
      void this.loadPage('next');
    });
    requireButton('searchOpenBtn').addEventListener('click', () => this.open());
    requireButton('searchBtn').addEventListener('click', () => {
      void this.runSearch();
    });
    const historyBtn = requireButton('searchHistoryBtn');
    historyBtn.addEventListener('click', () => {
      const panel = requireEl('searchHistoryPanel');
      panel.hidden = !panel.hidden;
      historyBtn.setAttribute('aria-expanded', String(!panel.hidden));
      if (!panel.hidden) {
        this.refreshHistory().catch((error: unknown) => this.toast(errorMessage(error), 'error'));
      }
    });
    requireButton('searchHistoryRefreshBtn').addEventListener('click', () => {
      this.refreshHistory().catch((error: unknown) => this.toast(errorMessage(error), 'error'));
    });

    const results = requireEl('searchResults');
    results.addEventListener('click', (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const item = target?.closest<HTMLElement>('.result-item[data-match-index]');
      if (item) void this.goResult(Number(item.dataset.matchIndex));
    });
    results.addEventListener(
      'scroll',
      () => {
        if (
          this.renderedCount < this.s.visibleSearchIndexes.length &&
          results.scrollTop + results.clientHeight >= results.scrollHeight - 160
        ) {
          this.appendResultChunk();
        }
      },
      { passive: true },
    );

    requireInput('searchResultFilter').addEventListener('input', () => {
      if (this.filterTimer !== undefined) {
        clearTimeout(this.filterTimer);
        this.filterTimer = undefined;
      }
      if (this.pages) {
        ++this.pageSerial;
        this.pageAbort?.abort();
        this.filterTimer = setTimeout(() => {
          void this.loadPage('reset');
        }, 100);
        return;
      }
      const matches = this.s.searchSession?.matches ?? [];
      const apply = (): void =>
        this.renderResults({
          matches: this.s.searchSession?.matches ?? [],
          mode: this.s.searchSession?.mode,
          status: this.s.searchSession?.status,
        });
      if (matches.length <= 250) apply();
      else this.filterTimer = setTimeout(apply, 80);
    });

    requireSelect('searchResultSheet').addEventListener('change', () => {
      if (this.filterTimer !== undefined) {
        clearTimeout(this.filterTimer);
        this.filterTimer = undefined;
      }
      if (this.pages) {
        void this.loadPage('reset');
        return;
      }
      this.renderResults({
        matches: this.s.searchSession?.matches ?? [],
        mode: this.s.searchSession?.mode,
        status: this.s.searchSession?.status,
      });
    });

    requireButton('copySearchResultsBtn').addEventListener('click', () => this.copyVisible());
    requireButton('saveSearchResultsBtn').addEventListener('click', () => this.downloadVisible());
    requireButton('searchPrevBtn').addEventListener('click', () => this.step(-1));
    requireButton('searchNextBtn').addEventListener('click', () => this.step(1));
    requireButton('cancelSearchBtn').addEventListener('click', () => {
      void this.cancelSearch(this.s.searchJob);
    });
    requireSelect('searchMode').addEventListener('change', () => this.syncMode());

    const advInput = requireTextArea('searchAdvancedInput');
    advInput.addEventListener('input', () => this.validateAdvanced());
    advInput.addEventListener('keydown', (event: KeyboardEvent) => {
      if (!isComposing(event) && event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        void this.runSearch();
      }
    });
    requireButton('searchSyntaxHelp').addEventListener('click', () => {
      const help = requireEl('searchAdvancedHelp');
      help.hidden = !help.hidden;
    });
    const chips = requireEl('searchAdvancedSnippets');
    for (const [label, syntax] of SNIPPETS) {
      const b = document.createElement('button');
      b.type = 'button';
      b.textContent = label;
      b.title = syntax;
      b.onclick = (): void => this.insertAdvanced(syntax);
      chips.append(b);
    }
    requireInput('searchInput').addEventListener('keydown', (event: KeyboardEvent) => {
      if (!isComposing(event) && event.key === 'Enter') void this.runSearch();
    });
  }
}
