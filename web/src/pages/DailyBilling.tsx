import { useEffect, useMemo, useState } from 'react';
import { useApi, useAsync } from '../app-context';
import type { DailyBillingCostSettings, DailyBillingDay, DailyBillingReport, DailyBillingTier } from '../api/client';
import { fmtCompact, fmtNum, fmtPct, fmtUSD } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Stat } from '../components/ui';
import { IconRefresh } from '../components/icons';

const TIERS = ['pro', 'ultra', 'other'] as const;
const TIER_COLOR: Record<string, string> = { pro: 'var(--primary)', ultra: 'var(--ok)', other: 'var(--text-3)' };
const EMPTY_COST: DailyBillingCostSettings = {
  pro_monthly_cost: 0,
  ultra_monthly_cost: 0,
  other_monthly_cost: 0,
  pro_daily_cost: 0,
  ultra_daily_cost: 0,
  other_daily_cost: 0,
  month_days: 0,
};

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

export function showsBillingCost(report?: { cost_configured?: boolean } | null): boolean {
  return !!report?.cost_configured;
}

export function findBillingTier(tiers: DailyBillingTier[] | undefined, name: string): DailyBillingTier | undefined {
  return tiers?.find((tier) => tier.tier === name);
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
  const showCost = showsBillingCost(data);

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            {t('dailyBilling')} <Badge kind="warn">{tr('估算', 'Estimate')}</Badge>
          </h1>
          <p>
            {tr(
              '全模型金额来自 new-api。pro / ultra 按当天 CPA 成功 token 占比分摊，不是逐条归因。',
              'Amounts cover every model in new-api. pro / ultra are split by that day’s CPA success-token share, not per request.',
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

      {data?.configured && (
        <div className="grid grid-2">
          <TierCard report={data} tier="pro" zh={zh} showCost={showCost} />
          <TierCard report={data} tier="ultra" zh={zh} showCost={showCost} />
        </div>
      )}
      {data?.configured && hasActivity(findBillingTier(data.tiers, 'other')) && (
        <TierCard report={data} tier="other" zh={zh} showCost={showCost} />
      )}

      {!data && (
        <div className="grid grid-4">
          <Stat label={tr('估算 USD', 'Estimated USD')} value="…" />
          <Stat label={tr('请求', 'Requests')} value="…" />
          <Stat label="quota" value="…" />
          <Stat label={tr('成功请求差', 'Success-count gap')} value="…" />
        </div>
      )}

      {data?.configured && (
        <p className="faint">
          {filterLine(data, tr)}
          {showCost ? ` · ${tr('成本为估算', 'cost is estimated')}` : ''}
        </p>
      )}

      {data && <CostForm settings={data.cost_settings} source={data.cost_source} onSaved={report.reload} />}

      <Card title={tr('每日估算 USD', 'Estimated USD by day')}>
        {!data && <Loading />}
        {data?.configured && data.days.length > 0 && <BillingChart days={data.days} />}
        {data?.configured && data.days.length === 0 && <Empty />}
        {data && !data.configured && <Empty>{tr('配置数据源后显示', 'Shown after the data source is configured')}</Empty>}
      </Card>

      {data?.configured &&
        TIERS.map((tier) => {
          const summary = findBillingTier(data.tiers, tier);
          const dayActivity = data.days.some((day) => hasActivity(findBillingTier(day.tiers, tier)));
          if (!hasActivity(summary) && !dayActivity) return null;
          return <TierModels key={tier} report={data} tier={tier} zh={zh} showCost={showCost} />;
        })}
    </div>
  );
}

function TierCard({ report, tier, zh, showCost }: { report: DailyBillingReport; tier: string; zh: boolean; showCost: boolean }) {
  const { tr } = useI18n();
  const summary = findBillingTier(report.tiers, tier);
  const span = Math.max(1, report.days.length);
  const active = span > 1 ? summary?.active_credential_days || summary?.active_credentials || 0 : summary?.active_credentials || 0;
  const profit = summary?.gross_profit_usd;
  return (
    <Card
      title={
        <h2>
          {tierLabel(tier, zh)} <Badge kind="warn">{tr('估算', 'Estimate')}</Badge>
        </h2>
      }
    >
      <div className="tier-metrics">
        <Metric label={tr('估算 USD', 'Estimated USD')} value={fmtUSD(summary?.usd)} sub={`quota ${fmtCompact(summary?.quota)}`} />
        <Metric label={tr('请求', 'Requests')} value={fmtNum(summary?.requests)} sub={`${tr('输入', 'in')} ${fmtCompact(summary?.prompt_tokens)} · ${tr('输出', 'out')} ${fmtCompact(summary?.completion_tokens)}`} />
        <Metric label="quota" value={fmtCompact(summary?.quota)} sub={`÷ ${fmtNum(report.quota_per_unit)}`} />
        {showCost && (
          <>
            <Metric label={tr('估算成本', 'Estimated cost')} value={fmtUSD(summary?.cost_usd)} sub={`${span > 1 ? tr('活跃凭据·天', 'credential-days') : tr('活跃凭据', 'Active credentials')} ${fmtNum(active)}`} />
            <Metric label={tr('估算毛利', 'Estimated margin $')} value={fmtUSD(profit)} sub={profit != null && profit < 0 ? tr('低于成本', 'below cost') : undefined} negative={profit != null && profit < 0} />
            <Metric label={tr('毛利率', 'Margin')} value={summary?.gross_margin == null ? '—' : fmtPct(summary.gross_margin)} sub={summary?.daily_unit_usd != null && span === 1 ? `${tr('日均摊', 'Daily unit')} ${fmtUSD(summary.daily_unit_usd)}` : tr('多日成本累加', 'Costs summed by day')} />
          </>
        )}
      </div>
    </Card>
  );
}

function Metric({ label, value, sub, negative }: { label: string; value: string; sub?: string; negative?: boolean }) {
  return (
    <div>
      <div className="stat-label">{label}</div>
      <div className="stat-value" style={negative ? { color: 'var(--err)' } : undefined}>{value}</div>
      {sub && <div className="stat-sub">{sub}</div>}
    </div>
  );
}

function TierModels({ report, tier, zh, showCost }: { report: DailyBillingReport; tier: string; zh: boolean; showCost: boolean }) {
  const { tr } = useI18n();
  const days = report.days
    .map((day) => ({ day, tier: findBillingTier(day.tiers, tier) }))
    .filter((item) => item.tier && (item.tier.requests > 0 || item.tier.quota > 0 || (item.tier.models?.length ?? 0) > 0));
  const models = days.flatMap(({ day, tier: summary }) =>
    (summary?.models ?? []).map((model) => ({ date: day.date, unallocated: day.unallocated, model })),
  );
  return (
    <details className="card tier-fold" open={tier !== 'other'}>
      <summary className="card-head">
        <h2>{tierLabel(tier, zh)} · {tr('模型明细', 'Models')}</h2>
      </summary>
      <div className="table-wrap">
        {report.days.length > 1 && days.length > 0 && (
          <table className="table">
            <thead>
              <tr>
                <th>{tr('日期', 'Date')}</th>
                <th className="right">{tr('请求', 'Requests')}</th>
                <th className="right">quota</th>
                <th className="right">{tr('估算 USD', 'Estimated USD')}</th>
                {showCost && <th className="right">{tr('活跃凭据', 'Active credentials')}</th>}
                {showCost && <th className="right">{tr('估算成本', 'Estimated cost')}</th>}
                {showCost && <th className="right">{tr('估算毛利', 'Estimated margin $')}</th>}
              </tr>
            </thead>
            <tbody>
              {days.map(({ day, tier: summary }) => (
                <tr key={`${tier}-${day.date}`}>
                  <td>
                    {day.date}
                    {day.unallocated && tier === 'other' ? <div className="cell-sub">{tr('当天无 CPA 样本，全部计入其他', 'No CPA sample this day; all of it is other')}</div> : null}
                  </td>
                  <td className="right">{fmtNum(summary?.requests)}</td>
                  <td className="right">{fmtCompact(summary?.quota)}</td>
                  <td className="right">{fmtUSD(summary?.usd)}</td>
                  {showCost && <td className="right">{fmtNum(summary?.active_credentials)}</td>}
                  {showCost && <td className="right">{fmtUSD(summary?.cost_usd)}</td>}
                  {showCost && <td className="right">{fmtUSD(summary?.gross_profit_usd)}</td>}
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {models.length > 0 ? (
          <table className="table">
            <thead>
              <tr>
                <th>{tr('日期', 'Date')}</th>
                <th>{tr('模型', 'Model')}</th>
                <th className="right">{tr('请求', 'Requests')}</th>
                <th className="right">{tr('输入', 'Prompt')}</th>
                <th className="right">{tr('输出', 'Completion')}</th>
                <th className="right">quota</th>
                <th className="right">{tr('估算 USD', 'Estimated USD')}</th>
              </tr>
            </thead>
            <tbody>
              {models.map((row) => (
                <tr key={`${row.date}-${row.model.model}`}>
                  <td>{row.date}</td>
                  <td title={row.model.model}>{variantLabel(row.model.model)}</td>
                  <td className="right">{fmtNum(row.model.requests)}</td>
                  <td className="right">{fmtCompact(row.model.prompt_tokens)}</td>
                  <td className="right">{fmtCompact(row.model.completion_tokens)}</td>
                  <td className="right">{fmtCompact(row.model.quota)}</td>
                  <td className="right">{fmtUSD(row.model.usd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <Empty />
        )}
      </div>
    </details>
  );
}

function CostForm({ settings, source, onSaved }: { settings?: DailyBillingCostSettings; source?: string; onSaved: () => void }) {
  const api = useApi();
  const { tr, t } = useI18n();
  const [draft, setDraft] = useState<DailyBillingCostSettings>(settings ?? EMPTY_COST);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!dirty && settings) setDraft({ ...EMPTY_COST, ...settings });
  }, [settings, dirty]);

  const set = (key: keyof DailyBillingCostSettings, value: number) => {
    setDirty(true);
    setDraft((prev) => ({ ...prev, [key]: value }));
  };

  return (
    <details className="card tier-fold">
      <summary className="card-head">
        <h2>{tr('成本口径', 'Cost basis')}</h2>
        <span className="faint">{tr('留空或 0 只显示计费', 'Empty or 0 shows billing only')}</span>
      </summary>
      <form
        className="card-body"
        onSubmit={async (e) => {
          e.preventDefault();
          setSaving(true);
          setError(null);
          try {
            await api.updateDailyBillingCost({
              pro_monthly_cost: num(draft.pro_monthly_cost),
              ultra_monthly_cost: num(draft.ultra_monthly_cost),
              other_monthly_cost: num(draft.other_monthly_cost),
              pro_daily_cost: num(draft.pro_daily_cost),
              ultra_daily_cost: num(draft.ultra_daily_cost),
              other_daily_cost: num(draft.other_daily_cost),
              month_days: Math.max(0, Math.round(num(draft.month_days))),
            });
            setDirty(false);
            onSaved();
          } catch (err) {
            setError(err);
          } finally {
            setSaving(false);
          }
        }}
      >
        <p className="faint" style={{ marginTop: 0 }}>
          {tr(
            '每个凭据的月成本按当月天数均摊，再乘当天该档有成功请求的去重凭据数。填写日成本则覆盖月均摊。金额都是估算。',
            'Monthly cost per credential is spread over the calendar month, then multiplied by that day’s distinct credentials with a successful request. A daily cost replaces the monthly figure. Amounts are estimates.',
          )}
          {source === 'panel' ? ` ${tr('当前来自面板。', 'Currently from the panel.')}` : ` ${tr('当前来自配置。', 'Currently from config.')}`}
        </p>
        {!!error && <ErrorBox error={error} />}
        <div className="cost-form">
          <Money label={tr('pro 月成本 / 凭据', 'pro monthly / credential')} value={draft.pro_monthly_cost} onChange={(v) => set('pro_monthly_cost', v)} />
          <Money label={tr('ultra 月成本 / 凭据', 'ultra monthly / credential')} value={draft.ultra_monthly_cost} onChange={(v) => set('ultra_monthly_cost', v)} />
          <Money label={tr('其他 月成本 / 凭据', 'other monthly / credential')} value={draft.other_monthly_cost} onChange={(v) => set('other_monthly_cost', v)} />
          <Money label={tr('pro 日成本 / 凭据', 'pro daily / credential')} value={draft.pro_daily_cost} onChange={(v) => set('pro_daily_cost', v)} />
          <Money label={tr('ultra 日成本 / 凭据', 'ultra daily / credential')} value={draft.ultra_daily_cost} onChange={(v) => set('ultra_daily_cost', v)} />
          <Money label={tr('其他 日成本 / 凭据', 'other daily / credential')} value={draft.other_daily_cost} onChange={(v) => set('other_daily_cost', v)} />
          <Money label={tr('均摊天数（空=当月）', 'Days in month (empty = calendar)')} value={draft.month_days} step="1" onChange={(v) => set('month_days', v)} />
          <div>
            <button className="btn btn-primary btn-sm" type="submit" disabled={saving}>{saving ? t('loading') : t('save')}</button>
          </div>
        </div>
      </form>
    </details>
  );
}

function Money({ label, value, onChange, step = '0.01' }: { label: string; value: number; onChange: (v: number) => void; step?: string }) {
  return (
    <label>
      {label}
      <input
        className="input input-sm"
        type="number"
        min="0"
        step={step}
        value={value || ''}
        onChange={(e) => onChange(e.target.value === '' ? 0 : Number(e.target.value))}
      />
    </label>
  );
}

function num(value: number | undefined): number {
  if (value == null || Number.isNaN(value) || value < 0) return 0;
  return value;
}

function hasActivity(tier?: DailyBillingTier): boolean {
  if (!tier) return false;
  return tier.requests > 0 || tier.quota > 0 || (tier.cost_usd ?? 0) > 0 || (tier.models?.length ?? 0) > 0;
}

function filterLine(data: DailyBillingReport, tr: (zh: string, en: string) => string): string {
  const model = data.models?.length ? data.models.join(', ') : data.model_prefix ? `${tr('前缀', 'prefix')} ${data.model_prefix}` : tr('全模型', 'all models');
  const exclude = data.model_exclude ? ` · ${tr('排除', 'exclude')} ${data.model_exclude}` : '';
  const channels = data.channel_ids?.length ? data.channel_ids.join(', ') : data.channel_id ? String(data.channel_id) : tr('全部', 'all');
  return `${model}${exclude} · ${tr('渠道', 'Channel')} ${channels} · group_ratio ${data.group_ratio ?? '—'} · ${data.timezone ?? ''}`;
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
