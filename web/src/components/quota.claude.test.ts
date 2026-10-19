import { describe, expect, it } from 'vitest';
import { isClaudeWindow } from './quota';

describe('isClaudeWindow', () => {
  it('matches the Antigravity third-party bucket and Claude account windows', () => {
    expect(isClaudeWindow({ id: 'antigravity-3p-5h', label: 'Claude and GPT models', scope_id: 'third-party' })).toBe(true);
    expect(isClaudeWindow({ id: 'antigravity-3p-weekly' })).toBe(true);
    expect(isClaudeWindow({ id: 'claude-five-hour' })).toBe(true);
    expect(isClaudeWindow({ id: 'antigravity-gemini-5h', label: 'Gemini Models', scope_id: 'gemini' })).toBe(false);
    expect(isClaudeWindow({ id: 'codex-5-hour' })).toBe(false);
  });
});
