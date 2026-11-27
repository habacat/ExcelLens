import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const srcRoot = path.join(root, 'internal/ui/src');

function collectTests(dir, out) {
  if (!fs.existsSync(dir)) return;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      collectTests(full, out);
    } else if (entry.name.endsWith('.test.ts')) {
      out.push(path.relative(root, full).split(path.sep).join('/'));
    }
  }
}

const tests = [];
collectTests(srcRoot, tests);
if (tests.length === 0) {
  console.error('FAIL: no TypeScript unit tests found under internal/ui/src');
  process.exit(1);
}

const result = spawnSync(process.execPath, ['--test', '--test-reporter=spec', ...tests], {
  cwd: root,
  stdio: 'inherit',
});
process.exit(result.status ?? 1);
