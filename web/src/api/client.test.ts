import { describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiError, buildQuery, configPath, generateApiKey, isSupportedProvider, maskSecret, stateFromRedirect } from './client';

function mockFetch(status: number, body: unknown, contentType = 'application/json') {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const text = typeof body === 'string' ? body : JSON.stringify(body);
    return new Response(status === 204 ? null : text, { status, headers: { 'Content-Type': contentType } });
  });
  return { fn, calls };
}

describe('ApiClient', () => {
  it('sends bearer key and parses JSON', async () => {
    const { fn, calls } = mockFetch(200, { files: [] });
    const c = new ApiClient('secret', { fetch: fn });
    const res = await c.credentials();
    expect(res.files).toEqual([]);
    expect(calls[0].url).toBe('/v8/management/credentials');
    expect((calls[0].init?.headers as Record<string, string>).Authorization).toBe('Bearer secret');
    expect(calls[0].init?.method).toBe('GET');
  });

  it('encodes query parameters and skips empty ones', async () => {
    const { fn, calls } = mockFetch(200, { items: [], total: 0, limit: 50, offset: 0 });
    const c = new ApiClient('k', { fetch: fn });
    await c.requestEvents({ limit: 50, offset: 0, model: '', status: 'failed', search: 'a b' });
    expect(calls[0].url).toBe('/v8/management/request-events?limit=50&offset=0&status=failed&search=a+b');
  });

  it('sends JSON bodies for mutations', async () => {
    const { fn, calls } = mockFetch(200, { status: 'ok', disabled: true });
    const c = new ApiClient('k', { fetch: fn });
    await c.setCredentialDisabled('cred.json', true);
    expect(calls[0].init?.method).toBe('PATCH');
    expect(calls[0].url).toBe('/v8/management/credentials/status');
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ name: 'cred.json', disabled: true });
    expect((calls[0].init?.headers as Record<string, string>)['Content-Type']).toBe('application/json');
  });

  it('deletes access keys by stable id', async () => {
    const { fn, calls } = mockFetch(200, { status: 'ok' });
    await new ApiClient('k', { fetch: fn }).deleteAccessKey(7);
    expect(calls[0].init?.method).toBe('DELETE');
    expect(calls[0].url).toBe('/v8/management/access/api-keys?id=7');
  });

  it('raises ApiError with server code and message', async () => {
    const { fn } = mockFetch(409, { error: 'api_key_exists', message: 'already exists' });
    const c = new ApiClient('k', { fetch: fn });
    await expect(c.createAccessKey('sk-x')).rejects.toMatchObject({ status: 409, code: 'api_key_exists', message: 'already exists' });
  });

  it('invokes onUnauthorized for 401', async () => {
    const { fn } = mockFetch(401, { error: 'invalid management key' });
    const c = new ApiClient('bad', { fetch: fn });
    const spy = vi.fn();
    c.onUnauthorized = spy;
    await expect(c.verify()).rejects.toBeInstanceOf(ApiError);
    expect(spy).toHaveBeenCalledOnce();
  });

  it('treats an IP ban as an auth error', async () => {
    const { fn } = mockFetch(403, { error: 'IP banned due to too many failed attempts. Try again in 29m59s' });
    const c = new ApiClient('bad', { fetch: fn });
    const err = await c.verify().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.isAuth).toBe(true);
    expect(err.message).toMatch(/banned/);
  });

  it('returns fallback for missing config paths', async () => {
    const { fn, calls } = mockFetch(404, { error: 'not_found' });
    const c = new ApiClient('k', { fetch: fn });
    expect(await c.configGet('api-keys/gemini', [])).toEqual([]);
    expect(calls[0].url).toBe('/v8/management/config/api-keys/gemini');
  });

  it('rethrows non-404 config errors', async () => {
    const { fn } = mockFetch(500, { error: 'config_load_failed' });
    await expect(new ApiClient('k', { fetch: fn }).configGet('x', null)).rejects.toMatchObject({ status: 500 });
  });

  it('uploads YAML as raw text', async () => {
    const { fn, calls } = mockFetch(200, { status: 'ok' });
    await new ApiClient('k', { fetch: fn }).putConfigYAML('a: 1\n');
    expect(calls[0].init?.body).toBe('a: 1\n');
    expect((calls[0].init?.headers as Record<string, string>)['Content-Type']).toBe('application/yaml');
  });

  it('posts multipart credential uploads without a JSON content type', async () => {
    const { fn, calls } = mockFetch(200, { status: 'ok', uploaded: 1 });
    const file = new File(['{"type":"antigravity"}'], 'a.json', { type: 'application/json' });
    await new ApiClient('k', { fetch: fn }).uploadCredentialFiles([file]);
    expect(calls[0].init?.body).toBeInstanceOf(FormData);
    expect((calls[0].init?.headers as Record<string, string>)['Content-Type']).toBeUndefined();
  });

  it('submits OAuth callbacks with the pasted redirect URL', async () => {
    const { fn, calls } = mockFetch(200, { status: 'ok' });
    await new ApiClient('k', { fetch: fn }).oauthSubmitCallback('http://localhost:51121/oauth-callback?code=c&state=s', 's', 'antigravity');
    expect(calls[0].url).toBe('/v8/management/oauth/callback');
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      redirect_url: 'http://localhost:51121/oauth-callback?code=c&state=s',
      state: 's',
      provider: 'antigravity',
    });
  });

  it('handles empty successful responses', async () => {
    const fn = vi.fn(async () => new Response('', { status: 200 }));
    expect(await new ApiClient('k', { fetch: fn }).clearLogs()).toBeUndefined();
  });
});

