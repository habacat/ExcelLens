"""Compare structural copy-edit transactions and their saved files with desktop Excel."""
import argparse
import hashlib
import json
import struct
from pathlib import Path
import shutil
from datetime import datetime
from tempfile import TemporaryDirectory
from uuid import uuid4
from xml.etree import ElementTree as ET
from zipfile import ZipFile

import pythoncom
from win32com.client import DispatchEx
from edit_recalculation_excel import service
from pivot_reference import compare_saved_pivot


def native_axes(pivot, axes):
    groups = (('rows', 1, pivot.RowFields), ('columns', 2, pivot.ColumnFields), ('pages', 3, pivot.PageFields))
    targets = {k:[p['field'] for p in v] if k == 'pages' else v for k,v in axes.items()}
    old = [(field.SourceName, k) for k,_,fields in groups for field in fields]
    for name, k in old:
        if name not in targets.get(k, []):
            pivot.PivotFields(name).Orientation = 0
    for key, orientation, _ in groups:
        names=targets.get(key, [])
        # Excel numbers the bottom/last report filter first. The editor and
        # SpreadsheetML pageFields both use the visible top-to-bottom order.
        order=list(reversed(names)) if key=='pages' else names
        for name in order:
            field = pivot.PivotFields(name)
            field.Orientation = orientation
        for index,name in enumerate(order,1):
            pivot.PivotFields(name).Position = index
    for page in axes.get('pages', []):
        field = pivot.PivotFields(page['field'])
        field.ClearAllFilters()
        def item_caption(choice):
            if choice['type']=='b':return 'TRUE' if choice['value']=='1' else 'FALSE'
            if choice['type']=='m':return '(空白)'
            if choice['type']=='d':
                wanted = datetime.fromisoformat(choice['value'])
                for member in field.PivotItems():
                    for pattern in ('%m/%d/%Y %H:%M:%S', '%m/%d/%Y'):
                        try:
                            date = datetime.strptime(str(member.SourceNameStandard), pattern)
                        except ValueError:
                            continue
                        if date == wanted:
                            return str(member.Name)
                raise AssertionError(('missing date item', choice))
            return choice['value']
        items = [item_caption(choice) for choice in page.get('items', [])]
        field.EnableMultiplePageItems = len(items)>1
        if len(items)==1:
            field.CurrentPage = items[0]
        elif items:
            for member in field.PivotItems():
                member.Visible = str(member.Name) in items


def native_operation(book, change):
    sheet = book.Worksheets(change['sheet'])
    kind = change['kind']
    if kind == 'refreshCharts':
        for chart in sheet.ChartObjects(): chart.Chart.Refresh()
        return
    if kind in ('refreshPivot','configurePivot'):
        pivot = sheet.PivotTables(change['pivot'])
        if kind == 'configurePivot':
            pivot.PivotCache().Refresh()
            if 'axes' in change:
                native_axes(pivot, change['axes'])
            else:
                field = pivot.PivotFields(change['rows'])
                if field.Name != pivot.RowFields(1).Name:
                    former = pivot.RowFields(1).Name
                    field.Orientation = 1
                    pivot.PivotFields(former).Orientation = 0
            if pivot.DataFields(1).SourceName != change['data']:
                pivot.DataFields(1).Orientation = 0
                pivot.AddDataField(pivot.PivotFields(change['data']))
            method = {'sum':-4157,'count':-4112,'average':-4106,'min':-4139,'max':-4136}[change['aggregate']]
            if pivot.DataFields(1).Function != method:
                pivot.DataFields(1).Function = method
            if change.get('filterMode') == 'all':
                pivot.RowFields(1).ClearManualFilter()
            if change.get('item'):
                for item in pivot.RowFields(1).PivotItems(): item.Visible = (str(item.Name)==change['item'])
        assert pivot.RefreshTable()
        return
    if kind == 'renameSheet':
        sheet.Name = change['name']
        return
    rows = kind.endswith('Rows')
    axis = sheet.Rows if rows else sheet.Columns
    at, count = change['at'], change['count']
    block = sheet.Range(axis(at), axis(at + count - 1))
    if kind.startswith('insert'):
        block.Insert()
    elif kind.startswith('delete'):
        block.Delete()
    else:
        block.Cut()
        axis(change['before']).Insert()
        book.Application.CutCopyMode = False


def defined_names(book):
    return {n.Name.replace('['+book.Name+']', ''): n.RefersTo.replace('['+book.Name+']', '') for n in book.Names}


