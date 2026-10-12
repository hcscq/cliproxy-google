import type { QuotaItem } from '../api/client';
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
    <div className="stack" style={{ gap: compact ? 6 : 10, minWidth: compact ? 180 : undefined }}>
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
              <span className="spacer ellipsis">{w.label ?? w.id}</span>
              <b>{valueText}</b>
              {!compact && w.reset_at && <span className="faint">· {tr('重置', 'resets')} {fmtRelative(w.reset_at, lang)}</span>}
            </div>
            <Progress ratio={ratio} />
          </div>
        );
      })}
    </div>
  );
}
