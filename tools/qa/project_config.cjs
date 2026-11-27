// Optional QA harnesses use the production bootstrap and maintained version.
// No private data, browser state, or second format registry is read here.
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const {execFileSync} = require('node:child_process');
const root = path.resolve(__dirname, '../..');

exports.version = () => {
  const source = fs.readFileSync(path.join(root, 'internal/buildinfo/version.go'), 'utf8');
  const match = source.match(/const Version = "(\d+\.\d+\.\d+)"/);
  if (!match) throw Error('Application version is missing');
  return match[1];
};

// Development work can keep its receipts separate from the last released
// version while still asserting the real bootstrap version at runtime.
exports.validationVersion = () => {
  const value=process.env.QA_OUTPUT_VERSION||exports.version();
  if(!/^\d+\.\d+\.\d+$/.test(value))throw Error('QA_OUTPUT_VERSION must be a numeric version');
  return value;
};

exports.index = () => {
  const renderedPage = execFileSync('go', ['run', './tools/project', 'render-index'], {
    cwd: root, encoding: 'utf8', windowsHide: true,
    env: {...process.env, GOTOOLCHAIN:'local', GOPROXY:'off', GOSUMDB:'off', GOFLAGS:'-mod=vendor', CGO_ENABLED:'0'}
  });
  return renderedPage.replace(/(\/assets\/[\w.-]+)\?v=[a-f0-9]{64}(?=")/g, '$1');
};

exports.verifyAssets = async (address, ...styles) => {
  const digests = {};
  for (const name of ['app.js', ...styles]) {
    const response = await fetch(new URL('/assets/' + name, address));
    if (!response.ok) throw Error('Missing embedded asset: ' + name);
    const actual = createHash('sha256').update(Buffer.from(await response.arrayBuffer())).digest('hex');
    const local = path.join(root, 'internal/ui/web', name === 'app.js' ? 'dist' : '', name);
    const expected = createHash('sha256').update(fs.readFileSync(local)).digest('hex');
    if (actual !== expected) throw Error('Stale embedded asset: ' + name);
    digests[name] = actual;
  }
  return digests;
};
