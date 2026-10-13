import { describe, expect, it } from 'vitest';
import { defaultOrder, loadCredSort } from './Credentials';

describe('credential sort persistence', () => {
  it('defaults to newest created first', () => {
    expect(loadCredSort('', null)).toEqual({ sort: 'created', order: 'desc' });
  });
  it('URL wins over localStorage', () => {
    expect(loadCredSort('?sort=quota', JSON.stringify({ sort: 'name', order: 'desc' }))).toEqual({ sort: 'quota', order: 'asc' });
  });
  it('falls back to stored, ignores junk', () => {
    expect(loadCredSort('', JSON.stringify({ sort: 'egress', order: 'desc' }))).toEqual({ sort: 'egress', order: 'desc' });
    expect(loadCredSort('?sort=bogus', '{bad')).toEqual({ sort: 'created', order: 'desc' });
    expect(defaultOrder('last_used')).toBe('desc');
  });
});

import { callStatsTooltip, fmtCompact, successPct } from '../components/CallStats';

describe('call stats', () => {
  it('formats compactly', () => {
    expect(fmtCompact(950)).toBe('950');
    expect(fmtCompact(1234)).toBe('1.2k');
    expect(fmtCompact(25000)).toBe('25k');
    expect(successPct({ calls: 0, ok: 0 })).toBeNull();
    expect(successPct({ calls: 4, ok: 3 })).toBe(75);
  });
  it('tooltip lists error classes', () => {
    const tip = callStatsTooltip({ calls: 10, ok: 7, by_class: { '429': 2, net: 1 } }, '24h', (zh) => zh);
    expect(tip).toContain('429 额度/限流: 2');
    expect(tip).toContain('网络/出口: 1');
  });
});
