/**
 * Lazy viewport worksheet picture overlay and modal image gallery viewer.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { ImageDTO, ImageMarker } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireAnchor, requireButton, requireEl } from '../core/dom.ts';
import type { WorkbookState } from './state.ts';
import { decodeImagesApiResponse } from '../core/http-decoders.generated.ts';

const clamp = (x: number, min: number, max: number): number => Math.max(min, Math.min(max, x));
const fmtPx = (x: number): string => `${Math.round(x * 100) / 100}px`;

type PaneName = 'normal' | 'top' | 'left' | 'corner';
const PANE_NAMES: readonly PaneName[] = ['normal', 'top', 'left', 'corner'];

interface Rect {
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

interface PaneBox extends Rect {
  readonly dx: number;
  readonly dy: number;
}

function validRect(r: Rect): boolean {
  return (
    Number.isFinite(r.x) &&
    Number.isFinite(r.y) &&
    Number.isFinite(r.w) &&
    Number.isFinite(r.h) &&
    r.w > 0 &&
    r.h > 0
  );
}

export interface PicturesOptions {
  readonly state: WorkbookState;
  readonly grid: HTMLElement;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly rowHead: number;
  readonly scale: () => number;
  readonly rowTop: (row: number) => number;
  readonly colLeft: (col: number) => number;
  readonly headerHeight: () => number;
  readonly frozen: () => { readonly rows: number; readonly cols: number };
}

/** @httpResponse ImagesApiResponse */
export interface ImagesApiResponse {
  readonly images?: readonly ImageDTO[];
}

export class Pictures {
  private readonly o: PicturesOptions;
  private items: ImageDTO[] = [];
  private readonly nodes = new Map<string, HTMLButtonElement>();
  private enabled = true;
  private frame = 0;
  private serial = 0;
  private active = -1;
  private rotation = 0;
  private zoom = 1;
  private panX = 0;
  private panY = 0;
  private natural = { w: 1, h: 1 };
  private readonly initialRects = new Map<number, { x?: number; y?: number; w: number; h: number }>();
  private focusBeforeViewer: HTMLElement | null = null;
  private flipH = false;
  private flipV = false;
  private fitToWindow = true;
  private readonly viewer: HTMLElement;
  private readonly stage: HTMLElement;
  private readonly preview: HTMLImageElement;
  private readonly root: HTMLElement;
  private readonly panes: Record<PaneName, HTMLElement>;

  constructor(options: PicturesOptions) {
    this.o = options;
    this.viewer = requireEl('pictureViewer');
    this.stage = requireEl('pictureStage');
    const previewEl = requireEl('picturePreview');
    if (!(previewEl instanceof HTMLImageElement)) {
      throw new Error('Expected HTMLImageElement for #picturePreview');
    }
    this.preview = previewEl;
    this.root = requireEl('pictureOverlay');
    this.panes = {
      normal: requireEl('picturePane-normal'),
      top: requireEl('picturePane-top'),
      left: requireEl('picturePane-left'),
      corner: requireEl('picturePane-corner'),
    };
    this.bind();
    this.o.grid.addEventListener('scroll', () => this.schedule(), { passive: true });
    new ResizeObserver(() => this.schedule()).observe(this.o.grid);
  }

  reset(): void {
    ++this.serial;
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    this.items = [];
    for (const node of this.nodes.values()) node.remove();
    this.nodes.clear();
    this.root.hidden = true;
    this.close();
    this.active = -1;
    this.initialRects.clear();
    requireButton('imagesBtn').hidden = true;
  }

  async load(sheet: string, epoch: number): Promise<void> {
    this.reset();
    const serial = this.serial;
    const data = await this.o.api(
      `/api/images?sheet=${encodeURIComponent(sheet)}`,
      undefined,
      decodeImagesApiResponse,
    );
    if (serial !== this.serial || this.o.state.sheetEpoch !== epoch || this.o.state.sheet !== sheet) {
      return;
    }
    this.items = data.images ? [...data.images] : [];
    this.root.hidden = this.items.length === 0;
    const btn = requireButton('imagesBtn');
    btn.hidden = this.items.length === 0;
    btn.innerHTML =
      '<svg class="icon" aria-hidden="true" focusable="false"><use href="#i-image"/></svg>图片<span class="image-count"></span>';
    const countEl = btn.querySelector('.image-count');
    if (countEl) countEl.textContent = String(this.items.length);
    btn.setAttribute('aria-pressed', String(this.enabled));
    btn.title = `工作表共有 ${this.items.length} 张图片；单击此按钮显示或隐藏图片`;
    this.schedule();
  }

