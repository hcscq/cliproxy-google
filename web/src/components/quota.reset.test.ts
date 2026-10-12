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

import { fmtRecover } from './quota';
describe('fmtRecover', () => {
  it('shows HH:mm today and MM-DD HH:mm otherwise', () => {
    const today = new Date(); today.setHours(19, 12, 0, 0);
    expect(fmtRecover(today.toISOString())).toBe('19:12');
    expect(fmtRecover(new Date(2030, 9, 17, 13, 36).toISOString())).toBe('10-17 13:36');
    expect(fmtRecover(undefined)).toBe('');
  });
});
