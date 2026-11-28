/**
 * ExcelLens audit bookmarks: stored outside disposable caches.
 * No changes are made to the original workbook.
 */
import { t as localize } from '../core/i18n.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { Bookmark, BookmarkStore } from '../contracts/features.ts';
import type { SourceNote } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
  requireTextArea,
} from '../core/dom.ts';
import { confirmOverlay, type ConfirmOverlayOptions } from '../core/overlays.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeBookmarkStoreApiResponse,
  decodeSourceNotesResponse,
} from '../core/http-decoders.generated.ts';

export interface BookmarkNotePayload {
  readonly sheet: string;
  readonly ref: string;
  readonly text: string;
}

/** @httpResponse SourceNotesResponse */
export interface SourceNotesResponse {
  readonly items?: readonly SourceNote[] | null;
  readonly threaded?: boolean;
}

/** @httpResponse BookmarkStoreApiResponse */
export interface BookmarkStoreApiResponse extends Omit<BookmarkStore, 'items'> {
  readonly items?: readonly Bookmark[] | null;
}

export interface BookmarksOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly openDialog: (id: string) => void;
  readonly navigateToCell: (sheet: string, ref: string) => Promise<unknown>;
  readonly onChange: () => void;
  readonly canNotes?: () => boolean;
  readonly applyNotes?: (book: string, notes: readonly BookmarkNotePayload[]) => Promise<void> | void;
  readonly confirm?: (options: ConfirmOverlayOptions) => Promise<boolean>;
}

const quoteCsv = (value: string): string => `"${value.replaceAll('"', '""')}"`;

export class Bookmarks {
  public items: Bookmark[] = [];
  public keys = new Set<string>();
  public loadedBook = '';

  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly openDialog: (id: string) => void;
  private readonly navigate: (sheet: string, ref: string) => Promise<unknown>;
  private readonly onChange: () => void;
  private readonly canNotes: () => boolean;
  private readonly applyNotes?: (
    book: string,
    notes: readonly BookmarkNotePayload[],
  ) => Promise<void> | void;
  private readonly confirm: (options: ConfirmOverlayOptions) => Promise<boolean>;
  private readonly tagNames: ReadonlyMap<string, string>;

  private request = 0;
  private pending: AbortController | null = null;
  private saving = false;
  private targetBook = '';
  private readonly checked = new Set<string>();
  private noteSerial = 0;
  private noteController: AbortController | null = null;
  private notes: SourceNote[] = [];
  private noteThreaded = false;
  private noteSheet = '';
  private notePage = 0;

  constructor(options: BookmarksOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.openDialog = options.openDialog;
    this.navigate = options.navigateToCell;
    this.onChange = options.onChange;
    this.canNotes = options.canNotes ?? (() => false);
    this.applyNotes = options.applyNotes;
    this.confirm = options.confirm ?? ((opts) => confirmOverlay(opts));
    this.tagNames = new Map(Array.from(requireSelect('bookmarkTag').options, (option) => [option.value, option.text]));

    requireButton('bookmarkOpenBtn').addEventListener('click', () => this.open());
    requireButton('bookmarkSaveBtn').addEventListener('click', () => {
      void this.save();
    });
    requireButton('bookmarkExportBtn').addEventListener('click', () => this.exportCSV());
    requireButton('bookmarkSourceNotes').addEventListener('click', () => {
      void this.readNotes();
    });
    requireButton('bookmarkWriteNotes').addEventListener('click', () => {
      void this.writeNotes();
    });
    requireButton('sourceNotesPrev').addEventListener('click', () =>
      this.drawNotes(this.notePage - 1),
    );
    requireButton('sourceNotesNext').addEventListener('click', () =>
      this.drawNotes(this.notePage + 1),
    );
    requireInput('bookmarkSearch').addEventListener('input', () => this.render());
    requireButton('bookmarkRefreshBtn').addEventListener('click', () => {
      void this.load(true);
    });
  }

  async readNotes(): Promise<void> {
    const book = this.s.book;
    const sheet = this.s.sheet ?? '';
    const serial = ++this.noteSerial;
    this.noteController?.abort();
    const controller = new AbortController();
    this.noteController = controller;
    requireEl('sourceNotesPanel').hidden = false;
    this.notes = [];
    this.drawNotes(0);
    requireEl('sourceNotesStatus').textContent = localize('ui.bookmarks.readingAnnotations');
    try {
      const data = await this.api(
        `/api/notes?${new URLSearchParams({ book, sheet }).toString()}`,
        { signal: controller.signal },
        decodeSourceNotesResponse,
      );
      if (serial !== this.noteSerial || book !== this.s.book || sheet !== this.s.sheet) return;
      this.notes = data.items ? [...data.items] : [];
      this.noteThreaded = Boolean(data.threaded);
      this.noteSheet = sheet;
      this.drawNotes(0);
    } catch (error: unknown) {
      if (serial === this.noteSerial && !(error instanceof Error && error.name === 'AbortError')) {
        requireEl('sourceNotesStatus').textContent = errorMessage(error);
      }
    }
  }

