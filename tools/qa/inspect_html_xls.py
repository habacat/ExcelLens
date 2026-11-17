"""Independent streaming HTML worksheet coordinate/value oracle (no ExcelLens).

Only digests and structural counts are emitted, never individual cell values.
"""
import argparse
import hashlib
import json
import re
from html.parser import HTMLParser
from pathlib import Path


class Worksheet(HTMLParser):
    def __init__(self, columns):
        super().__init__(convert_charrefs=True)
        self.columns = columns
        self.tables = self.row_count = self.merge_count = self.formulas = 0
        self.inside = self.cell = self.para = self.helper = False
        self.rows = []
        self.fields = []
        self.occupied = {}
        self.col = 1
        self.span = self.down = 1
        self.text = []
        self.digest = hashlib.sha256()
        self.anchors = {}
        self.merges = []

    def emit(self, fields):
        self.row_count += 1
        assert len(fields) <= self.columns
        fields = fields + [''] * (self.columns - len(fields))
        self.digest.update(('{}|'.format(self.row_count)).encode())
        for field in fields:
            match = re.fullmatch(r'="((?:[^"]|"")*)"', field)
            if match:
                self.formulas += 1
                field = match[1].replace('""', '"')
            data = field.encode('utf-8')
            self.digest.update(str(len(data)).encode() + b':' + data)
        self.digest.update(b'\n')

    def append_text(self, data):
        # Paragraph boundaries / BR are explicit newlines; source indentation
        # uses ordinary HTML collapsing. A sentinel keeps these distinguishable.
        self.text.append(re.sub(r'[ \t\r\n\f]+', ' ', data))

    def flush_cell(self):
        if not self.cell:
            return
        self.cell = False
        current = self.row_count + 1
        while self.occupied.get(self.col, 0) >= current:
            self.col += 1
        end = self.col + self.span - 1
        while len(self.fields) < end:
            self.fields.append('')
        raw = ''.join(self.text).replace(' \0', '\0').replace('\0 ', '\0')
        raw = raw.replace('\0', '\n').strip(' \t\r\n\f')
        # Splitting/rejoining normal whitespace across adjacent callbacks is
        # needed because character references and chunk boundaries can split it.
        raw = re.sub(' +', ' ', raw)
        self.fields[self.col - 1] = raw
        for col in range(self.col, end + 1):
            assert self.occupied.get(col, 0) < current
            self.occupied[col] = current + self.down - 1
        if self.span > 1 or self.down > 1:
            self.merge_count += 1
            self.merges.append([current, self.col, current + self.down - 1, end])
        self.col = end + 1
        self.text = []

    def flush_row(self):
        if self.rows:
            self.flush_cell()
            self.emit(self.fields)
            self.rows = []

    def handle_decl(self, decl):
        pass

    def unknown_decl(self, decl):
        if 'supportMisalignedColumns' in decl:
            self.helper = True
        elif 'endif' in decl:
            self.helper = False

    def handle_starttag(self, tag, attrs):
        if self.helper:
            return
        attrs = dict(attrs)
        if tag == 'table':
            assert not self.inside
            self.tables += 1
            self.inside = True
            self.occupied = {}
        elif self.inside and tag == 'tr':
            self.flush_row()
            self.rows = [True]
            self.fields = [''] * max([c for c, r in self.occupied.items() if r >= self.row_count + 1] or [0])
            self.col = 1
        elif self.inside and tag in ('td', 'th'):
            self.flush_cell()
            self.cell = True
            self.span, self.down = int(attrs.get('colspan', 1)), int(attrs.get('rowspan', 1))
        elif self.cell and tag == 'br':
            self.text.append('\0')
        elif not self.inside and tag == 'p':
            self.para = True
            self.text = []

    def handle_endtag(self, tag):
        if self.helper:
            return
        if tag in ('td', 'th'):
            self.flush_cell()
        elif tag == 'tr':
            self.flush_row()
        elif tag == 'table':
            self.flush_row()
            self.inside = False
        elif tag == 'p' and self.cell:
            if self.text and not self.text[-1].endswith('\0'):
                self.text.append('\0')
        elif tag == 'p' and self.para:
            self.para = False
            self.emit([''.join(self.text).strip()])
            self.text = []

    def handle_data(self, data):
        if self.cell or self.para:
            self.append_text(data)


def inspect(path, columns):
    parser = Worksheet(columns)
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    with path.open('r', encoding='utf-8-sig', newline='') as stream:
        for chunk in iter(lambda: stream.read(256 * 1024), ''):
            parser.feed(chunk)
    parser.close()
    return dict(source_bytes=path.stat().st_size, source_sha256=digest.hexdigest(),
                table_sections=parser.tables, worksheets=1, rows=parser.row_count,
                columns=columns, merges=parser.merge_count, literal_formulas=parser.formulas,
                canonical_sha256=parser.digest.hexdigest(),
                merge_sha256=hashlib.sha256(json.dumps(parser.merges, separators=(',', ':')).encode()).hexdigest())


if __name__ == '__main__':
    args = argparse.ArgumentParser()
    args.add_argument('file', type=Path)
    args.add_argument('--columns', type=int, default=14)
    args.add_argument('--output', type=Path)
    opts = args.parse_args()
    result = json.dumps(inspect(opts.file, opts.columns), ensure_ascii=False, indent=2) + '\n'
    if opts.output:
        opts.output.parent.mkdir(parents=True, exist_ok=True)
        opts.output.write_text(result, encoding='utf-8')
    print(result, end='')
