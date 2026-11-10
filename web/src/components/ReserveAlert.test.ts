import { describe, expect, it } from 'vitest';
import {
  reserveShortText,
  reserveSummaryText,
  reserveRecentText,
  shortReserveDetail,
  reserveEventKindLabel,
  reserveEventKindTone,
} from './ReserveAlert';
import type { ReserveEvent, ReserveStats } from '../api/client';

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

describe('shortReserveDetail', () => {
  it('collapses ToS violation text', () => {
    const long = 'This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.';
    expect(shortReserveDetail(long, zh)).toBe('违反服务条款(403)');
  });
  it('keeps a short first clause', () => {
    expect(shortReserveDetail('auto promote for argon shortfall', zh)).toBe('auto promote for argon shortfall');
  });
  it('truncates long non-ToS detail', () => {
    const long = 'x'.repeat(60);
    expect(shortReserveDetail(long, zh).endsWith('…')).toBe(true);
    expect(shortReserveDetail(long, zh).length).toBeLessThanOrEqual(41);
  });
  it('empty detail is empty', () => {
    expect(shortReserveDetail(undefined, zh)).toBe('');
  });
});

describe('reserveEventKindLabel', () => {
  it('labels known kinds', () => {
    expect(reserveEventKindLabel('promote', zh)).toBe('自动启用');
    expect(reserveEventKindLabel('ban', zh)).toBe('封禁');
    expect(reserveEventKindLabel('clear-on-disable', zh)).toBe('停用时清除储备');
  });
});

describe('reserveEventKindTone', () => {
  it('maps tones', () => {
    expect(reserveEventKindTone('ban')).toBe('err');
    expect(reserveEventKindTone('promote')).toBe('primary');
    expect(reserveEventKindTone('reserve')).toBe('ok');
  });
});

const tierDrop = '订阅等级变更：g1-pro-tier → free-tier（Antigravity Starter Quota）· loadCodeAssist paidTier';
const tierUp = '订阅等级变更：free-tier → g1-pro-tier（Google AI Pro）· loadCodeAssist paidTier';
const tierDown = '订阅等级变更：g1-ultra-tier → g1-pro-tier（Google AI Pro）· loadCodeAssist paidTier';

describe('tier_changed events', () => {
  it('labels a drop to free as 订阅掉档 and an upgrade as 订阅升档', () => {
    expect(reserveEventKindLabel('tier_changed', zh, tierDrop)).toBe('订阅掉档');
    expect(reserveEventKindTone('tier_changed', tierDrop)).toBe('err');
    expect(reserveEventKindLabel('tier_changed', zh, tierDown)).toBe('订阅掉档');
    expect(reserveEventKindTone('tier_changed', tierDown)).toBe('warn');
    expect(reserveEventKindLabel('tier_changed', zh, tierUp)).toBe('订阅升档');
    expect(reserveEventKindTone('tier_changed', tierUp)).toBe('ok');
  });
  it('shows the from → to in the short reason', () => {
    expect(shortReserveDetail(tierDrop, zh)).toBe('g1-pro-tier → free-tier');
  });
  it('counts 24h drops beside promote and ban, skipping upgrades and older rows', () => {
    const now = 1_700_000_000_000;
    const events: ReserveEvent[] = [
      { auth_id: 'a', kind: 'tier_changed', at: new Date(now - 1000).toISOString(), was_reserved: true, detail: tierDrop },
      { auth_id: 'a', kind: 'tier_changed', at: new Date(now - 1000).toISOString(), was_reserved: false, detail: tierDown },
      { auth_id: 'a', kind: 'tier_changed', at: new Date(now - 1000).toISOString(), was_reserved: false, detail: tierUp },
      { auth_id: 'a', kind: 'tier_changed', at: new Date(now - 48 * 3600 * 1000).toISOString(), was_reserved: false, detail: tierDrop },
      { auth_id: 'b', kind: 'ban', at: new Date(now - 1000).toISOString(), was_reserved: false },
    ];
    expect(reserveRecentText({ ...base, promotions_24h: 2 }, events, zh, now)).toBe('近24h 自动启用 2 · 封禁 1 · 掉档 2');
  });
});
