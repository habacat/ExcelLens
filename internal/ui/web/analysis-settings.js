(() => {
  'use strict';
  const $=id=>document.getElementById(id),numbers=window.ExcelLensAnalysisNumbers;
  class AnalysisSettings {
    constructor({api,toast}){
      this.api=api;this.toast=toast;this.serial=0;this.config=null;this.saving=false;
      this.dialog=$('analysisSettingsDialog');
      $('analysisSettingsOpen').onclick=()=>{this.dialog.showModal();this.load();};
      $('analysisSettingsClose').onclick=()=>this.dialog.close();
      $('analysisSettingsReload').onclick=()=>this.load();
      $('analysisSettingsForm').onsubmit=event=>{event.preventDefault();this.save();};
      $('analysisSettingsFields').onchange=()=>this.preview();
      $('analysisSettingsDefaults').onclick=()=>this.fill(numbers.defaults);
      this.dialog.addEventListener('cancel',event=>{if(this.saving)event.preventDefault();});
      this.fill(numbers.get());
    }
    draft(){return {groupThousands:$('analysisGroupThousands').checked,scientific:$('analysisScientific').checked,scientificExponent:Number($('analysisScientificExponent').value)};}
    fill(options){$('analysisGroupThousands').checked=options.groupThousands;$('analysisScientific').checked=options.scientific;$('analysisScientificExponent').value=String(options.scientificExponent);this.preview();}
    preview(){
      const options=this.draft();$('analysisScientificExponent').disabled=!options.scientific;
      for(const [id,value] of [['analysisNumberPreview','1234567.89'],['analysisLargePreview','9007199254741015.11']]){
        $(id).textContent=numbers.format({display:value,exact:value},options);$(id).title='精确值：'+value;
      }
    }
    status(text,error=false){const node=$('analysisSettingsStatus');node.textContent=text;node.setAttribute('role',error?'alert':'status');node.classList.toggle('has-error',error);}
    busy(value){$('analysisSettingsFields').disabled=value;$('analysisSettingsSave').disabled=value||!this.config;$('analysisSettingsReload').disabled=value;$('analysisSettingsClose').disabled=this.saving;}
    accept(config){
      if(config?.version!==1||!Number.isSafeInteger(config.revision)||config.revision<0||!numbers.valid(config.options))throw Error('分析显示设置响应无效');
      this.config=config;const changed=JSON.stringify(numbers.get())!==JSON.stringify(config.options);
      numbers.set(config.options);if(changed)document.dispatchEvent(new Event('excellens:analysisnumberchange'));
    }
    async load(){
      if(this.saving)return;
      const serial=++this.serial;this.config=null;this.busy(true);this.status('正在读取设置…');
      try{
        const config=await this.api('/api/settings/analysis-display');if(serial!==this.serial)return;
        this.accept(config);this.fill(config.options);this.status('');
      }catch(error){if(serial===this.serial){this.status(error.message,true);if(!this.dialog.open)this.toast('显示设置未能载入：'+error.message,'warning');}}
      finally{if(serial===this.serial)this.busy(false);}
    }
    async save(){
      if(!this.config||this.saving)return;
      const serial=++this.serial,options=this.draft(),expectedRevision=this.config.revision;
      this.saving=true;this.busy(true);this.status('正在保存…');
      try{
        const config=await this.api('/api/settings/analysis-display',{method:'POST',body:JSON.stringify({expectedRevision,options})});
        if(serial!==this.serial)return;
        this.accept(config);this.status('已保存');this.dialog.close();this.toast('分析显示设置已保存','success');
      }catch(error){if(serial===this.serial){if(error.status===409||error.status===422)this.config=null;this.status(error.message,true);}}
      finally{if(serial===this.serial){this.saving=false;this.busy(false);}}
    }
  }
  window.ExcelLensAnalysisSettings=AnalysisSettings;
})();
