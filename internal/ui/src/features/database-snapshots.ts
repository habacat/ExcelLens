/**
 * Retained database snapshots manager: filtering, storage/review policy reminders,
 * staging diagnostics, cryptographic verification, reference-checked deletion, and opening.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { WorkbookStatusResponse } from '../contracts/workbook.ts';
import type { ApiClient } from '../contracts/common.ts';
import type { DatabaseSnapshotSummary } from '../contracts/features.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
} from '../core/dom.ts';
import { DatabaseEvidence } from './database-evidence.ts';
import {
  decodeDatabaseEvidenceVerifyResult,
  decodeDatabaseSnapshotPolicy,
  decodeDatabaseSnapshotRemovalResponse,
  decodeDatabaseSnapshotsListResponse,
} from '../core/http-decoders.generated.ts';
import { decodeOpenedWorkbook } from '../core/workbook-response.ts';

/** @httpResponse DatabaseSnapshotPolicy */
export interface DatabaseSnapshotPolicy {
  readonly budgetBytes?: number;
  readonly reviewDays?: number;
}

export interface DatabaseSnapshotStage {
  readonly name: string;
  readonly bytes: number;
  readonly state: string;
  readonly error?: string;
}

/** @httpResponse DatabaseSnapshotsListResponse */
export interface DatabaseSnapshotsListResponse {
  readonly items?: readonly DatabaseSnapshotSummary[];
  readonly policy?: DatabaseSnapshotPolicy;
  readonly policyError?: string;
  readonly stages?: readonly DatabaseSnapshotStage[];
  readonly stageRoot?: string;
}

/** @httpResponse DatabaseSnapshotRemovalResponse */
export interface DatabaseSnapshotRemovalResponse {
  readonly references?: readonly string[];
}

export interface DatabaseSnapshotsOptions {
  readonly api: ApiClient;
  readonly fmtBytes: (value: unknown) => string;
  readonly openWorkbook: (
    loader: () => Promise<WorkbookStatusResponse>,
    fileName: string,
    fileSize: number,
    guard: () => boolean,
  ) => Promise<boolean>;
  readonly onRetained: (key: string) => void;
}

const ENDPOINT = '/api/database/snapshots';

const STAGE_STATES: Readonly<Record<string, string>> = {
  active: localize('ui.common.use'),
  orphan: localize('ui.databaseSnapshots.interruptedNextTimeStartCleaning'),
  unknown: localize('ui.databaseSnapshots.ownershipNotConfirmedCheckDirectory'),
};

function requireForm(id: string): HTMLFormElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFormElement)) {
    throw new Error(`Expected HTMLFormElement for #${id}`);
  }
  return node;
}

function formInput(form: HTMLFormElement, name: string): HTMLInputElement {
  const item = form.elements.namedItem(name);
  if (!(item instanceof HTMLInputElement)) {
    throw new Error(`Expected HTMLInputElement named "${name}"`);
  }
  return item;
}

export class DatabaseSnapshots {
  private readonly api: ApiClient;
  private readonly fmtBytes: (value: unknown) => string;
  private readonly openWorkbook: DatabaseSnapshotsOptions['openWorkbook'];
  private readonly onRetained: (key: string) => void;
  public readonly dialog: HTMLDialogElement;
  private readonly list: HTMLElement;
  public readonly evidence: DatabaseEvidence;
  private readonly filters: HTMLFormElement;
  private readonly policyForm: HTMLFormElement;

  private items: readonly DatabaseSnapshotSummary[] = [];
  private policy: DatabaseSnapshotPolicy = {};
  private removing: DatabaseSnapshotSummary | null = null;
  private controller: AbortController | null = null;

  constructor(options: DatabaseSnapshotsOptions) {
    this.api = options.api;
    this.fmtBytes = options.fmtBytes;
    this.openWorkbook = options.openWorkbook;
    this.onRetained = options.onRetained;
    this.dialog = requireDialog('databaseSnapshotsDialog');
    this.list = requireEl('databaseSnapshotsList');
    this.evidence = new DatabaseEvidence(options.api, options.fmtBytes);
    this.filters = requireForm('dbSnapshotFilters');
    this.policyForm = requireForm('dbSnapshotPolicy');

    this.filters.addEventListener('input', () => this.render());
    this.filters.addEventListener('submit', (event: SubmitEvent) => event.preventDefault());
    this.filters.addEventListener('reset', () => queueMicrotask(() => this.render()));
    this.policyForm.addEventListener('submit', (event: SubmitEvent) => {
      event.preventDefault();
      this.savePolicy();
    });
    requireButton('dbSnapshotRemovalBack').addEventListener('click', () => this.setRemoval(null));
    requireButton('dbSnapshotRemovalConfirm').addEventListener('click', () => this.confirmRemoval());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    for (const id of ['databaseSnapshotsClose', 'databaseSnapshotsCancel']) {
      requireButton(id).addEventListener('click', () => this.close());
    }
  }

