"""Compare the saved series and hierarchy caches before native Excel opens them."""
from xml.etree import ElementTree as ET
from zipfile import ZipFile


def chart_series(path):
    result = {}
    with ZipFile(path) as book:
        for file in book.namelist():
            if not file.startswith('xl/charts/chart') or not file.endswith('.xml'):
                continue
            root = ET.fromstring(book.read(file))
            source = root.find('{*}pivotSource')
            if source is None:
                continue
            # Excel embeds the current filename on each SaveCopyAs.
            key = (file, source.findtext('{*}name').split(']')[-1], source.find('{*}fmtId').get('val'))
            series = []
            for s in root.findall('.//{*}ser'):
                content = [int(s.find('{*}idx').get('val')), int(s.find('{*}order').get('val'))]
                for name in ('tx', 'cat', 'val'):
                    role = s.find('{*}'+name)[0]
                    cache = next(c for c in role if c.tag.endswith('Cache'))
                    levels = cache.findall('{*}lvl') or [cache]
                    points = [[(int(p.get('idx')), p.findtext('{*}v')) for p in lev.findall('{*}pt')] for lev in levels]
                    content.append((role.tag, role.findtext('{*}f'), cache.findtext('{*}formatCode'),
                                    int(cache.find('{*}ptCount').get('val')), points))
                series.append(content)
            result[key] = series
        bindings = {}
        for file in book.namelist():
            if not file.startswith('xl/pivotTables/pivotTable') or not file.endswith('.xml'):
                continue
            table = ET.fromstring(book.read(file))
            def tree(node):
                return (node.tag, sorted(node.attrib.items()), [tree(child) for child in node])
            formats = table.find('{*}chartFormats')
            if formats is not None:
                bindings[table.get('name')] = tree(formats)
    return result, bindings


def compare_pivot_charts(actual, expected):
    (a, af), (b, bf) = (chart_series(path) for path in (actual, expected))
    assert af == bf, ('chart format member bindings', af, bf)
    assert a and a.keys() == b.keys(), (a.keys(), b.keys())
    count = 0
    for key, series in a.items():
        assert len(series) == len(b[key]), (key, len(series), len(b[key]))
        for i, (s, target) in enumerate(zip(series, b[key])):
            for j, (value, wanted) in enumerate(zip(s, target)):
                assert value == wanted, (key, i, j, value, wanted)
                if j >= 2:
                    count += sum(len(level) for level in value[-1])
    return count
