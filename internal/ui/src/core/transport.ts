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
  JsonDecoder,
  PostClient,
  SessionService,
} from '../contracts/common.ts';
import { decodeOpenResult } from './workbook-response.ts';
import type { WorkbookStatusResponse } from '../contracts/workbook.ts';
import { readAppToken } from './dom.ts';
import { interfaceLocale, languageHeader, t as localize } from './i18n.ts';

export type { ApiClient, PostClient };

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

export function createTransport(deps: TransportDependencies = {}): TransportService {
  const token = deps.token ?? readAppToken();

  function api<T>(url: string, options: ApiRequestOptions | undefined, decode: JsonDecoder<T>): Promise<T>;
  function api(url: string, options?: ApiRequestOptions, responseType?: 'json'): Promise<unknown>;
  function api(url: string, options: ApiRequestOptions | undefined, responseType: 'text'): Promise<string>;
  function api(url: string, options: ApiRequestOptions | undefined, responseType: 'blob'): Promise<Blob>;
  function api(url: string, options: ApiRequestOptions | undefined, responseType: 'response'): Promise<Response>;
  async function api(
    url: string,
    options: ApiRequestOptions = {},
    responseType: ApiResponseType | JsonDecoder<unknown> = 'json',
  ): Promise<unknown> {
    const activity = deps.activity?.request(url, options);
    try {
      await deps.session?.ready;
      options.signal?.throwIfAborted();
      const headers = new Headers(options.headers ?? {});
      headers.set('X-ExcelLens-Token', token);
      headers.set(languageHeader, interfaceLocale());
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
            : localize('ui.transport.requestFailedHttp', { status: response.status });
        const error: HttpError = new Error(errMsg);
        error.status = response.status;
        if (isRecord(raw) && typeof raw.code === 'string') error.code = raw.code;
        throw error;
      }
      const raw: unknown = await response.json().catch((error: unknown) => {
        if (error instanceof SyntaxError) {
          throw new Error(localize('ui.transport.localServiceReturnedInvalidJson'), { cause: error });
        }
        throw error;
      });
      options.signal?.throwIfAborted();
      return typeof responseType === 'function' ? responseType(raw) : raw;
    } finally {
      activity?.end();
    }
  }

  const post: PostClient = (
    url: string,
    payload: unknown,
    options: ApiRequestOptions = {},
  ): Promise<unknown> =>
    api(url, { ...options, method: 'POST', body: JSON.stringify(payload) });

  function upload(
    file: File,
    activity: ActivityHandle | null | undefined,
    fmtBytes: (value: unknown) => string,
  ): Promise<WorkbookStatusResponse> {
    return new Promise<WorkbookStatusResponse>((resolve, reject) => {
      activity?.stage(localize('ui.transport.readingFile'), 0, 28);
      const form = new FormData();
      form.append('file', file, file.name);
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/open');
      xhr.setRequestHeader('X-ExcelLens-Token', token);
      xhr.setRequestHeader(languageHeader, interfaceLocale());
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
        activity?.stage(localize('ui.transport.parsingFile'), 28, 60);
      };
      xhr.onload = (): void => {
        let data: unknown;
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          // Surface malformed output as an open failure.
        }
        if (xhr.status >= 200 && xhr.status < 300 && data !== undefined) {
          try {
            resolve(decodeOpenResult(data));
          } catch (error: unknown) {
            reject(error instanceof Error ? error : new Error(localize('ui.transport.invalidOpenResponse')));
          }
        } else {
          const msg =
            isRecord(data) && typeof data.error === 'string' && data.error
              ? data.error
              : localize('ui.transport.unableOpenFileHttp', { status: xhr.status });
          reject(new Error(msg));
        }
      };
      xhr.onerror = (): void => reject(new Error(localize('ui.transport.failedReadLocalFileSelect')));
      xhr.onabort = (): void => reject(new Error(localize('ui.transport.readingCanceled')));
      xhr.send(form);
    });
  }

  return Object.freeze({ api, post, upload });
}
