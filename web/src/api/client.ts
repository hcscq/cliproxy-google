// Typed client for the Home Management API (v8). Every request carries the
// management key; the key never leaves the browser except as a request header.

export const MANAGEMENT_BASE = '/v8/management';

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: unknown;
  constructor(status: number, code: string, message: string, body?: unknown) {
    super(message || code || `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.body = body;
  }
  get isAuth(): boolean {
    return this.status === 401 || (this.status === 403 && /banned|management|remote/i.test(this.message));
  }
}

export type Query = Record<string, string | number | boolean | undefined | null>;

export interface RequestOptions {
  query?: Query;
  body?: unknown;
  form?: FormData;
  rawBody?: string;
  contentType?: string;
  headers?: Record<string, string>;
  responseType?: 'json' | 'text' | 'blob';
  signal?: AbortSignal;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export function buildQuery(query?: Query): string {
  if (!query) return '';
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue;
    params.append(k, String(v));
  }
  const s = params.toString();
  return s ? `?${s}` : '';
}

/** Encodes a slash separated config path ("api-keys/gemini") segment by segment. */
export function configPath(path: string): string {
  return path
    .split('/')
    .filter(Boolean)
    .map((p) => encodeURIComponent(p))
    .join('/');
}

export class ApiClient {
  private key: string;
  private readonly fetcher: FetchLike;
  private readonly base: string;
  onUnauthorized?: (err: ApiError) => void;

  constructor(key: string, opts: { fetch?: FetchLike; base?: string } = {}) {
    this.key = key;
    this.fetcher = opts.fetch ?? ((input, init) => fetch(input, init));
    this.base = opts.base ?? MANAGEMENT_BASE;
  }

  setKey(key: string): void {
    this.key = key;
  }

  async request<T = unknown>(method: string, path: string, opts: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.key}`, ...(opts.headers ?? {}) };
    let body: BodyInit | undefined;
    if (opts.form) {
      body = opts.form;
    } else if (opts.rawBody !== undefined) {
      body = opts.rawBody;
      headers['Content-Type'] = opts.contentType ?? 'application/json';
    } else if (opts.body !== undefined) {
      body = JSON.stringify(opts.body);
      headers['Content-Type'] = 'application/json';
    }
    const url = `${this.base}${path.startsWith('/') ? path : `/${path}`}${buildQuery(opts.query)}`;
    const res = await this.fetcher(url, { method, headers, body, signal: opts.signal });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      let parsed: unknown = undefined;
      let code = '';
      let message = '';
      try {
        parsed = text ? JSON.parse(text) : undefined;
      } catch {
        parsed = undefined;
      }
      if (parsed && typeof parsed === 'object') {
        const o = parsed as Record<string, unknown>;
        code = typeof o.error === 'string' ? o.error : '';
        message = typeof o.message === 'string' ? o.message : code;
      } else {
        message = text.trim().slice(0, 300);
      }
      const err = new ApiError(res.status, code, message || `HTTP ${res.status}`, parsed);
      if (err.isAuth && this.onUnauthorized) this.onUnauthorized(err);
      throw err;
    }
    if (opts.responseType === 'blob') return (await res.blob()) as T;
    const text = await res.text();
    if (opts.responseType === 'text') return text as T;
    if (!text) return undefined as T;
    try {
      return JSON.parse(text) as T;
    } catch {
      return text as T;
    }
  }

  get<T = unknown>(path: string, query?: Query) {
    return this.request<T>('GET', path, { query });
  }
  post<T = unknown>(path: string, body?: unknown, query?: Query) {
    return this.request<T>('POST', path, { body, query });
  }
  put<T = unknown>(path: string, body?: unknown, query?: Query) {
    return this.request<T>('PUT', path, { body, query });
  }
  patch<T = unknown>(path: string, body?: unknown, query?: Query) {
    return this.request<T>('PATCH', path, { body, query });
  }
  del<T = unknown>(path: string, query?: Query) {
    return this.request<T>('DELETE', path, { query });
  }

  // ---- Auth / health ---------------------------------------------------------
  /** Validates the key with a cheap authenticated call. */
  verify() {
    return this.get<{ capabilities: Record<string, boolean> }>('/capabilities');
  }

  // ---- Cluster ---------------------------------------------------------------
  topology() {
    return this.get<Topology>('/topology');
  }
  nodes() {
    return this.get<{ nodes: ProxyNode[] }>('/nodes');
  }
  latestVersion() {
    return this.get<Record<string, unknown>>('/server/latest-version');
  }

  // ---- Credentials -----------------------------------------------------------
  credentials() {
    return this.get<{ files: Credential[] }>('/credentials');
  }
  credentialModels(name: string) {
    return this.get<{ models: ModelInfo[] }>('/credentials/models', { name });
  }
  uploadCredentialFiles(files: File[]) {
    const form = new FormData();
    files.forEach((f, i) => form.append(`file${i}`, f, f.name));
    return this.request<{ status: string; uploaded?: number; files?: string[] }>('POST', '/credentials', { form });
  }
  uploadCredentialJSON(json: string) {
    return this.request<{ status: string; name?: string }>('POST', '/credentials', { rawBody: json });
  }
  setCredentialDisabled(name: string, disabled: boolean) {
    return this.patch<{ status: string; disabled: boolean }>('/credentials/status', { name, disabled });
  }
  updateCredentialFields(id: string, fields: Record<string, unknown>) {
    return this.patch<{ status: string }>('/credentials/fields', { id, ...fields });
  }
  deleteCredential(id: string) {
    return this.del<{ status: string }>('/credentials', { id });
  }
  refreshCredential(authIndex: string) {
    return this.post<{ ok: boolean; auth?: Credential }>('/credentials/refresh', { auth_index: authIndex });
  }
  refreshAllCredentials() {
    return this.post<{ ok: boolean; results?: { id: string; success: boolean; error?: string }[] }>('/credentials/refresh', { all: true });
  }
  downloadCredential(id: string) {
    return this.request<Blob>('GET', '/credentials/download', { query: { id }, responseType: 'blob' });
  }
  resetCooldown(authIndex: string) {
    return this.post<{ status: string; models?: string[] }>('/routing/cooldown/reset', { auth_index: authIndex });
  }
  fetchQuota(authIndex: string) {
    return this.post<Record<string, unknown>>('/credentials/quota/fetch', { auth_index: authIndex });
  }
  quotaCredentials(query?: Query) {
    return this.get<QuotaList>('/quota/credentials', query);
  }
  importVertex(file: File, location?: string) {
    const form = new FormData();
    form.append('file', file, file.name);
    if (location) form.append('location', location);
    return this.request<{ status: string; 'auth-file'?: string; project_id?: string; email?: string; location?: string }>(
      'POST',
      '/oauth/import',
      { form, query: { provider: 'vertex' } },
    );
  }

  // ---- OAuth -----------------------------------------------------------------
  oauthStart(provider: string) {
    return this.get<{ status: string; url: string; state: string; user_code?: string }>('/oauth/auth-url', { provider });
  }
  oauthStatus(state: string) {
    return this.get<{ status: 'ok' | 'wait' | 'error'; error?: string }>('/oauth/status', { state });
  }
  oauthSubmitCallback(redirectUrl: string, state?: string, provider?: string) {
    return this.post<{ status: string }>('/oauth/callback', { redirect_url: redirectUrl, state, provider });
  }
  oauthCancel(state: string) {
    return this.del<{ status: string; cancelled: boolean }>('/oauth/session', { state });
  }

  // ---- Models ----------------------------------------------------------------
  models(scope: 'available' | 'static' = 'available') {
    return this.get<{ scope: string; models: ModelInfo[] | Record<string, ModelInfo[]> }>('/models', { scope });
  }
  modelDefinitions(channel: string) {
    return this.get<{ channel?: string; models: ModelInfo[] }>(`/routing/model-definitions/${encodeURIComponent(channel)}`);
  }

  // ---- Config ----------------------------------------------------------------
  config() {
    return this.get<Record<string, unknown>>('/config');
  }
  /** Reads a config subtree; returns `fallback` when the path is not set (404). */
  async configGet<T>(path: string, fallback: T): Promise<T> {
    try {
      return await this.get<T>(`/config/${configPath(path)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return fallback;
      throw e;
    }
  }
  configPut(path: string, value: unknown) {
    return this.put<{ status: string }>(`/config/${configPath(path)}`, value);
  }
  configPatch(value: Record<string, unknown>) {
    return this.patch<{ status: string }>('/config', value);
  }
  configDelete(path: string) {
    return this.del<{ status: string }>(`/config/${configPath(path)}`);
  }
  configYAML() {
    return this.request<string>('GET', '/config.yaml', { responseType: 'text' });
  }
  putConfigYAML(yaml: string) {
    return this.request<{ status: string }>('PUT', '/config.yaml', { rawBody: yaml, contentType: 'application/yaml' });
  }

  // ---- Client access keys ----------------------------------------------------
  accessKeys() {
    return this.get<{ items: AccessKey[] }>('/access/api-keys');
  }
  createAccessKey(apiKey: string, displayName?: string) {
    return this.post<{ api_key: AccessKey }>('/access/api-keys', { api_key: apiKey, display_name: displayName || null });
  }
  renameAccessKey(id: number, displayName: string) {
    return this.patch<{ api_key: AccessKey }>('/access/api-keys', { id, display_name: displayName || null });
  }
  deleteAccessKey(id: number) {
    return this.del<{ status: string }>('/access/api-keys', { id });
  }

  // ---- Usage / logs ----------------------------------------------------------
  usageOverview(query?: Query) {
    return this.get<UsageOverview>('/usage/overview', query);
  }
  requestEvents(query?: Query) {
    return this.get<Paged<RequestEvent>>('/request-events', query);
  }
  requestEvent(id: string) {
    return this.get<{ event: RequestEvent; payload_summary?: Record<string, unknown>; log_excerpt?: string[] }>(
      `/request-events/${encodeURIComponent(id)}`,
      { include_payload: true, include_logs: true },
    );
  }
  requestEventFilterOptions(query?: Query) {
    return this.get<Record<string, string[]>>('/request-events/filter-options', query);
  }
  credentialHealth(query?: Query) {
    return this.get<{ items: Record<string, unknown>[] }>('/usage/health/credentials', query);
  }
  logs(query?: Query) {
    return this.get<{ logs: LogLine[]; total: number; limit: number; offset: number }>('/observability/logs', query);
  }
  clearLogs() {
    return this.del<{ success: boolean; removed: number }>('/observability/logs');
  }
}

// ---- Types (only the fields the panel uses) ---------------------------------

export interface HomeNode {
  id: string;
  ip: string;
  port: number;
  is_master: boolean;
  role?: string;
  health?: string;
  healthy?: boolean;
  cpa_count?: number;
  healthy_cpa_count?: number;
  client_count?: number;
  last_seen_at?: string;
  started_at?: string;
}

export interface ProxyNode {
  node_id: string;
  node_name?: string;
  ip?: string;
  healthy?: boolean;
  health?: string;
  state?: string;
  home_id?: string;
  home_ip?: string;
  connected_time?: string;
  last_seen_at?: string;
}

export interface Topology {
  homes: HomeNode[];
  cpas: ProxyNode[];
  master?: HomeNode;
  management?: { home_id?: string; home_ip?: string; home_port?: number };
}

export interface Credential {
  id: string;
  auth_index: string;
  name: string;
  file_name?: string;
  type?: string;
  provider: string;
  label?: string;
  email?: string;
  status?: string;
  status_message?: string;
  disabled?: boolean;
  unavailable?: boolean;
  runtime_only?: boolean;
  source?: string;
  prefix?: string;
  proxy_url?: string;
  priority?: number;
  note?: string;
  websockets?: boolean;
  'disable-cooling'?: boolean;
  'request-retry'?: number;
  created_at?: string;
  updated_at?: string;
  in_flight?: number | null;
  max_in_flight?: number | null;
}

export interface QuotaWindow {
  id: string;
  label?: string;
  status?: string;
  unit?: string;
  used?: number;
  remaining?: number;
  limit?: number;
  remaining_ratio?: number;
  used_ratio?: number;
  is_unlimited?: boolean;
  reset_at?: string | null;
}

export interface QuotaItem {
  credential_id: string;
  auth_index: string;
  provider: string;
  label?: string;
  quota_status?: string;
  freshness?: string;
  collection_status?: string;
  observed_at?: string | null;
  earliest_reset_at?: string | null;
  next_probe_at?: string | null;
  primary_windows?: QuotaWindow[];
  error?: unknown;
}

export interface QuotaList {
  items: QuotaItem[];
  total: number;
  global_summary?: Record<string, number | string | null>;
}

export interface ModelInfo {
  id: string;
  name?: string;
  display_name?: string;
  description?: string;
  type?: string;
  owned_by?: string;
  providers?: string[];
  context_length?: number;
  max_completion_tokens?: number;
  inputTokenLimit?: number;
  outputTokenLimit?: number;
  thinking?: { min?: number; max?: number; levels?: string[]; zero_allowed?: boolean; dynamic_allowed?: boolean };
  supportedInputModalities?: string[];
  user_defined?: boolean;
}

export interface AccessKey {
  id: number;
  api_key: string;
  display_name?: string | null;
  user_id?: number | null;
  channels?: number[];
  model_groups?: number[];
}

export interface Paged<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}

export interface TokenCounts {
  input_tokens?: number;
  output_tokens?: number;
  reasoning_tokens?: number;
  cached_tokens?: number;
  total_tokens?: number;
}

export interface RequestEvent {
  id: string;
  request_id?: string;
  timestamp: string;
  status?: string;
  failed?: boolean;
  status_code?: number | null;
  provider?: string;
  model?: string;
  original_model?: string;
  endpoint?: string;
  event_type?: string;
  credential?: { label?: string; credential_id?: string; provider?: string };
  client?: { client_key_label?: string; client_key_masked?: string; client_ip?: string };
  error?: { message?: string | null; reason?: string | null; status_code?: number | null; body_preview?: string | null };
  tokens?: TokenCounts;
  performance?: { latency_ms?: number | null; ttft_ms?: number | null; tps?: number | null };
  runtime?: { home_id?: string; cpa_node_id?: string; cpa_ip?: string };
}

export interface UsageBucket extends TokenCounts {
  bucket_start: string;
  bucket_end: string;
  request_count: number;
  success_count: number;
  failed_count: number;
}

export interface UsageTop {
  id: string;
  label?: string;
  request_count: number;
  total_tokens?: number;
  failed_count?: number;
  error_rate?: number;
  avg_latency_ms?: number | null;
  last_used_at?: string;
}

export interface UsageOverview {
  range?: Record<string, unknown>;
  live?: { rpm?: number; tpm?: number; error_rate?: number; p50_latency_ms?: number | null; window_seconds?: number };
  totals: TokenCounts & {
    request_count: number;
    success_count: number;
    failed_count: number;
    error_rate?: number;
    success_rate?: number;
    avg_latency_ms?: number | null;
    p95_latency_ms?: number | null;
    active_model_count?: number;
    active_credential_count?: number;
    active_client_key_count?: number;
  };
  trend: UsageBucket[];
  top?: Partial<Record<'models' | 'credentials' | 'client_keys' | 'providers' | 'endpoints' | 'errors', UsageTop[]>>;
}

export interface LogLine {
  id: number;
  timestamp: string;
  level: string;
  line: string;
  request_id?: string;
  home_ip?: string;
  client_ip?: string;
}

// ---- Helpers shared by pages --------------------------------------------------

/** Providers kept in this project. Anything else is hidden from pickers. */
export const SUPPORTED_PROVIDERS = ['antigravity', 'gemini', 'vertex', 'aistudio'] as const;

export function isSupportedProvider(p?: string): boolean {
  if (!p) return false;
  const v = p.toLowerCase();
  return (SUPPORTED_PROVIDERS as readonly string[]).includes(v) || v.startsWith('gemini') || v.startsWith('vertex');
}

/** Generates a client API key like `sk-<48 hex>` using WebCrypto. */
export function generateApiKey(bytes = 24): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return `sk-${Array.from(buf, (b) => b.toString(16).padStart(2, '0')).join('')}`;
}

/** Extracts `state` from a pasted OAuth redirect URL (or returns undefined). */
export function stateFromRedirect(url: string): string | undefined {
  try {
    const u = new URL(url.trim());
    return u.searchParams.get('state') ?? undefined;
  } catch {
    const m = /[?&]state=([^&#\s]+)/.exec(url);
    return m ? decodeURIComponent(m[1]) : undefined;
  }
}

export function maskSecret(s: string, keep = 4): string {
  if (!s) return '';
  if (s.length <= keep * 2) return '•'.repeat(s.length);
  return `${s.slice(0, keep)}…${s.slice(-keep)}`;
}

/**
 * Collapses Home topology for display. After Home pods are replaced, Home keeps
 * rows of the dead replicas (state "stale"/"draining") until garbage collection,
 * and each proxy appears once per Home it was attached to. We keep one row per
 * proxy (preferring the healthy/active one) and separate live from stale Homes.
 */
export function normalizeTopology(t: Topology | undefined): { homes: HomeNode[]; staleHomes: HomeNode[]; proxies: ProxyNode[]; leader?: HomeNode } {
  if (!t) return { homes: [], staleHomes: [], proxies: [] };
  const live = (t.homes ?? []).filter((h) => h.healthy || h.health === 'healthy');
  const stale = (t.homes ?? []).filter((h) => !(h.healthy || h.health === 'healthy'));
  const score = (p: ProxyNode) => (p.healthy ? 4 : 0) + (p.state === 'active' ? 2 : 0) + (p.last_seen_at ? new Date(p.last_seen_at).getTime() / 1e15 : 0);
  const byId = new Map<string, ProxyNode>();
  for (const p of t.cpas ?? []) {
    const prev = byId.get(p.node_id);
    if (!prev || score(p) > score(prev)) byId.set(p.node_id, p);
  }
  const proxies = Array.from(byId.values()).sort((a, b) => (a.node_name ?? a.node_id).localeCompare(b.node_name ?? b.node_id));
  const leader = live.find((h) => h.is_master) ?? (t.master && (t.master.healthy || t.master.health === 'healthy') ? t.master : undefined);
  return { homes: live, staleHomes: stale, proxies, leader };
}
