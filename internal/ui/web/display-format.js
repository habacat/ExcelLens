(() => {
  'use strict';
  const normal=new Intl.NumberFormat('en-US',{maximumFractionDigits:1}),small=new Intl.NumberFormat('en-US',{maximumFractionDigits:3});
  function bytes(value){
    if(typeof value!=='number'||!Number.isFinite(value)||value<0)return '—';
    let size=value/1024;
    const units=['KB','MB','GB','TB'];let unit=0;
    while(size>=1024&&unit<units.length-1){size/=1024;unit++;}
    return (size>0&&size<.1?small:normal).format(size)+' '+units[unit];
  }
  window.ExcelLensDisplayFormat={bytes};
})();
