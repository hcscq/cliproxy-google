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

import { looksLikeRefreshTokenText } from './AddCredential';
describe('looksLikeRefreshTokenText', () => {
  it('detects email----token and bare tokens', () => {
    expect(looksLikeRefreshTokenText(`a@gmail.com----${tok}\nb@x.com----${tok}`)).toBe(true);
    expect(looksLikeRefreshTokenText(tok)).toBe(true);
    expect(looksLikeRefreshTokenText('{"type":"antigravity"')).toBe(false);
    expect(looksLikeRefreshTokenText('hello world')).toBe(false);
  });
});