def check_chart_cache(path, expected):
    """Read the actual serialized caches before Excel can rebuild them on open."""
    ns = {'c':'http://schemas.openxmlformats.org/drawingml/2006/chart'}
    with ZipFile(path) as package:
        for part, series in expected.items():
            nodes = ET.fromstring(package.read(part)).findall('.//c:ser', ns)
            assert len(nodes) == len(series), (part, len(nodes))
            for node, values in zip(nodes, series):
                for role, points in values.items():
                    cache = node.find('c:'+role+'/*/*[c:ptCount]', ns)
                    assert cache is not None and int(cache.find('c:ptCount', ns).get('val')) == len(points)
                    actual = {int(p.get('idx')):p.findtext('c:v', '', ns) for p in cache.findall('c:pt', ns)}
                    assert actual == {i:str(v) for i,v in enumerate(points) if v is not None}, (role, actual, points)


def comparison_stats():
    return dict(exact=0, withinTolerance=0, maxAbsDifference=0, maxUlps=0, constantFormulaBarDifferences=0)


def ordered_double(x):
    bits = struct.unpack('>q', struct.pack('>d',x))[0]
    return -(1 << 63) - bits if bits < 0 else bits


def compare_books(excel, expected, artifact, rectangles, check_names=False, format_cells=None, numeric=None):
    actual = excel.Workbooks.Open(str(artifact), UpdateLinks=0, ReadOnly=True, CorruptLoad=0)
    try:
        excel.CalculateFull()
        assert actual.ReadOnly and Path(actual.FullName) == artifact
        assert [s.Name for s in actual.Worksheets] == [s.Name for s in expected.Worksheets]
        if check_names:
            assert defined_names(actual) == defined_names(expected), (defined_names(actual),defined_names(expected))
        for sheet, addresses in (format_cells or {}).items():
            for address in addresses:
                a,b = (book.Worksheets(sheet).Range(address) for book in (actual,expected))
                assert a.NumberFormat == b.NumberFormat, (sheet,address,a.NumberFormat,b.NumberFormat)
        count = 0
        for index, address in (rectangles.items() if isinstance(rectangles,dict) else enumerate(rectangles,1)):
            a = actual.Worksheets(index).Range(address)
            b = expected.Worksheets(index).Range(address)
            left, right = a.Value2, b.Value2
            left_formulas, right_formulas = a.Formula, b.Formula
            rows, columns = a.Rows.Count, a.Columns.Count
            assert len(left) == len(right) == rows
            for row,(l,r) in enumerate(zip(left,right)):
                assert len(l) == len(r) == columns
                for col,(v,w) in enumerate(zip(l,r)):
                    coords = (index,address,row+1,col+1)
                    assert type(v) is type(w), (coords,'Value2 type',type(v),type(w))
                    if type(w) is float:
                        delta = abs(v-w)
                        assert delta <= max(1e-12,abs(w)*2e-15), (coords,'Value2',v,w)
                        if numeric is not None:
                            key = 'exact' if v == w else 'withinTolerance'
                            numeric[key] += 1
                            numeric['maxAbsDifference'] = max(numeric['maxAbsDifference'],delta)
                            # Coordinate ordering spans both signs and treats +0 and -0 as equal.
                            if delta:
                                numeric['maxUlps'] = max(numeric['maxUlps'],abs(ordered_double(v)-ordered_double(w)))
                    else:
                        assert v == w, (coords,'Value2',v,w)
                    f,g = left_formulas[row][col],right_formulas[row][col]
                    if type(f) is not type(g) or f != g:
                        # Range.Formula formats even a *constant* as a 15-digit
                        # string in some Excel builds. Verify its stored double
                        # above; compare every true formula expression literally.
                        assert type(w) is float and not a.Cells(row+1,col+1).HasFormula and not b.Cells(row+1,col+1).HasFormula, (coords,'Formula',f,g)
                        if numeric is not None:
                            numeric['constantFormulaBarDifferences'] += 1
            count += rows * columns
            if b.Worksheet.AutoFilterMode:
                assert a.Worksheet.AutoFilter.Range.Address == b.Worksheet.AutoFilter.Range.Address
            assert a.Worksheet.ChartObjects().Count == b.Worksheet.ChartObjects().Count
            for number in range(1,b.Worksheet.ChartObjects().Count+1):
                charts = [sh.ChartObjects(number).Chart for sh in (a.Worksheet,b.Worksheet)]
                assert charts[0].SeriesCollection().Count == charts[1].SeriesCollection().Count
                for k in range(1,charts[1].SeriesCollection().Count+1):
                    left,right = [chart.SeriesCollection(k) for chart in charts]
                    assert left.Values == right.Values and left.XValues == right.XValues
            assert a.Worksheet.PivotTables().Count == b.Worksheet.PivotTables().Count
            for number in range(1,b.Worksheet.PivotTables().Count+1):
                pivots = [sh.PivotTables(number) for sh in (a.Worksheet,b.Worksheet)]
                first,second=pivots
                assert first.Name==second.Name and first.TableRange1.Address==second.TableRange1.Address
                assert first.TableRange2.Address==second.TableRange2.Address and (first.RowGrand,first.ColumnGrand)==(second.RowGrand,second.ColumnGrand)
                assert [first.RowFields.Item(j).SourceName for j in range(1,first.RowFields.Count+1)]==[second.RowFields.Item(j).SourceName for j in range(1,second.RowFields.Count+1)]
                for attr in ('ColumnFields','PageFields'):
                    def axis_names(pivot):
                        fields = getattr(pivot, attr)
                        return [(f.SourceName,str(f.CurrentPage.Name)) if attr=='PageFields' else f.SourceName for f in sorted(fields,key=lambda x:x.Position)]
                    assert axis_names(first)==axis_names(second), (attr,axis_names(first),axis_names(second))
                assert [(first.DataFields.Item(j).SourceName,first.DataFields.Item(j).Function) for j in range(1,first.DataFields.Count+1)]==[(second.DataFields.Item(j).SourceName,second.DataFields.Item(j).Function) for j in range(1,second.DataFields.Count+1)]
        return count
    finally:
        actual.Close(False)


