import type { QuotaItem, QuotaWindow } from '../api/client';
import { fmtNum, fmtRelative } from '../format';
import { useI18n } from '../i18n';
import { Badge, Progress } from './ui';

export function quotaKind(status?: string): 'ok' | 'warn' | 'err' | undefined {
  switch (status) {
    case 'healthy':
      return 'ok';
    case 'low':
    case 'stale':
      return 'warn';
    case 'exhausted':
    case 'error':
      return 'err';
    default:
      return undefined;
  }
}

export function QuotaStatusBadge({ item }: { item?: QuotaItem }) {
  const { tr } = useI18n();
  if (!item) return <span className="faint small">—</span>;
  const names: Record<string, [string, string]> = {
    healthy: ['充足', 'Healthy'],
    low: ['偏低', 'Low'],
    exhausted: ['耗尽', 'Exhausted'],
    error: ['错误', 'Error'],
    unknown: ['未知', 'Unknown'],
    unsupported: ['不支持', 'Unsupported'],
  };
  const n = names[item.quota_status ?? 'unknown'] ?? [item.quota_status ?? '?', item.quota_status ?? '?'];
  return (
    <Badge kind={quotaKind(item.quota_status)} dot>
      {tr(n[0], n[1])}
    </Badge>
  );
}

/** Compact quota windows (e.g. Antigravity "Gemini Models" weekly %). */
/** Below this remaining ratio the compact view shows the known reset time. */
export const LOW_QUOTA_RATIO = 0.05;

