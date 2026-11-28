/**
 * Saved analysis plans contain only declared operations and field bindings.
 * Capture, preview and replay share the normal cancellable analysis jobs.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { AsyncJobStatusEnvelope } from '../core/async-jobs.ts';
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
import {
  decodeAnalysisQueryStatusResponse,
  decodeRecipeCaptureStatus,
  decodeRecipeCatalogResponse,
  decodeRecipeCommitResponse,
  decodeRecipePreviewResponse,
} from '../core/http-decoders.generated.ts';

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

/** @httpResponse RecipeCatalogResponse */
export interface RecipeCatalogResponse {
  readonly book: string;
  readonly store: RecipeStore;
}

/** @httpResponse RecipeCaptureStatus */
export type RecipeCaptureStatus = AsyncJobStatusEnvelope<RecipeCaptureResult>;

interface RecipeCaptureResult {
  readonly captureId: string;
}

/** @httpResponse RecipeCommitResponse */
export interface RecipeCommitResponse {
  readonly id: string;
  readonly revision: number;
}

export interface RecipeTargetRef {
  readonly book: string;
  readonly source: string;
  readonly headerRow: number;
  readonly stamp?: string;
}

/** @httpResponse RecipePreviewResponse */
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
  match: localize('ui.recipes.matchedSuccessfully'),
  missing: localize('ui.recipes.nameDoesNotMatchSelect'),
  manual: localize('ui.recipes.needManualSelection'),
  ambiguous: localize('ui.recipes.nameDuplicationSelect'),
  uncertain: localize('ui.recipes.uncertainReadingType'),
  untyped: localize('ui.recipes.sampleEmptySelect'),
  type_mismatch: localize('ui.recipes.incompatibleReadingType'),
  mixed: localize('ui.recipes.mixedTypesCheckSelect'),
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
  if (!preview) return localize('ui.recipes.readFieldsFirst');
  const used = new Set<string>();
  for (const field of preview.recipe.fields) {
    const target = preview.fields.find((item) => item.id === mapping[field.id]);
    if (!target) return localize('ui.recipes.selectTargetFieldItemItem');
    if (used.has(target.id)) return localize('ui.recipes.eachTargetFieldCanBe');
    if (!isRecipeFieldCompatible(field, target)) return localize('ui.recipes.readingTypeTargetFieldIncompatible');
    used.add(target.id);
  }
  return '';
}

