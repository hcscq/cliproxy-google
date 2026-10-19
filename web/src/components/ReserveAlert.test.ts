import { describe, expect, it } from 'vitest';
import { reserveShortText, reserveSummaryText } from './ReserveAlert';
import type { ReserveStats } from '../api/client';

const zh = (z: string) => z;
const base: ReserveStats = { reserved: 0, ever_reserved: 0, bans_reserved: 0, bans_active: 0, bans_after_promotion: 0, promotions_24h: 0, min_active: 10, active: 3, eligible: 0, short: true };

describe('reserveShortText', () => {
  it('warns when short with empty reserve', () => {
    expect(reserveShortText(base, zh)).toBe('储备不足：可用 3（不含 Free）/ 最低 10，储备 0');
  });
  it('notes tier filter when reserves exist but none eligible', () => {
    expect(reserveShortText({ ...base, reserved: 2 }, zh)).toContain('无符合订阅筛选');
  });
  it('silent when not short', () => {
    expect(reserveShortText({ ...base, short: false }, zh)).toBeNull();
    expect(reserveShortText(null, zh)).toBeNull();
  });
});

describe('reserveSummaryText', () => {
  it('shows counts excluding Free and the compared metric', () => {
    const now = 1_000_000_000_000;
    const t = reserveSummaryText({ ...base, enabled: 40, usable: 35, count_cooled: true, reserved: 31, eligible: 30, last_promotion: now / 1000 - 3 * 3600 }, zh, now)!;
    expect(t).toBe('储备：最低 10（按启用数）· 启用 40 · 可用 35（均不含 Free）· 储备 31 / 可提升 30 · 上次提升 3 小时前');
  });
  it('handles unknown values and no promotion', () => {
    const t = reserveSummaryText({ ...base, min_active: 0, eligible: -1, count_cooled: false }, zh)!;
    expect(t).toContain('最低 未设置（按可用数）');
    expect(t).toContain('启用 — · 可用 —');
    expect(t).toContain('可提升 — · 上次提升 从未');
  });
});
