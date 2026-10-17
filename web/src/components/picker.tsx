import { useEffect, useMemo, useState, type ReactNode } from 'react';
import type { CredentialQuery, CredentialSearch, CredentialState, GroupsSummary } from '../api/client';
import { useI18n } from '../i18n';
import { Badge, Spinner } from './ui';

/** Debounces a value (search boxes hit the server). */
export function useDebounced<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setV(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return v;
}

/** Multi-selection keyed by id, independent of the current page. */
export function useSelection<K extends string | number>() {
  const [sel, setSel] = useState<Set<K>>(() => new Set());
  return useMemo(
    () => ({
      set: sel,
      size: sel.size,
      has: (k: K) => sel.has(k),
      toggle: (k: K, on?: boolean) =>
        setSel((prev) => {
          const n = new Set(prev);
          if (on ?? !n.has(k)) n.add(k);
          else n.delete(k);
          return n;
        }),
      addMany: (ks: K[]) => setSel((prev) => new Set([...prev, ...ks])),
      removeMany: (ks: K[]) =>
        setSel((prev) => {
          const n = new Set(prev);
          ks.forEach((k) => n.delete(k));
          return n;
        }),
      replace: (ks: K[]) => setSel(new Set(ks)),
      clear: () => setSel(new Set()),
      list: () => [...sel],
    }),
    [sel],
  );
}
export type Selection<K extends string | number> = ReturnType<typeof useSelection<K>>;

export const STATE_LABEL: Record<CredentialState | 'missing', [string, string]> = {
  ok: ['可用', 'Usable'],
  cooling: ['部分模型冷却', 'Some models cooling'],
  error: ['整体不可用', 'Unavailable'],
  disabled: ['已禁用', 'Disabled'],
  reserve: ['储备', 'Reserve'],
  missing: ['凭据不存在', 'Missing'],
};

export function StateBadge({ state }: { state?: CredentialState | 'missing' }) {
  const { tr } = useI18n();
  if (!state) return null;
  const kind = state === 'ok' ? 'ok' : state === 'cooling' ? 'warn' : state === 'disabled' || state === 'reserve' ? undefined : 'err';
  const [zh, en] = STATE_LABEL[state];
  return (
    <Badge kind={kind} dot>
      {tr(zh, en)}
    </Badge>
  );
}

