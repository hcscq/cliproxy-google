import { useState } from 'react';
import type { ReserveEvent, ReserveStats } from '../api/client';
import { useI18n } from '../i18n';

const EVENTS_OPEN_KEY = 'cred-reserve-events-open';
const EVENTS_SHOW = 10;

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

export function reserveEventKindTone(kind: string): 'ok' | 'warn' | 'err' | 'primary' | undefined {
  switch (kind) {
    case 'promote':
      return 'primary';
    case 'ban':
      return 'err';
    case 'unreserve':
    case 'clear-on-disable':
      return 'warn';
    case 'reserve':
      return 'ok';
    default:
      return undefined;
  }
}

/** Short display form of an event detail; full text stays in a title tooltip. */
export function shortReserveDetail(detail: string | undefined, tr: (zh: string, en: string) => string): string {
  if (!detail) return '';
  const d = detail.trim();
  if (/violation of Terms of Service|Terms of Service/i.test(d) || /服务条款|违反.*条款/.test(d)) {
    return tr('违反服务条款(403)', 'ToS violation (403)');
  }
  if (/RESOURCE_EXHAUSTED|quota.?exhausted|额度耗尽/i.test(d)) {
    return tr('额度耗尽', 'Quota exhausted');
  }
  if (/disabled|已停用|已禁用/i.test(d) && d.length > 40) {
    return tr('账号已停用', 'Account disabled');
  }
  const first = d.split(/[。.；;!\n]/)[0]?.trim() || d;
  if (first.length > 40) return `${first.slice(0, 40)}…`;
  return first;
}

function countEventsSince(events: ReserveEvent[] | null | undefined, kind: string, sinceMs: number): number {
  if (!events?.length) return 0;
  return events.reduce((n, e) => (e.kind === kind && new Date(e.at).getTime() >= sinceMs ? n + 1 : n), 0);
}

function fmtEventTime(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  const hh = String(d.getHours()).padStart(2, '0');
  const mi = String(d.getMinutes()).padStart(2, '0');
  return `${mm}-${dd} ${hh}:${mi}`;
}

function readOpen(): boolean {
  try {
    return localStorage.getItem(EVENTS_OPEN_KEY) === '1';
  } catch {
    return false;
  }
}

function writeOpen(v: boolean) {
  try {
    localStorage.setItem(EVENTS_OPEN_KEY, v ? '1' : '0');
  } catch {
    /* ignore */
  }
}

/**
 * Compact reserve banner: one summary line + optional expandable event table.
 * Collapsed by default; open state persists in localStorage.
 */
export function ReserveAlert({ stats, events }: { stats?: ReserveStats | null; events?: ReserveEvent[] | null }) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(readOpen);
  const full = reserveSummaryText(stats, tr);
  if (!stats || !full) return null;

  const short = reserveShortText(stats, tr);
  const n = (v?: number) => (v === undefined || v < 0 ? '—' : String(v));
  const usable = stats.count_cooled === false ? stats.usable : stats.enabled;
  const chip = tr(
    `储备 ${stats.reserved} · 可提升 ${stats.eligible >= 0 ? stats.eligible : '—'} · 可用 ${n(usable)}/${stats.min_active > 0 ? stats.min_active : '—'}`,
    `Reserve ${stats.reserved} · ready ${stats.eligible >= 0 ? stats.eligible : '—'} · usable ${n(usable)}/${stats.min_active > 0 ? stats.min_active : '—'}`,
  );

  const since = Date.now() - 24 * 3600 * 1000;
  const promotes = typeof stats.promotions_24h === 'number' ? stats.promotions_24h : countEventsSince(events, 'promote', since);
  const bans = countEventsSince(events, 'ban', since);
  const recentLine = tr(`近24h 自动启用 ${promotes} · 封禁 ${bans}`, `24h promote ${promotes} · ban ${bans}`);

  const list = events ?? [];
  const shown = open ? list.slice(0, EVENTS_SHOW) : [];
  const more = open && list.length > EVENTS_SHOW ? list.length - EVENTS_SHOW : 0;

  const toggle = () => {
    const next = !open;
    setOpen(next);
    writeOpen(next);
  };

  return (
    <div className={`reserve-alert${short ? ' warn' : ''}`} data-testid="reserve-alert">
      <div className="reserve-alert-head">
        <button
          type="button"
          className={`reserve-chip${short ? ' warn' : ''}`}
          onClick={toggle}
          title={[full, short].filter(Boolean).join('\n')}
          data-testid="reserve-summary"
          aria-expanded={open}
        >
          {short && <span data-testid="reserve-short">⚠ {tr('储备不足', 'Reserve short')} · </span>}
          {chip}
          <span className="faint reserve-alert-sep">·</span>
          <span className="reserve-alert-recent" data-testid="reserve-recent-counts">{recentLine}</span>
          <span className="faint">{open ? '▴' : '▾'}</span>
        </button>
        {list.length > 0 && (
          <button type="button" className="btn btn-xs btn-ghost" onClick={toggle} data-testid="reserve-events-toggle">
            {open ? tr('收起事件', 'Hide events') : tr(`事件 ${list.length}`, `Events ${list.length}`)}
          </button>
        )}
      </div>
      {open && (
        <div className="reserve-alert-detail small muted" data-testid="reserve-chip-detail">
          {full}
          {short ? ` — ${short}` : ''}
        </div>
      )}
      {open && shown.length > 0 && (
        <div className="reserve-events" data-testid="reserve-events">
          <table className="reserve-events-table">
            <thead>
              <tr>
                <th>{tr('时间', 'Time')}</th>
                <th>{tr('类型', 'Type')}</th>
                <th>{tr('凭据', 'Credential')}</th>
                <th>{tr('原因', 'Reason')}</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((e, i) => {
                const tone = reserveEventKindTone(e.kind);
                const reason = shortReserveDetail(e.detail, tr);
                return (
                  <tr key={`${e.auth_id}-${e.at}-${i}`}>
                    <td className="mono nowrap faint">{fmtEventTime(e.at)}</td>
                    <td>
                      <span className={`reserve-ev-badge${tone ? ` ${tone}` : ''}`}>{reserveEventKindLabel(e.kind, tr)}</span>
                    </td>
                    <td className="mono nowrap">
                      {e.seq ? `#${e.seq} · ` : ''}
                      {e.auth_id.slice(0, 8)}
                    </td>
                    <td className="ellipsis" title={e.detail || undefined}>
                      {reason || <span className="faint">—</span>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {more > 0 && (
            <div className="reserve-events-more faint" data-testid="reserve-events-more">
              {tr(`还有 ${more} 条（接口最多返回近期事件）`, `${more} more (API returns recent events only)`)}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** @deprecated Prefer ReserveAlert; kept for callers that only need a flat list. */
export function ReserveEventsList({ events }: { events?: ReserveEvent[] | null }) {
  const { tr } = useI18n();
  if (!events?.length) return null;
  return (
    <div className="small muted" data-testid="reserve-events" style={{ margin: '4px 0' }}>
      {events.slice(0, 8).map((e, i) => (
        <div key={`${e.auth_id}-${e.at}-${i}`}>
          {reserveEventKindLabel(e.kind, tr)}
          {e.seq ? ` #${e.seq}` : ''} · {e.auth_id.slice(0, 8)}
          {e.detail ? ` · ${shortReserveDetail(e.detail, tr)}` : ''}
        </div>
      ))}
    </div>
  );
}
