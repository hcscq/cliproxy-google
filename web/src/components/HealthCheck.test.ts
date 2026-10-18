import { describe, expect, it } from 'vitest';
import { healthBadgeText, healthSummaryText } from './HealthCheck';

const zh = (z: string) => z;

describe('health check labels', () => {
  it('badge text', () => {
    expect(healthBadgeText({ status: 'pass', categories: [] }, zh)).toBe('体检通过');
    expect(healthBadgeText({ status: 'fail', categories: ['needs_verify'] }, zh)).toBe('需验证');
    expect(healthBadgeText({ status: 'fail', categories: ['tier', 'quota_used'] }, zh)).toBe('非Pro +1');
    expect(healthBadgeText({ status: 'running', categories: [] }, zh)).toBe('体检中');
    expect(healthBadgeText({ status: 'fail', categories: ['region'] }, zh)).toBe('地区限制');
  });
  it('summary text', () => {
    expect(healthSummaryText({ total: 5, pass: 3, fail: 2, running: 0, by_category: { needs_verify: 1, tos: 1 } }, zh)).toMatch(/^体检：通过 3 · 失败 2（/);
  });
});
