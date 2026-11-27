/**
 * Unified HTTP transport client assembling CSRF token, per-tab session lease,
 * foreground activity tracking, storage-recovery redirect, and compact wire unpacking.
 */
import type {
  ActivityHandle,
  ActivityService,
  ApiClient,
  ApiRequestOptions,
  ApiResponseType,
  HttpError,
  PostClient,
  SessionService,
} from '../contracts/common.ts';

export type { ApiClient, PostClient };
import type { WireRow, WorkbookStatusResponse } from '../contracts/workbook.ts';
import { readAppToken } from './dom.ts';
import { unpackWireRows } from './wire.ts';

export interface TransportService {
  readonly api: ApiClient;
  readonly post: PostClient;
  upload(
    file: File,
    activity: ActivityHandle | null | undefined,
    fmtBytes: (value: unknown) => string,
  ): Promise<WorkbookStatusResponse>;
}

export interface TransportDependencies {
  readonly session?: SessionService;
  readonly activity?: ActivityService;
  readonly token?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isWorkbookStatusResponse(value: unknown): value is WorkbookStatusResponse {
  return isRecord(value) && (typeof value.opened === 'boolean' || typeof value.archiveOnly === 'boolean');
}

export function createTransport(deps: TransportDependencies = {}): TransportService {
  const token = deps.token ?? readAppToken();

  async function apiImpl(
    url: string,
    options: ApiRequestOptions = {},
    responseType: ApiResponseType = 'json',
  ): Promise<unknown> {
    const activity = deps.activity?.request(url, options);
    try {
      await deps.session?.ready;
      const headers = new Headers(options.headers ?? {});
      headers.set('X-ExcelLens-Token', token);
      deps.session?.apply(headers);
      if (
        options.body &&
        typeof FormData !== 'undefined' &&
        !(options.body instanceof FormData) &&
        !headers.has('Content-Type')
      ) {
        headers.set('Content-Type', 'application/json');
      }
      const { activity: _omitActivity, ...fetchInit } = options;
      const response = await fetch(url, { ...fetchInit, headers, cache: 'no-store' });
      if (response.headers.get('X-ExcelLens-Recovery') === 'required') {
        window.location.replace(window.location.pathname);
      }
      if (responseType === 'response') return response;
      if (response.ok && responseType === 'text') return await response.text();
      if (response.ok && responseType === 'blob') return await response.blob();
      if (!response.ok) {
        const raw: unknown = await response.json().catch(() => null);
        const errMsg =
          isRecord(raw) && typeof raw.error === 'string' && raw.error
            ? raw.error
            : `请求失败（HTTP ${response.status}）`;
        const error: HttpError = new Error(errMsg);
        error.status = response.status;
        throw error;
      }
      const raw: unknown = await response.json().catch((error: unknown) => {
        if (error instanceof SyntaxError) {
          throw new Error('本机服务返回了无效的 JSON 数据', { cause: error });
        }
        throw error;
      });
      if (isRecord(raw) && raw.compact === 1 && Array.isArray(raw.rows)) {
        raw.rows = unpackWireRows(raw.rows as WireRow[]);
      }
      return raw;
    } finally {
      activity?.end();
    }
  }

  const api = apiImpl as ApiClient;

  const post: PostClient = <T = unknown>(
    url: string,
    payload: unknown,
    options: ApiRequestOptions = {},
  ): Promise<T> =>
    api<T>(url, { ...options, method: 'POST', body: JSON.stringify(payload) });

  function upload(
    file: File,
    activity: ActivityHandle | null | undefined,
    fmtBytes: (value: unknown) => string,
  ): Promise<WorkbookStatusResponse> {
    return new Promise<WorkbookStatusResponse>((resolve, reject) => {
      activity?.stage('正在读取文件', 0, 28);
      const form = new FormData();
      form.append('file', file, file.name);
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/open');
      xhr.setRequestHeader('X-ExcelLens-Token', token);
      deps.session?.apply(new Headers()).forEach((value, name) => xhr.setRequestHeader(name, value));
      xhr.upload.onprogress = (event: ProgressEvent): void => {
        if (event.lengthComputable) {
          activity?.report(
            event.loaded,
            event.total,
            '',
            `${fmtBytes(event.loaded)} / ${fmtBytes(event.total)}`,
          );
        }
      };
      xhr.upload.onload = (): void => {
        activity?.stage('正在解析文件', 28, 60);
      };
      xhr.onload = (): void => {
        let data: unknown;
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          // Surface malformed output as an open failure.
        }
        if (xhr.status >= 200 && xhr.status < 300 && isWorkbookStatusResponse(data)) {
          resolve(data);
        } else {
          const msg =
            isRecord(data) && typeof data.error === 'string' && data.error
              ? data.error
              : `无法打开文件（HTTP ${xhr.status}）`;
          reject(new Error(msg));
        }
      };
      xhr.onerror = (): void => reject(new Error('本机文件读取失败，请重新选择文件'));
      xhr.onabort = (): void => reject(new Error('读取已取消'));
      xhr.send(form);
    });
  }

  return Object.freeze({ api, post, upload });
}
