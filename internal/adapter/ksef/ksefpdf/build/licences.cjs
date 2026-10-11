// Prints the licences of the generator's production dependencies, which
// the bundle contains, then Roboto's (ksef pdf spec §3.1).
const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');
let ls;
try {
  ls = execSync('npm ls --omit=dev --all --parseable', { encoding: 'utf8' });
} catch (e) {
  ls = e.stdout;
}
const seen = new Set();
const out = [];
for (const d of ls.trim().split('\n').slice(1)) {
  const pj = JSON.parse(fs.readFileSync(path.join(d, 'package.json'), 'utf8'));
  const key = pj.name + '@' + pj.version;
  if (seen.has(key)) continue;
  seen.add(key);
  const lic = fs.readdirSync(d).find((f) => /^(licen[cs]e|copying)/i.test(f));
  out.push('== ' + key + ' (' + (pj.license || 'unstated') + ')\n' +
    (lic ? fs.readFileSync(path.join(d, lic), 'utf8').trim() : '(the package has no licence file)'));
}
out.push('== Roboto fonts (Apache-2.0)\nCopyright Google LLC. Licensed under the Apache License, Version 2.0:\nhttps://www.apache.org/licenses/LICENSE-2.0');
console.log(out.join('\n\n'));
