"""Check Excel table revisions against native workbook snapshots (private Windows QA).

The JSON plan lists states with paths relative to the plan: expected, actual, sheet,
focus (A1 rectangle), an optional validation range, and case and state labels.
Excel opens both inputs read-only. A temporary native resave checks persisted caches.
"""
import argparse
import json
import math
from pathlib import Path
from tempfile import TemporaryDirectory
from xml.etree import ElementTree as ET
from zipfile import ZipFile

from openpyxl.utils.cell import get_column_letter, range_boundaries
from win32com.client import DispatchEx

from edit_recalculation_excel import SML, compare, snapshot
from edit_table_excel import native_open


def filter_node(node):
    if node is None:
        return None
    return (node.tag, sorted((k, v) for k, v in node.attrib.items() if not k.startswith('{')),
            [filter_node(child) for child in node if child.tag != SML + 'extLst'])


def table_definitions(path):
    tables = {}
    with ZipFile(path) as package:
        for part in package.namelist():
            if not (part.startswith('xl/tables/') and part.endswith('.xml')):
                continue
            table = ET.fromstring(package.read(part))
            columns = [(c.get('id'), c.get('name'), c.get('totalsRowFunction', 'none'),
                        c.get('totalsRowLabel', ''), c.findtext(SML + 'calculatedColumnFormula', ''),
                        c.findtext(SML + 'totalsRowFormula', '')) for c in table.find(SML + 'tableColumns')]
            name = table.attrib['displayName']
            assert name not in tables, name
            tables[name] = (table.attrib['ref'], table.get('headerRowCount', '1'), table.get('totalsRowCount', '0'),
                            columns, filter_node(table.find(SML + 'autoFilter')), filter_node(table.find(SML + 'sortState')))
    return tables


def compare_caches(reference, other):
    assert reference.keys() == other.keys(), 'formula addresses differ'
    rounded = 0
    for address, value in reference.items():
        verdict = compare(value, other[address], 1e-12)
        assert verdict != 'different', (address, value, other[address])
        rounded += verdict != 'exact'
    return rounded


def last_address(sheet):
    used = sheet.UsedRange
    return used.Row + used.Rows.Count - 1, used.Column + used.Columns.Count - 1


def compare_grid(expected, actual, formula_addresses):
    names = [s.Name for s in expected.Worksheets]
    assert names == [s.Name for s in actual.Worksheets], 'worksheet order'
    cells = formulas = 0
    for name in names:
        a, b = expected.Worksheets(name), actual.Worksheets(name)
        rows, cols = (max(2, x, y) for x, y in zip(last_address(a), last_address(b)))
        area = 'A1:' + get_column_letter(cols) + str(rows)
        av, bv = a.Range(area).Value2, b.Range(area).Value2
        af, bf = a.Range(area).Formula2, b.Range(area).Formula2
        for row, (left, right) in enumerate(zip(av, bv), 1):
            for col, (v, w) in enumerate(zip(left, right), 1):
                address = (name, get_column_letter(col) + str(row))
                assert type(v) is type(w), (address, type(v), type(w))
                if v != w:
                    assert isinstance(v, float) and math.isclose(v, w, rel_tol=2e-15, abs_tol=1e-12), (address, v, w)
                if address in formula_addresses:
                    assert af[row-1][col-1] == bf[row-1][col-1], (address, 'Formula2')
                    formulas += 1
        cells += rows * cols
    assert formulas == len(formula_addresses)
    return cells, formulas


def compare_focus(a, b, rect):
    left, top, right, bottom = range_boundaries(rect)
    for row in range(top, bottom + 1):
        assert a.Rows(row).Hidden == b.Rows(row).Hidden, ('hidden', row)
    for col in range(left, right + 1):
        letter = get_column_letter(col)
        block = f'{letter}{top}:{letter}{bottom}'
        x, y = a.Range(block).NumberFormat, b.Range(block).NumberFormat
        if x is not None and x == y:
            continue
        for row in range(top, bottom + 1):
            ref = letter + str(row)
            assert a.Range(ref).NumberFormat == b.Range(ref).NumberFormat, ('number format', ref)
    return (right-left+1)*(bottom-top+1), bottom-top+1


def validation(cell):
    from pywintypes import com_error
    rule = cell.Validation
    try:
        kind = rule.Type
    except com_error:
        return None
    return kind, rule.Operator, rule.Formula1, rule.Formula2


def verify(excel, directory, item):
    expected, actual = (directory / item[k] for k in ('expected', 'actual'))
    tables = table_definitions(expected)
    assert table_definitions(actual) == tables, 'table definition, filter or sort state'
    caches = snapshot(expected)
    rounded = compare_caches(caches, snapshot(actual))
    a = native_open(excel, expected)
    try:
        b = native_open(excel, actual)
        try:
            excel.CalculateFullRebuild()
            cells, formulas = compare_grid(a, b, caches)
            source, changed = a.Worksheets(item['sheet']), b.Worksheets(item['sheet'])
            formats, rows = compare_focus(source, changed, item['focus'])
            if 'appearance' in item:
                for cell in source.Range(item['appearance']).Cells:
                    other = changed.Range(cell.Address)
                    for prop in ('HorizontalAlignment', 'VerticalAlignment', 'WrapText', 'Locked'):
                        assert getattr(cell, prop) == getattr(other, prop), (cell.Address, prop)
                    assert cell.Font.Bold == other.Font.Bold and cell.Interior.Color == other.Interior.Color, cell.Address
            rules = 0
            if 'validation' in item:
                for cell in source.Range(item['validation']).Cells:
                    assert validation(cell) == validation(changed.Range(cell.Address)), ('validation', cell.Address)
                    rules += 1
            with TemporaryDirectory() as tmp:
                saved = Path(tmp) / 'resaved.xlsx'
                b.SaveCopyAs(str(saved))
                assert table_definitions(saved) == tables, 'resaved table definitions'
                rounded += compare_caches(caches, snapshot(saved))
        finally:
            b.Close(False)
    finally:
        a.Close(False)
    return dict(cells=cells, formulas=formulas, cacheComparisons=2*len(caches), roundedCaches=rounded,
                numberFormats=formats, rowVisibility=rows, validationCells=rules, tables=len(tables))


def main(plan, output):
    items = json.loads(plan.read_text(encoding='utf-8'))
    excel = DispatchEx('Excel.Application')
    excel.Visible, excel.DisplayAlerts, excel.EnableEvents, excel.AutomationSecurity = False, False, False, 3
    report = dict(excel=excel.Version, build=excel.Build, states=[], errors=[])
    try:
        for item in items:
            label = dict(case=item['case'], state=item['state'])
            try:
                details = verify(excel, plan.resolve().parent, item)
                report['states'].append(dict(**label, **details))
                print(*label.values(), 'PASS', flush=True)
            except Exception as error:
                report['errors'].append(dict(**label, detail=str(error)))
                print(*label.values(), 'FAIL', error, flush=True)
            output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    finally:
        excel.Quit()
    if report['errors']:
        raise SystemExit(1)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('plan', type=Path)
    parser.add_argument('output', type=Path)
    opts = parser.parse_args()
    main(opts.plan, opts.output)
