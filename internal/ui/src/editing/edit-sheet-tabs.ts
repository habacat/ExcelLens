/**
 * Sheet tab bar editing controls: add-sheet button, context menu for
 * new/copy/rename/delete sheet actions, and unique 31-char sheet naming.
 */
import { t as localize } from '../core/i18n.ts';
import type { ActivityHandle } from '../contracts/common.ts';
import type { EditSheetMeta, EditStructure } from '../contracts/edit.ts';
import { requireButton, requireEl } from '../core/dom.ts';

export interface EditSheetTabsOwner {
  readonly data: {
    readonly encoding?: string;
    readonly sheets: readonly EditSheetMeta[];
  } | null;
  readonly grid: {
    readonly active: boolean;
    readonly blocked: boolean;
    readonly view: {
      positionMenu(menu: HTMLElement, x: number, y: number): void;
      switchSheet(sheet: string): Promise<void> | void;
    };
    act(
      next: ((activity?: ActivityHandle | null) => Promise<boolean | undefined> | boolean | undefined) | null,
      confirmDraft?: boolean,
      label?: string,
    ): Promise<boolean>;
  };
  context(): { readonly sheet: string | null };
  advanced(tool?: string): unknown;
  commit(
    action: 'structure',
    changes: undefined,
    options: { readonly structure: EditStructure; readonly direct?: boolean },
  ): Promise<boolean | undefined>;
}

export class EditSheetTabs {
  private readonly editor: EditSheetTabsOwner;
  private source: HTMLElement | null = null;

  constructor(editor: EditSheetTabsOwner) {
    this.editor = editor;
    const tabs = requireEl('sheetTabs');
    const menu = requireEl('editSheetMenu');
    requireButton('editSheetAdd').addEventListener('click', () => {
      const sheet = editor.context().sheet;
      if (sheet) this.apply('newSheet', sheet);
    });
    const show = (event: Event, tab: Element | null, x: number, y: number): void => {
      if (
        !(tab instanceof HTMLElement) ||
        !editor.grid.active ||
        editor.data?.encoding ||
        editor.grid.blocked
      ) {
        return;
      }
      event.preventDefault();
      this.source = tab;
      const deleteBtn = menu.querySelector('[data-sheet-action="deleteSheet"]');
      if (deleteBtn instanceof HTMLButtonElement) {
        deleteBtn.disabled = (editor.data?.sheets.length ?? 0) < 2;
      }
      editor.grid.view.positionMenu(menu, x, y);
      const firstBtn = menu.querySelector('button');
      if (firstBtn instanceof HTMLElement) firstBtn.focus();
    };
    tabs.addEventListener('contextmenu', (event: MouseEvent) => {
      const target = event.target;
      show(
        event,
        target instanceof Element ? target.closest('[role="tab"]') : null,
        event.clientX,
        event.clientY,
      );
    });
    tabs.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key !== 'F10' || !event.shiftKey) return;
      const target = event.target;
      const tab = target instanceof Element ? target.closest('[role="tab"]') : null;
      if (!tab) return;
      const box = tab.getBoundingClientRect();
      show(event, tab, box.left, box.top);
    });
    menu.addEventListener('focusout', (e: FocusEvent) => {
      const related = e.relatedTarget;
      if (!(related instanceof Node) || !menu.contains(related)) menu.hidden = true;
    });
    menu.addEventListener('keydown', (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        menu.hidden = true;
        this.source?.focus();
      } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        const items = [...menu.querySelectorAll<HTMLButtonElement>('button:enabled')];
        if (!items.length) return;
        const active =
          document.activeElement instanceof HTMLButtonElement ? document.activeElement : null;
        const i = active ? items.indexOf(active) : -1;
        const next = items[(i + items.length + (e.key === 'ArrowDown' ? 1 : -1)) % items.length];
        next?.focus();
      } else {
        return;
      }
      e.preventDefault();
      e.stopPropagation();
    });
    const onMenuClick = async (event: MouseEvent): Promise<void> => {
      const target = event.target;
      const button = target instanceof Element ? target.closest('button') : null;
      const action = button instanceof HTMLButtonElement ? button.dataset.sheetAction : undefined;
      const tab = this.source;
      if (!action || !tab) return;
      menu.hidden = true;
      const sheet = tab.textContent;
      if (action === 'deleteSheet' || action === 'renameSheet') {
        if (sheet !== editor.context().sheet) await editor.grid.view.switchSheet(sheet);
        if (sheet === editor.context().sheet) editor.advanced(action);
      } else {
        this.apply(action, sheet);
      }
    };
    menu.addEventListener('click', (event: MouseEvent) => {
      void onMenuClick(event);
    });
  }

  nextName(kind: string, sheet: string): string {
    const names = new Set((this.editor.data?.sheets ?? []).map((s) => s.name.toLowerCase()));
    for (let n = kind === 'newSheet' ? 1 : 2; ; n++) {
      const suffix = kind === 'newSheet' ? String(n) : ` (${n})`;
      const prefix =
        kind === 'newSheet'
          ? 'Sheet'
          : sheet.slice(0, 31 - suffix.length).replace(/[\uD800-\uDBFF]$/, '');
      if (!names.has(`${prefix}${suffix}`.toLowerCase())) return `${prefix}${suffix}`;
    }
  }

  apply(kind: string, sheet: string): void {
    const g = this.editor.grid;
    if (!g.active || g.blocked || this.editor.data?.encoding) return;
    void g.act(
      () =>
        this.editor.commit('structure', undefined, {
          structure: { kind, sheet, name: this.nextName(kind, sheet) },
          direct: true,
        }),
      true,
      localize('ui.editSheetTabs.updatingWorksheets'),
    );
  }
}
