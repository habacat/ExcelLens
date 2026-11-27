import { existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const [major, minor] = process.versions.node.split('.').map(Number);
if (!(major >= 24 || (major === 22 && minor >= 18))) {
  throw new Error('Frontend checks require Node.js 22.18 (22.x) or 24+.');
}
const tsc = 'node_modules/typescript/bin/tsc';
const eslint = 'node_modules/eslint/bin/eslint.js';
if (![tsc, eslint].every(bin => existsSync(new URL('../../' + bin, import.meta.url)))) {
  throw new Error('Frontend dependencies are missing. Run npm ci.');
}

for (const args of [
  [tsc, '-p', 'tsconfig.json', '--noEmit'],
  [tsc, '-p', 'tsconfig.app.json', '--noEmit'],
  [eslint, 'internal/ui/src', '--max-warnings=0'],
  ['tools/frontend/check-arch.mjs'],
  ['tools/frontend/build.mjs', '--check'],
  ['tools/frontend/run-tests.mjs'],
]) {
  const result = spawnSync(process.execPath, args, { cwd: root, stdio: 'inherit' });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}
