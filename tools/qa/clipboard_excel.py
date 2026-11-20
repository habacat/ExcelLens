"""Check the production browser clipboard by pasting into a separate Windows Excel instance.

Run with --output in a private validation directory. Add real samples with
--real FILE SHEET RANGE. Requires pywin32, Chrome and the QA_UIPROBE binary.
"""
import argparse
import hashlib
import json
import subprocess
import time
from bisect import bisect_left
from contextlib import contextmanager
from pathlib import Path
from tempfile import TemporaryDirectory

from win32com.client import DispatchEx
import pywintypes
import win32clipboard as clipboard


@contextmanager
def open_clipboard():
    deadline = time.monotonic()+5
    while True:
        try:
            clipboard.OpenClipboard()
            break
        except pywintypes.error:
            if time.monotonic() >= deadline:
                raise
            time.sleep(.025)
    try:
        yield
    finally:
        try:
            clipboard.CloseClipboard()
        except pywintypes.error as error:
            # A delayed format provider can release the clipboard while GetClipboardData
            # renders it. The sequence is checked again before Excel pastes.
            if error.winerror != 1418:  # ERROR_CLIPBOARD_NOT_OPEN
                raise


def snapshot():
    formats = {}
    with open_clipboard():
        fmt = 0
        while True:
            fmt = clipboard.EnumClipboardFormats(fmt)
            if not fmt:
                break
            try:
                value = clipboard.GetClipboardData(fmt)
            except pywintypes.error as error:
                # Delayed clipboard owners can advertise a format without
                # supplying data when Windows asks them to render it.
                if error.winerror:
                    raise
                continue
            if not isinstance(value, (str, bytes)):
                raise ValueError('Existing clipboard contains a non-memory format')
            formats[fmt] = value
    return formats


def restore(formats, sequence):
    with open_clipboard():
        if clipboard.GetClipboardSequenceNumber() != sequence:
            return
        clipboard.EmptyClipboard()
        for fmt, value in formats.items():
            clipboard.SetClipboardData(fmt, value)


def request(browser, **data):
    browser.stdin.write(json.dumps(data) + '\n')
    browser.stdin.flush()
    line = browser.stdout.readline()
    if not line:
        raise RuntimeError('Browser probe exited before replying')
    reply = json.loads(line)
    if 'probeError' in reply:
        raise RuntimeError(reply)
    return reply


def fixture(excel, file):
    book = excel.Workbooks.Add()
    try:
        sheet = book.Worksheets(1)
        sheet.Name = 'Grid'
        sheet.Range('A1:I7').Value2 = tuple(tuple('R%dC%d' % (r, c) for c in range(1, 10)) for r in range(1, 8))
        for ref in ('B2:D3', 'A4:B5', 'E2:G2', 'H2:H5', 'F6:H7'):
            sheet.Range(ref).Merge()
        sheet.Range('B2').Value2 = '标题 <&>\n下一行'
        sheet.Range('E2').NumberFormat = '@'
        sheet.Range('E2').Value2 = '000123'
        sheet.Range('H2').Value2 = 12.5
        sheet.Range('F6').MergeArea.ClearContents()
        visible = book.Worksheets.Add()
        visible.Name = 'Visible'
        visible.Range('A2:C6').Value2 = tuple(tuple(f'{r}-{c}' for c in range(1,4)) for r in range(2,7))
        visible.Range('A2:B5').Merge()
        visible.Rows(3).Hidden = True
        sheet.Activate()
        book.SaveAs(str(file), FileFormat=51)
    finally:
        book.Close(False)


