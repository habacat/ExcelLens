import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { ApiRequestOptions } from '../contracts/common.ts';
import { createFileFormats } from '../core/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';
import { restoreReportQuery } from './pivot-view.ts';
import { AnalysisWorkspace } from './workspace.ts';

const tick = (): Promise<void> =>
  new Promise((resolve) => {
    setImmediate(resolve);
  });

function deferred<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

function setup() {
  const dom = installDom({ fromIndexHtml: true });
  const root = document.getElementById('analysisRoot');
  assert.ok(root);

  const messages: string[] = [];
  let apiImpl: (path: string, options?: ApiRequestOptions) => Promise<unknown> = async () => ({});

  let wsRef: AnalysisWorkspace | null = null;
  const ws = new AnalysisWorkspace(root, {
    api: async <T = unknown>(path: string, options?: ApiRequestOptions): Promise<T> =>
      (await apiImpl(path, options)) as T,
    formats: createFileFormats(makeTestFileFormatsConfig()),
    getSourceContext: () => {
      assert.ok(wsRef);
      return {
        source: wsRef.state.source,
        workbookKey: wsRef.state.fingerprint,
        book: wsRef.state.book,
        filterId: '',
        viewId: 'view',
        excludeThroughRow: 0,
      };
    },
    onToast: (msg) => {
      messages.push(msg);
    },
  });
  wsRef = ws;

  ws.state.fingerprint = 'same-content';
  ws.state.book = 'path-one';
  ws.state.opened = true;
  ws.state.source = 'wb:同名表';
  ws.state.headerRow = 1;
  ws.state.query = restoreReportQuery({ source: 'wb:同名表', rows: [], aggregate: 'count' });
  ws.state.fields = [{ id: 'C1', name: 'old', column: 1 }];
  ws.state.fieldsReady = true;
  ws.state.abortIndex = 0;

  let clusterResets = 0;
  const origClusterReset = ws.clusters.reset.bind(ws.clusters);
  ws.clusters.reset = () => {
    clusterResets++;
    origClusterReset();
  };

  return {
    ws,
    state: ws.state,
    messages,
    getClusterResets: () => clusterResets,
    setApi: (fn: typeof apiImpl) => {
      apiImpl = fn;
    },
    restore: () => {
      ws.close();
      dom.restore();
    },
  };
}

test('a profile from identical sheet names at another workbook path cannot publish data or errors', async () => {
  for (const isError of [false, true]) {
    const h = setup();
    try {
      const gate = deferred<unknown>();
      h.setApi(() => gate.promise);
      const pending = h.ws.loadFieldProfile({ id: 'C1', name: '原字段', column: 1 });
      h.state.book = 'path-two';
      const profileEl = document.getElementById('analysisFieldProfile');
      assert.ok(profileEl);
      profileEl.textContent = '新文件';
      if (isError) gate.reject(new Error('旧文件错误'));
      else gate.resolve({ rows: 99 });
      await pending;
      assert.equal(profileEl.textContent, '新文件');
      assert.deepEqual(h.messages, []);
    } finally {
      h.restore();
    }
  }
});

test('new field intent cancels the older profile even when the old response ignores abort', async () => {
  const h = setup();
  try {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    let calls = 0;
    h.setApi(() => (++calls === 1 ? first.promise : second.promise));

    const a = h.ws.loadFieldProfile({ id: 'C1', name: '甲', column: 1 });
    const b = h.ws.loadFieldProfile({ id: 'C2', name: '乙', column: 2 });
    first.reject(new Error('甲的过期错误'));
    await a;
    assert.match(document.getElementById('analysisFieldProfile')?.textContent ?? '', /乙/);
    assert.deepEqual(h.messages, []);

    const abortErr = new Error('cancelled');
    abortErr.name = 'AbortError';
    second.reject(abortErr);
    await b;
  } finally {
    h.restore();
  }
});

test('a late captured range and schema cannot replace a newer full-source intent', async () => {
  const h = setup();
  try {
    const gate = deferred<unknown>();
    const cleared: unknown[] = [];
    h.setApi(async (path, options) => {
      if (path.endsWith('/capture')) return gate.promise;
      if (path.endsWith('/clear')) {
        const body =
          typeof options?.body === 'string'
            ? (JSON.parse(options.body) as { id?: unknown })
            : {};
        cleared.push(body.id);
        return {};
      }
      return {
        fields: [{ id: 'C1', name: '完整源字段', column: 1 }],
        preview: [],
        totalRows: 10,
        headerRow: 1,
        truncatedFields: false,
      };
    });

    const old = h.ws.loadFields({ mode: 'visible' });
    await tick();
    await h.ws.loadFields({ mode: 'full' });
    gate.resolve({
      id: 'late',
      source: h.state.source,
      rows: 5,
      workbookKey: h.state.fingerprint,
      book: h.state.book,
      sourceScope: { version: 1, mode: 'visible', fingerprint: h.state.fingerprint },
    });
    await old;
    assert.equal(h.state.fields[0]?.name, '完整源字段');
    assert.equal(h.state.query?.sourceScope, undefined);
    assert.equal(h.state.fieldsReady, true);
    assert.equal(h.state.fieldsLoading, false);
    assert.deepEqual(cleared, ['late']);
    assert.deepEqual(h.messages, []);
    assert.equal(h.getClusterResets(), 2);
  } finally {
    h.restore();
  }
});

test('drill guards both group and result task when requests reuse the same page offset', async () => {
  const h = setup();
  try {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    const requests: Record<string, unknown>[] = [];
    h.state.jobID = 'completed';
    h.state.activeResultQuery = restoreReportQuery({
      source: h.state.source,
      rows: [],
      aggregate: 'count',
    });
    h.setApi(async (_path, options) => {
      const body =
        typeof options?.body === 'string'
          ? (JSON.parse(options.body) as Record<string, unknown>)
          : {};
      requests.push(body);
      return requests.length === 1 ? first.promise : second.promise;
    });

    const a = h.ws.pivotView.drill(['甲'], '', 0);
    const b = h.ws.pivotView.drill(['乙'], '', 0);
    first.reject(new Error('旧组错误'));
    await a;
    assert.equal(document.getElementById('analysisDrillInfo')?.textContent, '正在读取源数据…');
    assert.deepEqual(h.messages, []);
    assert.equal(requests[0]?.jobId, 'completed');

    second.resolve({ rows: [], fields: [], hasMore: false, scanned: 3 });
    await b;
    assert.match(document.getElementById('analysisDrillInfo')?.textContent ?? '', /3/);
  } finally {
    h.restore();
  }
});
