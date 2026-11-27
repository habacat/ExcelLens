import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { StylesResponse } from '../contracts/workbook.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeDefaultViewportPipelineDeps } from '../test-helpers/grid-fixture.ts';
import { ViewportPipeline } from './viewport-pipeline.ts';

test('ViewportPipeline.loadStylePalette tolerates Go null style slices, surfaces richTextNotice, and recovers from errors', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const toasts: { readonly msg: string; readonly tone?: string }[] = [];
    let reply: StylesResponse | Error = { styles: null };
    const deps = makeDefaultViewportPipelineDeps({
      api: async <T = unknown>() => {
        if (reply instanceof Error) throw reply;
        return reply as T;
      },
      toast: (msg, tone) => toasts.push({ msg, tone }),
    });
    deps.S.styles = [{ bold: true }];
    const pipeline = new ViewportPipeline(deps);

    await pipeline.loadStylePalette();
    assert.equal(deps.S.styles.length, 0);
    assert.equal(toasts.length, 0, 'null styles slice from CSV/TXT must not trigger an error toast');

    reply = { styles: [{ background: '#FFEEAA' }], richTextNotice: '部分富文本字体已回退' };
    await pipeline.loadStylePalette();
    assert.equal(deps.S.styles.length, 1);
    assert.equal(deps.S.styles[0]?.background, '#FFEEAA');
    assert.deepEqual(toasts.at(-1), { msg: '部分富文本字体已回退', tone: 'warning' });

    reply = new Error('样式表读取中断');
    await pipeline.loadStylePalette();
    assert.equal(deps.S.styles.length, 0);
    assert.equal(toasts.at(-1)?.tone, 'error');
    assert.match(toasts.at(-1)?.msg ?? '', /原始格式暂不可用：样式表读取中断/);
  } finally {
    dom.restore();
  }
});
