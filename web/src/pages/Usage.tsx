import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync } from '../app-context';
import { ApiError, affinitySourceLabel, summarizeError, type RequestAffinity, type RequestEvent } from '../api/client';
import { downloadBlob, fmtCompact, fmtMs, fmtNum, fmtPct, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, CodeBlock, Drawer, Empty, ErrorBox, Loading, Pager, Spinner, Stat } from '../components/ui';
import { RankList, TrendChart } from '../components/charts';
import { IconDownload, IconRefresh } from '../components/icons';

const RANGES = [
  { id: '1h', hours: 1, interval: 'minute' },
  { id: '24h', hours: 24, interval: 'hour' },
  { id: '7d', hours: 168, interval: 'hour' },
  { id: '30d', hours: 720, interval: 'day' },
] as const;

export function UsagePage() {
  const api = useApi();
  const { t, tr } = useI18n();
  const [range, setRange] = useState<(typeof RANGES)[number]['id']>('24h');
  const r = RANGES.find((x) => x.id === range)!;
  const from = useMemo(() => new Date(Date.now() - r.hours * 3600e3).toISOString(), [r.hours]); // eslint-disable-line react-hooks/exhaustive-deps
  const overview = useAsync(() => api.usageOverview({ from, interval: r.interval, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }), [from, r.interval]);
  const tot = overview.data?.totals;
  const top = overview.data?.top;
  const aff = useAsync(() => api.affinityStats(r.hours), [r.hours]);
  const st = aff.data?.stats;

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('usage')}</h1>
          <p>{tr('所有代理节点上报到 Home 的请求记录（存储在 PostgreSQL）。', 'Requests reported by every proxy node to Home (stored in PostgreSQL).')}</p>
        </div>
        <div className="toolbar">
          <div className="seg">
            {RANGES.map((x) => (
              <button key={x.id} className={range === x.id ? 'active' : ''} onClick={() => setRange(x.id)}>
                {x.id}
              </button>
            ))}
          </div>
          <button className="btn" onClick={overview.reload}>
            <IconRefresh />
            {t('refresh')}
          </button>
        </div>
      </div>

      {!!overview.error && <ErrorBox error={overview.error} onRetry={overview.reload} />}
      <div className="grid grid-4">
        <Stat label={tr('请求数', 'Requests')} value={tot ? fmtCompact(tot.request_count) : '…'} sub={tot ? `${tr('失败', 'failed')} ${fmtNum(tot.failed_count)}` : undefined} />
        <Stat label={t('successRate')} value={tot ? fmtPct(tot.request_count ? tot.success_count / tot.request_count : undefined) : '…'} sub={overview.data?.live ? `RPM ${fmtNum(overview.data.live.rpm, 1)} · TPM ${fmtCompact(overview.data.live.tpm)}` : undefined} />
        <Stat label={t('tokens')} value={tot ? fmtCompact(tot.total_tokens) : '…'} sub={tot ? `${tr('输入', 'in')} ${fmtCompact(tot.input_tokens)} · ${tr('输出', 'out')} ${fmtCompact(tot.output_tokens)} · ${tr('思考', 'reasoning')} ${fmtCompact(tot.reasoning_tokens)} · ${tr('缓存命中', 'cached')} ${fmtCompact(tot.cached_tokens)}${tot.input_tokens ? ` (${fmtPct((tot.cached_tokens ?? 0) / tot.input_tokens)})` : ''}` : undefined} />
        <Stat label={t('latency')} value={tot ? fmtMs(tot.avg_latency_ms) : '…'} sub={tot ? `P95 ${fmtMs(tot.p95_latency_ms)}` : undefined} />
      </div>
      <div className="grid grid-4" data-testid="cache-stats">
        <Stat
          label={tr('粘性命中率', 'Sticky hit rate')}
          value={st ? (st.with_key ? fmtPct(st.hit_rate) : '—') : '…'}
          sub={st ? `${tr('命中', 'hit')} ${fmtNum(st.hits)} · ${tr('新绑定', 'new')} ${fmtNum(st.new)} · ${tr('改绑', 're-pinned')} ${fmtNum(st.repins)}` : undefined}
        />
        <Stat
          label={tr('亲和键覆盖', 'Affinity coverage')}
          value={st ? (st.total ? fmtPct(st.with_key / st.total) : '—') : '…'}
          sub={st ? `${tr('无键请求', 'no key')} ${fmtNum(st.none)} · ${tr('活跃绑定', 'active bindings')} ${fmtNum(st.active_bindings)}` : undefined}
        />
        <Stat
          label={tr('缓存命中率（token）', 'Cache hit rate (tokens)')}
          value={tot ? (tot.input_tokens ? fmtPct((tot.cached_tokens ?? 0) / tot.input_tokens) : '—') : '…'}
          sub={tot ? `${tr('缓存', 'cached')} ${fmtCompact(tot.cached_tokens)} / ${tr('输入', 'input')} ${fmtCompact(tot.input_tokens)}` : undefined}
        />
        <Stat
          label={tr('键来源', 'Key sources')}
          value={st ? fmtNum(Object.keys(st.by_source).length) : '…'}
          sub={st ? Object.entries(st.by_source).sort((a, b) => b[1] - a[1]).slice(0, 3).map(([k, v]) => `${affinitySourceLabel(k, tr)} ${fmtNum(v)}`).join(' · ') || '—' : undefined}
        />
      </div>

      <Card title={tr('请求趋势', 'Request trend')}>{overview.data ? <TrendChart buckets={overview.data.trend} /> : <Loading />}</Card>

      <div className="grid grid-3">
        {(
          [
            ['models', tr('模型', 'Models')],
            ['credentials', tr('凭据', 'Credentials')],
            ['client_keys', tr('访问密钥', 'Access keys')],
          ] as const
        ).map(([k, title]) => (
          <Card key={k} title={title} className="card-capped">
            {top?.[k]?.length ? <RankList items={top[k]!.map((x) => ({ label: x.label || x.id, value: x.request_count, sub: x.failed_count ? `✕${x.failed_count}` : undefined }))} /> : overview.data ? <Empty /> : <Loading />}
          </Card>
        ))}
      </div>

      <RequestEvents from={from} />
    </div>
  );
}

