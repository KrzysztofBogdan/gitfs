// Prints the licences of the code inside the built bundle (ksef pdf spec
// §3.1), then Roboto's: the packages named by the bundle's source map
// (argv[2]) and by pdfmake's own prebuilt bundle, whose source map pdfmake
// ships. A package that is not installed (pdfmake's build-time polyfills)
// has its licence fetched from the npm registry with npm pack.
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

function packagesOf(mapFile) {
  const m = JSON.parse(fs.readFileSync(mapFile, 'utf8'));
  const out = new Set();
  for (const s of m.sources) {
    const re = /node_modules\/((?:@[^/]+\/)?[^/]+)/g;
    let x, last;
    while ((x = re.exec(s))) last = x[1];
    if (last) out.add(last);
  }
  return out;
}

function licenceIn(dir) {
  const f = fs.readdirSync(dir).find((n) => /^(licen[cs]e|copying)/i.test(n));
  return f ? fs.readFileSync(path.join(dir, f), 'utf8').trim() : '';
}

function fromRegistry(name) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'gfs-lic-'));
  try {
    const tgz = execFileSync('npm', ['pack', name, '--silent', '--pack-destination', tmp], { encoding: 'utf8' }).trim().split('\n').pop();
    execFileSync('tar', ['xzf', path.join(tmp, tgz), '-C', tmp]);
    const pj = JSON.parse(fs.readFileSync(path.join(tmp, 'package', 'package.json'), 'utf8'));
    return { version: pj.version + ', latest at build time; the bundled one may differ', license: pj.license, text: licenceIn(path.join(tmp, 'package')) };
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

const names = packagesOf(process.argv[2]);
if (names.has('pdfmake')) {
  for (const n of packagesOf('node_modules/pdfmake/build/pdfmake.js.map')) names.add(n);
}
const out = [];
for (const name of [...names].sort()) {
  const dir = path.join('node_modules', name);
  let e;
  if (fs.existsSync(path.join(dir, 'package.json'))) {
    const pj = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
    e = { version: pj.version, license: pj.license, text: licenceIn(dir) };
  } else {
    e = fromRegistry(name);
  }
  out.push('== ' + name + '@' + e.version + ' (' + (e.license || 'unstated') + ')\n' + (e.text || '(the package has no licence file)'));
}
out.push('== Roboto fonts (Apache-2.0), Copyright Google LLC\n' + fs.readFileSync(path.join(__dirname, 'APACHE-2.0.txt'), 'utf8').trim());
console.log(out.join('\n\n'));
