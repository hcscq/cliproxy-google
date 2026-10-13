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

export function egressLabel(e: EgressStatus, tr: (zh: string, en: string) => string) {
  return `${egressIP(e)} · ${egressNode(e)} · ${egressState(e, tr)} · ${e.credentials}`;
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
