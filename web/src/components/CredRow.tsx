import { useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { coolingModels, type Credential, type HealthInfo } from '../api/client';
import { Badge } from './ui';
import { healthBadgeText, healthCategoryLabel, safeVerifyURL } from './HealthCheck';

type Tr = (zh: string, en: string) => string;
export type PillKind = 'ok' | 'warn' | 'err' | 'primary' | undefined;
type C = Credential & { reserved?: boolean; reserved_since?: string; health_check?: HealthInfo };

function fmtLeft(iso?: string): string {
  if (!iso) return '';
  const s = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 1000));
  if (s >= 3600) return `${Math.floor(s / 3600)}h${Math.round((s % 3600) / 60)}m`;
  if (s >= 60) return `${Math.floor(s / 60)}m`;
  return `${s}s`;
}

/**
 * One status for the list: enabled state, reserve and health check merged into
 * a single short label; everything else goes into the tooltip lines.
 * Order: hard-off reasons > failed check > reserve > cooling > checking > ok.
 */
export function mergedStatus(cr: Credential, tr: Tr, extra: string[] = []): { kind: PillKind; label: string; lines: string[] } {
  const c = cr as C;
  const h = c.health_check;
  const lines: string[] = [];
  const msg = (c.status_message ?? '').trim();
  const unauthorized = msg.toLowerCase() === 'unauthorized';
  // enablement
  if (c.disabled) {
    const why = c.auto_disabled_reason
      ? c.auto_disabled_reason === 'account_verification_required' ? tr('需验证（403）', 'verification required (403)') : tr('封禁（403）', 'suspended (403)')
      : c.auto_paused_reason ? (c.auto_paused_reason === 'egress_removed' ? tr('出口 IP 已移除', 'egress IP removed') : tr('出口离线', 'egress down'))
      : unauthorized ? tr('刷新令牌失效（401），需重新授权；启用无效', 'refresh token rejected (401); re-authorise')
      : tr('手动禁用', 'disabled manually');
    lines.push(`${tr('已停用', 'Disabled')}: ${why}`);
    if (msg && !unauthorized) lines.push(msg.replace(/\s+/g, ' ').slice(0, 160));
  } else lines.push(tr('已启用', 'Enabled'));
  if (c.reserved) lines.push(tr(`储备中：不参与调度，仍刷新令牌/额度${c.reserved_since ? `；自 ${new Date(c.reserved_since).toLocaleString()}` : ''}`, `In reserve: not scheduled, still refreshed${c.reserved_since ? `; since ${new Date(c.reserved_since).toLocaleString()}` : ''}`));
  // health check
  if (!h) lines.push(tr('体检：尚未体检', 'Check: not checked yet'));
  else {
    lines.push(`${tr('体检', 'Check')}: ${healthBadgeText(h, tr)}${h.checked_at ? ` · ${new Date(h.checked_at).toLocaleString()}` : ''}${h.tier ? ` · ${h.tier}` : ''}`);
    if (h.status === 'fail' && (h.categories?.length ?? 0) > 1) lines.push('  ' + h.categories!.map((x) => healthCategoryLabel(x, tr)).join(', '));
    for (const r of h.reasons ?? []) lines.push('  ' + r);
    if (h.evidence) lines.push(`  ${tr('上游原文', 'Upstream')}: ${h.evidence}`);
    if (safeVerifyURL(h)) lines.push(tr('  ⋯ 菜单 →「去验证」完成后「重新体检」', '  ⋯ menu → Verify, then Re-check'));
  }
  const degraded = (Array.isArray(c.health) ? c.health : []).filter((x) => x && x.state);
  for (const x of degraded) lines.push(`${x.label} ${x.state} · TTFB ${(x.ttfb_ms / 1000).toFixed(1)}s · fail ${(x.fail_ratio * 100).toFixed(0)}%`);
  lines.push(...extra);

  const cooling = coolingModels(c);
  const bucket = cooling.some((m) => m.bucket_exhausted || m.error_code === 'quota_bucket_exhausted');
  let kind: PillKind;
  let label: string;
  if (c.disabled && c.auto_disabled_reason) [kind, label] = ['err', c.auto_disabled_reason === 'account_verification_required' ? tr('403 需验证', '403 verify') : tr('403 封禁', '403 banned')];
  else if (c.disabled && c.auto_paused_reason) [kind, label] = ['err', tr('出口暂停', 'Paused')];
  else if (c.disabled && unauthorized) [kind, label] = ['err', tr('401 失效', '401')];
  else if (c.disabled) [kind, label] = [undefined, tr('已禁用', 'Off')];
  else if (h?.status === 'fail') [kind, label] = ['err', healthBadgeText(h, tr)];
  else if (c.reserved) [kind, label] = ['primary', tr('储备', 'Reserve')];
  else if (bucket) [kind, label] = ['warn', tr('额度耗尽', 'Quota 0%')];
  else if (c.unavailable || c.credential_scope_error) [kind, label] = ['warn', c.next_retry_after ? tr(`冷却 ${fmtLeft(c.next_retry_after)}`, `Cool ${fmtLeft(c.next_retry_after)}`) : tr('不可用', 'Unavail.')];
  else if (cooling.length) [kind, label] = ['warn', tr(`${cooling.length}模型冷却`, `${cooling.length} cooling`)];
  else if (h?.status === 'running') [kind, label] = [undefined, tr('体检中', 'Checking')];
  else if (c.status && c.status !== 'active' && c.status !== 'error') [kind, label] = ['err', c.status];
  else if (degraded.some((x) => x.state === '降级')) [kind, label] = ['warn', tr('可用·降级', 'OK · degraded')];
  else if (h?.categories?.includes('region_warn')) [kind, label] = ['warn', tr('可用·地区', 'OK · region')];
  else if (!h) [kind, label] = ['ok', tr('可用·未检', 'OK · unchecked')];
  else [kind, label] = ['ok', tr('可用', 'OK')];
  return { kind, label, lines };
}

