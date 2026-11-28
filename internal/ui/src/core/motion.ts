/**
 * Cancellable motion feedback for UI controls. Grid geometry never animates.
 */

export interface MotionOptions {
  readonly distance?: number;
  readonly duration?: number;
}

const active = new WeakMap<Element, Animation>();
const ghosts = new WeakMap<Element, HTMLElement>();

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
  const ghost = ghosts.get(node);
  if (ghost) {
    ghosts.delete(node);
    ghost.remove();
    if (node instanceof HTMLElement) node.style.removeProperty('opacity');
  }
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

export function collapseBannerToBadgeMotion(
  from: DOMRect,
  target: HTMLElement | null | undefined,
  ghostClass: string,
): void {
  stopMotion(target);
  if (!target || reduced() || from.width <= 0 || from.height <= 0) return;
  if (typeof target.animate !== 'function') return;
  const to = target.getBoundingClientRect();
  if (to.width <= 0 || to.height <= 0) return;

  const ghost = document.createElement('span');
  ghost.className = ghostClass;
  ghost.setAttribute('aria-hidden', 'true');
  document.body.appendChild(ghost);
  ghosts.set(target, ghost);

  const rootStyle = getComputedStyle(document.documentElement);
  const token = (name: string, fallback: string): string =>
    rootStyle.getPropertyValue(name).trim() || fallback;
  const warnSoft = token('--warn-soft', '#fff5d9');
  const warnLine = token('--warn-line', '#ecd596');
  const badgeBg = token('--warn-badge-bg', '#f6c445');
  const badgeLine = token('--warn-badge-line', '#d49b1a');

  const midW = Math.min(from.width * 0.28, Math.max(to.width * 3.5, 56));
  const midH = Math.max(to.height + 4, Math.min(from.height * 0.68, 24));
  const midLeft = to.left + (from.left - to.left) * 0.24;
  const midTop = to.top + (from.top - to.top) * 0.42;

  target.style.opacity = '0';
  const flight = ghost.animate(
    [
      {
        left: `${from.left}px`,
        top: `${from.top}px`,
        width: `${from.width}px`,
        height: `${from.height}px`,
        borderRadius: '3px',
        backgroundColor: warnSoft,
        borderColor: warnLine,
        opacity: 0.96,
        offset: 0,
      },
      {
        left: `${midLeft}px`,
        top: `${midTop}px`,
        width: `${midW}px`,
        height: `${midH}px`,
        borderRadius: '12px',
        backgroundColor: badgeBg,
        borderColor: badgeLine,
        opacity: 1,
        offset: 0.48,
      },
      {
        left: `${to.left}px`,
        top: `${to.top}px`,
        width: `${to.width}px`,
        height: `${to.height}px`,
        borderRadius: '50%',
        backgroundColor: badgeBg,
        borderColor: badgeLine,
        opacity: 1,
        offset: 1,
      },
    ],
    {
      duration: 240,
      easing: 'cubic-bezier(0.22, 1, 0.36, 1)',
      fill: 'forwards',
    },
  );
  active.set(target, flight);
  flight.finished.then(
    () => {
      if (active.get(target) !== flight) return;
      stopMotion(target);
      if (target.hidden) return;
      const pop = target.animate(
        [
          { transform: 'scale(0.78)' },
          { transform: 'scale(1.18)', offset: 0.55 },
          { transform: 'scale(1)' },
        ],
        {
          duration: 150,
          easing: 'cubic-bezier(0.2, 0.75, 0.25, 1)',
        },
      );
      active.set(target, pop);
      pop.finished.then(
        () => {
          if (active.get(target) === pop) active.delete(target);
        },
        () => {
          // Cancelled animations are expected and ignored.
        },
      );
    },
    () => {
      // Cancelled animations are cleaned up via stopMotion.
    },
  );
}
