"""Compare an editing revision to native Excel at every formula address (Windows QA).

Example: python tools/qa/edit_recalculation_excel.py --probe artifacts/qa/uiprobe.exe
  --source report.xlsx --input "Data!B2=17" --output validation/private/report.json
Requires Excel and pywin32 on the verification machine.
"""
import argparse
from collections import Counter
from contextlib import contextmanager, ExitStack
from hashlib import sha256
import json
from pathlib import Path
import posixpath
import re
import shutil
import struct
import subprocess
from tempfile import TemporaryDirectory
import time
from urllib.parse import urlencode, urlsplit
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener
from uuid import uuid4
import xml.etree.ElementTree as ET
from zipfile import ZipFile

import pythoncom
from win32com.client import DispatchEx

SML = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'
REL = '{http://schemas.openxmlformats.org/officeDocument/2006/relationships}'


def snapshot(path):
    cells = {}
    with ZipFile(path) as z:
        rels = ET.fromstring(z.read('xl/_rels/workbook.xml.rels'))
        parts = {r.get('Id'): posixpath.normpath(posixpath.join('xl', r.get('Target'))).lstrip('/') for r in rels}
        shared = []
        if 'xl/sharedStrings.xml' in z.namelist():
            shared = [''.join(si.itertext()) for si in ET.fromstring(z.read('xl/sharedStrings.xml'))]
        for sheet in ET.fromstring(z.read('xl/workbook.xml')).find(SML + 'sheets'):
            for c in ET.fromstring(z.read(parts[sheet.get(REL+'id')])).iter(SML+'c'):
                if c.find(SML+'f') is None:
                    continue
                kind, raw = c.get('t','n'), c.findtext(SML+'v')
                if kind == 's':
                    kind, raw = 'str', shared[int(raw)]
                if kind == 'b':
                    raw = 'TRUE' if raw == '1' else 'FALSE'
                cells[sheet.get('name'), c.get('r')] = (kind, raw)
    return cells


def compare(left, right, absolute_tolerance=0):
    if left is None or right is None or left[0] != right[0]:
        return 'different'
    if left[0] != 'n' or left[1] in (None, '') or right[1] in (None, ''):
        return 'exact' if left == right else 'different'
    a, b = float(left[1]), float(right[1])
    if a == b:
        return 'exact'
    # A native SaveCopyAs serializes the binary value as decimal OOXML.
    # Both values have the same sign, so their ordered IEEE encodings give
    # the number of representable doubles between them.
    bits = lambda v: struct.unpack('>Q',struct.pack('>d',v))[0]
    if (a<0)==(b<0) and abs(bits(a)-bits(b))<=8:
        return 'within8ULP'
    if abs(a-b)<=absolute_tolerance:
        return 'withinAbsoluteTolerance'
    return 'different'


@contextmanager
def service(probe, local, source, filename="case.xlsx"):
    with subprocess.Popen([str(probe), '-duration','30m','-storage',str(local)], stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, creationflags=subprocess.CREATE_NO_WINDOW) as child:
        try:
            base = json.loads(child.stdout.readline())['url'].rstrip('/')
            assert urlsplit(base).hostname == '127.0.0.1'
            http = build_opener(ProxyHandler({}))
            with http.open(base, timeout=30) as page:
                token = re.search(r'<meta name="app-token" content="([a-z0-9]+)"',page.read().decode())[1]

            def call(route, args=None):
                headers = {'X-ExcelLens-Token':token}
                data = None
                if route == '/api/open':
                    boundary = uuid4().hex
                    headers['Content-Type'] = 'multipart/form-data; boundary='+boundary
                    data = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n\r\n'.encode()
                            + source + f'\r\n--{boundary}--\r\n'.encode())
                elif args is not None:
                    headers['Content-Type'] = 'application/json'
                    data = json.dumps(args).encode()
                try:
                    with http.open(Request(base+route,data=data,headers=headers), timeout=240) as response:
                        if route.startswith('/api/edits/download?'):
                            return response.read()
                        return json.load(response)
                except HTTPError as err:
                    raise RuntimeError(route,err.code,err.read(2048).decode()) from err
            yield call
        finally:
            child.terminate()


