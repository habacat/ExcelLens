// Generated OOXML for independent source-font, DPI and application-zoom checks.
const {zip}=require('./generated_workbook.cjs');
function presentation(){
  const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const text=(ref,value,style=0)=>`<c r="${ref}" s="${style}" t="inlineStr"><is><t>${value}</t></is></c>`;
  const number=(ref,value,style=2)=>`<c r="${ref}" s="${style}"><v>${value}</v></c>`;
  const rows=[
    `<row r="1" ht="25" customHeight="1">${text('A1','源字体与精确金额',1)}</row>`,
    '<row r="2" ht="20" customHeight="1"/>',
    `<row r="3">${text('A3','000003')}${number('B3','12345678.25')}<c r="C3" t="inlineStr"><is><r><rPr><rFont val="宋体"/><sz val="14"/><b val="0"/><u val="none"/></rPr><t>中😀</t></r><r><rPr><rFont val="Arial"/><sz val="10"/><i/><vertAlign val="superscript"/><color rgb="FF1255AA"/></rPr><t>字体对照</t></r></is></c>${number('E3','9007199254740993.31',3)}</row>`,
    `<row r="4">${text('A4','0000001234567890123')}${number('B4','123.45')}${number('C4','1.236')}${number('D4','45292',4)}<c r="E4"><f>SUM(B3:B4)</f></c></row>`,
    `<row r="5">${text('A5','源行与标题')}${number('B5','-0.25')}${text('C5','&lt;img src=x onerror="window.badPresentation=1"&gt;')}</row>`,
    `<row r="6" hidden="1">${text('A6','隐藏行')}${number('B6','999')}</row>`,
    `<row r="7">${text('A7','合并数字')}${number('D7','1234567890.25',3)}</row>`,
    `<row r="8" s="3" customFormat="1"><c r="B8"><v>9876.5</v></c>${text('C8','继承行字体')}</row>`,
    `<row r="12">${text('A12','末行完整')}</row>`
  ].join('');
  const styles=`<styleSheet xmlns="${ns}"><fonts count="3"><font><name val="Arial"/><sz val="11"/></font><font><name val="宋体"/><sz val="14"/><b/><color rgb="FF154E86"/></font><font><name val="Consolas"/><sz val="11"/><b/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFE7EEF3"/></patternFill></fill></fills><borders count="2"><border/><border><bottom style="double"><color rgb="FF154E86"/></bottom></border></borders><cellXfs count="5"><xf fontId="0" numFmtId="0" fillId="0" borderId="0"/><xf fontId="1" numFmtId="0" fillId="1" borderId="1"><alignment horizontal="center" vertical="center"/></xf><xf fontId="0" numFmtId="4" fillId="0" borderId="0"/><xf fontId="2" numFmtId="4" fillId="0" borderId="0"/><xf fontId="0" numFmtId="14" fillId="0" borderId="0"/></cellXfs><dxfs count="1"><dxf><font><b val="0"/><color rgb="FFB83232"/></font><numFmt numFmtId="166" formatCode="0.00"/></dxf></dxfs></styleSheet>`;
  return zip({
    'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets><sheet name="呈现" sheetId="1" r:id="s1"/></sheets><definedNames><definedName name="_xlnm.Print_Area" localSheetId="0">'呈现'!$A$1:$F$12</definedName></definedNames></workbook>`,
    'xl/_rels/workbook.xml.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="${rel}/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
    'xl/styles.xml':styles,
    'xl/worksheets/sheet1.xml':`<worksheet xmlns="${ns}"><sheetFormatPr defaultRowHeight="22"/><cols><col min="1" max="1" width="14"/><col min="2" max="2" width="13"/><col min="3" max="3" width="22"/><col min="4" max="6" width="10"/></cols><sheetData>${rows}</sheetData><mergeCells count="2"><mergeCell ref="A1:B2"/><mergeCell ref="D7:E7"/></mergeCells><conditionalFormatting sqref="C4"><cfRule type="cellIs" priority="1" dxfId="0" operator="greaterThan"><formula>0</formula></cfRule></conditionalFormatting><printOptions gridLines="1" headings="1"/><pageMargins left="0.2" right="0.2" top="0.2" bottom="0.2" header="0.1" footer="0.1"/><pageSetup paperSize="9" orientation="landscape" scale="100"/></worksheet>`
  });
}
module.exports={presentation};