  drawNotes(page: number): void {
    this.notePage = Math.max(0, page);
    const items = this.notes;
    const list = requireEl('sourceNotesList');
    list.replaceChildren();
    const start = this.notePage * 50;
    for (const item of items.slice(start, start + 50)) {
      const line = document.createElement('button');
      line.type = 'button';
      line.className = 'bookmark-visit';
      const label = document.createElement('strong');
      const body = document.createElement('span');
      label.textContent = `${item.ref} · ${item.author}`;
      body.className = 'bookmark-desc';
      body.style.whiteSpace = 'pre-wrap';
      body.textContent = item.text;
      line.append(label, body);
      line.onclick = (): void => {
        requireDialog('bookmarkDialog').close();
        void this.navigate(this.noteSheet, item.ref);
      };
      list.append(line);
    }
    const statusText =
      (items.length
        ? localize('ui.bookmarks.annotations', { start: start + 1, count: Math.min(start + 50, items.length), count2: items.length })
        : localize('ui.bookmarks.noAnnotationsOnThisSheet')) + (this.noteThreaded ? localize('ui.bookmarks.alsoIncludesConversationalAnnotations') : '');
    requireEl('sourceNotesStatus').textContent = statusText;
    requireButton('sourceNotesPrev').hidden = this.notePage <= 0;
    requireButton('sourceNotesNext').hidden = start + 50 >= items.length;
  }

