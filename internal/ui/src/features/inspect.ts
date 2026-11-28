/**
 * Selection inspector (Go To Special & single-column duplicates).
 * Performs a single read-only Go scan with bounded result previews and O(1)
 * decoration of mounted grid cells. Results remain navigable across sheets.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { InspectResponse } from '../contracts/features.ts';
import type { SearchHit } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import { currentFileFormats } from '../core/formats.ts';
import { MultiSelection } from '../grid/multi-selection.ts';
import type { WorkbookState } from '../grid/state.ts';
import { decodeInspectApiResponse } from '../core/http-decoders.generated.ts';

/** @httpResponse InspectApiResponse */
export interface InspectApiResponse extends Omit<InspectResponse, 'matches'> {
  readonly matches?: readonly SearchHit[] | null;
}

export interface InspectRequestOptions {
  sheet: string;
  mode: string;
  start: string;
  end: string;
  filterId: string;
  limit: number;
  visualStart?: number;
  visualEnd?: number;
  excludeThroughRow?: number;
  viewId?: string;
  skipThroughRow?: number;
}

export interface SelectionInspectorOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly colName: (col: number) => string;
  readonly parseColumn: (value: string | number | null | undefined) => number;
  readonly toast: ToastFn;
  readonly navigate: (sheet: string, ref: string) => Promise<unknown>;
  readonly onChange: () => void;
  readonly copy: (value: string) => Promise<boolean> | boolean | undefined;
  readonly rowLimit?: () => number;
  readonly colLimit?: () => number;
  readonly largeDataRows?: number;
  readonly excludeThroughRow?: () => number;
}

export class SelectionInspector {
  public matches: SearchHit[] = [];
  public hits = new Set<string>();
  public sheet = '';
  public current = -1;

  private readonly state: WorkbookState;
  private readonly api: ApiClient;
  private readonly colName: (col: number) => string;
  private readonly parseColumn: (value: string | number | null | undefined) => number;
  private readonly toast: ToastFn;
  private readonly navigate: (sheet: string, ref: string) => Promise<unknown>;
  private readonly onChange: () => void;
  private readonly copy: (value: string) => Promise<boolean> | boolean | undefined;
  private readonly rowLimit: () => number;
  private readonly colLimit: () => number;
  private readonly largeDataRows: number;
  private readonly excludeThroughRow: () => number;

  private abort: AbortController | null = null;
  private serial = 0;
  private showCount = 0;
  private readonly cache = new Map<string, InspectApiResponse>();

  constructor(options: SelectionInspectorOptions) {
    this.state = options.state;
    this.api = options.api;
    this.colName = options.colName;
    this.parseColumn = options.parseColumn;
    this.toast = options.toast;
    this.navigate = options.navigate;
    this.onChange = options.onChange;
    this.copy = options.copy;
    this.rowLimit = options.rowLimit ?? (() => Number.MAX_SAFE_INTEGER);
    this.colLimit = options.colLimit ?? (() => currentFileFormats()?.maxColumns ?? 16384);
    this.largeDataRows = options.largeDataRows ?? Number.MAX_SAFE_INTEGER;
    this.excludeThroughRow = options.excludeThroughRow ?? (() => 0);
    this.attach();
  }