  show(legacyKey = ''): void {
    if (this.dialog.open) return;
    this.items = [];
    this.filters.reset();
    this.setRemoval(null);
    requireEl('dbSnapshotUsage').textContent = '';
    requireEl('dbSnapshotMatches').textContent = '';
    requireEl('dbSnapshotStages').hidden = true;
    this.list.replaceChildren();
    this.dialog.showModal();
    if (legacyKey) {
      this.retain(legacyKey);
      return;
    }
    void this.run(localize('ui.databaseSnapshots.readingSnapshot'), (signal) => this.refresh(signal));
  }

  private async refresh(signal: AbortSignal, message = ''): Promise<void> {
    const result = await this.api(ENDPOINT, { signal }, decodeDatabaseSnapshotsListResponse);
    if (signal.aborted) return;
    this.items = result.items ?? [];
    this.policy = result.policy ?? {};
    formInput(this.policyForm, 'budget').value = this.policy.budgetBytes
      ? String(this.policy.budgetBytes / 1024 ** 3)
      : '';
    formInput(this.policyForm, 'days').value = this.policy.reviewDays
      ? String(this.policy.reviewDays)
      : '';
    const policyBtn = this.policyForm.querySelector('button');
    if (policyBtn) policyBtn.disabled = Boolean(result.policyError);
    this.renderStages(result.stages ?? [], result.stageRoot ?? '');
    this.render();
    this.feedback(result.policyError || message, Boolean(result.policyError));
  }

  private renderStages(stages: readonly DatabaseSnapshotStage[], root: string): void {
    const area = requireEl('dbSnapshotStages');
    const body = area.querySelector('div');
    area.hidden = !stages.length;
    if (!body) return;
    body.replaceChildren();
    if (!stages.length) return;
    const summaryEl = area.querySelector('summary');
    if (summaryEl) {
      summaryEl.textContent = localize('ui.databaseSnapshots.dataCollectionTemporaryStorage', { fmtBytes: this.fmtBytes(stages.reduce((n, s) => n + s.bytes, 0)) });
    }
    const location = document.createElement('p');
    location.textContent = root;
    body.append(location);
    for (const stage of stages) {
      const line = document.createElement('p');
      const state = STAGE_STATES[stage.state] ?? '';
      line.textContent = [stage.name, this.fmtBytes(stage.bytes), stage.error || state].join(' · ');
      body.append(line);
    }
  }

  private savePolicy(): void {
    const budget = formInput(this.policyForm, 'budget').valueAsNumber;
    const days = formInput(this.policyForm, 'days').valueAsNumber;
    const policy = {
      budgetBytes: Math.round((budget || 0) * 1024 ** 3),
      reviewDays: days || 0,
    };
    void this.run(localize('ui.databaseSnapshots.savingReminderProgress'), async (signal) => {
      const saved = await this.api(ENDPOINT, {
        method: 'POST',
        body: JSON.stringify({ action: 'policy', policy }),
        signal,
      }, decodeDatabaseSnapshotPolicy);
      if (signal.aborted) return;
      this.policy = saved;
      this.render();
      this.feedback(localize('ui.databaseSnapshots.savedReminder'));
    });
  }

  private due(item: DatabaseSnapshotSummary): boolean {
    const reviewDays = this.policy.reviewDays;
    return (
      Boolean(reviewDays) &&
      !item.error &&
      Date.now() - new Date(item.taken).getTime() >= (reviewDays ?? 0) * 86400000
    );
  }

  private request(action: string, key: string, signal: AbortSignal): Promise<unknown> {
    return this.api(ENDPOINT, {
      method: 'POST',
      body: JSON.stringify({ action, key }),
      signal,
    });
  }