describe('helpers', () => {
  it('buildQuery', () => {
    expect(buildQuery()).toBe('');
    expect(buildQuery({ a: 1, b: undefined, c: null, d: '', e: true })).toBe('?a=1&e=true');
  });
  it('configPath encodes segments', () => {
    expect(configPath('/oauth/excluded-models/')).toBe('oauth/excluded-models');
    expect(configPath('oauth/model-alias/a b')).toBe('oauth/model-alias/a%20b');
  });
  it('generateApiKey', () => {
    const k = generateApiKey();
    expect(k).toMatch(/^sk-[0-9a-f]{48}$/);
    expect(generateApiKey()).not.toBe(k);
  });
  it('stateFromRedirect', () => {
    expect(stateFromRedirect('http://localhost:51121/oauth-callback?code=x&state=agv-123')).toBe('agv-123');
    expect(stateFromRedirect('code=x&state=abc')).toBe('abc');
    expect(stateFromRedirect('nothing')).toBeUndefined();
  });
  it('isSupportedProvider', () => {
    expect(isSupportedProvider('antigravity')).toBe(true);
    expect(isSupportedProvider('vertex')).toBe(true);
    expect(isSupportedProvider('gemini-interactions')).toBe(true);
    expect(isSupportedProvider('codex')).toBe(false);
    expect(isSupportedProvider(undefined)).toBe(false);
  });
  it('maskSecret', () => {
    expect(maskSecret('sk-1234567890abcdef')).toBe('sk-1…cdef');
    expect(maskSecret('abc')).toBe('•••');
  });
});

import { normalizeTopology } from './client';

describe('normalizeTopology', () => {
  it('dedupes proxies and separates stale homes after a rollout', () => {
    const now = new Date().toISOString();
    const old = new Date(Date.now() - 60000).toISOString();
    const r = normalizeTopology({
      homes: [
        { id: 'old:1', ip: 'old', port: 1, is_master: true, healthy: false, health: 'stale', last_seen_at: old },
        { id: 'new:1', ip: 'new', port: 1, is_master: false, healthy: true, health: 'healthy', last_seen_at: now },
        { id: 'new:2', ip: 'new2', port: 1, is_master: true, healthy: true, health: 'healthy', last_seen_at: now },
      ],
      cpas: [
        { node_id: 'a', node_name: 'pod-a', healthy: false, state: 'draining', last_seen_at: old },
        { node_id: 'a', node_name: 'pod-a', healthy: true, state: 'active', last_seen_at: now },
        { node_id: 'b', node_name: 'pod-b', healthy: true, state: 'active', last_seen_at: now },
        { node_id: 'b-old', node_name: 'pod-b', healthy: false, health: 'stale', last_seen_at: old },
      ],
    });
    expect(r.homes.map((h) => h.id)).toEqual(['new:1', 'new:2']);
    expect(r.staleHomes.map((h) => h.id)).toEqual(['old:1']);
    expect(r.leader?.id).toBe('new:2');
    expect(r.proxies).toHaveLength(2);
    expect(r.proxies[0]).toMatchObject({ node_id: 'a', healthy: true });
  });
  it('handles missing topology', () => {
    expect(normalizeTopology(undefined)).toEqual({ homes: [], staleHomes: [], proxies: [] });
  });
});

import { summarizeError } from './client';

describe('summarizeError', () => {
  it('extracts message, code/status and reasons from a Google error body', () => {
    const body = JSON.stringify({ error: { code: 503, message: 'No capacity available for model x on the server', status: 'UNAVAILABLE', details: [{ '@type': 't', reason: 'MODEL_CAPACITY_EXHAUSTED' }] } });
    expect(summarizeError({ message: '{ "error": { "code": 503, "mess', body_preview: body })).toEqual({
      title: '503 UNAVAILABLE',
      message: 'No capacity available for model x on the server',
      reasons: ['MODEL_CAPACITY_EXHAUSTED'],
    });
  });
  it('falls back to the server message for truncated bodies', () => {
    expect(summarizeError({ message: 'boom', reason: 'r', body_preview: '{"error": {"cod' })).toEqual({ title: 'r', message: 'boom', reasons: [] });
  });
});