  private attach(): void {
    requireButton('inspectClose').addEventListener('click', () => this.close());
    requireButton('inspectRun').addEventListener('click', () => {
      void this.run();
    });
    requireButton('inspectCancel').addEventListener('click', () => this.cancel());
    requireSelect('inspectMode').addEventListener('change', () => this.updateFields());
    requireSelect('inspectScope').addEventListener('change', () => this.updateFields());
    requireButton('inspectMore').addEventListener('click', () => this.appendMore());
    requireButton('inspectPrev').addEventListener('click', () => this.step(-1));
    requireButton('inspectNext').addEventListener('click', () => this.step(1));
    requireButton('inspectClear').addEventListener('click', () => this.clear());
    requireButton('inspectCopy').addEventListener('click', () => {
      void this.copyResults();
    });
    requireEl('inspectPanel').addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.close();
      }
    });
  }

  open(mode = 'formulas'): void {
    if (!this.state.indexReady) {
      this.toast(localize('ui.common.openWorksheetFirst'), 'warning');
      return;
    }
    if (this.state.multiRanges.length) {
      this.toast(localize('ui.inspect.selectContiguousAreaLocationCheck'), 'warning');
      return;
    }
    const range = this.state.range;
    if (
      mode === 'duplicates' &&
      this.state.largeData &&
      (!range || range.col1 !== range.col2 || Math.abs(range.row2 - range.row1) + 1 > this.largeDataRows)
    ) {
      this.toast(
        localize('ui.inspect.selectUpRowsOneColumn', { largeDataRows: this.largeDataRows.toLocaleString(interfaceLocale()) }),
        'warning',
      );
      return;
    }
    requireSelect('inspectMode').value = mode;
    const s = this.state;
    requireSelect('inspectScope').value = s.range ? 'selection' : 'used';
    requireInput('inspectColumn').value = this.colName(s.selected?.col || 1);
    requireInput('inspectSkipHeader').checked = Boolean(s.autoFilter?.row || s.usedMaxRow > 1);
    this.updateFields();
    requireEl('inspectPanel').hidden = false;
    requireButton('inspectRun').focus({ preventScroll: true });
  }

  close(): void {
    this.cancel();
    requireEl('inspectPanel').hidden = true;
  }

  updateFields(): void {
    const duplicates = requireSelect('inspectMode').value === 'duplicates';
    requireEl('inspectScopeWrap').hidden = duplicates;
    requireEl('inspectColumnWrap').hidden = !duplicates;
    requireEl('inspectSkipHeaderWrap').hidden = !duplicates;
    requireEl('inspectHint').textContent = '';
  }

  options(): InspectRequestOptions {
    const s = this.state;
    const mode = requireSelect('inspectMode').value;
    const scopeEl = requireSelect('inspectScope');
    const skipHeader = requireInput('inspectSkipHeader').checked;
    let row1 = 1;
    let col1 = 1;
    let row2 = Math.max(1, s.usedMaxRow);
    let col2 = Math.max(1, s.usedMaxCol);

    if (mode === 'duplicates') {
      const col = this.parseColumn(requireInput('inspectColumn').value);
      if (!col) throw new Error(localize('ui.inspect.enterValidColumnNameSuch'));
      col1 = col;
      col2 = col;
      if (s.range && s.range.col1 === s.range.col2 && s.range.col1 === col) {
        row1 = Math.min(s.range.row1, s.range.row2);
        row2 = Math.max(s.range.row1, s.range.row2);
      }
      if (skipHeader) {
        row1 = Math.max(row1, Math.min(row2, Math.max(2, (s.autoFilter?.row || 1) + 1)));
      }
    } else if (scopeEl.value === 'selection') {
      if (s.range) {
        row1 = Math.min(s.range.row1, s.range.row2);
        row2 = Math.max(s.range.row1, s.range.row2);
        col1 = Math.min(s.range.col1, s.range.col2);
        col2 = Math.max(s.range.col1, s.range.col2);
      } else if (s.selected) {
        // Excel's Go To Special on a single active cell considers the
        // worksheet's used range, not just that one cell.
        scopeEl.value = 'used';
      }
    }

    // Clamp to actual content extents, not the grid's optional blank margin.
    row2 = Math.min(row2, this.rowLimit());
    col2 = Math.min(col2, this.colLimit());
    const opt: InspectRequestOptions = {
      sheet: s.sheet ?? '',
      mode,
      start: `${this.colName(col1)}${row1}`,
      end: `${this.colName(col2)}${row2}`,
      filterId: s.filtered ? s.filterId : '',
      limit: 1200,
    };
    const selected = Boolean(
      s.range &&
        (mode === 'duplicates'
          ? s.range.col1 === col1 && s.range.col2 === col1
          : scopeEl.value === 'selection'),
    );
    if ((s.filtered || s.rowView) && selected && s.range) {
      const pts = MultiSelection.points(s.range, this.colName);
      opt.visualStart = pts.visualStart;
      opt.visualEnd = pts.visualEnd;
      opt.start = `${this.colName(col1)}${s.range.row1}`;
      opt.end = `${this.colName(col2)}${s.range.row2}`;
      opt.excludeThroughRow = this.excludeThroughRow();
      if (s.rowView) opt.viewId = s.rowView.id;
      if (mode === 'duplicates' && skipHeader) {
        opt.skipThroughRow = Math.max(1, s.autoFilter?.row || 1);
      }
    }
    return opt;
  }

  cancel(): void {
    const wasRunning = this.abort !== null;
    this.serial++;
    this.abort?.abort();
    this.abort = null;
    requireButton('inspectCancel').hidden = true;
    requireButton('inspectRun').disabled = false;
    const progress = requireEl('inspectProgress');
    if (wasRunning) {
      progress.textContent = localize('ui.inspect.thisCheckCanceled');
    }
  }

  async run(): Promise<void> {
    if (!this.state.indexReady) return;
    let opt: InspectRequestOptions;
    try {
      opt = this.options();
    } catch (e: unknown) {
      this.toast(errorMessage(e), 'warning');
      return;
    }
    this.cancel();
    const ticket = ++this.serial;
    const abort = new AbortController();
    this.abort = abort;
    const book = this.state.workbookKey;
    const current = (): boolean =>
      ticket === this.serial && !abort.signal.aborted && book === this.state.workbookKey;
    requireButton('inspectRun').disabled = true;
    requireButton('inspectCancel').hidden = false;
    requireEl('inspectProgress').textContent = localize('ui.inspect.checkingSourceData');
    try {
      const key = JSON.stringify([book, opt]);
      let res = this.cache.get(key);
      if (!res) {
        res = await this.api('/api/inspect', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(opt),
          signal: abort.signal,
        }, decodeInspectApiResponse);
        if (!current()) return;
        if (this.cache.size >= 10) {
          const oldest = this.cache.keys().next();
          if (!oldest.done) this.cache.delete(oldest.value);
        }
        this.cache.set(key, res);
      }
      if (!current()) return;
      this.sheet = opt.sheet;
      this.matches = res.matches ? [...res.matches] : [];
      this.hits = new Set(this.matches.map((x) => x.ref));
      this.current = -1;
      this.showCount = 0;
      const dupSummary =
        res.mode === 'duplicates' ? `${localize('ui.inspect.groupsDuplicates', { duplicateGroups: res.duplicateGroups || 0 })} ` : '';
      const truncSummary = res.truncated ? ` ${localize('ui.inspect.onlyFirstItemsListed', { count: this.matches.length })}` : '';
      requireEl('inspectProgress').textContent =
        localize('ui.inspect.totalCellsMilliseconds', { start: opt.start, end: opt.end, matched: (res.matched || 0).toLocaleString(interfaceLocale()), dupSummary, durationMs: res.durationMs || 0, truncSummary });
      requireEl('inspectResultList').replaceChildren();
      this.appendMore();
      this.onChange();
    } catch (e: unknown) {
      if (!(e instanceof Error && e.name === 'AbortError') && ticket === this.serial) {
        const msg = errorMessage(e);
        requireEl('inspectProgress').textContent = msg;
        this.toast(msg, 'error');
      }
    } finally {
      if (ticket === this.serial) {
        this.abort = null;
        requireButton('inspectRun').disabled = false;
        requireButton('inspectCancel').hidden = true;
      }
    }
  }

  appendMore(): void {
    const end = Math.min(this.matches.length, this.showCount + 100);
    const frag = document.createDocumentFragment();
    for (let i = this.showCount; i < end; i++) {
      const hit = this.matches[i];
      if (!hit) continue;
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'inspect-hit';
      btn.dataset.index = String(i);
      const ref = document.createElement('strong');
      ref.textContent = hit.ref;
      const preview = document.createElement('span');
      preview.textContent = hit.formula || hit.value || localize('ui.common.empty');
      btn.append(ref, preview);
      btn.title = hit.formula || hit.value || hit.ref;
      btn.addEventListener('click', () => {
        void this.navigateHit(i);
      });
      frag.append(btn);
    }
    const list = requireEl('inspectResultList');
    list.append(frag);
    this.showCount = end;
    requireButton('inspectMore').hidden = end >= this.matches.length;
    requireButton('inspectPrev').disabled = this.matches.length === 0;
    requireButton('inspectNext').disabled = this.matches.length === 0;
    if (!this.matches.length) {
      const empty = document.createElement('div');
      empty.className = 'inspect-empty';
      empty.textContent = localize('ui.inspect.noCellsThatMeetCriteria');
      list.append(empty);
    }
  }

  async copyResults(): Promise<void> {
    if (!this.matches.length) {
      this.toast(localize('ui.inspect.noMatchingResultsCopy'), 'warning');
      return;
    }
    const text = this.matches
      .map((hit) => `${this.sheet}!${hit.ref}\t${(hit.value ?? '').replace(/[\t\r\n]/g, ' ')}`)
      .join('\n');
    if ((await this.copy(text)) === false) return;
    this.toast(localize('ui.inspect.rowsResultsHaveCopiedTab', { count: this.matches.length }), 'success');
  }

  async navigateHit(index: number): Promise<void> {
    const hit = this.matches[index];
    if (!hit) return;
    this.current = index;
    const list = requireEl('inspectResultList');
    list.querySelectorAll('.inspect-hit.current').forEach((x) => x.classList.remove('current'));
    while (index >= this.showCount) this.appendMore();
    const element = list.querySelector(`.inspect-hit[data-index="${index}"]`);
    if (element) {
      element.classList.add('current');
      element.scrollIntoView({ block: 'nearest' });
    }
    try {
      await this.navigate(this.sheet, hit.ref);
    } catch (e: unknown) {
      this.toast(errorMessage(e), 'error');
    }
  }

  step(delta: number): void {
    if (!this.matches.length) return;
    void this.navigateHit((this.current + delta + this.matches.length) % this.matches.length);
  }

  clear(): void {
    this.cancel();
    this.sheet = '';
    this.matches = [];
    this.hits.clear();
    this.showCount = 0;
    this.current = -1;
    requireEl('inspectProgress').textContent = '';
    requireEl('inspectResultList').replaceChildren();
    requireButton('inspectMore').hidden = true;
    requireButton('inspectNext').disabled = true;
    requireButton('inspectPrev').disabled = true;
    this.onChange();
  }

  resetWorkbook(): void {
    this.cancel();
    this.clear();
    this.cache.clear();
  }

  decorateCell(element: Element, ref: string | undefined, sheet: string | null): void {
    element.classList.toggle(
      'inspect-highlight',
      Boolean(ref) && this.sheet === sheet && this.hits.has(ref ?? ''),
    );
  }
}