  schedule(): void {
    if (this.frame) return;
    this.frame = requestAnimationFrame(() => {
      this.frame = 0;
      this.render();
    });
  }

  private anchorPoint(m: ImageMarker): { readonly x: number; readonly y: number } {
    const s = this.o.scale();
    return {
      x: this.o.colLeft(m.col + 1) + (m.colOff || 0) * s,
      y: this.o.rowTop(m.row + 1) + (m.rowOff || 0) * s,
    };
  }

  private rect(im: ImageDTO): Rect {
    const s = this.o.scale();
    let x: number;
    let y: number;
    let w: number;
    let h: number;
    if (im.kind === 'absolute') {
      x = (Number(im.x) || 0) * s;
      y = (Number(im.y) || 0) * s;
      w = (Number(im.width) || 0) * s;
      h = (Number(im.height) || 0) * s;
    } else {
      const start = this.anchorPoint(im.from);
      x = start.x;
      y = start.y;
      if (im.kind === 'twoCell' && im.to) {
        const end = this.anchorPoint(im.to);
        const frozen = this.initialRects.get(im.id);
        if (im.editAs === 'absolute') {
          const baseline = frozen ?? {
            x: im.hasTransform ? Number(im.x) || 0 : x / s,
            y: im.hasTransform ? Number(im.y) || 0 : y / s,
            w: Number(im.width) || (end.x - x) / s,
            h: Number(im.height) || (end.y - y) / s,
          };
          if (!frozen) this.initialRects.set(im.id, baseline);
          x = (baseline.x ?? 0) * s;
          y = (baseline.y ?? 0) * s;
          w = baseline.w * s;
          h = baseline.h * s;
        } else if (im.editAs === 'oneCell') {
          const baseline = frozen ?? {
            w: Number(im.width) || (end.x - x) / s,
            h: Number(im.height) || (end.y - y) / s,
          };
          if (!frozen) this.initialRects.set(im.id, baseline);
          w = baseline.w * s;
          h = baseline.h * s;
        } else {
          w = end.x - x;
          h = end.y - y;
        }
      } else {
        w = (Number(im.width) || 0) * s;
        h = (Number(im.height) || 0) * s;
      }
    }
    return { x: x + this.o.rowHead, y: y + this.o.headerHeight(), w, h };
  }

  private paneGeometry(): Record<PaneName, PaneBox> {
    const grid = this.o.grid;
    const pin = this.o.frozen();
    const hh = this.o.headerHeight();
    const lh = this.o.rowHead;
    const left = lh + this.o.colLeft(pin.cols + 1);
    const top = hh + this.o.rowTop(pin.rows + 1);
    const width = grid.clientWidth;
    const height = grid.clientHeight;
    return {
      normal: {
        x: pin.cols ? left : lh,
        y: pin.rows ? top : hh,
        w: width - (pin.cols ? left : lh),
        h: height - (pin.rows ? top : hh),
        dx: -grid.scrollLeft,
        dy: -grid.scrollTop,
      },
      top: {
        x: pin.cols ? left : lh,
        y: hh,
        w: width - (pin.cols ? left : lh),
        h: top - hh,
        dx: -grid.scrollLeft,
        dy: 0,
      },
      left: {
        x: lh,
        y: pin.rows ? top : hh,
        w: left - lh,
        h: height - (pin.rows ? top : hh),
        dx: 0,
        dy: -grid.scrollTop,
      },
      corner: { x: lh, y: hh, w: left - lh, h: top - hh, dx: 0, dy: 0 },
    };
  }

