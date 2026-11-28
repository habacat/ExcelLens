/**
 * Chart image (PNG / SVG) + exact-data ZIP package export modal controller.
 */
import { t as localize, th as localizeHTML } from '../core/i18n.ts';
import type { ActivityService, ToastFn } from '../contracts/common.ts';
import type { ChartExportLimits } from '../contracts/formats.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage, formatBytes } from '../core/display-format.ts';
import {
  readAppVersion,
  requireAnchor,
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import { requireFileFormats } from '../core/formats.ts';
import { chartSnapshot, type ChartResultLike } from './charts.ts';
import {
  boundedStringify,
  buildZipBlob,
  captureChartExportPayload,
  chartExportCapacityError,
  validateChartExportDimensions,
  type ChartExportAnalysisInput,
  type ChartExportBackground,
  type ChartExportPayload,
  type ChartExportSelectionQuickInput,
  type ChartExportSpec,
  type ChartExportType,
} from './chart-export-model.ts';
import { chartExportDescriptions, renderChartExportImage } from './chart-export-surface.ts';

type DistributiveOmit<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never;

export type ChartExportOpenInput = DistributiveOmit<
  ChartExportAnalysisInput | ChartExportSelectionQuickInput,
  'theme' | 'numberSettings' | 'applicationVersion'
> & {
  readonly canvas: HTMLCanvasElement;
  readonly expectedResult: ChartResultLike;
  readonly current: () => boolean;
  readonly notify?: ToastFn;
};

interface BoundChartExport {
  readonly payload: ChartExportPayload;
  readonly current: () => boolean;
}

interface AcceptedChartExport {
  readonly spec: ChartExportSpec;
  readonly image: Blob;
  readonly blob: Blob;
  readonly imageURL: string;
  readonly packageURL: string;
}

function makeAbortError(): Error {
  const err = new Error(localize('ui.chartExport.chartExportCanceled'));
  err.name = 'AbortError';
  return err;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function requireFieldSet(id: string): HTMLFieldSetElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFieldSetElement)) {
    throw new Error(`Expected HTMLFieldSetElement for #${id}`);
  }
  return node;
}

function requireImage(id: string): HTMLImageElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLImageElement)) {
    throw new Error(`Expected HTMLImageElement for #${id}`);
  }
  return node;
}

function parseExportType(raw: string): ChartExportType {
  return raw === 'svg' ? 'svg' : 'png';
}

function parseExportBackground(raw: string): ChartExportBackground {
  return raw === 'white' ? 'white' : 'captured';
}

export interface ChartExportControllerOverrides {
  readonly snapshot?: typeof chartSnapshot;
  readonly descriptions?: typeof chartExportDescriptions;
  readonly image?: typeof renderChartExportImage;
}

export class ChartExportController {
  private readonly limits: ChartExportLimits;
  private readonly activity?: ActivityService;
  private readonly overrides?: ChartExportControllerOverrides;
  public readonly dialog: HTMLDialogElement;
  private serial = 0;
  public busy = false;
  public bound: BoundChartExport | null = null;
  public accepted: AcceptedChartExport | null = null;
  private abort: AbortController | null = null;

