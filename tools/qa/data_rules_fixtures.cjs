// Synthetic public fixtures. Rule arrays stay metadata, independent of cells.
const {zip}=require('./generated_workbook.cjs');
const escape=s=>String(s).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;');
function ooxml({extra=125,incomplete=true}={}){
  const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main',rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const values=Array.from({length:121},(_,i)=>i===1?'':i===2?'<img src=x onerror="window.badDataRule=1">':String(i+1).padStart(3,'0'));
  const formula='"'+values.join(',').replaceAll('"','""')+'"',refs=Array.from({length:121},(_,i)=>'A'+(i+2)).join(' ');
  const rules=[`<dataValidation type="list" sqref="${refs}" allowBlank="1" showInputMessage="1" showErrorMessage="1" errorStyle="warning" promptTitle="编号" prompt="保留前导零" errorTitle="核对" error="请选择保存的编号"><formula1>${escape(formula)}</formula1></dataValidation>`,
    '<dataValidation type="decimal" sqref="B2:B1048576"><formula1>0</formula1><formula2>9007199254740994.31</formula2></dataValidation>',
    '<dataValidation type="custom" sqref="B2 B2:B3"><formula1>OR(B2=0,B2&gt;1)</formula1></dataValidation>',
    ...(incomplete?['<dataValidation type="whole" sqref="XFE1"><formula1>1</formula1><formula2>9</formula2></dataValidation>']:[]),
    '<dataValidation type="list" sqref="D2"><formula1>尚未展开的名称</formula1></dataValidation>',
    ...Array.from({length:extra},(_,i)=>`<dataValidation type="whole" sqref="C1:C1048576"><formula1>${i}</formula1><formula2>${i+1}</formula2></dataValidation>`)].join('');
  const ext='<extLst><ext uri="{CCE6A557-97BC-4B89-ADB6-D9C93CAAB3DF}" xmlns:k="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main" xmlns:m="http://schemas.microsoft.com/office/excel/2006/main"><k:dataValidations count="1" disablePrompts="1"><k:dataValidation type="whole"><k:formula1><m:f>1</m:f></k:formula1><k:formula2><m:f>9</m:f></k:formula2><m:sqref>B2:B3</m:sqref></k:dataValidation></k:dataValidations></ext></extLst>';
  return zip({
    'xl/workbook.xml':`<workbook xmlns="${ns}" xmlns:r="${rel}"><sheets><sheet name="明细" sheetId="1" r:id="s1"/><sheet name="无规则" sheetId="2" r:id="s2"/></sheets></workbook>`,
    'xl/_rels/workbook.xml.rels':`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="${rel}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="${rel}/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
    'xl/worksheets/sheet1.xml':`<worksheet xmlns="${ns}"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>编号</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>001</t></is></c><c r="B2"><v>9007199254740993.31</v></c><c r="C2" t="inlineStr"><is><t>1234.5</t></is></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>002</t></is></c><c r="B3"><v>0</v></c></row><row r="4"><c r="A4" t="inlineStr"><is><t>003</t></is></c><c r="B4"><v>-2</v></c></row><row r="5"><c r="B5"><f>SUM(B2:B4)</f></c></row></sheetData><dataValidations count="${(incomplete?5:4)+extra}">${rules}</dataValidations>${ext}</worksheet>`,
    'xl/worksheets/sheet2.xml':`<worksheet xmlns="${ns}"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>没有规则</t></is></c></row></sheetData></worksheet>`
  });
}
module.exports={ooxml};