  render(): void {
    if (this.root.hidden || !this.enabled || !this.o.state.indexReady) return;
    if (this.o.state.filtered) {
      for (const n of this.nodes.values()) n.remove();
      this.nodes.clear();
      return;
    }
    const panes = this.paneGeometry();
    const pin = this.o.frozen();
    const required = new Set<string>();
    for (const name of PANE_NAMES) {
      const el = this.panes[name];
      const p = panes[name];
      el.style.left = fmtPx(p.x);
      el.style.top = fmtPx(p.y);
      el.style.width = fmtPx(Math.max(0, p.w));
      el.style.height = fmtPx(Math.max(0, p.h));
    }
    const usePane = (im: ImageDTO): PaneName => {
      const frozenRow = pin.rows > 0 && im.kind !== 'absolute' && im.from.row < pin.rows;
      const frozenCol = pin.cols > 0 && im.kind !== 'absolute' && im.from.col < pin.cols;
      if (frozenRow && frozenCol) return 'corner';
      if (frozenRow) return 'top';
      if (frozenCol) return 'left';
      return 'normal';
    };

    for (const im of this.items) {
      const rect = this.rect(im);
      if (!validRect(rect)) continue;
      const name = usePane(im);
      const p = panes[name];
      if (p.w <= 0 || p.h <= 0) continue;
      const x = rect.x + p.dx;
      const y = rect.y + p.dy;
      if (x + rect.w < p.x - 60 || x > p.x + p.w + 60 || y + rect.h < p.y - 60 || y > p.y + p.h + 60) {
        continue;
      }
      const key = String(im.id);
      required.add(key);
      let node = this.nodes.get(key);
      if (!node) {
        node = document.createElement('button');
        node.type = 'button';
        node.className = 'worksheet-picture';
        node.setAttribute('aria-label', `查看图片：${im.description || im.name || `图片 ${im.id + 1}`}`);
        const img = document.createElement('img');
        img.src = im.src;
        img.alt = im.description || im.name || '';
        img.loading = 'lazy';
        img.decoding = 'async';
        img.draggable = false;
        const crop = im.crop ?? {};
        const l = clamp(Number(crop.left) || 0, 0, 0.95);
        const r = clamp(Number(crop.right) || 0, 0, 0.95);
        const t = clamp(Number(crop.top) || 0, 0, 0.95);
        const b = clamp(Number(crop.bottom) || 0, 0, 0.95);
        const cw = Math.max(0.02, 1 - l - r);
        const ch = Math.max(0.02, 1 - t - b);
        img.style.left = `${(-l / cw) * 100}%`;
        img.style.top = `${(-t / ch) * 100}%`;
        img.style.width = `${100 / cw}%`;
        img.style.height = `${100 / ch}%`;
        node.style.transform = `rotate(${Number(im.rotation) || 0}deg) scale(${im.flipH ? -1 : 1},${im.flipV ? -1 : 1})`;
        const buttonNode = node;
        img.addEventListener('error', () => buttonNode.classList.add('picture-load-failed'));
        node.appendChild(img);
        node.dataset.id = key;
        node.addEventListener('click', (e: MouseEvent) => {
          e.stopPropagation();
          this.open(im.id);
        });
        this.nodes.set(key, node);
      }
      if (node.parentElement !== this.panes[name]) this.panes[name].appendChild(node);
      const style = node.style;
      const px = x - p.x;
      const py = y - p.y;
      const leftVal = fmtPx(px);
      const topVal = fmtPx(py);
      const widthVal = fmtPx(rect.w);
      const heightVal = fmtPx(rect.h);
      if (style.left !== leftVal) style.left = leftVal;
      if (style.top !== topVal) style.top = topVal;
      if (style.width !== widthVal) style.width = widthVal;
      if (style.height !== heightVal) style.height = heightVal;
    }
    for (const [key, node] of this.nodes) {
      if (!required.has(key)) {
        node.remove();
        this.nodes.delete(key);
      }
    }
  }

  toggle(): void {
    const first = this.items[0];
    if (this.o.state.filtered && first) {
      this.open(first.id);
      return;
    }
    this.enabled = !this.enabled;
    const btn = requireButton('imagesBtn');
    btn.setAttribute('aria-pressed', String(this.enabled));
    if (!this.enabled) {
      for (const node of this.nodes.values()) node.remove();
      this.nodes.clear();
    } else {
      this.schedule();
    }
    btn.title = this.enabled ? '隐藏工作表图片' : '显示工作表图片';
  }