def refresh_and_resave(excel, path, saved, change):
    book = excel.Workbooks.Open(str(path), UpdateLinks=0, ReadOnly=True, CorruptLoad=0)
    try:
        pivot = book.Worksheets(change['sheet']).PivotTables(change['pivot'])
        excel.CalculateFull()
        pivot.PivotCache().Refresh()
        assert pivot.RefreshTable()
        book.SaveCopyAs(str(saved))
    finally:
        book.Close(False)


def native_ribbon_history(book, case, folder):
    """Capture the built-in command and its real Excel undo stack before opening any other workbook."""
    change, = case['changes']
    sheet = book.Worksheets(change['sheet'])
    pivot = sheet.PivotTables(change['pivot'])
    sheet.Activate()
    sheet.Range(case['nativeRibbon']['cell']).Select()
    bars = book.Application.CommandBars
    states = {}
    paths = {}
    for stage, command in [('base',None), ('edit',case['nativeRibbon']['command']), ('undo','Undo'), ('redo','Redo')]:
        if command:
            assert bars.GetEnabledMso(command), ('Excel command unavailable', command)
            bars.ExecuteMso(command)
            book.Application.CalculateUntilAsyncQueriesDone()
        states[stage] = (pivot.TableRange2.Address, pivot.TableRange2.Value2, pivot.TableRange2.Formula,
                         [(p.SourceName,str(p.CurrentPage)) for p in pivot.PageFields])
        paths[stage] = folder/('ribbon-'+stage+'.xlsx')
        book.SaveCopyAs(str(paths[stage]))
    assert states['base'] != states['edit']
    assert states['undo'] == states['base'] and states['redo'] == states['edit']
    return paths


