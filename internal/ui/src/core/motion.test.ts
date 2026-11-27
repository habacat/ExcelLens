import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { enterMotion, exitMotion } from './motion.ts';

test('shared motion cancels replaced feedback and respects reduced motion', () => {
  const dom = installDom();
  try {
    let reduced = false;
    let cancelled = 0;
    const calls: { readonly frames: Keyframe[]; readonly options: KeyframeAnimationOptions }[] = [];
    Reflect.set(window, 'matchMedia', () => ({ matches: reduced }));
    Reflect.set(globalThis, 'getComputedStyle', () => ({
      transform: 'matrix(1,0,0,1,-50,0)',
      getPropertyValue: () => '',
    }));

    const node = document.createElement('div');
    Reflect.set(
      node,
      'animate',
      (frames: Keyframe[], options: KeyframeAnimationOptions) => {
        calls.push({ frames, options });
        return {
          cancel: () => {
            cancelled++;
          },
          finished: new Promise<void>(() => undefined),
        };
      },
    );

    enterMotion(node, { distance: 3 });
    enterMotion(node);
    assert.equal(cancelled, 1);
    assert.equal(calls.length, 2);
    const firstTransform = calls[0]?.frames[0]?.transform;
    assert.equal(typeof firstTransform, 'string');
    assert.match(String(firstTransform), /matrix.*translateY/);
    assert.equal(calls[0]?.options.duration, 160);

    reduced = true;
    enterMotion(node);
    assert.equal(calls.length, 2);
    enterMotion(document.createElement('span'));
  } finally {
    dom.restore();
  }
});

test('exit completion is cancelled by a newer reveal and reduced motion stops existing animation', async () => {
  const dom = installDom();
  try {
    let reduced = false;
    let finish: (() => void) | undefined;
    let cancelled = 0;
    Reflect.set(window, 'matchMedia', () => ({ matches: reduced }));
    Reflect.set(globalThis, 'getComputedStyle', () => ({
      transform: 'none',
      getPropertyValue: () => '',
    }));

    const node = document.createElement('div');
    Reflect.set(node, 'animate', () => ({
      cancel(): void {
        cancelled++;
      },
      finished: new Promise<void>((resolve) => {
        finish = resolve;
      }),
    }));

    const leaving = exitMotion(node);
    const oldFinish = finish;
    enterMotion(node);
    oldFinish?.();
    assert.equal(await leaving, false);
    assert.equal(cancelled, 1);

    reduced = true;
    enterMotion(node);
    assert.equal(cancelled, 2);
    assert.equal(await exitMotion(node), true);
  } finally {
    dom.restore();
  }
});
