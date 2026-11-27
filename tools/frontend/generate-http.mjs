// Generate runtime response checks from the same declarations that type the UI.
// The decoded type comes from the guard, including each nested object and union.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const root = fileURLToPath(new URL('../../', import.meta.url));
const output = path.join(root, 'internal/ui/src/core/http-decoders.generated.ts');
const config = ts.readConfigFile(path.join(root, 'tsconfig.app.json'), ts.sys.readFile);
if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, '\n'));
const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root);
const program = ts.createProgram(parsed.fileNames, parsed.options);
const checker = program.getTypeChecker();
const responses = new Map();
const identifier = /^[A-Za-z_$][\w$]*$/u;

for (const file of program.getSourceFiles()) {
  if (file.isDeclarationFile) continue;
  for (const declaration of file.statements) {
    const marker = ts.getJSDocTags(declaration).find(tag => tag.tagName.text === 'httpResponse');
    if (!marker) continue;
    const name = typeof marker.comment === 'string' ? marker.comment.trim() : declaration.name?.text;
    if (!name || !identifier.test(name) || responses.has(name)) {
      throw new Error('Invalid or duplicate HTTP response name in ' + file.fileName + ': ' + name);
    }
    responses.set(name, checker.getTypeAtLocation(declaration));
  }
}
if (!responses.size) throw new Error('No @httpResponse declarations found');

const helpers = new Set(['decoder']);
const names = new Map();
const taken = new Set();
const active = new Set();
const declarations = [];

function helper(name) {
  helpers.add(name);
  return name;
}

// Mark factory calls individually so the recovery page only includes its own decoders.
function call(name, ...args) {
  return '/* @__PURE__ */ ' + helper(name) + '(' + args.join(', ') + ')';
}

function unionGuard(types, origin) {
  const literals = [];
  const alternatives = new Set();
  for (const item of types) {
    if (item.flags & ts.TypeFlags.Undefined) continue;
    if (item.isLiteral()) literals.push(item.value);
    else if (item.flags & ts.TypeFlags.BooleanLiteral) literals.push(item.intrinsicName === 'true');
    else alternatives.add(guard(item, origin));
  }
  if (literals.length === 2 && literals.includes(true) && literals.includes(false)) {
    alternatives.add(helper('bool'));
  } else if (literals.length) {
    alternatives.add(call('literals', ...literals.map(value => JSON.stringify(value))));
  }
  if (!alternatives.size) throw new Error('Empty wire union at ' + origin);
  return alternatives.size === 1 ? [...alternatives][0] : call('oneOf', ...alternatives);
}

function fields(entries) {
  return entries.length ? '{\n  ' + entries.join(',\n  ') + ',\n}' : '{}';
}

function objectGuard(type, origin) {
  if (names.has(type)) return names.get(type);
  if (active.has(type)) throw new Error('Recursive wire type at ' + origin);
  active.add(type);

  const required = [];
  const optional = [];
  for (const property of type.getProperties()) {
    const location = property.valueDeclaration ?? property.declarations?.[0] ?? type.symbol?.declarations?.[0];
    if (!location) throw new Error('Missing declaration for ' + origin + '.' + property.name);
    const propertyType = checker.getTypeOfSymbolAtLocation(property, location);
    const key = identifier.test(property.name) ? property.name : JSON.stringify(property.name);
    const bucket = property.flags & ts.SymbolFlags.Optional ? optional : required;
    bucket.push(key + ': ' + guard(propertyType, origin + '.' + property.name));
  }
  let expression = call('object', fields(required), fields(optional));
  const index = checker.getIndexTypeOfType(type, ts.IndexKind.String) ?? checker.getIndexTypeOfType(type, ts.IndexKind.Number);
  if (index) {
    const dictionary = call('dictionary', guard(index, origin + '[*]'));
    expression = required.length || optional.length ? call('both', expression, dictionary) : dictionary;
  }

  const symbol = type.aliasSymbol?.name ?? type.symbol?.name;
  const base = 'is' + (symbol && /^[A-Z]/u.test(symbol) ? symbol : origin.replace(/[^\w]/gu, '_'));
  let name = base;
  for (let suffix = 2; taken.has(name); suffix++) name = base + suffix;
  taken.add(name);
  names.set(type, name);
  active.delete(type);
  declarations.push('const ' + name + ' = ' + expression + ';');
  return name;
}

function guard(type, origin) {
  if (type.aliasSymbol?.name === 'JsonValue') return helper('jsonValue');
  const flags = type.flags;
  if (flags & ts.TypeFlags.String) return helper('text');
  if (flags & ts.TypeFlags.Number) return helper('numeric');
  if (flags & ts.TypeFlags.Boolean) return helper('bool');
  if (flags & ts.TypeFlags.Null) return helper('nil');
  if (flags & ts.TypeFlags.Unknown) return helper('unknownValue');
  if (type.isUnion()) return unionGuard(type.types, origin);
  if (type.isLiteral() || flags & ts.TypeFlags.BooleanLiteral) return unionGuard([type], origin);
  if (checker.isArrayType(type)) return call('array', guard(checker.getTypeArguments(type)[0], origin + '[]'));
  if (checker.isTupleType(type) || type.getCallSignatures().length || !(flags & (ts.TypeFlags.Object | ts.TypeFlags.Intersection))) {
    throw new Error('Unsupported wire type ' + checker.typeToString(type) + ' at ' + origin);
  }
  return objectGuard(type, origin);
}

const exports = [...responses.keys()].sort().map(name => {
  const check = call('decoder', JSON.stringify(name), guard(responses.get(name), name));
  return 'export const decode' + name + ' = ' + check + ';';
});
const result = [
  '// Generated by tools/frontend/generate-http.mjs. Edit the @httpResponse source types.',
  "import { " + [...helpers].sort().join(', ') + " } from './json-contract.ts';",
  '', ...declarations, '', ...exports, '',
].join('\n');

if (process.argv.includes('--check')) {
  if (!fs.existsSync(output) || fs.readFileSync(output, 'utf8').replaceAll('\r\n', '\n') !== result) {
    throw new Error('Stale HTTP decoders. Run npm run generate:http.');
  }
} else {
  fs.writeFileSync(output, result);
}
console.log('Verified ' + responses.size + ' HTTP response decoders (' + declarations.length + ' nested shapes).');
