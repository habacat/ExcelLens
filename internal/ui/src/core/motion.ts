/**
 * Cancellable motion feedback for UI controls. Grid geometry never animates.
 */

export interface MotionOptions {
  readonly distance?: number;
  readonly duration?: number;
}

const active = new WeakMap<Element, Animation>();

function reduced(): boolean {
  return typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

export function stopMotion(node: Element | null | undefined): void {
  if (!node) return;
  const animation = active.get(node);
  active.delete(node);
  animation?.cancel();
}

function timing(style: CSSStyleDeclaration, token: string, fallback: number): number {
  const value = style.getPropertyValue(token).trim();
  if (!value) return fallback;
  return value.endsWith('ms') ? parseFloat(value) : parseFloat(value) * 1000;
}

export function enterMotion(
  node: Element | null | undefined,
  options: MotionOptions = {},
): Animation | undefined {
  const { distance = 0, duration } = options;
  stopMotion(node);
  if (!node || typeof node.animate !== 'function' || reduced()) return undefined;
  const style = getComputedStyle(node);
  const firstFrame: Keyframe = { opacity: 0 };
  const secondFrame: Keyframe = { opacity: 1 };
  if (distance) {
    const base = style.transform;
    firstFrame.transform = `${base === 'none' ? '' : base} translateY(${distance}px)`;
    secondFrame.transform = base;
  }
  const easing = style.getPropertyValue('--motion-ease').trim() || 'ease-out';
  const animation = node.animate([firstFrame, secondFrame], {
    duration: duration ?? timing(style, '--motion-normal', 160),
    easing,
  });
  active.set(node, animation);
  animation.finished.then(
    () => {
      if (active.get(node) === animation) active.delete(node);
    },
    () => {
      // Cancelled animations are expected and ignored.
    },
  );
  return animation;
}

export function exitMotion(
  node: Element | null | undefined,
  options: MotionOptions = {},
): Promise<boolean> {
  const { distance = 4, duration } = options;
  stopMotion(node);
  if (!node || typeof node.animate !== 'function' || reduced()) return Promise.resolve(true);
  const style = getComputedStyle(node);
  const base = style.transform;
  const animation = node.animate(
    [
      { opacity: 1, transform: base },
      { opacity: 0, transform: `${base === 'none' ? '' : base} translateY(${distance}px)` },
    ],
    {
      duration: duration ?? timing(style, '--motion-exit', 110),
      easing: 'ease-in',
      fill: 'forwards',
    },
  );
  active.set(node, animation);
  return animation.finished.then(
    () => {
      if (active.get(node) !== animation) return false;
      stopMotion(node);
      return true;
    },
    () => false,
  );
}
