/**
 * Format registry adapter. Configuration comes from `ui.Index` and `internal/formats`,
 * never a second extension list. Independent of workbook state.
 */
import type {
  FileFormatsConfig,
  FileFormatsRegistry,
  SourceCapabilities,
} from '../contracts/formats.ts';

import { decodeFileFormatsConfig } from './http-decoders.generated.ts';

export type { FileFormatsRegistry, FileFormatsRegistry as FileFormats };

let cachedRaw: string | null = null;
let cachedInstance: FileFormatsRegistry | null = null;

export function createFileFormats(config: FileFormatsConfig): FileFormatsRegistry {
  const extensions = new Set(config.extensions);
  const sources = config.sources;
  const sourceValues = Object.values(sources);
  const fallbackSource = sources.xlsx;
  if (!fallbackSource || sourceValues.some(source =>
    !Number.isSafeInteger(source.maxRows) || source.maxRows < 1 ||
    !Number.isSafeInteger(source.maxColumns) || source.maxColumns < 1
  )) {
    throw new Error('Invalid fileFormats source capabilities');
  }
  const maxRows = Math.max(...sourceValues.map(source => source.maxRows));
  const maxColumns = Math.max(...sourceValues.map(source => source.maxColumns));

  return {
    raw: config,
    ...config,
    maxRows,
    maxColumns,
    supports(name: string): boolean {
      const dot = name.lastIndexOf('.');
      return dot >= 0 && extensions.has(name.slice(dot).toLowerCase());
    },
    source(id?: string): SourceCapabilities {
      if (id) {
        const found = sources[id];
        if (found) return found;
      }
      return fallbackSource;
    },
  };
}

export function currentFileFormats(): FileFormatsRegistry | null {
  const text =
    typeof document !== 'undefined' ? (document.getElementById('fileFormats')?.textContent ?? '') : '';
  if (cachedInstance && text === cachedRaw) return cachedInstance;
  if (!text) return null;
  cachedInstance = createFileFormats(decodeFileFormatsConfig(JSON.parse(text)));
  cachedRaw = text;
  return cachedInstance;
}

export function requireFileFormats(): FileFormatsRegistry {
  const registry = currentFileFormats();
  if (!registry) {
    throw new Error('Missing #fileFormats bootstrap configuration');
  }
  return registry;
}

export function resetFileFormatsCache(): void {
  cachedRaw = null;
  cachedInstance = null;
}
