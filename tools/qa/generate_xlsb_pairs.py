"""Make public synthetic BIFF12 and OOXML QA pairs from native Excel output."""
import argparse
import base64
import hashlib
from pathlib import Path
import re
from struct import pack
from tempfile import TemporaryDirectory
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo



def fidelity(book, directory, excel):
    s = book.Worksheets(1)
    s.Name = "账簿 明细"
    rates = book.Worksheets.Add(None, s)
    rates.Name = "费率 表"
    hidden = book.Worksheets.Add(None, rates)
    hidden.Name = "核对"
    hidden.Visible = 2
    rates.Range("A1:B2").Value2 = (("Key", "Rate"), ("当前", 0.125))
    s.Columns("A").NumberFormat = "@"
    s.Range("A1:C5").Value2 = (
        ("ID", "Amount", "Date"),
        ("000012", 123456789.123456, 45985.75),
        ("000020", -12.5, 59.5),
        ("000021", 0.0011, 61),
        ("000025", 120, 45500),
    )
    rates.Range("D1:E5").Value2 = (("ID", "Amount"), ("A", 123), ("B", -12.5), ("C", 1.25), ("D", 120))
    table = rates.ListObjects.Add(1, rates.Range("D1:E5"), None, 1)
    table.Name = "Ledger"
    s.Range("B2:B5").NumberFormat = "#,##0.0000"
    s.Range("C2:C5").NumberFormat = "yyyy-mm-dd hh:mm:ss"
    s.Range("D2:D5").FormulaR1C1 = "=RC[-2]*'费率 表'!R2C2"
    s.Range("E2").Formula = "=SUM(Ledger[Amount])"
    s.Range("E3").Formula = "=IF(B3<0,TRUE,FALSE)"
    s.Range("E4").Formula = '=A4&"-后"'
    s.Range("E5").Formula = "=1/0"
    s.Range("E7").Formula = "=SUM($B$2:B5)"
    s.Range("E8").Formula = "=LEN(A2)"
    s.Range("E9").Formula2 = "=SUM({1,2;3,4})"
    s.Range("D21:D1080").FormulaR1C1 = "=ROW()*0.125"
    s.Range("F2:F5").FormulaArray = "=B2:B5*2"
    s.Range("G2").Formula = "=INDEX(Ledger[Amount],2)"
    s.Range("G4").Value2 = "Alpha中文Beta"
    chars = s.Range("G4").GetCharacters(1, 5)
    chars.Font.Bold = True
    chars.Font.Color = 0x3232DD
    s.Range("K2").Value2 = True
    s.Range("L2").Value2 = 0.125
    s.Range("L2").NumberFormat = "0.00%"
    s.Range("G6:I7").Merge()
    s.Range("G6").Value2 = "合并标题"
    s.Range("G6").Font.Name = "Arial"
    s.Range("G6").Font.Size = 14
    s.Range("G6").Font.Bold = True
    s.Range("G6").Font.Italic = True
    s.Range("G6").Font.ThemeColor = 5
    s.Range("G6").Font.TintAndShade = 0.25
    s.Range("G6:I7").Interior.Color = 0xCCDDEE
    s.Range("G6:I7").Borders(7).LineStyle = 1
    s.Range("G6:I7").Borders(7).Color = 0xAA3333
    s.Range("G6").HorizontalAlignment = -4108
    s.Range("G6").VerticalAlignment = -4108
    s.Range("G6").WrapText = True
    s.Rows(6).RowHeight = 28
    s.Rows(3).Hidden = True
    s.Columns("A").ColumnWidth = 17
    s.Columns("K").Hidden = True
    s.Range("B4").AddComment("同源 QA 批注")
    s.Hyperlinks.Add(s.Range("G10"), "https://example.com/", "", "示例", "外部链接")
    s.Hyperlinks.Add(s.Range("G11"), "", "'费率 表'!B2", "跳转", "内部链接")
    book.Names.Add("AuditAmount", "='账簿 明细'!$B$2:$B$5")
    s.Names.Add("LocalAudit", "='账簿 明细'!$H$12:$H$15")
    v = s.Range("H12:H15").Validation
    v.Add(3, 1, 1, "低,中,高")
    v.InputTitle = "验证"
    v.InputMessage = "请选择值"
    v.ErrorMessage = "请用列表"
    fmt = s.Range("B2:B5").FormatConditions.Add(1, 5, "=15")
    fmt.Interior.Color = 0x88FF88
    fmt.Font.Color = 0x333333
    s.Range("F2:F5").FormatConditions.AddDataBar()
    s.Range("D2:D5").FormatConditions.AddColorScale(3)
    png = directory / "isolated-pixel.png"
    png.write_bytes(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGNgAAAAAgABSK+kcQAAAABJRU5ErkJggg=="))
    marker = s.Range("J12")
    shape = s.Shapes.AddPicture(str(png), False, True, marker.Left, marker.Top, 30, 25)
    shape.Placement = 1
    x = s.PageSetup
    x.PrintArea = "$A$1:$L$20"
    x.PrintTitleRows = "$1:$1"
    x.PrintTitleColumns = "$A:$A"
    x.Orientation = 2
    x.LeftMargin = 21
    x.CenterHorizontally = True
    x.CenterHeader = "核验"
    s.HPageBreaks.Add(s.Range("A18"))
    s.Activate()
    win = excel.ActiveWindow
    win.DisplayGridlines = False
    win.SplitRow = 2
    win.SplitColumn = 1
    win.FreezePanes = True
    excel.Calculate()


def varint(value):
    out = bytearray()
    while value >= 128:
        out.append(0x80 | value & 0x7F)
        value >>= 7
    out.append(value)
    return out


def read_varint(data, at, maximum):
    value = 0
    for shift in range(0, maximum * 7, 7):
        byte = data[at]
        at += 1
        value |= (byte & 0x7F) << shift
        if byte < 128:
            return value, at
    raise ValueError("invalid BIFF12 record header")


def scrub_binary(data, part):
    records = []
    at = 0
    while at < len(data):
        start = at
        kind, at = read_varint(data, at, 2)
        size, at = read_varint(data, at, 4)
        end = at + size
        if end > len(data):
            raise ValueError("truncated BIFF12 metadata")
        records.append((kind, data[at:end], data[start:end]))
        at = end
    out = bytearray()
    i = 0
    while i < len(records):
        kind, payload, raw = records[i]
        if part == "book" and tuple(record[0] for record in records[i:i+3]) == (37, 2071, 38):
            # The optional ACABSPATH production contains an absolute path.
            i += 3
            continue
        if kind == 2071:
            raise ValueError("unexpected BrtAbsPath15 placement")
        if part == "comments" and kind == 632:  # BrtCommentAuthor
            name = "ExcelLens QA".encode("utf-16-le")
            payload = pack("<I", len(name) // 2) + name
        elif part == "sheet" and kind == 478:  # BrtPageSetup
            if len(payload) < 38:
                raise ValueError("truncated BIFF12 page setup")
            payload = payload[:34] + pack("<I", 0xFFFFFFFF)
        else:
            out.extend(raw)
            i += 1
            continue
        out.extend(varint(kind) + varint(len(payload)) + payload)
        i += 1
    return out


def scrub_fixture(source, target):
    with ZipFile(source) as original, ZipFile(target, "w", ZIP_DEFLATED, compresslevel=9) as cleaned:
        for name in sorted(original.namelist()):
            lower = name.lower()
            if lower.startswith("xl/printersettings/") or lower == "docprops/custom.xml":
                continue
            if any(kind in lower for kind in ("vba", "externallinks", "threadedcomments", "persons/", "connections.")):
                raise ValueError(f"unexpected fixture part: {name}")
            body = original.read(name)
            if lower in ("docprops/core.xml", "docprops/app.xml"):
                body = re.sub(rb"(<(?:dc:creator|cp:lastModifiedBy)>)[^<]*(</[^>]+>)", rb"\1ExcelLens QA\2", body)
                body = re.sub(rb"(<dcterms:(?:created|modified)\b[^>]*>)[^<]*(</[^>]+>)", rb"\g<1>2000-01-01T00:00:00Z\2", body)
                body = re.sub(rb"<(Company|Manager|HyperlinkBase)>[^<]*</\1>", rb"<\1/>", body)
            elif name == "[Content_Types].xml":
                body = re.sub(rb'<(?:Override|Default)\s+[^<>]*ContentType="[^"]*\.printerSettings"[^<>]*/>', b"", body)
            elif lower.endswith(".rels"):
                body = re.sub(rb'<Relationship\s+[^<>]*Type="[^"]*/printerSettings"[^<>]*/>', b"", body)
            elif re.fullmatch(r"xl/comments[0-9]+\.xml", lower):
                body = re.sub(rb"<author>[^<]*</author>", b"<author>ExcelLens QA</author>", body)
            elif re.fullmatch(r"xl/comments[0-9]+\.bin", lower):
                body = scrub_binary(body, "comments")
            elif lower == "xl/workbook.bin":
                body = scrub_binary(body, "book")
            elif lower == "xl/workbook.xml":
                body = re.sub(
                    rb"<mc:AlternateContent\b[^<>]*>\s*<mc:Choice\b[^<>]*>\s*"
                    rb"<x15ac:absPath\b[^<>]*/>\s*</mc:Choice>\s*</mc:AlternateContent>", b"", body
                )
                if b":absPath" in body:
                    raise ValueError("unexpected OOXML absolute path placement")
            elif re.fullmatch(r"xl/worksheets/sheet[0-9]+\.xml", lower):
                body = re.sub(rb"<pageSetup\b[^<>]*>", lambda m: re.sub(rb' r:id="[^"]+"', b"", m[0]), body)
            elif re.fullmatch(r"xl/worksheets/sheet[0-9]+\.bin", lower):
                body = scrub_binary(body, "sheet")
            if any(marker in body for prefix in ("file:///", "\\Users\\", ":/Users/", "LOCALAPPDATA")
                   for marker in (prefix.encode(), prefix.encode("utf-16-le"))):
                raise ValueError(f"machine-specific path in {name}")
            part = ZipInfo(name, (2020, 1, 1, 0, 0, 0))
            part.compress_type = ZIP_DEFLATED
            part.create_system = 0
            cleaned.writestr(part, body, compresslevel=9)


def generate(directory):
    from win32com.client import DispatchEx
    excel = DispatchEx("Excel.Application")
    try:
        excel.DisplayAlerts = False
        excel.EnableEvents = False
        excel.AutomationSecurity = 3
        for name in ("fidelity2", "fidelity1904"):
            book = excel.Workbooks.Add(-4167)
            try:
                book.Date1904 = name == "fidelity1904"
                fidelity(book, directory, excel)
                for extension, code in (("xlsx", 51), ("xlsb", 50)):
                    book.SaveAs(str(directory / f"{name}.{extension}"), code)
            finally:
                book.Close(False)
    finally:
        excel.Quit()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path, help="output directory for normalized fixtures")
    parser.add_argument("--from-native", type=Path, help="normalize a previously generated synthetic Excel pair")
    args = parser.parse_args()
    output = args.output.resolve()
    with TemporaryDirectory(prefix="excellens-xlsb-pairs-") as staging:
        source = args.from_native.resolve() if args.from_native else Path(staging)
        if source == output:
            parser.error("native source and output must be different directories")
        if not args.from_native:
            generate(source)
        output.mkdir(parents=True, exist_ok=True)
        names = ("fidelity2.xlsb", "fidelity2.xlsx", "fidelity1904.xlsb", "fidelity1904.xlsx")
        entries = []
        for name in names:
            target = output / name
            scrub_fixture(source / name, target)
            entries.append(f"{hashlib.sha256(target.read_bytes()).hexdigest()}  {name}\n")
        (output / "SHA256SUMS").write_bytes("".join(entries).encode("ascii"))


if __name__ == "__main__":
    main()
