"""Synthetic two-row ledger headers for the N22/N23 paint regression."""
import html
import sys
import zipfile
from pathlib import Path

out = Path(sys.argv[1]).resolve()
out.mkdir(parents=True, exist_ok=True)
merges = [(15, 2, 15, 12), (21, 2, 22, 2), (21, 3, 22, 3),
          (21, 4, 22, 4), (21, 5, 22, 5), (21, 10, 22, 10),
          (21, 6, 21, 7), (21, 8, 21, 9), (21, 11, 21, 12)]
values = {(15, 2): "三栏式明细账", (16, 2): "期间从：", (16, 3): "2025-01",
          (17, 2): "期间至：", (17, 3): "2025-11", (18, 2): "合成测试账簿",
          (21, 2): "日期", (21, 3): "凭证编号", (21, 4): "摘要", (21, 5): "本币种",
          (21, 6): "借方", (21, 8): "贷方", (21, 10): "方向", (21, 11): "余额",
          (300, 14): "viewport extent"}
for col in (6, 8, 11):
    values[(22, col)] = "原币"
    values[(22, col + 1)] = "本位币"
for row in range(24, 70):
    for col, value in {2: "2025/1/3", 3: "000000123456789", 4: "合成明细", 5: "CNY", 10: "贷", 12: "12.82"}.items():
        values[(row, col)] = value


def letter(col):
    return chr(64 + col)


def covered(row, col):
    return any(r1 <= row <= r2 and c1 <= col <= c2 and (row, col) != (r1, c1)
               for r1, c1, r2, c2 in merges)


rows = []
html_rows = []
for row in range(1, 301):
    cells = []
    html_cells = []
    for col in range(1, 15):
        value = values.get((row, col), "")
        style = 1 if row in (21, 22) and 2 <= col <= 12 else 0
        cells.append('<c r="%s%d" s="%d" t="inlineStr"><is><t>%s</t></is></c>' %
                     (letter(col), row, style, html.escape(value)))
        if covered(row, col):
            continue
        attrs = ' class="header"' if style else ''
        for r1, c1, r2, c2 in merges:
            if (row, col) == (r1, c1):
                attrs += ' rowspan="%d" colspan="%d"' % (r2 - r1 + 1, c2 - c1 + 1)
        html_cells.append('<td%s>%s</td>' % (attrs, html.escape(value) or '&#160;'))
    rows.append('<row r="%d">%s</row>' % (row, ''.join(cells)))
    html_rows.append('<tr>%s</tr>' % ''.join(html_cells))

columns = ''.join('<col min="%d" max="%d" width="%d" customWidth="1"/>' % (col, col, {1: 4, 3: 27, 4: 38, 5: 8, 10: 6}.get(col, 11)) for col in range(1, 15))
merge_xml = ''.join('<mergeCell ref="%s%d:%s%d"/>' % (letter(c1), r1, letter(c2), r2) for r1, c1, r2, c2 in merges)
ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
parts = {
    'xl/workbook.xml': '<workbook xmlns="%s" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="合并表头" sheetId="1" r:id="rId1"/></sheets></workbook>' % ns,
    'xl/_rels/workbook.xml.rels': '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
    'xl/styles.xml': '<styleSheet xmlns="%s"><fonts count="2"><font><sz val="9"/><name val="SimSun"/></font><font><b/><sz val="9"/><name val="SimSun"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFCCFFFF"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="2"><border/><border><left style="thin"><color rgb="FF000000"/></left><right style="thin"><color rgb="FF000000"/></right><top style="thin"><color rgb="FF000000"/></top><bottom style="thin"><color rgb="FF000000"/></bottom></border></borders><cellStyleXfs count="1"><xf fontId="0" numFmtId="0"/></cellStyleXfs><cellXfs count="2"><xf fontId="0" fillId="0" borderId="0" numFmtId="0"/><xf fontId="1" fillId="2" borderId="1" numFmtId="0"><alignment horizontal="center" vertical="center"/></xf></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>' % ns,
    'xl/worksheets/sheet1.xml': '<worksheet xmlns="%s"><sheetFormatPr defaultRowHeight="15"/><cols>%s</cols><sheetData>%s</sheetData><mergeCells count="%d">%s</mergeCells></worksheet>' % (ns, columns, ''.join(rows), len(merges), merge_xml)
}
with zipfile.ZipFile(out / 'merged-selection.xlsx', 'w', zipfile.ZIP_DEFLATED) as archive:
    for name, content in parts.items():
        archive.writestr(name, content.encode('utf-8'))
document = '<html><head><meta charset="utf-8"><style>td{height:20px;font-family:SimSun;font-size:9pt;white-space:nowrap}.header{background:#ccffff;border:1px solid #000;font-weight:bold;text-align:center;vertical-align:middle}</style></head><body><table>' + ''.join(html_rows) + '</table></body></html>'
with (out / 'merged-selection.xls').open('w', encoding='utf-8', newline='\n') as file:
    file.write(document)
print('Created synthetic XLSX and HTML XLS headers at rows 21:22.')