def check(excel, browser, source, target, case):
    sheet = source.Worksheets(case['sheet'])
    source.Activate()
    sheet.Activate()
    sheet.Range(case['address']).Select()
    area = excel.Selection
    left, width = area.Column, area.Columns.Count
    rows = [r for r in range(area.Row,area.Row+area.Rows.Count) if not case.get('visible') or not sheet.Rows(r).Hidden]
    height = len(rows)
    result = {'file':Path(case['file']).name, 'sheet':case['sheet'], 'requested':case['address'],
              'nativeSelection':area.Address, 'mode':case.get('mode', 'shortcut'), 'cells':height*width}
    result.update(request(browser, action='select', **case))
    target.Parent.Activate()
    target.Activate()
    target.UsedRange.UnMerge()
    target.UsedRange.Clear()
    target.Range(target.Cells(4,3),target.Cells(5+height,4+width)).Value2 = 'guard'
    if target.Cells(4,3).Value2 != 'guard' or target.Cells(5+height,4+width).Value2 != 'guard':
        raise AssertionError('Could not initialize the destination border')
    dest = target.Cells(5, 4)
    dest.Select()
    saved = snapshot()
    initial_sequence = clipboard.GetClipboardSequenceNumber()
    sequence = None
    try:
        copied = request(browser, action='copy', mode=result['mode'])
        if copied['toast'] != '已复制':
            raise AssertionError(copied['toast'])
        with open_clipboard():
            sequence = clipboard.GetClipboardSequenceNumber()
            if initial_sequence == sequence:
                sequence = None
                raise AssertionError('The browser did not update the Windows clipboard')
            if not clipboard.IsClipboardFormatAvailable(13):
                raise AssertionError('Windows clipboard has no Unicode text')
            native_html = clipboard.GetClipboardData(clipboard.RegisterClipboardFormat('HTML Format'))
        if b'<table' not in native_html.lower():
            raise AssertionError('Windows clipboard has no HTML table')
        if clipboard.GetClipboardSequenceNumber() != sequence:
            raise AssertionError('Windows clipboard changed before native paste')
        target.Paste()
    finally:
        if sequence is not None:
            restore(saved, sequence)
    mismatches = []
    merges = set()
    for r in range(-1, height+1):
        for c in range(-1, width+1):
            actual = target.Cells(5+r, 4+c)
            if not (0 <= r < height and 0 <= c < width):
                if actual.Value2 != 'guard' or actual.MergeCells:
                    mismatches.append([actual.Address, 'outside pasted rectangle'])
                continue
            cell = sheet.Cells(rows[r], left+c)
            origin = cell.MergeArea
            merged = actual.MergeArea
            origin_row = origin.Row
            first = bisect_left(rows, origin_row)
            last = bisect_left(rows, origin_row+origin.Rows.Count)
            want = (first, origin.Column-left, last-first, origin.Columns.Count) if rows[first]==origin_row else (r,c,1,1)
            got = (merged.Row-5, merged.Column-4, merged.Rows.Count, merged.Columns.Count)
            if want[2:] != (1, 1):
                merges.add(origin.Address)
            value = case.get('expected', cell.Value2) if r==0 and c==0 else cell.Value2
            pasted = actual.Value2
            if want!=got or value!=pasted or isinstance(value,bool)!=isinstance(pasted,bool) or actual.HasFormula:
                mismatches.append([cell.Address, actual.Address, want, got, value, pasted])
    result.update(merges=sorted(merges), mismatches=mismatches, windowsHTMLBytes=len(native_html),
                  sameCellBreaks=native_html.lower().count(b'mso-data-placement'))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--real', nargs=3, action='append', default=[], metavar=('FILE','SHEET','RANGE'))
    parser.add_argument('--real-only', action='store_true', help='run only the supplied workbook selections')
    args = parser.parse_args()
    if args.real_only and not args.real:
        parser.error('--real-only requires at least one --real selection')
    args.output = args.output.resolve()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    scratch = TemporaryDirectory(prefix='clipboard-source-',dir=args.output.parent)
    sample = Path(scratch.name) / 'clipboard-geometry.xlsx'
    report = {'result':'FAIL', 'cases':[]}
    excel = DispatchEx('Excel.Application')
    sources = {}
    browser = None
    try:
        excel.Visible = False
        excel.DisplayAlerts = False
        excel.AutomationSecurity = 3
        excel.AskToUpdateLinks = False
        report['excel'] = [excel.Version, excel.Build]
        fixture(excel, sample)
        cases = [dict(file=str(sample),sheet='Grid',address=ref,mode=mode) for ref, mode in
                 [('E2','shortcut'), ('H2','shortcut'), ('B2','shortcut'), ('C3:E5','shortcut'),
                  ('F6','shortcut'), ('A1:I7','menu'), ('B2','fallback'), ('E2','special')]]
        cases.append(dict(file=str(sample),sheet='Visible',address='A2:C6',visible=True))
        cases.extend(dict(file=str(sample),sheet='Grid',address='B2',edit=True,**options) for options in [
            dict(value='已修改标题',expected='已修改标题'), dict(history='undo',revision=3),
            dict(history='redo',revision=4,expected='已修改标题')])
        if args.real_only:
            cases.clear()
        cases.extend(dict(file=str(Path(file).resolve()),sheet=sheet,address=ref) for file,sheet,ref in args.real)
        hashes = {c['file']:hashlib.sha256(Path(c['file']).read_bytes()).hexdigest() for c in cases}
        target = excel.Workbooks.Add().Worksheets(1)
        browser = subprocess.Popen(['node', str(Path(__file__).with_name('clipboard_qa.cjs')), '--excel'],
                                   stdin=subprocess.PIPE,stdout=subprocess.PIPE,encoding='utf-8')
        for case in cases:
            file = case['file']
            if file not in sources:
                sources[file] = excel.Workbooks.Open(file, UpdateLinks=0, ReadOnly=True, AddToMru=False)
            result = check(excel,browser,sources[file],target,case)
            report['cases'].append(result)
            print(result['file'],result['requested'],len(result['mismatches']),'mismatches',flush=True)
        report['sourceUnchanged'] = all(hashlib.sha256(Path(f).read_bytes()).hexdigest()==digest for f,digest in hashes.items())
        if not report['sourceUnchanged']:
            raise AssertionError('Source digest changed')
        if any(case['mismatches'] for case in report['cases']):
            raise AssertionError('Native paste mismatch; see per-cell results')
        report['result'] = 'PASS'
    except Exception as error:
        report['error'] = str(error)
        raise
    finally:
        try:
            if browser:
                browser.stdin.close()
                if browser.wait(timeout=45) and report['result']=='PASS':
                    report['result'] = 'FAIL'
                    report['error'] = 'Browser probe did not close cleanly'
                    raise RuntimeError('Browser probe did not close cleanly')
        finally:
            try:
                excel.Quit()
                scratch.cleanup()
            finally:
                args.output.write_text(json.dumps(report,ensure_ascii=False,indent=2,default=str)+'\n',encoding='utf-8')


if __name__ == '__main__':
    main()
