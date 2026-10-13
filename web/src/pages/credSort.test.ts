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
