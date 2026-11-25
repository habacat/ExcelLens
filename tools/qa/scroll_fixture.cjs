// Deterministic, densely styled workbook used by both sides of the scroll comparison.
const {zip}=require('./generated_workbook.cjs');
const colors=['C5DCEF','E7C1AF','BADABE','F1D49B','CBBFE2','9ECFD1','D5CBAA'];
const border='48647A';
function column(n){let s='';while(n){n--;s=String.fromCharCode(65+n%26)+s;n=Math.floor(n/26);}return s;}
function fixture(extraMerges=[]){
  const ns='http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  const rel='http://schemas.openxmlformats.org/officeDocument/2006/relationships';
  const rows=[],merges=extraMerges.map(ref=>'<mergeCell ref="'+ref+'"/>');
  for(let r=1;r<=1600;r++){
    const cells=[];
    for(let c=1;c<=96;c++){
      const value=r*1000+c,ref=column(c)+r,s=1+(r*3+c*5+Math.floor(r/11))%colors.length;
      const body=c===19?'<f>ROW()*1000+COLUMN()</f><v>'+value+'</v>':c===31?'<is><r><rPr><b/></rPr><t>'+r+'</t></r><r><t>:'+c+'</t></r></is>':'<v>'+value+'</v>';
      cells.push('<c r="'+ref+'" s="'+s+'"'+(c===31?' t="inlineStr"':'')+'>'+body+'</c>');
    }
    if(r%41===0)merges.push('<mergeCell ref="H'+r+':I'+(r+1)+'"/>');
    rows.push('<row r="'+r+'">'+cells.join('')+'</row>');
  }
  const fills=colors.map(color=>'<fill><patternFill patternType="solid"><fgColor rgb="FF'+color+'"/></patternFill></fill>').join('');
  const borders=['thin','medium','dashed','double'].map(type=>'<border>'+['left','right','top','bottom'].map(side=>'<'+side+' style="'+type+'"><color rgb="FF'+border+'"/></'+side+'>').join('')+'</border>').join('');
  const formats=colors.map((_,i)=>'<xf numFmtId="0" fontId="0" fillId="'+(i+2)+'" borderId="'+(i%4+1)+'" applyFill="1" applyBorder="1"/>').join('');
  return zip({
    '[Content_Types].xml':'<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>',
    '_rels/.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="book" Type="'+rel+'/officeDocument" Target="xl/workbook.xml"/></Relationships>',
    'xl/workbook.xml':'<workbook xmlns="'+ns+'" xmlns:r="'+rel+'"><sheets><sheet name="Scroll" sheetId="1" r:id="s1"/></sheets></workbook>',
    'xl/_rels/workbook.xml.rels':'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="'+rel+'/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="styles" Type="'+rel+'/styles" Target="styles.xml"/></Relationships>',
    'xl/styles.xml':'<styleSheet xmlns="'+ns+'"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="9"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>'+fills+'</fills><borders count="5"><border/>'+borders+'</borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="8"><xf/>'+formats+'</cellXfs></styleSheet>',
    'xl/worksheets/sheet1.xml':'<worksheet xmlns="'+ns+'"><dimension ref="A1:CR1600"/><sheetFormatPr defaultRowHeight="18"/><cols><col min="1" max="96" width="12" customWidth="1"/></cols><sheetData>'+rows.join('')+'</sheetData><mergeCells count="'+merges.length+'">'+merges.join('')+'</mergeCells></worksheet>'
  });
}
module.exports={fixture,colors,border};
