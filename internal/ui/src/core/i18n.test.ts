import assert from 'node:assert/strict';
import test from 'node:test';
import { initializeLocalization, interfaceLocale, t, th } from './i18n.ts';

test('translations interpolate named arguments and escape HTML without re-interpolating source values', () => {
  initializeLocalization('ru', { 'ui.workspace.restoreConfirmationPrompt': '«{word}»' });
  assert.equal(interfaceLocale(), 'ru');
  assert.equal(t('ui.workspace.restoreConfirmationPrompt', { word: '<& {word}' }), '«<& {word}»');
  assert.equal(th('ui.workspace.restoreConfirmationPrompt', { word: '<& {word}' }), '«&lt;&amp; {word}»');
});
