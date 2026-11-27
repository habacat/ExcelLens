/**
 * Saved analysis plans contain only declared operations and field bindings.
 * Capture, preview and replay share the normal cancellable analysis jobs.
 */
import type {
  AnalysisQueryStatusResponse,
  AnalysisResult,
  HeaderMatch,
  Recipe,
  RecipeField,
  RecipeStore,
  SourceScope,
} from '../contracts/analysis.ts';
import type { ApiClient, ApiRequestOptions, ToastFn } from '../contracts/common.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';

export interface RecipeSourceChoice {
  readonly value: string;
  readonly label: string;
}

export interface RecipeContext {
  readonly bookKey: string;
  readonly opened: boolean;
  readonly intent: number;
  readonly source: string;
  readonly headerRow: number;
  readonly sourceId: string;
  readonly sourceScope?: SourceScope | null;
  readonly resultJob: string;
  readonly sources: readonly RecipeSourceChoice[];
}

export interface RecipeCaptureRequest {
  readonly jobId: string;
  readonly format: string;
}

interface RecipeCatalogResponse {
  readonly book: string;
  readonly store: RecipeStore;
}

interface RecipeCaptureResult {
  readonly captureId: string;
}

interface RecipeCommitResponse {
  readonly id: string;
  readonly revision: number;
}

export interface RecipeTargetRef {
  readonly book: string;
  readonly source: string;
  readonly headerRow: number;
  readonly stamp?: string;
}

export interface RecipePreviewResponse {
  readonly recipe: Recipe;
  readonly fields: readonly RecipeField[];
  readonly matches: readonly HeaderMatch[];
  readonly expectedTypes: Readonly<Record<string, string>>;
  readonly targetTypes: Readonly<Record<string, string>>;
  readonly sampled: number;
  readonly fieldsTruncated: boolean;
  readonly digest: string;
  readonly stamp: string;
  readonly target: {
    readonly total: number;
    readonly stamp: string;
  };
}

export interface RecipePreviewState extends RecipePreviewResponse {
  readonly sourceId: string;
  readonly ref: RecipeTargetRef;
}

export interface RecipeRunCompleted {
  readonly job: string;
  readonly result: AnalysisQueryStatusResponse;
  readonly current: () => boolean;
}

export interface RecipesOptions {
  readonly api: ApiClient;
  readonly context: () => RecipeContext;
  readonly capture: () => RecipeCaptureRequest;
  readonly adopt: (
    completed: RecipeRunCompleted,
    preview: { readonly ref: { readonly source: string }; readonly stamp?: string },
    current: () => boolean,
  ) => Promise<boolean>;
  readonly toast: ToastFn;
}

interface RecipeCaptureDraft {
  readonly id: string;
  readonly job: string;
  readonly format: string;
  readonly key: string;
}

const MATCH_LABELS: Readonly<Record<string, string>> = {
  match: '已匹配',
  missing: '名称未匹配，请选择',
  manual: '需手动选择',
  ambiguous: '名称重复，请选择',
  uncertain: '读取类型不确定',
  untyped: '样本为空，请选择',
  type_mismatch: '读取类型不兼容',
  mixed: '混合类型，请核对后选择',
};

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function isQueryStatusResponse(
  value: AnalysisQueryStatusResponse | AnalysisResult,
): value is AnalysisQueryStatusResponse {
  return 'status' in value;
}

export function isRecipeFieldCompatible(
  expected: RecipeField | null | undefined,
  actual: RecipeField | null | undefined,
): boolean {
  return Boolean(
    expected &&
      actual &&
      expected.mask &&
      !expected.uncertain &&
      !actual.uncertain &&
      !(actual.mask & ~expected.mask),
  );
}

