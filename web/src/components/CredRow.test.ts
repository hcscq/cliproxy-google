import { describe, expect, it } from 'vitest';
import type { Credential } from '../api/client';
import { mergedStatus } from './CredRow';

const tr = (zh: string) => zh;
const base = { id: 'x', provider: 'antigravity', status: 'active', disabled: false } as unknown as Credential;
const st = (p: Record<string, unknown>) => mergedStatus({ ...base, ...p } as Credential, tr);

describe('mergedStatus', () => {
  it('hard-off reasons win over everything', () => {
    expect(st({ disabled: true, auto_disabled_reason: 'account_suspended', reserved: true }).label).toBe('403 封禁');
    expect(st({ disabled: true, status_message: 'unauthorized' }).label).toBe('401 失效');
    expect(st({ disabled: true }).label).toBe('已禁用');
  });
  it('failed check beats reserve; reserve beats ok', () => {
    const f = st({ reserved: true, health_check: { status: 'fail', categories: ['needs_verify', 'quota_unknown'] } });
    expect(f.kind).toBe('err');
    expect(f.label).toBe('需验证 +1');
    expect(f.lines.some((l) => l.includes('储备中'))).toBe(true);
    expect(st({ reserved: true, health_check: { status: 'pass', categories: [] } }).label).toBe('储备');
  });
  it('marks unchecked and region notice', () => {
    expect(st({}).label).toBe('可用·未检');
    expect(st({ health_check: { status: 'pass', categories: ['region_warn'] } }).kind).toBe('warn');
    expect(st({ health_check: { status: 'pass', categories: [] } })).toMatchObject({ kind: 'ok', label: '可用' });
  });
});
