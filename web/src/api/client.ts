// Typed client for the Home Management API (v8). Every request carries the
// management key; the key never leaves the browser except as a request header.

export const BULK_CHUNK = 500;

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
  /** Redis / usage stream / PostgreSQL pool status as seen by the serving Home. */
  infra() {
    return this.get<InfraStatus>('/infra');
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
  uploadCredentialFiles(files: File[], egressId?: string, allowPrimary?: boolean) {
    const form = new FormData();
    files.forEach((f, i) => form.append(`file${i}`, f, f.name));
    return this.request<{ status: string; uploaded?: number; files?: string[] }>('POST', '/credentials', { form, query: { egress_id: egressId, allow_primary: allowPrimary === undefined ? undefined : String(allowPrimary) } });
  }
  importRefreshTokens(body: { text?: string; items?: unknown[]; dry_run?: boolean; egress_id?: string; allow_primary?: boolean; reserve?: boolean; health_check?: boolean; batch_id?: string }) {
    return this.post<{ status: string; dry_run: boolean; summary: RTImportSummary; results: RTImportResult[]; batch_id?: string; health_batch_id?: string }>('/credentials/import/refresh-tokens', body);
  }
  uploadCredentialJSON(json: string, egressId?: string, allowPrimary?: boolean) {
    return this.request<{ status: string; name?: string }>('POST', '/credentials', { rawBody: json, query: { egress_id: egressId, allow_primary: allowPrimary === undefined ? undefined : String(allowPrimary) } });
  }
  setCredentialDisabled(name: string, disabled: boolean) {
    return this.patch<{ status: string; disabled: boolean; requested_disabled?: boolean; applied?: boolean; state?: string; status_message?: string; reserved?: boolean }>('/credentials/status', { name, disabled });
  }
  updateCredentialFields(id: string, fields: Record<string, unknown>) {
    return this.patch<{ status: string }>('/credentials/fields', { id, ...fields });
  }
  setRequestCap(ids: string[], mode: 'inherit' | 'custom' | 'unlimited', hourly = 0, daily = 0) {
    return this.post<{ status: string; updated: number; unchanged: number; failed: string[] }>('/credentials/request-cap', { ids, mode, hourly, daily });
  }
  forbiddenCredentials() {
    return this.get<{ total: number; categories: ForbiddenCategory[] }>('/credentials/forbidden');
  }
  purgeForbiddenCredentials(ids: string[], dryRun = false) {
    return this.post<{ status: string; dry_run: boolean; deleted?: string[]; skipped?: string[]; failed?: string[]; backup_prefix: string }>('/credentials/forbidden/purge', { ids, dry_run: dryRun });
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
  resetCooldown(authIndex: string, model?: string) {
    return this.post<{ status: string; models?: string[] }>('/routing/cooldown/reset', model ? { auth_index: authIndex, model } : { auth_index: authIndex });
  }
  fetchQuota(authIndex: string) {
    return this.post<Record<string, unknown>>('/credentials/quota/fetch', { auth_index: authIndex });
  }
  egressStatus() {
    return this.get<{ items: EgressStatus[] }>('/proxy/egress');
  }
  egressRebalance(dryRun: boolean, allowPrimary?: boolean) {
    return this.post<{ dry_run: boolean; moves: EgressMove[]; applied?: number }>('/proxy/egress/rebalance', { dry_run: dryRun, allow_primary: allowPrimary });
  }
  async egressAssign(ids: string[], egressId: string, allowPrimary?: boolean, onProgress?: (done: number, total: number) => void) {
    const out = { applied: 0, failed: [] as { id: string; error: string }[] };
    for (let i = 0; i < ids.length; i += BULK_CHUNK) {
      const r = await this.post<{ applied: number; failed: { id: string; error: string }[] }>('/proxy/egress/assign', { ids: ids.slice(i, i + BULK_CHUNK), egress_id: egressId, allow_primary: allowPrimary });
      out.applied += r.applied ?? 0;
      out.failed.push(...(r.failed ?? []));
      onProgress?.(Math.min(i + BULK_CHUNK, ids.length), ids.length);
    }
    return out;
  }
  egressSettings() {
    return this.get<{ allow_primary: boolean; max_per_egress?: number; disabled?: string[] }>('/proxy/egress/settings');
  }
  saveEgressSettings(v: { allow_primary: boolean; max_per_egress?: number; disabled?: string[] }) {
    return this.request<{ allow_primary: boolean; max_per_egress?: number; disabled?: string[] }>('PUT', '/proxy/egress/settings', { body: v });
  }
  egressAck(id: string) {
    return this.post<{ status: string }>('/proxy/egress/ack', { id });
  }
  egressForget(id: string) {
    return this.post<{ status: string }>('/proxy/egress/forget', { id });
  }
  quotaCredentials(query?: Query) {
    return this.get<QuotaList>('/quota/credentials', query);
  }
  /** All quota snapshots. The server caps `limit` at 200, so page with offset. */
  async quotaCredentialsAll(query?: Query): Promise<QuotaList> {
    const pageSize = 200;
    const first = await this.quotaCredentials({ ...query, limit: pageSize, offset: 0 });
    const items = [...(first.items ?? [])];
    while (items.length < (first.total ?? 0) && items.length < 20000) {
      const next = await this.quotaCredentials({ ...query, limit: pageSize, offset: items.length });
      if (!next.items?.length) break;
      items.push(...next.items);
    }
    return { ...first, items };
  }
  /** Ask Home to re-probe quota now (empty ids = every eligible credential). */
  collectQuota(credentialIds?: string[]) {
    return this.post<{ accepted: number; running: boolean }>('/quota/collect', credentialIds?.length ? { credential_ids: credentialIds } : {});
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
  oauthStart(provider: string, egressId?: string, allowPrimary?: boolean) {
    return this.get<{ status: string; url: string; state: string; user_code?: string }>('/oauth/auth-url', { provider, egress_id: egressId, allow_primary: allowPrimary === undefined ? undefined : String(allowPrimary) });
  }
  oauthStatus(state: string) {
    return this.get<OAuthResult & { status: 'ok' | 'wait' | 'error'; error?: string }>('/oauth/status', { state });
  }
  oauthSubmitCallback(redirectUrl: string, state?: string, provider?: string) {
    return this.post<OAuthResult & { status: string }>('/oauth/callback', { redirect_url: redirectUrl, state, provider });
  }
  oauthCancel(state: string) {
    return this.del<{ status: string; cancelled: boolean }>('/oauth/session', { state });
  }

  // ---- Models ----------------------------------------------------------------
  models(scope: 'available' | 'static' = 'available') {
    return this.get<{ scope: string; models: ModelInfo[] | Record<string, ModelInfo[]> }>('/models', { scope });
  }
  antigravityCatalog() {
    return this.get<CatalogResponse>('/models/catalog');
  }
  refreshSubscriptionTiers(ids: string[]) {
    return this.post<{ results: { id: string; ok: boolean; tier?: string; tier_name?: string }[] }>('/auth-files/subscription/refresh', { ids });
  }
  refreshAntigravityCatalog(authId?: string) {
    return this.post<CatalogResponse>('/models/catalog/refresh', authId ? { auth_id: authId } : {});
  }
  modelHealth() {
    return this.get<ModelHealthResponse>('/models/health');
  }
  retestModel(model: string) {
    return this.post<{ result: ModelHealthCheck; model?: ModelHealth }>('/models/health/retest', { model });
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
  // ---- Scale: server-side search, bulk, rules, summaries, import/export ----
  searchCredentials(q: CredentialQuery & { page?: number; page_size?: number }) {
    return this.get<CredentialSearch>('/credentials/search', q as Query);
  }
  credentialCallStats(id: string, window: StatsWindow) {
    return this.get<{ window: StatsWindow; total: CallStats; models: (CallStats & { model: string })[] }>(`/credentials/${encodeURIComponent(id)}/call-stats`, { stats_window: window });
  }
  credentialIds(q: CredentialQuery) {
    return this.get<{ ids: string[]; total: number }>('/credentials/search', { ...(q as Query), ids_only: '1' });
  }
  // Large selections are sent in chunks (BULK_CHUNK ids per request) so
  // thousands of credentials neither hit request limits nor block Home for long.
  startHealthCheck(ids: string[]) {
    return this.post<{ status: string; accepted: number; failed: string[] }>('/credentials/health-check', { ids });
  }
  healthBatch(batchId: string) {
    return this.get<{ batch_id: string; imported: number; summary: HealthSummary; items: HealthItem[] }>('/credentials/health-check/batch', { batch_id: batchId });
  }
  exportHealth(q: { batch_id?: string; status?: 'fail' | 'all'; ids?: string; format?: 'csv' | 'text' }) {
    return q.format === 'text'
      ? this.request<string>('GET', '/credentials/health-check/export', { query: q as Query, responseType: 'text' })
      : this.request<Blob>('GET', '/credentials/health-check/export', { query: q as Query, responseType: 'blob' });
  }
  reserveStats() {
    return this.get<{ stats: ReserveStats; events: ReserveEvent[] }>('/credentials/reserve');
  }
  async bulkCredentials(body: { ids: string[]; action: BulkCredentialAction; tags?: string[]; group_id?: number }, onProgress?: (done: number, total: number) => void) {
    type R = { status: string; updated?: number; unchanged?: number; failed?: string[]; result?: BulkResult };
    const out: R = { status: 'ok' };
    for (let i = 0; i < body.ids.length || i === 0; i += BULK_CHUNK) {
      const r = await this.post<R>('/credentials/bulk', { ...body, ids: body.ids.slice(i, i + BULK_CHUNK) });
      out.status = r.status;
      if (r.updated !== undefined) out.updated = (out.updated ?? 0) + r.updated;
      if (r.unchanged !== undefined) out.unchanged = (out.unchanged ?? 0) + r.unchanged;
      if (r.failed?.length) out.failed = [...(out.failed ?? []), ...r.failed];
      if (r.result) {
        const prev = out.result as Record<string, number> | undefined;
        const cur = r.result as unknown as Record<string, number>;
        const merged: Record<string, number> = { ...(prev ?? {}) };
        for (const k of Object.keys(cur)) merged[k] = typeof cur[k] === 'number' ? (merged[k] ?? 0) + cur[k] : cur[k];
        out.result = merged as unknown as BulkResult;
      }
      onProgress?.(Math.min(i + BULK_CHUNK, body.ids.length), body.ids.length);
      if (body.ids.length === 0) break;
    }
    return out;
  }
  groupsSummary() {
    return this.get<GroupsSummary>('/groups/summary');
  }
  credentialGroupMembersPaged(id: number, q: { q?: string; source?: string; page?: number; page_size?: number }) {
    return this.get<MemberPage>(`/channel-groups/${id}/members`, q as Query);
  }
  credentialGroupMemberIds(id: number, q: { q?: string; source?: string }) {
    return this.get<{ ids: string[]; total: number }>(`/channel-groups/${id}/members`, { ...(q as Query), ids_only: '1' });
  }
  bulkCredentialGroupMembers(id: number, change: { add?: string[]; remove?: string[] }) {
    return this.post<{ status: string; result: BulkResult }>(`/channel-groups/${id}/members/bulk`, change);
  }
  groupRule(id: number) {
    return this.get<{ rule: GroupRule | null; enabled: boolean; matched: number; updated_at?: string }>(`/channel-groups/${id}/rule`);
  }
  saveGroupRule(id: number, rule: GroupRule, enabled: boolean) {
    return this.put<{ status: string; matched: number }>(`/channel-groups/${id}/rule`, { rule, enabled });
  }
  deleteGroupRule(id: number) {
    return this.del<{ status: string }>(`/channel-groups/${id}/rule`);
  }
  previewGroupRule(rule: GroupRule) {
    return this.post<{ matched: number; sample: { id: string; label: string; provider: string }[]; empty: boolean }>('/group-rules/preview', { rule });
  }
  bulkModelGroupMembers(id: number, change: { add?: string[]; remove?: string[]; channels?: number[] }) {
    return this.post<{ status: string; result: BulkResult }>(`/model-groups/${id}/members/bulk`, change);
  }
  searchAccessKeys(q: KeyQuery & { page?: number; page_size?: number }) {
    return this.get<{ items: AccessKey[]; total: number; all: number; page: number; page_size: number }>('/access/api-keys/search', q as Query);
  }
  accessKeyIds(q: KeyQuery) {
    return this.get<{ ids: number[]; total: number }>('/access/api-keys/search', { ...(q as Query), ids_only: '1' });
  }
  bulkAccessKeys(change: KeyScopeChange) {
    return this.post<{ status: string; updated: number; unchanged: number }>('/access/api-keys/bulk', change);
  }
  exportGroups(format: 'json' | 'csv') {
    return this.request<Blob>('GET', '/groups/export', { query: { format }, responseType: 'blob' });
  }
  importGroups(body: { format: 'json' | 'csv'; data: string; mode: 'merge' | 'replace'; dry_run?: boolean }) {
    return this.post<{ status: string; report: ImportReport }>('/groups/import', body);
  }

  /** Binds a key to credential groups / model groups (empty arrays = unrestricted). */
  setAccessKeyScope(id: number, scope: { channels?: number[]; model_groups?: number[] }) {
    return this.patch<{ api_key: AccessKey }>('/access/api-keys', { id, ...scope });
  }

  // ---- credential groups (backend: channel groups) ----
  credentialGroups() {
    return this.get<{ channel_groups: CredentialGroup[] }>('/channel-groups');
  }
  createCredentialGroup(name: string) {
    return this.post<{ channel_group: CredentialGroup }>('/channel-groups', { channel_name: name, enabled: true });
  }
  updateCredentialGroup(id: number, patch: { channel_name?: string; enabled?: boolean }) {
    return this.patch<{ channel_group: CredentialGroup }>(`/channel-groups/${id}`, patch);
  }
  deleteCredentialGroup(id: number) {
    return this.del<{ status: string }>(`/channel-groups/${id}`);
  }
  credentialGroupMembers() {
    return this.get<{ channel_group_details: CredentialGroupMember[] }>('/channel-group-details');
  }
  addCredentialGroupMember(groupId: number, authId: string) {
    return this.post<{ channel_group_detail: CredentialGroupMember }>('/channel-group-details', { channel_group_id: groupId, auth_id: authId });
  }
  removeCredentialGroupMember(memberId: number) {
    return this.del<{ status: string }>(`/channel-group-details/${memberId}`);
  }

  // ---- model groups ----
  modelGroups() {
    return this.get<{ model_groups: ModelGroup[] }>('/model-groups');
  }
  createModelGroup(name: string) {
    return this.post<{ model_group: ModelGroup }>('/model-groups', { group_name: name, enabled: true });
  }
  updateModelGroup(id: number, patch: { group_name?: string; enabled?: boolean }) {
    return this.patch<{ model_group: ModelGroup }>(`/model-groups/${id}`, patch);
  }
  deleteModelGroup(id: number) {
    return this.del<{ status: string }>(`/model-groups/${id}`);
  }
  modelGroupMembers() {
    return this.get<{ model_group_details: ModelGroupMember[] }>('/model-group-details');
  }
  modelGroupMembersOf(groupId: number) {
    return this.get<{ model_group_details: ModelGroupMember[] }>('/model-group-details', { model_group_id: groupId });
  }
  addModelGroupMember(groupId: number, modelId: string, channels: number[] = []) {
    return this.post<{ model_group_detail: ModelGroupMember }>('/model-group-details', { model_group_id: groupId, model_id: modelId, channels });
  }
  updateModelGroupMember(memberId: number, channels: number[]) {
    return this.patch<{ model_group_detail: ModelGroupMember }>(`/model-group-details/${memberId}`, { channels });
  }
  removeModelGroupMember(memberId: number) {
    return this.del<{ status: string }>(`/model-group-details/${memberId}`);
  }
  deleteAccessKey(id: number) {
    return this.del<{ status: string }>('/access/api-keys', { id });
  }

  // ---- Usage / logs ----------------------------------------------------------
  usageRetention() {
    return this.get<{ configured: number; status: UsageRetentionStatus }>('/usage/retention');
  }
  affinityStats(hours: number) {
    return this.get<{ hours: number; stats: AffinityStats }>('/usage/affinity', { hours });
  }
  requestAffinity(requestId: string) {
    return this.get<RequestAffinity>('/usage/request-affinity', { request_id: requestId });
  }
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
  /** Raw request log (headers + bodies) when detailed request logging is enabled. */
  requestLog(requestId: string, homeIp?: string, homePort?: number) {
    return this.request<string>('GET', `/observability/logs/requests/${encodeURIComponent(requestId)}`, {
      query: { home_ip: homeIp, home_port: homePort },
      responseType: 'text',
    });
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
  /** Home the leader assigned this proxy to (null: none yet, rendezvous fallback). */
  assigned_home_id?: string | null;
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

export type ModelHealthStatus = 'available' | 'partial' | 'cooling' | 'rejected' | 'retired' | 'removed' | 'unknown';

export interface ModelHealthCheck {
  outcome: 'ok' | 'retired' | 'rejected' | 'quota' | 'capacity' | 'auth_error' | 'error' | string;
  http_status?: number;
  message?: string;
  checked_at: string;
  details?: { auth_id: string; label: string; outcome: string; http_status: number; message?: string; latency_ms: number }[];
}

export interface ModelHealthCredential {
  label: string;
  state: 'ok' | 'cooling' | 'error' | string;
  http_status?: number;
  message?: string;
  next_retry_after?: string;
  updated_at?: string;
  in_catalog: boolean;
  quota_remaining?: number;
  quota_reset_time?: string;
  removed_at?: string;
}

export interface ModelHealth {
  id: string;
  display_name?: string;
  status: ModelHealthStatus;
  reason?: string;
  hidden: boolean;
  in_catalog: boolean;
  registered: boolean;
  tag_title?: string;
  tag_description?: string;
  deprecated_to?: string;
  credentials: ModelHealthCredential[];
  last_check?: ModelHealthCheck;
  last_error?: string;
  last_error_at?: string;
  notes?: string[];
}

export interface ModelHealthResponse {
  models: ModelHealth[];
  generated_at: string;
  catalog_fetched_at?: string;
  missing_switch_targets?: string[];
}

/** Toggles `model` in the antigravity list of an oauth-excluded-models map. */
export function toggleExcludedModel(map: Record<string, string[]>, model: string, hide: boolean): Record<string, string[]> {
  const next: Record<string, string[]> = { ...map };
  const list = (next.antigravity ?? []).filter((m) => m.toLowerCase() !== model.toLowerCase());
  if (hide) list.push(model);
  next.antigravity = list;
  return next;
}

export interface ModelCooldown {
  model: string;
  cooling: boolean;
  status_message?: string;
  quota_exceeded?: boolean;
  next_retry_after?: string;
  http_status?: number;
  updated_at?: string;
  /** Per-model 429 cooldown managed by the Home quota prober. */
  probe_managed?: boolean;
  /** Machine code of the last error, e.g. quota_bucket_exhausted. */
  error_code?: string;
  /** Blocked because the credential's quota probe shows this model's bucket at 0%. */
  bucket_exhausted?: boolean;
  /** Explicit per-model QUOTA_EXHAUSTED (hidden per-model quota window). */
  model_quota?: boolean;
  quota_short_cooldown_seconds?: number;
  quota_probe_interval_seconds?: number;
  probe?: QuotaProbeStatus;
}

export interface QuotaProbeStatus {
  at: string;
  result: 'ok' | 'quota' | 'error';
  http_status?: number;
  next_probe: string;
  detail?: string;
}

/** A credential is usable unless disabled or blocked as a whole; per-model
 * cooldowns (quota on one model, capacity) only affect those models. */
export function credentialUsable(c: Credential): boolean {
  return !c.disabled && !c.unavailable && !c.credential_scope_error;
}

/** Models currently cooling down on this credential. */
export function coolingModels(c: Credential): ModelCooldown[] {
  return (c.model_cooldowns ?? []).filter((m) => m.cooling);
}

export interface CredentialHealth {
  family: string;
  label: string;
  state: string;
  weight: number;
  ttfb_ms: number;
  fail_ratio: number;
}

export interface Credential {
  id: string;
  /** Degraded (credential, model-family) pairs, cluster-wide. */
  health?: CredentialHealth[];
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
  /** Set when Home auto-disabled the credential for an account-level 403. */
  auto_disabled_reason?: string;
  /** Set when Home paused the credential because its egress is down. */
  auto_paused_reason?: string;
  /** Sticky egress exit IP / name. */
  egress_ip?: string;
  egress_id?: string;
  /** loadCodeAssist paidTier id / name, e.g. g1-pro-tier / Google AI Pro. */
  subscription_tier?: string;
  subscription_tier_name?: string;
  subscription_checked_at?: string;
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
  next_retry_after?: string;
  model_cooldowns?: ModelCooldown[];
  credential_scope_error?: boolean;
  tried_models_all_cooling?: boolean;
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
  /** Failed attempt whose request later succeeded on another attempt. */
  retried_ok?: { auth_index?: string; seq?: number; status_code?: number; at?: string } | null;
  provider?: string;
  model?: string;
  original_model?: string;
  endpoint?: string;
  event_type?: string;
  credential?: { label?: string; credential_id?: string; provider?: string };
  client?: { client_key_label?: string; client_key_masked?: string; client_ip?: string };
  error?: { message?: string | null; reason?: string | null; status_code?: number | null; upstream_status_code?: number | null; body_preview?: string | null; body_truncated?: boolean | null; body_size?: number | null };
  tokens?: TokenCounts;
  performance?: { latency_ms?: number | null; ttft_ms?: number | null; tps?: number | null };
  runtime?: { home_id?: string; cpa_node_id?: string; cpa_ip?: string };
  upstream_status_code?: number | null;
  /** Coarse thinking label (level, or a budget bucketed to a level). */
  reasoning_effort?: string | null;
  /** Exact thinking parameters sent upstream, e.g. "budget=8192,include_thoughts=true". */
  thinking_params?: string | null;
  /** Set when a "thinking off" request could not be fully honoured on this model. */
  thinking_note?: string | null;
  related?: { request_log?: { available?: boolean; request_id?: string; home_ip?: string; home_port?: number } };
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
    /** failed attempts whose request later succeeded (excluded from failed_count) */
    retried_ok_count?: number;
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
    // A restarted connection may re-enroll with a new ephemeral identity; the pod
    // name is the stable key while the old identity waits for garbage collection.
    const key = p.node_name || p.node_id;
    const prev = byId.get(key);
    if (!prev || score(p) > score(prev)) byId.set(key, p);
  }
  const proxies = Array.from(byId.values()).sort((a, b) => (a.node_name ?? a.node_id).localeCompare(b.node_name ?? b.node_id));
  const leader = live.find((h) => h.is_master) ?? (t.master && (t.master.healthy || t.master.health === 'healthy') ? t.master : undefined);
  return { homes: live, staleHomes: stale, proxies, leader };
}

/** Human summary of an upstream error body (Google style {error:{code,message,status,details}}). */
export function summarizeError(err?: RequestEvent['error']): { title: string; message: string; reasons: string[] } {
  const out = { title: err?.reason || '', message: err?.message || '', reasons: [] as string[] };
  const body = err?.body_preview?.trim() || (err?.message?.trim().startsWith('{') ? err.message.trim() : undefined);
  if (body) {
    try {
      const j = JSON.parse(body);
      const e = (j && typeof j === 'object' && 'error' in j ? j.error : j) as Record<string, unknown>;
      if (e && typeof e === 'object') {
        if (typeof e.message === 'string') out.message = e.message;
        const parts = [e.code, e.status].filter((x) => x !== undefined && x !== null && x !== '');
        if (parts.length) out.title = parts.join(' ');
        if (Array.isArray(e.details))
          for (const d of e.details) if (d && typeof d === 'object' && typeof (d as Record<string, unknown>).reason === 'string') out.reasons.push(String((d as Record<string, unknown>).reason));
      }
    } catch {
      // Truncated preview: pull the well-known fields out with regexes.
      const str = (k: string) => body.match(new RegExp(`"${k}"\\s*:\\s*"((?:[^"\\\\]|\\\\.)*)"`))?.[1];
      const msg = str('message');
      if (msg) out.message = msg.replace(/\\"/g, '"');
      const code = body.match(/"code"\s*:\s*(\d+)/)?.[1];
      const status = str('status');
      const parts = [code, status].filter(Boolean);
      if (parts.length) out.title = parts.join(' ');
      for (const m of body.matchAll(/"reason"\s*:\s*"([A-Z0-9_]+)"/g)) out.reasons.push(m[1]);
    }
  }
  return out;
}

export interface CredentialGroup {
  id: number;
  channel_name: string;
  enabled: boolean;
  disabled?: boolean;
  created_at?: string;
  updated_at?: string;
}
export interface CredentialGroupMember {
  id: number;
  channel_group_id: number;
  auth_id: string;
}
export interface ModelGroup {
  id: number;
  group_name: string;
  enabled: boolean;
  disabled?: boolean;
}
export interface ModelGroupMember {
  id: number;
  model_group_id: number;
  model_id: string;
  channels?: number[] | null;
}
export interface CatalogChange {
  model: string;
  at: string;
}
export interface CatalogCredentialStatus {
  auth_id: string;
  label: string;
  status: 'ok' | 'error' | 'pending' | string;
  error?: string;
  http_status?: number;
  model_count: number;
  fetched_at?: string;
  attempted_at?: string;
  next_due_at?: string;
  trigger?: string;
  fetched_by?: string;
  added: CatalogChange[];
  removed: CatalogChange[];
}
export interface CatalogResponse {
  interval_seconds: number;
  leader: boolean;
  credentials: CatalogCredentialStatus[];
}

/** Live availability of a credential group: members that exist and are usable right now. */
export function credentialGroupAvailability(
  groupId: number,
  members: CredentialGroupMember[],
  creds: Credential[],
): { total: number; usable: number; missing: number } {
  const byId = new Map(creds.map((c) => [c.id, c]));
  let total = 0;
  let usable = 0;
  let missing = 0;
  for (const m of members) {
    if (m.channel_group_id !== groupId) continue;
    total++;
    const c = byId.get(m.auth_id);
    if (!c) missing++;
    else if (credentialUsable(c)) usable++;
  }
  return { total, usable, missing };
}

/** Human summary of what an access key may use (Chinese/English via tr). */
export function keyScopeSummary(
  k: { channels?: number[]; model_groups?: number[] },
  cgNames: Map<number, string>,
  mgNames: Map<number, string>,
): { credentials: string[] | null; models: string[] | null } {
  const ch = k.channels ?? [];
  const mg = k.model_groups ?? [];
  return {
    credentials: ch.length ? ch.map((id) => cgNames.get(id) ?? `#${id}`) : null,
    models: mg.length ? mg.map((id) => mgNames.get(id) ?? `#${id}`) : null,
  };
}

// ---- scale types ----
export interface CredentialQuery {
  q?: string;
  provider?: string;
  status?: string;
  /** import health check: pass | fail | running | unchecked */
  health?: string;
  domain?: string;
  tag?: string;
  /** group id, or "none" for credentials in no group */
  group?: string;
  /** hide current members of this credential group */
  exclude_group?: number;
  /** pinned egress id */
  egress?: string;
  /** created (default, newest first) | name | status | quota | last_used | egress */
  sort?: CredentialSortKey;
  order?: 'asc' | 'desc';
  /** call-stats window (default 24h) */
  stats_window?: StatsWindow;
}
export type CredentialSortKey = 'seq' | 'created' | 'name' | 'status' | 'quota' | 'last_used' | 'egress' | 'success' | 'tier';
export type CredentialState = 'ok' | 'cooling' | 'error' | 'disabled' | 'reserve';
export type StatsWindow = '1h' | '24h' | '7d';
export interface CallStats {
  calls: number;
  ok: number;
  /** error class -> count: 429 | 503 | 403 | 404 | 400 | 5xx | net | cancel | other */
  by_class?: Record<string, number>;
}
export type CredentialSearchItem = Credential & { health_check?: HealthInfo; reserved?: boolean; reserved_since?: string; request_cap?: RequestCapUsage; seq?: number; tags?: string[]; group_ids?: number[]; state?: CredentialState; created_at?: string; last_used_at?: string; call_stats?: CallStats };
export interface CredentialSearch {
  items: CredentialSearchItem[];
  total: number;
  all: number;
  page: number;
  page_size: number;
  facets: Record<'provider' | 'status' | 'domain' | 'tag', Record<string, number>> & { health?: Record<string, number> };
}
export type BulkCredentialAction = 'enable' | 'disable' | 'reserve' | 'unreserve' | 'add_tags' | 'remove_tags' | 'set_tags' | 'add_to_group' | 'remove_from_group';
export interface BulkResult {
  added: number;
  removed: number;
  unchanged: number;
}
export interface CredentialGroupSummary {
  id: number;
  channel_name: string;
  enabled: boolean;
  static_count: number;
  rule_count: number;
  total: number;
  usable: number;
  ok: number;
  cooling: number;
  error: number;
  disabled: number;
  missing: number;
  has_rule: boolean;
  keys: number;
}
export interface ModelGroupSummary {
  id: number;
  group_name: string;
  enabled: boolean;
  models: number;
  keys: number;
}
export interface GroupsSummary {
  credential_groups: CredentialGroupSummary[];
  model_groups: ModelGroupSummary[];
  credentials: number;
  ungrouped_credentials: number;
  keys: number;
  unrestricted_keys: number;
  snapshot?: { built_at: string; build_ms: number };
}
export interface MemberRow {
  id: string;
  label: string;
  email: string;
  provider: string;
  state: CredentialState | 'missing';
  source: 'static' | 'rule' | 'both';
  missing: boolean;
}
export interface MemberPage {
  items: MemberRow[];
  total: number;
  page: number;
  page_size: number;
  static_count: number;
  rule_count: number;
}
export interface GroupRule {
  providers?: string[];
  email_pattern?: string;
  label_pattern?: string;
  tags_any?: string[];
  tags_all?: string[];
  min_priority?: number;
  max_priority?: number;
  match_all?: boolean;
}
export interface KeyQuery {
  q?: string;
  channel?: number;
  model_group?: number;
  scope?: '' | 'restricted' | 'unrestricted';
}
export interface KeyScopeChange {
  ids: number[];
  add_channels?: number[];
  remove_channels?: number[];
  add_model_groups?: number[];
  remove_model_groups?: number[];
  clear_channels?: boolean;
  clear_model_groups?: boolean;
}
export interface ImportReport {
  groups_created: number;
  members_added: number;
  members_removed: number;
  models_added: number;
  models_removed: number;
  rules_set: number;
  unresolved: string[];
  dry_run: boolean;
}

/** Drops empty fields so a rule round-trips cleanly. */
export function cleanRule(r: GroupRule): GroupRule {
  const out: GroupRule = {};
  const list = (v?: string[]) => (v ?? []).map((x) => x.trim()).filter(Boolean);
  if (list(r.providers).length) out.providers = list(r.providers);
  if (r.email_pattern?.trim()) out.email_pattern = r.email_pattern.trim();
  if (r.label_pattern?.trim()) out.label_pattern = r.label_pattern.trim();
  if (list(r.tags_any).length) out.tags_any = list(r.tags_any).map((t) => t.toLowerCase());
  if (list(r.tags_all).length) out.tags_all = list(r.tags_all).map((t) => t.toLowerCase());
  if (typeof r.min_priority === 'number' && !Number.isNaN(r.min_priority)) out.min_priority = r.min_priority;
  if (typeof r.max_priority === 'number' && !Number.isNaN(r.max_priority)) out.max_priority = r.max_priority;
  if (r.match_all) out.match_all = true;
  return out;
}

/** Human summary of a rule (Chinese / English). */
export function describeRule(r: GroupRule | null | undefined, tr: (zh: string, en: string) => string): string {
  if (!r) return '';
  const parts: string[] = [];
  if (r.providers?.length) parts.push(tr(`提供方 ∈ {${r.providers.join(', ')}}`, `provider in {${r.providers.join(', ')}}`));
  if (r.email_pattern) parts.push(tr(`邮箱匹配 ${r.email_pattern}`, `email ~ ${r.email_pattern}`));
  if (r.label_pattern) parts.push(tr(`名称匹配 ${r.label_pattern}`, `label ~ ${r.label_pattern}`));
  if (r.tags_any?.length) parts.push(tr(`含任一标签 ${r.tags_any.join('/')}`, `any tag ${r.tags_any.join('/')}`));
  if (r.tags_all?.length) parts.push(tr(`含全部标签 ${r.tags_all.join('+')}`, `all tags ${r.tags_all.join('+')}`));
  if (r.min_priority !== undefined) parts.push(tr(`优先级 ≥ ${r.min_priority}`, `priority ≥ ${r.min_priority}`));
  if (r.max_priority !== undefined) parts.push(tr(`优先级 ≤ ${r.max_priority}`, `priority ≤ ${r.max_priority}`));
  if (!parts.length) return r.match_all ? tr('全部凭据', 'all credentials') : tr('（空规则，不匹配任何凭据）', '(empty rule matches nothing)');
  return parts.join(tr(' 且 ', ' AND '));
}

export interface AffinityStats {
  total: number;
  with_key: number;
  hits: number;
  new: number;
  repins: number;
  none: number;
  hit_rate: number;
  by_source: Record<string, number>;
  active_bindings: number;
}

export interface RequestAffinity {
  found: boolean;
  source?: string;
  outcome?: 'hit' | 'new' | 'repin' | 'none';
  auth_id?: string;
  prev_auth_id?: string;
}

/** Chinese / English label of a sticky-session key source. */
export function affinitySourceLabel(src: string | undefined, tr: (zh: string, en: string) => string): string {
  switch (src) {
    case 'header':
      return tr('会话请求头', 'session header');
    case 'prompt-cache-key':
      return 'prompt_cache_key';
    case 'openai-user':
      return tr('OpenAI user 字段', 'OpenAI user');
    case 'claude-metadata':
      return 'Claude metadata.user_id';
    case 'gemini-cached-content':
      return 'Gemini cachedContent';
    case 'prefix':
      return tr('提示前缀哈希', 'prompt prefix hash');
    default:
      return tr('无（提示过短 / 未提供会话）', 'none (short prompt / no session)');
  }
}

export interface UsagePartitionInfo {
  name: string;
  from?: string;
  to?: string;
  is_default?: boolean;
  rows: number;
  bytes: number;
}

export interface UsageRetentionStatus {
  backend: string;
  partitioned: boolean;
  retention_days: number;
  oldest_timestamp?: string;
  total_rows: number;
  total_bytes: number;
  legacy_table: boolean;
  legacy_rows?: number;
  partitions?: UsagePartitionInfo[];
  last_maintenance?: string;
  last_dropped?: string[];
  last_deleted?: number;
  last_error?: string;
}

export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export interface InfraStatus {
  home: string;
  redis: {
    enabled: boolean;
    healthy: boolean;
    mode: string;
    master?: string;
    role?: string;
    connected_slaves?: string;
    used_memory?: string;
    ping_ms: number;
    sentinels?: { addr: string; master?: string; error?: string }[];
    error?: string;
  };
  usage_stream?: {
    enabled: boolean;
    active: boolean;
    consumer: string;
    length: number;
    pending: number;
    consumers: number;
    appended: number;
    flushed: number;
    fallback: number;
    redelivered: number;
    last_flush_at: string;
    last_error?: string;
    max_len: number;
  };
  postgres: {
    pooler_addr?: string;
    via_pooler: boolean;
    pooler_error?: string;
    main: { MaxOpenConnections: number; OpenConnections: number; InUse: number; Idle: number; WaitCount: number; WaitDuration: number };
    lease?: { OpenConnections: number; InUse: number };
    ping_ms: number;
    ping_error?: string;
  };
}

/** Non-secret result of a completed OAuth login (reported by any Home replica). */
export type OAuthResult = { already_completed?: boolean; credential_id?: string; email?: string; updated_existing?: boolean; seq?: number; reenabled?: boolean };

export interface RTImportResult {
  index: number;
  source?: string;
  token_hint?: string;
  status: 'created' | 'updated' | 'would_create' | 'would_update' | 'failed' | 'skipped' | string;
  code?: string;
  reason?: string;
  warning?: string;
  credential_id?: string;
  email?: string;
  project_id?: string;
  matched_by?: string;
}
export interface RTImportSummary {
  total: number;
  created: number;
  updated: number;
  failed: number;
  skipped: number;
}

export interface EgressStatus {
  id: string;
  name: string;
  ip: string;
  proxy_url: string;
  healthy: boolean;
  failures: number;
  last_check?: string;
  last_error?: string;
  credentials: number;
  paused: number;
  node?: string;
  primary?: boolean;
  public_ip?: string;
  removed?: boolean;
  public_changed?: boolean;
  expected_public_ip?: string;
}
export interface EgressMove {
  credential_id: string;
  seq?: number;
  label: string;
  from: string;
  from_ip: string;
  to: string;
  to_ip: string;
}

export type ForbiddenCred = { id: string; seq?: number; label: string; category: string; message: string; source: string; last_403_at?: string; count_403_7d: number; ok_7d: number; disabled: boolean; persistent: boolean };
export type ForbiddenCategory = { key: string; label: string; count: number; persistent: number; items: ForbiddenCred[] };
export type RequestCapUsage = { mode: 'inherit' | 'custom' | 'unlimited'; tier: string; hourly_cap: number; daily_cap: number; hourly_used: number; daily_used: number; capped_until?: string };

export interface ReserveStats {
  reserved: number;
  ever_reserved: number;
  bans_reserved: number;
  bans_active: number;
  bans_after_promotion: number;
  promotions_24h: number;
  min_active: number;
  active: number;
  eligible: number;
  short: boolean;
  shortfall_since?: number;
  /** Excluding Free tier: schedulable and not in reserve. */
  enabled?: number;
  /** Excluding Free tier: enabled and not cooling. */
  usable?: number;
  /** Promoter compares `enabled` (true) or `usable` (false) with min_active. */
  count_cooled?: boolean;
  last_promotion?: number;
}
export interface ReserveEvent { auth_id: string; seq?: number; kind: string; at: string; was_reserved: boolean; reserved_since?: string; detail?: string }

export interface HealthInfo { status: 'pass' | 'fail' | 'running' | string; categories: string[]; reasons?: string[]; checked_at?: string; tier?: string; evidence?: string }
export interface HealthSummary { total: number; pass: number; fail: number; running: number; by_category: Record<string, number> }
export interface HealthItem extends HealthInfo { auth_id: string; seq?: number; email?: string; batch_id?: string; imported_at?: string; model?: string }
