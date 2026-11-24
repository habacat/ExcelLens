(() => {
  'use strict';
  const TOKEN=document.querySelector('meta[name="app-token"]')?.content||'';
  async function api(url, options = {}, responseType = 'json') {
    const activity = window.ExcelLensActivity?.request(url, options);
    try {
      await window.ExcelLensSession?.ready;
      const headers = new Headers(options.headers || {});
      headers.set("X-ExcelLens-Token", TOKEN);
      window.ExcelLensSession?.apply(headers);
      if (options.body && typeof FormData !== 'undefined' && !(options.body instanceof FormData) && !headers.has('Content-Type')) {
        headers.set('Content-Type', 'application/json');
      }
      const response = await fetch(url, { ...options, headers, cache: "no-store" });
      if (response.headers?.get?.('X-ExcelLens-Recovery') === 'required') window.location.replace(window.location.pathname);
      if (responseType === 'response') return response;
      if (response.ok && responseType === 'text') return response.text();
      if (response.ok && responseType === 'blob') return response.blob();
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {const error=new Error(data.error || `请求失败（HTTP ${response.status}）`);error.status=response.status;throw error;}
      if(data.compact===1 && Array.isArray(data.rows))data.rows=window.ExcelLensWire.unpack(data.rows);
      return data;
    } finally { activity?.end(); }
  }
  const post=(url,payload,options={})=>api(url,{...options,method:'POST',body:JSON.stringify(payload)});

  function upload(file,activity,fmtBytes){
    return new Promise((resolve, reject) => {
      activity?.stage('正在读取文件', 0, 28);
      const form = new FormData();
      form.append("file", file, file.name);
      const xhr = new XMLHttpRequest();
      xhr.open("POST", "/api/open");
      xhr.setRequestHeader("X-ExcelLens-Token", TOKEN);
      window.ExcelLensSession?.apply(new Headers()).forEach((value, name) => xhr.setRequestHeader(name, value));
      xhr.upload.onprogress = event => {
        if (event.lengthComputable) activity?.report(event.loaded, event.total, '', fmtBytes(event.loaded) + ' / ' + fmtBytes(event.total));
      };
      xhr.upload.onload = () => activity?.stage('正在解析文件', 28, 60);
      xhr.onload = () => {
        let data;
        try { data = JSON.parse(xhr.responseText); } catch { /* Surface malformed output as an open failure. */ }
        if (xhr.status >= 200 && xhr.status < 300 && data) resolve(data);
        else reject(new Error(data?.error || '无法打开文件（HTTP ' + xhr.status + '）'));
      };
      xhr.onerror = () => reject(new Error("本机文件读取失败，请重新选择文件"));
      xhr.onabort = () => reject(new Error("读取已取消"));
      xhr.send(form);
    });
  }
  window.ExcelLensTransport=Object.freeze({api,post,upload});
})();
