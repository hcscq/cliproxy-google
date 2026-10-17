import type { HealthInfo, HealthSummary } from '../api/client';
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
};

export function healthCategoryLabel(c: string, tr: (zh: string, en: string) => string, tier?: string): string {
  if (c === 'tier' && tier) return tr(`非预期订阅（${tier}）`, `Tier ${tier}`);
  const l = HEALTH_CATEGORY[c];
  return l ? tr(l[0], l[1]) : c;
}

/** Compact badge text for a health result (first category + count). */
export function healthBadgeText(h: HealthInfo | undefined, tr: (zh: string, en: string) => string): string {
  if (!h) return '';
  if (h.status === 'pass') return tr('体检通过', 'Check passed');
  if (h.status === 'running') return tr('体检中', 'Checking');
  const cats = h.categories ?? [];
  if (!cats.length) return tr('体检失败', 'Check failed');
  const first = cats[0] === 'tier' ? tr('非Pro', 'Not Pro') : healthCategoryLabel(cats[0], tr);
  return cats.length > 1 ? `${first} +${cats.length - 1}` : first;
}

export function HealthBadge({ h }: { h?: HealthInfo }) {
  const { tr } = useI18n();
  if (!h) return null;
  const kind = h.status === 'pass' ? 'ok' : h.status === 'running' ? undefined : 'err';
  const title = [
    `${tr('体检', 'Check')} ${h.checked_at ? new Date(h.checked_at).toLocaleString() : ''}${h.tier ? ` · ${h.tier}` : ''}`,
    ...(h.reasons ?? []),
    h.evidence ? `${tr('上游原文', 'Upstream')}: ${h.evidence}` : '',
  ]
    .filter(Boolean)
    .join('\n');
  return (
    <span title={title} data-testid="health-badge">
      <Badge kind={kind} dot>
        {healthBadgeText(h, tr)}
      </Badge>
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
