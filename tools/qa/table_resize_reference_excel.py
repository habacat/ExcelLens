"""Generate Excel's saved A1 reference matrix for TestNativeTableResizeReferenceMatrix."""
import argparse
import itertools
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from xml.etree import ElementTree as ET
from zipfile import ZipFile

from openpyxl import Workbook
from openpyxl.worksheet.table import Table
from win32com.client import DispatchEx


def saved(path):
    with ZipFile(path) as archive:
        sheet = ET.fromstring(archive.read("xl/worksheets/sheet2.xml"))
    return {c.get("r"): c.findtext("{*}f") for c in sheet.findall(".//{*}c")}


def fixture(path, totals):
    book = Workbook()
    sheet = book.active
    sheet.title = "Data"
    audit = book.create_sheet("Look")
    for col, name in enumerate(("Item", "Qty", "Cost"), 2):
        sheet.cell(2, col, name)
        for row in range(3, 6):
            sheet.cell(row, col, row)
    template = Table(displayName="Fees", ref="B2:D6" if totals else "B2:D5",
                     totalsRowCount=int(totals), totalsRowShown=totals)
    sheet.add_table(template)
    if totals:
        sheet["B6"] = "Total"
    cols = itertools.combinations_with_replacement("ABCDE", 2)
    rows = itertools.combinations_with_replacement(range(1, 12), 2)
    for i, ((a, b), (top, end)) in enumerate(itertools.product(cols, rows), 1):
        audit.cell(i, 1, f"=SUM(Data!{a}{top}:{b}{end})")
    book.save(path)


def main(output):
    result = {}
    excel = DispatchEx("Excel.Application")
    excel.Visible, excel.DisplayAlerts, excel.EnableEvents, excel.AutomationSecurity = False, False, False, 3
    try:
        with TemporaryDirectory() as temp:
            directory = Path(temp)
            source, after = directory / "source.xlsx", directory / "resized.xlsx"
            for totals in (False, True):
                fixture(source, totals)
                original = saved(source)
                for ref in ("B2:D9", "B2:D4", "C2:E8", "C2:C4", "B2:D6", "B2:D7"):
                    book = excel.Workbooks.Open(str(source), UpdateLinks=0, ReadOnly=True, AddToMru=False)
                    try:
                        sheet = book.Worksheets("Data")
                        table = sheet.ListObjects("Fees")
                        assert bool(table.ShowTotals) == totals
                        table.Resize(sheet.Range(ref))
                        book.SaveCopyAs(str(after))
                    finally:
                        book.Close(False)
                    current = saved(after)
                    assert original.keys() == current.keys() and all(current.values())
                    result[str(totals) + ref] = {expr: current[key] for key, expr in original.items()}
    finally:
        excel.Quit()
    output.write_text(json.dumps(result), encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    main(parser.parse_args().output)
