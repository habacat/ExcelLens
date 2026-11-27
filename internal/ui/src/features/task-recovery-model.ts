/**
 * Persistent task checkpoint validation and action resolution.
 * Metadata only: no source reads or automatic job starts.
 */
import type { TaskCheckpointInfo } from '../contracts/features.ts';

export type TaskOperationKind = 'search' | 'filter' | 'export' | '';
export type TaskRecoveryAction = 'open' | 'resume' | 'download' | '';

export interface TaskRecoveryBookState {
  readonly opened: boolean;
  readonly book: string;
  readonly workbookKey: string;
}

export interface TaskRecoveryRequestPayload {
  readonly id: string;
  readonly revision: string;
  readonly book: string;
  readonly workbookKey: string;
}

const MAX_UINT64 = 18446744073709551615n;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

export function taskOperation(item: Pick<TaskCheckpointInfo, 'driver'>): TaskOperationKind {
  if (item.driver === 'source-search') return 'search';
  if (item.driver === 'source-filter') return 'filter';
  if (item.driver === 'source-export') return 'export';
  return '';
}

function readRequiredString(raw: Record<string, unknown>, key: string): string {
  const value = raw[key];
  if (typeof value !== 'string') throw new Error('任务信息不完整，请刷新重试');
  return value;
}

function readRequiredBoolean(raw: Record<string, unknown>, key: string): boolean {
  const value = raw[key];
  if (typeof value !== 'boolean') throw new Error('任务状态无效，请刷新重试');
  return value;
}

export function validateTaskItems(data: unknown, maximum: number): TaskCheckpointInfo[] {
  if (!isRecord(data) || !Array.isArray(data.items) || data.items.length > maximum) {
    throw new Error('任务列表无效，请刷新重试');
  }
  const seen = new Set<string>();
  return data.items.map((raw: unknown): TaskCheckpointInfo => {
    if (!isRecord(raw)) throw new Error('任务列表无效，请刷新重试');
    const id = raw.id;
    const revision = raw.revision;
    if (
      typeof id !== 'string' ||
      !/^[a-f0-9]{32}$/.test(id) ||
      seen.has(id) ||
      typeof revision !== 'string' ||
      !/^\d+$/.test(revision) ||
      BigInt(revision) > MAX_UINT64
    ) {
      throw new Error('任务列表无效，请刷新重试');
    }
    seen.add(id);
    const driver = readRequiredString(raw, 'driver');
    const file = readRequiredString(raw, 'file');
    const path = readRequiredString(raw, 'path');
    const book = readRequiredString(raw, 'book');
    const workbookKey = readRequiredString(raw, 'workbookKey');
    const sheet = readRequiredString(raw, 'sheet');
    const query = readRequiredString(raw, 'query');
    const mode = readRequiredString(raw, 'mode');
    const scope = readRequiredString(raw, 'scope');
    const error = readRequiredString(raw, 'error');
    const busy = readRequiredBoolean(raw, 'busy');
    const complete = readRequiredBoolean(raw, 'complete');
    const backup = readRequiredBoolean(raw, 'backup');
    if (revision === '0' && !error) throw new Error('任务版本无效，请刷新重试');
    return {
      full: typeof raw.full === 'boolean' ? raw.full : undefined,
      id,
      revision,
      kind: typeof raw.kind === 'string' ? raw.kind : '',
      driver,
      title: typeof raw.title === 'string' ? raw.title : '',
      file,
      path,
      book,
      workbookKey,
      sheet,
      query,
      mode,
      scope,
      updated: typeof raw.updated === 'string' ? raw.updated : '',
      complete,
      busy,
      backup,
      error,
    };
  });
}

export function taskMatchesBook(
  item: Pick<TaskCheckpointInfo, 'book' | 'workbookKey'>,
  state: TaskRecoveryBookState,
): boolean {
  return state.opened && item.book === state.book && item.workbookKey === state.workbookKey;
}

export function taskRequestPayload(
  item: Pick<TaskCheckpointInfo, 'id' | 'revision'>,
  state: Pick<TaskRecoveryBookState, 'book' | 'workbookKey'>,
): TaskRecoveryRequestPayload {
  return {
    id: item.id,
    revision: item.revision,
    book: state.book || '',
    workbookKey: state.workbookKey || '',
  };
}

export function taskLabel(item: Pick<TaskCheckpointInfo, 'driver' | 'mode'>): string {
  const op = taskOperation(item);
  if (op === 'search') return '搜索';
  if (op === 'filter') return '筛选 / 排序';
  if (op === 'export') return `导出 ${item.mode.toUpperCase()}`;
  return '任务';
}

export function taskAction(
  item: Pick<TaskCheckpointInfo, 'driver' | 'busy' | 'error' | 'revision' | 'book' | 'workbookKey' | 'complete'>,
  state: TaskRecoveryBookState,
): TaskRecoveryAction {
  const op = taskOperation(item);
  if (item.busy || item.error || !op || item.revision === '0') return '';
  if (!taskMatchesBook(item, state)) return 'open';
  return item.complete && op === 'export' ? 'download' : 'resume';
}

export const TaskRecoveryModel = Object.freeze({
  items: validateTaskItems,
  operation: taskOperation,
  matches: taskMatchesBook,
  request: taskRequestPayload,
  label: taskLabel,
  action: taskAction,
});
