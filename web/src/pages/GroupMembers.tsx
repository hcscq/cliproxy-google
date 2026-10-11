import { useEffect, useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import {
  cleanRule, describeRule,
  type CredentialGroupSummary, type CredentialQuery, type GroupRule, type GroupsSummary, type ModelGroupMember, type ModelGroupSummary, type ModelHealthStatus,
} from '../api/client';
import { downloadBlob } from '../format';
import { useI18n } from '../i18n';
import { Badge, Drawer, Empty, ErrorBox, Loading, Modal, Spinner } from '../components/ui';
import { CredentialFilterBar, PageNav, SelectionBar, StateBadge, useDebounced, useSelection } from '../components/picker';
import { IconDownload, IconPlus, IconTrash } from '../components/icons';

type Tr = (zh: string, en: string) => string;

export function availabilityLabel(g: Pick<CredentialGroupSummary, 'usable' | 'total'>, tr: Tr) {
  return g.total === 0 ? tr('无成员', 'no members') : tr(`当前可用 ${g.usable}/${g.total}`, `usable ${g.usable}/${g.total}`);
}

export function availabilityTitle(g: CredentialGroupSummary, tr: Tr) {
  return tr(
    `可用 ${g.ok} · 部分模型冷却 ${g.cooling} · 整体不可用 ${g.error} · 已禁用 ${g.disabled}${g.missing ? ` · 已不存在 ${g.missing}` : ''}`,
    `ok ${g.ok} · cooling ${g.cooling} · unavailable ${g.error} · disabled ${g.disabled}${g.missing ? ` · missing ${g.missing}` : ''}`,
  );
}

// ------------------------------------------------------------ credential members

export function CredentialMembersDrawer({ group, summary, onClose, onChanged }: { group: CredentialGroupSummary; summary: GroupsSummary; onClose: () => void; onChanged: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [q, setQ] = useState('');
  const dq = useDebounced(q);
  const [source, setSource] = useState('');
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);
  const [picking, setPicking] = useState(false);
  const [ruling, setRuling] = useState(false);
  const sel = useSelection<string>();
  useEffect(() => setPage(1), [dq, source]);
  const res = useAsync(() => api.credentialGroupMembersPaged(group.id, { q: dq || undefined, source: source || undefined, page, page_size: size }), [group.id, dq, source, page, size]);
  const reload = async () => {
    await res.reload();
    await onChanged();
  };
  const removeSelected = async () => {
    const ids = sel.list();
    const ok = await confirm({
      title: tr(`从「${group.channel_name}」移除 ${ids.length} 个凭据`, `Remove ${ids.length} credentials from "${group.channel_name}"`),
      body: tr('只移除手动添加的成员；规则匹配的成员需修改规则。凭据本身不受影响。', 'Only manually added members are removed; rule members follow the rule. Credentials are not affected.'),
      danger: true,
      confirmText: tr('移除', 'Remove'),
    });
    if (!ok) return;
    await run('rm', async () => {
      const r = await api.bulkCredentialGroupMembers(group.id, { remove: ids });
      toast(tr(`已移除 ${r.result.removed} 个`, `Removed ${r.result.removed}`));
      sel.clear();
      await reload();
    });
  };
  const items = res.data?.items ?? [];
  return (
    <Drawer title={tr(`成员 · ${group.channel_name}`, `Members · ${group.channel_name}`)} onClose={onClose}>
      <div className="stack">
        <div className="row" style={{ gap: 8 }}>
          <Badge kind={group.usable === 0 && group.total > 0 ? 'err' : 'ok'} dot>
            {availabilityLabel(group, tr)}
          </Badge>
          <span className="faint small" title={availabilityTitle(group, tr)}>
            {tr(`手动 ${res.data?.static_count ?? group.static_count} · 规则 ${res.data?.rule_count ?? group.rule_count}`, `manual ${res.data?.static_count ?? group.static_count} · rule ${res.data?.rule_count ?? group.rule_count}`)}
          </span>
          <span className="spacer" />
          <button className="btn btn-sm" onClick={() => setRuling(true)} data-testid="open-rule">
            {group.has_rule ? tr('编辑规则', 'Edit rule') : tr('规则（自动加入）', 'Rule (auto-join)')}
          </button>
          <button className="btn btn-sm btn-primary" onClick={() => setPicking(true)} data-testid="open-picker">
            <IconPlus />
            {tr('批量添加凭据', 'Add credentials')}
          </button>
        </div>
        <div className="filter-bar">
          <input className="input input-sm" style={{ width: 220 }} placeholder={tr('搜索成员', 'Search members')} value={q} onChange={(e) => setQ(e.target.value)} data-testid="member-q" />
          <select className="select input-sm" value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="">{tr('全部来源', 'All sources')}</option>
            <option value="static">{tr('手动添加', 'Manual')}</option>
            <option value="rule">{tr('规则匹配', 'Rule')}</option>
          </select>
        </div>
        <SelectionBar
          selected={sel.size}
          total={res.data?.total ?? 0}
          pageIds={items.filter((m) => m.source !== 'rule').map((m) => m.id)}
          sel={sel}
          loadAllIds={async () => (await api.credentialGroupMemberIds(group.id, { q: dq || undefined, source: source || undefined })).ids}
        >
          <button className="btn btn-sm btn-danger" disabled={!sel.size || !!busy} onClick={() => void removeSelected()} data-testid="remove-selected">
            {busy === 'rm' ? <Spinner /> : <IconTrash />}
            {tr(`移除所选（${sel.size}）`, `Remove (${sel.size})`)}
          </button>
        </SelectionBar>
        {res.error ? (
          <ErrorBox error={res.error} onRetry={res.reload} />
        ) : !res.data ? (
          <Loading />
        ) : items.length === 0 ? (
          <Empty>{tr('没有成员', 'No members')}</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table compact" data-testid="member-table">
              <thead>
                <tr>
                  <th style={{ width: 28 }} />
                  <th>{tr('凭据', 'Credential')}</th>
                  <th>{tr('状态', 'State')}</th>
                  <th>{tr('来源', 'Source')}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((m) => (
                  <tr key={m.id}>
                    <td>
                      <input type="checkbox" disabled={m.source === 'rule'} title={m.source === 'rule' ? tr('规则成员：修改规则以移除', 'Rule member: change the rule to remove') : undefined} checked={sel.has(m.id)} onChange={() => sel.toggle(m.id)} />
                    </td>
                    <td>
                      <div className="ellipsis" style={{ maxWidth: 340 }} title={m.email || m.label || m.id}>
                        {m.email || m.label || m.id}
                      </div>
                      <div className="faint small mono ellipsis" style={{ maxWidth: 340 }}>
                        {m.provider ? `${m.provider} · ` : ''}
                        {m.id}
                      </div>
                    </td>
                    <td>
                      <StateBadge state={m.state} />
                    </td>
                    <td className="small">{m.source === 'rule' ? <Badge kind="primary">{tr('规则', 'rule')}</Badge> : m.source === 'both' ? tr('手动 + 规则', 'manual + rule') : tr('手动', 'manual')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <PageNav page={page} pageSize={size} total={res.data?.total ?? 0} onPage={setPage} onPageSize={(n) => (setSize(n), setPage(1))} />
      </div>
      {picking && (
        <CredentialPickerModal
          group={group}
          summary={summary}
          onClose={() => setPicking(false)}
          onDone={async () => {
            setPicking(false);
            await reload();
          }}
        />
      )}
      {ruling && (
        <RuleModal
          group={group}
          onClose={() => setRuling(false)}
          onSaved={async () => {
            setRuling(false);
            await reload();
          }}
        />
      )}
    </Drawer>
  );
}

export function CredentialPickerModal({ group, summary, onClose, onDone }: { group: CredentialGroupSummary; summary: GroupsSummary; onClose: () => void; onDone: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [filter, setFilter] = useState<CredentialQuery>({ exclude_group: group.id });
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);
  const sel = useSelection<string>();
  useEffect(() => setPage(1), [filter]);
  const res = useAsync(() => api.searchCredentials({ ...filter, page, page_size: size }), [filter, page, size]);
  const items = res.data?.items ?? [];
  const names = new Map(summary.credential_groups.map((g) => [g.id, g.channel_name]));
  const add = () =>
    run('add', async () => {
      const r = await api.bulkCredentialGroupMembers(group.id, { add: sel.list() });
      toast(tr(`已添加 ${r.result.added} 个${r.result.unchanged ? `（${r.result.unchanged} 个已在组内）` : ''}`, `Added ${r.result.added}${r.result.unchanged ? ` (${r.result.unchanged} already in)` : ''}`));
      await onDone();
    });
  return (
    <Modal
      title={tr(`添加凭据到「${group.channel_name}」`, `Add credentials to "${group.channel_name}"`)}
      onClose={onClose}
      full
      footer={
        <>
          <span className="faint small spacer">{tr('列表已排除当前成员；“选择全部筛选结果”会跨页选择。', 'Current members are hidden; "select all filtered" spans pages.')}</span>
          <button className="btn" onClick={onClose}>
            {tr('取消', 'Cancel')}
          </button>
          <button className="btn btn-primary" disabled={!sel.size || !!busy} onClick={() => void add()} data-testid="picker-add">
            {busy && <Spinner />}
            {tr(`添加所选（${sel.size}）`, `Add selected (${sel.size})`)}
          </button>
        </>
      }
    >
      <div className="stack">
        <CredentialFilterBar value={filter} onChange={setFilter} facets={res.data?.facets} groups={summary.credential_groups} />
        <SelectionBar selected={sel.size} total={res.data?.total ?? 0} pageIds={items.map((c) => c.id)} sel={sel} loadAllIds={async () => (await api.credentialIds(filter)).ids} />
        {res.error ? (
          <ErrorBox error={res.error} onRetry={res.reload} />
        ) : !res.data ? (
          <Loading />
        ) : items.length === 0 ? (
          <Empty>{tr('没有匹配的凭据', 'No matching credentials')}</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table compact" data-testid="picker-table">
              <thead>
                <tr>
                  <th style={{ width: 28 }} />
                  <th>{tr('凭据', 'Credential')}</th>
                  <th>{tr('提供方', 'Provider')}</th>
                  <th>{tr('状态', 'State')}</th>
                  <th className="hide-sm">{tr('标签', 'Tags')}</th>
                  <th className="hide-sm">{tr('已在分组', 'Groups')}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((c) => (
                  <tr key={c.id} className="clickable" onClick={() => sel.toggle(c.id)}>
                    <td>
                      <input type="checkbox" checked={sel.has(c.id)} onChange={() => sel.toggle(c.id)} onClick={(e) => e.stopPropagation()} />
                    </td>
                    <td>
                      <div className="ellipsis" style={{ maxWidth: 300 }}>
                        {c.email || c.label || c.name}
                      </div>
                      <div className="faint small mono ellipsis" style={{ maxWidth: 300 }}>
                        {c.id}
                      </div>
                    </td>
                    <td>
                      <Badge kind="primary">{c.provider}</Badge>
                    </td>
                    <td>
                      <StateBadge state={c.state} />
                    </td>
                    <td className="hide-sm small">{(c.tags ?? []).map((t) => `#${t}`).join(' ') || <span className="faint">—</span>}</td>
                    <td className="hide-sm small ellipsis" style={{ maxWidth: 200 }}>
                      {(c.group_ids ?? []).map((id) => names.get(id) ?? `#${id}`).join('、') || <span className="faint">—</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <PageNav page={page} pageSize={size} total={res.data?.total ?? 0} onPage={setPage} onPageSize={(n) => (setSize(n), setPage(1))} />
      </div>
    </Modal>
  );
}

// ------------------------------------------------------------ rule editor

const splitList = (s: string) => s.split(/[,，\s]+/).map((x) => x.trim()).filter(Boolean);

export function RuleModal({ group, onClose, onSaved }: { group: CredentialGroupSummary; onClose: () => void; onSaved: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const current = useAsync(() => api.groupRule(group.id), [group.id]);
  const facets = useAsync(() => api.searchCredentials({ page: 1, page_size: 1 }), []);
  const [form, setForm] = useState({ providers: [] as string[], email: '', label: '', any: '', all: '', min: '', max: '', enabled: true });
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    if (loaded || !current.data) return;
    const r = current.data.rule ?? {};
    setForm({
      providers: r.providers ?? [],
      email: r.email_pattern ?? '',
      label: r.label_pattern ?? '',
      any: (r.tags_any ?? []).join(', '),
      all: (r.tags_all ?? []).join(', '),
      min: r.min_priority?.toString() ?? '',
      max: r.max_priority?.toString() ?? '',
      enabled: current.data.rule ? current.data.enabled : true,
    });
    setLoaded(true);
  }, [current.data, loaded]);
  const rule: GroupRule = useMemo(
    () =>
      cleanRule({
        providers: form.providers,
        email_pattern: form.email,
        label_pattern: form.label,
        tags_any: splitList(form.any),
        tags_all: splitList(form.all),
        min_priority: form.min === '' ? undefined : Number(form.min),
        max_priority: form.max === '' ? undefined : Number(form.max),
      }),
    [form],
  );
  const debouncedRule = useDebounced(rule, 350);
  const preview = useAsync(() => api.previewGroupRule(debouncedRule), [JSON.stringify(debouncedRule)]);
  const empty = Object.keys(rule).length === 0;
  const providers = Object.keys(facets.data?.facets.provider ?? {}).sort();
  const tags = Object.keys(facets.data?.facets.tag ?? {}).sort();
  return (
    <Modal
      title={tr(`规则 · ${group.channel_name}`, `Rule · ${group.channel_name}`)}
      onClose={onClose}
      wide
      footer={
        <>
          {current.data?.rule && (
            <button
              className="btn btn-danger"
              disabled={!!busy}
              onClick={async () => {
                if (!(await confirm({ title: tr('删除规则', 'Delete rule'), body: tr('规则匹配的成员将离开分组（手动添加的成员保留）。', 'Rule members leave the group (manual members stay).'), danger: true }))) return;
                await run('del', async () => {
                  await api.deleteGroupRule(group.id);
                  toast(tr('规则已删除', 'Rule deleted'));
                  await onSaved();
                });
              }}
            >
              {tr('删除规则', 'Delete rule')}
            </button>
          )}
          <span className="spacer" />
          <button className="btn" onClick={onClose}>
            {tr('取消', 'Cancel')}
          </button>
          <button
            className="btn btn-primary"
            disabled={empty || !!busy}
            data-testid="rule-save"
            onClick={() =>
              void run('save', async () => {
                const r = await api.saveGroupRule(group.id, rule, form.enabled);
                toast(tr(`规则已保存，当前匹配 ${r.matched} 个凭据`, `Saved; ${r.matched} credentials match`));
                await onSaved();
              })
            }
          >
            {busy === 'save' && <Spinner />}
            {tr('保存规则', 'Save rule')}
          </button>
        </>
      }
    >
      {!loaded ? (
        <Loading />
      ) : (
        <div className="stack">
          <p className="hint">
            {tr(
              '满足全部已填条件的凭据自动成为成员（包括以后新增的凭据，约 2 秒内生效）。规则成员与手动成员取并集；空规则不匹配任何凭据。通配符：* 任意字符、? 单个字符，例如 *@corp.com。',
              'Credentials matching every filled condition join automatically (including future ones, within ~2s). Rule and manual members are unioned; an empty rule matches nothing. Globs: * and ?, e.g. *@corp.com.',
            )}
          </p>
          <div className="field">
            <label>{tr('提供方（不选 = 任意）', 'Providers (none = any)')}</label>
            <div className="row" style={{ gap: 12 }}>
              {providers.map((p) => (
                <label key={p} className="check">
                  <input type="checkbox" checked={form.providers.includes(p)} onChange={(e) => setForm({ ...form, providers: e.target.checked ? [...form.providers, p] : form.providers.filter((x) => x !== p) })} />
                  <span>{p}</span>
                </label>
              ))}
            </div>
          </div>
          <div className="grid grid-2">
            <div className="field">
              <label>{tr('邮箱匹配', 'Email pattern')}</label>
              <input className="input mono" placeholder="*@corp.com" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} data-testid="rule-email" />
            </div>
            <div className="field">
              <label>{tr('名称匹配', 'Label pattern')}</label>
              <input className="input mono" placeholder="team-a-*" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} />
            </div>
            <div className="field">
              <label>{tr('含任一标签', 'Has any tag')}</label>
              <input className="input" list="rule-tags" placeholder="vip, prod" value={form.any} onChange={(e) => setForm({ ...form, any: e.target.value })} data-testid="rule-tags-any" />
            </div>
            <div className="field">
              <label>{tr('含全部标签', 'Has all tags')}</label>
              <input className="input" list="rule-tags" value={form.all} onChange={(e) => setForm({ ...form, all: e.target.value })} />
            </div>
            <div className="field">
              <label>{tr('优先级 ≥', 'Priority ≥')}</label>
              <input className="input" type="number" value={form.min} onChange={(e) => setForm({ ...form, min: e.target.value })} />
            </div>
            <div className="field">
              <label>{tr('优先级 ≤', 'Priority ≤')}</label>
              <input className="input" type="number" value={form.max} onChange={(e) => setForm({ ...form, max: e.target.value })} />
            </div>
          </div>
          <datalist id="rule-tags">
            {tags.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
          <label className="check">
            <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
            <span>{tr('启用规则', 'Rule enabled')}</span>
          </label>
          <div className={`alert small ${empty ? 'warn' : ''}`} data-testid="rule-preview">
            <strong>{tr('预览：', 'Preview: ')}</strong>
            {empty ? (
              tr('请至少填写一个条件。', 'Fill at least one condition.')
            ) : preview.loading && !preview.data ? (
              <Spinner />
            ) : (
              <>
                {describeRule(rule, tr)} → {tr(`匹配 ${preview.data?.matched ?? 0} 个凭据`, `${preview.data?.matched ?? 0} credentials match`)}
                {(preview.data?.sample.length ?? 0) > 0 && (
                  <div className="faint small ellipsis" style={{ marginTop: 4 }} title={preview.data!.sample.map((s) => s.label || s.id).join(', ')}>
                    {preview.data!.sample.slice(0, 8).map((s) => s.label || s.id).join('、')}
                    {(preview.data?.matched ?? 0) > 8 ? ' …' : ''}
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      )}
    </Modal>
  );
}

// ------------------------------------------------------------ model members

const HEALTH_LABEL: Record<ModelHealthStatus, [string, string]> = {
  available: ['可用', 'available'],
  partial: ['部分可用', 'partial'],
  cooling: ['冷却中', 'cooling'],
  rejected: ['上游拒绝', 'rejected'],
  retired: ['已下线', 'retired'],
  removed: ['上游已移除', 'removed'],
  unknown: ['未知', 'unknown'],
};

export function ModelMembersDrawer({ group, summary, onClose, onChanged }: { group: ModelGroupSummary; summary: GroupsSummary; onClose: () => void; onChanged: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const members = useAsync(() => api.modelGroupMembersOf(group.id).then((r) => r.model_group_details ?? []), [group.id]);
  const health = useAsync(() => api.modelHealth().catch(() => undefined), []);
  const [tab, setTab] = useState<'members' | 'add'>('members');
  const [q, setQ] = useState('');
  const [status, setStatus] = useState('');
  const [page, setPage] = useState(1);
  const [manual, setManual] = useState('');
  const [restrict, setRestrict] = useState<number[]>([]);
  const [editing, setEditing] = useState<ModelGroupMember | null>(null);
  const selMembers = useSelection<string>();
  const selAdd = useSelection<string>();
  const size = 50;
  useEffect(() => setPage(1), [q, status, tab]);
  const statusOf = new Map((health.data?.models ?? []).map((m) => [m.id.toLowerCase(), m.status]));
  const have = new Set((members.data ?? []).map((m) => m.model_id.toLowerCase()));
  const cgName = new Map(summary.credential_groups.map((g) => [g.id, g.channel_name]));
  const match = (id: string) => (!q || id.toLowerCase().includes(q.toLowerCase())) && (!status || (statusOf.get(id.toLowerCase()) ?? 'unknown') === status);
  const memberRows = (members.data ?? []).filter((m) => match(m.model_id));
  const candidates = (health.data?.models ?? []).map((m) => m.id).filter((id) => !have.has(id.toLowerCase()) && match(id));
  const rows = tab === 'members' ? memberRows.map((m) => m.model_id) : candidates;
  const pageRows = rows.slice((page - 1) * size, page * size);
  const sel = tab === 'members' ? selMembers : selAdd;
  const reload = async () => {
    await members.reload();
    await onChanged();
  };
  const statusCounts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const m of health.data?.models ?? []) c[m.status] = (c[m.status] ?? 0) + 1;
    return c;
  }, [health.data]);
  const addIds = async (ids: string[]) => {
    const r = await api.bulkModelGroupMembers(group.id, { add: ids, channels: restrict });
    toast(tr(`已添加 ${r.result.added} 个模型`, `Added ${r.result.added} models`));
    selAdd.clear();
    await reload();
  };
  return (
    <Drawer title={tr(`模型 · ${group.group_name}`, `Models · ${group.group_name}`)} onClose={onClose}>
      <div className="stack">
        <div className="tabs">
          <button className={tab === 'members' ? 'active' : ''} onClick={() => setTab('members')} data-testid="mg-tab-members">
            {tr(`当前模型（${members.data?.length ?? 0}）`, `Models (${members.data?.length ?? 0})`)}
          </button>
          <button className={tab === 'add' ? 'active' : ''} onClick={() => setTab('add')} data-testid="mg-tab-add">
            {tr('添加模型', 'Add models')}
          </button>
        </div>
        <div className="filter-bar">
          <input className="input input-sm" style={{ width: 220 }} placeholder={tr('搜索模型 ID', 'Search model id')} value={q} onChange={(e) => setQ(e.target.value)} data-testid="mg-q" />
          <select className="select input-sm" value={status} onChange={(e) => setStatus(e.target.value)} data-testid="mg-status">
            <option value="">{tr('全部可用性', 'Any availability')}</option>
            {(Object.keys(HEALTH_LABEL) as ModelHealthStatus[]).map((s) => (
              <option key={s} value={s}>
                {tr(HEALTH_LABEL[s][0], HEALTH_LABEL[s][1])} ({statusCounts[s] ?? 0})
              </option>
            ))}
          </select>
        </div>
        {tab === 'add' && (
          <div className="row" style={{ gap: 6 }}>
            <input className="input input-sm mono" style={{ width: 260 }} placeholder={tr('手动输入模型 ID（逗号分隔）', 'Model ids, comma separated')} value={manual} onChange={(e) => setManual(e.target.value)} data-testid="mg-manual-input" />
            <button className="btn btn-sm" data-testid="mg-manual-add" disabled={!manual.trim() || !!busy} onClick={() => void run('manual', async () => { await addIds(splitList(manual)); setManual(''); })}>
              <IconPlus />
              {tr('添加', 'Add')}
            </button>
            <span className="faint small">{tr('新加入的模型限定凭据分组：', 'Limit new models to credential groups:')}</span>
            <select className="select input-sm" style={{ width: 180 }} value="" onChange={(e) => e.target.value && setRestrict([...new Set([...restrict, Number(e.target.value)])])}>
              <option value="">{restrict.length ? restrict.map((id) => cgName.get(id)).join('、') : tr('不限（全部凭据）', 'none (all credentials)')}</option>
              {summary.credential_groups.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.channel_name} · {availabilityLabel(g, tr)}
                </option>
              ))}
            </select>
            {restrict.length > 0 && (
              <button className="btn btn-sm btn-ghost" onClick={() => setRestrict([])}>
                {tr('清除限定', 'Clear')}
              </button>
            )}
          </div>
        )}
        <SelectionBar selected={sel.size} total={rows.length} pageIds={pageRows} sel={sel} loadAllIds={async () => rows}>
          {tab === 'members' ? (
            <button
              className="btn btn-sm btn-danger"
              disabled={!sel.size || !!busy}
              data-testid="mg-remove-selected"
              onClick={async () => {
                if (!(await confirm({ title: tr(`移除 ${sel.size} 个模型`, `Remove ${sel.size} models`), body: tr('绑定此分组的密钥将不能再调用这些模型（除非其他分组授予）。', 'Keys bound to this group lose these models unless another group grants them.'), danger: true }))) return;
                await run('rm', async () => {
                  const r = await api.bulkModelGroupMembers(group.id, { remove: selMembers.list() });
                  toast(tr(`已移除 ${r.result.removed} 个`, `Removed ${r.result.removed}`));
                  selMembers.clear();
                  await reload();
                });
              }}
            >
              <IconTrash />
              {tr(`移除所选（${sel.size}）`, `Remove (${sel.size})`)}
            </button>
          ) : (
            <button className="btn btn-sm btn-primary" disabled={!sel.size || !!busy} onClick={() => void run('add', () => addIds(selAdd.list()))} data-testid="mg-add-selected">
              {busy === 'add' && <Spinner />}
              {tr(`添加所选（${sel.size}）`, `Add selected (${sel.size})`)}
            </button>
          )}
        </SelectionBar>
        {members.error ? (
          <ErrorBox error={members.error} onRetry={members.reload} />
        ) : !members.data ? (
          <Loading />
        ) : pageRows.length === 0 ? (
          <Empty>{tab === 'members' ? tr('此分组还没有模型（绑定它的密钥将无模型可用）', 'No models (bound keys can call nothing)') : tr('没有可添加的模型', 'Nothing to add')}</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table compact" data-testid="mg-table">
              <thead>
                <tr>
                  <th style={{ width: 28 }} />
                  <th>{tr('模型', 'Model')}</th>
                  <th>{tr('可用性', 'Availability')}</th>
                  {tab === 'members' && <th>{tr('限定凭据分组', 'Credential groups')}</th>}
                </tr>
              </thead>
              <tbody>
                {pageRows.map((id) => {
                  const st = statusOf.get(id.toLowerCase());
                  const m = tab === 'members' ? memberRows.find((x) => x.model_id === id) : undefined;
                  const ch = m?.channels ?? [];
                  return (
                    <tr key={id} className="clickable" onClick={() => sel.toggle(id)}>
                      <td>
                        <input type="checkbox" checked={sel.has(id)} onChange={() => sel.toggle(id)} onClick={(e) => e.stopPropagation()} />
                      </td>
                      <td className="mono">{id}</td>
                      <td>{st ? <Badge kind={st === 'available' ? 'ok' : st === 'partial' || st === 'cooling' ? 'warn' : st === 'unknown' ? undefined : 'err'}>{tr(HEALTH_LABEL[st][0], HEALTH_LABEL[st][1])}</Badge> : <span className="faint small">{tr('不在当前目录', 'not in catalog')}</span>}</td>
                      {tab === 'members' && (
                        <td onClick={(e) => e.stopPropagation()}>
                          <button className="btn btn-sm btn-ghost" onClick={() => m && setEditing(m)}>
                            {ch.length ? ch.map((c) => cgName.get(c) ?? `#${c}`).join('、') : tr('全部凭据', 'all credentials')}
                          </button>
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <PageNav page={page} pageSize={size} total={rows.length} onPage={setPage} />
      </div>
      {editing && (
        <Modal
          title={tr(`限定 ${editing.model_id} 的凭据分组`, `Credential groups for ${editing.model_id}`)}
          onClose={() => setEditing(null)}
          footer={
            <button className="btn btn-primary" disabled={!!busy} onClick={() => void run('ch', async () => { await api.updateModelGroupMember(editing.id, editing.channels ?? []); setEditing(null); await reload(); })}>
              {tr('保存', 'Save')}
            </button>
          }
        >
          <p className="hint">{tr('不选 = 由密钥可用的全部凭据服务；选择后只路由到所选凭据分组（与密钥自身绑定的凭据分组取交集，交集为空时请求失败）。', 'None = any credential the key may use; otherwise only the chosen groups (intersected with the key’s own groups; empty fails).')}</p>
          <div className="checklist">
            {summary.credential_groups.map((g) => (
              <label key={g.id} className="check">
                <input
                  type="checkbox"
                  checked={(editing.channels ?? []).includes(g.id)}
                  onChange={(e) => setEditing({ ...editing, channels: e.target.checked ? [...(editing.channels ?? []), g.id] : (editing.channels ?? []).filter((x) => x !== g.id) })}
                />
                <span>{g.channel_name}</span>
                <span className="faint small"> · {availabilityLabel(g, tr)}</span>
              </label>
            ))}
          </div>
        </Modal>
      )}
    </Drawer>
  );
}

// ------------------------------------------------------------ import / export

export function ImportExportModal({ onClose, onImported }: { onClose: () => void; onImported: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [format, setFormat] = useState<'json' | 'csv'>('json');
  const [mode, setMode] = useState<'merge' | 'replace'>('merge');
  const [data, setData] = useState('');
  const [report, setReport] = useState<Awaited<ReturnType<typeof api.importGroups>>['report'] | null>(null);
  const doImport = (dry: boolean) =>
    run(dry ? 'dry' : 'import', async () => {
      const r = await api.importGroups({ format, data, mode, dry_run: dry });
      setReport(r.report);
      if (!dry) {
        toast(tr('导入完成', 'Imported'));
        await onImported();
      }
    });
  return (
    <Modal title={tr('导入 / 导出分组成员', 'Import / export membership')} onClose={onClose} wide>
      <div className="stack">
        <div className="row" style={{ gap: 8 }}>
          <strong>{tr('导出', 'Export')}</strong>
          {(['json', 'csv'] as const).map((f) => (
            <button
              key={f}
              className="btn btn-sm"
              disabled={!!busy}
              data-testid={`export-${f}`}
              onClick={() =>
                void run(`exp-${f}`, async () => {
                  const blob = await api.exportGroups(f);
                  downloadBlob(blob, `groups-${new Date().toISOString().slice(0, 10)}.${f}`);
                })
              }
            >
              <IconDownload />
              {f.toUpperCase()}
            </button>
          ))}
          <span className="faint small">{tr('JSON 含规则与模型的凭据分组限定；CSV 每行一条成员（type,group,member,label,credential_groups）。', 'JSON includes rules and per-model credential groups; CSV has one member per row (type,group,member,label,credential_groups).')}</span>
        </div>
        <hr />
        <strong>{tr('导入', 'Import')}</strong>
        <div className="row" style={{ gap: 12 }}>
          <select className="select input-sm" style={{ width: 100 }} value={format} onChange={(e) => setFormat(e.target.value as 'json' | 'csv')}>
            <option value="json">JSON</option>
            <option value="csv">CSV</option>
          </select>
          <label className="check">
            <input type="radio" checked={mode === 'merge'} onChange={() => setMode('merge')} />
            <span>{tr('合并（只添加）', 'Merge (add only)')}</span>
          </label>
          <label className="check">
            <input type="radio" checked={mode === 'replace'} onChange={() => setMode('replace')} />
            <span>{tr('替换（文件中出现的分组以文件为准）', 'Replace (listed groups match the file)')}</span>
          </label>
          <input
            type="file"
            accept=".json,.csv,text/csv,application/json"
            onChange={async (e) => {
              const f = e.target.files?.[0];
              if (!f) return;
              setData(await f.text());
              setFormat(f.name.toLowerCase().endsWith('.csv') ? 'csv' : 'json');
              setReport(null);
            }}
          />
        </div>
        <textarea className="input mono" rows={8} value={data} onChange={(e) => (setData(e.target.value), setReport(null))} placeholder={format === 'csv' ? 'type,group,member\ncredential,team-a,user@corp.com\nmodel,basic,gemini-3-flash' : '{"credential_groups":[{"name":"team-a","enabled":true,"members":["user@corp.com"]}]}'} data-testid="import-data" />
        <p className="hint">{tr('分组按名称匹配，不存在则创建；成员可写凭据 ID、邮箱或名称。整个导入在一个事务中执行，先“预检”查看影响。', 'Groups match by name (created if missing); members may be credential ids, emails or labels. The import runs in one transaction — dry-run first.')}</p>
        {report && (
          <div className={`alert small ${report.unresolved.length ? 'warn' : ''}`} data-testid="import-report">
            {report.dry_run ? tr('预检结果（未写入）：', 'Dry run (nothing written): ') : tr('已导入：', 'Imported: ')}
            {tr(
              `新建分组 ${report.groups_created} · 添加成员 ${report.members_added} · 移除成员 ${report.members_removed} · 添加模型 ${report.models_added} · 移除模型 ${report.models_removed} · 规则 ${report.rules_set}`,
              `groups created ${report.groups_created} · members +${report.members_added} −${report.members_removed} · models +${report.models_added} −${report.models_removed} · rules ${report.rules_set}`,
            )}
            {report.unresolved.length > 0 && (
              <div className="small" style={{ marginTop: 4 }}>
                {tr(`无法识别 ${report.unresolved.length} 项：`, `${report.unresolved.length} unresolved: `)}
                {report.unresolved.slice(0, 10).join('；')}
                {report.unresolved.length > 10 ? ' …' : ''}
              </div>
            )}
          </div>
        )}
        <div className="row" style={{ justifyContent: 'flex-end', gap: 8 }}>
          <button className="btn" disabled={!data.trim() || !!busy} onClick={() => void doImport(true)} data-testid="import-dry">
            {busy === 'dry' && <Spinner />}
            {tr('预检', 'Dry run')}
          </button>
          <button className="btn btn-primary" disabled={!data.trim() || !!busy} onClick={() => void doImport(false)} data-testid="import-apply">
            {busy === 'import' && <Spinner />}
            {tr('导入', 'Import')}
          </button>
        </div>
      </div>
    </Modal>
  );
}
