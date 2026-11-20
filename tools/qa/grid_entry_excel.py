"""Compare production grid gestures with Excel on a private expense report and an independent validation workbook."""
import argparse
import hashlib
import json
import math
import subprocess
from pathlib import Path

from pywintypes import com_error
from win32com.client import DispatchEx


def digest(file):
    return hashlib.sha256(file.read_bytes()).hexdigest()


def run_browser(file, sheet, steps, directory):
    directory.mkdir(parents=True, exist_ok=True)
    case = directory / "case.json"
    case.write_text(json.dumps(dict(file=str(file), sheet=sheet, steps=steps), ensure_ascii=False), encoding="utf-8")
    result = subprocess.run(["node", str(Path(__file__).with_name("grid_entry_ui.cjs")), str(case), str(directory)],
                            capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=480)
    (directory / "browser.log").write_text(result.stdout + result.stderr, encoding="utf-8")
    result.check_returncode()
    return json.loads(result.stdout)


def snapshot(sheet, area):
    found = []
    for cell in sheet.Range(area).Cells:
        found.append((cell.Address, cell.Value2, cell.Formula2 if cell.HasFormula else None,
                      cell.NumberFormat, cell.MergeArea.Address))
    return found


def compare(excel, sheet, expected, directory):
    counts = {}
    for name, records in expected.items():
        result = excel.Workbooks.Open(str(directory / (name + ".xlsx")), UpdateLinks=0, ReadOnly=True, AddToMru=False)
        try:
            excel.Calculate()
            for address, value, formula, style, merge in records:
                cell = result.Worksheets(sheet).Range(address)
                actual = (cell.Value2, cell.Formula2 if cell.HasFormula else None, cell.NumberFormat, cell.MergeArea.Address)
                same = actual[0] == value and type(actual[0]) is type(value)
                if isinstance(value, float) and isinstance(actual[0], float):
                    same = math.isclose(value, actual[0], rel_tol=1e-14, abs_tol=1e-12)
                if not same or actual[1:] != (formula, style, merge):
                    raise AssertionError((name, address, (value, formula, style, merge), actual))
            counts[name] = len(records)
        finally:
            result.Close(False)
    return counts


def expense(excel, file, sheet_name, directory):
    original = digest(file)
    steps = [
        dict(range="I8:I9", paste="21\r\n37", rev=2, save="input"),
        dict(range="K8", paste="=$I8*2\t=$I8+3", rev=3),
        dict(range="K8:L10", key="d", mods=2, rev=4, save="down"),
        dict(range="K8:M10", key="r", mods=2, rev=5, save="right"),
        dict(range="K12:L13", text="=I8+1", key="Enter", mods=2, rev=6, save="entry"),
        dict(range="K9:L10", key="Delete", rev=7, save="clear"),
        dict(key="z", mods=2, rev=8, save="undo"),
        dict(key="y", mods=2, rev=9, save="redo"),
        dict(range="K8:L10", key="Tab", ref="L8"),
        dict(key="Tab", ref="K9"), dict(key="Tab", mods=8, ref="L8"),
        dict(key="Enter", ref="L9"), dict(key="Enter", mods=8, ref="L8"),
        dict(range="K8:M8",copy=True), dict(range="K14:M15",key="v",mods=2,rev=10,save="copy"),
    ]
    browser = run_browser(file, sheet_name, steps, directory)
    base = excel.Workbooks.Open(str(file), UpdateLinks=0, ReadOnly=True, AddToMru=False)
    try:
        sheet = base.Worksheets(sheet_name)
        all_cells = "A1:N19"
        def state():
            excel.Calculate()
            return snapshot(sheet, all_cells)
        expected = {}
        sheet.Range("I8").Value2 = 21
        sheet.Range("I9").Value2 = 37
        expected["input"] = state()
        sheet.Range("K8").Formula = "=$I8*2"
        sheet.Range("L8").Formula = "=$I8+3"
        sheet.Range("K8:L10").FillDown()
        expected["down"] = state()
        sheet.Range("K8:M10").FillRight()
        expected["right"] = state()
        sheet.Range("K12:L13").FormulaR1C1 = "=R[-4]C[-2]+1"
        expected["entry"] = state()
        sheet.Range("K9:L10").ClearContents()
        expected["clear"] = state()
        expected["undo"], expected["redo"] = expected["entry"], expected["clear"]
        sheet.Range("K8:M8").Copy(Destination=sheet.Range("K14:M15"))
        expected["copy"] = state()
    finally:
        base.Close(False)
    counts = compare(excel, sheet_name, expected, directory)
    assert digest(file) == original
    return dict(browser=browser, cells=counts, originalSHA256=original)


