import { test } from 'node:test';
import assert from 'node:assert/strict';
import { searchKey } from './sheet-controller.ts';

test('advanced regex caches preserve syntax case while ordinary search remains case-insensitive', () => {
  assert.notEqual(
    searchKey('advanced', 'current', 'A regex "\\D"', 'Data'),
    searchKey('advanced', 'current', 'A regex "\\d"', 'Data'),
  );
  assert.notEqual(
    searchKey('advanced', 'all', 'A regex "ABC"', 'Data'),
    searchKey('advanced', 'all', 'A regex "abc"', 'Other'),
  );
  assert.equal(
    searchKey('formula', 'current', ' SUM(A1) ', 'Data'),
    searchKey('formula', 'current', 'sum(a1)', 'Data'),
  );
  assert.equal(
    searchKey('value', 'all', 'Hello', 'Data'),
    searchKey('value', 'all', 'hello', 'Other'),
  );
});
