import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { toggleExcludedModel, type ModelHealth, type ModelHealthStatus } from '../api/client';
import { fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Drawer, Empty, ErrorBox, Loading, Spinner, Switch } from '../components/ui';
import { IconRefresh } from '../components/icons';

const STATUS_ORDER: ModelHealthStatus[] = ['retired', 'rejected', 'cooling', 'partial', 'unknown', 'available'];

export function statusBadge(s: ModelHealthStatus, tr: (zh: string, en: string) => string) {
  switch (s) {
    case 'available':
      return <Badge kind="ok" dot>{tr('可用', 'Available')}</Badge>;
    case 'partial':
      return <Badge kind="warn" dot>{tr('部分冷却', 'Partly cooling')}</Badge>;
    case 'cooling':
      return <Badge kind="warn" dot>{tr('冷却中', 'Cooling')}</Badge>;
    case 'rejected':
      return <Badge kind="err" dot>{tr('上游拒绝', 'Rejected upstream')}</Badge>;
    case 'retired':
      return <Badge kind="err" dot>{tr('已下线（上游）', 'Retired upstream')}</Badge>;
    default:
      return <Badge dot>{tr('未知', 'Unknown')}</Badge>;
  }
}

const OUTCOME_ZH: Record<string, string> = {
  ok: '正常',
  retired: '已下线',
  rejected: '上游拒绝',
  quota: '额度/限流',
  capacity: '容量不足',
  auth_error: '凭据认证失败',
  error: '错误',
};