export function recipeSummary(value: Recipe | null | undefined): string {
  if (!value) return '';
  const steps = value.steps;
  const group = steps.find((step) => step.kind === 'group')?.group;
  const exportFormat = steps.find((step) => step.kind === 'export')?.export?.format.toUpperCase() ?? '';
  return localize('ui.recipes.itemsConvertedItemsFilteredDimensions', { detail: group?.layout === 'tree' ? `${localize('ui.recipes.expandLevels')} ` : '', count: steps.filter((step) => step.kind === 'transform').length, count2: steps.filter((step) => step.kind === 'filter').length, detail2: (group?.rows.length ?? 0) + (group?.columns.length ?? 0), detail3: group?.metrics.length || 1, exportFormat });
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
        `${status.stage || localize('ui.recipes.analyzing')}${
          status.total
            ? ` ${localize('ui.recipes.rows', { detail: (status.progress ?? 0).toLocaleString(interfaceLocale()), total: status.total.toLocaleString(interfaceLocale()) })}`
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
      decodeStatus: decodeRecipeCaptureStatus,
      resultFromStatus: status => {
        if (!status.result) throw new Error(localize('ui.recipes.comparisonSchemeFieldsLacksCompleted'));
        return status.result;
      },
      label: localize('ui.recipes.verifySchemeFields'),
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
      decodeStatus: decodeAnalysisQueryStatusResponse,
      label: localize('ui.recipes.scenarioAnalysis'),
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

  private post(url: string, data: unknown, options: ApiRequestOptions = {}): Promise<unknown> {
    return this.api(url, { method: 'POST', body: JSON.stringify(data), ...options });
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
      this.toast(localize('ui.recipes.openAnalysisWorkspaceFirst'), 'warning');
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
    requireEl('analysisPlanSaveHint').textContent = c.resultJob ? '' : localize('ui.recipes.runAnalysisFirst');
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
    list.append(new Option(this.store.items.length ? localize('ui.recipes.selectAnalysisScheme') : localize('ui.recipes.noAnalysisPlanYet'), ''));
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
      const data = await this.api('/api/recipes', {
        signal: controller.signal,
      }, decodeRecipeCatalogResponse);
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
          ? localize('ui.recipes.listRefreshedTrySavingVerified')
          : localize('ui.recipes.solutions', { detail: this.store?.items.length ?? 0 }),
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
      this.status(localize('ui.recipes.enterProposalNameCharacters'), true);
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
        this.status(localize('ui.recipes.verifyingFieldTypeFullyEntered'));
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
      this.status(localize('ui.recipes.savingAnalysisPlan'));
      const saved = await this.post('/api/recipes/capture/commit', {
        id: this.draft.id,
        name,
        book: this.book,
        workbookKey: key,
        expectedRevision: this.store.revision,
      }).then(decodeRecipeCommitResponse);
      if (!current()) return;
      this.draft = null;
      this.store = { ...this.store, revision: saved.revision };
      try {
        const data = await this.api('/api/recipes', undefined, decodeRecipeCatalogResponse);
        if (current()) this.setCatalog(data, saved.id);
      } catch {
        if (current()) this.status(localize('ui.recipes.schemeSavedViewAfterRefreshing'));
        return;
      }
      if (current()) this.status(localize('ui.recipes.analysisPlanSavedLocally'));
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(
          errorMessage(error) + (this.draft ? localize('ui.recipes.tryAgainAfterRefreshingList') : ''),
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
      const data = await this.post('/api/recipes', {
        action: 'delete',
        id: selected.id,
        expectedRevision: this.store.revision,
      }).then(decodeRecipeCatalogResponse);
      if (this.current(serial, epoch, key)) {
        this.setCatalog(data);
        this.status(localize('ui.recipes.selectedAnalysisSchemeDeleted'));
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
    this.status(localize('ui.recipes.readingSampleFieldsTypesProgress'));
    try {
      const target = this.target();
      if (!Number.isInteger(target.headerRow) || target.headerRow < 1) {
        throw new Error(localize('ui.recipes.rowNumbersHeaderMustBe'));
      }
      const c = this.context();
      const useCurrent = requireSelect('analysisPlanScope').value === 'current';
      const sourceId = useCurrent ? c.sourceId : '';
      if (useCurrent && (!sourceId || c.source !== target.source || !c.sourceScope)) {
        throw new Error(localize('ui.recipes.currentAnalysisRangeChangedReselect'));
      }
      const data = await this.post(
        '/api/recipes/preview',
        {
          id: selected.id,
          book: this.book,
          workbookKey: key,
          target,
          ...(sourceId ? { sourceId } : {}),
        },
        { signal: controller.signal },
      ).then(decodeRecipePreviewResponse);
      if (!this.current(serial, epoch, key)) return;
      this.preview = { ...data, sourceId, ref: { ...target, stamp: data.target.stamp } };
      this.renderPreview();
      this.status(localize('ui.recipes.runAfterVerifyingFieldMapping'));
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
      localize('ui.recipes.previewRow', { sampled: preview.sampled, total: preview.target.total.toLocaleString(interfaceLocale()), detail: preview.fieldsTruncated ? ` ${localize('ui.recipes.onlyFirstColumns')}` : '' });
    if (!preview.recipe.fields.length) {
      const note = document.createElement('p');
      note.textContent = localize('ui.recipes.thisSolutionOnlyCountsNumber');
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
      select.append(new Option(localize('ui.recipes.selectTargetField'), ''));
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
      const defaultNote = (match ? MATCH_LABELS[match.status] : undefined) ?? localize('ui.recipes.needManualSelection');
      note.textContent = defaultNote;
      select.addEventListener('change', () => {
        note.textContent = select.value ? localize('ui.recipes.manualSelectionMadeCheckReading') : defaultNote;
        this.controls();
        const error = validateRecipeMapping(preview, this.mapping());
        if (error) this.status(error, true);
        else this.status(localize('ui.recipes.fieldMappingCompleted'));
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
    this.status(localize('ui.recipes.applicationRequestedBeCanceledCurrent'));
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
    this.status(localize('ui.recipes.sourceBeingVerifiedAnalysisBeing'));
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
        throw new Error(localize('ui.recipes.planExecutionStatusResponseInvalid'));
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
        this.toast(localize('ui.recipes.analysisPlansHaveRun', { name: preview.recipe.name }), 'success');
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