export function StatusPill({ c, tr, extra }: { c: Credential; tr: Tr; extra?: string[] }) {
  const s = mergedStatus(c, tr, extra);
  return (
    <span className="cell-status" title={s.lines.join('\n')} data-testid="cred-status">
      <Badge kind={s.kind} dot>{s.label}</Badge>
    </span>
  );
}

const PROVIDER_COLOR: Record<string, string> = { antigravity: '#6366f1', 'gemini-cli': '#0ea5e9', gemini: '#2563eb', vertex: '#16a34a', codex: '#111827', claude: '#d97706' };
export function ProviderIcon({ provider, abbr }: { provider: string; abbr: string }) {
  return (
    <span className="prov-icon" title={provider} style={{ background: PROVIDER_COLOR[(provider || '').toLowerCase()] ?? '#64748b' }}>
      {abbr.slice(0, 2)}
    </span>
  );
}

export interface MenuItem { label: string; onClick?: () => void; href?: string; danger?: boolean; disabled?: boolean; testId?: string }

/** Compact ⋯ menu; the popover is portalled so the table's overflow never clips it. */
export function RowMenu({ items, label = '⋯' }: { items: MenuItem[]; label?: ReactNode }) {
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const place = () => {
    const r = btn.current?.getBoundingClientRect();
    if (!r || (r.width === 0 && r.height === 0)) return null;
    const h = items.length * 30 + 8;
    const top = r.bottom + h > window.innerHeight ? Math.max(4, r.top - h) : r.bottom + 2;
    return { top, right: Math.max(4, window.innerWidth - r.right) };
  };
  const isOpen = !!pos;
  useEffect(() => {
    if (!isOpen) return;
    const close = () => setPos(null);
    // Follow the button when the page or table scrolls (focus may scroll the table on open).
    const follow = () => setPos(place());
    window.addEventListener('scroll', follow, true);
    window.addEventListener('resize', close);
    document.addEventListener('click', close);
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') close(); };
    document.addEventListener('keydown', esc);
    return () => { window.removeEventListener('scroll', follow, true); window.removeEventListener('resize', close); document.removeEventListener('click', close); document.removeEventListener('keydown', esc); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen]);
  const open = (e: React.MouseEvent) => {
    e.stopPropagation();
    setPos(pos ? null : place());
  };
  return (
    <>
      <button ref={btn} className="btn btn-sm btn-ghost row-menu-btn" onClick={open} aria-haspopup="menu" data-testid="row-menu">{label}</button>
      {pos && createPortal(
        <div className="row-menu" role="menu" style={{ top: pos.top, right: pos.right }} onClick={(e) => e.stopPropagation()}>
          {items.map((it, i) => it.href ? (
            <a key={i} role="menuitem" className="row-menu-item" href={it.href} target="_blank" rel="noopener noreferrer" data-testid={it.testId} onClick={() => setPos(null)}>{it.label}</a>
          ) : (
            <button key={i} role="menuitem" className={`row-menu-item${it.danger ? ' danger' : ''}`} disabled={it.disabled} data-testid={it.testId}
              onClick={() => { setPos(null); it.onClick?.(); }}>{it.label}</button>
          ))}
        </div>,
        document.body,
      )}
    </>
  );
}

/** Column visibility, persisted per browser. */
export function useHiddenColumns(key: string, defaults: string[]) {
  const [hidden, setHidden] = useState<Set<string>>(() => {
    try { const v = localStorage.getItem(key); if (v) return new Set(JSON.parse(v) as string[]); } catch { /* ignore */ }
    return new Set(defaults);
  });
  const toggle = (k: string) => setHidden((prev) => {
    const n = new Set(prev);
    if (n.has(k)) n.delete(k); else n.add(k);
    localStorage.setItem(key, JSON.stringify([...n]));
    return n;
  });
  return { hidden, toggle };
}

export function ColumnToggle({ cols, hidden, onToggle, tr }: { cols: { key: string; label: string }[]; hidden: Set<string>; onToggle: (k: string) => void; tr: Tr }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);
  return (
    <div className="col-toggle" ref={ref}>
      <button className="btn btn-sm" onClick={() => setOpen(!open)} data-testid="col-toggle">{tr('列', 'Columns')} ▾</button>
      {open && (
        <div className="row-menu col-toggle-menu">
          {cols.map((c) => (
            <label key={c.key} className="row-menu-item">
              <input type="checkbox" checked={!hidden.has(c.key)} onChange={() => onToggle(c.key)} /> {c.label}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
