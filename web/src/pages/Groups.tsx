import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import type { CredentialGroupSummary, GroupsSummary, ModelGroupSummary } from '../api/client';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Modal, Spinner, Switch } from '../components/ui';
import { IconEdit, IconPlus, IconRefresh, IconTrash } from '../components/icons';
import { PageNav, useDebounced } from '../components/picker';
import { availabilityLabel, availabilityTitle, CredentialMembersDrawer, ImportExportModal, ModelMembersDrawer } from './GroupMembers';

type Tab = 'credentials' | 'models';

export function GroupsPage() {
  const api = useApi();
  const { tr } = useI18n();
  const [tab, setTab] = useState<Tab>('credentials');
  const [io, setIo] = useState(false);
  const summary = useAsync(() => api.groupsSummary(), [], 15000);
  const s = summary.data;
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{tr('分组与权限', 'Groups & access')}</h1>
          <p>
            {tr(
              '凭据分组把上游账号分组隔离（例如不同下游服务使用不同账号额度）；模型分组限定访问密钥可调用的模型。两者都在“访问密钥”页面绑定（支持批量）。',
              'Credential groups isolate upstream accounts (e.g. per downstream service quota); model groups limit which models an access key may call. Bind both on the Access keys page (bulk supported).',
            )}
          </p>
        </div>
        <div className="toolbar">
          <button className="btn" onClick={() => setIo(true)} data-testid="open-import-export">
            {tr('导入 / 导出', 'Import / export')}
          </button>
          <button className="btn" onClick={summary.reload}>
            <IconRefresh />
            {tr('刷新', 'Refresh')}
          </button>
        </div>
      </div>
      {s && (
        <div className="faint small" data-testid="groups-overview">
          {tr(
            `凭据 ${s.credentials} 个（未加入任何分组 ${s.ungrouped_credentials}）· 访问密钥 ${s.keys} 个（不受限 ${s.unrestricted_keys}）· 凭据分组 ${s.credential_groups.length} · 模型分组 ${s.model_groups.length}`,
            `${s.credentials} credentials (${s.ungrouped_credentials} in no group) · ${s.keys} keys (${s.unrestricted_keys} unrestricted) · ${s.credential_groups.length} credential groups · ${s.model_groups.length} model groups`,
          )}
        </div>
      )}
      <div className="tabs">
        <button className={tab === 'credentials' ? 'active' : ''} onClick={() => setTab('credentials')} data-testid="tab-credential-groups">
          {tr('凭据分组', 'Credential groups')}
          {s ? ` (${s.credential_groups.length})` : ''}
        </button>
        <button className={tab === 'models' ? 'active' : ''} onClick={() => setTab('models')} data-testid="tab-model-groups">
          {tr('模型分组', 'Model groups')}
          {s ? ` (${s.model_groups.length})` : ''}
        </button>
      </div>
      {summary.error ? (
        <ErrorBox error={summary.error} onRetry={summary.reload} />
      ) : !s ? (
        <Loading />
      ) : tab === 'credentials' ? (
        <CredentialGroupsTab s={s} reload={summary.reload} />
      ) : (
        <ModelGroupsTab s={s} reload={summary.reload} />
      )}
      {io && <ImportExportModal onClose={() => setIo(false)} onImported={summary.reload} />}
    </div>
  );
}

function useNameFilter<T>(items: T[], name: (t: T) => string) {
  const [q, setQ] = useState('');
  const dq = useDebounced(q, 150);
  const [page, setPage] = useState(1);
  const filtered = useMemo(() => items.filter((i) => !dq || name(i).toLowerCase().includes(dq.toLowerCase())), [items, dq, name]);
  const size = 30;
  const pages = Math.max(1, Math.ceil(filtered.length / size));
  const p = Math.min(page, pages);
  return { q, setQ, filtered, rows: filtered.slice((p - 1) * size, p * size), page: p, setPage, size };
}

async function unbindKeys(api: ReturnType<typeof useApi>, field: 'channel' | 'model_group', id: number) {
  const ids = (await api.accessKeyIds({ [field]: id })).ids;
  if (ids.length) await api.bulkAccessKeys(field === 'channel' ? { ids, remove_channels: [id] } : { ids, remove_model_groups: [id] });
  return ids.length;
}

// ---------------------------------------------------------------- credential groups

