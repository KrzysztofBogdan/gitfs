// Adds the gfs build to the generator's vite config (ksef pdf spec §3.2);
// fails when the config no longer has the lines it changes.
const fs = require('fs');
const p = process.argv[2];
let s = fs.readFileSync(p, 'utf8');
const alias = "'@shared': path.resolve(__dirname, 'src/shared'),";
const entry = "entry: path.resolve(__dirname, libRoot, 'index.ts'),";
if (!s.includes(alias) || !s.includes(entry)) {
  console.error(p + ': the vite config changed; update build/patch-vite.cjs');
  process.exit(1);
}
s = s.replace(alias, alias + "\n        ...(process.env.GFS ? { 'pdfmake/build/vfs_fonts': path.resolve(__dirname, 'src/gfs-empty-vfs.js') } : {}),");
s = s.replace(entry, "entry: path.resolve(__dirname, libRoot, process.env.GFS ? 'gfs-entry.ts' : 'index.ts'),");
fs.writeFileSync(p, s);
