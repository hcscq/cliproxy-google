import { describe, expect, it } from 'vitest';
import { reserveShortText } from './ReserveAlert';
import type { ReserveStats } from '../api/client';

const zh = (z: string) => z;
const base: ReserveStats = { reserved: 0, ever_reserved: 0, bans_reserved: 0, bans_active: 0, bans_after_promotion: 0, promotions_24h: 0, min_active: 10, active: 3, eligible: 0, short: true };

describe('reserveShortText', () => {
  it('warns when short with empty reserve', () => {
    expect(reserveShortText(base, zh)).toBe('储备不足：可用 3 / 最低 10，储备 0');
  });
  it('notes tier filter when reserves exist but none eligible', () => {
    expect(reserveShortText({ ...base, reserved: 2 }, zh)).toContain('无符合订阅筛选');
  });
  it('silent when not short', () => {
    expect(reserveShortText({ ...base, short: false }, zh)).toBeNull();
    expect(reserveShortText(null, zh)).toBeNull();
  });
});
