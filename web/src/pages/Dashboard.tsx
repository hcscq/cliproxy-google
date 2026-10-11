import { Link } from 'react-router-dom';
import { useApi, useAsync } from '../app-context';
import { isSupportedProvider, normalizeTopology, type Credential, type QuotaItem, credentialUsable, coolingModels } from '../api/client';
import { fmtCompact, fmtMs, fmtPct, fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Stat } from '../components/ui';
import { RankList, TrendChart } from '../components/charts';
import { QuotaStatusBadge, QuotaWindows } from '../components/quota';
import { IconRefresh } from '../components/icons';

export const DASHBOARD_TOP_N = 8;

/** Top-N rows by value plus one aggregated "other (N)" row for the rest. */
export function capRank<T extends { label: string; value: number }>(items: T[], n: number, otherLabel: (count: number) => string): { label: string; value: number; sub?: string }[] {
  const sorted = [...items].sort((a, b) => b.value - a.value);
  if (sorted.length <= n) return sorted;
  const rest = sorted.slice(n);
  return [...sorted.slice(0, n), { label: otherLabel(rest.length), value: rest.reduce((a, b) => a + b.value, 0) }];
}

export function DashboardPage() {
  const api = useApi();
  const { t, tr, lang } = useI18n();
  const topo = useAsync(() => api.topology(), [], 15000);
  const creds = useAsync(() => api.credentials(), [], 30000);
  const quota = useAsync(() => api.quotaCredentials({ limit: 100 }), [], 60000);
  const usage = useAsync(() => api.usageOverview({ interval: 'hour' }), [], 60000);
  const failures = useAsync(() => api.requestEvents({ status: 'failed', limit: 5 }), [], 60000);

  const reloadAll = () => Promise.all([topo.reload(), creds.reload(), quota.reload(), usage.reload(), failures.reload()]);

  const { homes, staleHomes, proxies, leader } = normalizeTopology(topo.data);
  const files: Credential[] = (creds.data?.files ?? []).filter((c) => isSupportedProvider(c.provider));
  const activeCreds = files.filter(credentialUsable).length;
  const quotaById = new Map<string, QuotaItem>((quota.data?.items ?? []).map((q) => [q.credential_id, q]));
  const totals = usage.data?.totals;
  // Credentials ordered by 24h request count (unused ones last, by name).
  const credUsage = new Map<string, number>();
  for (const u of usage.data?.top?.credentials ?? []) {
    credUsage.set(u.id, u.request_count);
    if (u.label) credUsage.set(u.label, u.request_count);
  }
  const usageOf = (c: Credential) => credUsage.get(c.id) ?? credUsage.get(c.name) ?? credUsage.get(c.label ?? '') ?? 0;
  const rankedFiles = [...files].sort((a, b) => usageOf(b) - usageOf(a) || (a.label || a.name).localeCompare(b.label || b.name));

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('dashboard')}</h1>
          <p>{tr('集群健康、凭据额度与最近 24 小时的请求情况。', 'Cluster health, credential quota and the last 24 hours of traffic.')}</p>
        </div>
        <button className="btn" onClick={reloadAll}>
          <IconRefresh />
          {t('refresh')}
        </button>
      </div>

      <div className="grid grid-4">
        <Stat
          label={t('homeReplicas')}
          value={topo.data ? String(homes.length) : '…'}
          sub={`${leader ? `${t('leader')}: ${leader.id}` : tr('无主节点', 'No leader elected')}${staleHomes.length ? tr(` · ${staleHomes.length} 个失联待回收`, ` · ${staleHomes.length} stale`) : ''}`}
        />
        <Stat
          label={t('proxyNodes')}
          value={topo.data ? `${proxies.filter((p) => p.healthy).length} / ${proxies.length}` : '…'}
          sub={tr('健康 / 已连接', 'healthy / connected')}
        />
        <Stat
          label={t('credentials')}
          value={creds.data ? `${activeCreds} / ${files.length}` : '…'}
          sub={tr('可用 / 总数', 'usable / total')}
        />
        <Stat
          label={t('requests24h')}
          value={totals ? fmtCompact(totals.request_count) : '…'}
          sub={totals ? `${t('successRate')} ${fmtPct(totals.request_count ? totals.success_count / totals.request_count : undefined)} · ${fmtCompact(totals.total_tokens)} tokens` : undefined}
        />
      </div>

      <div className="grid grid-trend">
        <Card title={tr('请求趋势（24 小时）', 'Requests (24h)')} extra={totals && <span className="faint small">P95 {fmtMs(totals.p95_latency_ms)} · {tr('平均', 'avg')} {fmtMs(totals.avg_latency_ms)}</span>}>
          {usage.error ? <ErrorBox error={usage.error} onRetry={usage.reload} /> : usage.data ? <TrendChart buckets={usage.data.trend} /> : <Loading />}
        </Card>
        <Card title={tr('热门模型', 'Top models')} className="card-capped" extra={<Link to="/usage" data-testid="models-view-all">{tr('查看全部 →', 'View all →')}</Link>}>
          {usage.data?.top?.models?.length ? (
            <RankList
              items={capRank(
                usage.data.top.models.map((m) => ({ label: m.label || m.id, value: m.request_count })),
                DASHBOARD_TOP_N,
                (n) => tr(`其他 (${n})`, `Other (${n})`),
              )}
            />
          ) : usage.data ? (
            <Empty />
          ) : (
            <Loading />
          )}
        </Card>
      </div>

      <div className="grid grid-2">
        <Card title={tr('凭据额度', 'Credential quota')} className="card-capped" extra={<Link to="/credentials" data-testid="creds-view-all">{tr('查看全部 →', 'View all →')}</Link>}>
          {creds.error ? (
            <ErrorBox error={creds.error} onRetry={creds.reload} />
          ) : !creds.data ? (
            <Loading />
          ) : files.length === 0 ? (
            <Empty>
              {tr('还没有凭据。', 'No credentials yet. ')}
              <Link to="/credentials">{t('addCredential')}</Link>
            </Empty>
          ) : (
            <div className="stack">
              {rankedFiles.slice(0, DASHBOARD_TOP_N).map((c) => (
                <div key={c.id} className="stack" style={{ gap: 6 }}>
                  <div className="row">
                    <Badge kind="primary">{c.provider}</Badge>
                    <span className="ellipsis spacer" title={c.label || c.name}>
                      {c.label || c.email || c.name}
                    </span>
                    {c.disabled ? <Badge>{t('disabled')}</Badge> : <QuotaStatusBadge item={quotaById.get(c.id)} />}
                    {!c.disabled && coolingModels(c).length > 0 && (
                      <Badge kind="warn" dot>
                        <span title={coolingModels(c).map((m) => m.model).join(', ')}>
                          {tr(`${coolingModels(c).length} 个模型冷却中`, `${coolingModels(c).length} model(s) cooling`)}
                        </span>
                      </Badge>
                    )}
                  </div>
                  <QuotaWindows item={quotaById.get(c.id)} />
                </div>
              ))}
              {rankedFiles.length > DASHBOARD_TOP_N && (
                <Link to="/credentials" className="list-row muted small" data-testid="creds-other">
                  <span>{tr(`其他 (${rankedFiles.length - DASHBOARD_TOP_N})`, `Other (${rankedFiles.length - DASHBOARD_TOP_N})`)}</span>
                  <span>
                    {tr('请求', 'requests')} {fmtCompact(rankedFiles.slice(DASHBOARD_TOP_N).reduce((a, c) => a + usageOf(c), 0))} →
                  </span>
                </Link>
              )}
            </div>
          )}
        </Card>

        <Card title={tr('Home 副本', 'Home replicas')} className="card-capped" extra={<Link to="/nodes">{tr('全部节点 →', 'All nodes →')}</Link>}>
          {topo.error ? (
            <ErrorBox error={topo.error} onRetry={topo.reload} />
          ) : !topo.data ? (
            <Loading />
          ) : (
            <div>
              {homes.map((h) => (
                <div className="list-row" key={h.id}>
                  <div>
                    <div className="mono">{h.id}</div>
                    <div className="faint small">
                      {t('since')} {fmtTime(h.started_at)} · {t('lastSeen')} {fmtRelative(h.last_seen_at, lang)}
                    </div>
                  </div>
                  <div className="row">
                    <span className="faint small">{tr(`${h.cpa_count ?? 0} 个代理`, `${h.cpa_count ?? 0} proxies`)}</span>
                    {h.is_master ? <Badge kind="primary">{t('leader')}</Badge> : <Badge>{t('follower')}</Badge>}
                    <Badge kind={h.healthy ? 'ok' : 'err'} dot>
                      {h.healthy ? t('healthy') : t('unhealthy')}
                    </Badge>
                  </div>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>

      <Card title={tr('最近失败的请求', 'Recent failed requests')} extra={<Link to="/usage">{tr('查看全部 →', 'View all →')}</Link>} bodyClass="table-wrap">
        {failures.data?.items.length ? (
          <table className="table">
            <thead>
              <tr>
                <th>{tr('时间', 'Time')}</th>
                <th>{tr('模型', 'Model')}</th>
                <th>{t('status')}</th>
                <th>{tr('错误', 'Error')}</th>
              </tr>
            </thead>
            <tbody>
              {failures.data.items.map((e) => (
                <tr key={e.id}>
                  <td className="nowrap">{fmtTime(e.timestamp)}</td>
                  <td className="mono">{e.model}</td>
                  <td>
                    <Badge kind="err">{e.status_code ?? e.error?.status_code ?? '—'}</Badge>
                  </td>
                  <td className="small muted break">{e.error?.message || e.error?.reason || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : failures.data ? (
          <Empty>{tr('没有失败请求 🎉', 'No failures 🎉')}</Empty>
        ) : (
          <Loading />
        )}
      </Card>
    </div>
  );
}