  constructor(
    limits: ChartExportLimits,
    activity?: ActivityService,
    overrides?: ChartExportControllerOverrides,
  ) {
    this.limits = limits;
    this.activity = activity;
    this.overrides = overrides;
    if (!limits.types.length || !limits.scales.length || !limits.dataBytes || !limits.packageBytes) {
      throw new Error(localize('ui.chartExport.chartExportCapacityUnavailable'));
    }

    const dialog = document.createElement('dialog');
    dialog.id = 'chartExportDialog';
    dialog.className = 'chart-export-dialog';
    dialog.setAttribute('aria-labelledby', 'chartExportTitle');
    // Static template only. Captured source labels below use textContent.
    dialog.innerHTML = `<div class="chart-export-head"><h3 id="chartExportTitle">${localizeHTML('ui.chartExport.exportImage')}</h3><button id="chartExportClose" type="button" class="mini-button" aria-label="${localizeHTML('ui.chartExport.disableChartExport')}">${localizeHTML('ui.chartExport.close')}</button></div>
      <form id="chartExportForm"><fieldset id="chartExportOptions"><label>${localizeHTML('ui.chartExport.format')}<select id="chartExportType" class="field-control"></select></label><label>${localizeHTML('ui.chartExport.widthPixels')}<input id="chartExportWidth" class="field-control" type="number" step="1"></label><label>${localizeHTML('ui.chartExport.heightPixels')}<input id="chartExportHeight" class="field-control" type="number" step="1"></label><label>${localizeHTML('ui.chartExport.pngMagnification')}<select id="chartExportScale" class="field-control"></select></label><label>${localizeHTML('ui.chartExport.background')}<select id="chartExportBackground" class="field-control"><option value="captured">${localizeHTML('ui.chartExport.currentColorScheme')}</option><option value="white">${localizeHTML('ui.chartExport.whiteBackground')}</option></select></label></fieldset>
      <div class="chart-export-actions"><button id="chartExportBuild" class="mini-button" type="submit">${localizeHTML('ui.chartExport.generateImage')}</button><button id="chartExportCancel" class="mini-button" type="button" hidden>${localizeHTML('ui.chartExport.cancel')}</button></div></form>
      <p id="chartExportStatus" class="chart-export-status" role="status" aria-live="polite"></p><div id="chartExportResult" class="chart-export-result" hidden><p id="chartExportPackageInfo"></p><div class="chart-export-actions"><a id="chartExportImageDownload" class="mini-button">${localizeHTML('ui.chartExport.downloadImage')}</a><a id="chartExportDownload" class="mini-button" download="ExcelLens-Chart.zip">${localizeHTML('ui.chartExport.downloadZip')}</a></div><div class="chart-export-preview"><img id="chartExportPreview" alt="${localizeHTML('ui.chartExport.previewImage')}"></div></div>`;
    document.body.append(dialog);
    this.dialog = dialog;

    const typeSelect = requireSelect('chartExportType');
    for (const kind of limits.types) typeSelect.append(new Option(kind.toUpperCase(), kind));
    const scaleSelect = requireSelect('chartExportScale');
    for (const scale of limits.scales) scaleSelect.append(new Option(`${scale}×`, String(scale)));
    const widthInput = requireInput('chartExportWidth');
    widthInput.min = String(limits.minWidth);
    widthInput.max = String(limits.maxWidth);
    const heightInput = requireInput('chartExportHeight');
    heightInput.min = String(limits.minHeight);
    heightInput.max = String(limits.maxHeight);

    requireButton('chartExportClose').addEventListener('click', () => this.close());
    dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    dialog.addEventListener('close', () => {
      if (!dialog.open) this.clear();
    });
    requireEl('chartExportForm').addEventListener('submit', (event: SubmitEvent) => {
      event.preventDefault();
      void this.generate();
    });
    requireButton('chartExportCancel').addEventListener('click', () => this.cancel());
    requireFieldSet('chartExportOptions').addEventListener('change', () => {
      this.controls();
      if (this.accepted) this.status(localize('ui.chartExport.settingsHaveChangedDownloadStill'));
    });
    for (const id of ['chartExportImageDownload', 'chartExportDownload'] as const) {
      requireAnchor(id).addEventListener('click', (event: MouseEvent) => {
        if (!this.revalidate() || this.busy || !this.accepted) event.preventDefault();
      });
    }
  }

  status(text: string, error = false): void {
    const p = requireEl('chartExportStatus');
    p.textContent = text;
    p.classList.toggle('is-error', error);
    p.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    requireFieldSet('chartExportOptions').disabled = this.busy;
    requireButton('chartExportBuild').disabled = this.busy;
    requireButton('chartExportCancel').hidden = !this.busy;
    requireSelect('chartExportScale').disabled =
      this.busy || requireSelect('chartExportType').value === 'svg';
    const disabled = this.busy || !this.accepted;
    for (const id of ['chartExportImageDownload', 'chartExportDownload'] as const) {
      const link = requireAnchor(id);
      link.setAttribute('aria-disabled', String(disabled));
      link.setAttribute('tabindex', disabled ? '-1' : '0');
    }
  }

  revalidate(): boolean {
    if (this.bound && !this.bound.current()) {
      this.close();
      return false;
    }
    return this.bound !== null;
  }