def run(probe, source, case, output):
    digest = hashlib.sha256(source.read_bytes()).hexdigest()
    result = dict(sourceSHA256=digest, probeSHA256=hashlib.sha256(probe.read_bytes()).hexdigest(), stages=[])
    excel = DispatchEx('Excel.Application')
    excel.DisplayAlerts = False
    excel.Visible = False
    excel.AutomationSecurity = 3
    try:
        result['excel'] = [excel.Version,excel.Build]
        with TemporaryDirectory(prefix='excellens-ui-qa-') as tmp:
            root = Path(tmp)
            original = root/'reference.xlsx'
            shutil.copyfile(source, original)
            ref = excel.Workbooks.Open(str(original), UpdateLinks=0, ReadOnly=True, CorruptLoad=0)
            try:
                ribbon = None
                if 'nativeRibbon' in case:
                    assert 'input' not in case, 'Prepare the source edit before capturing the native undo stack'
                    ribbon = native_ribbon_history(ref,case,root)
                    result['nativeRibbonHistory'] = [case['nativeRibbon']['command'],'Undo','Redo']
                def select_reference(path):
                    nonlocal ref
                    ref.Close(False)
                    ref = excel.Workbooks.Open(str(path), UpdateLinks=0, ReadOnly=True, CorruptLoad=0)
                with service(probe,root,source.read_bytes()) as call:
                    opened = call('/api/open')
                    state = call('/api/edits/start',dict(book=opened['book']))
                    doc = {k:state['document'][k] for k in ('id','binding')}
                    rev, book = state['document']['revision'],opened['book']
                    def step(action, change=None):
                        nonlocal rev, book
                        op = dict(request=uuid4().hex, revision=rev,action=action)
                        if change: op['structure']=change
                        response = call('/api/edits/commit',dict(document=doc,operation=op))
                        rev = response['committed']
                        assert call('/api/edits/commit',dict(document=doc,operation=op))==response
                        if response['book'] != book:
                            book = call('/api/edits/base',dict(book=book,document=doc,revision=rev))['book']
                        assert book==response['book']
                        call('/api/edits/grid',dict(document=doc,revision=rev,book=book,mode='projection'))
                    def verify(stage, index):
                        x = call('/api/edits/save',dict(document=doc,revision=rev))
                        artifact=root/('generated-'+rev+'.xlsx')
                        artifact.write_bytes(call(x['downloadUrl']))
                        saved_caches = case.get('chartCaches', {}).get(str(index))
                        if saved_caches:
                            check_chart_cache(artifact, saved_caches)
                        record = dict(stage=stage, revision=rev)
                        if case.get('comparePivotDisk'):
                            native = root/('native-saved-'+rev+'.xlsx')
                            ref.SaveCopyAs(str(native))
                            pivot = case['changes'][0]
                            record['savedPivot'] = compare_saved_pivot(artifact,native,pivot['sheet'],case['rectangles'][pivot['sheet']],pivot['pivot'],records=index>0)
                        formats = case.get('formatCells') if index > 0 else None
                        record['numbers'] = comparison_stats()
                        n = compare_books(excel,ref,artifact,case['rectangles'],case.get('names',False),formats,record['numbers'])
                        record['cells']=n
                        if saved_caches: record['chartCacheParts'] = len(saved_caches)
                        if case.get('refreshAndResave'):
                            reference = root/'reference-state.xlsx'
                            ref.SaveCopyAs(str(reference))
                            files = [root/('excel-'+rev+str(i)+'.xlsx') for i in range(2)]
                            for path, dest in zip([reference, artifact], files):
                                refresh_and_resave(excel, path, dest, case['changes'][0])
                            checked = excel.Workbooks.Open(str(files[0]), UpdateLinks=0, ReadOnly=True, CorruptLoad=0)
                            record['numbersAfterRefresh'] = comparison_stats()
                            try: record['refreshedReopenedCells'] = compare_books(excel,checked,files[1],case['rectangles'],format_cells=formats,numeric=record['numbersAfterRefresh'])
                            finally: checked.Close(False)
                        result['stages'].append(record)
                        print(stage,rev,n,flush=True)
                    cell = case.get('input')
                    if cell:
                        addr = ref.Worksheets(cell['sheet']).Cells(cell['row'],cell['col'])
                        addr.Value2 = float(cell['value']['raw'])
                        op = dict(action='change',request=uuid4().hex,revision=rev,changes=[cell])
                        rev = call('/api/edits/commit',dict(document=doc,operation=op))['committed']
                    for i,change in enumerate(case['changes']):
                        step('structure',change)
                        if ribbon:
                            select_reference(ribbon['edit'])
                        else:
                            native_operation(ref,change)
                        verify(change['kind'], i+1)
                    if len(case['changes'])==1 or case.get('fullHistory'):
                        # Reconstruct each Excel reference from the immutable source.
                        for index in range(len(case['changes'])-1, -1, -1):
                            select_reference(ribbon['undo'] if ribbon else original)
                            if cell: ref.Worksheets(cell['sheet']).Cells(cell['row'],cell['col']).Value2 = float(cell['value']['raw'])
                            for change in case['changes'][:index]: native_operation(ref,change)
                            step('undo')
                            verify('undo', index)
                        for index,change in enumerate(case['changes'],1):
                            step('redo')
                            if ribbon:
                                select_reference(ribbon['redo'])
                            else:
                                native_operation(ref,change)
                            verify('redo', index)
            finally:
                ref.Close(False)
    finally:
        excel.Quit()
    assert hashlib.sha256(source.read_bytes()).hexdigest()==digest
    result.update(result='PASS',sourceUnchanged=True)
    output.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ['probe','source','case','output']:
        parser.add_argument('--'+name,type=Path,required=True)
    opts=parser.parse_args()
    pythoncom.CoInitialize()
    try:
        run(opts.probe.resolve(),opts.source.resolve(),json.loads(opts.case.read_text(encoding='utf-8')),opts.output)
    finally:
        pythoncom.CoUninitialize()
