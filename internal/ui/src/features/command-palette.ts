/**
 * Keyboard-first command & worksheet palette (Ctrl+K).
 */
import { t as localize } from '../core/i18n.ts';
import type { ToastFn } from '../contracts/common.ts';
import type { SheetMeta } from '../contracts/workbook.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';

export interface ButtonCommandAction {
  readonly name: string;
  readonly button: string;
  readonly tags?: string;
  readonly shortcut?: string;
  readonly run?: undefined;
}

export interface CallbackCommandAction {
  readonly name: string;
  readonly button?: undefined;
  readonly tags?: string;
  readonly shortcut?: string;
  readonly run: () => unknown;
}

export type CommandAction = ButtonCommandAction | CallbackCommandAction;

export interface CommandPaletteOptions {
  readonly node: (id: string) => HTMLElement | null;
  readonly sheets: () => readonly SheetMeta[];
  readonly selectSheet: (name: string) => unknown;
  readonly canOpen: () => boolean;
  readonly openDialog: (id: string) => void;
  readonly closeDialog: (id: string) => void;
  readonly toast: ToastFn;
  readonly reveal?: (id: string) => HTMLElement | null | undefined;
}

export function buttonCommand(
  name: string,
  button: string,
  tags = '',
  shortcut = '',
): ButtonCommandAction {
  return { name, button, tags, shortcut };
}

export function windowStart(total: number, index: number, limit: number): number {
  return Math.max(0, Math.min(index - Math.floor(limit / 2), total - limit));
}

function isDisabledElement(target: HTMLElement): boolean {
  if (
    target instanceof HTMLButtonElement ||
    target instanceof HTMLInputElement ||
    target instanceof HTMLSelectElement ||
    target instanceof HTMLTextAreaElement
  ) {
    return target.disabled;
  }
  return false;
}

export class CommandPalette {
  public readonly actions: readonly CommandAction[];
  public visible: CommandAction[] = [];
  public index = 0;

  private readonly node: (id: string) => HTMLElement | null;
  private readonly sheets: () => readonly SheetMeta[];
  private readonly selectSheet: (name: string) => unknown;
  private readonly canOpen: () => boolean;
  private readonly openDialog: (id: string) => void;
  private readonly closeDialog: (id: string) => void;
  private readonly toast: ToastFn;
  private readonly reveal?: (id: string) => HTMLElement | null | undefined;

  constructor(actions: readonly CommandAction[], options: CommandPaletteOptions) {
    this.actions = actions;
    this.node = options.node;
    this.sheets = options.sheets;
    this.selectSheet = options.selectSheet;
    this.canOpen = options.canOpen;
    this.openDialog = options.openDialog;
    this.closeDialog = options.closeDialog;
    this.toast = options.toast;
    this.reveal = options.reveal;
  }

  private searchInput(): HTMLInputElement | null {
    const el = this.node('commandSearch');
    return el instanceof HTMLInputElement ? el : null;
  }

  bind(): void {
    const search = this.searchInput();
    if (!search) return;
    search.addEventListener('input', () => {
      this.index = 0;
      this.render();
    });
    search.addEventListener('keydown', (event: KeyboardEvent) => {
      if (isComposing(event)) return;
      if (event.key === 'Enter') {
        event.preventDefault();
        void this.run(this.visible[this.index]);
      } else if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
        event.preventDefault();
        const next = this.index + (event.key === 'ArrowDown' ? 1 : -1);
        this.index = Math.max(0, Math.min(this.visible.length - 1, next));
        this.render();
      }
    });
  }

  unavailable(action: CommandAction | undefined): boolean {
    if (!action?.button) return false;
    const target = this.node(action.button);
    return !target || isDisabledElement(target) || Boolean(target.hidden);
  }

  async run(action: CommandAction | undefined): Promise<void> {
    if (!action || this.unavailable(action)) return;
    this.closeDialog('commandDialog');
    try {
      await Promise.resolve().then(() => {
        if (this.unavailable(action)) return undefined;
        if (!action.button) return action.run?.();
        // Bring the control's tab forward first, so its menu has an anchor.
        this.reveal?.(action.button);
        this.node(action.button)?.click();
        return undefined;
      });
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'error');
    }
  }

  open(): void {
    if (!this.canOpen()) return;
    this.index = 0;
    const input = this.searchInput();
    if (!input) return;
    input.value = '';
    this.render();
    this.openDialog('commandDialog');
    input.focus({ preventScroll: true });
  }

  // Every word must appear in the name, search words or shortcut. Commands
  // whose own name matches come first: exact, then prefix, then anywhere.
  matches(query: string): CommandAction[] {
    const terms = query.split(/\s+/).filter(Boolean);
    const firstTerm = terms[0];
    if (!firstTerm) return [...this.actions];
    const ranked: { readonly action: CommandAction; readonly rank: number; readonly order: number }[] =
      [];
    for (const [order, action] of this.actions.entries()) {
      const name = action.name.toLocaleLowerCase();
      const all = [action.name, action.tags ?? '', action.shortcut ?? '']
        .join(' ')
        .toLocaleLowerCase();
      if (!terms.every((term) => all.includes(term))) continue;
      const rank =
        name === query
          ? 0
          : name.startsWith(firstTerm)
            ? 1
            : terms.every((term) => name.includes(term))
              ? 2
              : 3;
      ranked.push({ action, rank, order });
    }
    return ranked
      .sort((a, b) => a.rank - b.rank || a.order - b.order)
      .map((item) => item.action);
  }

  render(): void {
    const search = this.searchInput();
    const list = this.node('commandList');
    if (!search || !list) return;
    const query = search.value.toLocaleLowerCase().trim();
    this.visible = this.matches(query);
    if (query) {
      for (const sheet of this.sheets()) {
        if (sheet.name.toLocaleLowerCase().includes(query)) {
          this.visible.push({
            name: localize('ui.commandPalette.openWorksheet', { name: sheet.name }),
            run: () => this.selectSheet(sheet.name),
          });
        }
      }
    }
    this.index = Math.min(this.index, Math.max(0, this.visible.length - 1));
    const limit = Math.max(60, this.actions.length);
    const start = windowStart(this.visible.length, this.index, limit);
    const doc = list.ownerDocument;
    const fragment = doc.createDocumentFragment();
    for (const [offset, action] of this.visible.slice(start, start + limit).entries()) {
      const selected = start + offset === this.index;
      const item = doc.createElement('button');
      item.type = 'button';
      item.className = `command-entry${selected ? ' active' : ''}`;
      item.setAttribute('role', 'option');
      item.setAttribute('aria-selected', String(selected));
      item.disabled = this.unavailable(action);
      if (action.button) item.dataset.commandButton = action.button;
      const label = doc.createElement('span');
      const shortcut = doc.createElement('span');
      label.textContent = action.name;
      shortcut.className = 'command-shortcut';
      shortcut.textContent = action.shortcut ?? '';
      item.append(label, shortcut);
      item.addEventListener('click', () => {
        void this.run(action);
      });
      fragment.append(item);
    }
    if (!this.visible.length) {
      const empty = doc.createElement('div');
      empty.className = 'command-empty';
      empty.textContent = localize('ui.commandPalette.noMatchingFunctionsOrWorksheets');
      fragment.append(empty);
    }
    list.replaceChildren(fragment);
    list.querySelector('.active')?.scrollIntoView({ block: 'nearest' });
  }
}
