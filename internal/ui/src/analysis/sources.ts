/**
 * Runtime owners are separate from durable query scopes. A late response is
 * still observed so its own ID can be released, never the newer owner's ID.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  CompositionDefinition,
  CompositionTaskResult,
  SourceScope,
} from '../contracts/analysis.ts';
import type { ApiClient } from '../contracts/common.ts';
import type { SourceTaskContext, SourceTasks } from '../core/source-tasks.ts';
import type { WorkspaceAnalysisQuery } from './results.ts';
import {
  decodeAnalysisSourceBindingResponse,
  decodeCompositionTaskResult,
} from '../core/http-decoders.generated.ts';

export type AnalysisSourceMode = 'full' | 'filtered' | 'visible' | 'selection';

export interface GridSourceContextData {
  readonly workbookKey: string;
  readonly book: string;
  readonly source: string;
  readonly filterId: string;
  readonly viewId: string;
  readonly excludeThroughRow: number;
  readonly start?: string;
  readonly end?: string;
  readonly visualStart?: number;
  readonly visualEnd?: number;
}

export interface AnalysisSourceRecord {
  readonly id: string;
  readonly source: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly rows?: number;
  readonly sourceScope: SourceScope | null;
  readonly composition?: CompositionDefinition;
  readonly kind: 'composition' | 'scope';
  readonly identity: string;
  readonly current: () => boolean;
}

export interface AnalysisSourcePrepareOptions {
  readonly source: string;
  readonly sourceScope?: SourceScope | null;
  readonly composition?: CompositionDefinition | null;
  readonly prepared?: CompositionTaskResult | null;
  readonly mode?: AnalysisSourceMode | '';
}

export interface AnalysisSourcesOptions {
  readonly api: ApiClient;
  readonly context: () => SourceTaskContext;
  readonly grid: (mode: Exclude<AnalysisSourceMode, 'full'>) => GridSourceContextData;
  readonly tasks?: Pick<SourceTasks, 'run'>;
}

const MODE_LABELS: Readonly<Record<string, string>> = {
  full: localize('ui.common.completeSourceTable'),
  filtered: localize('ui.sources.completeFilteringResults'),
  visible: localize('ui.sources.currentlyVisibleRows'),
  selection: localize('ui.sources.currentSelection'),
};

function cloneValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function makeAbortError(): Error {
  const err = new Error(localize('ui.sources.sourceSelectionCancelled'));
  err.name = 'AbortError';
  return err;
}

export function sourceScopeKey(scope: SourceScope | null | undefined): string {
  return JSON.stringify(scope ?? null);
}

export function sourceScopeCaption(scope: SourceScope | null | undefined): string {
  if (!scope) return MODE_LABELS.full ?? localize('ui.common.completeSourceTable');
  const area = scope.selection;
  const base = MODE_LABELS[scope.mode] ?? localize('ui.sources.savedRange');
  return (
    base +
    (area ? ` ${localize('ui.sources.columnSourceRows', { startColumn: area.startColumn, endColumn: area.endColumn, startRow: area.startRow, endRow: area.endRow })}` : '')
  );
}

export const AnalysisSourceModel = Object.freeze({
  caption: sourceScopeCaption,
  key: sourceScopeKey,
});

export class AnalysisSources {
  private readonly api: ApiClient;
  private readonly context: () => SourceTaskContext;
  private readonly grid: (mode: Exclude<AnalysisSourceMode, 'full'>) => GridSourceContextData;
  private readonly tasks?: Pick<SourceTasks, 'run'>;
  private serial = 0;
  public binding: AnalysisSourceRecord | null = null;
  public readonly candidates = new Set<AnalysisSourceRecord>();

  constructor(options: AnalysisSourcesOptions) {
    this.api = options.api;
    this.context = options.context;
    this.grid = options.grid;
    this.tasks = options.tasks;
  }

  identity(): string {
    const c = this.context();
    return JSON.stringify([c.bookKey, c.book, c.opened]);
  }

  private post(path: string, data: unknown): Promise<unknown> {
    return this.api(path, { method: 'POST', body: JSON.stringify(data) });
  }

  clear(record: AnalysisSourceRecord | null | undefined): void {
    if (!record?.id) return;
    const path =
      record.kind === 'composition' || record.composition
        ? '/api/composition/clear'
        : '/api/analysis/source/clear';
    void this.post(path, { id: record.id }).catch(() => {
      // Best-effort server source release.
    });
  }

  release(record: AnalysisSourceRecord | null | undefined): void {
    if (!record || record === this.binding) return;
    if (this.candidates.delete(record)) this.clear(record);
  }

  cancel(): void {
    ++this.serial;
    for (const record of this.candidates) this.release(record);
  }

  reset(): void {
    this.cancel();
    const old = this.binding;
    this.binding = null;
    this.clear(old);
  }

  matches(source: string, scope: SourceScope | null | undefined): boolean {
    return (
      this.binding !== null &&
      this.binding.source === source &&
      this.binding.identity === this.identity() &&
      sourceScopeKey(this.binding.sourceScope) === sourceScopeKey(scope)
    );
  }

  async prepare(
    options: AnalysisSourcePrepareOptions,
    guard: () => boolean = () => true,
  ): Promise<AnalysisSourceRecord> {
    const {
      source,
      sourceScope = null,
      composition = null,
      prepared = null,
      mode = '',
    } = options;
    this.cancel();
    const serial = this.serial;
    const identity = this.identity();
    const c = this.context();
    const current = (): boolean =>
      serial === this.serial && identity === this.identity() && c.opened && guard();
    if (!current()) throw makeAbortError();
    if (!mode && !prepared && this.matches(source, sourceScope) && this.binding) {
      return this.binding;
    }

    let id = '';
    let respSource = source;
    let respBookKey = c.bookKey;
    let respBook = c.book;
    let respScope: SourceScope | null = null;
    let respComposition: CompositionDefinition | undefined;
    let respRows: number | undefined;

    if (source.startsWith('append:')) {
      if (!composition || sourceScope || (mode && mode !== 'full') || !this.tasks) {
        throw new Error(localize('ui.sources.combinedSourceDefinitionInvalidReselect'));
      }
      const data =
        prepared ??
        (await this.tasks.run(decodeCompositionTaskResult,
          {
            mode: 'append',
            definition: cloneValue(composition),
            ...(this.binding?.composition ? { keepId: this.binding.id } : {}),
          },
          current,
        ));
      id = data.id;
      respSource = data.source;
      respBookKey = data.workbookKey;
      respBook = data.book;
      respRows = data.rows;
      respComposition = data.definition;
      respScope = null;
    } else if (mode && mode !== 'full') {
      const g = this.grid(mode);
      if (g.workbookKey !== c.bookKey || g.book !== c.book || g.source !== source) {
        throw new Error(localize('ui.sources.returnWorkbookSelectRangeOn'));
      }
      const data = await this.post('/api/analysis/source/capture', {
        ...g,
        mode,
      }).then(decodeAnalysisSourceBindingResponse);
      id = data.id;
      respSource = data.source;
      respBookKey = data.workbookKey;
      respBook = data.book;
      respRows = data.rows;
      respScope = data.sourceScope;
    } else if (!mode && sourceScope) {
      const data = await this.post('/api/analysis/source/restore', {
        workbookKey: c.bookKey,
        book: c.book,
        source,
        sourceScope: cloneValue(sourceScope),
      }).then(decodeAnalysisSourceBindingResponse);
      id = data.id;
      respSource = data.source;
      respBookKey = data.workbookKey;
      respBook = data.book;
      respRows = data.rows;
      respScope = data.sourceScope;
    }

    const record: AnalysisSourceRecord = {
      id,
      source: respSource,
      workbookKey: respBookKey,
      book: respBook,
      ...(respRows !== undefined ? { rows: respRows } : {}),
      ...(respComposition ? { composition: cloneValue(respComposition) } : {}),
      kind: source.startsWith('append:') ? 'composition' : 'scope',
      identity,
      sourceScope: respScope ? cloneValue(respScope) : null,
      current,
    };
    this.candidates.add(record);
    if (!current()) {
      this.release(record);
      throw makeAbortError();
    }
    const needsBinding = (Boolean(mode) && mode !== 'full') || (!mode && Boolean(sourceScope));
    if (
      respSource !== source ||
      respBookKey !== c.bookKey ||
      respBook !== c.book ||
      (needsBinding && (!id || !respScope))
    ) {
      this.release(record);
      throw new Error(localize('ui.sources.sourceResponseDoesNotMatch'));
    }
    return record;
  }

  commit(record: AnalysisSourceRecord): AnalysisSourceRecord {
    if (record === this.binding) return record;
    if (!this.candidates.has(record) || !record.current()) {
      this.release(record);
      throw makeAbortError();
    }
    const old = this.binding;
    this.binding = record;
    this.candidates.delete(record);
    this.clear(old);
    return record;
  }

  query(query: WorkspaceAnalysisQuery): WorkspaceAnalysisQuery {
    const q = cloneValue(query);
    if (this.matches(q.source, q.sourceScope) && this.binding?.id) {
      q.sourceId = this.binding.id;
    }
    return q;
  }
}
