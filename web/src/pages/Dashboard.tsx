import { Link } from 'react-router-dom';
import { useApi, useAsync } from '../app-context';
import { isSupportedProvider, normalizeTopology, type Credential, type QuotaItem } from '../api/client';
import { fmtCompact, fmtMs, fmtPct, fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Stat } from '../components/ui';
import { RankList, TrendChart } from '../components/charts';
import { QuotaStatusBadge, QuotaWindows } from '../components/quota';
import { IconRefresh } from '../components/icons';

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
  const activeCreds = files.filter((c) => !c.disabled && !c.unavailable && c.status === 'active').length;
  const quotaById = new Map<string, QuotaItem>((quota.data?.items ?? []).map((q) => [q.credential_id, q]));
  const totals = usage.data?.totals;

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

      <div className="grid grid-3" style={{ gridTemplateColumns: 'minmax(0,2fr) minmax(0,1fr)' }}>
        <Card title={tr('请求趋势（24 小时）', 'Requests (24h)')} extra={totals && <span className="faint small">P95 {fmtMs(totals.p95_latency_ms)} · {tr('平均', 'avg')} {fmtMs(totals.avg_latency_ms)}</span>}>
          {usage.error ? <ErrorBox error={usage.error} onRetry={usage.reload} /> : usage.data ? <TrendChart buckets={usage.data.trend} /> : <Loading />}
        </Card>
        <Card title={tr('热门模型', 'Top models')}>
          {usage.data?.top?.models?.length ? (
            <RankList items={usage.data.top.models.slice(0, 6).map((m) => ({ label: m.label || m.id, value: m.request_count }))} />
          ) : usage.data ? (
            <Empty />
          ) : (
            <Loading />
          )}
        </Card>
      </div>

      <div className="grid grid-2">
        <Card title={tr('凭据额度', 'Credential quota')} extra={<Link to="/credentials">{tr('管理凭据 →', 'Manage →')}</Link>}>
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
              {files.slice(0, 6).map((c) => (
                <div key={c.id} className="stack" style={{ gap: 6 }}>
                  <div className="row">
                    <Badge kind="primary">{c.provider}</Badge>
                    <span className="ellipsis spacer" title={c.label || c.name}>
                      {c.label || c.email || c.name}
                    </span>
                    {c.disabled ? <Badge>{t('disabled')}</Badge> : <QuotaStatusBadge item={quotaById.get(c.id)} />}
                  </div>
                  <QuotaWindows item={quotaById.get(c.id)} />
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card title={tr('Home 副本', 'Home replicas')} extra={<Link to="/nodes">{tr('全部节点 →', 'All nodes →')}</Link>}>
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
