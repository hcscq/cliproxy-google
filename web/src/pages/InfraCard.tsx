import { useApi, useAsync } from '../app-context';
import type { InfraStatus } from '../api/client';
import { fmtNum, fmtRelative } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Loading } from '../components/ui';

const ZERO_TIME = '0001-01-01T00:00:00Z';

/** Redis, usage stream and PostgreSQL / pgbouncer status of the serving Home. */
export function InfraCard() {
  const api = useApi();
  const { tr, lang } = useI18n();
  const infra = useAsync(() => api.infra(), [], 10000);
  const d: InfraStatus | undefined = infra.data;
  return (
    <Card title={tr('基础设施', 'Infrastructure')} extra={d && <span className="small faint mono">{tr('来自 Home', 'via Home')} {d.home}</span>}>
      {!d ? (
        infra.error ? <span className="faint">{tr('此 Home 版本不提供状态', 'not available on this Home')}</span> : <Loading />
      ) : (
        <div className="grid grid-3" data-testid="infra">
          <div>
            <h4>Redis</h4>
            {!d.redis.enabled ? (
              <Badge>{tr('未启用（PostgreSQL / 内存）', 'disabled (PostgreSQL / in-memory)')}</Badge>
            ) : (
              <dl className="kv small">
                <dt>{tr('状态', 'Status')}</dt>
                <dd>
                  <Badge kind={d.redis.healthy ? 'ok' : 'err'} dot>
                    {d.redis.healthy ? tr('健康', 'healthy') : tr('不可用，已回退', 'down, falling back')}
                  </Badge>
                </dd>
                <dt>{tr('主节点', 'Primary')}</dt>
                <dd className="mono">{d.redis.master || '—'}</dd>
                <dt>{tr('副本', 'Replicas')}</dt>
                <dd>{d.redis.connected_slaves ?? '—'}</dd>
                <dt>Sentinel</dt>
                <dd>
                  {(d.redis.sentinels ?? []).map((s) => (
                    <Badge key={s.addr} kind={s.error ? 'err' : 'ok'} dot>
                      {s.addr.split('.')[0]}
                    </Badge>
                  ))}
                  {!d.redis.sentinels?.length && '—'}
                </dd>
                <dt>{tr('内存 / 延迟', 'Memory / ping')}</dt>
                <dd>
                  {d.redis.used_memory ?? '—'} / {d.redis.ping_ms.toFixed(1)} ms
                </dd>
              </dl>
            )}
          </div>
          <div>
            <h4>{tr('请求日志流', 'Request-log stream')}</h4>
            {!d.usage_stream?.enabled ? (
              <Badge>{tr('未启用（内存队列直写 PostgreSQL）', 'off (in-memory queue to PostgreSQL)')}</Badge>
            ) : (
              <dl className="kv small">
                <dt>{tr('状态', 'Status')}</dt>
                <dd>
                  <Badge kind={d.usage_stream.active ? 'ok' : 'warn'} dot>
                    {d.usage_stream.active ? tr('经 Redis Stream', 'via Redis stream') : tr('已回退到内存队列', 'fallback: in-memory queue')}
                  </Badge>
                </dd>
                <dt>{tr('积压 / 待确认', 'Backlog / pending')}</dt>
                <dd data-testid="stream-backlog">
                  {fmtNum(d.usage_stream.length)} / {fmtNum(d.usage_stream.pending)}
                </dd>
                <dt>{tr('消费者', 'Consumers')}</dt>
                <dd>{d.usage_stream.consumers}</dd>
                <dt>{tr('本 Home 写入 / 回退', 'This Home flushed / fallback')}</dt>
                <dd>
                  {fmtNum(d.usage_stream.flushed)} / {fmtNum(d.usage_stream.fallback)}
                </dd>
                <dt>{tr('上次落库', 'Last flush')}</dt>
                <dd>{d.usage_stream.last_flush_at && d.usage_stream.last_flush_at !== ZERO_TIME ? fmtRelative(d.usage_stream.last_flush_at, lang) : '—'}</dd>
                {d.usage_stream.last_error && (
                  <>
                    <dt>{tr('错误', 'Error')}</dt>
                    <dd className="small">{d.usage_stream.last_error}</dd>
                  </>
                )}
              </dl>
            )}
          </div>
          <div>
            <h4>PostgreSQL</h4>
            <dl className="kv small">
              <dt>pgbouncer</dt>
              <dd data-testid="pooler">
                {d.postgres.via_pooler ? (
                  <Badge kind="ok" dot>
                    {d.postgres.pooler_addr}
                  </Badge>
                ) : d.postgres.pooler_addr ? (
                  <Badge kind="warn">{tr('不可用，直连', 'unavailable, direct')}</Badge>
                ) : (
                  <Badge>{tr('未启用（直连）', 'off (direct)')}</Badge>
                )}
              </dd>
              <dt>{tr('主连接池', 'Main pool')}</dt>
              <dd>
                {d.postgres.main.InUse} {tr('使用中', 'in use')} / {d.postgres.main.OpenConnections} {tr('已打开', 'open')} / {d.postgres.main.MaxOpenConnections} max
              </dd>
              <dt>{tr('等待', 'Waits')}</dt>
              <dd>{fmtNum(d.postgres.main.WaitCount)}</dd>
              <dt>{tr('租约池（直连）', 'Lease pool (direct)')}</dt>
              <dd>{d.postgres.lease ? `${d.postgres.lease.InUse} / ${d.postgres.lease.OpenConnections}` : '—'}</dd>
              <dt>{tr('延迟', 'Ping')}</dt>
              <dd>{d.postgres.ping_error ? <Badge kind="err">{d.postgres.ping_error}</Badge> : `${d.postgres.ping_ms.toFixed(1)} ms`}</dd>
            </dl>
          </div>
        </div>
      )}
    </Card>
  );
}
