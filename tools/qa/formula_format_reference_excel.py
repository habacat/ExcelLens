"""Record Excel's own number formats for single-cell Formula2 entries.

The source XLSX supplies the cells, formats and sheets. A UTF-8 JSON array of
formulas supplies the input order. Results are placed in an unused column of a
temporary copy. The JSON matrix can be passed to TestNativeFormulaEntryFormats.
"""
import argparse
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from zipfile import ZipFile
from xml.etree import ElementTree as ET

from openpyxl.utils.cell import column_index_from_string, coordinate_to_tuple
from win32com.client import DispatchEx
from pivot_reference import S, R, target
from edit_table_excel import native_open


def recorded_formulas(path, sheet, column):
    """Read native XF IDs after saving, before a second engine can reformat them."""
    with ZipFile(path) as package:
        book = ET.fromstring(package.read('xl/workbook.xml'))
        current = next(s for s in book.find(S+'sheets') if s.get('name') == sheet)
        links = ET.fromstring(package.read('xl/_rels/workbook.xml.rels'))
        part = target('xl', next(r.get('Target') for r in links if r.get('Id') == current.get(R+'id')))
        styles = package.read('xl/styles.xml').decode('utf-8-sig')
        xfs = ET.fromstring(styles).find(S+'cellXfs')
        cells, cases = {}, []
        for cell in ET.fromstring(package.read(part)).iter(S+'c'):
            ref, xf = cell.get('r'), int(cell.get('s','0'))
            row, col = coordinate_to_tuple(ref)
            formula = cell.findtext(S+'f')
            if col == column and formula:
                cases.append(dict(formula='='+formula, expected=int(xfs[xf].get('numFmtId','0')), row=row))
            elif xf:
                cells[ref] = str(xf)
        for other in book.find(S+'sheets'):
            if other.get('name') == sheet:
                continue
            name = other.get('name')
            rel = next(r for r in links if r.get('Id') == other.get(R+'id'))
            if not rel.get('Type').endswith('/worksheet'):
                continue
            for cell in ET.fromstring(package.read(target('xl',rel.get('Target')))).iter(S+'c'):
                if cell.get('s','0') != '0':
                    cells[name+'!'+cell.get('r')] = cell.get('s')
        return dict(sheet=sheet, styles=styles, cells=cells, cases=cases)


def main():
    arg = argparse.ArgumentParser(description=__doc__)
    arg.add_argument('source', type=Path)
    arg.add_argument('formulas', type=Path)
    arg.add_argument('output', type=Path)
    arg.add_argument('--sheet', required=True)
    arg.add_argument('--column', required=True)
    opts = arg.parse_args()
    formulas = json.loads(opts.formulas.read_text('utf8'))
    col = column_index_from_string(opts.column)
    excel = DispatchEx('Excel.Application')
    excel.DisplayAlerts = excel.EnableEvents = False
    excel.AutomationSecurity = 3
    try:
        with TemporaryDirectory() as folder:
            sample = Path(folder)/'native.xlsx'
            book = native_open(excel,opts.source.resolve())
            try:
                sheet = book.Worksheets(opts.sheet)
                if sheet.UsedRange.Column + sheet.UsedRange.Columns.Count > col:
                    raise ValueError('Choose an empty column beyond the source data')
                for i,formula in enumerate(formulas,1):
                    sheet.Cells(i,col).Formula2 = formula if formula.startswith('=') else '='+formula
                book.SaveCopyAs(str(sample))
            finally:
                book.Close(False)
            result = recorded_formulas(sample,opts.sheet,col)
            if len(result['cases']) != len(formulas):
                raise ValueError('Excel did not store every input as a formula')
            opts.output.write_text(json.dumps([result],ensure_ascii=False),'utf8')
    finally:
        excel.Quit()


if __name__ == '__main__':
    main()
