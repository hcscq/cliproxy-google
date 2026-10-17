import type { ReserveStats } from '../api/client';
import { useI18n } from '../i18n';

/** Text of the shortfall warning, or null when there is nothing to warn about. */
export function reserveShortText(s: ReserveStats | null | undefined, tr: (zh: string, en: string) => string): string | null {
  if (!s || !s.short) return null;
  const hasReserve = s.reserved > 0;
  return tr(
    `储备不足：可用 ${s.active} / 最低 ${s.min_active}，储备 ${s.reserved}${hasReserve ? '（无符合订阅筛选/体检条件的）' : ''}`,
    `Reserve short: available ${s.active} / min ${s.min_active}, reserve ${s.reserved}${hasReserve ? ' (none match the tier filter / check policy)' : ''}`,
  );
}

export function ReserveShortAlert({ stats }: { stats?: ReserveStats | null }) {
  const { tr } = useI18n();
  const text = reserveShortText(stats, tr);
  if (!text) return null;
  return (
    <div className="alert warn small" data-testid="reserve-short">
      {text}
    </div>
  );
}
