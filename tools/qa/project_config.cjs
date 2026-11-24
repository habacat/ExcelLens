// Optional QA harnesses use the production bootstrap and maintained version.
// No private data, browser state, or second format registry is read here.
const fs = require('node:fs');
const path = require('node:path');
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
  const page = renderedPage.replace(/(\/assets\/[\w.-]+)\?v=[a-f0-9]{64}(?=")/g, '$1');
  const formats = fs.readFileSync(path.join(root, 'internal/ui/web/formats.js'), 'utf8');
  // Legacy about:blank harnesses strip external script tags before installing
  // their API fixtures. This tiny independent adapter can run immediately.
  const display=fs.readFileSync(path.join(root,'internal/ui/web/display-format.js'),'utf8');
  return page.replace('<script src="/assets/formats.js"></script>', '<script>' + formats + '</script>').replace('<script src="/assets/display-format.js"></script>','<script>'+display+'</script>');
};