export function validateRecipeMapping(
  preview: Pick<RecipePreviewResponse, 'recipe' | 'fields'> | null | undefined,
  mapping: Readonly<Record<string, string>>,
): string {
  if (!preview) return '请先读取字段';
  const used = new Set<string>();
  for (const field of preview.recipe.fields) {
    const target = preview.fields.find((item) => item.id === mapping[field.id]);
    if (!target) return '请逐项选择目标字段';
    if (used.has(target.id)) return '每个目标字段只能使用一次';
    if (!isRecipeFieldCompatible(field, target)) return '目标字段的读取类型不兼容';
    used.add(target.id);
  }
  return '';
}

export function recipeSummary(value: Recipe | null | undefined): string {
  if (!value) return '';
  const steps = value.steps;
  const group = steps.find((step) => step.kind === 'group')?.group;
  const exportFormat = steps.find((step) => step.kind === 'export')?.export?.format.toUpperCase() ?? '';
  return `${group?.layout === 'tree' ? '层级展开 · ' : ''}${steps.filter((step) => step.kind === 'transform').length}项转换 · ${steps.filter((step) => step.kind === 'filter').length}项筛选 · ${(group?.rows.length ?? 0) + (group?.columns.length ?? 0)}个维度 · ${group?.metrics.length || 1}个指标 · ${exportFormat}`;
}

export const RecipesModel = Object.freeze({
  compatible: isRecipeFieldCompatible,
  validateMapping: validateRecipeMapping,
  summary: recipeSummary,
});

export class RecipesController {
  private readonly api: ApiClient;
  private readonly context: () => RecipeContext;
  private readonly capture: () => RecipeCaptureRequest;
  private readonly adopt: RecipesOptions['adopt'];
  private readonly toast: ToastFn;
  public captureJobs: AsyncJobs<RecipeCaptureResult>;
  public runJobs: AsyncJobs<
    AnalysisQueryStatusResponse | AnalysisResult,
    AnalysisQueryStatusResponse
  >;

  public readonly dialog: HTMLDialogElement;
  public epoch = 0;
  public serial = 0;
  public catalogSerial = 0;
  private catalogLoading = false;
  public opened = false;
  public busy = false;
  public mode = '';
  public book = '';
  public store: RecipeStore | null = null;
  public preview: RecipePreviewState | null = null;
  public draft: RecipeCaptureDraft | null = null;
  private catalogController: AbortController | null = null;
  private controller: AbortController | null = null;