function facetOptions(f?: Record<string, number>) {
  return Object.entries(f ?? {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** Search + provider / status / domain / tag / group filters (server-side). */
export function CredentialFilterBar({
  value,
  onChange,
  facets,
  groups,
  hideGroup,
}: {
  value: CredentialQuery;
  onChange: (q: CredentialQuery) => void;
  facets?: CredentialSearch['facets'];
  groups?: GroupsSummary['credential_groups'];
  hideGroup?: boolean;
}) {
  const { tr } = useI18n();
  const [text, setText] = useState(value.q ?? '');
  const debounced = useDebounced(text);
  useEffect(() => {
    if ((value.q ?? '') !== debounced) onChange({ ...value, q: debounced || undefined });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);
  const set = (k: keyof CredentialQuery, v: string) => onChange({ ...value, [k]: v || undefined });
  const active = ['provider', 'status', 'domain', 'tag', 'group'].filter((k) => value[k as keyof CredentialQuery]).length + (value.q ? 1 : 0);
  return (
    <div className="filter-bar" data-testid="cred-filter-bar">
      <input className="input input-sm" style={{ width: 200 }} placeholder={tr('搜索邮箱 / 名称 / ID / 备注', 'Search email / name / id / note')} value={text} onChange={(e) => setText(e.target.value)} data-testid="cred-filter-q" />
      <select className="select input-sm" value={value.provider ?? ''} onChange={(e) => set('provider', e.target.value)} data-testid="cred-filter-provider">
        <option value="">{tr('全部提供方', 'All providers')}</option>
        {facetOptions(facets?.provider).map(([k, n]) => (
          <option key={k} value={k}>
            {k} ({n})
          </option>
        ))}
      </select>
      <select className="select input-sm" value={value.status ?? ''} onChange={(e) => set('status', e.target.value)} data-testid="cred-filter-status">
        <option value="">{tr('全部状态', 'All states')}</option>
        {(['ok', 'cooling', 'error', 'disabled', 'reserve'] as const).map((s) => (
          <option key={s} value={s}>
            {tr(STATE_LABEL[s][0], STATE_LABEL[s][1])} ({facets?.status?.[s] ?? 0})
          </option>
        ))}
      </select>
      <select className="select input-sm" value={value.domain ?? ''} onChange={(e) => set('domain', e.target.value)} data-testid="cred-filter-domain">
        <option value="">{tr('全部邮箱域', 'All domains')}</option>
        {facetOptions(facets?.domain).map(([k, n]) => (
          <option key={k} value={k}>
            @{k} ({n})
          </option>
        ))}
      </select>
      <select className="select input-sm" value={value.tag ?? ''} onChange={(e) => set('tag', e.target.value)} data-testid="cred-filter-tag">
        <option value="">{tr('全部标签', 'All tags')}</option>
        {facetOptions(facets?.tag).map(([k, n]) => (
          <option key={k} value={k}>
            #{k} ({n})
          </option>
        ))}
      </select>
      {!hideGroup && (
        <select className="select input-sm" value={value.group ?? ''} onChange={(e) => set('group', e.target.value)} data-testid="cred-filter-group">
          <option value="">{tr('全部分组', 'Any group')}</option>
          <option value="none">{tr('未加入任何分组', 'In no group')}</option>
          {(groups ?? []).map((g) => (
            <option key={g.id} value={String(g.id)}>
              {g.channel_name} ({g.total})
            </option>
          ))}
        </select>
      )}
      {active > 0 && (
        <button
          className="btn btn-sm btn-ghost"
          onClick={() => {
            setText('');
            onChange({ exclude_group: value.exclude_group });
          }}
        >
          {tr('清除筛选', 'Clear')}
        </button>
      )}
    </div>
  );
}

/** Header row for selection: page checkbox state + "select all N filtered". */
export function SelectionBar({
  selected,
  total,
  pageIds,
  sel,
  loadAllIds,
  children,
}: {
  selected: number;
  total: number;
  pageIds: (string | number)[];
  sel: Selection<string> | Selection<number>;
  loadAllIds: () => Promise<(string | number)[]>;
  children?: ReactNode;
}) {
  const { tr } = useI18n();
  const [loading, setLoading] = useState(false);
  const s = sel as unknown as Selection<string | number>;
  const pageAll = pageIds.length > 0 && pageIds.every((id) => s.has(id));
  return (
    <div className={`selection-bar ${selected ? 'active' : ''}`} data-testid="selection-bar">
      <label className="check">
        <input type="checkbox" checked={pageAll} onChange={(e) => (e.target.checked ? s.addMany(pageIds) : s.removeMany(pageIds))} data-testid="select-page" />
        <span>{tr('本页', 'This page')}</span>
      </label>
      <button
        className="btn btn-sm btn-ghost"
        disabled={loading || total === 0}
        onClick={async () => {
          setLoading(true);
          try {
            s.addMany(await loadAllIds());
          } finally {
            setLoading(false);
          }
        }}
        data-testid="select-all-filtered"
      >
        {loading && <Spinner />}
        {tr(`选择全部筛选结果（${total}）`, `Select all ${total} filtered`)}
      </button>
      <span className="faint small">{selected ? tr(`已选 ${selected} 项`, `${selected} selected`) : tr('未选择', 'Nothing selected')}</span>
      {selected > 0 && (
        <button className="btn btn-sm btn-ghost" onClick={() => s.clear()} data-testid="clear-selection">
          {tr('清空选择', 'Clear selection')}
        </button>
      )}
      <span className="spacer" />
      {children}
    </div>
  );
}

/** Page-number pager with page-size picker. */
export function PageNav({ page, pageSize, total, onPage, onPageSize }: { page: number; pageSize: number; total: number; onPage: (p: number) => void; onPageSize?: (n: number) => void }) {
  const { tr } = useI18n();
  const pages = Math.max(1, Math.ceil(total / pageSize));
  return (
    <div className="pager" data-testid="page-nav">
      <span className="faint small">{tr(`共 ${total} 条 · 第 ${Math.min(page, pages)}/${pages} 页`, `${total} total · page ${Math.min(page, pages)}/${pages}`)}</span>
      {onPageSize && (
        <select className="select input-sm" style={{ width: 90 }} value={pageSize} onChange={(e) => onPageSize(Number(e.target.value))}>
          {[25, 50, 100, 200].map((n) => (
            <option key={n} value={n}>
              {tr(`${n} 条/页`, `${n}/page`)}
            </option>
          ))}
        </select>
      )}
      <button className="btn btn-sm" disabled={page <= 1} onClick={() => onPage(1)}>
        «
      </button>
      <button className="btn btn-sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
        {tr('上一页', 'Prev')}
      </button>
      <button className="btn btn-sm" disabled={page >= pages} onClick={() => onPage(page + 1)} data-testid="page-next">
        {tr('下一页', 'Next')}
      </button>
      <button className="btn btn-sm" disabled={page >= pages} onClick={() => onPage(pages)}>
        »
      </button>
    </div>
  );
}
