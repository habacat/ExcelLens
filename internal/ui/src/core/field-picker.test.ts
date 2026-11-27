import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import { FieldPicker } from './field-picker.ts';

function setup() {
  const dom = installDom();
  const picker = new FieldPicker({ id: 'testPicker', pageRows: 3 });
  const fields = Array.from({ length: 8 }, (_, i) => ({ column: i + 1, name: `字段${i + 1}` }));
  return {
    picker,
    dialog: picker.dialog,
    fields,
    get: (id: string) => dom.document.getElementById(id),
    restore: () => dom.restore(),
  };
}

test('paged multiselect retains off-page choices, enforces capacity and applies only on confirmation', () => {
  const h = setup();
  try {
    let chosen: readonly number[] | null = null;
    h.picker.open({
      fields: h.fields,
      multiple: true,
      selected: [1],
      limit: 3,
      onChoose: (x) => {
        chosen = x;
      },
    });
    assert.equal(h.get('testPickerItems')?.children.length, 3);
    h.picker.select(2);
    h.picker.page = 1;
    h.picker.render();
    h.picker.select(4);
    h.picker.select(5);
    assert.equal(h.picker.selected.has(5), false);
    assert.equal(chosen, null);
    h.picker.apply();
    assert.deepEqual(chosen, [1, 2, 4]);
    assert.equal(h.get('testPickerItems')?.children.length, 0);
  } finally {
    h.restore();
  }
});

test('cancel does not mutate input selection and stale field buttons cannot target a newer source', async () => {
  const h = setup();
  try {
    const selected = [1];
    let changes = 0;
    h.picker.open({
      fields: h.fields,
      multiple: true,
      selected,
      limit: 3,
      onChoose: () => {
        changes++;
      },
    });
    const stale = h.get('testPickerItems')?.children[0];
    h.picker.select(2);
    h.picker.close();
    await Promise.resolve();
    assert.deepEqual(selected, [1]);
    h.picker.open({
      fields: [{ column: 10, name: '新来源' }],
      onChoose: () => {
        changes++;
      },
    });
    stale?.click();
    assert.equal(changes, 0);
    assert.equal(h.picker.dialog.open, true);
  } finally {
    h.restore();
  }
});

test('repeated empty columns remain selectable while real duplicate source fields are disabled', () => {
  const h = setup();
  try {
    let chosen = -1;
    h.picker.open({
      fields: h.fields,
      allowEmpty: true,
      disabled: [0, 1],
      onChoose: (value) => {
        chosen = value;
      },
    });
    const items = h.get('testPickerItems')?.children ?? [];
    assert.equal(Reflect.get(items[0] ?? {}, 'disabled'), false);
    assert.equal(Reflect.get(items[1] ?? {}, 'disabled'), true);
    items[0]?.click();
    assert.equal(chosen, 0);
  } finally {
    h.restore();
  }
});

test('search is paged and per-page clearing preserves other selections', () => {
  const h = setup();
  try {
    h.picker.open({
      fields: h.fields,
      multiple: true,
      selected: [1, 7],
      limit: 8,
      onChoose: () => undefined,
    });
    const search = document.getElementById('testPickerSearch');
    assert.ok(search instanceof HTMLInputElement);
    search.value = '7';
    search.dispatchEvent(new Event('input'));
    assert.equal(h.get('testPickerItems')?.children.length, 1);
    const clear = h
      .get('testPickerTools')
      ?.children.find((item) => item.textContent === '清空本页');
    clear?.click();
    assert.equal(h.picker.selected.has(1), true);
    assert.equal(h.picker.selected.has(7), false);
  } finally {
    h.restore();
  }
});

test('queued native close does not clear an immediately reopened picker', async () => {
  const h = setup();
  try {
    h.picker.open({ fields: h.fields, onChoose: () => undefined });
    h.picker.close();
    h.picker.open({ fields: h.fields.slice(0, 2), onChoose: () => undefined });
    await Promise.resolve();
    assert.equal(h.picker.dialog.open, true);
    assert.equal(h.get('testPickerItems')?.children.length, 2);
  } finally {
    h.restore();
  }
});