  constructor(options: RecipesOptions) {
    this.api = options.api;
    this.context = options.context;
    this.capture = options.capture;
    this.adopt = options.adopt;
    this.toast = options.toast;
    this.dialog = requireDialog('analysisPlanDialog');

    const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));
    const onProgress = (status: {
      readonly stage?: string;
      readonly progress?: number;
      readonly total?: number;
    }): void => {
      this.status(
        `${status.stage || '正在分析'}${
          status.total
            ? ` · ${(status.progress ?? 0).toLocaleString()} / ${status.total.toLocaleString()}行`
            : ''
        }`,
      );
    };

    this.captureJobs = new AsyncJobs<RecipeCaptureResult>({
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.context().bookKey,
      sleep,
      maxPolls: 0,
      onProgress,
      endpoint: '/api/recipes/capture',
      label: '方案字段核对',
    });

    this.runJobs = new AsyncJobs<
      AnalysisQueryStatusResponse | AnalysisResult,
      AnalysisQueryStatusResponse
    >({
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.context().bookKey,
      sleep,
      maxPolls: 0,
      onProgress,
      endpoint: '/api/recipes/run',
      label: '方案分析',
      resultFromStatus: (status) => status,
    });

    requireButton('analysisPlansOpen').addEventListener('click', () => {
      void this.open();
    });
    requireButton('analysisPlanRefresh').addEventListener('click', () => {
      void this.refresh();
    });
    requireButton('analysisPlanSave').addEventListener('click', () => {
      void this.save();
    });
    requireSelect('analysisPlanList').addEventListener('change', () => this.select());
    requireButton('analysisPlanDelete').addEventListener('click', () => {
      void this.remove();
    });
    requireButton('analysisPlanPreview').addEventListener('click', () => {
      void this.readPreview();
    });
    requireButton('analysisPlanRun').addEventListener('click', () => {
      void this.run();
    });
    requireButton('analysisPlanCancel').addEventListener('click', () => this.cancel());
    for (const id of ['analysisPlanTarget', 'analysisPlanHeader', 'analysisPlanScope'] as const) {
      requireEl(id).addEventListener('change', () => {
        this.clearPreview();
        this.targetControls();
      });
    }
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.reset();
    });
  }

  identity(): string {
    const c = this.context();
    return JSON.stringify([c.bookKey, c.opened, c.intent, this.epoch, this.opened, this.dialog.open]);
  }

  current(serial: number, epoch: number, key: string): boolean {
    return (
      this.opened &&
      this.dialog.open &&
      serial === this.serial &&
      epoch === this.epoch &&
      this.context().bookKey === key &&
      this.context().opened
    );
  }

  private post<T>(url: string, data: unknown, options: ApiRequestOptions = {}): Promise<T> {
    return this.api<T>(url, { method: 'POST', body: JSON.stringify(data), ...options });
  }

  status(text: string, error = false): void {
    const node = requireEl('analysisPlanStatus');
    node.textContent = text;
    node.classList.toggle('is-error', error);
  }

  selected(): Recipe | undefined {
    const selectedId = requireSelect('analysisPlanList').value;
    return this.store?.items.find((item) => item.id === selectedId);
  }

  mapping(): Record<string, string> {
    const result: Record<string, string> = {};
    for (const select of requireEl('analysisPlanBindings').querySelectorAll<HTMLSelectElement>(
      'select[data-field]',
    )) {
      const field = select.dataset.field;
      if (field) result[field] = select.value;
    }
    return result;
  }

  controls(): void {
    const hasSelected = Boolean(this.selected());
    const c = this.context();
    const blocked = this.busy || this.catalogLoading;
    requireButton('analysisPlanRefresh').disabled = blocked;
    requireInput('analysisPlanName').disabled = blocked;
    requireSelect('analysisPlanTarget').disabled = blocked;
    requireInput('analysisPlanHeader').disabled = blocked;
    requireSelect('analysisPlanScope').disabled = blocked;
    requireSelect('analysisPlanList').disabled = blocked || !this.store;
    requireButton('analysisPlanSave').disabled = blocked || !this.store || !c.resultJob;
    requireButton('analysisPlanDelete').disabled = blocked || !hasSelected;
    requireButton('analysisPlanPreview').disabled =
      blocked || !hasSelected || !requireSelect('analysisPlanTarget').value;
    requireButton('analysisPlanRun').disabled =
      blocked || Boolean(validateRecipeMapping(this.preview, this.mapping()));
    for (const select of requireEl('analysisPlanBindings').querySelectorAll<HTMLSelectElement>(
      'select',
    )) {
      select.disabled = blocked;
    }
    requireButton('analysisPlanCancel').hidden =
      !this.busy || !['capture', 'preview', 'run'].includes(this.mode);
    this.targetControls();
  }

  targetControls(): void {
    const targetVal = requireSelect('analysisPlanTarget').value;
    const scratch = targetVal.startsWith('scratch:');
    const c = this.context();
    const scope = requireSelect('analysisPlanScope');
    const headerInput = requireInput('analysisPlanHeader');
    headerInput.disabled = this.busy || this.catalogLoading || scratch;
    if (scratch) headerInput.value = '1';
    const current =
      !scratch && c.source === targetVal && Boolean(c.sourceId) && Boolean(c.sourceScope);
    const currentOpt = scope.querySelector<HTMLOptionElement>('[value="current"]');
    if (currentOpt) currentOpt.disabled = !current;
    if (!current) scope.value = 'full';
  }

  stopCatalog(): void {
    ++this.catalogSerial;
    this.catalogController?.abort();
    this.catalogController = null;
    this.catalogLoading = false;
  }

  clearPreview(): void {
    this.preview = null;
    requireEl('analysisPlanBindings').replaceChildren();
    requireEl('analysisPlanSample').textContent = '';
    requireEl('analysisPlanMapping').hidden = true;
    this.controls();
  }

  reset(): void {
    ++this.epoch;
    ++this.serial;
    this.stopCatalog();
    this.controller?.abort();
    this.controller = null;
    this.captureJobs.reset();
    this.runJobs.reset();
    this.opened = false;
    this.busy = false;
    this.mode = '';
    this.draft = null;
    this.clearPreview();
  }

  close(): void {
    this.reset();
    if (this.dialog.open) this.dialog.close();
  }

  async open(): Promise<void> {
    const c = this.context();
    if (!c.opened || !c.bookKey) {
      this.toast('请先打开分析工作区', 'warning');
      return;
    }
    this.opened = true;
    this.store = null;
    this.book = '';
    const target = requireSelect('analysisPlanTarget');
    target.replaceChildren();
    for (const source of c.sources) target.append(new Option(source.label, source.value));
    target.value = c.source;
    requireInput('analysisPlanHeader').value = String(c.headerRow || 1);
    requireSelect('analysisPlanScope').value = 'full';
    requireEl('analysisPlanSaveHint').textContent = c.resultJob ? '' : '请先运行分析';
    if (!this.dialog.open) this.dialog.showModal();
    this.clearPreview();
    await this.refresh();
  }

  setCatalog(data: RecipeCatalogResponse, selected = ''): void {
    this.book = data.book;
    this.store = data.store;
    const list = requireSelect('analysisPlanList');
    const previous = selected || list.value;
    list.replaceChildren();
    list.append(new Option(this.store.items.length ? '选择分析方案' : '尚无分析方案', ''));
    for (const value of this.store.items) list.append(new Option(value.name, value.id));
    list.value = this.store.items.some((value) => value.id === previous) ? previous : '';
    this.select();
  }

  select(): void {
    this.clearPreview();
    requireEl('analysisPlanSummary').textContent = recipeSummary(this.selected());
    this.controls();
  }

  async refresh(): Promise<void> {
    if (this.busy) return;
    const serial = ++this.catalogSerial;
    const epoch = this.epoch;
    const key = this.context().bookKey;
    this.catalogController?.abort();
    const controller = new AbortController();
    this.catalogController = controller;
    this.catalogLoading = true;
    this.controls();
    try {
      const data = await this.api<RecipeCatalogResponse>('/api/recipes', {
        signal: controller.signal,
      });
      if (
        serial !== this.catalogSerial ||
        epoch !== this.epoch ||
        key !== this.context().bookKey ||
        !this.opened
      ) {
        return;
      }
      this.setCatalog(data);
      this.status(
        this.draft
          ? '列表已刷新，可重试保存已核对的方案。'
          : `${this.store?.items.length ?? 0}个方案`,
      );
    } catch (error: unknown) {
      if (
        serial === this.catalogSerial &&
        epoch === this.epoch &&
        this.opened &&
        !isAbortError(error)
      ) {
        this.store = null;
        this.status(errorMessage(error), true);
        this.controls();
      }
    } finally {
      if (this.catalogController === controller) {
        this.catalogController = null;
        this.catalogLoading = false;
        this.controls();
      }
    }
  }

  async save(): Promise<void> {
    if (this.busy || !this.store) return;
    const nameInput = requireInput('analysisPlanName');
    const name = nameInput.value.trim();
    if (!name || Array.from(name).length > 80 || /[\r\n\0]/.test(name)) {
      this.status('请输入1至80字的方案名称', true);
      nameInput.focus();
      return;
    }
    let captured: RecipeCaptureRequest;
    try {
      captured = this.capture();
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
      return;
    }
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const key = this.context().bookKey;
    const identity = this.identity();
    const current = (): boolean =>
      this.current(serial, epoch, key) && identity === this.identity();
    this.busy = true;
    this.mode = 'capture';
    this.controls();
    try {
      if (
        this.draft?.job !== captured.jobId ||
        this.draft.format !== captured.format ||
        this.draft.key !== key
      ) {
        this.status('正在核对完整输入的字段类型…');
        const completed = await this.captureJobs.execute(
          { ...captured, book: this.book, workbookKey: key },
          { main: true },
        );
        if (!current() || !completed.current()) return;
        this.draft = {
          id: completed.result.captureId,
          job: captured.jobId,
          format: captured.format,
          key,
        };
      }
      this.mode = 'commit';
      this.controls();
      this.status('正在保存分析方案…');
      const saved = await this.post<RecipeCommitResponse>('/api/recipes/capture/commit', {
        id: this.draft.id,
        name,
        book: this.book,
        workbookKey: key,
        expectedRevision: this.store.revision,
      });
      if (!current()) return;
      this.draft = null;
      this.store = { ...this.store, revision: saved.revision };
      try {
        const data = await this.api<RecipeCatalogResponse>('/api/recipes');
        if (current()) this.setCatalog(data, saved.id);
      } catch {
        if (current()) this.status('方案已保存；刷新列表后查看。');
        return;
      }
      if (current()) this.status('分析方案已保存到本机');
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(
          errorMessage(error) + (this.draft ? '；可刷新列表或更名后重试。' : ''),
          true,
        );
      }
    } finally {
      if (this.current(serial, epoch, key)) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
    }
  }

  async remove(): Promise<void> {
    const selected = this.selected();
    if (this.busy || !selected || !this.store) return;
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const key = this.context().bookKey;
    this.busy = true;
    this.mode = 'delete';
    this.controls();
    try {
      const data = await this.post<RecipeCatalogResponse>('/api/recipes', {
        action: 'delete',
        id: selected.id,
        expectedRevision: this.store.revision,
      });
      if (this.current(serial, epoch, key)) {
        this.setCatalog(data);
        this.status('已删除所选分析方案');
      }
    } catch (error: unknown) {
      if (this.current(serial, epoch, key)) this.status(errorMessage(error), true);
    } finally {
      if (this.current(serial, epoch, key)) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
    }
  }

  target(): RecipeTargetRef {
    return {
      book: this.book,
      source: requireSelect('analysisPlanTarget').value,
      headerRow: Number(requireInput('analysisPlanHeader').value),
    };
  }

  async readPreview(): Promise<void> {
    const selected = this.selected();
    if (this.busy || !selected) return;
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const key = this.context().bookKey;
    const controller = new AbortController();
    this.controller = controller;
    this.busy = true;
    this.mode = 'preview';
    this.clearPreview();
    this.status('正在读取字段与类型样本…');
    try {
      const target = this.target();
      if (!Number.isInteger(target.headerRow) || target.headerRow < 1) {
        throw new Error('表头行号须为正整数');
      }
      const c = this.context();
      const useCurrent = requireSelect('analysisPlanScope').value === 'current';
      const sourceId = useCurrent ? c.sourceId : '';
      if (useCurrent && (!sourceId || c.source !== target.source || !c.sourceScope)) {
        throw new Error('当前分析范围已变化，请重新选择');
      }
      const data = await this.post<RecipePreviewResponse>(
        '/api/recipes/preview',
        {
          id: selected.id,
          book: this.book,
          workbookKey: key,
          target,
          ...(sourceId ? { sourceId } : {}),
        },
        { signal: controller.signal },
      );
      if (!this.current(serial, epoch, key)) return;
      this.preview = { ...data, sourceId, ref: { ...target, stamp: data.target.stamp } };
      this.renderPreview();
      this.status('核对字段映射后运行');
    } catch (error: unknown) {
      if (this.current(serial, epoch, key) && !isAbortError(error)) {
        this.status(errorMessage(error), true);
      }
    } finally {
      if (this.current(serial, epoch, key)) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
      if (this.controller === controller) this.controller = null;
    }
  }

  renderPreview(): void {
    const preview = this.preview;
    if (!preview) return;
    const host = requireEl('analysisPlanBindings');
    host.replaceChildren();
    requireEl('analysisPlanMapping').hidden = false;
    requireEl('analysisPlanSample').textContent =
      `预览 ${preview.sampled} / ${preview.target.total.toLocaleString()} 行${preview.fieldsTruncated ? ' · 仅前256列' : ''}`;
    if (!preview.recipe.fields.length) {
      const note = document.createElement('p');
      note.textContent = '此方案只统计记录数，无需绑定字段。';
      host.append(note);
      return;
    }
    for (const field of preview.recipe.fields) {
      const match = preview.matches.find((item) => item.field === field.id);
      const row = document.createElement('div');
      row.className = 'analysis-plan-binding';
      const label = document.createElement('label');
      const name = document.createElement('strong');
      const type = document.createElement('span');
      name.textContent = `${field.name} · ${field.id}`;
      type.textContent = preview.expectedTypes[field.id] ?? '';
      label.append(name, type);

      const select = document.createElement('select');
      select.className = 'field-control';
      select.dataset.field = field.id;
      select.id = `analysisPlanField_${field.id}`;
      label.htmlFor = select.id;
      select.append(new Option('选择目标字段', ''));
      for (const target of preview.fields) {
        const option = new Option(
          `${target.name} · ${target.id} · ${preview.targetTypes[target.id] ?? ''}`,
          target.id,
        );
        option.disabled = !isRecipeFieldCompatible(field, target);
        select.append(option);
      }
      select.value = match?.status === 'match' ? match.target : '';

      const note = document.createElement('small');
      const defaultNote = (match ? MATCH_LABELS[match.status] : undefined) ?? '需手动选择';
      note.textContent = defaultNote;
      select.addEventListener('change', () => {
        note.textContent = select.value ? '已手动选择，请核对读取类型' : defaultNote;
        this.controls();
        const error = validateRecipeMapping(preview, this.mapping());
        if (error) this.status(error, true);
        else this.status('字段映射已就绪');
      });
      const control = document.createElement('div');
      control.append(select, note);
      row.append(label, control);
      host.append(row);
    }
  }

  cancel(): void {
    if (!this.busy || !['capture', 'preview', 'run'].includes(this.mode)) return;
    ++this.serial;
    this.controller?.abort();
    this.captureJobs.cancelMain();
    this.runJobs.cancelMain();
    this.busy = false;
    this.mode = '';
    this.status('已请求取消，当前分析结果保留');
    this.controls();
  }

  async run(): Promise<void> {
    if (this.busy || !this.preview) return;
    const mapping = this.mapping();
    const error = validateRecipeMapping(this.preview, mapping);
    if (error) {
      this.status(error, true);
      return;
    }
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const key = this.context().bookKey;
    const identity = this.identity();
    const current = (): boolean =>
      this.current(serial, epoch, key) && identity === this.identity();
    const preview = this.preview;
    this.busy = true;
    this.mode = 'run';
    this.controls();
    this.status('正在核对来源并运行分析…');
    try {
      const completed = await this.runJobs.execute(
        {
          id: preview.recipe.id,
          digest: preview.digest,
          book: this.book,
          workbookKey: key,
          target: preview.ref,
          stamp: preview.stamp,
          mapping,
          ...(preview.sourceId ? { sourceId: preview.sourceId } : {}),
        },
        { main: true },
      );
      if (!current() || !completed.current()) return;
      if (!isQueryStatusResponse(completed.result)) {
        throw new Error('方案运行状态响应无效');
      }
      this.mode = 'adopt';
      this.controls();
      const adopted = await this.adopt(
        { job: completed.job, result: completed.result, current: completed.current },
        preview,
        current,
      );
      if (!adopted) return;
      if (this.current(serial, epoch, key)) {
        this.toast(`已运行分析方案：${preview.recipe.name}`, 'success');
        this.dialog.close();
      }
    } catch (error: unknown) {
      if (this.current(serial, epoch, key) && !isAbortError(error)) {
        this.status(errorMessage(error), true);
      }
    } finally {
      if (this.current(serial, epoch, key)) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
    }
  }
}
