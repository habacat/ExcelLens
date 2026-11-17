"""Independent xlrd digest of every nonempty source coordinate and value."""
import hashlib
import json
import struct
import sys
from pathlib import Path
import xlrd
from xlrd.biffh import error_text_from_code

file = Path(sys.argv[1])
book = xlrd.open_workbook(str(file), formatting_info=True, on_demand=True)
sheet = book.sheet_by_index(0)
digest = hashlib.sha256()
count = 0
for row in range(sheet.nrows):
    for col in range(sheet.ncols):
        cell = sheet.cell(row, col)
        if cell.ctype in (xlrd.XL_CELL_EMPTY, xlrd.XL_CELL_BLANK) or cell.ctype == xlrd.XL_CELL_TEXT and cell.value == "":
            continue
        if cell.ctype in (xlrd.XL_CELL_NUMBER, xlrd.XL_CELL_DATE):
            value = "n" + struct.pack(">d", cell.value).hex()
        elif cell.ctype == xlrd.XL_CELL_BOOLEAN:
            value = "b" + str(int(cell.value))
        elif cell.ctype == xlrd.XL_CELL_ERROR:
            value = "e" + error_text_from_code[cell.value]
        else:
            value = "s" + cell.value
        data = value.encode("utf-8")
        digest.update(struct.pack(">III", row + 1, col + 1, len(data)))
        digest.update(data)
        count += 1
report = {"reader": "independent xlrd", "rows": sheet.nrows, "columns": sheet.ncols, "merges": len(sheet.merged_cells), "nonemptyCells": count, "sourceSHA256": hashlib.sha256(file.read_bytes()).hexdigest(), "cellsSHA256": digest.hexdigest()}
Path(sys.argv[2]).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
print(json.dumps(report))
