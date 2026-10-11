#!/usr/bin/env bash
# Rebuilds assets/ and the golden PDFs in testdata/ from a commit of the
# Ministry's ksef-pdf-generator (ksef pdf spec §3.2). Needs git, Node.js 22
# and npm; nothing else in gfs does.
#
#   internal/adapter/ksef/ksefpdf/build.sh f59fc4e
set -euo pipefail
commit=${1:?usage: build.sh <commit of github.com/CIRFMF/ksef-pdf-generator>}
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git clone -q https://github.com/CIRFMF/ksef-pdf-generator.git "$work/gen"
cd "$work/gen"
git checkout -q "$commit"
npm ci --no-audit --no-fund >/dev/null

# Golden PDFs from the unchanged source, as the web app renders them.
cp "$here/build/golden.spec.ts" src/gfs-golden.spec.ts
TESTDATA="$here/testdata" TZ=Europe/Warsaw npx vitest run src/gfs-golden.spec.ts >/dev/null
rm src/gfs-golden.spec.ts

# The gfs build: fonts come from Go.
cp "$here/build/gfs-entry.ts" "$here/build/gfs-empty-vfs.js" src/
node "$here/build/patch-vite.cjs" vite.config.ts
GFS=1 npm run build -- --sourcemap >/dev/null
mkdir -p "$here/assets"
sed '/^\/\/# sourceMappingURL=/d' dist/ksef-fe-invoice-converter.umd.cjs > "$here/assets/generator.js"
node "$here/build/fonts.cjs" "$here/assets"
cp LICENSE "$here/assets/LICENSE"
node "$here/build/licences.cjs" dist/ksef-fe-invoice-converter.umd.cjs.map > "$here/assets/THIRD-PARTY.txt"
version=$(node -p 'require("./package.json").version')
rev=$(git rev-parse --short=7 HEAD)
echo "$version $rev" > "$here/assets/VERSION"