def reader_tiles(call, coords):
    tiles = {}
    windows = {}
    for sheet, ref in coords:
        row, col = coordinates(ref)
        windows.setdefault(sheet,set()).add(((row-1)//150*150+1,(col-1)//80*80+1))
    for sheet, areas in windows.items():
        job = call('/api/index/start',{'sheet':sheet})
        if not job.get('ready'):
            while True:
                progress = call('/api/index/status?'+urlencode({'id':job['jobId']}))
                if progress['status'] == 'done':
                    break
                assert progress['status'] not in ('error','cancelled'), progress
                time.sleep(.05)
        for row, col in sorted(areas):
            params = dict(sheet=sheet,row=row,col=col,cols=80,limit=150,rowWindow=1)
            for data in call('/api/sheet?'+urlencode(params))['rows'] or []:
                for cell in data['cells'] or []:
                    if cell.get('hasFormula'):
                        tiles[sheet,cell['ref']] = cell
    return tiles


def coordinates(ref):
    letters, row = re.fullmatch(r'([A-Z]+)([1-9]\d*)',ref).groups()
    col = 0
    for letter in letters:
        col = col*26 + ord(letter)-64
    return int(row), col


def stage_values(tiles, projection):
    actual = dict(tiles)
    for field in ('cells','calculated'):
        for sheet, items in projection.get(field,{}).items():
            for ref, cell in items.items():
                if cell is None:
                    continue
                if cell.get('hasFormula'):
                    actual[sheet,ref] = cell
                else:
                    actual.pop((sheet,ref), None)
    out = {}
    for key,cell in actual.items():
        kind, raw = cell.get('type') or 'n', cell.get('raw')
        if kind == 'b':
            raw = {'0':'FALSE','1':'TRUE'}.get(raw,raw)
        if kind in ('inlineStr','s'):
            kind = 'str'
        if cell.get('cacheMissing') or projection.get('affected',{}).get(key[0]):
            kind = 'pending'
        for area in projection.get('generated',[]):
            start=area['start']
            if start['sheet']==key[0]:
                row,col=coordinates(key[1])
                if start['row']<=row<=area['endRow'] and start['col']<=col<=area['endCol'] and (area.get('pivot') or (row,col)!=(start['row'],start['col'])):
                    kind='pending'
        out[key] = (kind,raw)
    return out


def run(options, app, directory, report):
    source = options.source.read_bytes()
    report['sourceSHA256'] = sha256(source).hexdigest()
    original = snapshot(options.source)
    local = directory/'native.xlsx'
    shutil.copyfile(options.source,local)
    miss = pythoncom.Missing
    book = app.Workbooks.Open(str(local),0,True,miss,miss,miss,True,miss,miss,miss,False,miss,False,miss,0)
    try:
        assert book.ReadOnly
        inputs = []
        for arg in options.input:
            address, number = arg.rsplit('=',1)
            sheet, ref = address.rsplit('!',1)
            cell = book.Worksheets.Item(sheet).Range(ref)
            assert not cell.HasFormula and isinstance(cell.Value2,(int,float))
            inputs.append((sheet,ref,cell.Value2,float(number)))
        with ExitStack() as processes:
            call=processes.enter_context(service(options.probe.resolve(),directory,source))
            opened = call('/api/open')
            started = call('/api/edits/start',dict(book=opened['book']))['document']
            key = {k:started[k] for k in ('id','binding')}
            revision = started['revision']
            tiles = reader_tiles(call,original)
            assert set(tiles)==set(original),(len(tiles),len(original))
            baseline = None
            views = {}
            for label, action in [('base',None),('edit','change'),('undo','undo'),('redo','redo')]:
                if label=='redo' and options.restart_before_redo:
                    processes.close()
                    call=processes.enter_context(service(options.probe.resolve(),directory,source))
                    reopened=call('/api/edits/base',dict(document=key,book=''))
                    assert reopened['book']==opened['book']
                    assert call('/api/edits/state',dict(document=key))['document']['revision']==revision
                    tiles=reader_tiles(call,original)
                    report['restartedBeforeRedo']=True
                begin = time.perf_counter()
                if action:
                    op = dict(request=uuid4().hex,action=action,revision=revision)
                    if action=='change':
                        op['changes']=[]
                        for sheet,ref,_,number in inputs:
                            row,col = coordinates(ref)
                            op['changes'].append(dict(sheet=sheet,row=row,col=col,value=dict(kind='number',raw=str(number))))
                    revision = call('/api/edits/commit',dict(document=key,operation=op))['document']['revision']
                request = dict(document=key,revision=revision,book=opened['book'],mode='projection')
                projection = call('/api/edits/grid',request)
                elapsed = round((time.perf_counter()-begin)*1000,1)
                if action:
                    for sheet,ref,prior,number in inputs:
                        book.Worksheets.Item(sheet).Range(ref).Value2 = prior if label=='undo' else number
                app.CalculateFullRebuild()
                app.CalculateUntilAsyncQueriesDone()
                native = directory/(label+'.xlsx')
                book.SaveCopyAs(str(native))
                expected = snapshot(native)
                actual = stage_values(tiles,projection)
                counts = Counter()
                mismatches = []
                changed = Counter()
                for k in sorted(set(expected)|set(actual)):
                    match = compare(actual.get(k),expected.get(k),options.absolute_tolerance)
                    counts[match]+=1
                    if match=='different' and len(mismatches)<20:
                        mismatches.append([*k,actual.get(k),expected.get(k)])
                    if baseline is not None and compare(baseline.get(k),expected.get(k))=='different':
                        changed[k[0]]+=1
                if baseline is None:
                    baseline=expected
                if label in ('undo','redo'):
                    assert actual==views['base' if label=='undo' else 'edit'], ('revision did not restore every value',label)
                views[label]=actual
                differences=[abs(float(actual[k][1])-float(expected[k][1])) for k in actual.keys() & expected.keys()
                             if actual[k][0]==expected[k][0]=='n' and actual[k][1] and expected[k][1]]
                result=dict(stage=label,revision=revision,formulaCells=len(expected),comparison=dict(counts),
                            changedBySheet=dict(changed),calculated=sum(map(len,projection.get('calculated',{}).values())),
                            generatedAreas=len(projection.get('generated',[])),commitAndGridMs=elapsed,
                            maxAbsoluteNumericDifference=max(differences,default=0),mismatches=mismatches)
                for sheet,ref,prior,number in inputs:
                    cell=call('/api/edits/read',dict(document=key,revision=revision,sheet=sheet,ref=ref))['value']
                    assert cell['kind']=='number' and float(cell['raw'])==(prior if label in ('base','undo') else number)
                report['stages'].append(result)
                print(label,counts,'calculated',result['calculated'],'changed',sum(changed.values()),elapsed,flush=True)
    finally:
        book.Close(False)
    assert sha256(options.source.read_bytes()).hexdigest()==report['sourceSHA256']
    report['sourceUnchanged']=True
    assert all(not stage['comparison'].get('different') and stage['formulaCells'] for stage in report['stages'])
    assert report['stages'][1]['changedBySheet'] and not report['stages'][2]['changedBySheet']
    assert report['stages'][1]['changedBySheet']==report['stages'][3]['changedBySheet']


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--probe',type=Path,required=True)
    parser.add_argument('--source',type=Path,required=True)
    parser.add_argument('--input',action='append',required=True)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--absolute-tolerance',type=float,default=0,help='Optional absolute number tolerance, reported separately from exact and ULP matches')
    parser.add_argument('--restart-before-redo',action='store_true')
    options=parser.parse_args()
    report=dict(result='FAIL',absoluteTolerance=options.absolute_tolerance,stages=[])
    pythoncom.CoInitialize()
    try:
        app=DispatchEx('Excel.Application')
        try:
            app.Visible=False
            app.DisplayAlerts=False
            app.AskToUpdateLinks=False
            app.AutomationSecurity=3
            report.update(excelVersion=str(app.Version),excelBuild=str(app.Build))
            with TemporaryDirectory(prefix='excellens-ui-qa-') as work:
                run(options,app,Path(work),report)
            report['result']='PASS'
        finally:
            app.Quit()
    finally:
        pythoncom.CoUninitialize()
        options.output.parent.mkdir(parents=True,exist_ok=True)
        options.output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')


if __name__=='__main__':
    main()
