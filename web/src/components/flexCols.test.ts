import { describe, expect, it } from 'vitest';
import { distributeColumns } from './flexCols';
describe('distributeColumns', () => {
  it('respects min, max and weights', () => {
    const w = distributeColumns([{ min: 50 }, { min: 100, max: 150, weight: 1 }, { min: 100, weight: 1 }], 500);
    expect(w).toEqual([50, 150, 300]);
  });
  it('never goes below min', () => {
    expect(distributeColumns([{ min: 100, weight: 1 }, { min: 100, weight: 1 }], 120)).toEqual([100, 100]);
  });
});
