import { useState } from 'react';
import { useApi, useAsync } from '../app-context';
import { normalizeTopology } from '../api/client';
import { fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Stat, Switch } from '../components/ui';
import { IconRefresh } from '../components/icons';

export function NodesPage() {
  const api = useApi();
  const { t, tr, lang } = useI18n();
  const topo = useAsync(() => api.topology(), [], 10000);
  const [showStale, setShowStale] = useState(false);
  const norm = normalizeTopology(topo.data);
  const { leader, proxies, staleHomes } = norm;
  const homes = showStale ? [...norm.homes, ...staleHomes] : norm.homes;
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('nodes')}</h1>
          <p>{tr('Home 副本通过 PostgreSQL 选主；代理节点启动时用 bootstrap token 自注册，拿到临时身份，心跳停止 10 分钟后自动回收。', 'Home replicas elect a leader via PostgreSQL; proxies self-enroll with the bootstrap token and get ephemeral identities that are garbage-collected 10 minutes after heartbeats stop.')}</p>
        </div>
        <button className="btn" onClick={topo.reload}>
          <IconRefresh />
          {t('refresh')}
        </button>
      </div>
      {!!topo.error && <ErrorBox error={topo.error} onRetry={topo.reload} />}
      <div className="grid grid-3">
        <Stat label={t('leader')} value={<span className="mono" style={{ fontSize: 18 }}>{leader?.id ?? '—'}</span>} sub={leader ? `${t('since')} ${fmtTime(leader.started_at)}` : undefined} />
        <Stat label={t('homeReplicas')} value={String(norm.homes.length)} sub={staleHomes.length ? tr(`另有 ${staleHomes.length} 个失联副本等待回收`, `${staleHomes.length} stale replica(s) awaiting GC`) : tr('全部在线', 'all live')} />
        <Stat label={t('proxyNodes')} value={`${proxies.filter((p) => p.healthy).length} / ${proxies.length}`} sub={tr('健康 / 已连接', 'healthy / connected')} />
      </div>
      <Card
        title={t('homeReplicas')}
        bodyClass="table-wrap"
        extra={staleHomes.length > 0 && <Switch checked={showStale} onChange={setShowStale} label={<span className="small">{tr(`显示失联副本（${staleHomes.length}）`, `Show stale (${staleHomes.length})`)}</span>} />}
      >
        {!topo.data ? (
          <Loading />
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>{tr('地址', 'Address')}</th>
                <th>{tr('角色', 'Role')}</th>
                <th>{t('status')}</th>
                <th>{tr('连接的代理', 'Proxies')}</th>
                <th>{tr('分配的代理', 'Assigned')}</th>
                <th className="hide-sm">{t('since')}</th>
                <th>{t('lastSeen')}</th>
              </tr>
            </thead>
            <tbody>
              {homes.map((h) => (
                <tr key={h.id}>
                  <td className="mono">{h.id}</td>
                  <td>{h.is_master ? <Badge kind="primary">{t('leader')}</Badge> : <Badge>{t('follower')}</Badge>}</td>
                  <td>
                    <Badge kind={h.healthy ? 'ok' : 'err'} dot>
                      {h.health ?? (h.healthy ? t('healthy') : t('unhealthy'))}
                    </Badge>
                  </td>
                  <td>
                    {h.healthy_cpa_count ?? 0} / {h.cpa_count ?? 0}
                  </td>
                  <td data-testid="assigned-count">{proxies.filter((p) => p.assigned_home_id === h.id).length}</td>
                  <td className="hide-sm small">{fmtTime(h.started_at)}</td>
                  <td className="small">{fmtRelative(h.last_seen_at, lang)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      <Card title={t('proxyNodes')} bodyClass="table-wrap">
        {!topo.data ? (
          <Loading />
        ) : proxies.length === 0 ? (
          <Empty>{tr('没有已连接的代理节点', 'No proxy nodes connected')}</Empty>
        ) : (
          <table className="table" data-testid="proxy-nodes">
            <thead>
              <tr>
                <th>{tr('Pod', 'Pod')}</th>
                <th>{tr('临时身份', 'Ephemeral identity')}</th>
                <th>IP</th>
                <th>{t('status')}</th>
                <th className="hide-sm">{tr('连接的 Home', 'Home')}</th>
                <th className="hide-sm">{tr('分配的 Home', 'Assigned Home')}</th>
                <th className="hide-sm">{tr('连接时间', 'Connected')}</th>
                <th>{t('lastSeen')}</th>
              </tr>
            </thead>
            <tbody>
              {proxies.map((p) => (
                <tr key={p.node_id}>
                  <td className="mono">{p.node_name ?? '—'}</td>
                  <td className="mono small faint">{p.node_id}</td>
                  <td className="mono">{p.ip}</td>
                  <td>
                    <Badge kind={p.healthy ? 'ok' : p.health === 'stale' ? 'warn' : 'err'} dot>
                      {p.health ?? (p.healthy ? t('healthy') : t('unhealthy'))}
                    </Badge>
                  </td>
                  <td className="hide-sm mono small">{p.home_id}</td>
                  <td className="hide-sm mono small" data-testid="assigned-home">
                    {p.assigned_home_id ? (
                      <>
                        {p.assigned_home_id}{' '}
                        {p.assigned_home_id !== p.home_id && <Badge kind="warn">{tr('迁移中', 'moving')}</Badge>}
                      </>
                    ) : (
                      <span className="faint">{tr('未分配（哈希）', 'none (hash)')}</span>
                    )}
                  </td>
                  <td className="hide-sm small">{fmtTime(p.connected_time)}</td>
                  <td className="small">{fmtRelative(p.last_seen_at, lang)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
