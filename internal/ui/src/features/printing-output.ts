/**
 * Bounded standalone printable HTML manuscript builder for print preview export.
 */
import { interfaceLocale, t as localize, th as localizeHTML } from '../core/i18n.ts';
import type { PrintingLimits } from '../contracts/formats.ts';
import type { RenderedPrintPage } from './printing-model.ts';

export function parsePageRange(text: string, total: number, limit: number): number[] {
  const selected = new Set<number>();
  for (const part of text.trim().split(/[,，]/)) {
    const match = /^\s*(\d+)(?:\s*[-–]\s*(\d+))?\s*$/.exec(part);
    if (!match?.[1]) throw new Error(localize('ui.printingOutput.pageSegmentsCanBeFilled'));
    const first = Number(match[1]);
    const last = Number(match[2] || match[1]);
    if (!Number.isSafeInteger(last) || first < 1 || last < first || last > total) {
      throw new Error(localize('ui.printingOutput.outputPageSegmentExceedsConfirmed'));
    }
    if (last - first + 1 > limit) throw new Error(localize('ui.common.numberPagesBeExportedExceeds'));
    for (let page = first; page <= last; page++) {
      selected.add(page - 1);
      if (selected.size > limit) throw new Error(localize('ui.common.numberPagesBeExportedExceeds'));
    }
  }
  return [...selected].sort((a, b) => a - b);
}

function paperCSS(): string {
  const rules: string[] = [];
  for (const sheet of Array.from(document.styleSheets)) {
    if (!sheet.href || new URL(sheet.href).origin !== location.origin) continue;
    for (const rule of Array.from(sheet.cssRules)) {
      if (
        rule instanceof CSSStyleRule &&
        rule.selectorText
          .split(',')
          .some((s) => /^\.(print-(paper|data|edges?|body|cell|source-heading)|cell-rich)/.test(s.trim()))
      ) {
        rules.push(rule.cssText);
      }
    }
  }
  return rules.join('\n');
}

export class PrintOutput {
  private readonly title: string;
  private readonly limits: Pick<PrintingLimits, 'outputPages' | 'outputBytes'>;
  public readonly parts: string[] = [];
  private readonly sizes = new Map<string, string>();
  private readonly notes = new Set<string>();
  private bytes = 0;
  private readonly encoder = new TextEncoder();

  constructor(title: string, limits: Pick<PrintingLimits, 'outputPages' | 'outputBytes'>) {
    this.title = title;
    this.limits = limits;
  }

  add(
    rendered: RenderedPrintPage,
    source: string,
    page: number,
    notes: readonly string[],
  ): void {
    if (this.parts.length >= this.limits.outputPages) {
      throw new Error(localize('ui.common.numberPagesBeExportedExceeds'));
    }
    const paper = rendered.node;
    paper.style.transform = 'none';
    const size = `${rendered.width}px ${rendered.height}px`;
    let pageName = this.sizes.get(size);
    if (!pageName) {
      pageName = `paper${this.sizes.size}`;
      this.sizes.set(size, pageName);
    }
    paper.style.setProperty('page', pageName);
    paper.setAttribute('aria-label', localize('ui.printingOutput.page', { source, page }));
    for (const cell of paper.querySelectorAll('[data-missing-cache]')) {
      if (!cell.textContent.trim()) {
        cell.textContent = cell.getAttribute('data-missing-cache') ?? '';
      }
    }
    const html = paper.outerHTML;
    this.bytes += this.encoder.encode(html).length;
    if (this.bytes > this.limits.outputBytes) throw new Error(localize('ui.printingOutput.subdocumentContentExceedsCapacityExport'));
    for (const note of [...notes, ...rendered.warnings]) {
      if (note) this.notes.add(note);
    }
    this.parts.push(html);
  }

  file(): Blob {
    if (!this.parts.length) throw new Error(localize('ui.printingOutput.thereNoPrintablePagesSelected'));
    const safe = (text: string): string => {
      const el = document.createElement('span');
      el.textContent = text;
      return el.innerHTML;
    };
    const pages = [...this.sizes]
      .map(([size, name]) => `@page ${name}{size:${size};margin:0}`)
      .join('\n');
    const css =
      `${paperCSS()}\n${pages}` +
      '\n*{box-sizing:border-box}html,body{margin:0;padding:0}body{background:#eee}.print-paper{position:relative;left:auto;top:auto;margin:12px auto;break-after:page;break-inside:avoid;print-color-adjust:exact;-webkit-print-color-adjust:exact}.print-paper:last-child{break-after:auto}.notes{font:14px sans-serif;padding:12px;color:#222}.print-paper [data-missing-cache]{color:#765200}@media print{body{background:white}.notes{display:none}.print-paper{margin:0;box-shadow:none}}';
    const notes = [...this.notes, localize('ui.printingOutput.fontProvidedSystemThatOpens')];
    const head =
      '<!doctype html><html lang="' + safe(interfaceLocale()) + '"><meta charset="utf-8">' +
      `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">` +
      `<title>${safe(this.title)}</title><style>${css}</style><body><section class="notes">` +
      `<strong>${safe(this.title)}</strong><p>${localizeHTML('ui.printingOutput.pagePrintableOrPdfFormat', { count: this.parts.length })}</p>` +
      `${notes.map((n) => `<p>${safe(n)}</p>`).join('')}</section>`;
    const blob = new Blob([head, ...this.parts, '</body></html>'], {
      type: 'text/html;charset=utf-8',
    });
    if (blob.size > this.limits.outputBytes) throw new Error(localize('ui.printingOutput.subdocumentContentExceedsCapacityExport'));
    return blob;
  }
}

export const PrintingOutput = Object.freeze({
  pageRange: parsePageRange,
  Output: PrintOutput,
});
