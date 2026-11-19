"""Read SpreadsheetML formula ownership and caches before a native open can repair them."""
import math
from xml.etree import ElementTree as ET
from zipfile import ZipFile

from pivot_reference import S, R, coordinate, target


def rich_errors(book):
    part = 'xl/richData/rdrichvalue.xml'
    if part not in book.namelist():
        return {}
    metadata = ET.fromstring(book.read('xl/metadata.xml'))
    types = [t.get('name') for t in metadata.find(S + 'metadataTypes')]
    future = metadata.find(S + 'futureMetadata[@name="XLRICHVALUE"]')
    if future is None:
        return {}
    indexes = [int(b.find('.//{*}rvb').get('i')) for b in future]
    structures = list(ET.fromstring(book.read('xl/richData/rdrichvaluestructure.xml')))
    records = list(ET.fromstring(book.read(part)))
    errors = {}
    for i, bk in enumerate(metadata.find(S + 'valueMetadata'), 1):
        ref = bk.find(S + 'rc')
        if types[int(ref.get('t')) - 1] != 'XLRICHVALUE':
            continue
        record = records[indexes[int(ref.get('v'))]]
        definition = structures[int(record.get('s'))]
        if definition.get('t') == '_error':
            errors[str(i)] = {key.get('n'): int(v.text) for key, v in zip(definition, record)}
    return errors


def cached_regions(path, regions):
    result = {}
    with ZipFile(path) as archive:
        book = ET.fromstring(archive.read('xl/workbook.xml'))
        paths = {r.get('Id'): target('xl', r.get('Target')) for r in
                 ET.fromstring(archive.read('xl/_rels/workbook.xml.rels'))}
        strings = []
        if 'xl/sharedStrings.xml' in archive.namelist():
            strings = [''.join(t.text or '' for t in s.iter(S + 't')) for s in
                       ET.fromstring(archive.read('xl/sharedStrings.xml'))]
        errors = rich_errors(archive)
        for sheet in book.find(S + 'sheets'):
            name = sheet.get('name')
            if name not in regions:
                continue
            area = regions[name].split(':')
            (top, left), (bottom, right) = coordinate(area[0]), coordinate(area[-1])
            for row in range(top, bottom + 1):
                for col in range(left, right + 1):
                    result[name, row, col] = (None, None, None, False)
            xml = ET.fromstring(archive.read(paths[sheet.get(R + 'id')]))
            for c in xml.iter(S + 'c'):
                row, col = coordinate(c.get('r'))
                key = name, row, col
                if key not in result:
                    continue
                kind, raw = c.get('t', 'n'), c.findtext(S + 'v')
                formula = c.find(S + 'f')
                if kind == 'inlineStr':
                    kind, raw = 'text', ''.join(t.text or '' for t in c.iter(S + 't'))
                elif raw is not None:
                    if kind == 's':
                        kind, raw = 'text', strings[int(raw)]
                    elif kind == 'str':
                        kind = 'text'
                    elif kind == 'n' and raw:
                        raw = float(raw)
                    elif kind == 'e' and c.get('vm'):
                        raw = (raw, errors[c.get('vm')])
                else:
                    kind = None
                definition = None if formula is None else (formula.get('t', 'normal'), formula.get('ref'), formula.text)
                result[key] = (kind, raw, definition, c.get('cm') is not None)
    return result


def compare_array_caches(actual, expected, regions):
    a, b = (cached_regions(path, regions) for path in (actual, expected))
    assert a.keys() == b.keys()
    for key in b:
        va, vb = a[key], b[key]
        if va != vb:
            assert va[0] == vb[0] == 'n' and va[2:] == vb[2:] and math.isclose(
                va[1], vb[1], rel_tol=2e-15, abs_tol=1e-12), (key, va, vb)
    return len(b)
