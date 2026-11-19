"""Compare saved pivot cells and axis-item paths before Excel can refresh them."""
import math
import posixpath
import re
from xml.etree import ElementTree as ET
from zipfile import ZipFile

S = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'
R = '{http://schemas.openxmlformats.org/officeDocument/2006/relationships}'


def coordinate(ref):
    letters, row = re.fullmatch(r'([A-Z]+)(\d+)', ref).groups()
    col = 0
    for letter in letters:
        col = col * 26 + ord(letter)-64
    return int(row), col


def target(base, link):
    return posixpath.normpath(posixpath.join(base,link)).lstrip('/')


def saved_pivot(path, sheet, area, name, records=False):
    top,left = coordinate(area.split(':')[0])
    bottom,right = coordinate(area.split(':')[-1])
    with ZipFile(path) as archive:
        book=ET.fromstring(archive.read('xl/workbook.xml'))
        sheets=book.find(S+'sheets')
        links={r.get('Id'):target('xl',r.get('Target')) for r in ET.fromstring(archive.read('xl/_rels/workbook.xml.rels'))}
        part=links[next(s.get(R+'id') for s in sheets if s.get('name')==sheet)]
        root=ET.fromstring(archive.read(part))
        shared=[]
        if 'xl/sharedStrings.xml' in archive.namelist():
            shared=[''.join(t.text or '' for t in si.iter(S+'t')) for si in ET.fromstring(archive.read('xl/sharedStrings.xml'))]
        values={}
        for c in root.iter(S+'c'):
            row,col=coordinate(c.get('r'))
            if not(top<=row<=bottom and left<=col<=right):continue
            kind=c.get('t','n')
            value=c.findtext(S+'v')
            if kind=='inlineStr':
                value=''.join(t.text or '' for t in c.iter(S+'t'));kind='s'
            elif kind=='s' and value is not None:value=shared[int(value)]
            elif kind=='n' and value is not None:value=float(value)
            formula=c.find(S+'f')
            if formula is not None:
                # Compare actual Excel Formula and Value2 for surrounding worksheet
                # formulas in compare_books; their saved calculation caches may be absent.
                values[(row,col)]=('formula',None)
            elif value is not None:values[(row,col)]=(kind,value)
        sheetrels=posixpath.join(posixpath.dirname(part),'_rels',posixpath.basename(part)+'.rels')
        for rel in ET.fromstring(archive.read(sheetrels)):
            if not rel.get('Type').endswith('/pivotTable'):continue
            table=ET.fromstring(archive.read(target(posixpath.dirname(part),rel.get('Target'))))
            if table.get('name')==name:break
        else:raise AssertionError(name)
        cacheid=next(c.get(R+'id') for c in book.find(S+'pivotCaches') if c.get('cacheId')==table.get('cacheId'))
        cachepart=links[cacheid]
        definition=ET.fromstring(archive.read(cachepart))
        cache=definition.find(S+'cacheFields')
        fields=table.find(S+'pivotFields')
        def item(f, pos):
            it=fields[f].find(S+'items')[pos]
            shared=cache[f].find(S+'sharedItems')[int(it.get('x','0'))]
            kind=shared.tag.split('}')[-1]
            value=shared.get('v')
            return kind,float(value) if kind=='n' else value
        axes={}
        for axis in ('row','col'):
            indexes=[int(f.get('x')) for f in table.findall(S+axis+'Fields/'+S+'field')]
            previous=[]
            items=[]
            for it in table.findall(S+axis+'Items/'+S+'i'):
                kind=it.get('t','data')
                if kind=='grand':current=[]
                else:
                    current=previous[:int(it.get('r','0'))]+[int(x.get('v','0')) for x in it]
                items.append((kind,tuple(item(f,v) for f,v in zip(indexes,current))))
                previous=current
            axes[axis]=([cache[i].get('name') for i in indexes],items)
        pages=[]
        for f in table.findall(S+'pageFields/'+S+'pageField'):
            k=int(f.get('fld'))
            its=fields[k].find(S+'items')
            selected=None
            if f.get('item') is not None:selected=[item(k,int(f.get('item')))]
            elif any(i.get('h') in ('1','true') for i in its):
                selected=[item(k,j) for j,i in enumerate(its) if i.get('t','data')=='data' and i.get('h') not in ('1','true')]
            pages.append((cache[k].get('name'),selected))
        loc=table.find(S+'location')
        attrs={k:loc.get(k,'0') for k in ('ref','firstDataRow','firstDataCol','rowPageCount','colPageCount')}
        if not records:return values,axes,pages,attrs
        cachelinks=posixpath.join(posixpath.dirname(cachepart),'_rels',posixpath.basename(cachepart)+'.rels')
        rec=next(r for r in ET.fromstring(archive.read(cachelinks)) if r.get('Type').endswith('/pivotCacheRecords'))
        raw=ET.fromstring(archive.read(target(posixpath.dirname(cachepart),rec.get('Target'))))
        def typed(el):
            k=el.tag.split('}')[-1];v=el.get('v')
            if k=='n':v=float(v)
            if k=='b':v=v in ('true','1')
            return k,v
        dictionaries=[[typed(el) for el in field.findall(S+'sharedItems/*')] for field in cache]
        rows=[]
        for row in raw.findall(S+'r'):
            vals=[]
            for i,e in enumerate(row):
                if e.tag==S+'x':
                    idx=int(e.get('v'));v=('m',None) if idx==4294967295 else dictionaries[i][idx]
                else:v=typed(e)
                vals.append(v)
            rows.append(vals)
        assert len(rows)==int(raw.get('count'))==int(definition.get('recordCount'))
        return values,axes,pages,attrs,([f.get('name') for f in cache],rows)


def compare_saved_pivot(artifact, native, sheet, area, name, records=False):
    actual,expected=[saved_pivot(f,sheet,area,name,records) for f in (artifact,native)]
    for i,key in enumerate(('axes','pages','location'),1):
        assert actual[i]==expected[i], (key,actual[i],expected[i])
    for pos in set(actual[0])|set(expected[0]):
        a,b=actual[0].get(pos),expected[0].get(pos)
        if a!=b:
            assert a and b and a[0]==b[0]=='n' and math.isclose(a[1],b[1],rel_tol=2e-15,abs_tol=1e-12), (sheet,pos,a,b)
    totals={}
    if records:
        ac,ar=actual[4];bc,br=expected[4]
        assert ac==bc and len(ar)==len(br), (ac,bc,len(ar),len(br))
        compared=0
        for row,(a,b) in enumerate(zip(ar,br),1):
            assert len(a)==len(b)==len(ac)
            for col,(v,w) in enumerate(zip(a,b)):
                assert v==w or v[0]==w[0]=='n' and math.isclose(v[1],w[1],rel_tol=2e-15,abs_tol=1e-12),(row,ac[col],v,w)
                compared+=1
        totals['cacheRecords']=len(ar)
        totals['cacheValues']=compared
    for axis,(fields,items) in expected[1].items():
        totals[axis+'Grand']=sum(k=='grand' for k,_ in items)
        totals[axis+'GroupOrSubtotal']=sum(k not in ('blank','grand') and 0<len(p)<len(fields) for k,p in items)
    return totals
