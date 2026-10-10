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
export function QuotaWindows({ item, compact }: { item?: QuotaItem; compact?: boolean }) {
  const { tr, lang } = useI18n();
  if (!item || !item.primary_windows?.length) return <span className="faint small">{tr('暂无额度数据', 'No quota data')}</span>;
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
