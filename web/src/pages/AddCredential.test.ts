import { describe, expect, it } from 'vitest';
import { chunkTokenInput, parseCredentialJSON } from './AddCredential';

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
  it('detects vendor export lines and mixed batches', () => {
    const json = `{"type":"antigravity","email":"a@gmail.com","refresh_token":"${tok}","project_id":"p1"}`;
    const exportLine = `a@gmail.com----FakePassw0rd!x----0123456789abcdef0123456789abcdef----${json}`;
    expect(looksLikeRefreshTokenText(exportLine)).toBe(true);
    expect(looksLikeRefreshTokenText(`${exportLine}\nb@x.com----${tok}`)).toBe(true);
    expect(looksLikeRefreshTokenText('not-this-format')).toBe(false);
  });
});

describe('parseCredentialJSON', () => {
  it('accepts object, array and NDJSON', () => {
    expect(parseCredentialJSON('{"type":"antigravity","refresh_token":"x"}').items).toHaveLength(1);
    const arr = parseCredentialJSON('[{"email":"a@gmail.com","refresh_token":"1//a"},{"email":"b@gmail.com","refresh_token":"1//b"}]');
    expect(arr.error).toBeUndefined();
    expect(arr.items).toHaveLength(2);
    expect(parseCredentialJSON('{"refresh_token":"1//a"}\n{"refresh_token":"1//b"},\n').items).toHaveLength(2);
  });
  it('rejects non-objects', () => {
    expect(parseCredentialJSON('[1,2]').error).toContain('第 1 项');
    expect(parseCredentialJSON('[]').error).toBeTruthy();
    expect(parseCredentialJSON('"x"').error).toBeTruthy();
    expect(parseCredentialJSON('{bad').error).toBeTruthy();
    expect(parseCredentialJSON('  ').items).toHaveLength(0);
  });
});
