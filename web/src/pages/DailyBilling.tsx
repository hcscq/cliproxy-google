import { useMemo, useState } from 'react';
import { useApi, useAsync } from '../app-context';
import type { DailyBillingDay, DailyBillingReport } from '../api/client';
import { fmtCompact, fmtNum, fmtPct, fmtUSD } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Stat } from '../components/ui';
import { IconRefresh } from '../components/icons';

const TIERS = ['pro', 'ultra', 'other'] as const;
const TIER_COLOR: Record<string, string> = { pro: 'var(--primary)', ultra: 'var(--ok)', other: 'var(--text-3)' };

export function isoDate(d: Date): string {
  const z = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`;
}

/** Default inclusive range: yesterday through today in the local calendar. */
export function defaultBillingRange(now = new Date()): { from: string; to: string } {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  return { from: isoDate(yesterday), to: isoDate(today) };
}

export function variantLabel(model: string): string {
  const prefix = 'gemini-4-argon';
  if (model === prefix) return 'argon';
  if (model.startsWith(`${prefix}-`)) return model.slice(prefix.length + 1);
  return model;
}

export function tierLabel(tier: string, zh: boolean): string {
  if (tier === 'other') return zh ? '其他' : 'other';
  return tier;
}

export function DailyBillingPage() {
  const api = useApi();
  const { t, tr, lang } = useI18n();
  const initial = useMemo(() => defaultBillingRange(), []);
  const [from, setFrom] = useState(initial.from);
  const [to, setTo] = useState(initial.to);
  const [applied, setApplied] = useState(initial);
  const timezone = useMemo(() => Intl.DateTimeFormat().resolvedOptions().timeZone, []);
  const report = useAsync(
    () => api.dailyBilling({ from: applied.from, to: applied.to, timezone }),
    [applied.from, applied.to, timezone],
  );
  const data = report.data;
  const zh = lang === 'zh';

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            {t('dailyBilling')} <Badge kind="warn">{tr('估算', 'Estimate')}</Badge>
          </h1>
          <p>
            {tr(
              '金额来自 new-api。pro / ultra 按当天 CPA 成功 token 占比分摊，不是逐条归因。',
              'Amounts come from new-api. pro / ultra are split by that day’s CPA success-token share, not per request.',
            )}
          </p>
        </div>
        <form
          className="toolbar"
          onSubmit={(e) => {
            e.preventDefault();
            setApplied({ from, to });
          }}
        >
          <input className="input input-sm" style={{ width: 150 }} type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} aria-label={tr('开始日期', 'From')} />
          <input className="input input-sm" style={{ width: 150 }} type="date" value={to} min={from} onChange={(e) => setTo(e.target.value)} aria-label={tr('结束日期', 'To')} />
          <button className="btn btn-sm" type="submit">{tr('查询', 'Apply')}</button>
          <button className="btn btn-sm" type="button" onClick={report.reload}>
            <IconRefresh />
            {t('refresh')}
          </button>
        </form>
      </div>

      {!!report.error && <ErrorBox error={report.error} onRetry={report.reload} />}
      {data && !data.configured && <div className="alert">{data.message || tr('未配置 new-api 数据源', 'new-api data source is not configured')}</div>}
      {data?.configured && <Discrepancy report={data} />}

      <div className="grid grid-4">
        <Stat label={tr('估算 USD', 'Estimated USD')} value={data?.configured ? fmtUSD(data.totals.usd) : data ? '—' : '…'} sub={data?.configured ? `quota ÷ ${fmtNum(data.quota_per_unit)}` : undefined} />
        <Stat label={tr('pro 占比', 'pro share')} value={data?.configured ? fmtPct(data.totals.pro_ratio) : data ? '—' : '…'} sub={data?.configured ? `pro ${fmtUSD(data.totals.pro_usd)} · ultra ${fmtUSD(data.totals.ultra_usd)}` : undefined} />
        <Stat label={tr('请求', 'Requests')} value={data?.configured ? fmtNum(data.totals.requests) : data ? '—' : '…'} sub={data?.configured ? `${tr('输入', 'in')} ${fmtCompact(data.totals.prompt_tokens)} · ${tr('输出', 'out')} ${fmtCompact(data.totals.completion_tokens)}` : undefined} />
        <Stat
          label={tr('成功请求差', 'Success-count gap')}
          value={data?.configured && data.discrepancy ? fmtNum(data.discrepancy.difference) : data ? '—' : '…'}
          sub={data?.configured && data.discrepancy ? `new-api ${fmtNum(data.discrepancy.new_api_success_requests)} · CPA ${fmtNum(data.discrepancy.cpa_success_requests)}` : undefined}
        />
      </div>

      {data?.configured && (
        <p className="faint">
          {tr('渠道', 'Channel')} {data.channel_id || tr('全部', 'all')} · group_ratio {data.group_ratio} · {data.timezone}
        </p>
      )}

      <Card title={tr('每日估算 USD', 'Estimated USD by day')}>
        {!data && <Loading />}
        {data?.configured && data.days.length > 0 && <BillingChart days={data.days} />}
        {data?.configured && data.days.length === 0 && <Empty />}
        {data && !data.configured && <Empty>{tr('配置数据源后显示', 'Shown after the data source is configured')}</Empty>}
      </Card>

      {data?.configured && (
      <Card bodyClass="table-wrap" title={<h2>{tr('档位 × 变体', 'Tier × variant')}</h2>}>
        {data.days.some((d) => d.rows.length > 0) ? (
          <table className="table">
            <thead>
              <tr>
                <th>{tr('日期', 'Date')}</th>
                <th>{tr('变体', 'Variant')}</th>
                <th>{tr('档位', 'Tier')}</th>
                <th className="right">{tr('请求', 'Requests')}</th>
                <th className="right">{tr('输入', 'Prompt')}</th>
                <th className="right">{tr('输出', 'Completion')}</th>
                <th className="right">quota</th>
                <th className="right">USD</th>
              </tr>
            </thead>
            <tbody>
              {data.days.flatMap((day) =>
                day.rows.map((row) => (
                  <tr key={`${day.date}-${row.model}-${row.tier}`}>
                    <td>
                      {day.date}
                      {day.unallocated && row.tier === 'other' ? <div className="cell-sub">{tr('当天无 CPA 样本，全部计入其他', 'No CPA sample this day; all of it is other')}</div> : null}
                    </td>
                    <td title={row.model}>{variantLabel(row.model)}</td>
                    <td>{tierLabel(row.tier, zh)}</td>
                    <td className="right">{fmtNum(row.requests)}</td>
                    <td className="right">{fmtCompact(row.prompt_tokens)}</td>
                    <td className="right">{fmtCompact(row.completion_tokens)}</td>
                    <td className="right">{fmtCompact(row.quota)}</td>
                    <td className="right">{fmtUSD(row.usd)}</td>
                  </tr>
                )),
              )}
            </tbody>
          </table>
        ) : (
          <Empty />
        )}
      </Card>
      )}
    </div>
  );
}

function Discrepancy({ report }: { report: DailyBillingReport }) {
  const { tr } = useI18n();
  const gap = report.discrepancy;
  if (!gap) return null;
  const text = tr(
    `成功请求 new-api ${fmtNum(gap.new_api_success_requests)}，CPA ${fmtNum(gap.cpa_success_requests)}，差 ${fmtNum(gap.difference)}。档位拆分按这个误差理解。`,
    `Successful requests: new-api ${fmtNum(gap.new_api_success_requests)}, CPA ${fmtNum(gap.cpa_success_requests)}, gap ${fmtNum(gap.difference)}. Read the tier split with that gap in mind.`,
  );
  return <div className={`alert ${gap.difference === 0 ? '' : 'warn'}`}>{text}</div>;
}

function BillingChart({ days }: { days: DailyBillingDay[] }) {
  const { tr } = useI18n();
  const width = 800;
  const height = 180;
  const padL = 44;
  const padB = 22;
  const max = Math.max(0.000001, ...days.map((d) => d.totals.usd || 0));
  const n = Math.max(1, days.length);
  const bw = (width - padL) / n;
  const h = height - padB;
  return (
    <div>
      <svg className="chart" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ height }}>
        {days.map((day, i) => {
          const x = padL + i * bw + bw * 0.18;
          const w = Math.max(1, bw * 0.64);
          const parts = TIERS.map((tier) => (tier === 'pro' ? day.totals.pro_usd : tier === 'ultra' ? day.totals.ultra_usd : day.totals.other_usd) || 0);
          let y = h;
          return (
            <g key={day.date}>
              {parts.map((value, pi) => {
                const bar = (value / max) * (h - 8);
                y -= bar;
                return <rect key={TIERS[pi]} x={x} y={y} width={w} height={bar} fill={TIER_COLOR[TIERS[pi]]} rx={1.5} />;
              })}
              <text x={padL + i * bw + bw / 2} y={height - 4} textAnchor="middle">
                {day.date.slice(5)}
              </text>
            </g>
          );
        })}
      </svg>
      <div className="legend" style={{ marginTop: 6 }}>
        {TIERS.map((tier) => (
          <span key={tier}>
            <i style={{ background: TIER_COLOR[tier] }} />
            {tier === 'other' ? tr('其他', 'other') : tier}
          </span>
        ))}
      </div>
    </div>
  );
}
