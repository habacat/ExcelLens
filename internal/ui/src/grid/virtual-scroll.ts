/**
 * Logical worksheet pixel coordinate scaling against the browser's bounded
 * native scrollbar range, plus scroll velocity/prediction tracking.
 */

export interface WheelEventLike {
  readonly deltaMode: number;
  readonly deltaX: number;
  readonly deltaY: number;
  readonly shiftKey: boolean;
}

export interface WheelTarget {
  readonly top: number;
  readonly left: number;
}

interface WheelAnchor {
  readonly physical: number;
  readonly logical: number;
}

export class ScrollMotion {
  public readMs = 35;
  public renderMs = 12;
  public x: number | null = null;
  public y: number | null = null;
  public time: number | null = null;
  public vx = 0;
  public vy = 0;
  public dx = 0;
  public dy = 0;

  constructor() {
    this.reset();
  }

  reset(): void {
    this.x = null;
    this.y = null;
    this.time = null;
    this.vx = 0;
    this.vy = 0;
    this.dx = 0;
    this.dy = 0;
  }

  sample(x: number, y: number, time: number): void {
    const dt = this.time === null ? 0 : time - this.time;
    this.dx = this.x === null ? 0 : x - this.x;
    this.dy = this.y === null ? 0 : y - this.y;
    if (this.time !== null && dt > 0 && dt < 280) {
      const velocity = (delta: number, prior: number): number => {
        const instant = delta / Math.max(4, dt);
        return Math.sign(instant) === Math.sign(prior) ? instant * 0.65 + prior * 0.35 : instant;
      };
      this.vx = velocity(this.dx, this.vx);
      this.vy = velocity(this.dy, this.vy);
    } else {
      this.vx = 0;
      this.vy = 0;
    }
    this.x = x;
    this.y = y;
    this.time = time;
  }

  observe(kind: 'read' | 'render', ms: number): void {
    const clamped = Math.max(1, Math.min(600, ms));
    if (kind === 'read') {
      this.readMs = this.readMs * 0.75 + clamped * 0.25;
    } else {
      this.renderMs = this.renderMs * 0.75 + clamped * 0.25;
    }
  }

  predict(
    width: number,
    height: number,
    time: number,
  ): { readonly x: readonly [number, number]; readonly y: readonly [number, number] } {
    const moving = this.time !== null && time - this.time < 300;
    const horizon = Math.min(450, Math.max(65, this.readMs + this.renderMs * 2 + 48));
    const axis = (size: number, speed: number): readonly [number, number] => {
      const lead = Math.min(size * 3, Math.abs(speed) * (horizon + 120) + (speed ? size * 0.9 : 0));
      return [size * 0.65 + (speed < 0 ? lead : 0), size * 0.65 + (speed > 0 ? lead : 0)];
    };
    return {
      x: axis(width, moving ? this.vx : 0),
      y: axis(height, moving ? this.vy : 0),
    };
  }
}

export class VirtualScroll {
  public static readonly Motion = ScrollMotion;
  public readonly maxPixels: number;
  public scale = 1;
  public logicalMax = 0;
  public physicalMax = 0;
  private wheelAnchor: WheelAnchor | null = null;

  constructor(maxPixels = 15_000_000) {
    this.maxPixels = maxPixels;
  }

  configure(height: number, viewport: number): number {
    const physical = Math.max(viewport, Math.min(this.maxPixels, height));
    const nextLogicalMax = Math.max(0, height - viewport);
    const nextPhysicalMax = Math.max(0, physical - viewport);
    if (this.logicalMax !== nextLogicalMax || this.physicalMax !== nextPhysicalMax) {
      this.wheelAnchor = null;
    }
    this.logicalMax = nextLogicalMax;
    this.physicalMax = nextPhysicalMax;
    this.scale = this.physicalMax > 0 ? Math.max(1, this.logicalMax / this.physicalMax) : 1;
    return physical;
  }

  logical(physical: number): number {
    return Math.min(this.logicalMax, Math.max(0, physical) * this.scale);
  }

  physical(logical: number): number {
    return Math.min(this.physicalMax, Math.max(0, logical) / this.scale);
  }

  rowPosition(logicalRow: number, physical: number): number {
    return logicalRow - this.logical(physical) + physical;
  }

  wheel(
    event: WheelEventLike,
    physical: number,
    rowHeight: number,
    pageHeight: number,
  ): WheelTarget {
    const unit =
      event.deltaMode === 1 ? rowHeight : event.deltaMode === 2 ? Math.max(rowHeight, pageHeight) : 1;
    const x = (event.deltaX || 0) * unit;
    const y = (event.deltaY || 0) * unit;
    if (event.shiftKey && !x) return { top: physical, left: y };
    const previous = this.wheelAnchor;
    const base =
      previous && Math.abs(physical - previous.physical) <= 1 / 32
        ? previous.logical
        : this.logical(physical);
    const target = Math.min(this.logicalMax, Math.max(0, base + y));
    const top =
      target <= 0 ? 0 : target >= this.logicalMax ? this.physicalMax : this.physical(target);
    this.wheelAnchor = { physical: top, logical: target };
    return { top, left: x };
  }
}
