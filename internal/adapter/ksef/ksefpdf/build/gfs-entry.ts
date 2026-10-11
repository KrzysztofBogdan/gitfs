import pdfMake from 'pdfmake/build/pdfmake';

export * from './index';

// gfsAddFont stores an already decoded font in pdfmake's virtual file
// system: with no encoding writeFileSync keeps the bytes as they are.
export function gfsAddFont(name: string, bytes: ArrayBuffer): void {
  (pdfMake as any).virtualfs.writeFileSync(name, new Uint8Array(bytes));
}
