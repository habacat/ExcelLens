/** Runtime checks used at the JSON boundary. Object checks allow additive server fields. */
import { t as localize } from './i18n.ts';
import type { JsonDecoder, JsonValue } from '../contracts/common.ts';

type Guard<T> = (value: unknown) => value is T;
type Value<G> = G extends Guard<infer T> ? T : never;
type Fields = Readonly<Record<string, Guard<unknown>>>;
type ObjectValue<R extends Fields, O extends Fields> =
  { [K in keyof R]: Value<R[K]> } & { [K in keyof O]?: Value<O[K]> };

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

const details = new WeakMap<Guard<unknown>, (value: unknown, path: string) => string>();
function failurePath(guard: Guard<unknown>, value: unknown, path: string): string {
  return details.get(guard)?.(value, path) ?? path;
}

export const text: Guard<string> = (value): value is string => typeof value === 'string';
export const numeric: Guard<number> = (value): value is number => typeof value === 'number' && Number.isFinite(value);
export const bool: Guard<boolean> = (value): value is boolean => typeof value === 'boolean';
export const nil: Guard<null> = (value): value is null => value === null;
export const unknownValue: Guard<unknown> = (_value): _value is unknown => true;
export const jsonValue: Guard<JsonValue> = (value): value is JsonValue =>
  value === null || text(value) || numeric(value) || bool(value) ||
  (Array.isArray(value) ? value.every(jsonValue) : record(value) && Object.values(value).every(jsonValue));

export function literals<const T extends readonly (string | number | boolean)[]>(...values: T): Guard<T[number]> {
  const allowed = new Set<unknown>(values);
  return (value): value is T[number] => allowed.has(value);
}

export function array<T>(item: Guard<T>): Guard<T[]> {
  const guard: Guard<T[]> = (value): value is T[] => Array.isArray(value) && value.every(item);
  details.set(guard, (value, path) => {
    if (!Array.isArray(value)) return path;
    const index = value.findIndex((entry: unknown) => !item(entry));
    return failurePath(item, value[index], `${path}[${index}]`);
  });
  return guard;
}

export function dictionary<T>(item: Guard<T>): Guard<Record<string, T>> {
  return (value): value is Record<string, T> => {
    if (!record(value)) return false;
    for (const key in value) if (!item(value[key])) return false;
    return true;
  };
}

export function oneOf<const G extends readonly Guard<unknown>[]>(...options: G): Guard<Value<G[number]>> {
  return (value): value is Value<G[number]> => options.some(test => test(value));
}

export function both<A,B>(a: Guard<A>, b: Guard<B>): Guard<A & B> {
  return (value): value is A & B => a(value) && b(value);
}

export function object<R extends Fields,O extends Fields>(required: R, optional: O): Guard<ObjectValue<R,O>> {
  const entries = Object.entries(required);
  // Iterate the optional fields actually sent. Wide cell models normally carry
  // only a handful. Required fields must be present even when typed as unknown.
  const guard = (value: unknown): value is ObjectValue<R,O> => {
    if (!record(value)) return false;
    for (const [key, test] of entries) {
      if (!Object.hasOwn(value,key) || !test(value[key])) return false;
    }
    for (const key in value) {
      if (Object.hasOwn(optional,key) && !optional[key]?.(value[key])) return false;
    }
    return true;
  };
  details.set(guard, (value, path) => {
    if (!record(value)) return path;
    for (const [key, test] of entries) {
      if (!Object.hasOwn(value,key) || !test(value[key])) return failurePath(test,value[key],path+'.'+key);
    }
    for (const [key, test] of Object.entries(optional)) {
      if (Object.hasOwn(value,key) && !test(value[key])) return failurePath(test,value[key],path+'.'+key);
    }
    return path;
  });
  return guard;
}

export function decoder<T>(name: string, matches: Guard<T>): JsonDecoder<T> {
  return value => {
    if (!matches(value)) throw new Error(localize('ui.jsonContract.dataReturnedLocalServiceIncorrect', { failurePath: failurePath(matches, value, name) }));
    return value;
  };
}