  open(id: number): void {
    const index = this.items.findIndex((im) => im.id === id);
    const im = this.items[index];
    if (index < 0 || !im) return;
    if (this.viewer.hidden) {
      this.focusBeforeViewer =
        document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    this.active = index;
    this.panX = 0;
    this.panY = 0;
    this.rotation = Number(im.rotation) || 0;
    this.flipH = Boolean(im.flipH);
    this.flipV = Boolean(im.flipV);
    this.preview.onload = (): void => {
      if (!this.viewer.hidden && this.items[this.active] === im) {
        this.natural = { w: this.preview.naturalWidth || 1, h: this.preview.naturalHeight || 1 };
        this.fit();
      }
    };
    this.preview.onerror = (): void => {
      requireEl('pictureCaption').textContent = '该图片格式无法预览';
    };
    this.preview.alt = im.description || im.name || `图片 ${index + 1}`;
    this.preview.src = im.src;
    this.viewer.hidden = false;
    this.viewer.focus({ preventScroll: true });
    this.setCaption();
    if (this.preview.complete && this.preview.naturalWidth) {
      this.natural = { w: this.preview.naturalWidth, h: this.preview.naturalHeight };
      this.fit();
    }
  }

  close(): void {
    const wasOpen = !this.viewer.hidden;
    this.viewer.hidden = true;
    this.preview.removeAttribute('src');
    if (wasOpen && this.focusBeforeViewer?.isConnected) {
      this.focusBeforeViewer.focus({ preventScroll: true });
    }
    this.focusBeforeViewer = null;
  }

  private setCaption(): void {
    const im = this.items[this.active];
    if (!im) return;
    requireEl('pictureCaption').textContent = im.description || im.name || `图片 ${this.active + 1}`;
    requireEl('pictureIndex').textContent = `${this.active + 1} / ${this.items.length}`;
    requireButton('picturePrevious').disabled = this.items.length < 2;
    requireButton('pictureNext').disabled = this.items.length < 2;
    const download = requireAnchor('pictureDownload');
    download.href = `${im.src}&download=1`;
    download.download = '';
  }

  private select(delta: number): void {
    if (!this.items.length) return;
    const next = this.items[(this.active + delta + this.items.length) % this.items.length];
    if (next) this.open(next.id);
  }

  private fit(): void {
    if (this.viewer.hidden) return;
    const r = this.stage.getBoundingClientRect();
    const angle = (this.rotation * Math.PI) / 180;
    const cos = Math.abs(Math.cos(angle));
    const sin = Math.abs(Math.sin(angle));
    const width = this.natural.w * cos + this.natural.h * sin;
    const height = this.natural.w * sin + this.natural.h * cos;
    this.zoom = clamp(Math.min(1, (r.width - 56) / width, (r.height - 56) / height), 0.01, 8);
    this.fitToWindow = true;
    this.panX = 0;
    this.panY = 0;
    this.paintViewer();
  }

  private actual(): void {
    this.fitToWindow = false;
    this.zoom = 1;
    this.panX = 0;
    this.panY = 0;
    this.paintViewer();
  }

  private changeZoom(value: number, point?: { readonly x: number; readonly y: number }): void {
    const next = clamp(value, 0.05, 8);
    const prev = this.zoom;
    if (next === prev) return;
    const bounds = this.stage.getBoundingClientRect();
    const x = (point?.x ?? bounds.left + bounds.width / 2) - bounds.left - bounds.width / 2;
    const y = (point?.y ?? bounds.top + bounds.height / 2) - bounds.top - bounds.height / 2;
    this.panX = x - (x - this.panX) * (next / prev);
    this.panY = y - (y - this.panY) * (next / prev);
    this.fitToWindow = false;
    this.zoom = next;
    this.paintViewer();
  }

  private paintViewer(): void {
    this.preview.style.width = fmtPx(this.natural.w);
    this.preview.style.height = fmtPx(this.natural.h);
    this.preview.style.transform = `translate(calc(-50% + ${this.panX}px),calc(-50% + ${this.panY}px)) rotate(${this.rotation}deg) scale(${this.flipH ? -this.zoom : this.zoom},${this.flipV ? -this.zoom : this.zoom})`;
    requireEl('pictureZoomValue').textContent = `${Math.round(this.zoom * 100)}%`;
  }

  private async copy(): Promise<void> {
    const im = this.items[this.active];
    if (!im) return;
    try {
      const blob = await (await fetch(im.src)).blob();
      if (typeof navigator.clipboard.write !== 'function' || typeof ClipboardItem === 'undefined') {
        throw new Error('当前环境不支持复制图片');
      }
      let png = blob;
      if (blob.type !== 'image/png') {
        const bitmap = await createImageBitmap(blob);
        const canvas = document.createElement('canvas');
        canvas.width = bitmap.width;
        canvas.height = bitmap.height;
        const ctx = canvas.getContext('2d');
        if (!ctx) throw new Error('图片转换失败');
        ctx.drawImage(bitmap, 0, 0);
        bitmap.close();
        png = await new Promise<Blob>((resolve, reject) =>
          canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('图片转换失败'))), 'image/png'),
        );
      }
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': png })]);
      this.o.toast('图片已复制', 'success');
    } catch (err: unknown) {
      this.o.toast(`复制图片失败：${errorMessage(err)}`, 'error');
    }
  }

  private bind(): void {
    requireButton('imagesBtn').addEventListener('click', () => this.toggle());
    requireButton('pictureClose').addEventListener('click', () => this.close());
    this.viewer.addEventListener('pointerdown', (e: PointerEvent) => {
      if (e.target === this.viewer) this.close();
    });
    requireButton('pictureFit').addEventListener('click', () => this.fit());
    requireButton('pictureActual').addEventListener('click', () => this.actual());
    requireButton('pictureZoomIn').addEventListener('click', () => this.changeZoom(this.zoom * 1.25));
    requireButton('pictureZoomOut').addEventListener('click', () => this.changeZoom(this.zoom / 1.25));
    requireButton('pictureRotate').addEventListener('click', () => {
      this.rotation = (this.rotation + 90) % 360;
      this.fit();
    });
    requireButton('picturePrevious').addEventListener('click', () => this.select(-1));
    requireButton('pictureNext').addEventListener('click', () => this.select(1));
    requireButton('pictureCopy').addEventListener('click', () => {
      void this.copy();
    });
    this.stage.addEventListener(
      'wheel',
      (e: WheelEvent) => {
        e.preventDefault();
        this.changeZoom(this.zoom * Math.exp(-e.deltaY * 0.0015), { x: e.clientX, y: e.clientY });
      },
      { passive: false },
    );

    let drag: { id: number; x: number; y: number; panX: number; panY: number } | null = null;
    this.stage.addEventListener('pointerdown', (e: PointerEvent) => {
      if (e.button !== 0 || this.viewer.hidden) return;
      drag = { id: e.pointerId, x: e.clientX, y: e.clientY, panX: this.panX, panY: this.panY };
      this.stage.setPointerCapture(e.pointerId);
      this.stage.classList.add('panning');
      e.preventDefault();
    });
    this.stage.addEventListener('pointermove', (e: PointerEvent) => {
      if (drag?.id !== e.pointerId) return;
      this.panX = drag.panX + e.clientX - drag.x;
      this.panY = drag.panY + e.clientY - drag.y;
      this.paintViewer();
    });
    const stop = (e: PointerEvent): void => {
      if (drag?.id === e.pointerId) {
        drag = null;
        this.stage.classList.remove('panning');
      }
    };
    this.stage.addEventListener('pointerup', stop);
    this.stage.addEventListener('pointercancel', stop);
    this.stage.addEventListener('dblclick', () => {
      if (Math.abs(this.zoom - 1) < 0.01) this.fit();
      else this.actual();
    });

    document.addEventListener(
      'keydown',
      (e: KeyboardEvent) => {
        if (this.viewer.hidden) return;
        if (e.key === 'Tab') {
          const controls = [
            ...this.viewer.querySelectorAll<HTMLElement>('button:not(:disabled),a[href]'),
          ];
          const first = controls[0];
          const last = controls[controls.length - 1];
          if (first && last) {
            if (e.shiftKey && (document.activeElement === first || document.activeElement === this.viewer)) {
              e.preventDefault();
              last.focus();
            } else if (
              !e.shiftKey &&
              (document.activeElement === last || document.activeElement === this.viewer)
            ) {
              e.preventDefault();
              first.focus();
            }
          }
          return;
        }
        if (e.key === 'Escape') {
          e.preventDefault();
          e.stopImmediatePropagation();
          this.close();
          return;
        }
        const tag = document.activeElement?.tagName ?? '';
        if (['INPUT', 'TEXTAREA', 'SELECT'].includes(tag)) return;
        let done = true;
        switch (e.key) {
          case '+':
          case '=':
            this.changeZoom(this.zoom * 1.25);
            break;
          case '-':
            this.changeZoom(this.zoom / 1.25);
            break;
          case '0':
            this.fit();
            break;
          case '1':
            this.actual();
            break;
          case 'ArrowLeft':
            this.select(-1);
            break;
          case 'ArrowRight':
            this.select(1);
            break;
          case 'r':
          case 'R':
            this.rotation = (this.rotation + 90) % 360;
            this.fit();
            break;
          default:
            done = false;
        }
        if (done) {
          e.preventDefault();
          e.stopImmediatePropagation();
        }
      },
      true,
    );

    new ResizeObserver(() => {
      if (!this.viewer.hidden) {
        if (this.fitToWindow) this.fit();
        else this.paintViewer();
      }
    }).observe(this.stage);
  }
}
