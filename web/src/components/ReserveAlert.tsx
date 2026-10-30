import { useState } from 'react';
import type { ReserveEvent, ReserveStats } from '../api/client';
import { useI18n } from '../i18n';

/** Text of the shortfall warning, or null when there is nothing to warn about. */
export function reserveShortText(s: ReserveStats | null | undefined, tr: (zh: string, en: string) => string): string | null {
  if (!s || !s.short) return null;
  const hasReserve = s.reserved > 0;
  return tr(
    `储备不足：可用 ${s.active}（不含 Free）/ 最低 ${s.min_active}，储备 ${s.reserved}${hasReserve ? '（无符合订阅筛选/体检条件的）' : ''}`,
    `Reserve short: available ${s.active} (excl. Free) / min ${s.min_active}, reserve ${s.reserved}${hasReserve ? ' (none match the tier filter / check policy)' : ''}`,
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

/** One-line reserve summary (counts exclude Free tier, as the promoter counts them). */
export function reserveSummaryText(s: ReserveStats | null | undefined, tr: (zh: string, en: string) => string, now = Date.now()): string | null {
  if (!s) return null;
  const n = (v?: number) => (v === undefined || v < 0 ? '—' : String(v));
  const cmp = s.count_cooled === false ? tr('可用', 'usable') : tr('启用', 'enabled');
  const ready = s.eligible >= 0 ? n(s.eligible) : '—';
  let last = tr('从未', 'never');
  if (s.last_promotion) {
    const mins = Math.max(0, Math.round((now / 1000 - s.last_promotion) / 60));
    last = mins < 60 ? tr(`${mins} 分钟前`, `${mins}m ago`) : mins < 2880 ? tr(`${Math.round(mins / 60)} 小时前`, `${Math.round(mins / 60)}h ago`) : tr(`${Math.round(mins / 1440)} 天前`, `${Math.round(mins / 1440)}d ago`);
  }
  return tr(
    `储备：最低 ${s.min_active > 0 ? s.min_active : '未设置'}（按${cmp}数）· 启用 ${n(s.enabled)} · 可用 ${n(s.usable)}（均不含 Free）· 储备 ${s.reserved} / 可提升 ${ready} · 上次提升 ${last}`,
    `Reserve: min ${s.min_active > 0 ? s.min_active : 'off'} (vs ${cmp}) · enabled ${n(s.enabled)} · usable ${n(s.usable)} (excl. Free) · reserve ${s.reserved} / ready ${ready} · last promotion ${last}`,
  );
}

export function ReserveSummary({ stats }: { stats?: ReserveStats | null }) {
  const { tr } = useI18n();
  const text = reserveSummaryText(stats, tr);
  if (!text) return null;
  return (
    <div className="small muted" data-testid="reserve-summary" style={{ margin: '4px 0' }}>
      {text}
    </div>
  );
}

/** Slim reserve chip: one short line; click to expand the full summary. Turns amber when short. */
export function ReserveChip({ stats }: { stats?: ReserveStats | null }) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const full = reserveSummaryText(stats, tr);
  if (!stats || !full) return null;
  const short = reserveShortText(stats, tr);
  const n = (v?: number) => (v === undefined || v < 0 ? '—' : String(v));
  const usable = stats.count_cooled === false ? stats.usable : stats.enabled;
  const chip = tr(
    `储备 ${stats.reserved} · 可提升 ${stats.eligible >= 0 ? stats.eligible : '—'} · 可用 ${n(usable)}/${stats.min_active > 0 ? stats.min_active : '—'}`,
    `Reserve ${stats.reserved} · ready ${stats.eligible >= 0 ? stats.eligible : '—'} · usable ${n(usable)}/${stats.min_active > 0 ? stats.min_active : '—'}`,
  );
  return (
    <div className="reserve-chip-row small">
      <button type="button" className={`reserve-chip${short ? ' warn' : ''}`} onClick={() => setOpen(!open)} title={[full, short].filter(Boolean).join('\n')} data-testid="reserve-summary" aria-expanded={open}>
        {short && <span data-testid="reserve-short">⚠ {tr('储备不足', 'Reserve short')} · </span>}
        {chip} <span className="faint">{open ? '▴' : '▾'}</span>
      </button>
      {open && <span className="muted reserve-chip-detail">{full}{short ? ` — ${short}` : ''}</span>}
    </div>
  );
}


/** Human-readable label for a reserve audit event kind. */
export function reserveEventKindLabel(kind: string, tr: (zh: string, en: string) => string): string {
  switch (kind) {
    case 'reserve':
      return tr('设为储备', 'Reserve');
    case 'unreserve':
      return tr('取消储备', 'Unreserve');
    case 'promote':
      return tr('自动启用', 'Promote');
    case 'ban':
      return tr('封禁', 'Ban');
    case 'clear-on-disable':
      return tr('停用时清除储备', 'Cleared on disable');
    default:
      return kind;
  }
}

/** Compact recent reserve audit events (newest first). */
export function ReserveEventsList({ events }: { events?: ReserveEvent[] | null }) {
  const { tr } = useI18n();
  if (!events?.length) return null;
  return (
    <div className="small muted" data-testid="reserve-events" style={{ margin: '4px 0' }}>
      {events.slice(0, 8).map((e, i) => (
        <div key={`${e.auth_id}-${e.at}-${i}`}>
          {reserveEventKindLabel(e.kind, tr)}
          {e.seq ? ` #${e.seq}` : ''} · {e.auth_id.slice(0, 8)}
          {e.detail ? ` · ${e.detail}` : ''}
        </div>
      ))}
    </div>
  );
}
