/**
 * Analysis number presentation settings modal controller.
 */
import type { AnalysisDisplayConfig, AnalysisDisplayOptions } from '../contracts/analysis.ts';
import type { ApiClient, HttpError, ToastFn } from '../contracts/common.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import {
  DEFAULT_ANALYSIS_NUMBER_OPTIONS,
  formatAnalysisNumber,
  getAnalysisNumberOptions,
  isValidAnalysisNumberOptions,
  setAnalysisNumberOptions,
} from './numbers.ts';

export interface AnalysisSettingsOptions {
  readonly api: ApiClient;
  readonly toast: ToastFn;
}

function requireFieldSet(id: string): HTMLFieldSetElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFieldSetElement)) {
    throw new Error(`Expected HTMLFieldSetElement for #${id}`);
  }
  return node;
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function parseExponent(raw: string): 9 | 12 | 15 {
  const n = Number(raw);
  if (n === 9 || n === 12 || n === 15) return n;
  return 12;
}

export class AnalysisSettings {
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private serial = 0;
  private config: AnalysisDisplayConfig | null = null;
  private saving = false;
  public readonly dialog: HTMLDialogElement;

  constructor(options: AnalysisSettingsOptions) {
    this.api = options.api;
    this.toast = options.toast;
    this.dialog = requireDialog('analysisSettingsDialog');

    requireButton('analysisSettingsOpen').onclick = (): void => {
      this.dialog.showModal();
      void this.load();
    };
    requireButton('analysisSettingsClose').onclick = (): void => this.dialog.close();
    requireButton('analysisSettingsReload').onclick = (): void => {
      void this.load();
    };
    requireEl('analysisSettingsForm').onsubmit = (event: SubmitEvent): void => {
      event.preventDefault();
      void this.save();
    };
    requireFieldSet('analysisSettingsFields').onchange = (): void => this.preview();
    requireButton('analysisSettingsDefaults').onclick = (): void => {
      this.fill(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
    };
    this.dialog.addEventListener('cancel', (event: Event) => {
      if (this.saving) event.preventDefault();
    });
    this.fill(getAnalysisNumberOptions());
  }

  draft(): AnalysisDisplayOptions {
    return {
      groupThousands: requireInput('analysisGroupThousands').checked,
      scientific: requireInput('analysisScientific').checked,
      scientificExponent: parseExponent(requireSelect('analysisScientificExponent').value),
    };
  }

  fill(options: AnalysisDisplayOptions): void {
    requireInput('analysisGroupThousands').checked = options.groupThousands;
    requireInput('analysisScientific').checked = options.scientific;
    requireSelect('analysisScientificExponent').value = String(options.scientificExponent);
    this.preview();
  }

  preview(): void {
    const options = this.draft();
    requireSelect('analysisScientificExponent').disabled = !options.scientific;
    const previews: readonly (readonly [string, string])[] = [
      ['analysisNumberPreview', '1234567.89'],
      ['analysisLargePreview', '9007199254741015.11'],
    ];
    for (const [id, value] of previews) {
      const node = requireEl(id);
      node.textContent = formatAnalysisNumber({ display: value, exact: value }, options);
      node.title = `精确值：${value}`;
    }
  }

  status(text: string, error = false): void {
    const node = requireEl('analysisSettingsStatus');
    node.textContent = text;
    node.setAttribute('role', error ? 'alert' : 'status');
    node.classList.toggle('has-error', error);
  }

  busy(value: boolean): void {
    requireFieldSet('analysisSettingsFields').disabled = value;
    requireButton('analysisSettingsSave').disabled = value || !this.config;
    requireButton('analysisSettingsReload').disabled = value;
    requireButton('analysisSettingsClose').disabled = this.saving;
  }

  accept(config: AnalysisDisplayConfig | null | undefined): void {
    if (
      config?.version !== 1 ||
      !Number.isSafeInteger(config.revision) ||
      config.revision < 0 ||
      !isValidAnalysisNumberOptions(config.options)
    ) {
      throw new Error('分析显示设置响应无效');
    }
    this.config = config;
    const changed = JSON.stringify(getAnalysisNumberOptions()) !== JSON.stringify(config.options);
    setAnalysisNumberOptions(config.options);
    if (changed) {
      document.dispatchEvent(new Event('excellens:analysisnumberchange'));
    }
  }

  async load(): Promise<void> {
    if (this.saving) return;
    const serial = ++this.serial;
    this.config = null;
    this.busy(true);
    this.status('正在读取设置…');
    try {
      const config = await this.api<AnalysisDisplayConfig>('/api/settings/analysis-display');
      if (serial !== this.serial) return;
      this.accept(config);
      this.fill(config.options);
      this.status('');
    } catch (error: unknown) {
      if (serial === this.serial) {
        const message = errorMessage(error);
        this.status(message, true);
        if (!this.dialog.open) this.toast(`显示设置未能载入：${message}`, 'warning');
      }
    } finally {
      if (serial === this.serial) this.busy(false);
    }
  }

  async save(): Promise<void> {
    if (!this.config || this.saving) return;
    const serial = ++this.serial;
    const options = this.draft();
    const expectedRevision = this.config.revision;
    this.saving = true;
    this.busy(true);
    this.status('正在保存…');
    try {
      const config = await this.api<AnalysisDisplayConfig>('/api/settings/analysis-display', {
        method: 'POST',
        body: JSON.stringify({ expectedRevision, options }),
      });
      if (serial !== this.serial) return;
      this.accept(config);
      this.status('已保存');
      this.dialog.close();
      this.toast('分析显示设置已保存', 'success');
    } catch (error: unknown) {
      if (serial === this.serial) {
        const status = getHttpStatus(error);
        if (status === 409 || status === 422) this.config = null;
        this.status(errorMessage(error), true);
      }
    } finally {
      if (serial === this.serial) {
        this.saving = false;
        this.busy(false);
      }
    }
  }
}
