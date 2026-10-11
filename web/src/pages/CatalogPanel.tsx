import { useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import type { CatalogCredentialStatus } from '../api/client';
import { fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Spinner } from '../components/ui';
import { IconRefresh } from '../components/icons';

/** Human label for what triggered the last catalog fetch. */
export function triggerLabel(trigger: string | undefined, tr: (zh: string, en: string) => string): string {
  if (!trigger) return '—';
  if (trigger === 'timer') return tr('定时', 'timer');
  if (trigger === 'startup') return tr('启动', 'startup');
  if (trigger === 'manual') return tr('手动', 'manual');
  if (trigger.startsWith('event:')) return tr(`错误触发（${trigger.slice(6)}）`, `error (${trigger.slice(6)})`);
  return trigger;
}

export function intervalLabel(seconds: number, tr: (zh: string, en: string) => string): string {
  if (!seconds) return tr('定时刷新已关闭（仅手动 / 错误触发）', 'Timer off (manual / error-triggered only)');
  const m = Math.round(seconds / 60);
  return m >= 60 && m % 60 === 0 ? tr(`每 ${m / 60} 小时自动刷新`, `Auto refresh every ${m / 60}h`) : tr(`每 ${m} 分钟自动刷新`, `Auto refresh every ${m} min`);
}

function statusBadge(c: CatalogCredentialStatus, tr: (zh: string, en: string) => string) {
  if (c.status === 'ok') return <Badge kind="ok" dot>{tr('正常', 'OK')}</Badge>;
  if (c.status === 'error') return <Badge kind="err" dot>{tr(`失败${c.http_status ? ` ${c.http_status}` : ''}`, `Failed${c.http_status ? ` ${c.http_status}` : ''}`)}</Badge>;
  return <Badge dot>{tr('等待首次获取', 'Pending')}</Badge>;
}

/** Upstream catalog status per credential with a manual refresh. */
export function CatalogPanel({ onRefreshed }: { onRefreshed?: () => Promise<void> | void }) {
  const api = useApi();
  const { tr, lang } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const res = useAsync(() => api.antigravityCatalog(), [], 30000);
  const [open, setOpen] = useState(false);
  const data = res.data;
  if (!data) return null;
  const creds = data.credentials ?? [];
  const failing = creds.filter((c) => c.status === 'error').length;
  const latest = creds.map((c) => c.fetched_at).filter(Boolean).sort().reverse()[0];
  const removedTotal = creds.reduce((n, c) => n + c.removed.length, 0);

  const refresh = (authId?: string) =>
    run(`refresh:${authId ?? 'all'}`, async () => {
      const r = await api.refreshAntigravityCatalog(authId);
      const bad = r.credentials.filter((c) => c.status === 'error');
      toast(bad.length ? tr(`刷新完成，${bad.length} 个凭据失败`, `Refreshed, ${bad.length} failed`) : tr('模型目录已刷新', 'Catalog refreshed'));
      await res.reload();
      await onRefreshed?.();
    });

  return (
    <div className="info-bar small" data-testid="catalog-panel">
      <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
        <button className="btn-link" onClick={() => setOpen(!open)} aria-expanded={open}>
          {open ? '▾' : '▸'} {tr('上游模型目录', 'Upstream model catalog')}
        </button>
        <span className="faint">
          {intervalLabel(data.interval_seconds, tr)}
          {latest && <> · {tr('最近', 'last')} {fmtRelative(latest, lang)}</>}
          {' · '}
          {failing ? <span className="err-text">{tr(`${failing}/${creds.length} 获取失败`, `${failing}/${creds.length} failing`)}</span> : tr(`${creds.length} 个凭据正常`, `${creds.length} OK`)}
          {removedTotal > 0 && <> · <span className="warn-text">{tr(`近 7 天上游移除 ${removedTotal} 项`, `${removedTotal} removed (7d)`)}</span></>}
        </span>
        <span className="spacer" />
        <button className="btn btn-sm" disabled={!!busy} onClick={() => void refresh()} data-testid="catalog-refresh-all">
          {busy === 'refresh:all' ? <Spinner /> : <IconRefresh />} {tr('立即刷新目录', 'Refresh catalog now')}
        </button>
      </div>
      {open && (
        <div className="info-body">
          <p>
            {tr(
              '模型目录决定每个凭据可服务的模型（/v1/models 与路由都以此为准）。只有 Home 主节点按间隔定时获取并写入数据库，其他副本读取同一份结果；某凭据对某模型返回 404 / 400 时会立即补刷该凭据（每凭据每分钟最多一次）。上游移除的模型在 7 天内标记为“上游已移除”。间隔可在 设置 → 路由 中修改。',
              'The catalog decides which models each credential serves (model lists and routing). Only the Home leader fetches on the timer and stores it in the database; other replicas read it. A 404 / 400 for a model triggers an immediate refresh of that credential (at most once a minute). Removed models are flagged for 7 days. Change the interval in Settings → Routing.',
            )}
          </p>
          <div className="table-wrap">
            <table className="table catalog-table" data-testid="catalog-table">
              <thead>
                <tr>
                  <th>{tr('凭据', 'Credential')}</th>
                  <th>{tr('状态', 'Status')}</th>
                  <th>{tr('模型数', 'Models')}</th>
                  <th>{tr('最近成功', 'Last success')}</th>
                  <th className="hide-sm">{tr('最近尝试', 'Last attempt')}</th>
                  <th className="hide-sm">{tr('变化（7 天）', 'Changes (7d)')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {creds.map((c) => (
                  <tr key={c.auth_id}>
                    <td className="ellipsis" style={{ maxWidth: 220 }} title={c.auth_id}>
                      {c.label}
                    </td>
                    <td title={c.error}>{statusBadge(c, tr)}</td>
                    <td>{c.status === 'pending' ? '—' : c.model_count}</td>
                    <td className="nowrap">{c.fetched_at ? fmtRelative(c.fetched_at, lang) : '—'}</td>
                    <td className="hide-sm nowrap faint" title={c.fetched_by ? `${tr('执行副本', 'by')} ${c.fetched_by}` : undefined}>
                      {c.attempted_at ? `${fmtRelative(c.attempted_at, lang)} · ${triggerLabel(c.trigger, tr)}` : '—'}
                      {c.next_due_at && <div className="small">{tr('下次', 'next')} {fmtTime(c.next_due_at)}</div>}
                    </td>
                    <td className="hide-sm small">
                      {c.removed.length === 0 && c.added.length === 0 && <span className="faint">{tr('无', 'none')}</span>}
                      {c.removed.map((x) => (
                        <div key={`r-${x.model}`} className="err-text" title={fmtTime(x.at)}>
                          − {x.model}
                        </div>
                      ))}
                      {c.added.map((x) => (
                        <div key={`a-${x.model}`} className="ok-text" title={fmtTime(x.at)}>
                          + {x.model}
                        </div>
                      ))}
                    </td>
                    <td className="right">
                      <button className="btn btn-sm" disabled={!!busy} onClick={() => void refresh(c.auth_id)} title={tr('只刷新此凭据', 'Refresh this credential')}>
                        {busy === `refresh:${c.auth_id}` ? <Spinner /> : <IconRefresh />}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
