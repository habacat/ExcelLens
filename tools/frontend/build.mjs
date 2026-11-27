import { build } from 'esbuild';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const checkOnly = process.argv.includes('--check');

const entryPoints = [
  {
    in: path.join(root, 'internal/ui/src/app/main.ts'),
    out: path.join(root, 'internal/ui/web/dist/app.js'),
  },
  {
    in: path.join(root, 'internal/ui/src/app/storage-recovery.ts'),
    out: path.join(root, 'internal/ui/web/dist/storage-recovery.js'),
  },
];

const banner = '/* ExcelLens compiled TypeScript frontend bundle. Source: internal/ui/src */';

async function run() {
  let stale = false;
  for (const entry of entryPoints) {
    if (!fs.existsSync(entry.in)) {
      throw new Error(`Missing frontend entrypoint: ${path.relative(root, entry.in)}`);
    }
    const result = await build({
      entryPoints: [entry.in],
      bundle: true,
      write: false,
      format: 'iife',
      platform: 'browser',
      target: ['es2022', 'chrome112', 'edge112', 'firefox115', 'safari16.4'],
      charset: 'utf8',
      minify: true,
      legalComments: 'none',
      banner: { js: banner },
      logLevel: 'silent',
    });
    const outputFile = result.outputFiles[0];
    if (!outputFile) {
      throw new Error(`esbuild produced no output for ${path.relative(root, entry.in)}`);
    }
    const normalized = outputFile.text.replace(/\r\n/g, '\n');
    const bytes = Buffer.from(normalized.endsWith('\n') ? normalized : `${normalized}\n`, 'utf8');
    if (checkOnly) {
      if (!fs.existsSync(entry.out)) {
        console.error(`FAIL: missing built asset ${path.relative(root, entry.out)}`);
        stale = true;
        continue;
      }
      const existing = fs.readFileSync(entry.out);
      if (!existing.equals(bytes)) {
        console.error(`FAIL: built asset ${path.relative(root, entry.out)} is stale; run "npm run build:ui"`);
        stale = true;
      }
    } else {
      fs.mkdirSync(path.dirname(entry.out), { recursive: true });
      fs.writeFileSync(entry.out, bytes);
      console.log(`Built ${path.relative(root, entry.out)} (${bytes.length} bytes)`);
    }
  }
  if (stale) {
    process.exitCode = 1;
  }
}

run().catch((err) => {
  console.error(err instanceof Error ? err.message : String(err));
  process.exitCode = 1;
});
