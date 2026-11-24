"""Deterministic, streaming source fixtures. Counts include the header."""
import hashlib
import struct
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo
from xml.etree import ElementTree as ET

NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG = "http://schemas.openxmlformats.org/package/2006/relationships"
TYPES = "http://schemas.openxmlformats.org/package/2006/content-types"

def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()

def csv_file(path, rows, different=False):
    with path.open("wb") as f:
        f.write(b"id,amount,office\n")
        for first in range(1, rows, 8192):
            f.writelines(f"{i:016d},{(i % 997) + (1 if different and i == rows-1 else 0)}.25,office-{i%100:02d}\n".encode()
                         for i in range(first, min(first+8192, rows)))

def member(name):
    result = ZipInfo(name, (2020, 1, 1, 0, 0, 0))
    result.compress_type = ZIP_DEFLATED
    return result

def xlsx_file(path, rows):
    header = lambda col, text: f'<c r="{col}1" t="inlineStr"><is><t>{text}</t></is></c>'
    with ZipFile(path, "w", ZIP_DEFLATED) as z:
        parts = {
            "[Content_Types].xml": f'<Types xmlns="{TYPES}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>'+''.join(f'<Override PartName="/xl/worksheets/sheet{i}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' for i in (1,2))+'</Types>',
            "_rels/.rels": f'<Relationships xmlns="{PKG}"><Relationship Id="book" Type="{REL}/officeDocument" Target="xl/workbook.xml"/></Relationships>',
            "xl/workbook.xml": f'<workbook xmlns="{NS}" xmlns:r="{REL}"><sheets><sheet name="Ledger" sheetId="1" r:id="s1"/><sheet name="Summary" sheetId="2" r:id="s2"/></sheets><definedNames><definedName name="AuditAmounts">Ledger!$B$2:$B$100</definedName></definedNames></workbook>',
            "xl/_rels/workbook.xml.rels": f'<Relationships xmlns="{PKG}"><Relationship Id="s1" Type="{REL}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="{REL}/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="styles" Type="{REL}/styles" Target="styles.xml"/></Relationships>',
            "xl/styles.xml": f'<styleSheet xmlns="{NS}"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="2"><xf xfId="0"/><xf xfId="0" numFmtId="4" applyNumberFormat="1"/></cellXfs></styleSheet>',
            "xl/worksheets/sheet2.xml": f'<worksheet xmlns="{NS}"><sheetData><row r="1"><c r="A1"><f>Ledger!B3+1</f><v>3.25</v></c></row></sheetData></worksheet>'
        }
        for name, text in parts.items():
            z.writestr(member(name), text.encode())
        with z.open(member("xl/worksheets/sheet1.xml"), "w") as sheet:
            sheet.write((f'<worksheet xmlns="{NS}"><dimension ref="A1:E{rows}"/><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" state="frozen"/></sheetView></sheetViews><cols><col min="1" max="1" width="24" customWidth="1"/></cols><sheetData><row r="1">'+header('A','id')+header('B','amount')+header('C','office')+header('D','notes')+'</row>').encode())
            for first in range(2,rows+1,4096):
                text=''.join(f'<row r="{i}"><c r="A{i}" t="inlineStr"><is><t>{i-1:016d}</t></is></c><c r="B{i}" s="1"><v>{(i-1)%997}.25</v></c><c r="C{i}" t="inlineStr"><is><t>office-{(i-1)%100:02d}</t></is></c></row>' for i in range(first,min(first+4096,rows+1)))
                sheet.write(text.encode())
            sheet.write(b'</sheetData><mergeCells count="1"><mergeCell ref="D1:E1"/></mergeCells></worksheet>')

def edit_fidelity(source, output):
    """Verify every unchanged cell and sheet feature, and hash every other OOXML part."""
    primary="xl/worksheets/sheet1.xml"
    def fingerprint(z, changed):
        h=hashlib.sha256(); cells=0; changes=0; features=[]
        with z.open(primary) as f:
            for _,el in ET.iterparse(f, events=("end",)):
                if el.tag=="{"+NS+"}c":
                    ref=el.get("r")
                    if ref=="B3":
                        assert el.findtext("{"+NS+"}v")==("4242.5" if changed else "2.25"), ref
                        assert el.get("s")=="1" and el.get("t","n")=="n"
                        changes+=1
                    else:
                        data=ET.tostring(el,encoding="utf-8").rstrip()
                        h.update(struct.pack("<I",len(data)));h.update(data)
                    cells+=1;el.clear()
                elif el.tag=="{"+NS+"}row": el.clear()
                elif el.tag.rsplit("}",1)[-1] in ("mergeCell","pane","col"):
                    features.append((el.tag,sorted(el.attrib.items())))
        assert changes==1
        return h.hexdigest(),cells,features
    with ZipFile(source) as a, ZipFile(output) as b:
        assert set(a.namelist())==set(b.namelist())
        workbook="xl/workbook.xml"
        before_book=ET.fromstring(a.read(workbook));after_book=ET.fromstring(b.read(workbook))
        calc=after_book.find("{"+NS+"}calcPr")
        assert calc is not None and calc.attrib=={"fullCalcOnLoad":"1","forceFullCalc":"1","calcCompleted":"0","calcOnSave":"1"}
        after_book.remove(calc)
        assert ET.tostring(before_book)==ET.tostring(after_book),"workbook content outside calculation flags changed"
        secondary="xl/worksheets/sheet2.xml"
        old=ET.fromstring(a.read(secondary));new=ET.fromstring(b.read(secondary))
        cell=old.find(".//{"+NS+"}c");value=cell.find("{"+NS+"}v")
        assert cell.findtext("{"+NS+"}f")=="Ledger!B3+1" and value.text=="3.25"
        cell.remove(value)
        assert ET.tostring(old)==ET.tostring(new),"dependent formula or sheet changed beyond invalidated cache"
        other=[n for n in a.namelist() if n not in (primary,workbook,secondary)]
        for n in other: assert a.read(n)==b.read(n), n
        before=fingerprint(a,False);after=fingerprint(b,True)
        assert before==after, (before,after)
        return {"recalcMetadataVerified":True,"dependentFormulaPreservedAndCacheCleared":True,"otherPartsMatched":len(other),"unchangedCells":before[1]-1,"sheetFeaturesMatched":len(before[2]),"editedCell":"B3","value":"4242.5"}


if __name__ == "__main__":
    import json,sys
    mode, target, option = sys.argv[1:4]
    if mode == "verify":
        print(json.dumps(edit_fidelity(Path(target),Path(option))))
    elif mode == "csv":
        csv_file(Path(target),int(option),"right" in sys.argv[4:])
    elif mode == "xlsx":
        xlsx_file(Path(target),int(option))
    else:
        raise ValueError(mode)
