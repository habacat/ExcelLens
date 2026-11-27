/**
 * Standalone entry point for `storage-recovery.html`.
 * Polls `/api/storage/recovery`, allows retrying a pending recovery transaction,
 * and shows current storage group usage without pulling in the main workbook bundle.
 */
import type { StorageRecoveryResponse, StorageUsageResponse } from '../contracts/features.ts';
import { errorMessage, formatBytes } from '../core/display-format.ts';
import { readAppToken, requireButton, requireEl } from '../core/dom.ts';

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function readError(value: unknown): string {
  return isRecord(value) && typeof value.error === 'string' ? value.error : '';
}

function initStorageRecovery(): void {
  const button = requireButton('recoveryRetry');
  const status = requireEl('recoveryProgress');
  const headers: Record<string, string> = { 'X-ExcelLens-Token': readAppToken() };

  async function update(method: 'GET' | 'POST'): Promise<void> {
    button.disabled = true;
    try {
      const response = await fetch('/api/storage/recovery', {
        method,
        headers,
        cache: 'no-store',
      });
      const raw: unknown = await response.json();
      if (!response.ok) {
        throw new Error(readError(raw) || '无法读取恢复事务');
      }
      const result = raw as StorageRecoveryResponse;
      if (!result.pending) {
        window.location.replace('/');
        return;
      }
      const reason = requireEl('recoveryReason');
      reason.hidden = result.running;
      reason.textContent = result.running ? '' : result.reason;
      requireEl('recoveryDirectories').textContent =
        `配置目录　${result.roots.config}\n数据目录　${result.roots.local}`;
      const progress = result.progress;
      const transferred = progress.total
        ? `${formatBytes(progress.bytes || 0)} / ${formatBytes(progress.total)}`
        : '';
      const files = progress.items ? `${progress.files || 0} / ${progress.items} 项` : '';
      status.textContent = result.running
        ? [progress.phase, transferred, files].filter(Boolean).join(' · ')
        : '';
      button.disabled = result.running;
      if (result.running) {
        setTimeout(() => {
          void update('GET');
        }, 600);
      }
    } catch (error: unknown) {
      status.textContent = errorMessage(error);
      button.disabled = false;
    }
  }

  const usage = requireEl('recoveryUsage') as HTMLDetailsElement;
  usage.addEventListener('toggle', () => {
    if (!usage.open) return;
    const holder = requireEl('recoveryGroups');
    void (async () => {
      try {
        const response = await fetch('/api/storage', { headers, cache: 'no-store' });
        const raw: unknown = await response.json();
        if (!response.ok) throw new Error(readError(raw) || `请求失败 (${response.status})`);
        const data = raw as StorageUsageResponse;
        holder.replaceChildren(
          ...(data.groups ?? []).map((group) => {
            const row = document.createElement('p');
            row.textContent = `${group.name}　${group.error || formatBytes(group.allocated)}`;
            return row;
          }),
        );
      } catch (error: unknown) {
        holder.textContent = errorMessage(error);
      }
    })();
  });

  button.addEventListener('click', () => {
    void update('POST');
  });
  void update('GET');
}

initStorageRecovery();
