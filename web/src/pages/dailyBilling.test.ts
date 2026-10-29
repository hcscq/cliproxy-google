import { describe, expect, it } from 'vitest';
import { defaultBillingRange, tierLabel, variantLabel } from './DailyBilling';

describe('daily billing labels', () => {
  it('shortens gemini-4 argon variants', () => {
    expect(variantLabel('gemini-4-argon')).toBe('argon');
    expect(variantLabel('gemini-4-argon-high')).toBe('high');
    expect(variantLabel('gemini-4-argon-medium')).toBe('medium');
    expect(variantLabel('gemini-4-argon-low')).toBe('low');
    expect(variantLabel('other-model')).toBe('other-model');
  });

  it('defaults to yesterday through today', () => {
    expect(defaultBillingRange(new Date(2026, 9, 29, 15, 0, 0))).toEqual({ from: '2026-10-28', to: '2026-10-29' });
  });

  it('names the residual tier in Chinese', () => {
    expect(tierLabel('other', true)).toBe('其他');
    expect(tierLabel('pro', true)).toBe('pro');
    expect(tierLabel('other', false)).toBe('other');
  });
});
