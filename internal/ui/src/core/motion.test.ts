import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { collapseBannerToBadgeMotion, enterMotion, exitMotion, stopMotion } from './motion.ts';

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

test('collapseBannerToBadgeMotion creates a transient flight ghost, cleans up on finish or cancel, and honors reduced motion', async () => {
  const dom = installDom();
  try {
    let reduced = false;
    let cancelled = 0;
    let finishFlight: (() => void) | undefined;
    let badgePopped = 0;
    Reflect.set(window, 'matchMedia', () => ({ matches: reduced }));
    Reflect.set(globalThis, 'getComputedStyle', () => ({
      transform: 'none',
      getPropertyValue: () => '',
    }));

    const badge = document.createElement('button');
    Reflect.set(badge, 'getBoundingClientRect', () => ({
      left: 160,
      top: 8,
      width: 16,
      height: 16,
      right: 176,
      bottom: 24,
      x: 160,
      y: 8,
      toJSON: () => ({}),
    }));
    const prevAnimate: unknown = Reflect.get(HTMLElement.prototype, 'animate');
    Reflect.set(HTMLElement.prototype, 'animate', function (this: HTMLElement) {
      if (this === badge) {
        badgePopped++;
        return {
          cancel(): void {
            cancelled++;
          },
          finished: Promise.resolve(),
        };
      }
      return {
        cancel(): void {
          cancelled++;
        },
        finished: new Promise<void>((resolve) => {
          finishFlight = resolve;
        }),
      };
    });
    try {
      const from: DOMRect = {
        left: 0,
        top: 72,
        width: 1200,
        height: 34,
        right: 1200,
        bottom: 106,
        x: 0,
        y: 72,
        toJSON: () => ({}),
      };

      collapseBannerToBadgeMotion(from, badge, 'source-integrity-flight');
      assert.equal(document.querySelectorAll('.source-integrity-flight').length, 1);
      assert.equal(badge.style.opacity, '0');

      // Completing the flight removes the ghost, restores opacity, and pops the badge.
      finishFlight?.();
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(document.querySelectorAll('.source-integrity-flight').length, 0);
      assert.equal(badge.style.opacity, '');
      assert.equal(badgePopped, 1);

      // Cancelling mid-flight cleans up the ghost and restores opacity without popping.
      collapseBannerToBadgeMotion(from, badge, 'source-integrity-flight');
      assert.equal(document.querySelectorAll('.source-integrity-flight').length, 1);
      stopMotion(badge);
      assert.equal(document.querySelectorAll('.source-integrity-flight').length, 0);
      assert.equal(badge.style.opacity, '');
      assert.ok(cancelled >= 1);

      // Reduced motion skips creating any flight ghost.
      reduced = true;
      collapseBannerToBadgeMotion(from, badge, 'source-integrity-flight');
      assert.equal(document.querySelectorAll('.source-integrity-flight').length, 0);
    } finally {
      if (prevAnimate === undefined) Reflect.deleteProperty(HTMLElement.prototype, 'animate');
      else Reflect.set(HTMLElement.prototype, 'animate', prevAnimate);
    }
  } finally {
    dom.restore();
  }
});
