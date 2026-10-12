import { describe, expect, it } from 'vitest';
import { fmtResetShort, LOW_QUOTA_RATIO } from './quota';

describe('fmtResetShort', () => {
  it('formats MM-DD HH:mm in local time', () => {
    const d = new Date(2026, 9, 17, 13, 36);
    expect(fmtResetShort(d.toISOString())).toBe('10-17 13:36');
  });
  it('treats near-zero as low quota', () => {
    expect(0.00035 < LOW_QUOTA_RATIO).toBe(true);
  });
});
