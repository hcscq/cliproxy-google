import { useApi, useAsync } from '../app-context';
import type { EgressStatus } from '../api/client';
import { useI18n } from '../i18n';

/** "u04gzhfs/1.2.3.4" or legacy "egress-u04gzhfs" → "u04gzhfs" */
export const egressNode = (e: Pick<EgressStatus, 'name' | 'node'>) =>
  (e.node ? e.node.replace(/^rainyun-/, '') : e.name.split('/')[0]).replace(/^egress-/, '');

/** Exit IP shown to users: the probed public IP when known. */
export const egressIP = (e: Pick<EgressStatus, 'ip' | 'public_ip'>) => e.public_ip || e.ip;

export function egressState(e: EgressStatus, tr: (zh: string, en: string) => string) {
  if (e.removed) return tr('IP 已移除', 'IP removed');
  if (!e.healthy) return tr('离线', 'down');
  if (e.public_changed) return tr('⚠ IP 变更', '⚠ IP changed');
  return tr('正常', 'up');
}

/** 主 = node primary IP (default route source), 副 = secondary. */
export const egressRole = (e: Pick<EgressStatus, 'primary'>) => (e.primary ? '主' : '副');

export function egressLabel(e: EgressStatus, tr: (zh: string, en: string) => string) {
  return `${egressIP(e)} ${egressRole(e)} · ${egressNode(e)} · ${egressState(e, tr)} · ${e.credentials}`;
}

/** Global default for "允许分配到主 IP" (Settings). */
export function useEgressSettings() {
  const api = useApi();
  return useAsync(() => api.egressSettings(), [], 0);
}

/** "允许分配到主 IP" checkbox; shown for auto-balance only. Default follows the global setting. */
export function AllowPrimaryToggle({
  value,
  onChange,
  testId = 'allow-primary',
}: {
  value: boolean | undefined;
  onChange: (v: boolean) => void;
  testId?: string;
}) {
  const { tr } = useI18n();
  const settings = useEgressSettings();
  const checked = value ?? settings.data?.allow_primary ?? false;
  return (
    <label
      className="small"
      style={{ display: 'inline-flex', gap: 6, alignItems: 'center', marginTop: 6 }}
      title={tr('关闭时只分配到副 IP；没有健康的副 IP 时直接失败，不会改用主 IP', 'Off: secondaries only; fails if none is healthy instead of using a primary IP')}
    >
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} data-testid={testId} />
      {tr('允许分配到主 IP', 'Allow primary IP')}
    </label>
  );
}

export function useEgressList() {
  const api = useApi();
  return useAsync(() => api.egressStatus().then((r) => r.items ?? []), [], 30000);
}

/** Egress choice: "auto" (least-loaded healthy), an egress name, or "custom" when allowCustom. */
export function EgressSelect({
  value,
  onChange,
  allowCustom,
  testId = 'egress-select',
}: {
  value: string;
  onChange: (v: string) => void;
  allowCustom?: boolean;
  testId?: string;
}) {
  const { tr } = useI18n();
  const list = useEgressList();
  const items = list.data ?? [];
  return (
    <select className="select" value={value} onChange={(e) => onChange(e.target.value)} data-testid={testId}>
      <option value="auto">{tr('自动均衡', 'Auto-balance')}</option>
      {items.filter((e) => !e.removed || e.name === value).map((e) => (
        <option key={e.name} value={e.name} disabled={e.removed}>
          {egressLabel(e, tr)}
        </option>
      ))}
      {allowCustom && <option value="custom">{tr('自定义 URL', 'Custom URL')}</option>}
    </select>
  );
}