describe('summarizeError truncated bodies', () => {
  it('extracts message/status/reason from a truncated JSON preview', () => {
    const body = '{\n  "error": {\n    "code": 503,\n    "message": "No capacity available for model gemini-3.1-pro-low on the server",\n    "status": "UNAVAILABLE",\n    "details": [\n      {\n        "@type": "type.googleapis.com/google.rpc.ErrorInfo",\n        "reason": "MODEL_CAPACITY_EXHAUSTED",\n        "metad';
    const s = summarizeError({ message: body });
    expect(s.message).toBe('No capacity available for model gemini-3.1-pro-low on the server');
    expect(s.title).toBe('503 UNAVAILABLE');
    expect(s.reasons).toEqual(['MODEL_CAPACITY_EXHAUSTED']);
  });
});

import { capRank } from '../pages/Dashboard';
describe('capRank', () => {
  it('keeps the top N and aggregates the rest', () => {
    const items = Array.from({ length: 12 }, (_, i) => ({ label: `m${i}`, value: i }));
    const out = capRank(items, 8, (n) => `其他 (${n})`);
    expect(out).toHaveLength(9);
    expect(out[0].label).toBe('m11');
    expect(out[8]).toEqual({ label: '其他 (4)', value: 0 + 1 + 2 + 3 });
  });
  it('returns everything when within N', () => {
    expect(capRank([{ label: 'a', value: 1 }], 8, () => 'x')).toHaveLength(1);
  });
});

describe('credentialUsable / coolingModels', () => {
  it('model-scoped cooldown keeps credential usable', async () => {
    const { credentialUsable, coolingModels } = await import('./client');
    const c = { id: 'a', auth_index: 'a', name: 'a', provider: 'antigravity', status: 'error', unavailable: false,
      model_cooldowns: [{ model: 'gemini-2.5-pro', cooling: true }, { model: 'x', cooling: false }] };
    expect(credentialUsable(c)).toBe(true);
    expect(coolingModels(c).map((m) => m.model)).toEqual(['gemini-2.5-pro']);
    expect(credentialUsable({ ...c, credential_scope_error: true })).toBe(false);
    expect(credentialUsable({ ...c, unavailable: true })).toBe(false);
  });
});

describe('toggleExcludedModel', () => {
  it('adds and removes models from the antigravity list only', async () => {
    const { toggleExcludedModel } = await import('./client');
    const base = { antigravity: ['chat_20706'], vertex: ['x'] };
    const hidden = toggleExcludedModel(base, 'gemini-3.5-flash-low', true);
    expect(hidden.antigravity).toEqual(['chat_20706', 'gemini-3.5-flash-low']);
    expect(hidden.vertex).toEqual(['x']);
    expect(toggleExcludedModel(hidden, 'GEMINI-3.5-flash-low', false).antigravity).toEqual(['chat_20706']);
    expect(toggleExcludedModel({}, 'a', true).antigravity).toEqual(['a']);
  });
});

import { credentialGroupAvailability, keyScopeSummary, type Credential } from './client';

describe('credential groups', () => {
  const creds = [
    { id: 'a', provider: 'antigravity', auth_index: 'a', name: 'a' },
    { id: 'b', provider: 'antigravity', auth_index: 'b', name: 'b', unavailable: true },
    { id: 'c', provider: 'antigravity', auth_index: 'c', name: 'c', disabled: true },
  ] as Credential[];
  const members = [
    { id: 1, channel_group_id: 7, auth_id: 'a' },
    { id: 2, channel_group_id: 7, auth_id: 'b' },
    { id: 3, channel_group_id: 7, auth_id: 'gone' },
    { id: 4, channel_group_id: 8, auth_id: 'c' },
  ];
  it('counts live usable members per group', () => {
    expect(credentialGroupAvailability(7, members, creds)).toEqual({ total: 3, usable: 1, missing: 1 });
    expect(credentialGroupAvailability(8, members, creds)).toEqual({ total: 1, usable: 0, missing: 0 });
    expect(credentialGroupAvailability(9, members, creds)).toEqual({ total: 0, usable: 0, missing: 0 });
  });
  it('summarises key scope; empty = unrestricted', () => {
    const cg = new Map([[7, 'svc-a']]);
    const mg = new Map([[3, 'flash-only']]);
    expect(keyScopeSummary({}, cg, mg)).toEqual({ credentials: null, models: null });
    expect(keyScopeSummary({ channels: [7, 99], model_groups: [3] }, cg, mg)).toEqual({ credentials: ['svc-a', '#99'], models: ['flash-only'] });
  });
});
