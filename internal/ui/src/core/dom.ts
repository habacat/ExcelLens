/**
 * Typed DOM lookup helpers eliminating unchecked casts and non-null assertions.
 */

export function byId(id: string): HTMLElement | null {
  return document.getElementById(id);
}

export function requireEl(id: string): HTMLElement {
  const node = document.getElementById(id);
  if (!node) {
    throw new Error(`Missing required DOM element: #${id}`);
  }
  return node;
}

export function requireButton(id: string): HTMLButtonElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLButtonElement)) {
    throw new Error(`Expected HTMLButtonElement for #${id}`);
  }
  return node;
}

export function requireInput(id: string): HTMLInputElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLInputElement)) {
    throw new Error(`Expected HTMLInputElement for #${id}`);
  }
  return node;
}

export function requireSelect(id: string): HTMLSelectElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLSelectElement)) {
    throw new Error(`Expected HTMLSelectElement for #${id}`);
  }
  return node;
}

export function requireTextArea(id: string): HTMLTextAreaElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLTextAreaElement)) {
    throw new Error(`Expected HTMLTextAreaElement for #${id}`);
  }
  return node;
}

export function requireDialog(id: string): HTMLDialogElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLDialogElement)) {
    throw new Error(`Expected HTMLDialogElement for #${id}`);
  }
  return node;
}

export function requireAnchor(id: string): HTMLAnchorElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLAnchorElement)) {
    throw new Error(`Expected HTMLAnchorElement for #${id}`);
  }
  return node;
}

export function queryInput(id: string): HTMLInputElement | null {
  const node = document.getElementById(id);
  return node instanceof HTMLInputElement ? node : null;
}

export function queryButton(id: string): HTMLButtonElement | null {
  const node = document.getElementById(id);
  return node instanceof HTMLButtonElement ? node : null;
}

export function querySelect(id: string): HTMLSelectElement | null {
  const node = document.getElementById(id);
  return node instanceof HTMLSelectElement ? node : null;
}

export function queryDialog(id: string): HTMLDialogElement | null {
  const node = document.getElementById(id);
  return node instanceof HTMLDialogElement ? node : null;
}

export function readAppToken(): string {
  const meta = document.querySelector('meta[name="app-token"]');
  return meta instanceof HTMLMetaElement ? meta.content : '';
}

export function readAppVersion(): string {
  const meta = document.querySelector('meta[name="app-version"]');
  return meta instanceof HTMLMetaElement ? meta.content : '';
}
