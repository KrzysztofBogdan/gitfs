import { it, vi } from 'vitest';
import { readFileSync, writeFileSync } from 'fs';
import { join } from 'path';
import { generateInvoice } from './lib-public/generate-invoice';

// Renders testdata/golden.json's invoices with the unchanged generator
// (ksef pdf spec §8.1), the clock frozen at each case's time.
it('golden', async () => {
  const dir = process.env.TESTDATA!;
  const cases = JSON.parse(readFileSync(join(dir, 'golden.json'), 'utf8'));
  for (const c of cases) {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(c.clockMs));
    const file = new File([readFileSync(join(dir, c.xml))], c.xml, { type: 'text/xml' });
    const add: any = { nrKSeF: c.ksef, acDate: c.acDate };
    if (c.qrCode) add.qrCode = c.qrCode;
    const b64 = await generateInvoice(file, add, 'base64');
    writeFileSync(join(dir, c.pdf), Buffer.from(b64 as string, 'base64'));
    vi.useRealTimers();
  }
}, 600000);
