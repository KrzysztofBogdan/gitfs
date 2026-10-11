// Writes the TTFs of the generator's pdfmake (base64 in vfs_fonts.js) into argv[2].
const fs = require('fs');
const vfs = require(process.cwd() + '/node_modules/pdfmake/build/vfs_fonts.js');
for (const [name, b64] of Object.entries(vfs)) {
  fs.writeFileSync(process.argv[2] + '/' + name, Buffer.from(b64, 'base64'));
}
