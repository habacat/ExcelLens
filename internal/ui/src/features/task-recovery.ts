/**
 * Persistent task recovery dialog and home-screen indicator.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { TaskCheckpointInfo } from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl } from '../core/dom.ts';
import type { RecoveredTaskStart } from '../grid/sheet-controller.ts';
import {
  TaskRecoveryModel,
  type TaskRecoveryAction,
  type TaskRecoveryBookState,
  type TaskRecoveryRequestPayload,
} from './task-recovery-model.ts';
import { decodeRecoveredTaskStart } from '../core/http-decoders.generated.ts';

export type TaskRecoveryRunAction = Exclude<TaskRecoveryAction, ''> | 'remove';

export interface TaskRecoveryTimers {
  readonly setTimeout: (fn: () => void, ms: number) => unknown;
  readonly clearTimeout: (id: unknown) => void;
}

export interface TaskRecoveryOptions {
  readonly state: TaskRecoveryBookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly openDialog: (id: string) => void;
  readonly maximum: number;
  readonly openSource: (
    payload: TaskRecoveryRequestPayload,
    current: () => boolean,
  ) => Promise<void>;
  readonly adopt: (
    start: RecoveredTaskStart,
    item: TaskCheckpointInfo,
  ) => Promise<boolean>;
  readonly canResume: (item: TaskCheckpointInfo, action: Exclude<TaskRecoveryAction, ''>) => boolean;
  readonly context?: () => string;
  readonly timers?: TaskRecoveryTimers;
}

const ACTION_LABELS: Readonly<Record<Exclude<TaskRecoveryAction, ''>, string>> = {
  open: '打开原文件',
  resume: '继续',
  download: '下载',
};

export class TaskRecovery {
  private readonly s: TaskRecoveryBookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly openDialog: (id: string) => void;
  private readonly maximum: number;
  private readonly openSource: TaskRecoveryOptions['openSource'];
  private readonly adopt: TaskRecoveryOptions['adopt'];
  private readonly canResume: TaskRecoveryOptions['canResume'];
  private readonly context: () => string;
  private readonly timers: TaskRecoveryTimers;

  public items: TaskCheckpointInfo[] = [];
  private serial = 0;
  private listSerial = 0;
  public busy = false;
  public opening = false;
  private pending: AbortController | null = null;
  private homeTimer: unknown = undefined;

  constructor(options: TaskRecoveryOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.openDialog = options.openDialog;
    this.maximum = options.maximum;
    this.openSource = options.openSource;
    this.adopt = options.adopt;
    this.canResume = options.canResume;
    this.context = options.context ?? (() => '');
    this.timers = options.timers ?? {
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (id) => {
        if (id !== undefined) clearTimeout(id as ReturnType<typeof setTimeout>);
      },
    };

    requireButton('taskRecoveryRefresh').addEventListener('click', () => {
      void this.load();
    });
    requireButton('taskRecoveryClose').addEventListener('click', () => this.close());
    const dialog = requireDialog('taskRecoveryDialog');
    dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    dialog.addEventListener('close', () => {
      if (dialog.open) return;
      ++this.serial;
      ++this.listSerial;
      this.pending?.abort();
      void this.refreshHome();
    });
  }

  async open(): Promise<void> {
    this.openDialog('taskRecoveryDialog');
    await this.load();
  }

  refreshHome(): Promise<void> | undefined {
    if (
      !document.hidden &&
      !requireEl('welcome').hidden &&
      !requireDialog('taskRecoveryDialog').open
    ) {
      return this.load();
    }
    return undefined;
  }

  updateHome(): void {
    requireEl('taskRecoveryHome').hidden = !this.items.some((item) =>
      Boolean(TaskRecoveryModel.action(item, this.s)),
    );
  }

  close(): void {
    if (this.opening) return;
    requireDialog('taskRecoveryDialog').close();
  }

  status(text: string, error = false): void {
    const node = requireEl('taskRecoveryStatus');
    node.textContent = text;
    node.classList.toggle('is-error', error);
    node.hidden = !text;
  }

  controls(): void {
    requireButton('taskRecoveryRefresh').disabled = this.busy;
    requireButton('taskRecoveryClose').disabled = this.opening;
    for (const button of requireEl('taskRecoveryList').querySelectorAll('button')) {
      button.disabled = this.busy || button.dataset.unavailable === 'true';
    }
  }

  dispose(): void {
    this.timers.clearTimeout(this.homeTimer);
    this.homeTimer = undefined;
    this.pending?.abort();
    this.pending = null;
  }

  async load(): Promise<void> {
    if (this.busy) return;
    const dialog = requireDialog('taskRecoveryDialog');
    const inDialog = dialog.open;
    this.timers.clearTimeout(this.homeTimer);
    this.homeTimer = undefined;
    const ticket = ++this.listSerial;
    this.pending?.abort();
    const controller = new AbortController();
    this.pending = controller;
    if (inDialog) this.status('正在读取…');
    try {
      const data = await this.api('/api/tasks', { signal: controller.signal });
      if (
        ticket !== this.listSerial ||
        inDialog !== dialog.open ||
        (!inDialog && requireEl('welcome').hidden)
      ) {
        return;
      }
      this.items = TaskRecoveryModel.items(data, this.maximum);
      this.updateHome();
      if (inDialog) {
        this.render();
        this.status('');
      } else if (this.items.some((item) => item.busy)) {
        this.homeTimer = this.timers.setTimeout(() => {
          void this.refreshHome();
        }, 5000);
      }
    } catch (error: unknown) {
      if (ticket === this.listSerial && !(error instanceof Error && error.name === 'AbortError')) {
        requireEl('taskRecoveryHome').hidden = true;
        if (inDialog) this.status(errorMessage(error), true);
      }
    } finally {
      if (this.pending === controller) this.pending = null;
    }
  }

  render(): void {
    const list = requireEl('taskRecoveryList');
    const active = document.activeElement;
    const focusEl = active instanceof HTMLElement ? active.closest<HTMLElement>('[data-task-id]') : null;
    const focus = focusEl?.dataset.taskId;
    const activeAction = active instanceof HTMLElement ? active.dataset.action : undefined;
    const fragment = document.createDocumentFragment();
    if (!this.items.length) {
      const empty = document.createElement('p');
      empty.className = 'task-recovery-empty';
      empty.textContent = '暂无可恢复的任务';
      fragment.append(empty);
    }
    for (const item of this.items) {
      const row = document.createElement('div');
      row.className = 'task-recovery-row';
      row.dataset.taskId = item.id;
      const main = document.createElement('div');
      main.className = 'task-recovery-main';
      const heading = document.createElement('div');
      heading.className = 'task-recovery-heading';
      const kind = document.createElement('span');
      kind.className = 'task-recovery-kind';
      kind.textContent = TaskRecoveryModel.label(item);
      const name = document.createElement('strong');
      name.className = 'task-recovery-name';
      name.textContent = item.file || '无法读取的任务';
      name.title = item.file;
      heading.append(kind, name);
      const meta = document.createElement('div');
      meta.className = 'task-recovery-meta';
      const sheet = document.createElement('span');
      sheet.className = 'task-recovery-sheet';
      sheet.textContent = item.sheet;
      sheet.title = item.sheet;
      const time = document.createElement('time');
      const date = new Date(item.updated);
      if (!Number.isNaN(date.getTime())) {
        time.textContent = new Intl.DateTimeFormat('zh-CN', {
          month: '2-digit',
          day: '2-digit',
          hour: '2-digit',
          minute: '2-digit',
          hour12: false,
        }).format(date);
        time.dateTime = date.toISOString();
      }
      const status = document.createElement('span');
      status.textContent = item.busy
        ? '运行中'
        : item.error
          ? '不可恢复'
          : item.complete
            ? '已完成'
            : item.backup
              ? '已找回进度'
              : '已暂停';
      meta.append(sheet, time, status);
      main.append(heading, meta);
      if (item.error) {
        const error = document.createElement('p');
        error.className = 'task-recovery-error';
        error.textContent = item.error;
        main.append(error);
      }
      const actions = document.createElement('div');
      actions.className = 'task-recovery-actions';
      const add = (
        action: TaskRecoveryRunAction,
        label: string,
        unavailable = false,
      ): void => {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'mini-button';
        button.dataset.action = action;
        button.dataset.unavailable = String(unavailable);
        button.textContent = label;
        button.addEventListener('click', () => {
          void this.run(item, action);
        });
        actions.append(button);
      };
      const next = TaskRecoveryModel.action(item, this.s);
      if (next) add(next, ACTION_LABELS[next]);
      add('remove', '移除', item.busy);
      row.append(main, actions);
      fragment.append(row);
    }
    const scroll = list.scrollTop;
    list.replaceChildren(fragment);
    list.scrollTop = scroll;
    this.controls();
    if (focus && activeAction) {
      list
        .querySelector<HTMLElement>(`[data-task-id="${focus}"] [data-action="${activeAction}"]`)
        ?.focus({ preventScroll: true });
    }
  }

  private async cancelOwned(
    started: RecoveredTaskStart | null | undefined,
    item: TaskCheckpointInfo,
  ): Promise<void> {
    const operation = TaskRecoveryModel.operation(item);
    if (!operation || !started?.jobId) return;
    try {
      await this.api(`/api/${operation}/cancel`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: started.jobId }),
      });
    } catch {
      // A finished task remains in the persistent directory.
    }
  }

  async run(item: TaskCheckpointInfo, action: TaskRecoveryRunAction): Promise<void> {
    if (this.busy || item.busy || !this.items.includes(item)) return;
    if (action !== 'remove') {
      if (TaskRecoveryModel.action(item, this.s) !== action) return;
      if (!this.canResume(item, action)) return;
    }
    ++this.listSerial;
    this.pending?.abort();
    const dialog = requireDialog('taskRecoveryDialog');
    const ticket = ++this.serial;
    const identity = this.context();
    const current = (): boolean =>
      ticket === this.serial && identity === this.context() && dialog.open;
    this.busy = true;
    this.opening = action === 'open';
    this.controls();
    this.status(
      action === 'remove' ? '正在移除…' : action === 'open' ? '正在打开…' : '正在恢复…',
    );
    let started: RecoveredTaskStart | null = null;
    let claimed = false;
    try {
      const payload = TaskRecoveryModel.request(item, this.s);
      if (action === 'open') {
        await this.openSource(payload, current);
        if (ticket !== this.serial || !dialog.open) return;
        this.status('');
        this.render();
        return;
      }
      if (action === 'remove') {
        await this.api('/api/tasks/discard', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        });
        if (!current()) return;
        this.items = this.items.filter((x) => x !== item);
        this.updateHome();
        this.render();
        this.status('');
        return;
      }
      started = await this.api('/api/tasks/resume', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }, decodeRecoveredTaskStart);
      if (!current()) return;
      if (!started.jobId || started.operation !== TaskRecoveryModel.operation(item)) {
        throw new Error('恢复响应无效，请刷新任务列表');
      }
      dialog.close();
      claimed = await this.adopt(started, item);
    } catch (error: unknown) {
      if (ticket === this.serial && dialog.open) this.status(errorMessage(error), true);
      else if (started) this.toast(errorMessage(error), 'error');
    } finally {
      if (started && !claimed) await this.cancelOwned(started, item);
      this.busy = false;
      this.opening = false;
      this.controls();
      if (action === 'remove' && !dialog.open) void this.refreshHome();
    }
  }
}
