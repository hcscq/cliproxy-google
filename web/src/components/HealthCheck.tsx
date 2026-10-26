import { useState } from 'react';
import type { HealthInfo, HealthSummary, ImportVerdict } from '../api/client';
import { useApi, useUi } from '../app-context';
import { useI18n } from '../i18n';
import { Badge } from './ui';

/** Short Chinese/English label per health check category. */
export const HEALTH_CATEGORY: Record<string, [string, string]> = {
  needs_verify: ['需验证', 'Needs verify'],
  tier: ['订阅不符', 'Wrong tier'],
  quota_used: ['额度非新', 'Quota used'],
  quota_unknown: ['额度未知', 'Quota unknown'],
  no_catalog: ['无模型目录', 'No catalog'],
  tos: ['403 停用', '403 TOS'],
  permission: ['403 无权限', '403 denied'],
  rate_limited: ['429', '429'],
  auth: ['令牌无效', 'Bad token'],
  no_egress: ['无出口', 'No egress'],
  error: ['检查失败', 'Check error'],
  region: ['地区限制', 'Region restricted'],
  region_warn: ['地区提示', 'Region notice'],
};

export function healthCategoryLabel(c: string, tr: (zh: string, en: string) => string, tier?: string): string {
  if (c === 'tier' && tier) return tr(`非预期订阅（${tier}）`, `Tier ${tier}`);
  const l = HEALTH_CATEGORY[c];
  return l ? tr(l[0], l[1]) : c;
}

/** Compact badge text for a health result (first category + count). */
export function healthBadgeText(h: HealthInfo | undefined, tr: (zh: string, en: string) => string): string {
  if (!h) return '';
  if (h.status === 'pass') return h.categories?.includes('region_warn') ? tr('通过·地区提示', 'Passed · region notice') : tr('体检通过', 'Check passed');
  if (h.status === 'running') return tr('体检中', 'Checking');
  const cats = h.categories ?? [];
  if (!cats.length) return tr('体检失败', 'Check failed');
  const first = cats[0] === 'tier' ? tr('非Pro', 'Not Pro') : healthCategoryLabel(cats[0], tr);
  return cats.length > 1 ? `${first} +${cats.length - 1}` : first;
}

/** Verification link from upstream, only when it points at Google's account pages. */
export function safeVerifyURL(h: HealthInfo | undefined): string | null {
  const u = h?.verify_url?.trim();
  if (!u || !h?.categories?.includes('needs_verify')) return null;
  try {
    const p = new URL(u);
    if (p.protocol === 'https:' && (p.hostname === 'accounts.google.com' || p.hostname.endsWith('.google.com'))) return u;
  } catch {
    /* ignore */
  }
  return null;
}

export function HealthBadge({ h, id, onRechecked }: { h?: HealthInfo; id?: string; onRechecked?: () => void }) {
  const { tr } = useI18n();
  const { toast } = useUi();
  const api = useApi();
  const [busy, setBusy] = useState(false);
  const verify = safeVerifyURL(h);
  const recheck = id
    ? async (e: React.MouseEvent) => {
        e.stopPropagation();
        setBusy(true);
        try {
          await api.startHealthCheck([id]);
          toast(tr('已开始重新体检，约 30 秒后刷新查看', 'Re-check started; refresh in ~30s'));
          onRechecked?.();
        } catch (err) {
          toast(String((err as Error)?.message ?? err), 'err');
        } finally {
          setBusy(false);
        }
      }
    : undefined;
  if (!h && !recheck) return null;
  const kind = !h ? undefined : h.status === 'pass' ? (h.categories?.includes('region_warn') ? 'warn' : 'ok') : h.status === 'running' ? undefined : 'err';
  const title = h
    ? [
        `${tr('体检', 'Check')} ${h.checked_at ? new Date(h.checked_at).toLocaleString() : ''}${h.tier ? ` · ${h.tier}` : ''}`,
        ...(h.reasons ?? []),
        h.evidence ? `${tr('上游原文', 'Upstream')}: ${h.evidence}` : '',
        h.import ? importVerdictText(h.import, tr) : '',
      ]
        .filter(Boolean)
        .join('\n')
    : tr('尚未体检', 'Not checked yet');
  return (
    <span title={title} data-testid="health-badge" style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
      <Badge kind={kind} dot>
        {h ? healthBadgeText(h, tr) : tr('未体检', 'Unchecked')}
      </Badge>
      {verify && (
        <a href={verify} target="_blank" rel="noopener noreferrer" className="small" data-testid="health-verify-link" onClick={(e) => e.stopPropagation()}
          title={tr('在浏览器中用该账号登录并完成验证，然后点「重新体检」', 'Sign in with this account in a browser to verify, then click Re-check')}>
          {tr('去验证', 'Verify')}
        </a>
      )}
      {h?.import?.sticky && h.import.status === 'fail' && <StickyImportFlag v={h.import} id={id} onCleared={onRechecked} />}
      {recheck && h?.status !== 'running' && (
        <button className="btn btn-sm btn-link" disabled={busy} onClick={recheck} data-testid="health-recheck">
          {tr('重新体检', 'Re-check')}
        </button>
      )}
    </span>
  );
}