  open(input: ChartExportOpenInput): boolean {
    if (this.dialog.open) return false;
    try {
      if (!input.current()) throw new Error(localize('ui.chartExport.chartResultsHaveChangedReopen'));
      const drawing = (this.overrides?.snapshot ?? chartSnapshot)(input.canvas);
      if (drawing?.result !== input.expectedResult || drawing.mode !== input.mode) {
        throw new Error(localize('ui.chartExport.chartNotUpdatedYetTry'));
      }
      const captureInput =
        input.kind === 'analysis'
          ? {
              ...input,
              theme: drawing.theme,
              numberSettings: drawing.numberSettings,
              applicationVersion: readAppVersion(),
            }
          : {
              ...input,
              theme: drawing.theme,
              numberSettings: drawing.numberSettings,
              applicationVersion: readAppVersion(),
            };
      const payload = captureChartExportPayload(captureInput, this.limits);
      if (!input.current()) throw makeAbortError();
      this.clear();
      this.bound = { payload, current: input.current };
      requireInput('chartExportWidth').value = String(this.limits.defaultWidth);
      requireInput('chartExportHeight').value = String(this.limits.defaultHeight);
      requireSelect('chartExportType').value = 'png';
      const defaultScale = this.limits.scales.at(-1) ?? 1;
      requireSelect('chartExportScale').value = String(defaultScale);
      requireSelect('chartExportBackground').value = 'captured';
      this.controls();
      this.status('');
      this.dialog.showModal();
      requireButton('chartExportBuild').focus();
      return true;
    } catch (error: unknown) {
      if (!isAbortError(error)) input.notify?.(errorMessage(error), 'error');
      return false;
    }
  }

  stop(): void {
    ++this.serial;
    this.abort?.abort();
    this.abort = null;
    this.busy = false;
  }

  private revoke(result: { readonly imageURL?: string; readonly packageURL?: string } | null): void {
    if (!result) return;
    if (result.imageURL) URL.revokeObjectURL(result.imageURL);
    if (result.packageURL) URL.revokeObjectURL(result.packageURL);
  }

  clear(): void {
    this.stop();
    this.revoke(this.accepted);
    this.accepted = null;
    this.bound = null;
    requireEl('chartExportResult').hidden = true;
    for (const id of ['chartExportImageDownload', 'chartExportDownload'] as const) {
      requireAnchor(id).removeAttribute('href');
    }
    requireImage('chartExportPreview').removeAttribute('src');
    this.status('');
  }

  close(): void {
    this.clear();
    if (this.dialog.open) this.dialog.close();
  }

  cancel(): void {
    this.stop();
    this.status(this.accepted ? localize('ui.chartExport.cancelledRetainingPreviousResults') : localize('ui.common.canceled'));
    this.controls();
  }

  specification(): ChartExportSpec {
    const readDigits = (id: string): number => {
      const value = requireInput(id).value.trim();
      if (!/^\d+$/.test(value)) throw new Error(localize('ui.chartExport.enterIntegerImageSize'));
      return Number(value);
    };
    const type = parseExportType(requireSelect('chartExportType').value);
    const spec: ChartExportSpec = {
      type,
      width: readDigits('chartExportWidth'),
      height: readDigits('chartExportHeight'),
      scale: type === 'svg' ? 1 : Number(requireSelect('chartExportScale').value),
      background: parseExportBackground(requireSelect('chartExportBackground').value),
    };
    validateChartExportDimensions(spec, this.limits);
    return spec;
  }

