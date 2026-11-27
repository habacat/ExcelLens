import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const srcRoot = path.join(root, 'internal/ui/src');
const webRoot = path.join(root, 'internal/ui/web');
const configPath = path.join(root, 'tsconfig.app.json');

const ALLOWED_LAYERS = {
  contracts: new Set(['contracts']),
  core: new Set(['contracts', 'core']),
  grid: new Set(['contracts', 'core', 'grid']),
  features: new Set(['contracts', 'core', 'grid', 'features']),
  editing: new Set(['contracts', 'core', 'grid', 'editing']),
  analysis: new Set(['contracts', 'core', 'grid', 'analysis']),
  app: new Set(['contracts', 'core', 'grid', 'features', 'editing', 'analysis', 'app']),
};

function toRel(filePath) {
  return path.relative(root, filePath).split(path.sep).join('/');
}

function layerOf(relPath) {
  const prefix = 'internal/ui/src/';
  if (!relPath.startsWith(prefix)) return null;
  const sub = relPath.slice(prefix.length);
  const slash = sub.indexOf('/');
  return slash < 0 ? null : sub.slice(0, slash);
}

function readProgram() {
  const readResult = ts.readConfigFile(configPath, ts.sys.readFile);
  if (readResult.error) {
    throw new Error(ts.flattenDiagnosticMessageText(readResult.error.messageText, '\n'));
  }
  const parsed = ts.parseJsonConfigFileContent(readResult.config, ts.sys, root);
  return ts.createProgram({ rootNames: parsed.fileNames, options: parsed.options });
}

function checkLegacyWebFiles(errors) {
  for (const entry of fs.readdirSync(webRoot, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (entry.name !== 'dist') {
        errors.push(`Unexpected directory in internal/ui/web: ${entry.name}`);
      }
      continue;
    }
    if (entry.name.endsWith('.js') || entry.name.endsWith('.cjs')) {
      errors.push(`Legacy script file must be removed from internal/ui/web: ${entry.name}`);
    }
  }
}

function main() {
  const errors = [];
  checkLegacyWebFiles(errors);

  const program = readProgram();
  const graph = new Map();

  for (const sourceFile of program.getSourceFiles()) {
    if (sourceFile.isDeclarationFile) continue;
    const abs = path.resolve(sourceFile.fileName);
    if (!abs.startsWith(srcRoot)) continue;
    const rel = toRel(abs);
    const layer = layerOf(rel);
    if (!layer || !Object.hasOwn(ALLOWED_LAYERS, layer)) {
      errors.push(`${rel}: file is outside registered layers (${Object.keys(ALLOWED_LAYERS).join(', ')})`);
      continue;
    }

    const text = sourceFile.getFullText();
    if (rel !== 'internal/ui/src/app/runtime-bridge.ts') {
      const globalMatch = /\b(?:window|globalThis|self)\.ExcelLens\w*/.exec(text);
      if (globalMatch) {
        errors.push(`${rel}: forbidden global namespace access "${globalMatch[0]}" (use ES module imports)`);
      }
    }
    const suppressMatch = /@ts-(?:ignore|expect-error|nocheck)|eslint-disable/.exec(text);
    if (suppressMatch) {
      errors.push(`${rel}: forbidden static-check suppression "${suppressMatch[0]}"`);
    }

    const deps = new Set();
    graph.set(rel, deps);

    const visit = (node) => {
      if (
        (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) &&
        node.moduleSpecifier &&
        ts.isStringLiteral(node.moduleSpecifier)
      ) {
        const spec = node.moduleSpecifier.text;
        if (!spec.startsWith('.')) {
          errors.push(`${rel}: non-relative import "${spec}" is not allowed in browser production code`);
        } else {
          const resolved = path.resolve(path.dirname(abs), spec);
          const targetRel = toRel(resolved);
          const targetLayer = layerOf(targetRel);
          if (!targetLayer || !ALLOWED_LAYERS[layer].has(targetLayer)) {
            errors.push(`${rel}: illegal cross-layer import "${rel}" (${layer}) -> "${targetRel}" (${targetLayer ?? 'unknown'})`);
          }
          if (layer === 'contracts') {
            const isTypeOnly =
              (ts.isImportDeclaration(node) && Boolean(node.importClause?.isTypeOnly)) ||
              (ts.isExportDeclaration(node) && Boolean(node.isTypeOnly));
            if (!isTypeOnly) {
              errors.push(`${rel}: contracts layer may only use type-only imports/exports ("${spec}")`);
            }
          }
          deps.add(targetRel);
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sourceFile);
  }

  // Detect any circular dependency (including type-level cycles for strict DAG discipline).
  const state = new Map(); // 0=unvisited, 1=visiting, 2=done
  const stack = [];
  const dfs = (node) => {
    state.set(node, 1);
    stack.push(node);
    for (const next of graph.get(node) ?? []) {
      const nextState = state.get(next) ?? 0;
      if (nextState === 1) {
        const idx = stack.indexOf(next);
        const cycle = [...stack.slice(idx), next].join(' -> ');
        errors.push(`Circular dependency detected: ${cycle}`);
      } else if (nextState === 0 && graph.has(next)) {
        dfs(next);
      }
    }
    stack.pop();
    state.set(node, 2);
  };
  for (const node of [...graph.keys()].sort()) {
    if ((state.get(node) ?? 0) === 0) {
      dfs(node);
    }
  }

  if (errors.length > 0) {
    for (const err of errors) {
      console.error(`ARCH FAIL: ${err}`);
    }
    process.exitCode = 1;
    return;
  }
  console.log(`PASS: frontend architecture check (${graph.size} production TS modules, 0 cycles, 0 layer violations)`);
}

main();
