"""Save native Excel's A1 reference rewrites for the optional Go range-transfer matrix test."""
import argparse
import itertools
import json
import tempfile
from pathlib import Path
from xml.etree import ElementTree as ET
from zipfile import ZipFile

from openpyxl import Workbook
from win32com.client import DispatchEx

MAIN = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"


def address(col, row):
    return chr(ord("A") + col - 1) + str(row)


def compare(excel, source, target, book_path, out, original):
    book = excel.Workbooks.Open(str(book_path), UpdateLinks=0, ReadOnly=True, AddToMru=False)
    try:
        sheet, dest = target.split("!")
        book.Worksheets("Origin").Range(source).Cut(Destination=book.Worksheets(sheet).Range(dest))
        book.SaveCopyAs(str(out))
    finally:
        book.Close(False)
    with ZipFile(out) as archive:
        data = ET.fromstring(archive.read("xl/worksheets/sheet3.xml"))
    formulas = {c.get("r"): c.findtext(MAIN + "f") for c in data.iter(MAIN + "c")}
    assert formulas.keys() == original.keys() and all(formulas.values())
    return {old: formulas[key] for key, old in original.items() if old != formulas[key]}


def main(output):
    spans = list(itertools.combinations_with_replacement(range(1, 10), 2))
    original = {}
    book = Workbook()
    book.active.title = "Origin"
    book.create_sheet("Target")
    audit = book.create_sheet("Audit")
    for i, ((left, right), (top, bottom)) in enumerate(itertools.product(spans, repeat=2), 1):
        for col, scope in enumerate(("Origin", "Target"), 1):
            operand = scope + "!" + address(left, top) + ":" + address(right, bottom)
            cell = audit.cell(i, col, "=SUM(" + operand + ")")
            original[cell.coordinate] = cell.value[1:]
    excel = DispatchEx("Excel.Application")
    excel.Visible, excel.DisplayAlerts, excel.EnableEvents, excel.AutomationSecurity = False, False, False, 3
    samples = []
    try:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "source.xlsx"
            converted = Path(directory) / "moved.xlsx"
            book.save(path)
            for region in ("C3:E5", "C3:F4", "C3:E3", "C3:C5", "C3"):
                for target in ("Target!G7", "Origin!G7", "Origin!D4", "Origin!D3", "Origin!C4",
                               "Origin!C1", "Origin!A1", "Origin!B4", "Origin!D2", "Origin!H3",
                               "Origin!C8", "Origin!F6", "Origin!D7", "Origin!B1", "Origin!A4", "Origin!F2"):
                    changes = compare(excel, region, target, path, converted, original)
                    samples.append(dict(source=region, destination=target, changes=changes))
    finally:
        excel.Quit()
    output.write_text(json.dumps(dict(formulas=list(original.values()), samples=samples)), encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    main(parser.parse_args().output)
