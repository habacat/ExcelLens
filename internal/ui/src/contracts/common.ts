/**
 * Shared domain primitives, coordinate types, and common UI/API helpers.
 * Layer 0 (`contracts`) — type definitions only, zero runtime code.
 */

export type ToastTone = 'info' | 'success' | 'warning' | 'error';

export type ToastFn = (message: string, tone?: ToastTone) => void;

export type ApiResponseType = 'json' | 'text' | 'blob' | 'response';

export interface ApiRequestOptions extends Omit<RequestInit, 'headers'> {
  readonly headers?: HeadersInit;
  readonly activity?: boolean;
}

export interface HttpError extends Error {
  status?: number;
}

export interface ApiClient {
  <T = unknown>(url: string, options?: ApiRequestOptions, responseType?: 'json'): Promise<T>;
  (url: string, options: ApiRequestOptions | undefined, responseType: 'text'): Promise<string>;
  (url: string, options: ApiRequestOptions | undefined, responseType: 'blob'): Promise<Blob>;
  (url: string, options: ApiRequestOptions | undefined, responseType: 'response'): Promise<Response>;
}

export type PostClient = <T = unknown>(
  url: string,
  payload: unknown,
  options?: ApiRequestOptions,
) => Promise<T>;

export interface CellRef {
  readonly row: number;
  readonly col: number;
  readonly ref: string;
}

export interface CellPoint {
  readonly row: number;
  readonly col: number;
  readonly visual?: number;
}

export interface SelectionArea {
  readonly row1: number;
  readonly row2: number;
  readonly col1: number;
  readonly col2: number;
  readonly visual1: number;
  readonly visual2: number;
}

export interface MergeRange {
  readonly row: number;
  readonly col: number;
  readonly endRow: number;
  readonly endCol: number;
}

export interface AxisRange {
  min: number;
  max: number;
}

export interface AxisSizeRange {
  readonly min: number;
  readonly max: number;
  readonly size: number;
}

export type AsyncJobStatusKind = 'queued' | 'running' | 'done' | 'error' | 'cancelled';

export interface AsyncJobProgress {
  readonly id?: string;
  readonly jobId?: string;
  readonly status?: AsyncJobStatusKind;
  readonly stage?: string;
  readonly done?: number;
  readonly total?: number;
  readonly progress?: number;
  readonly scanned?: number;
  readonly doneRows?: number;
  readonly totalRows?: number;
  readonly doneBytes?: number;
  readonly totalBytes?: number;
  readonly error?: string;
}

export interface ActivityHandle {
  pause(): void;
  resume(): void;
  stage(name: string, from?: number, to?: number): void;
  report(done: number | undefined, total: number | undefined, name?: string, detail?: string): void;
  onCancel(action: (() => void) | null): void;
  paintIfDue(): Promise<void> | undefined;
  job(result: AsyncJobProgress): void;
  end(): void;
}

export interface ActivityBeginOptions {
  readonly priority?: number;
  readonly signal?: AbortSignal;
  readonly slot?: string | null;
}

export interface ActivityService {
  begin(label: string, options?: ActivityBeginOptions): ActivityHandle;
  request(url: string, options?: ApiRequestOptions): ActivityHandle | null;
}

export interface SessionService {
  readonly cacheCleared: boolean;
  readonly nativeLaunch: boolean;
  readonly restoring: boolean;
  readonly id: string;
  readonly ready: Promise<void>;
  returnHomeAfterCacheReset(): void;
  apply(headers: Headers, session?: string, documentID?: string): Headers;
}

export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | readonly JsonValue[] | JsonObject;
export interface JsonObject {
  readonly [key: string]: JsonValue;
}
