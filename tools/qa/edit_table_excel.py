"""Compare grid revisions and saved copies with desktop Excel, using a private JSON case.

The case lists worksheet rectangles and single-cell inputs. Only temporary copies
are changed. Direct assignments use native before/after snapshots. ClearContents
and FillDown also exercise Excel's own undo and redo commands.
"""
import argparse
from collections import Counter
from contextlib import suppress
from datetime import datetime
from hashlib import sha256
import json
from pathlib import Path
import posixpath
import re
from tempfile import TemporaryDirectory
from uuid import uuid4
from zipfile import ZipFile
import xml.etree.ElementTree as ET

import pythoncom
from win32com.client import DispatchEx
from openpyxl.formula.translate import Translator
from openpyxl.formula.tokenizer import Tokenizer
from openpyxl.utils.cell import get_column_letter
from edit_recalculation_excel import service, coordinates, compare, SML, REL


def native_open(excel, path):
    m = pythoncom.Missing
    return excel.Workbooks.Open(str(path), 0, True, m, m, m, True, m, m, m, False, m, False, m, 0)


def native_formulas(path):
    formulas, tables, errors = {}, {}, {}
    with ZipFile(path) as z:
        links = {el.get('Id'): posixpath.normpath(posixpath.join('xl', el.get('Target'))).lstrip('/')
                 for el in ET.fromstring(z.read('xl/_rels/workbook.xml.rels'))}
        sheets = ET.fromstring(z.read('xl/workbook.xml')).find(SML+'sheets')
        for sheet in sheets:
            name = sheet.get('name')
            shared = {}
            for c in ET.fromstring(z.read(links[sheet.get(REL+'id')])).iter(SML+'c'):
                ref = c.get('r')
                if c.get('t') == 'e':
                    errors[name, ref] = c.findtext(SML+'v')
                f = c.find(SML+'f')
                if f is None:
                    continue
                raw = '='+f.text if f.text else ''
                if f.get('t') == 'shared':
                    group = f.get('si')
                    if raw:
                        shared[group] = Translator(raw, origin=ref)
                    else:
                        raw = shared[group].translate_formula(ref)
                formulas[name, ref] = raw
        for part in z.namelist():
            if part.startswith('xl/tables/') and part.endswith('.xml'):
                t = ET.fromstring(z.read(part))
                tables[t.get('displayName')] = [t.get('ref'), [[c.get('name'), c.get('id'),
                                                                c.get('totalsRowFunction') or 'none', c.get(
                                                                    'totalsRowLabel') or '',
                                                                c.findtext(SML+'calculatedColumnFormula') or '', c.findtext(SML+'totalsRowFormula') or '']
                                                               for c in t.find(SML+'tableColumns')]]
    return formulas, tables, errors


def typed(value):
    if value is None:
        return ('blank', '')
    if isinstance(value, bool):
        return ('boolean', str(value).upper())
    if isinstance(value, (float, int)):
        return ('n', str(value))
    return ('text', str(value))


def snapshot(excel, book, ranges, path):
    excel.CalculateFullRebuild()
    book.SaveCopyAs(str(path))
    formulas, tables, errors = native_formulas(path)
    cells = {}
    for sheet, first, last in ranges:
        block = book.Worksheets(sheet).Range(first, last)
        matrix = block.Value2
        row, col = coordinates(first)
        if first == last:
            matrix = ((matrix,),)
        for y, line in enumerate(matrix):
            for x, value in enumerate(line):
                ref = get_column_letter(col+x)+str(row+y)
                key = sheet, ref
                scalar = ('error', errors[key]) if key in errors else typed(value)
                cells[key] = (scalar, formulas.get(key, ''))
    return cells, tables


