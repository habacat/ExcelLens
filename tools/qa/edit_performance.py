"""Alternate production probes on the same editing workload and Excel oracle.

A private JSON plan supplies source, sheet, target (top-left empty cell),
count, formulas, input {sheet, ref, value}, and rounds per named case.
Each run gets fresh storage. Timings include loopback HTTP, JSON and the
normal file verifier. Downloads are compared by uncompressed ZIP part.
"""
import argparse
from hashlib import sha256
from io import BytesIO
import json
from pathlib import Path
from statistics import median
from tempfile import TemporaryDirectory
from time import perf_counter
from uuid import uuid4
from zipfile import ZipFile

from openpyxl.utils.cell import coordinate_to_tuple, get_column_letter
from win32com.client import DispatchEx
from edit_recalculation_excel import compare, service
from edit_table_excel import native_open


def gesture_medians(runs):
    stages = dict(single=('single','singleProjection'),batch=('batch','batchProjection'),
                  inputAndRecalculation=('precedent','recalculation'), undo=('undo','undoProjection'), redo=('redo','redoProjection'))
    return {name:{stage:round(median(r['ms'][first]+r['ms'][second] for r in runs if r['probe']==name),2)
                  for stage,(first,second) in stages.items()} for name in ('before','after')}


def oracle(excel, case):
    book = native_open(excel, Path(case['source']).resolve())
    try:
        sheet = book.Worksheets(case['sheet'])
        row, col = coordinate_to_tuple(case['target'])
        for i, formula in enumerate(case['formulas']):
            cell = sheet.Cells(row+i, col)
            if cell.Value2 is not None:
                raise ValueError('Performance target must be empty in the private workbook')
            cell.Formula2 = formula
        def values():
            excel.Calculate()
            return [sheet.Cells(row+i, col).Value2 for i in range(len(case['formulas']))]
        original = values()
        inp = case['input']
        book.Worksheets(inp['sheet']).Range(inp['ref']).Value2 = inp['value']
        updated = values()
        assert all(a != b for a, b in zip(original, updated)), 'each benchmark formula must depend on the input'
        return original, updated
    finally:
        book.Close(False)


def timed_run(probe, source, case, expected):
    times = {}
    row, col = coordinate_to_tuple(case['target'])
    letter = get_column_letter(col)
    sheet, formulas = case['sheet'], case['formulas']
    def cell(r):
        return dict(sheet=sheet,row=row+r,col=col,value=dict(kind='formula',raw=formulas[r % len(formulas)]))
    with TemporaryDirectory(prefix='excellens-ui-qa-') as tmp, service(probe,Path(tmp),source) as call:
        def timed(label, route, args):
            start = perf_counter()
            result = call(route,args)
            times[label] = round(1000*(perf_counter()-start), 2)
            return result
        book = call('/api/open')['book']
        doc = timed('start','/api/edits/start',dict(book=book))['document']
        key = {k:doc[k] for k in ('id','binding')}
        rev = doc['revision']
        def project(label):
            return timed(label,'/api/edits/grid',dict(book=book,document=key,revision=rev,mode='projection'))
        def change(label, action, changes=None):
            op = dict(action=action,revision=rev,request=uuid4().hex)
            if changes:
                op.update(changes=changes,inferNumberFormat=True,expandTable=True)
            return timed(label,'/api/edits/commit',dict(document=key,operation=op,direct=True))['committed']
        def check(label, count, native):
            results = project(label)['calculated'][sheet]
            for i in range(count):
                c = results[letter+str(row+i)]
                assert c['formula'] == formulas[i % len(formulas)] and c['type'] == 'n', (label,i,c)
                verdict = compare(('n',c['raw']),('n',str(native[i % len(native)])),1e-12)
                assert verdict != 'different', (label,i,c['raw'],native)
        project('initialProjection')
        rev = change('single','change',[cell(0)])
        check('singleProjection',1,expected[0])
        count = case['count']
        rev = change('batch','change',[cell(i) for i in range(1,count)])
        check('batchProjection',count,expected[0])
        inp = case['input']
        r,c = coordinate_to_tuple(inp['ref'])
        rev = change('precedent','change',[dict(sheet=inp['sheet'],row=r,col=c,value=dict(kind='number',raw=str(inp['value'])))])
        check('recalculation',count,expected[1])
        args = dict(document=key,revision=rev)
        artifact = timed('save','/api/edits/save',args)
        raw = call(artifact['downloadUrl'])
        assert len(raw) == artifact['bytes']
        assert timed('repeatSave','/api/edits/save',args)['downloadUrl'] == artifact['downloadUrl']
        with ZipFile(BytesIO(raw)) as output:
            parts = {n:sha256(output.read(n)).hexdigest() for n in output.namelist()}
        rev = change('undo','undo')
        check('undoProjection',count,expected[0])
        rev = change('redo','redo')
        check('redoProjection',count,expected[1])
    return times, parts


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('plan',type=Path)
    p.add_argument('baseline',type=Path)
    p.add_argument('candidate',type=Path)
    p.add_argument('output',type=Path)
    opts = p.parse_args()
    plan = json.loads(opts.plan.read_text('utf8'))
    probes = {'before':opts.baseline.resolve(),'after':opts.candidate.resolve()}
    report = {'probeSHA256':{k:sha256(path.read_bytes()).hexdigest() for k,path in probes.items()},'cases':{}}
    excel = DispatchEx('Excel.Application')
    excel.DisplayAlerts = excel.EnableEvents = False
    excel.AutomationSecurity = 3
    try:
        native = {case['name']:oracle(excel,case) for case in plan}
        report['excel'] = [excel.Version,excel.Build]
    finally:
        excel.Quit()
    for case in plan:
        source = Path(case['source'])
        raw = source.read_bytes()
        info = dict(sourceSHA256=sha256(raw).hexdigest(),formulaCells=case['count'],runs=[])
        report['cases'][case['name']] = info
        reference = None
        for i in range(case['rounds']):
            for name in (('before','after') if i%2==0 else ('after','before')):
                times, parts = timed_run(probes[name],raw,case,native[case['name']])
                if reference is None: reference = parts
                assert reference == parts, 'Exported workbook parts differ between runs'
                info['runs'].append(dict(probe=name,ms=times))
                print(case['name'],i+1,name,times['recalculation'],times['save'],flush=True)
                opts.output.write_text(json.dumps(report,ensure_ascii=False,indent=2),'utf8')
        info['medianMs'] = {name:{k:median(r['ms'][k] for r in info['runs'] if r['probe']==name) for k in times} for name in probes}
        info['commitAndProjectionMedianMs'] = gesture_medians(info['runs'])
        info['identicalExportParts'] = len(reference)
        info['checkedFormulaCellsPerRun'] = 1+4*case['count']
        assert sha256(source.read_bytes()).hexdigest() == info['sourceSHA256']
        opts.output.write_text(json.dumps(report,ensure_ascii=False,indent=2),'utf8')


if __name__ == '__main__':
    main()
