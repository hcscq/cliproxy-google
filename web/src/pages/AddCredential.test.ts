import { describe, expect, it } from 'vitest';
import { chunkTokenInput } from './AddCredential';

const tok = '1//06FakeTokenCgYIARAAGAYSNwF-L9IrFakeFake-abc_DEF0123456789xyzE68s';

describe('chunkTokenInput', () => {
  it('keeps email----token lines and strips copy-paste junk', () => {
    const out = chunkTokenInput(`\ufeffa@gmail.com----${tok}\u200b\r\nb@gmail.com－－－－${tok}\r`);
    expect(out).toEqual([{ text: `a@gmail.com----${tok}\nb@gmail.com－－－－${tok}` }]);
  });
  it('parses JSON arrays', () => {
    expect(chunkTokenInput(`["${tok}"]`)).toEqual([{ items: [tok] }]);
  });
});
