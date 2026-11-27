import type { EditOpenWorkbookResponse } from '../contracts/edit.ts';
import type { WorkbookStatusResponse } from '../contracts/workbook.ts';
import { decodeClosedWorkbookWire, decodeOpenedWorkbookWire } from './http-decoders.generated.ts';

export function decodeOpenedWorkbook(value: unknown): EditOpenWorkbookResponse {
  const book = decodeOpenedWorkbookWire(value);
  if (!book.opened || !book.book || !book.workbookKey || !book.source.format ||
      !Number.isSafeInteger(book.fileSize) || book.fileSize < 0 ||
      book.sheetCount !== (book.sheets?.length ?? 0)) {
    throw new Error('打开工作簿的来源或工作表清单无效');
  }
  const header = book.source.tableHeader;
  return {
    ...book,
    opened: true,
    sheets: book.sheets ?? [],
    source: { ...book.source, tableHeader: header ? { ...header, columns: header.columns ?? [] } : undefined },
  };
}

/** A status request can also report an idle session or an archive directory. */
export function decodeWorkbookStatus(value: unknown): WorkbookStatusResponse {
  if (typeof value === 'object' && value !== null && 'opened' in value && value.opened === true) {
    return decodeOpenedWorkbook(value);
  }
  const closed = decodeClosedWorkbookWire(value);
  if (closed.opened || (closed.archiveOnly && !closed.archive)) {
    throw new Error('压缩包目录或工作簿状态无效');
  }
  return closed;
}

export function decodeOpenResult(value: unknown): WorkbookStatusResponse {
  const status = decodeWorkbookStatus(value);
  if (!status.opened && !status.archiveOnly) throw new Error('服务未确认工作簿已打开');
  return status;
}