  render(): void {
    const lower = (value: string): string => value.trim().toLocaleLowerCase();
    const minInput = formInput(this.filters, 'min');
    const maxInput = formInput(this.filters, 'max');
    const fromInput = formInput(this.filters, 'from');
    const untilInput = formInput(this.filters, 'until');
    const sourceInput = formInput(this.filters, 'source');
    const tableInput = formInput(this.filters, 'table');
    const dueInput = formInput(this.filters, 'due');

    const min = minInput.valueAsNumber;
    const max = maxInput.valueAsNumber;
    const from = fromInput.value;
    const until = untilInput.value;
    const source = lower(sourceInput.value);
    const table = lower(tableInput.value);
    const invalid =
      !this.filters.checkValidity() || min > max || (Boolean(from) && Boolean(until) && from > until);

    const items = invalid
      ? []
      : this.items.filter((item) => {
          const date = new Date(item.taken);
          const day = [
            date.getFullYear(),
            String(date.getMonth() + 1).padStart(2, '0'),
            String(date.getDate()).padStart(2, '0'),
          ].join('-');
          if (
            (from || until) &&
            (Boolean(item.error) || (Boolean(from) && day < from) || (Boolean(until) && day > until))
          ) {
            return false;
          }
          const sourceParts = item.source
            ? [
                item.source.engine,
                item.source.protocol,
                item.source.host,
                String(item.source.port),
                item.source.database ?? '',
                item.source.tls,
              ]
            : [];
          const origin = [item.name, ...sourceParts].join(' ').toLocaleLowerCase();
          return (
            !(item.bytes < min * 1048576 || item.bytes > max * 1048576) &&
            (!source || origin.includes(source)) &&
            (!table || Boolean(item.tables?.some((name) => lower(name).includes(table)))) &&
            (!dueInput.checked || this.due(item))
          );
        });

    const bytes = this.items.reduce((sum, item) => sum + item.bytes, 0);
    const summary = [localize('ui.databaseSnapshots.totalCopies', { count: this.items.length }), localize('ui.databaseSnapshots.directoryOccupiesSpace', { fmtBytes: this.fmtBytes(bytes) })];
    const budget = this.policy.budgetBytes;
    if (budget) {
      summary.push(`${bytes >= budget ? `${localize('ui.databaseSnapshots.capacityAlertReached')} ` : `${localize('ui.databaseSnapshots.reminderCapacity')} `}${this.fmtBytes(budget)}`);
    }
    if (this.items.some((item) => Boolean(item.error))) {
      summary.push(localize('ui.databaseSnapshots.containsAbnormalDirectoryCheckCapacity'));
    }
    requireEl('dbSnapshotUsage').textContent = summary.join(' · ');

    const active = Boolean(
      source || table || from || until || minInput.value || maxInput.value || dueInput.checked,
    );
    requireEl('dbSnapshotMatches').textContent = invalid
      ? localize('ui.databaseSnapshots.verifyDateCapacityRange')
      : active
        ? localize('ui.databaseSnapshots.numberMatchingItems', { count: items.length, fmtBytes: this.fmtBytes(items.reduce((sum, item) => sum + item.bytes, 0)) })
        : '';

    this.list.replaceChildren();
    for (const item of items) {
      const row = document.createElement('div');
      row.className = 'database-snapshot-entry';
      const identity = document.createElement('div');
      identity.className = 'database-snapshot-identity';
      const name = document.createElement('strong');
      const details = document.createElement('small');
      name.textContent = item.name;
      const parts = [
        new Date(item.taken).toLocaleString(interfaceLocale()),
        localize('ui.databaseSnapshots.table', { sheets: (item.sheets || 0).toLocaleString(interfaceLocale()) }),
      ];
      if (item.retained) {
        parts.push(localize('ui.databaseSnapshots.thereRecords', { records: item.records.toLocaleString(interfaceLocale()) }), this.fmtBytes(item.bytes));
      } else {
        parts.push(localize('ui.databaseSnapshots.oldVersionSnapshot'), this.fmtBytes(item.bytes));
      }
      if (item.legacyKey) parts.push(localize('ui.databaseSnapshots.oldVersionSavedSnapshot'));
      if (this.due(item)) parts.push(localize('ui.databaseSnapshots.beReviewedFurther'));
      details.textContent = item.error || parts.join(' · ');
      identity.append(name, details);
      row.append(identity);

      const actions = document.createElement('div');
      actions.className = 'database-actions';
      const secondary: readonly [string, () => void] = item.retained
        ? [localize('ui.databaseSnapshots.verificationDetails'), () => this.verify(item)]
        : [localize('ui.databaseSnapshots.save'), () => this.retain(item.key)];
      const choices: (readonly [string, () => void])[] = item.error
        ? []
        : [secondary, [localize('ui.databaseSnapshots.openUp'), () => this.open(item)]];
      choices.push([localize('ui.common.delete'), () => this.inspectRemoval(item)]);
      for (const [text, work] of choices) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'mini-button';
        button.textContent = text;
        button.setAttribute('aria-label', `${text} ${item.name}`);
        button.addEventListener('click', work);
        actions.append(button);
      }
      row.append(actions);
      this.list.append(row);
    }

