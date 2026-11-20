"""Compare browser formula edits, undo, redo and Excel resaves with a native entry."""
import argparse
import json
from hashlib import sha256
from pathlib import Path
from tempfile import TemporaryDirectory

from win32com.client import DispatchEx
from edit_table_excel import native_open
from edit_structure_excel import compare_books, comparison_stats


def verify(plan, directory):
    source = Path(plan['file']).resolve()
    original = sha256(source.read_bytes()).hexdigest()
    excel = DispatchEx('Excel.Application')
    excel.DisplayAlerts = excel.EnableEvents = False
    excel.AutomationSecurity = 3
    results = {}
    try:
        with TemporaryDirectory() as temp:
            tmp = Path(temp)
            book = native_open(excel, source)
            try:
                sheet = book.Worksheets(plan['sheet'])
                target = sheet.Range(plan['target'])
                other = book.Worksheets(plan['other']).Range(plan['ref'])
                qualified = "'" + plan['other'].replace("'", "''") + "'!"
                target.Formula = '=SUM(' + plan['start'] + ':' + plan['end'] + ',' + qualified + other.Address + ')'
                excel.CalculateFull()
                formula, value = target.Formula, target.Value2
                book.SaveCopyAs(str(tmp/'native-entry.xlsx'))
            finally:
                book.Close(False)
            for state in ('changed','undo','redo'):
                expected = native_open(excel, source if state=='undo' else tmp/'native-entry.xlsx')
                try:
                    excel.CalculateFull()
                    artifact = directory/(state+'.xlsx')
                    numeric = comparison_stats()
                    count = compare_books(excel,expected,artifact,plan['rectangles'],numeric=numeric)
                    opened = native_open(excel,artifact)
                    try:
                        actual = opened.Worksheets(plan['sheet']).Range(plan['target'])
                        cell = expected.Worksheets(plan['sheet']).Range(plan['target'])
                        assert (actual.Formula,actual.Value2)==(cell.Formula,cell.Value2)
                        assert actual.NumberFormat==cell.NumberFormat
                        reopened = tmp/('excel-'+state+'.xlsx')
                        opened.SaveCopyAs(str(reopened))
                    finally:
                        opened.Close(False)
                    second = compare_books(excel,expected,reopened,plan['rectangles'],numeric=numeric)
                    results[state] = {'cells':count,'resavedCells':second,'numeric':numeric}
                finally:
                    expected.Close(False)
            assert sha256(source.read_bytes()).hexdigest() == original
            return {'excel':str(excel.Version),'build':int(excel.Build),'sourceHashUnchanged':True,
                    'sourceSHA256':original,'formula':formula,'value':value,'states':results}
    finally:
        excel.Quit()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('plan',type=Path)
    parser.add_argument('directory',type=Path)
    args=parser.parse_args()
    output=verify(json.loads(args.plan.read_text('utf8')),args.directory.resolve())
    (args.directory/'excel.json').write_bytes((json.dumps(output,ensure_ascii=False,indent=2)+'\n').encode())
    print({name:item['cells']+item['resavedCells'] for name,item in output['states'].items()})