def cross_sheet(excel, file, source_sheet, target_sheet, directory):
    before = digest(file)
    steps = [
        dict(range="F5", copy=True),
        dict(sheet=target_sheet,range="AZ50:BA51",menu="paste",rev=2,save="copied"),
        dict(range="AX50",paste="10\t2",rev=3,save="changed"),
        dict(key="z",mods=2,rev=4,save="undo"), dict(key="y",mods=2,rev=5,save="redo"),
    ]
    browser = run_browser(file,source_sheet,steps,directory)
    base = excel.Workbooks.Open(str(file), UpdateLinks=0, ReadOnly=True, AddToMru=False)
    try:
        src, dst = base.Worksheets(source_sheet), base.Worksheets(target_sheet)
        assert src.Range("F5").HasFormula
        src.Range("F5").Copy(Destination=dst.Range("AZ50:BA51"))
        excel.Calculate()
        copied = snapshot(dst, "AW49:BB52")
        dst.Range("AX50").Value2, dst.Range("AY50").Value2 = 10, 2
        excel.Calculate()
        changed = snapshot(dst, "AW49:BB52")
        assert copied != changed
    finally:
        base.Close(False)
    counts = compare(excel,target_sheet,dict(copied=copied,changed=changed,undo=copied,redo=changed),directory)
    assert digest(file) == before
    return dict(browser=browser,cells=counts,originalSHA256=before)


def protected(excel, directory):
    directory.mkdir(parents=True, exist_ok=True)
    file = directory / "protected.xlsx"
    book = excel.Workbooks.Add()
    try:
        s = book.Worksheets(1)
        s.Name = "录入"
        s.Range("B2:B4").Locked = False
        s.Range("B2").Value2 = 75
        s.Range("B3").Value2 = 30
        s.Range("C2").Value2 = "保留"
        s.Protect()
        book.SaveAs(str(file), 51)
    finally:
        book.Close(False)
    original = digest(file)
    steps = [
        dict(range="B4",key="Delete",rev=1),
        dict(range="B2:C2",paste="12\t改写",error="只读",rev=1),
        dict(range="B2:C2",key="Delete",error="只读",rev=1),
        dict(range="B2:B3",menu="down",pointer="B3",rev=2,save="fill"),
        dict(key="z",mods=2,rev=3,save="undo"),dict(key="y",mods=2,rev=4,save="redo"),
    ]
    browser = run_browser(file,"录入",steps,directory)
    book = excel.Workbooks.Open(str(file),UpdateLinks=0,ReadOnly=True,AddToMru=False)
    try:
        s = book.Worksheets(1)
        initial = snapshot(s,"A1:D5")
        try:
            s.Range("B2:C2").ClearContents()
        except com_error:
            assert initial == snapshot(s,"A1:D5")
        else:
            raise AssertionError("Excel cleared the locked cell")
        s.Range("B2:B3").FillDown()
        filled = snapshot(s,"A1:D5")
    finally:
        book.Close(False)
    counts = compare(excel,"录入",dict(fill=filled,undo=initial,redo=filled),directory)
    assert digest(file) == original
    return dict(browser=browser,cells=counts,originalSHA256=original)