/** "10-17 13:36" in local time. */
export function fmtResetShort(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Compact recovery time: "19:12" today, otherwise "10-17 13:36". */
export function fmtRecover(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const now = new Date();
  const p = (n: number) => String(n).padStart(2, '0');
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`;
  return d.toDateString() === now.toDateString() ? hm : `${fmtResetShort(iso)}`;
}

export function QuotaWindows({ item, compact, error, loading }: { item?: QuotaItem; compact?: boolean; error?: unknown; loading?: boolean }) {
  const { tr, lang } = useI18n();
  if (!item || !item.primary_windows?.length) {
    if (loading) return <span className="faint small">{tr('加载中…', 'Loading…')}</span>;
    if (error) {
      const msg = error instanceof Error ? error.message : String(error);
      return (
        <span className="err-text small" title={msg}>
          {tr('额度接口请求失败', 'Quota API request failed')}
        </span>
      );
    }
    let why: [string, string] = ['暂无额度数据：Home 尚未探测该凭据，可点表头“刷新”', 'No quota data yet: Home has not probed this credential — use Refresh in the header'];
    if (item?.quota_status === 'unsupported') why = ['该凭据类型上游不提供额度接口', 'Upstream exposes no quota API for this credential type'];
    else if (item?.collection_status === 'failed' || item?.quota_status === 'error') why = ['额度探测失败，详见凭据详情', 'Quota probe failed — see credential details'];
    else if (item) why = ['上游本次未返回额度窗口', 'Upstream returned no quota windows on the last probe'];
    return (
      <span className="faint small" title={tr(why[0], why[1])}>
        {item?.quota_status === 'unsupported' ? tr('不支持', 'Unsupported') : tr('暂无额度数据', 'No quota data')}
      </span>
    );
  }
  return (
    <div className="stack" style={{ gap: compact ? 6 : 10, minWidth: 0 }}>
      {item.primary_windows.map((w) => {
        const ratio = w.is_unlimited ? 1 : w.remaining_ratio ?? (w.limit ? (w.remaining ?? 0) / w.limit : undefined);
        const valueText = w.is_unlimited
          ? tr('无限制', 'Unlimited')
          : w.unit === 'percentage'
            ? `${fmtNum(w.remaining, 1)}%`
            : `${fmtNum(w.remaining)} / ${fmtNum(w.limit)}`;
        return (
          <div key={w.id}>
            <div className="row small" style={{ marginBottom: 3, gap: 6 }}>
              <span className="spacer ellipsis">{w.label ?? w.id}{windowPeriodTag(w) && !(w.label ?? '').match(/5h|week|周/i) ? ` · ${windowPeriodTag(w)}` : ''}</span>
              <b>{valueText}</b>
              {!compact && w.reset_at && <span className="faint">· {tr('重置', 'resets')} {fmtRelative(w.reset_at, lang)}</span>}
              {compact && w.reset_at && ratio !== undefined && ratio < LOW_QUOTA_RATIO && (
                <span className="warn-text" title={tr('额度将在此时重置', 'Quota resets at')}>{tr('重置', 'reset')} {fmtResetShort(w.reset_at)}</span>
              )}
            </div>
            <Progress ratio={ratio} />
          </div>
        );
      })}
    </div>
  );
}

/** True for the Claude / GPT ("third-party") bucket of an Antigravity credential, or a Claude account window. */
export function isClaudeWindow(w: QuotaWindow): boolean {
  const s = `${w.scope_id ?? ''} ${w.id} ${w.label ?? ''}`.toLowerCase();
  return s.includes('third-party') || s.includes('claude') || /(^|-)3p(-|$)/.test(w.id.toLowerCase());
}

/** "5h" / "周" for duration windows, '' when unknown. */
export function windowPeriodTag(w: QuotaWindow): string {
  const s = `${w.id} ${w.label ?? ''}`.toLowerCase();
  if (/(^|[-\s])5h($|[-\s])|5-hour|5 hour/.test(s) || (w.window_seconds && w.window_seconds <= 6 * 3600)) return '5h';
  if (s.includes('week') || (w.window_seconds && w.window_seconds >= 6 * 86400)) return '周';
  return '';
}

function windowGroupLabel(w: QuotaWindow): string {
  if (isClaudeWindow(w)) return 'Claude';
  const s = `${w.scope_id ?? ''} ${w.id} ${w.label ?? ''}`.toLowerCase();
  if (s.includes('gemini')) return 'Gemini';
  return (w.label ?? w.id).replace(/ models?$/i, '').slice(0, 10);
}

function windowShortLabel(w: QuotaWindow, withGroup: boolean): string {
  const tag = windowPeriodTag(w);
  if (!withGroup) return tag || windowGroupLabel(w);
  return tag ? `${windowGroupLabel(w)} ${tag}` : windowGroupLabel(w);
}

/** One-line quota for the list: "Gemini ▬ 94% · Claude ▬ 100%"; details in each segment's tooltip. */
export function QuotaInline({ item, showClaude, error, loading }: { item?: QuotaItem; showClaude: boolean; error?: unknown; loading?: boolean }) {
  const { tr, lang } = useI18n();
  if (!item || !item.primary_windows?.length) return <QuotaWindows item={item} compact error={error} loading={loading} />;
  const all = item.primary_windows;
  const shown = showClaude ? all : all.filter((w) => !isClaudeWindow(w));
  const hidden = all.length - shown.length;
  return (
    <span className="quota-inline" data-testid="quota-inline">
      {shown.map((w, i) => {
        const withGroup = i === 0 || windowGroupLabel(shown[i - 1]) !== windowGroupLabel(w);
        const ratio = w.is_unlimited ? 1 : w.remaining_ratio ?? (w.limit ? (w.remaining ?? 0) / w.limit : undefined);
        const value = w.is_unlimited ? '∞' : w.unit === 'percentage' ? `${fmtNum(w.remaining, 0)}%` : `${fmtNum(w.remaining)}/${fmtNum(w.limit)}`;
        const low = ratio !== undefined && ratio < LOW_QUOTA_RATIO;
        const title = [w.label ?? w.id, `${tr('剩余', 'remaining')} ${value}`, w.reset_at ? `${tr('重置', 'resets')} ${new Date(w.reset_at).toLocaleString()} (${fmtRelative(w.reset_at, lang)})` : ''].filter(Boolean).join('\n');
        const pct = ratio === undefined ? 0 : Math.max(0, Math.min(1, ratio)) * 100;
        return (
          <span key={w.id} className="qseg" title={title} data-testid={isClaudeWindow(w) ? 'quota-claude' : 'quota-window'}>
            <span className="qlabel">{windowShortLabel(w, withGroup)}</span>
            <span className="qbar"><span style={{ width: `${pct}%`, background: low ? 'var(--err)' : pct < 30 ? 'var(--warn)' : 'var(--ok)' }} /></span>
            <b className={low ? 'err-text' : undefined}>{value}</b>
          </span>
        );
      })}
      {hidden > 0 && <span className="faint" title={tr('Claude 额度已隐藏（列 ▾ →「显示 Claude 额度」）', 'Claude quota hidden (Columns ▾ → Show Claude quota)')}>+{hidden}</span>}
    </span>
  );
}