/** One-line batch summary: 通过 N · 失败 M（需验证 2，额度非新 1）. */
export function healthSummaryText(s: HealthSummary | undefined, tr: (zh: string, en: string) => string): string {
  if (!s) return '';
  const parts = Object.entries(s.by_category ?? {})
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${healthCategoryLabel(k, tr)} ${n}`);
  return tr(
    `体检：通过 ${s.pass} · 失败 ${s.fail}${parts.length ? `（${parts.join('，')}）` : ''}${s.running ? ` · 进行中 ${s.running}` : ''}`,
    `Check: pass ${s.pass} · fail ${s.fail}${parts.length ? ` (${parts.join(', ')})` : ''}${s.running ? ` · running ${s.running}` : ''}`,
  );
}

/** Import-time snapshot (never overwritten by later re-checks). */
export function importVerdictText(v: ImportVerdict, tr: (zh: string, en: string) => string): string {
  const cats = v.status === 'pass' ? tr('通过', 'pass') : v.categories.map((c) => healthCategoryLabel(c, tr, v.tier)).join(' / ');
  const argon = v.argon === undefined ? '' : v.argon ? ' · argon ✓' : tr(' · 无 argon', ' · no argon');
  return [`${tr('导入体检', 'Import check')} ${new Date(v.checked_at).toLocaleString()}${v.tier ? ` · ${v.tier}` : ''}${argon}: ${cats}`, ...(v.reasons ?? []), v.note ?? '', v.cleared_at ? tr(`标记已清除 ${new Date(v.cleared_at).toLocaleString()}`, `flag cleared ${new Date(v.cleared_at).toLocaleString()}`) : '']
    .filter(Boolean)
    .join('\n');
}

function StickyImportFlag({ v, id, onCleared }: { v: ImportVerdict; id?: string; onCleared?: () => void }) {
  const { tr } = useI18n();
  const { toast } = useUi();
  const api = useApi();
  const [gone, setGone] = useState(false);
  if (gone) return null;
  return (
    <span className="badge warn small" title={importVerdictText(v, tr)} data-testid="import-flag">
      {tr('导入', 'Import')}: {v.categories.map((c) => healthCategoryLabel(c, tr, v.tier)).join('/')}
      {id && (
        <button
          className="btn btn-sm btn-link"
          style={{ padding: '0 2px' }}
          title={tr('清除此标记（保留导入快照）', 'Clear flag (snapshot kept)')}
          onClick={async (e) => {
            e.stopPropagation();
            try {
              await api.clearImportVerdict([id]);
              setGone(true);
              onCleared?.();
            } catch (err) {
              toast(String((err as Error)?.message ?? err), 'err');
            }
          }}
          data-testid="import-flag-clear"
        >
          ✕
        </button>
      )}
    </span>
  );
}