def lists(excel, directory):
    directory.mkdir(parents=True, exist_ok=True)
    file = directory / "validation.xlsx"
    base = excel.Workbooks.Add()
    try:
        sheet = base.Worksheets(1)
        sheet.Name = "录入"
        for ref, value in (("D2", "00123"), ("D3", "00077"), ("F2", "0089")):
            sheet.Range(ref).NumberFormat = "@"
            sheet.Range(ref).Value2 = value
        sheet.Range("B2").Validation.Add(3, 1, 1, "=$D$2:$D$3")
        sheet.Range("B3").Validation.Add(3, 1, 1, "东区,西区")
        sheet.Range("C2").Validation.Add(3, 1, 1, "甲,乙")
        sheet.Range("C2").Validation.InCellDropdown = False
        sheet.Range("A5:B5").Merge()
        sheet.Range("A5").Value2 = "合并标题"
        sheet.Range("C5").Value2 = "相邻数据"
        base.SaveAs(str(file), 51)
    finally:
        base.Close(False)
    before = digest(file)
    steps = [
        dict(range="B2", list=["00123","00077"], pick="00077", rev=2, save="choose"),
        dict(range="D3", paste="00444", rev=3),
        dict(range="B2", list=["00123","00444"], pick="00444", rev=4, save="updated-list"),
        dict(width=360,range="B3", list=["东区","西区"], click=True, pick="西区", rev=5,shot="narrow-list"),
        dict(width=1280,range="C2", noList=True), dict(range="A2", noList=True),
        dict(range="E2:H3", paste="=1+1\t349\tTRUE\t17.5", rev=6, save="typed"),
        dict(range="A5:C5", menu="clear",pointer="C5", rev=7, save="merged-clear",shot="range-menu"),
        dict(key="z", mods=2, rev=8, save="merged-undo"),
        dict(key="y", mods=2, rev=9, save="merged-redo"),
        dict(range="E2:H3", paste="1\t2\t3", error="大小不一致", rev=9),
        dict(range="E2:H2",copy=True), dict(range="E7",key="v",mods=2,rev=10,save="copy"),
        dict(range="E9",paste="2",rev=11,save="external"),
    ]
    browser = run_browser(file, "录入", steps, directory)
    base = excel.Workbooks.Open(str(file), UpdateLinks=0, ReadOnly=True, AddToMru=False)
    try:
        sheet = base.Worksheets("录入")
        def state():
            excel.Calculate()
            return snapshot(sheet, "A1:I9")
        expected = {}
        sheet.Range("B2").Value2 = "'00077"
        expected["choose"] = state()
        sheet.Range("D3").Value2 = "00444"
        sheet.Range("B2").Value2 = "'00444"
        expected["updated-list"] = state()
        sheet.Range("B3").Value2 = "西区"
        for row in (2,3):
            sheet.Cells(row,5).Formula = "=1+1"
            sheet.Cells(row,6).Value2 = "349" if row == 2 else 349
            sheet.Cells(row,7).Value2 = True
            sheet.Cells(row,8).Value2 = 17.5
        expected["typed"] = state()
        sheet.Range("A5:C5").ClearContents()
        expected["merged-clear"] = state()
        expected["merged-undo"], expected["merged-redo"] = expected["typed"], expected["merged-clear"]
        sheet.Range("E2:H2").Copy(Destination=sheet.Range("E7:H7"))
        expected["copy"] = state()
        sheet.Range("E9").Value2 = 2
        expected["external"] = state()
    finally:
        base.Close(False)
    counts = compare(excel, "录入", expected, directory)
    assert digest(file) == before
    return dict(browser=browser, cells=counts, originalSHA256=before)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path, help="private accrual report with editable I8:I9 and blank K8:M13")
    parser.add_argument("sheet")
    parser.add_argument("directory", type=Path)
    parser.add_argument("--cross-report",type=Path)
    parser.add_argument("--copy-from-sheet")
    parser.add_argument("--paste-to-sheet")
    parser.add_argument("--only", choices=["expense","lists","cross","protected"])
    args = parser.parse_args()
    out = args.directory.resolve()
    out.mkdir(parents=True, exist_ok=True)
    excel = DispatchEx("Excel.Application")
    try:
        excel.Visible = False
        excel.DisplayAlerts = False
        excel.AutomationSecurity = 3
        excel.AskToUpdateLinks = False
        receipt = dict(excel=[excel.Version, excel.Build])
        cases = [("expense",lambda: expense(excel,args.report.resolve(),args.sheet,out/"expense")),
                 ("lists",lambda: lists(excel,out/"lists")),
                 ("protected",lambda: protected(excel,out/"protected"))]
        if args.cross_report:
            if not args.copy_from_sheet or not args.paste_to_sheet:
                parser.error("the cross-sheet case needs the source and target sheet names")
            cases.append(("cross",lambda: cross_sheet(excel,args.cross_report.resolve(),args.copy_from_sheet,args.paste_to_sheet,out/"cross")))
        elif args.only=="cross":
            parser.error("the cross-sheet case needs --cross-report")
        for name, run in cases:
            if args.only and args.only != name:
                continue
            receipt[name] = run()
            (out/"native.json").write_text(json.dumps(receipt,ensure_ascii=False,indent=2),encoding="utf-8")
    finally:
        excel.Quit()
