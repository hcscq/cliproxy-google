import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync } from '../app-context';
import type { RequestEvent } from '../api/client';
import { downloadBlob, fmtCompact, fmtMs, fmtNum, fmtPct, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Drawer, Empty, ErrorBox, Loading, Pager, Spinner, Stat } from '../components/ui';
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
        <Stat label={t('tokens')} value={tot ? fmtCompact(tot.total_tokens) : '…'} sub={tot ? `${tr('输入', 'in')} ${fmtCompact(tot.input_tokens)} · ${tr('输出', 'out')} ${fmtCompact(tot.output_tokens)} · ${tr('思考', 'reasoning')} ${fmtCompact(tot.reasoning_tokens)}` : undefined} />
        <Stat label={t('latency')} value={tot ? fmtMs(tot.avg_latency_ms) : '…'} sub={tot ? `P95 ${fmtMs(tot.p95_latency_ms)}` : undefined} />
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
          <Card key={k} title={title}>
            {top?.[k]?.length ? <RankList items={top[k]!.slice(0, 8).map((x) => ({ label: x.label || x.id, value: x.request_count, sub: x.failed_count ? `✕${x.failed_count}` : undefined }))} /> : overview.data ? <Empty /> : <Loading />}
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
                <tr key={e.id} className="clickable" onClick={() => setSelected(e)}>
                  <td className="nowrap small">{fmtTime(e.timestamp)}</td>
                  <td>
                    <span className="mono">{e.model}</span>
                    <div className="faint small">{e.endpoint}</div>
                  </td>
                  <td>
                    <Badge kind={e.failed ? 'err' : 'ok'}>{e.status_code ?? (e.failed ? 'ERR' : 200)}</Badge>
                  </td>
                  <td className="hide-sm small">{fmtCompact(e.tokens?.total_tokens)}</td>
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
            {tr('输入', 'in')} {fmtNum(e.tokens?.input_tokens)} · {tr('输出', 'out')} {fmtNum(e.tokens?.output_tokens)} · {tr('思考', 'reasoning')} {fmtNum(e.tokens?.reasoning_tokens)} · {tr('缓存', 'cached')} {fmtNum(e.tokens?.cached_tokens)}
          </dd>
          <dt>{tr('性能', 'Performance')}</dt>
          <dd>
            {tr('总耗时', 'latency')} {fmtMs(e.performance?.latency_ms)} · TTFT {fmtMs(e.performance?.ttft_ms)} · {fmtNum(e.performance?.tps, 1)} tok/s
          </dd>
        </dl>
        {e.failed && (
          <div className="alert err small">
            <b>{e.error?.reason || tr('错误', 'Error')}</b>
            <div className="break">{e.error?.message}</div>
            {e.error?.body_preview && <pre className="code-block" style={{ marginTop: 8 }}>{e.error.body_preview}</pre>}
          </div>
        )}
        {detail.loading && <Loading />}
        {detail.data?.log_excerpt && detail.data.log_excerpt.length > 0 && (
          <div>
            <h3 style={{ marginBottom: 6 }}>{tr('日志片段', 'Log excerpt')}</h3>
            <pre className="code-block">{detail.data.log_excerpt.join('\n')}</pre>
          </div>
        )}
      </div>
    </Drawer>
  );
}
