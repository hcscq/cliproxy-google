import { useApi, useAsync } from '../app-context';
import type { EgressStatus } from '../api/client';
import { useI18n } from '../i18n';

/** "egress-u04gzhfs" → "u04gzhfs" */
export const egressNode = (e: Pick<EgressStatus, 'name'>) => e.name.replace(/^egress-/, '');

export function egressLabel(e: EgressStatus, tr: (zh: string, en: string) => string) {
  return `${e.ip} · ${egressNode(e)} · ${e.healthy ? tr('正常', 'up') : tr('离线', 'down')} · ${e.credentials}`;
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
      {items.map((e) => (
        <option key={e.name} value={e.name}>
          {egressLabel(e, tr)}
        </option>
      ))}
      {allowCustom && <option value="custom">{tr('自定义 URL', 'Custom URL')}</option>}
    </select>
  );
}
