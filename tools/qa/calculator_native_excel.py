"""Record independent calculation-panel reference values with installed Microsoft Excel."""
import hashlib
import json
from pathlib import Path
from xml.etree import ElementTree as ET

from win32com.client import DispatchEx

DATA = Path(__file__).resolve().parents[2] / "internal/server/testdata/calculator-native"
NS = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"


def populate(sheet):
    for cell in ET.parse(DATA / "sheet.xml").iter(NS + "c"):
        target = sheet.Range(cell.attrib["r"])
        formula = cell.findtext(NS + "f")
        kind = cell.get("t")
        if formula is not None:
            target.Formula2 = "=" + formula
        elif kind == "inlineStr":
            target.NumberFormat = "@"
            target.Value2 = cell.findtext(NS + "is/" + NS + "t")
        else:
            raw = cell.findtext(NS + "v")
            target.Value2 = (raw == "1") if kind == "b" else float(raw)


def record(sheet, name, formula):
    cell = sheet.Range("Z1")
    cell.Clear()
    cell.Formula2 = formula
    if not cell.HasFormula:
        raise ValueError("Excel stored literal text instead of the requested formula")
    cell.Calculate()
    if sheet.Evaluate("ISERROR(Z1)"):
        return ["error", str(cell.Text)]
    value = cell.Value2
    if isinstance(value, bool):
        return ["boolean", str(value).upper()]
    if isinstance(value, (int, float)):
        return ["number", format(value, ".17g")]
    if isinstance(value, str):
        return ["text", value]
    raise ValueError((name, formula, value))


def main():
    cases = json.loads((DATA / "cases.json").read_text("utf-8"))
    excel = DispatchEx("Excel.Application")
    try:
        excel.DisplayAlerts = False
        excel.EnableEvents = False
        excel.AutomationSecurity = 3
        result = {"application": excel.Name, "version": excel.Version, "build": excel.Build,
                  "inputs": {n: hashlib.sha256((DATA / n).read_text("utf-8").encode()).hexdigest()
                             for n in ("cases.json", "sheet.xml")}, "systems": {}}
        for system in ("1900", "1904"):
            book = excel.Workbooks.Add(-4167)
            try:
                book.Date1904 = system == "1904"
                sheet = book.Worksheets(1)
                populate(sheet)
                groups = ("functions", "boundaries", "dates") if system == "1900" else ("dates",)
                rows = {}
                for group in groups:
                    for name, formula in cases[group].items():
                        try:
                            rows[name] = record(sheet, name, formula)
                        except Exception as error:
                            raise RuntimeError((system, name, formula)) from error
                result["systems"][system] = rows
            finally:
                book.Close(False)
    finally:
        excel.Quit()
    path = DATA / "excel.json"
    path.with_suffix(".tmp").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", "utf-8")
    path.with_suffix(".tmp").replace(path)
    print("Microsoft Excel", result["version"], result["build"], {k: len(v) for k, v in result["systems"].items()})


if __name__ == "__main__":
    main()
