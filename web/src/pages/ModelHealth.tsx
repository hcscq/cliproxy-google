import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { toggleExcludedModel, type ModelHealth, type ModelHealthStatus } from '../api/client';
import { fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Drawer, Empty, ErrorBox, Loading, Spinner, Switch } from '../components/ui';
import { IconRefresh } from '../components/icons';
import { CatalogPanel } from './CatalogPanel';
import { fmtRecover } from '../components/quota';

const STATUS_ORDER: ModelHealthStatus[] = ['retired', 'removed', 'rejected', 'cooling', 'partial', 'unknown', 'available'];

/** Credentials that count for a model: only in-catalog ones when any credential offers it. */
export function countedCreds(m: ModelHealth): ModelHealth['credentials'] {
  const inCat = m.credentials.filter((c) => c.in_catalog);
  return inCat.length > 0 ? inCat : m.credentials;
}

/** Earliest recovery time across the model's cooling credentials. */
function earliestRetry(m: ModelHealth): string | undefined {
  return countedCreds(m).map((c) => c.next_retry_after).filter(Boolean).sort()[0];
}

export function statusBadge(s: ModelHealthStatus, tr: (zh: string, en: string) => string, until?: string) {
  const at = fmtRecover(until);
  switch (s) {
    case 'available':
      return <Badge kind="ok" dot>{tr('可用', 'Available')}</Badge>;
    case 'partial':
      return <Badge kind="warn" dot>{tr('部分冷却', 'Partly cooling')}</Badge>;
    case 'cooling':
      return <Badge kind="warn" dot>{at ? tr(`冷却 · ${at}恢复`, `Cooling · ${at}`) : tr('冷却中', 'Cooling')}</Badge>;
    case 'rejected':
      return <Badge kind="err" dot>{tr('上游拒绝', 'Rejected upstream')}</Badge>;
    case 'retired':
      return <Badge kind="err" dot>{tr('已下线（上游）', 'Retired upstream')}</Badge>;
    case 'removed':
      return <Badge kind="err" dot>{tr('上游已移除', 'Removed upstream')}</Badge>;
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

/** One short Chinese reason for the status column (full text goes to the tooltip / drawer). */
export function shortReason(m: ModelHealth, tr: (zh: string, en: string) => string): string {
  const creds = countedCreds(m);
  const ok = creds.filter((c) => c.state !== 'cooling' && c.state !== 'removed').length;
  switch (m.status) {
    case 'retired':
      return tr('上游已下线，请改用其他模型', 'Retired upstream');
    case 'rejected':
      if (m.deprecated_to && m.deprecated_to !== '-') return tr(`已弃用 → ${m.deprecated_to}`, `Deprecated → ${m.deprecated_to}`);
      return tr(`上游拒绝${m.last_check?.http_status ? ` (${m.last_check.http_status})` : ''}`, 'Rejected upstream');
    case 'cooling': {
      const t = creds.map((c) => c.next_retry_after).filter(Boolean).sort()[0];
      return t ? tr(`冷却至 ${fmtTime(t)}`, `Cooling until ${fmtTime(t)}`) : tr('全部凭据冷却中', 'All credentials cooling');
    }
    case 'partial':
      return tr(`${ok}/${creds.length} 凭据可用`, `${ok}/${creds.length} usable`);
    case 'removed': {
      const t = m.credentials.map((c) => c.removed_at).filter(Boolean).sort().reverse()[0];
      return t ? tr(`${fmtTime(t)} 从上游目录移除`, `Removed ${fmtTime(t)}`) : tr('已从上游目录移除', 'Removed from catalog');
    }
    case 'unknown':
      return tr('不在当前账号目录中', 'Not in catalog');
    default:
      if (m.last_check?.outcome === 'capacity') return tr('最近测试：容量不足', 'Last check: no capacity');
      if (m.last_check?.outcome === 'quota') return tr('最近测试：限流', 'Last check: rate limited');
      return '';
  }
}

export type ChipKind = 'stream' | 'leaving' | 'switch' | 'text';
/** Shared notes become small chips; the legend explains them once. */
export function noteChips(m: ModelHealth): { kind: ChipKind; title: string }[] {
  const out: { kind: ChipKind; title: string }[] = [];
  for (const n of m.notes ?? []) {
    if (n.startsWith('流式思考')) out.push({ kind: 'stream', title: n });
    else if (n.includes('即将下线')) out.push({ kind: 'leaving', title: n });
    else if (n.startsWith('上游提示切换')) out.push({ kind: 'switch', title: n });
    else if (n.includes('仅支持文本')) out.push({ kind: 'text', title: n });
  }
  return out;
}

const CHIP: Record<ChipKind, [string, string, string]> = {
  stream: ['思', '流式思考受限', 'Streaming thinking limited'],
  leaving: ['降', '即将下线', 'Leaving soon'],
  switch: ['?', '建议的替代模型不在目录中', 'Suggested replacement not in catalog'],
  text: ['T', '仅文本', 'Text only'],
};

function Chip({ kind, title }: { kind: ChipKind; title: string }) {
  const { tr } = useI18n();
  return (
    <span className={`chip chip-${kind}`} title={title} aria-label={tr(CHIP[kind][1], CHIP[kind][2])}>
      {CHIP[kind][0]}
    </span>
  );
}

export type CredBucket = 'ok' | 'cooling' | 'error' | 'removed' | 'other';
const BUCKET_ORDER: CredBucket[] = ['ok', 'cooling', 'error', 'removed', 'other'];
const BUCKET_LABEL: Record<CredBucket, [string, string]> = {
  ok: ['可用', 'ok'],
  cooling: ['冷却', 'cooling'],
  error: ['错误', 'error'],
  removed: ['已移除', 'removed'],
  other: ['其他', 'other'],
};

/** Counts credentials per state; every credential lands in exactly one bucket. */
export function credBuckets(creds: ModelHealth['credentials']): { bucket: CredBucket; count: number }[] {
  const n: Record<CredBucket, number> = { ok: 0, cooling: 0, error: 0, removed: 0, other: 0 };
  for (const c of creds) {
    const b = (BUCKET_ORDER as string[]).includes(c.state) ? (c.state as CredBucket) : 'other';
    n[b]++;
  }
  return BUCKET_ORDER.filter((b) => n[b] > 0).map((b) => ({ bucket: b, count: n[b] }));
}

/** Fixed-height credential summary: usable/total plus one segmented bar (width ∝ count per state).
 *  Height never depends on the number of credentials; full per-credential list is in the tooltip and the drawer. */
function CredSummary({ m }: { m: ModelHealth }) {
  const { tr } = useI18n();
  const creds = countedCreds(m);
  const total = creds.length;
  const buckets = credBuckets(creds);
  const ok = buckets.find((b) => b.bucket === 'ok')?.count ?? 0;
  const counts = buckets.map((b) => `${tr(BUCKET_LABEL[b.bucket][0], BUCKET_LABEL[b.bucket][1])} ${b.count}`).join(' · ');
  const tip =
    counts +
    '\n' +
    creds
      .map((c) => `${c.label}: ${c.state}${c.http_status ? ` ${c.http_status}` : ''}${c.quota_remaining !== undefined ? ` · ${tr('额度', 'quota')} ${Math.round(c.quota_remaining * 100)}%` : ''}`)
      .join('\n') +
    '\n' +
    tr('点击模型名查看各凭据详情', 'Click the model for per-credential details');
  return (
    <span className="cred-sum" title={tip}>
      <span className={ok === total ? 'ok-text' : ok === 0 ? 'err-text' : 'warn-text'}>
        {ok}/{total}
      </span>
      <span className="seg-bar" role="img" aria-label={counts}>
        {buckets.map((b) => (
          <span key={b.bucket} className={`seg ${b.bucket}`} style={{ flexGrow: b.count }} />
        ))}
      </span>
      {buckets.some((b) => b.bucket !== 'ok') && (
        <span className="seg-counts small faint">
          {buckets
            .filter((b) => b.bucket !== 'ok')
            .map((b) => (
              <span key={b.bucket} className={`seg-count ${b.bucket}`}>{b.count}</span>
            ))}
        </span>
      )}
    </span>
  );
}

export function ModelHealthTab() {
  const api = useApi();
  const { tr, lang } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const res = useAsync(() => api.modelHealth(), [], 60000);
  const [filter, setFilter] = useState<'all' | 'problem' | 'hidden'>('all');
  const [q, setQ] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const [infoOpen, setInfoOpen] = useState(() => localStorage.getItem('panel.mhInfo') !== 'closed');
  const toggleInfo = () => {
    localStorage.setItem('panel.mhInfo', infoOpen ? 'closed' : 'open');
    setInfoOpen(!infoOpen);
  };

  const rows = useMemo(() => {
    const list = (res.data?.models ?? []).filter((m) => {
      if (q && !m.id.toLowerCase().includes(q.toLowerCase()) && !(m.display_name ?? '').toLowerCase().includes(q.toLowerCase())) return false;
      if (filter === 'problem') return m.status !== 'available';
      if (filter === 'hidden') return m.hidden;
      return true;
    });
    return list.sort((a, b) => STATUS_ORDER.indexOf(a.status) - STATUS_ORDER.indexOf(b.status) || a.id.localeCompare(b.id));
  }, [res.data, filter, q]);

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
  const data = res.data;
  const detail = open ? data.models.find((m) => m.id === open) : undefined;
  const missing = data.missing_switch_targets ?? [];
  const problems = data.models.filter((m) => m.status !== 'available').length;

  return (
    <div className="stack" data-testid="model-health">
      <div className="info-bar small" data-testid="mh-info">
        <button className="btn-link" onClick={toggleInfo} aria-expanded={infoOpen}>
          {infoOpen ? '▾' : '▸'} {tr('说明与图例', 'About & legend')}
        </button>
        <span className="faint">
          {data.catalog_fetched_at && <> · {tr('目录更新于', 'Catalog')} {fmtRelative(data.catalog_fetched_at, lang)}</>}
          {missing.length > 0 && <> · <span className="warn-text">{tr(`上游建议的 ${missing.join('、')} 不在账号目录中`, `${missing.join(', ')} not in catalog`)}</span></>}
        </span>
        {infoOpen && (
          <div className="info-body">
            <p>
              {tr(
                '状态综合上游目录（弃用 / 即将下线 / 额度）、各凭据冷却与最近错误、手动“重新测试”结果（存于数据库，所有 Home 副本共享）。不会自动隐藏模型；“显示”开关写入 oauth-excluded-models，可随时恢复。点击模型名查看上游原文、各凭据详情与错误。',
                'Status combines the upstream catalog, per-credential cooldowns/errors and manual retests (stored in the DB). Nothing is hidden automatically; the "shown" toggle writes oauth-excluded-models. Click a model for upstream messages and per-credential details.',
              )}
            </p>
            <div className="legend">
              <span><Chip kind="stream" title="" /> {tr('流式思考受限：上游流式只在思考较长时返回思考摘要、签名在正文后；可在设置开启“流式思考模拟”（默认关闭）', 'Streaming thinking limited upstream; see Settings → stream thinking emulation')}</span>
              <span><Chip kind="leaving" title="" /> {tr('上游标记即将下线', 'Marked "Leaving soon" upstream')}</span>
              <span><Chip kind="switch" title="" /> {tr('上游建议的替代模型不在当前账号目录中', 'Suggested replacement not in this account\'s catalog')}</span>
              <span><Chip kind="text" title="" /> {tr('仅支持文本输入', 'Text input only')}</span>
              <span><span className="seg-bar"><span className="seg ok" style={{ flexGrow: 6 }} /><span className="seg cooling" style={{ flexGrow: 3 }} /><span className="seg error" style={{ flexGrow: 1 }} /></span> {tr('凭据列：可用数 / 总数；条按状态分段（绿 可用 / 黄 冷却 / 红 错误·移除 / 灰 其他），右侧为非可用数；悬停看逐个凭据', 'Credentials: usable/total; bar segmented by state (green ok / amber cooling / red error·removed / grey other); hover for each credential')}</span>
            </div>
          </div>
        )}
      </div>
      <CatalogPanel onRefreshed={res.reload} />
      <Card
        title={
          <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
            {(['all', 'problem', 'hidden'] as const).map((f) => (
              <button key={f} className={`btn btn-sm ${filter === f ? 'btn-primary' : ''}`} onClick={() => setFilter(f)} data-testid={`mh-filter-${f}`}>
                {f === 'all' ? tr(`全部 ${data.models.length}`, `All ${data.models.length}`) : f === 'problem' ? tr(`有问题 ${problems}`, `Problems ${problems}`) : tr(`已隐藏 ${data.models.filter((m) => m.hidden).length}`, `Hidden ${data.models.filter((m) => m.hidden).length}`)}
              </button>
            ))}
            <input className="input" style={{ maxWidth: 200 }} placeholder={tr('搜索模型', 'Search model')} value={q} onChange={(e) => setQ(e.target.value)} />
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
            <table className="table mh-table" data-testid="model-health-table">
              <thead>
                <tr>
                  <th>{tr('模型', 'Model')}</th>
                  <th>{tr('状态', 'Status')}</th>
                  <th className="hide-sm">{tr('凭据', 'Creds')}</th>
                  <th className="hide-sm">{tr('最近检测', 'Last check')}</th>
                  <th className="right">{tr('操作', 'Actions')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((m) => {
                  const reason = shortReason(m, tr);
                  return (
                    <tr key={m.id} data-testid={`mh-row-${m.id}`} className={m.hidden ? 'row-muted' : ''}>
                      <td className="mh-model">
                        <button className="btn-link mono ellipsis" onClick={() => setOpen(m.id)} title={m.display_name ? `${m.id} — ${m.display_name}` : m.id}>
                          {m.id}
                        </button>
                        <span className="chips">
                          {noteChips(m).map((c) => (
                            <Chip key={c.kind} kind={c.kind} title={c.title} />
                          ))}
                          {m.hidden && <span className="chip chip-hidden" title={tr('已隐藏（oauth-excluded-models）', 'Hidden')}>{tr('隐', 'H')}</span>}
                        </span>
                      </td>
                      <td className="mh-status" title={m.reason || reason}>
                        {statusBadge(m.status, tr, earliestRetry(m))}
                        {reason && <span className="ellipsis faint small">{reason}</span>}
                      </td>
                      <td className="hide-sm">
                        <CredSummary m={m} />
                      </td>
                      <td className="hide-sm small nowrap" title={m.last_check?.message}>
                        {m.last_check ? (
                          <>
                            <span className={m.last_check.outcome === 'ok' ? 'ok-text' : 'err-text'}>
                              {tr(OUTCOME_ZH[m.last_check.outcome] ?? m.last_check.outcome, m.last_check.outcome)}
                              {m.last_check.http_status && m.last_check.outcome !== 'ok' ? ` ${m.last_check.http_status}` : ''}
                            </span>{' '}
                            <span className="faint">{fmtRelative(m.last_check.checked_at, lang)}</span>
                          </>
                        ) : (
                          <span className="faint">—</span>
                        )}
                      </td>
                      <td className="right nowrap">
                        <button className="btn btn-sm" disabled={!!busy} onClick={() => void retest(m.id)} data-testid={`mh-retest-${m.id}`} title={tr('重新测试', 'Retest')}>
                          {busy === `retest:${m.id}` ? <Spinner /> : <IconRefresh />}
                          <span className="hide-sm"> {tr('测试', 'Test')}</span>
                        </button>{' '}
                        <span title={m.hidden ? tr('已隐藏，点击显示', 'Hidden – click to show') : tr('显示中，点击隐藏', 'Shown – click to hide')} style={{ display: 'inline-block' }}>
                          <Switch checked={!m.hidden} disabled={!!busy} onChange={(v) => void setHidden(m.id, !v)} />
                        </span>
                      </td>
                    </tr>
                  );
                })}
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
          {statusBadge(m.status, tr, earliestRetry(m))}
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
                    {c.state === 'cooling' && c.next_retry_after ? ` · ${fmtRecover(c.next_retry_after)}恢复` : ''}
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