export function ModelHealthTab() {
  const api = useApi();
  const { tr, lang } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const res = useAsync(() => api.modelHealth(), [], 60000);
  const [filter, setFilter] = useState<'all' | 'problem' | 'hidden'>('all');
  const [q, setQ] = useState('');
  const [open, setOpen] = useState<string | null>(null);

  const rows = useMemo(() => {
    const list = (res.data?.models ?? []).filter((m) => {
      if (q && !m.id.toLowerCase().includes(q.toLowerCase()) && !(m.display_name ?? '').toLowerCase().includes(q.toLowerCase())) return false;
      if (filter === 'problem') return m.status !== 'available';
      if (filter === 'hidden') return m.hidden;
      return true;
    });
    return list.sort((a, b) => STATUS_ORDER.indexOf(a.status) - STATUS_ORDER.indexOf(b.status) || a.id.localeCompare(b.id));
  }, [res.data, filter, q]);

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const m of res.data?.models ?? []) c[m.status] = (c[m.status] ?? 0) + 1;
    return c;
  }, [res.data]);

  const retest = (model: string) =>
    run(`retest:${model}`, async () => {
      const r = await api.retestModel(model);
      toast(`${model}: ${tr(OUTCOME_ZH[r.result.outcome] ?? r.result.outcome, r.result.outcome)}${r.result.http_status ? ` (${r.result.http_status})` : ''}`);
      await res.reload();
    });

  const setHidden = (model: string, hide: boolean) =>
    run(`hide:${model}`, async () => {
      const current = await api.configGet<Record<string, string[]>>('oauth/excluded-models', {});
      await api.configPut('oauth/excluded-models', toggleExcludedModel(current, model, hide));
      toast(hide ? tr(`已隐藏 ${model}（写入 excluded-models）`, `Hidden ${model}`) : tr(`已取消隐藏 ${model}`, `Unhidden ${model}`));
      await res.reload();
    });

  if (res.error) return <ErrorBox error={res.error} onRetry={res.reload} />;
  if (!res.data) return <Loading />;
  const detail = open ? res.data.models.find((m) => m.id === open) : undefined;

  return (
    <div className="stack" data-testid="model-health">
      <div className="alert small">
        {tr(
          '按模型汇总真实状态：上游模型目录（弃用/即将下线/额度）、各凭据的冷却与最近错误、手动“重新测试”的结果（保存在数据库，所有 Home 副本共享）。不会自动隐藏任何模型；“隐藏”会写入 oauth-excluded-models，下游 /v1/models 不再列出，可随时取消。',
          'Per-model real state: upstream catalog (deprecated / leaving soon / quota), per-credential cooldowns and last errors, and manual retest results (stored in the database, shared by all Home replicas). Nothing is hidden automatically; "Hide" writes oauth-excluded-models and can be undone.',
        )}
        {res.data.catalog_fetched_at && (
          <span className="faint">
            {' '}
            {tr('目录更新于', 'Catalog fetched')} {fmtRelative(res.data.catalog_fetched_at, lang)}
          </span>
        )}
      </div>
      {(res.data.missing_switch_targets ?? []).length > 0 && (
        <div className="alert warn small" data-testid="missing-switch-targets">
          {tr(
            `上游提示把已下线模型切换到 ${res.data.missing_switch_targets!.join('、')}，但当前账号的模型目录中没有这些模型（可能尚未对该账号开放）。`,
            `Upstream suggests switching to ${res.data.missing_switch_targets!.join(', ')}, which is not in this account's model catalog.`,
          )}
        </div>
      )}
      <Card
        title={
          <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
            {(['all', 'problem', 'hidden'] as const).map((f) => (
              <button key={f} className={`btn btn-sm ${filter === f ? 'btn-primary' : ''}`} onClick={() => setFilter(f)}>
                {f === 'all' ? tr(`全部 ${res.data!.models.length}`, `All ${res.data!.models.length}`) : f === 'problem' ? tr(`有问题 ${res.data!.models.length - (counts.available ?? 0)}`, `Problems`) : tr(`已隐藏 ${res.data!.models.filter((m) => m.hidden).length}`, 'Hidden')}
              </button>
            ))}
            <input className="input" style={{ maxWidth: 220 }} placeholder={tr('搜索模型', 'Search model')} value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        }
        extra={
          <button className="btn btn-sm" onClick={() => void res.reload()}>
            <IconRefresh /> {tr('刷新', 'Refresh')}
          </button>
        }
      >
        {rows.length === 0 ? (
          <Empty />
        ) : (
          <div className="table-wrap">
            <table className="table" data-testid="model-health-table">
              <thead>
                <tr>
                  <th>{tr('模型', 'Model')}</th>
                  <th>{tr('状态', 'Status')}</th>
                  <th className="hide-sm">{tr('凭据', 'Credentials')}</th>
                  <th className="hide-sm">{tr('最近检测', 'Last check')}</th>
                  <th>{tr('操作', 'Actions')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((m) => (
                  <tr key={m.id} data-testid={`mh-row-${m.id}`} className={m.hidden ? 'row-muted' : ''}>
                    <td>
                      <button className="btn-link mono" onClick={() => setOpen(m.id)}>
                        {m.id}
                      </button>
                      {m.hidden && (
                        <>
                          {' '}
                          <Badge>{tr('已隐藏', 'hidden')}</Badge>
                        </>
                      )}
                      {m.tag_title && (
                        <>
                          {' '}
                          <Badge kind="warn">{m.tag_title === 'Leaving Soon' ? tr('即将下线', 'Leaving soon') : m.tag_title}</Badge>
                        </>
                      )}
                      {m.display_name && <div className="faint small">{m.display_name}</div>}
                    </td>
                    <td style={{ maxWidth: 380 }}>
                      {statusBadge(m.status, tr)}
                      {m.reason && <div className="small wrap-text" style={{ marginTop: 4 }}>{m.reason}</div>}
                      {(m.notes ?? []).map((n) => (
                        <div key={n} className="faint small wrap-text" style={{ marginTop: 2 }}>
                          ⓘ {n}
                        </div>
                      ))}
                    </td>
                    <td className="hide-sm small">
                      {m.credentials.map((c) => (
                        <div key={c.label} className="nowrap" title={c.message}>
                          <Badge kind={c.state === 'ok' ? 'ok' : c.state === 'cooling' ? 'warn' : 'err'} dot>
                            {c.state === 'ok' ? tr('正常', 'ok') : c.state === 'cooling' ? tr('冷却', 'cooling') : tr('错误', 'error')}
                            {c.http_status ? ` ${c.http_status}` : ''}
                          </Badge>{' '}
                          <span className="faint">{c.label}</span>
                          {c.quota_remaining !== undefined && <span className="faint"> · {Math.round(c.quota_remaining * 100)}%</span>}
                        </div>
                      ))}
                    </td>
                    <td className="hide-sm small">
                      {m.last_check ? (
                        <>
                          <Badge kind={m.last_check.outcome === 'ok' ? 'ok' : 'err'}>
                            {tr(OUTCOME_ZH[m.last_check.outcome] ?? m.last_check.outcome, m.last_check.outcome)}
                            {m.last_check.http_status ? ` ${m.last_check.http_status}` : ''}
                          </Badge>
                          <div className="faint">{fmtRelative(m.last_check.checked_at, lang)}</div>
                        </>
                      ) : (
                        <span className="faint">{tr('未检测', 'never')}</span>
                      )}
                    </td>
                    <td>
                      <div className="row" style={{ gap: 6, flexWrap: 'nowrap' }}>
                        <button className="btn btn-sm" disabled={!!busy} onClick={() => void retest(m.id)} data-testid={`mh-retest-${m.id}`}>
                          {busy === `retest:${m.id}` ? <Spinner /> : <IconRefresh />} {tr('重新测试', 'Retest')}
                        </button>
                        <Switch checked={!m.hidden} disabled={!!busy} onChange={(v) => void setHidden(m.id, !v)} label={tr('显示', 'Shown')} />
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      {detail && <ModelHealthDrawer m={detail} onClose={() => setOpen(null)} onRetest={() => void retest(detail.id)} busy={busy === `retest:${detail.id}`} />}
    </div>
  );
}

function ModelHealthDrawer({ m, onClose, onRetest, busy }: { m: ModelHealth; onClose: () => void; onRetest: () => void; busy: boolean }) {
  const { tr, lang } = useI18n();
  return (
    <Drawer title={m.id} onClose={onClose}>
      <div className="stack" data-testid="model-health-detail">
        <div className="row">
          {statusBadge(m.status, tr)}
          {m.hidden && <Badge>{tr('已隐藏', 'hidden')}</Badge>}
          <button className="btn btn-sm" onClick={onRetest} disabled={busy}>
            {busy ? <Spinner /> : <IconRefresh />} {tr('重新测试', 'Retest')}
          </button>
        </div>
        {m.reason && <div className="alert warn small wrap-text">{m.reason}</div>}
        {(m.notes ?? []).map((n) => (
          <div key={n} className="alert small wrap-text">
            {n}
          </div>
        ))}
        <dl className="kv small">
          <dt>{tr('显示名称', 'Display name')}</dt>
          <dd>{m.display_name || '—'}</dd>
          <dt>{tr('在上游目录', 'In catalog')}</dt>
          <dd>{m.in_catalog ? tr('是', 'yes') : tr('否', 'no')}</dd>
          <dt>{tr('已注册（下游可见）', 'Registered')}</dt>
          <dd>{m.registered ? tr('是', 'yes') : tr('否', 'no')}</dd>
          {m.deprecated_to && (
            <>
              <dt>{tr('上游弃用 → 替代', 'Deprecated → replacement')}</dt>
              <dd className="mono">{m.deprecated_to}</dd>
            </>
          )}
          {m.tag_title && (
            <>
              <dt>{tr('上游标签', 'Upstream tag')}</dt>
              <dd className="wrap-text">
                {m.tag_title}
                {m.tag_description ? ` — ${m.tag_description}` : ''}
              </dd>
            </>
          )}
          {m.last_error && (
            <>
              <dt>{tr('最近错误', 'Last error')}</dt>
              <dd className="wrap-text">
                <span className="mono small">{m.last_error}</span>
                {m.last_error_at && <div className="faint">{fmtTime(m.last_error_at)}</div>}
              </dd>
            </>
          )}
        </dl>
        <div className="small strong">{tr('各凭据状态', 'Per credential')}</div>
        <table className="table small">
          <thead>
            <tr>
              <th>{tr('凭据', 'Credential')}</th>
              <th>{tr('状态', 'State')}</th>
              <th>{tr('额度', 'Quota')}</th>
              <th>{tr('恢复时间', 'Retry at')}</th>
            </tr>
          </thead>
          <tbody>
            {m.credentials.map((c) => (
              <tr key={c.label}>
                <td>{c.label}</td>
                <td>
                  <Badge kind={c.state === 'ok' ? 'ok' : c.state === 'cooling' ? 'warn' : 'err'} dot>
                    {c.state}
                    {c.http_status ? ` ${c.http_status}` : ''}
                  </Badge>
                  {c.message && <div className="faint small wrap-text" style={{ maxWidth: 320 }}>{c.message}</div>}
                </td>
                <td>{c.quota_remaining !== undefined ? `${Math.round(c.quota_remaining * 100)}%` : '—'}{c.quota_reset_time && <div className="faint">{fmtTime(c.quota_reset_time)}</div>}</td>
                <td>{c.next_retry_after ? `${fmtTime(c.next_retry_after)} (${fmtRelative(c.next_retry_after, lang)})` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {m.last_check && (
          <>
            <div className="small strong">
              {tr('最近一次测试', 'Last check')} · {fmtTime(m.last_check.checked_at)}
            </div>
            <table className="table small">
              <thead>
                <tr>
                  <th>{tr('凭据', 'Credential')}</th>
                  <th>{tr('结果', 'Outcome')}</th>
                  <th>{tr('耗时', 'Latency')}</th>
                </tr>
              </thead>
              <tbody>
                {(m.last_check.details ?? []).map((d) => (
                  <tr key={d.auth_id}>
                    <td>{d.label}</td>
                    <td>
                      <Badge kind={d.outcome === 'ok' ? 'ok' : 'err'}>
                        {tr(OUTCOME_ZH[d.outcome] ?? d.outcome, d.outcome)} {d.http_status || ''}
                      </Badge>
                      {d.message && <div className="faint small wrap-text" style={{ maxWidth: 360 }}>{d.message}</div>}
                    </td>
                    <td>{d.latency_ms} ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </div>
    </Drawer>
  );
}