    if (!items.length) {
      const empty = document.createElement('p');
      empty.className = 'database-empty';
      empty.textContent = this.items.length ? localize('ui.databaseSnapshots.noMatchingSnapshotsFound') : localize('ui.databaseSnapshots.noSavedDatabaseSnapshotAvailable');
      this.list.append(empty);
    }
  }

  private retain(key: string): void {
    void this.run(localize('ui.databaseSnapshots.savingOldSnapshotsProgress'), async (signal) => {
      await this.request('retain', key, signal);
      if (signal.aborted) return;
      this.onRetained(key);
      await this.refresh(signal, localize('ui.databaseSnapshots.oldSnapshotsSaved'));
    });
  }

  private verify(item: DatabaseSnapshotSummary): void {
    void this.run(localize('ui.databaseSnapshots.verifying', { name: item.name }), async (signal) => {
      const result = await this.request('verify', item.key, signal).then(decodeDatabaseEvidenceVerifyResult);
      if (signal.aborted) return;
      if (!result.verified || result.key !== item.key) {
        throw new Error(localize('ui.databaseSnapshots.snapshotVerificationResultDoesNot'));
      }
      this.feedback('');
      this.evidence.show(result);
    });
  }

  private setRemoval(item: DatabaseSnapshotSummary | null): void {
    this.removing = item;
    requireEl('dbSnapshotRemoval').hidden = item === null;
    requireEl('dbSnapshotControls').hidden = item !== null;
    requireEl('dbSnapshotMatches').hidden = item !== null;
    this.list.hidden = item !== null;
    requireButton('dbSnapshotRemovalConfirm').hidden = true;
  }

  private inspectRemoval(item: DatabaseSnapshotSummary): void {
    void this.run(localize('ui.databaseSnapshots.checkingSnapshotReferences'), async (signal) => {
      const result = await this.api(`${ENDPOINT}/removal`, {
        method: 'POST',
        body: JSON.stringify({ key: item.key }),
        signal,
      }, decodeDatabaseSnapshotRemovalResponse);
      if (signal.aborted) return;
      this.setRemoval(item);
      requireEl('dbSnapshotRemovalName').textContent = `${item.name} · ${this.fmtBytes(item.bytes)}`;
      const refs = result.references ?? [];
      requireEl('dbSnapshotRemovalReason').textContent = refs.length
        ? localize('ui.databaseSnapshots.stillReferenced', { value: refs.join('、') })
        : localize('ui.databaseSnapshots.noResultsFromLocalWork');
      requireButton('dbSnapshotRemovalConfirm').hidden = refs.length > 0;
      requireButton('dbSnapshotRemovalBack').focus();
      this.feedback('');
    });
  }

  private confirmRemoval(): void {
    const item = this.removing;
    if (!item || requireButton('dbSnapshotRemovalConfirm').hidden) return;
    void this.run(localize('ui.databaseSnapshots.recheckingReferencesRemovingDuplicates'), async (signal) => {
      await this.api(`${ENDPOINT}/removal`, {
        method: 'POST',
        body: JSON.stringify({ key: item.key, confirm: true }),
        signal,
      });
      if (signal.aborted) return;
      this.setRemoval(null);
      await this.refresh(signal, localize('ui.databaseSnapshots.deleted', { name: item.name }));
    });
  }

  private open(item: DatabaseSnapshotSummary): void {
    void this.run(localize('ui.databaseSnapshots.verifyingOpeningProgress', { name: item.name }), async (signal) => {
      const opened = await this.openWorkbook(
        () => this.request('open', item.key, signal).then(decodeOpenedWorkbook),
        '',
        0,
        () => !signal.aborted,
      );
      if (opened && !signal.aborted) this.dialog.close();
    });
  }

  private feedback(message: string, error = false): void {
    const status = requireEl('databaseSnapshotsStatus');
    status.textContent = message;
    status.classList.toggle('is-error', error);
  }

  private async run(message: string, work: (signal: AbortSignal) => Promise<void>): Promise<void> {
    if (this.controller) return;
    const controller = new AbortController();
    this.controller = controller;
    this.setBusy(true);
    this.feedback(message);
    try {
      await work(controller.signal);
    } catch (err: unknown) {
      if (!controller.signal.aborted) this.feedback(errorMessage(err), true);
    } finally {
      if (this.controller === controller) {
        this.controller = null;
        this.setBusy(false);
      }
    }
  }

  private setBusy(busy: boolean): void {
    this.list.inert = busy;
    requireEl('dbSnapshotControls').inert = busy;
    requireButton('dbSnapshotRemovalConfirm').disabled = busy;
    requireButton('dbSnapshotRemovalBack').disabled = busy;
    requireEl('databaseSnapshotsProgress').hidden = !busy;
    requireButton('databaseSnapshotsCancel').textContent = busy ? localize('ui.common.cancel') : localize('ui.common.close');
  }

  close(): void {
    this.controller?.abort();
    this.controller = null;
    this.setBusy(false);
    this.dialog.close();
  }
}