function CredentialGroupsTab({ s, reload }: { s: GroupsSummary; reload: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [naming, setNaming] = useState<CredentialGroupSummary | 'new' | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const nf = useNameFilter(s.credential_groups, (g) => g.channel_name);
  const current = s.credential_groups.find((g) => g.id === open);

  const remove = async (g: CredentialGroupSummary) => {
    const ok = await confirm({
      title: tr(`删除凭据分组「${g.channel_name}」`, `Delete credential group "${g.channel_name}"`),
      body: g.keys
        ? tr(`有 ${g.keys} 个访问密钥绑定了该分组，删除后将自动解绑。若这些密钥没有其他凭据分组，将恢复为可使用全部凭据。`, `${g.keys} keys are bound to it and will be unbound. Keys left without groups can use all credentials again.`)
        : tr('分组成员关系与规则会一并删除，凭据本身不受影响。', 'Memberships and its rule are removed; credentials are not affected.'),
      danger: true,
      confirmText: tr('删除', 'Delete'),
    });
    if (!ok) return;
    await run(`del-cg-${g.id}`, async () => {
      await unbindKeys(api, 'channel', g.id);
      if (g.has_rule) await api.deleteGroupRule(g.id).catch(() => undefined);
      await api.deleteCredentialGroup(g.id);
      toast(tr('已删除', 'Deleted'));
      await reload();
    });
  };

  return (
    <div className="stack">
      <div className="alert warn" data-testid="cg-warning">
        <strong>{tr('注意：没有回退。', 'No fallback.')}</strong>{' '}
        {tr(
          '绑定了凭据分组的访问密钥只会使用分组内的凭据。分组内凭据全部冷却、额度耗尽或被禁用时，该密钥的请求会直接失败（503 / 429），不会借用分组外的凭据。“当前可用”由服务端实时计算。',
          'A key bound to credential groups only uses those credentials. When all of them are cooling, out of quota or disabled, its requests fail (503 / 429) instead of borrowing other credentials. "Usable now" is computed server-side.',
        )}
      </div>
      <Card
        title={
          <input className="input input-sm" style={{ width: 220 }} placeholder={tr('搜索分组名', 'Search groups')} value={nf.q} onChange={(e) => nf.setQ(e.target.value)} data-testid="cg-search" />
        }
        extra={
          <button className="btn btn-sm btn-primary" onClick={() => setNaming('new')} data-testid="create-credential-group">
            <IconPlus />
            {tr('新建分组', 'New group')}
          </button>
        }
        bodyClass="table-wrap"
      >
        {s.credential_groups.length === 0 ? (
          <Empty>{tr('还没有凭据分组。未绑定分组的密钥可使用全部凭据。', 'No credential groups. Keys without groups use all credentials.')}</Empty>
        ) : nf.rows.length === 0 ? (
          <Empty>{tr('没有匹配的分组', 'No matching groups')}</Empty>
        ) : (
          <table className="table" data-testid="credential-group-table">
            <thead>
              <tr>
                <th>{tr('分组', 'Group')}</th>
                <th>{tr('当前可用', 'Usable now')}</th>
                <th className="hide-sm">{tr('成员', 'Members')}</th>
                <th className="hide-sm">{tr('绑定密钥', 'Keys')}</th>
                <th style={{ textAlign: 'right' }}>{tr('操作', 'Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {nf.rows.map((g) => {
                const kind = !g.enabled ? undefined : g.total === 0 ? undefined : g.usable === 0 ? 'err' : g.usable < g.total ? 'warn' : 'ok';
                return (
                  <tr key={g.id} className={`clickable ${g.enabled ? '' : 'row-muted'}`} onClick={() => setOpen(g.id)} data-testid={`cg-${g.id}`}>
                    <td>
                      <strong>{g.channel_name}</strong>
                      {g.has_rule && (
                        <span className="tag" style={{ marginLeft: 6 }} title={tr('含自动加入规则', 'Has an auto-join rule')}>
                          ⚙ {tr('规则', 'rule')}
                        </span>
                      )}
                    </td>
                    <td title={availabilityTitle(g, tr)}>
                      <Badge kind={kind} dot>
                        {!g.enabled ? tr('已停用', 'Disabled') : availabilityLabel(g, tr)}
                      </Badge>
                      {g.enabled && g.total > 0 && g.usable === 0 && g.keys > 0 && <div className="err-text small">{tr('绑定的密钥当前请求会失败', 'Bound keys are failing now')}</div>}
                    </td>
                    <td className="hide-sm small">
                      {g.total}
                      <span className="faint">
                        {' '}
                        {tr(`（手动 ${g.static_count} · 规则 ${g.rule_count}）`, `(manual ${g.static_count} · rule ${g.rule_count})`)}
                      </span>
                    </td>
                    <td className="hide-sm">{g.keys || <span className="faint">0</span>}</td>
                    <td className="actions" onClick={(e) => e.stopPropagation()}>
                      <button className="btn btn-sm" onClick={() => setOpen(g.id)} data-testid={`cg-members-${g.id}`}>
                        {tr('成员', 'Members')}
                      </button>
                      <span title={g.enabled ? tr('启用中（停用后绑定此分组的密钥将无凭据可用）', 'Enabled (disabling leaves bound keys without credentials)') : tr('已停用', 'Disabled')}>
                        <Switch checked={g.enabled} disabled={!!busy} onChange={(v) => void run('toggle', async () => { await api.updateCredentialGroup(g.id, { enabled: v }); await reload(); })} />
                      </span>
                      <button className="btn btn-sm btn-ghost" onClick={() => setNaming(g)} title={tr('重命名', 'Rename')}>
                        <IconEdit />
                      </button>
                      <button className="btn btn-sm btn-ghost btn-danger" onClick={() => void remove(g)} disabled={busy === `del-cg-${g.id}`} title={tr('删除', 'Delete')} data-testid={`cg-delete-${g.id}`}>
                        {busy === `del-cg-${g.id}` ? <Spinner /> : <IconTrash />}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        {nf.filtered.length > nf.size && <PageNav page={nf.page} pageSize={nf.size} total={nf.filtered.length} onPage={nf.setPage} />}
      </Card>
      {current && <CredentialMembersDrawer group={current} summary={s} onClose={() => setOpen(null)} onChanged={reload} />}
      {naming && (
        <NameModal
          title={naming === 'new' ? tr('新建凭据分组', 'New credential group') : tr('重命名凭据分组', 'Rename credential group')}
          initial={naming === 'new' ? '' : naming.channel_name}
          onClose={() => setNaming(null)}
          onSave={async (name) => {
            if (naming === 'new') await api.createCredentialGroup(name);
            else await api.updateCredentialGroup(naming.id, { channel_name: name });
            setNaming(null);
            await reload();
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------- model groups

function ModelGroupsTab({ s, reload }: { s: GroupsSummary; reload: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [naming, setNaming] = useState<ModelGroupSummary | 'new' | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const nf = useNameFilter(s.model_groups, (g) => g.group_name);
  const current = s.model_groups.find((g) => g.id === open);

  const remove = async (g: ModelGroupSummary) => {
    const ok = await confirm({
      title: tr(`删除模型分组「${g.group_name}」`, `Delete model group "${g.group_name}"`),
      body: g.keys
        ? tr(`有 ${g.keys} 个访问密钥绑定了该分组，删除后将自动解绑。若这些密钥没有其他模型分组，将恢复为可调用全部模型。`, `${g.keys} keys are bound to it and will be unbound. Keys left without groups may call every model again.`)
        : tr('分组内的模型条目会一并删除。', 'Its model entries are removed too.'),
      danger: true,
      confirmText: tr('删除', 'Delete'),
    });
    if (!ok) return;
    await run(`del-mg-${g.id}`, async () => {
      await unbindKeys(api, 'model_group', g.id);
      await api.deleteModelGroup(g.id);
      toast(tr('已删除', 'Deleted'));
      await reload();
    });
  };

  return (
    <div className="stack">
      <div className="alert small">
        {tr(
          '未绑定模型分组的访问密钥可以调用全部模型。绑定后只能调用所绑定分组中模型的并集：/v1/models（以及 Gemini、Claude 的模型列表）只列出这些模型，请求其他模型返回 403（model_not_allowed）。可为单个模型额外限定只由某些凭据分组服务（与密钥自身绑定的凭据分组取交集）。',
          'Keys without model groups may call every model. Bound keys may only call the union of their groups: model lists show only those models and other models get 403 (model_not_allowed). A model entry can additionally be limited to credential groups (intersected with the key’s own credential groups).',
        )}
      </div>
      <Card
        title={<input className="input input-sm" style={{ width: 220 }} placeholder={tr('搜索分组名', 'Search groups')} value={nf.q} onChange={(e) => nf.setQ(e.target.value)} />}
        extra={
          <button className="btn btn-sm btn-primary" onClick={() => setNaming('new')} data-testid="create-model-group">
            <IconPlus />
            {tr('新建分组', 'New group')}
          </button>
        }
        bodyClass="table-wrap"
      >
        {s.model_groups.length === 0 ? (
          <Empty>{tr('还没有模型分组。', 'No model groups yet.')}</Empty>
        ) : (
          <table className="table" data-testid="model-group-table">
            <thead>
              <tr>
                <th>{tr('分组', 'Group')}</th>
                <th>{tr('模型数', 'Models')}</th>
                <th className="hide-sm">{tr('绑定密钥', 'Keys')}</th>
                <th style={{ textAlign: 'right' }}>{tr('操作', 'Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {nf.rows.map((g) => (
                <tr key={g.id} className={`clickable ${g.enabled ? '' : 'row-muted'}`} onClick={() => setOpen(g.id)} data-testid={`mg-${g.id}`}>
                  <td>
                    <strong>{g.group_name}</strong>
                  </td>
                  <td>
                    <Badge kind={!g.enabled ? undefined : g.models === 0 ? 'warn' : 'primary'}>
                      {!g.enabled ? tr('已停用', 'Disabled') : g.models === 0 ? tr('无模型', 'empty') : tr(`${g.models} 个模型`, `${g.models} models`)}
                    </Badge>
                  </td>
                  <td className="hide-sm">{g.keys || <span className="faint">0</span>}</td>
                  <td className="actions" onClick={(e) => e.stopPropagation()}>
                    <button className="btn btn-sm" onClick={() => setOpen(g.id)} data-testid={`mg-models-${g.id}`}>
                      {tr('模型', 'Models')}
                    </button>
                    <Switch checked={g.enabled} disabled={!!busy} onChange={(v) => void run('toggle', async () => { await api.updateModelGroup(g.id, { enabled: v }); await reload(); })} />
                    <button className="btn btn-sm btn-ghost" onClick={() => setNaming(g)} title={tr('重命名', 'Rename')}>
                      <IconEdit />
                    </button>
                    <button className="btn btn-sm btn-ghost btn-danger" onClick={() => void remove(g)} disabled={busy === `del-mg-${g.id}`} title={tr('删除', 'Delete')} data-testid={`mg-delete-${g.id}`}>
                      {busy === `del-mg-${g.id}` ? <Spinner /> : <IconTrash />}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {nf.filtered.length > nf.size && <PageNav page={nf.page} pageSize={nf.size} total={nf.filtered.length} onPage={nf.setPage} />}
      </Card>
      {current && <ModelMembersDrawer group={current} summary={s} onClose={() => setOpen(null)} onChanged={reload} />}
      {naming && (
        <NameModal
          title={naming === 'new' ? tr('新建模型分组', 'New model group') : tr('重命名模型分组', 'Rename model group')}
          initial={naming === 'new' ? '' : naming.group_name}
          onClose={() => setNaming(null)}
          onSave={async (name) => {
            if (naming === 'new') await api.createModelGroup(name);
            else await api.updateModelGroup(naming.id, { group_name: name });
            setNaming(null);
            await reload();
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------- shared bits

export function GroupChecklist({ groups, value, onChange }: { groups: { id: number; name: string; enabled: boolean; extra?: string }[]; value: number[]; onChange: (v: number[]) => void }) {
  const { tr } = useI18n();
  const [q, setQ] = useState('');
  if (groups.length === 0) return <div className="faint small">{tr('还没有分组，请先在“分组与权限”页面创建。', 'No groups yet – create them on the Groups page.')}</div>;
  const shown = groups.filter((g) => !q || g.name.toLowerCase().includes(q.toLowerCase()) || value.includes(g.id));
  return (
    <div className="stack" style={{ gap: 6 }}>
      {groups.length > 8 && <input className="input input-sm" style={{ width: 220 }} placeholder={tr(`搜索 ${groups.length} 个分组`, `Search ${groups.length} groups`)} value={q} onChange={(e) => setQ(e.target.value)} />}
      <div className="checklist scroll-list">
        {shown.map((g) => (
          <label key={g.id} className={`check ${g.enabled ? '' : 'faint'}`}>
            <input type="checkbox" checked={value.includes(g.id)} onChange={(e) => onChange(e.target.checked ? [...value, g.id] : value.filter((x) => x !== g.id))} />
            <span>{g.name}</span>
            {!g.enabled && <span className="small"> · {tr('已停用', 'disabled')}</span>}
            {g.extra && <span className="faint small"> · {g.extra}</span>}
          </label>
        ))}
      </div>
    </div>
  );
}

function NameModal({ title, initial, onClose, onSave }: { title: string; initial: string; onClose: () => void; onSave: (name: string) => Promise<void> }) {
  const { t, tr } = useI18n();
  const { busy, run } = useAction();
  const [name, setName] = useState(initial);
  const valid = name.trim().length > 0;
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {t('cancel')}
          </button>
          <button className="btn btn-primary" disabled={!valid || !!busy} onClick={() => void run('save', () => onSave(name.trim()))} data-testid="group-name-save">
            {busy && <Spinner />}
            {t('save')}
          </button>
        </>
      }
    >
      <div className="field">
        <label>{tr('名称', 'Name')}</label>
        <input className="input" value={name} maxLength={64} autoFocus onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && valid && void run('save', () => onSave(name.trim()))} data-testid="group-name-input" />
      </div>
    </Modal>
  );
}

/** Group names + server-side availability for the access-key and credentials pages. */
export function useGroupLookup() {
  const api = useApi();
  const res = useAsync(() => api.groupsSummary(), [], 30000);
  const maps = useMemo(
    () => ({
      cgNames: new Map((res.data?.credential_groups ?? []).map((g) => [g.id, g.channel_name])),
      mgNames: new Map((res.data?.model_groups ?? []).map((g) => [g.id, g.group_name])),
      cgById: new Map((res.data?.credential_groups ?? []).map((g) => [g.id, g])),
    }),
    [res.data],
  );
  return { ...res, ...maps };
}
