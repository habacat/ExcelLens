"""Regenerate the synthetic conditional-format oracle using an installed Excel.
Usage: python conditional_native_matrix.py <private-output-directory>
Only the newly created fixture is opened. The production executable is independent of Office.
"""
import json
import sys
from pathlib import Path

import openpyxl
from openpyxl.formatting.rule import FormulaRule
from openpyxl.styles import PatternFill
from openpyxl.workbook.defined_name import DefinedName
from openpyxl.utils.datetime import CALENDAR_MAC_1904
import win32com.client
from generate_xlsb_pairs import scrub_fixture


GROUPS = {
    "scoped-names": ['AND(Period="month",Cutoff=42)', 'AND(Period="half",Cutoff=42)',
                     'LocalCut=9', 'LocalCut=42', 'ABS(Cutoff-LocalCut)=33'],
    "range-and-types": ['SUM($A$2:$A$6)=9', 'COUNT($A$2:$A$6)=2', 'COUNTA($A$2:$A$8)=6',
                        'COUNTBLANK($A$2:$A$8)=2', 'AVERAGE($A$2:$A$6)=4.5',
                        'SUM($A$4,$A$5)=0', 'SUM("4",TRUE)=5', 'ISTEXT($A$4)', 'ISNUMBER($A$4)',
                        'ISBLANK($A$6)', 'ISBLANK($A$7)', 'ISLOGICAL($A$5)', 'ISNA($A$8)'],
    "criteria": ['COUNTIF($B$2:$B$8,"k")=3', 'COUNTIF($A$2:$A$8,"4")=1',
                 'COUNTIF($A$2:$A$8,"")=2', 'COUNTIF($B$2:$B$8,"k*")=3',
                 'COUNTIFS($B$2:$B$8,"k",$C$2:$C$8,">0")=2',
                 'SUMIF($B$2:$B$8,"k",$C$2:$C$8)=3',
                 'SUMIFS($C$2:$C$8,$B$2:$B$8,"k",$C$2:$C$8,">0")=5.2',
                 'MATCH("k",$B$2:$B$8,0)=1', 'MATCH("x",$B$2:$B$8,0)=3',
                 'ISNA(MATCH("absent",$B$2:$B$8,0))'],
    "errors-and-logic": ['IFERROR($A$8,7)=7','ISERROR(1/0)','ISERR($A$8)','ISNA($A$8)',
                         'AND($A$6,TRUE)','OR(FALSE,$A$5)','IF(FALSE,1/0,5)=5',
                         '_xlfn.IFNA($A$8,12)=12','ISERROR(PRODUCT(1E300,1E300))'],
    "date-and-number": ['ROUND(1.25,1)=1.3','ROUND(-1.25,1)=-1.3','ROUNDDOWN(-1.29,1)=-1.2',
                        'ROUNDUP(-1.21,1)=-1.3','MOD(-3,2)=1','0.1+0.2=0.3',
                        '1/3*3=1','1+1E-16=1','1+1E-14>1','1+4E-15=1','1+5E-15=1','0.1+0.2-0.3=0','SUM(1E16,1,-1E16)=0',
                        'DATE(1900,3,0)=60','WEEKDAY(1)=1','YEAR(0)=1904','MONTH(0)=1',
                        'DATE(9999,12,31)>2900000','DAY(60)=29','WEEKDAY(60,2)=3'],
    "text-and-wildcards": ['LEFT("编号001",2)="编号"','EXACT("A","a")','SEARCH("a~*","xa*b")=2',
                           'ISERROR(SEARCH("","abc",4))','FIND("","abc",4)=4','LEN(TRIM("  a b  "))=3'],
}


def fixture(output, mode):
    wb = openpyxl.Workbook()
    sheet = wb.active
    sheet.title = "Checks"
    params = wb.create_sheet("Ref O'Brien")
    params["A1"], params["B1"] = 42, "month"
    sheet["D1"] = 9
    if mode:
        wb.epoch = CALENDAR_MAC_1904
    for name, ref, scope in [('Period', "'Ref O''Brien'!$B$1", None),
                             ('Cutoff', "'Ref O''Brien'!$A$1", None),
                             ('LocalCut', "'Ref O''Brien'!$A$1", None),
                             ('LocalCut', "'Checks'!$D$1", 0)]:
        definition = DefinedName(name, attr_text=ref, localSheetId=scope)
        if hasattr(wb.defined_names, 'append'):
            wb.defined_names.append(definition)
        else:
            (wb.defined_names if scope is None else sheet.defined_names).add(definition)
    for row, value in {2: 3, 3: 6, 4: "4", 5: True, 7: '=""', 8: '=NA()'}.items():
        sheet.cell(row, 1, value)
    for row, label, number in [(2, "k", 1.2), (3, "K", -2.2), (4, "x", 8), (5, "k", 4)]:
        sheet.cell(row, 2, label)
        sheet.cell(row, 3, number)
    fill = PatternFill('solid', fgColor='FF00FF00', bgColor='FF00FF00')
    cases = []
    for group, formulas in GROUPS.items():
        for f in formulas:
            ref = "F" + str(len(cases) + 2)
            sheet[ref] = 1
            sheet.conditional_formatting.add(ref, FormulaRule(formula=[f], fill=fill))
            cases.append(dict(ref=ref, group=group, formula=f))
    for r in range(2, 7):
        sheet.cell(r, 7, 1)
        cases.append(dict(ref="G"+str(r), group="relative", formula="AND(ISNUMBER($A2),$A2>3)"))
    sheet.conditional_formatting.add("G2:G6", FormulaRule(formula=["AND(ISNUMBER($A2),$A2>3)"], fill=fill))
    wb.save(output)
    return cases


def main():
    dest = Path(sys.argv[1]).resolve()
    dest.mkdir(parents=True, exist_ok=True)
    app = win32com.client.DispatchEx("Excel.Application")
    lookup = []
    try:
        app.Visible = False
        app.DisplayAlerts = False
        app.AskToUpdateLinks = False
        app.AutomationSecurity = 3
        for epoch in ["1900", "1904"]:
            seed = dest / ("seed-" + epoch + ".xlsx")
            cases = fixture(seed, epoch == "1904")
            book = app.Workbooks.Open(str(seed), UpdateLinks=0, ReadOnly=True, AddToMru=False)
            try:
                app.Calculate()
                sheet = book.Worksheets("Checks")
                for case in cases:
                    case["highlight"] = int(sheet.Range(case["ref"]).DisplayFormat.Interior.Color) == 65280
                for fmt, ext in [(51, "xlsx"), (50, "xlsb")]:
                    book.SaveAs(str(dest / ("raw-" + epoch + "." + ext)), FileFormat=fmt)
                lookup.append(dict(epoch=epoch, cases=cases))
            finally:
                book.Close(False)
            for ext in ["xlsx","xlsb"]:
                scrub_fixture(dest / ("raw-" + epoch + "." + ext), dest / (epoch + "." + ext))
    finally:
        app.Quit()
    (dest / "expected.json").write_text(json.dumps(lookup, ensure_ascii=False, indent=2), encoding="utf-8")
    print("Excel recorded",sum(len(v["cases"]) for v in lookup),"display-format observations")


if __name__ == "__main__":
    main()
