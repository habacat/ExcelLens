import { escapeHtml } from './html.ts';
import type { MessageArguments, MessageKey } from './messages.generated.ts';

type Messages = Readonly<Record<string, string>>;

interface Localization {
  readonly locale: string;
  readonly messages: Messages;
}

const fallbackLocale = 'zh-Hans';
let active: Localization | undefined;

export function parseMessageCatalog(text: string): Messages {
  const input: unknown = JSON.parse(text);
  if (typeof input !== 'object' || input === null || Array.isArray(input)) {
    throw new Error('Invalid interface message catalog');
  }
  const catalog = input as Record<string, unknown>;
  for (const key of Object.keys(catalog)) {
    if (typeof catalog[key] !== 'string') throw new Error('Invalid interface message ' + key);
  }
  return catalog as Messages;
}

function readLocalization(): Localization {
  const locale = document.documentElement.lang || fallbackLocale;
  const source = document.getElementById('languageMessages')?.textContent;
  const messages = source ? parseMessageCatalog(source) : {};
  return { locale, messages };
}

function localization(): Localization {
  return (active ??= readLocalization());
}

export function initializeLocalization(locale: string, messages: Messages): void {
  active = { locale, messages };
}

export function interfaceLocale(): string {
  return localization().locale;
}

export const languageHeader = 'X-ExcelLens-Language';

function translate(key: MessageKey, values?: Readonly<Record<string, unknown>>): string {
  const { messages } = localization();
  const message = Object.hasOwn(messages, key) ? (messages[key] ?? key) : key;
  if (!values) return message;
  return message.replace(/\{([a-zA-Z][a-zA-Z0-9]*)\}/g, (token, name: string) =>
    Object.hasOwn(values, name) ? String(values[name]) : token,
  );
}

export function t<const K extends MessageKey>(key: K, ...args: MessageArguments[K]): string {
  return translate(key, args[0]);
}

export function th<const K extends MessageKey>(key: K, ...args: MessageArguments[K]): string {
  return escapeHtml(translate(key, args[0]));
}
