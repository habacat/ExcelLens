"""Create independent before, filter and refresh snapshots with desktop Excel.

The JSON plan names a private source, worksheet, pivot table, date field,
condition and optional start/end. Existing filters and worksheet data are
preserved in the before snapshot. The edited result is saved to a new file.
"""
import argparse
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from zipfile import ZipFile, ZIP_DEFLATED
from xml.etree import ElementTree as ET

import pythoncom
from win32com.client import DispatchEx
from edit_table_excel import native_open

FIXED = ['dateEqual','dateNotEqual','dateOlderThan','dateOlderThanOrEqual',
         'dateNewerThan','dateNewerThanOrEqual','dateBetween','dateNotBetween']
RELATIVE = ['tomorrow','today','yesterday','nextWeek','thisWeek','lastWeek','nextMonth','thisMonth','lastMonth',
            'nextQuarter','thisQuarter','lastQuarter','nextYear','thisYear','lastYear','yearToDate']
TYPES = {k:i for i,k in enumerate(FIXED+RELATIVE+['Q'+str(n) for n in range(1,5)]+['M'+str(n) for n in range(1,13)],29)}
WHOLE_DAY = (b'<extLst><ext uri="{0605FD5F-26C8-4aeb-8148-2DB25E43C511}">'
             b'<x15:pivotFilter xmlns:x15="http://schemas.microsoft.com/office/spreadsheetml/2010/11/main" useWholeDay="1"/>'
             b'</ext></extLst>')


def set_whole_day(source, dest, pivot, kind, field_index):
    """Enable the documented Office 2013 flag on the selected native filter.

    Some Excel builds disregard the optional WholeDayFilter argument of Add2.
    Excel performs the actual row selection, grouping and totals on refresh.
    """
    import re
    matches = 0
    with ZipFile(source) as src, ZipFile(dest,'w',ZIP_DEFLATED) as out:
        for name in src.namelist():
            raw = src.read(name)
            if name.startswith('xl/pivotTables/pivotTable') and name.endswith('.xml') and ET.fromstring(raw).get('name')==pivot:
                def patch(match):
                    nonlocal matches
                    data=match.group()
                    if not re.search(rb'\bfld="'+str(field_index).encode()+b'"',data.split(b'>',1)[0]) or b'type="'+kind.encode()+b'"' not in data.split(b'>',1)[0]:
                        return data
                    matches+=1
                    return data.replace(b'</autoFilter></filter>', b'</autoFilter>'+WHOLE_DAY+b'</filter>')
                raw=re.sub(rb'<filter\s[^>]*>.*?</filter>',patch,raw, flags=re.DOTALL)
            out.writestr(name,raw)
    assert matches==1


def create_reference(excel, case, output):
    output.mkdir(parents=True,exist_ok=True)
    book=native_open(excel,Path(case['source']).resolve())
    def table(b):return b.Worksheets(case['sheet']).PivotTables(case['pivot'])
    try:
        p=table(book)
        source=output/'native-base.xlsx'
        book.SaveCopyAs(str(source))
        f=p.PivotFields(case['field'])
        kind=case['kind']
        for i in range(f.PivotFilters.Count,0,-1):
            item=f.PivotFilters(i)
            if item.FilterType in TYPES.values():item.Delete()
        if kind in FIXED:
            first=case['start'].replace('-','/').replace('T',' ')
            last=case['end'].replace('-','/').replace('T',' ') if 'end' in case else pythoncom.Missing
            f.PivotFilters.Add2(TYPES[kind],None,first,last)
        elif kind != 'clear':
            f.PivotFilters.Add2(TYPES[kind])
        changed=output/'native-changed.xlsx'
        book.SaveCopyAs(str(changed))
        def native_whole_day(field):
            return next(item.WholeDayFilter for item in field.PivotFilters if item.FilterType==TYPES[kind])
        if case.get('wholeDay') and not native_whole_day(f):
            with ZipFile(changed) as z:
                definition=next(ET.fromstring(z.read(n)) for n in z.namelist() if n.startswith('xl/pivotTables/pivotTable') and n.endswith('.xml') and ET.fromstring(z.read(n)).get('name')==p.Name)
                matches=[item for item in definition.findall('{*}filters/{*}filter') if item.get('type')==kind]
                # Add2 appends the new criterion; the stable native ID disambiguates fields.
                target=max(matches,key=lambda node:int(node.get('id')))
            book.Close(False);book=None
            with TemporaryDirectory() as temp:
                extended=Path(temp)/'whole-day.xlsx'
                set_whole_day(changed,extended,case['pivot'],kind,int(target.get('fld')))
                book=native_open(excel,extended);p=table(book);p.RefreshTable()
                assert native_whole_day(p.PivotFields(case['field']))
                book.SaveCopyAs(str(changed))
                p.RefreshTable();book.SaveCopyAs(str(output/'native-refreshed.xlsx'))
                book.Close(False);book=None
        else:
            p.RefreshTable();book.SaveCopyAs(str(output/'native-refreshed.xlsx'))
    finally:
        if book is not None:book.Close(False)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('plan',type=Path)
    parser.add_argument('output',type=Path)
    opts=parser.parse_args()
    excel=DispatchEx('Excel.Application')
    excel.DisplayAlerts=excel.EnableEvents=False
    excel.AutomationSecurity=3
    try:
        for case in json.loads(opts.plan.read_text(encoding='utf8')):
            create_reference(excel,case,opts.output.resolve()/case['name'])
    finally:excel.Quit()


if __name__=='__main__':main()
