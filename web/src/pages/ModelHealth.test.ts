import { describe, expect, it } from 'vitest';
import { noteChips, shortReason } from './ModelHealth';
import type { ModelHealth } from '../api/client';

const tr = (zh: string) => zh;
const base: ModelHealth = { id: 'm', status: 'available', hidden: false, in_catalog: true, registered: true, credentials: [] };

describe('model health compact text', () => {
  it('gives short Chinese reasons without upstream English text', () => {
    expect(shortReason({ ...base, status: 'retired', reason: '已下线（上游）：Gemini 3.5 Flash is no longer available.' }, tr)).toBe('上游已下线，请改用其他模型');
    expect(shortReason({ ...base, status: 'rejected', deprecated_to: 'gemini-pro-agent' }, tr)).toBe('已弃用 → gemini-pro-agent');
    expect(shortReason({ ...base, status: 'partial', credentials: [{ label: 'a', state: 'ok', in_catalog: true }, { label: 'b', state: 'cooling', in_catalog: true }] }, tr)).toBe('1/2 凭据可用');
    expect(shortReason(base, tr)).toBe('');
  });
  it('turns shared notes into chips', () => {
    const chips = noteChips({ ...base, notes: ['流式思考文本不稳定（上游限制）：…', '上游标记“即将下线”：Use X', '上游提示切换到 Gemini 3.7 Flash，但…'] });
    expect(chips.map((c) => c.kind)).toEqual(['stream', 'leaving', 'switch']);
    expect(chips[1].title).toContain('Use X');
  });
});