function RequestEvents({ from }: { from: string }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { busy, run } = useAction();
  const [status, setStatus] = useState('');
  const [model, setModel] = useState('');
  const [search, setSearch] = useState('');
  const [applied, setApplied] = useState({ search: '' });
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<RequestEvent | null>(null);
  const limit = 25;
  const query = { from, status, model, search: applied.search, limit, offset };
  const events = useAsync(() => api.requestEvents(query), [from, status, model, applied.search, offset]);
  const options = useAsync(() => api.requestEventFilterOptions({ from }), [from]);

  return (
    <Card
      bodyClass="table-wrap"
      title={<h2>{tr('请求记录', 'Request log')}</h2>}
      extra={
        <>
          <form
            className="row"
            onSubmit={(e) => {
              e.preventDefault();
              setOffset(0);
              setApplied({ search });
            }}
          >
            <input className="input input-sm" style={{ width: 200 }} placeholder={tr('搜索请求 ID / 模型 / 节点…', 'Search request id / model / node…')} value={search} onChange={(e) => setSearch(e.target.value)} />
          </form>
          <select className="select input-sm" style={{ width: 120 }} value={status} onChange={(e) => (setOffset(0), setStatus(e.target.value))}>
            <option value="">{tr('全部状态', 'All statuses')}</option>
            <option value="success">{tr('成功', 'Success')}</option>
            <option value="failed">{tr('失败', 'Failed')}</option>
          </select>
          <select className="select input-sm" style={{ width: 180 }} value={model} onChange={(e) => (setOffset(0), setModel(e.target.value))}>
            <option value="">{tr('全部模型', 'All models')}</option>
            {(options.data?.models ?? []).map((m) => (
              <option key={m}>{m}</option>
            ))}
          </select>
          <button
            className="btn btn-sm"
            disabled={busy === 'export'}
            onClick={() =>
              run('export', async () => {
                const blob = await api.request<Blob>('GET', '/request-events/export', { query: { from, status, model, search: applied.search, format: 'csv' }, responseType: 'blob' });
                downloadBlob(blob, 'request-events.csv');
              })
            }
          >
            {busy === 'export' ? <Spinner /> : <IconDownload />}
            CSV
          </button>
        </>
      }
    >
      {events.error ? (
        <div className="card-body">
          <ErrorBox error={events.error} onRetry={events.reload} />
        </div>
      ) : !events.data ? (
        <Loading />
      ) : events.data.items.length === 0 ? (
        <Empty />
      ) : (
        <>
          <table className="table">
            <thead>
              <tr>
                <th>{tr('时间', 'Time')}</th>
                <th>{tr('模型', 'Model')}</th>
                <th>{t('status')}</th>
                <th className="hide-sm">{t('tokens')}</th>
                <th className="hide-sm">{t('latency')}</th>
                <th className="hide-sm">{tr('凭据', 'Credential')}</th>
                <th className="hide-sm">{tr('访问密钥', 'Key')}</th>
              </tr>
            </thead>
            <tbody>
              {events.data.items.map((e) => (
                <tr key={e.id} className="clickable" onClick={() => setSelected(e)} data-testid="event-row">
                  <td className="nowrap small">{fmtTime(e.timestamp)}</td>
                  <td>
                    <span className="mono">{e.model}</span>
                    <div className="faint small">{e.endpoint}</div>
                  </td>
                  <td>
                    <Badge kind={e.failed ? 'err' : 'ok'}>{e.status_code ?? (e.failed ? 'ERR' : 200)}</Badge>
                  </td>
                  <td className="hide-sm small" title={e.tokens?.cached_tokens ? `${tr('其中缓存命中', 'cached')} ${fmtNum(e.tokens.cached_tokens)}` : undefined}>
                    {fmtCompact(e.tokens?.total_tokens)}
                    {!!e.tokens?.cached_tokens && <span className="muted" data-testid="row-cached"> · {tr('缓存', 'cached')} {fmtCompact(e.tokens.cached_tokens)}</span>}
                  </td>
                  <td className="hide-sm small">{fmtMs(e.performance?.latency_ms)}</td>
                  <td className="hide-sm small ellipsis" style={{ maxWidth: 200 }}>
                    {e.credential?.label ?? '—'}
                  </td>
                  <td className="hide-sm small">{e.client?.client_key_label || e.client?.client_key_masked || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <Pager total={events.data.total} limit={limit} offset={offset} onChange={setOffset} />
        </>
      )}
      {selected && <EventDrawer ev={selected} onClose={() => setSelected(null)} />}
    </Card>
  );
}

// Antigravity does not return a thought-token count for Claude models (thinking
// tokens are folded into the output count), so 0 would be misleading.
export function upstreamOmitsReasoningTokens(e: RequestEvent): boolean {
  return (e.credential?.provider ?? e.provider) === 'antigravity' && (e.model ?? '').startsWith('claude') && !e.tokens?.reasoning_tokens;
}

function EventDrawer({ ev, onClose }: { ev: RequestEvent; onClose: () => void }) {
  const api = useApi();
  const { tr } = useI18n();
  const detail = useAsync(() => api.requestEvent(ev.id), [ev.id]);
  const e = detail.data?.event ?? ev;
  return (
    <Drawer title={tr('请求详情', 'Request details')} onClose={onClose}>
      <div className="stack">
        <dl className="kv small">
          <dt>{tr('时间', 'Time')}</dt>
          <dd>{fmtTime(e.timestamp)}</dd>
          <dt>{tr('请求 ID', 'Request ID')}</dt>
          <dd className="mono">{e.request_id}</dd>
          <dt>{tr('状态', 'Status')}</dt>
          <dd>
            <Badge kind={e.failed ? 'err' : 'ok'}>{e.status_code ?? '—'}</Badge>
          </dd>
          <dt>{tr('模型', 'Model')}</dt>
          <dd className="mono">
            {e.model}
            {e.original_model && e.original_model !== e.model ? ` ← ${e.original_model}` : ''}
          </dd>
          <dt>Endpoint</dt>
          <dd className="mono">{e.endpoint}</dd>
          <dt>{tr('凭据', 'Credential')}</dt>
          <dd>
            {e.credential?.label} <span className="faint">({e.credential?.provider})</span>
          </dd>
          <dt>{tr('访问密钥', 'Access key')}</dt>
          <dd>
            {e.client?.client_key_label} <span className="mono faint">{e.client?.client_key_masked}</span>
          </dd>
          <dt>{tr('客户端 IP', 'Client IP')}</dt>
          <dd>{e.client?.client_ip ?? '—'}</dd>
          <dt>{tr('代理节点', 'Proxy node')}</dt>
          <dd className="mono">
            {e.runtime?.cpa_ip} {e.runtime?.cpa_node_id && <span className="faint">({e.runtime.cpa_node_id.slice(0, 8)})</span>}
          </dd>
          <dt>Home</dt>
          <dd className="mono">{e.runtime?.home_id}</dd>
          <dt>Tokens</dt>
          <dd>
            {tr('输入', 'in')} {fmtNum(e.tokens?.input_tokens)} · {tr('输出', 'out')} {fmtNum(e.tokens?.output_tokens)} · {tr('思考', 'reasoning')}{' '}
            {upstreamOmitsReasoningTokens(e) ? (
              <span className="faint" data-testid="reasoning-not-reported">
                {tr('上游未报告', 'not reported by upstream')}
              </span>
            ) : (
              fmtNum(e.tokens?.reasoning_tokens)
            )}{' '}
            · {tr('缓存', 'cached')} {fmtNum(e.tokens?.cached_tokens)}
          </dd>
          <dt>{tr('思考参数', 'Thinking')}</dt>
          <dd data-testid="thinking-detail">
            <span className="mono">{e.thinking_params || e.reasoning_effort || tr('未发送（模型默认）', 'not sent (model default)')}</span>
            {e.thinking_note && (
              <div style={{ marginTop: 4 }}>
                <Badge kind="warn">{tr('已调整', 'adjusted')}</Badge> <span className="small">{e.thinking_note}</span>
              </div>
            )}
          </dd>
          <dt>{tr('粘性会话', 'Sticky session')}</dt>
          <dd data-testid="affinity-detail">
            <AffinityInfo requestId={e.request_id} />
          </dd>
          <dt>{tr('性能', 'Performance')}</dt>
          <dd>
            {tr('总耗时', 'latency')} {fmtMs(e.performance?.latency_ms)} · TTFT {fmtMs(e.performance?.ttft_ms)} · {fmtNum(e.performance?.tps, 1)} tok/s
          </dd>
        </dl>
        {e.failed && <ErrorSection ev={e} />}
        <RequestLogSection ev={e} />
        {detail.loading && <Loading />}
        {detail.data?.log_excerpt && detail.data.log_excerpt.length > 0 && (
          <div>
            <h3 style={{ marginBottom: 6 }}>{tr('日志片段', 'Log excerpt')}</h3>
            <CodeBlock text={detail.data.log_excerpt.join('\n')} title={tr('日志片段', 'Log excerpt')} />
          </div>
        )}
      </div>
    </Drawer>
  );
}

function ErrorSection({ ev }: { ev: RequestEvent }) {
  const { tr } = useI18n();
  const sum = summarizeError(ev.error);
  return (
    <div className="stack" style={{ gap: 8 }} data-testid="error-section">
      <div className="alert err">
        <div className="row" style={{ gap: 6 }}>
          <b>{tr('错误', 'Error')}</b>
          {sum.title && <Badge kind="err">{sum.title}</Badge>}
          {ev.error?.upstream_status_code != null && <span className="small">{tr('上游状态', 'upstream')} {ev.error.upstream_status_code}</span>}
          {sum.reasons.map((r) => (
            <span key={r} className="tag">
              {r}
            </span>
          ))}
        </div>
        {sum.message && (
          <div className="wrap-text small" style={{ marginTop: 6 }} data-testid="error-message">
            {sum.message}
          </div>
        )}
      </div>
      {ev.error?.body_preview && (
        <div>
          <div className="row" style={{ gap: 8, marginBottom: 6 }}>
            <h3 style={{ margin: 0 }}>{tr('上游响应体', 'Upstream response body')}</h3>
            {ev.error.body_truncated ? (
              <span className="tag" data-testid="error-body-truncated" title={tr('超过“错误响应体保存上限”，只保存了前面部分', 'Exceeded the error body cap; only the beginning was stored')}>
                {tr(`已截断（原始 ${fmtBytes(ev.error.body_size)}，保存 ${fmtBytes(ev.error.body_preview.length)}）`, `truncated (${fmtBytes(ev.error.body_size)} original, ${fmtBytes(ev.error.body_preview.length)} stored)`)}
              </span>
            ) : (
              ev.error.body_size != null && <span className="small muted">{fmtBytes(ev.error.body_size)}</span>
            )}
          </div>
          <CodeBlock text={ev.error.body_preview} title={tr('上游响应体', 'Upstream response body')} testId="error-body" />
        </div>
      )}
    </div>
  );
}

/** Full request log (request/response headers and bodies) when detailed request logging is on. */
function RequestLogSection({ ev }: { ev: RequestEvent }) {
  const api = useApi();
  const { tr } = useI18n();
  const rl = ev.related?.request_log;
  const [state, setState] = useState<{ loading?: boolean; text?: string; error?: string }>({});
  if (!rl?.request_id) return null;
  const load = async () => {
    setState({ loading: true });
    try {
      setState({ text: await api.requestLog(rl.request_id!, rl.home_ip, rl.home_port) });
    } catch (e) {
      setState({
        error:
          e instanceof ApiError && e.status === 404
            ? tr('没有找到该请求的详细日志：需要在“设置”里开启“详细请求日志”，之后的新请求才会记录请求/响应头和内容。', 'No detailed log for this request. Enable "Detailed request logs" in Settings; new requests will then include headers and bodies.')
            : e instanceof Error
              ? e.message
              : String(e),
      });
    }
  };
  return (
    <div>
      <div className="row" style={{ marginBottom: 6 }}>
        <h3 className="spacer">{tr('请求 / 响应详情（头与内容）', 'Request / response (headers & bodies)')}</h3>
        {!state.text && (
          <button className="btn btn-sm" onClick={load} disabled={state.loading}>
            {state.loading && <Spinner />}
            {tr('加载完整请求日志', 'Load full request log')}
          </button>
        )}
      </div>
      {state.error && <div className="alert small wrap-text">{state.error}</div>}
      {state.text && <CodeBlock text={state.text} title={tr('请求日志', 'Request log')} maxHeight={520} />}
    </div>
  );
}

function fmtBytes(n?: number | null): string {
  if (n == null) return '?';
  if (n < 1024) return `${n} B`;
  return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB (${n} bytes)`;
}

function AffinityInfo({ requestId }: { requestId?: string }) {
  const api = useApi();
  const { tr } = useI18n();
  const res = useAsync(() => (requestId ? api.requestAffinity(requestId) : Promise.resolve<RequestAffinity>({ found: false })), [requestId]);
  if (res.loading) return <Spinner />;
  const a = res.data;
  if (!a?.found) return <span className="faint">{tr('无记录（旧请求或未启用）', 'not recorded (older request or disabled)')}</span>;
  const kind = a.outcome === 'hit' ? 'ok' : a.outcome === 'repin' ? 'warn' : undefined;
  const label =
    a.outcome === 'hit'
      ? tr('命中绑定凭据', 'sticky hit')
      : a.outcome === 'new'
        ? tr('新建绑定', 'new binding')
        : a.outcome === 'repin'
          ? tr('绑定凭据不可用，已改绑', 'bound credential unavailable, re-pinned')
          : tr('未绑定', 'not sticky');
  return (
    <>
      <Badge kind={kind}>{label}</Badge> <span className="faint">· {tr('键来源', 'key source')}: {affinitySourceLabel(a.source, tr)}</span>
    </>
  );
}