  async generate(): Promise<void> {
    if (!this.revalidate() || !this.dialog.open || this.busy || !this.bound) return;
    let spec: ChartExportSpec;
    try {
      spec = this.specification();
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
      return;
    }
    this.stop();
    const serial = this.serial;
    const ctrl = new AbortController();
    const bound = this.bound;
    this.abort = ctrl;
    this.busy = true;
    this.controls();
    this.status(localize('ui.chartExport.generating'));
    const activity = this.activity
      ? this.activity.begin(localize('ui.chartExport.chartFileBeingGenerated'), { signal: ctrl.signal })
      : beginActivity(localize('ui.chartExport.chartFileBeingGenerated'), { signal: ctrl.signal });
    const current = (): boolean =>
      serial === this.serial &&
      !ctrl.signal.aborted &&
      this.bound === bound &&
      this.dialog.open &&
      bound.current();
    const checkpoint = async (): Promise<void> => {
      if (!current()) throw makeAbortError();
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      if (!current()) throw makeAbortError();
    };
    let candidate: { readonly imageURL: string; packageURL: string } | null = null;
    try {
      await checkpoint();
      const dimensions = validateChartExportDimensions(spec, this.limits);
      const payload = bound.payload;
      const data = boundedStringify(
        {
          ...payload,
          output: {
            ...spec,
            pixelWidth: dimensions.width,
            pixelHeight: dimensions.height,
            pixels: dimensions.pixels,
            generatedAt: new Date().toISOString(),
          },
        },
        this.limits.dataBytes,
      );
      const image = await (this.overrides?.image ?? renderChartExportImage)(
        payload,
        spec,
        this.limits,
        checkpoint,
        ctrl.signal,
      );
      await checkpoint();
      if (image.size > (spec.type === 'svg' ? this.limits.svgBytes : this.limits.imageBytes)) {
        throw chartExportCapacityError();
      }
      const descFn = this.overrides?.descriptions ?? chartExportDescriptions;
      const text = `${localize('ui.chartExport.excellensChartExportPackageSource', { title: payload.chart.title, source: payload.chart.source, metric: payload.chart.metric, unit: payload.chart.unit, descFn: descFn(payload).join('\n'), type: spec.type })}
`;
      const encoder = new TextEncoder();
      const bytes = new Uint8Array(await image.arrayBuffer());
      await checkpoint();
      const blob = await buildZipBlob(
        [
          { name: `chart.${spec.type}`, bytes },
          { name: 'exact-data.json', bytes: encoder.encode(data) },
          { name: 'README.txt', bytes: encoder.encode(text) },
        ],
        this.limits.packageBytes,
        checkpoint,
      );
      await checkpoint();
      const imageURL = URL.createObjectURL(image);
      candidate = { imageURL, packageURL: '' };
      const packageURL = URL.createObjectURL(blob);
      candidate.packageURL = packageURL;
      if (!current()) throw makeAbortError();
      const previous = this.accepted;
      this.accepted = { spec, image, blob, imageURL, packageURL };
      candidate = null;
      requireEl('chartExportPackageInfo').textContent =
        `${spec.type.toUpperCase()} · ${dimensions.width} × ${dimensions.height} · ${formatBytes(image.size)}`;
      const imgDownload = requireAnchor('chartExportImageDownload');
      imgDownload.href = this.accepted.imageURL;
      imgDownload.download = `ExcelLens-Chart.${spec.type}`;
      requireAnchor('chartExportDownload').href = this.accepted.packageURL;
      const previewImg = requireImage('chartExportPreview');
      previewImg.src = this.accepted.imageURL;
      previewImg.alt = localize('ui.chartExport.exportPreview', { title: payload.chart.title });
      requireEl('chartExportResult').hidden = false;
      this.revoke(previous);
      this.status('');
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(
          `${errorMessage(error)}${this.accepted ? localize('ui.common.keepPreviousResult') : ''}`,
          true,
        );
      }
    } finally {
      activity?.end();
      this.revoke(candidate);
      if (serial === this.serial) {
        this.abort = null;
        this.busy = false;
        this.controls();
        if (!bound.current()) this.close();
      }
    }
  }
}

let sharedController: ChartExportController | null = null;

export function getChartExportController(
  limits?: ChartExportLimits,
  activity?: ActivityService,
): ChartExportController {
  sharedController ??= new ChartExportController(
    limits ?? requireFileFormats().chartExport,
    activity,
  );
  return sharedController;
}

export function openChartExport(input: ChartExportOpenInput): boolean {
  return getChartExportController().open(input);
}

export function revalidateChartExport(): boolean {
  return sharedController?.revalidate() ?? false;
}

export function closeChartExport(): void {
  sharedController?.close();
}

export const ChartExportService = Object.freeze({
  Controller: ChartExportController,
  open: openChartExport,
  revalidate: revalidateChartExport,
  close: closeChartExport,
});