  async writeNotes(): Promise<void> {
    const notes: BookmarkNotePayload[] = this.items
      .filter((item) => this.checked.has(item.id))
      .map((item) => ({
        sheet: item.sheet,
        ref: item.ref,
        text: [this.tagNames.get(item.tag) ?? item.tag, item.note].filter(Boolean).join('\n'),
      }));
    if (!notes.length) {
      this.toast(localize('ui.bookmarks.checkMarkersWantWrite'), 'warning');
      return;
    }
    const book = this.s.book;
    requireDialog('bookmarkDialog').close();
    try {
      await this.applyNotes?.(book, notes);
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'error');
    }
  }

  resetBook(): void {
    this.checked.clear();
    this.noteSerial++;
    this.noteController?.abort();
    this.notes = [];
    requireEl('sourceNotesPanel').hidden = true;
    ++this.request;
    this.pending?.abort();
    this.pending = null;
    this.saving = false;
    this.loadedBook = '';
    this.targetBook = '';
    this.items = [];
    this.keys.clear();
    requireButton('bookmarkSaveBtn').disabled = false;
    const dialog = requireDialog('bookmarkDialog');
    if (dialog.open) dialog.close();
  }

  accept(data: BookmarkStoreApiResponse, book: string): boolean {
    if (book !== this.s.workbookKey) return false;
    this.items = data.items ? [...data.items] : [];
    this.loadedBook = book;
    this.keys = new Set(this.items.map((x) => `${x.sheet}!${x.ref}`));
    requireButton('bookmarkOpenBtn').title = localize('ui.bookmarks.marked', { count: this.items.length });
    this.onChange();
    this.render();
    return true;
  }

  async load(force = false): Promise<boolean> {
    if (!this.s.opened || this.saving) return false;
    const book = this.s.workbookKey;
    if (!force && this.loadedBook === book) return true;
    this.pending?.abort();
    const ctrl = new AbortController();
    this.pending = ctrl;
    const ticket = ++this.request;
    try {
      const response = await this.api('/api/bookmarks', {
        signal: ctrl.signal,
      }, decodeBookmarkStoreApiResponse);
      if (ctrl.signal.aborted || ticket !== this.request) return false;
      return this.accept(response, book);
    } catch (e: unknown) {
      if (!ctrl.signal.aborted && ticket === this.request && book === this.s.workbookKey) {
        this.toast(localize('ui.bookmarks.markingReadingFailed', { errorMessage: errorMessage(e) }), 'error');
      }
      return false;
    } finally {
      if (this.pending === ctrl) this.pending = null;
    }
  }

  has(sheet: string, ref: string): boolean {
    return this.loadedBook === this.s.workbookKey && this.keys.has(`${sheet}!${ref}`);
  }

  open(): void {
    requireEl('bookmarkNoteActions').hidden = !this.canNotes();
    requireEl('sourceNotesPanel').hidden = true;
    this.noteThreaded = false;
    if (!this.s.sheet || !this.s.selected) {
      this.toast(localize('ui.bookmarks.selectCellsFirst'), 'warning');
      return;
    }
    this.prepareCurrent();
    this.render();
    this.openDialog('bookmarkDialog');
    const book = this.s.workbookKey;
    const target = `${this.s.sheet}!${this.s.selected.ref}`;
    const note = requireTextArea('bookmarkNote').value;
    const tag = requireSelect('bookmarkTag').value;
    void this.load(true).then((accepted) => {
      if (
        accepted &&
        book === this.s.workbookKey &&
        target === `${this.s.sheet ?? ''}!${this.s.selected?.ref ?? ''}`
      ) {
        this.refreshCurrent(note, tag);
      }
    });
  }

  prepareCurrent(): void {
    const cell = this.s.selected;
    if (!cell) return;
    this.targetBook = this.s.workbookKey;
    requireEl('bookmarkTarget').textContent =
      `${this.s.sheet ?? ''}!${cell.ref} · ${cell.display.slice(0, 90)}`;
    const existing = this.items.find((x) => x.sheet === this.s.sheet && x.ref === cell.ref);
    const noteEl = requireTextArea('bookmarkNote');
    noteEl.dataset.id = existing?.id ?? '';
    noteEl.value = existing?.note ?? '';
    requireSelect('bookmarkTag').value = existing?.tag ?? '待复核';
    requireButton('bookmarkSaveBtn').textContent = existing ? localize('ui.bookmarks.updateMarker') : localize('ui.bookmarks.addMarkings');
  }

  refreshCurrent(note: string, tag: string): boolean {
    const noteEl = requireTextArea('bookmarkNote');
    const tagEl = requireSelect('bookmarkTag');
    const draft = noteEl.value;
    const category = tagEl.value;
    const edited = draft !== note || category !== tag;
    this.prepareCurrent();
    if (edited) {
      noteEl.value = draft;
      tagEl.value = category;
    }
    return edited;
  }

  async save(): Promise<void> {
    if (this.saving) return;
    if (this.targetBook !== this.s.workbookKey) {
      this.toast(localize('ui.bookmarks.workbookSwitchedReopenMarkingProcess'), 'warning');
      return;
    }
    const cell = this.s.selected;
    if (!cell || !this.s.sheet) return;
    const noteEl = requireTextArea('bookmarkNote');
    const tagEl = requireSelect('bookmarkTag');
    const note = noteEl.value.trim();
    if (Array.from(note).length > 800) {
      this.toast(localize('ui.bookmarks.descriptionExcellensSoftwareInterfaceCannot'), 'warning');
      return;
    }
    const request = {
      action: 'save',
      id: noteEl.dataset.id || '',
      sheet: this.s.sheet,
      ref: cell.ref,
      tag: tagEl.value,
      note,
      value: cell.display.slice(0, 1500),
      formula: (cell.formula ?? '').slice(0, 6000),
    };
    const book = this.s.workbookKey;
    const ticket = ++this.request;
    this.pending?.abort();
    this.saving = true;
    requireButton('bookmarkSaveBtn').disabled = true;
    try {
      const data = await this.api('/api/bookmarks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(request),
      }, decodeBookmarkStoreApiResponse);
      if (ticket !== this.request || !this.accept(data, book)) return;
      const draftPending =
        request.sheet === this.s.sheet &&
        request.ref === this.s.selected?.ref &&
        this.refreshCurrent(note, request.tag);
      this.toast(
        draftPending ? localize('ui.bookmarks.previousRemarksHaveSavedCurrent') : localize('ui.bookmarks.savedMarker'),
        draftPending ? 'warning' : 'success',
      );
    } catch (err: unknown) {
      if (ticket === this.request && book === this.s.workbookKey) {
        this.toast(errorMessage(err), 'error');
      }
    } finally {
      if (ticket === this.request) {
        this.saving = false;
        requireButton('bookmarkSaveBtn').disabled = false;
      }
    }
  }

  private isSaving(): boolean {
    return this.saving;
  }

  async remove(bookmark: Bookmark): Promise<void> {
    const book = this.s.workbookKey;
    if (this.saving || this.loadedBook !== book) return;
    const confirmed = await this.confirm({
      title: localize('ui.bookmarks.deletionMarker'),
      message: localize('ui.bookmarks.removeMarkerFrom', { sheet: bookmark.sheet, ref: bookmark.ref }),
      confirmLabel: localize('ui.common.delete'),
      danger: true,
    });
    if (!confirmed) return;
    if (
      this.isSaving() ||
      this.s.workbookKey !== book ||
      this.loadedBook !== book ||
      !this.items.includes(bookmark)
    ) {
      return;
    }
    const note = requireTextArea('bookmarkNote').value;
    const tag = requireSelect('bookmarkTag').value;
    const ticket = ++this.request;
    this.pending?.abort();
    this.saving = true;
    requireButton('bookmarkSaveBtn').disabled = true;
    try {
      const data = await this.api('/api/bookmarks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: 'delete', id: bookmark.id }),
      }, decodeBookmarkStoreApiResponse);
      if (ticket !== this.request || !this.accept(data, book)) return;
      this.refreshCurrent(note, tag);
    } catch (e: unknown) {
      if (ticket === this.request && book === this.s.workbookKey) {
        this.toast(errorMessage(e), 'error');
      }
    } finally {
      if (ticket === this.request) {
        this.saving = false;
        requireButton('bookmarkSaveBtn').disabled = false;
      }
    }
  }

  render(): void {
    const q = requireInput('bookmarkSearch').value.trim().toLocaleLowerCase();
    const list = requireEl('bookmarkList');
    const frag = document.createDocumentFragment();
    let count = 0;
    for (const b of this.items) {
      if (
        q &&
        !`${b.sheet} ${b.ref} ${this.tagNames.get(b.tag) ?? b.tag} ${b.note} ${b.value ?? ''}`.toLocaleLowerCase().includes(q)
      ) {
        continue;
      }
      const row = document.createElement('div');
      row.className = 'bookmark-item';
      const main = document.createElement('button');
      main.type = 'button';
      main.className = 'bookmark-visit';
      const head = document.createElement('span');
      head.className = 'bookmark-head';
      head.textContent = `${b.sheet}!${b.ref}`;
      const tag = document.createElement('span');
      tag.className = 'bookmark-tag';
      tag.textContent = this.tagNames.get(b.tag) ?? b.tag;
      const desc = document.createElement('span');
      desc.className = 'bookmark-desc';
      desc.textContent = b.note || b.value || localize('ui.bookmarks.noDescription');
      main.append(head, tag, desc);
      main.addEventListener('click', () => {
        requireDialog('bookmarkDialog').close();
        void Promise.resolve(this.navigate(b.sheet, b.ref)).catch((e: unknown) => {
          this.toast(errorMessage(e), 'error');
        });
      });
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'bookmark-remove';
      del.textContent = '×';
      del.title = localize('ui.bookmarks.deleteThisMarker');
      del.addEventListener('click', () => {
        void this.remove(b);
      });
      if (this.canNotes()) {
        const check = document.createElement('input');
        check.type = 'checkbox';
        check.checked = this.checked.has(b.id);
        check.setAttribute('aria-label', localize('ui.bookmarks.selectMarkers', { sheet: b.sheet, ref: b.ref }));
        check.onchange = (): void => {
          if (check.checked) this.checked.add(b.id);
          else this.checked.delete(b.id);
        };
        row.prepend(check);
      }
      row.append(main, del);
      frag.append(row);
      count++;
    }
    if (!count) {
      const empty = document.createElement('div');
      empty.className = 'dialog-summary';
      empty.textContent = localize('ui.bookmarks.noMatchingMarkersFound');
      frag.append(empty);
    }
    list.replaceChildren(frag);
    requireEl('bookmarkCount').textContent = localize('ui.bookmarks.markersHaveSaved', { count: this.items.length });
  }

  exportCSV(): void {
    const headers = [
      localize('ui.bookmarks.worksheet'),
      localize('ui.common.cell'),
      localize('ui.common.classification'),
      localize('ui.bookmarks.notesComments'),
      localize('ui.bookmarks.saveValueSnapshot'),
      localize('ui.bookmarks.formulaSnapshot'),
      localize('ui.bookmarks.creationTime'),
      localize('ui.bookmarks.updateTime'),
    ];
    const rows = this.items.map((b) => [
      b.sheet,
      b.ref,
      b.tag,
      b.note,
      b.value ?? '',
      b.formula ?? '',
      b.created,
      b.updated,
    ]);
    const content =
      '\ufeff' +
      [headers, ...rows]
        .map((row) =>
          row
            .map((value) => {
              let s = value;
              if (/^\s*[=+\-@]/.test(s)) s = `'${s}`;
              return quoteCsv(s);
            })
            .join(','),
        )
        .join('\r\n');
    const url = URL.createObjectURL(new Blob([content], { type: 'text/csv;charset=utf-8' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = localize('ui.bookmarks.excellensAuditmarkingCsv');
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 30000);
    this.toast(localize('ui.bookmarks.markedListExported'), 'success');
  }
}