def grid_snapshot(call, key, revision, ranges, date1904=False):
    cells = {}
    for sheet, first, last in ranges:
        clip = call('/api/edits/copy', dict(document=key, revision=revision, sheet=sheet, ref=first, end=last))
        row, col = coordinates(first)
        for i, c in enumerate(clip['cells']):
            ref = get_column_letter(col+i % clip['cols'])+str(row+i//clip['cols'])
            v = c.get('value') or {}
            kind, raw = v.get('kind', 'pending'), v.get('raw', '')
            if kind in ('missing', 'blank'):
                kind, raw = 'blank', ''
            elif kind == 'number':
                kind = 'n'
            elif kind == 'date':
                dt = datetime.fromisoformat(raw.replace('Z', '+00:00')).replace(tzinfo=None)
                origin = datetime(1904, 1, 1) if date1904 else datetime(1899, 12, 30)
                serial = (dt-origin).total_seconds()/86400
                if not date1904 and dt < datetime(1900, 3, 1):
                    serial -= 1
                kind, raw = 'n', str(serial)
            cells[sheet, ref] = ((kind, raw), c.get('formula') or '')
    return cells


def formula_key(raw):
    # Only Excel's redundant quotes on a simple worksheet name may differ
    # after native SaveCopyAs. Text literals and formulas otherwise stay exact.
    if "'!" not in raw:
        return raw
    tokens = Tokenizer(raw).items
    return '='+''.join(re.sub(r"^'([^\W\d]\w*)'!", r"\1!", t.value)
                       if t.type == 'OPERAND' and t.subtype == 'RANGE' else t.value for t in tokens)


def verify(actual, expected):
    counts = Counter()
    errors = []
    for k in sorted(set(actual) | set(expected)):
        got, want = actual.get(k), expected.get(k)
        verdict = compare(got[0], want[0]) if got and want else 'different'
        if got and want and got[1] != want[1] and formula_key(got[1]) != formula_key(want[1]):
            verdict = 'different'
        counts[verdict] += 1
        if verdict == 'different' and len(errors) < 5:
            errors.append([k, got, want])
    if errors:
        raise AssertionError((counts, errors))
    return dict(counts)


def native_value(kind, raw):
    if kind == 'number':
        return float(raw)
    if kind == 'boolean':
        return {'TRUE': True, 'FALSE': False}[raw]
    if kind == 'blank':
        return None
    return raw


def apply_native(excel, sheet, ref, kind, raw, command):
    cell = sheet.Range(ref)
    if command:
        sheet.Activate()
        if command == 'FillDown':
            row, col = coordinates(ref)
            sheet.Range(get_column_letter(col) + str(row-1), ref).Select()
        elif command == 'ClearContents':
            assert kind == 'blank'
            cell.Select()
        else:
            raise ValueError(command)
        assert excel.CommandBars.GetEnabledMso(command), command
        excel.CommandBars.ExecuteMso(command)
    elif kind == 'column':
        # Range.Formula assignment autofills from the first member even for
        # heterogeneous input. Compare the grid's paste with a native range
        # copy at identical coordinates, keeping relative references intact.
        scratch = excel.Workbooks.Add()
        try:
            source = scratch.Worksheets(1).Range(ref)
            source.Formula = tuple((native_value(k, v),) for k, v in raw)
            source.Copy(cell)
        finally:
            scratch.Close(False)
    elif kind == 'formula':
        cell.Formula = raw
    else:
        cell.Value2 = native_value(kind, raw)


def run(excel, options, case, directory, report):
    path = Path(case['source']).resolve()
    source = path.read_bytes()
    digest = sha256(source).hexdigest()
    local = directory/'source.xlsx'
    local.write_bytes(source)
    ranges = case['ranges']
    book = native_open(excel, local)
    try:
        date1904 = bool(book.Date1904)
        with service(options.probe.resolve(), directory, source) as call:
            opened = call('/api/open')
            doc = call('/api/edits/start', {'book': opened['book']})['document']
            key = {k: doc[k] for k in ('id', 'binding')}
            rev = doc['revision']

            def commit(action, changes=None):
                op = dict(action=action, revision=rev, request=uuid4().hex)
                if changes is not None:
                    op['changes'] = changes
                return call('/api/edits/commit', dict(document=key, operation=op))['document']['revision']
            before, before_tables = snapshot(excel, book, ranges, directory/'before.xlsx')
            report['base'] = verify(grid_snapshot(call, key, rev, ranges, date1904), before)
            if case.get('protection'):
                sheet, first, last = case['protection']
                sheet_obj = book.Worksheets(sheet)
                assert sheet_obj.ProtectContents
                tally = Counter()
                top, left = coordinates(first)
                bottom, right = coordinates(last)
                for row in range(top, bottom+1):
                    for col in range(left, right+1):
                        ref = get_column_letter(col)+str(row)
                        response = call('/api/edits/read', dict(document=key,
                                                                revision=rev, sheet=sheet, ref=ref))
                        locked = bool(sheet_obj.Range(ref).Locked)
                        assert bool(response.get('readOnly')) == locked, (sheet,
                                                                          ref, locked, response.get('readOnly'))
                        tally['locked' if locked else 'unlocked'] += 1
                report['protection'] = dict(tally)
                trial = []
                for ref in case['mixedBatch']:
                    row, col = coordinates(ref)
                    trial.append(dict(sheet=sheet, row=row, col=col, value=dict(kind='number', raw='71')))
                try:
                    commit('change', trial)
                except RuntimeError as exc:
                    assert '只读' in str(exc), exc
                else:
                    raise AssertionError('mixed lock batch accepted')
                assert call('/api/edits/state', {'document': key})['document']['revision'] == rev
                report['mixedBatchRejected'] = True
            if case.get('blockedExpansion'):
                sheet, ref, raw = case['blockedExpansion']
                assert not book.Worksheets(sheet).Range(ref).Locked
                try:
                    book.Worksheets(sheet).Range(ref).Formula = raw
                except pythoncom.com_error as exc:
                    assert exc.excepinfo and exc.excepinfo[1] == 'Microsoft Excel', exc
                else:
                    raise AssertionError('Excel changed a column with locked members')
                values, definitions = snapshot(excel, book, ranges, directory/'rejected.xlsx')
                # Excel may modify the column template before reporting the
                # protection error. Restart from the pristine oracle for the
                # next successful edit. ExcelLens rejects the entire operation.
                report['excelAfterRejectedExpansion'] = {
                    'changedCells': sum(values[k] != v for k, v in before.items()),
                    'changedTables': sum(definitions[k] != v for k, v in before_tables.items()),
                }
                book.Close(False)
                book = native_open(excel, local)
                row, col = coordinates(ref)
                try:
                    commit('change', [dict(sheet=sheet, row=row, col=col,
                                           value=dict(kind='formula', raw=raw))])
                except RuntimeError as exc:
                    assert '只读' in str(exc), exc
                else:
                    raise AssertionError('grid expanded into locked members')
                assert call('/api/edits/state', {'document': key})['document']['revision'] == rev
                report['blockedExpansion'] = verify(grid_snapshot(call, key, rev, ranges, date1904), before)
            for i, entry in enumerate(case['inputs']):
                sheet, ref, kind, raw = entry[:4]
                command = entry[4] if len(entry) == 5 else ('ClearContents' if kind == 'blank' else '')
                step = {'input': ref, 'sheet': sheet, 'kind': kind}
                report['steps'].append(step)
                apply_native(excel, book.Worksheets(sheet), ref, kind, raw, command)
                after, tables = snapshot(excel, book, ranges, directory/('native-%d.xlsx' % i))
                assert after != before or tables != before_tables, (sheet, ref, 'no native change')
                first, _, last = ref.partition(':')
                row, col = coordinates(first)
                inputs = raw if kind == 'column' else [(kind, raw)]
                changes = [dict(sheet=sheet, row=row+j, col=col, value=dict(kind=k, raw=v))
                           for j, (k, v) in enumerate(inputs)]
                if kind == 'column':
                    assert coordinates(last) == (row+len(changes)-1, col)
                    preview = call('/api/edits/preview', dict(document=key, revision=rev,
                                                              sheet=sheet, ref=first, end=last, mode='changes',
                                                              operation=dict(changes=changes)))
                    changes = preview['changes']
                for action, label, want in [('change', 'edit', after), ('undo', 'undo', before), ('redo', 'redo', after)]:
                    if command and action in ('undo', 'redo'):
                        native_action = action.title()
                        assert excel.CommandBars.GetEnabledMso(native_action), native_action
                        excel.CommandBars.ExecuteMso(native_action)
                        observed, definitions = snapshot(
                            excel, book, ranges, directory/(action+'-%d.xlsx' % i))
                        assert definitions == (before_tables if action == 'undo' else tables)
                        verify(observed, want)
                        want = observed
                        step['nativeCommand'] = command
                    rev = commit(action, changes if action == 'change' else None)
                    step[label] = verify(grid_snapshot(call, key, rev, ranges, date1904), want)
                artifact = call('/api/edits/save', dict(document=key, revision=rev))
                data = call(artifact['downloadUrl'])
                output = directory/('generated-%d.xlsx' % i)
                output.write_bytes(data)
                # Compare the produced definitions before either side is re-saved by Excel.
                _, actual_tables, _ = native_formulas(output)
                assert actual_tables == tables, [(k, v, tables.get(k))
                                                 for k, v in actual_tables.items() if v != tables.get(k)][:1]
                if case.get('protection'):
                    with ZipFile(local) as src, ZipFile(output) as out:
                        keypart = case['protectionPart']
                        assert src.read('xl/styles.xml') == out.read('xl/styles.xml')
                        pattern = br'<(?:\w+:)?sheetProtection\b[^>]+/>'
                        assert re.search(pattern, src.read(keypart)).group(
                        ) == re.search(pattern, out.read(keypart)).group()
                reopened = native_open(excel, output)
                try:
                    values, reopened_tables = snapshot(
                        excel, reopened, ranges, directory/('reopened-%d.xlsx' % i))
                    assert reopened_tables == tables
                    step['excelReopen'] = verify(values, after)
                finally:
                    reopened.Close(False)
                with TemporaryDirectory(prefix='excellens-ui-qa-') as fresh, service(options.probe.resolve(), Path(fresh), data) as again:
                    newbook = again('/api/open')
                    newdoc = again('/api/edits/start', {'book': newbook['book']})['document']
                    newkey = {k: newdoc[k] for k in ('id', 'binding')}
                    step['gridReopen'] = verify(grid_snapshot(
                        again, newkey, newdoc['revision'], ranges, date1904), after)
                print(ref, i, step['edit'], flush=True)
                before, before_tables = after, tables
            report.update(tables=len(tables), sourceSHA256=digest,
                          sourceUnchanged=sha256(path.read_bytes()).hexdigest() == digest)
            assert report['sourceUnchanged']
    finally:
        with suppress(pythoncom.com_error, AttributeError):
            book.Close(False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ('case', 'probe', 'output'):
        parser.add_argument('--'+flag, required=True, type=Path)
    args = parser.parse_args()
    spec = json.loads(args.case.read_text(encoding='utf8'))
    excel = DispatchEx('Excel.Application')
    excel.Visible = False
    excel.AutomationSecurity = 3
    excel.DisplayAlerts = False
    excel.EnableEvents = False
    prior = excel.AutoCorrect.AutoFillFormulasInLists
    report = {'excel': str(excel.Version), 'build': str(excel.Build), 'steps': [], 'passed': False}
    try:
        excel.AutoCorrect.AutoFillFormulasInLists = True
        with TemporaryDirectory(prefix='excellens-ui-qa-') as stage:
            run(excel, args, spec, Path(stage), report)
        report['passed'] = True
    finally:
        with suppress(pythoncom.com_error, AttributeError):
            excel.AutoCorrect.AutoFillFormulasInLists = prior
        with suppress(pythoncom.com_error, AttributeError):
            excel.Quit()
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n', encoding='utf8')
