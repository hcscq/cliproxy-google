import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import {
  credentialGroupAvailability, credentialUsable, coolingModels,
  type AccessKey, type Credential, type CredentialGroup, type CredentialGroupMember, type ModelGroup, type ModelGroupMember,
} from '../api/client';
import { useI18n } from '../i18n';
import { Badge, Card, Empty, ErrorBox, Loading, Modal, Spinner, Switch } from '../components/ui';
import { IconEdit, IconPlus, IconRefresh, IconTrash, IconX } from '../components/icons';

type Tab = 'credentials' | 'models';

/** Everything the group pages need, loaded together so counts stay consistent. */
function useGroupData() {
  const api = useApi();
  return useAsync(
    async () => {
      const [cg, cgm, mg, mgm, keys, creds, health] = await Promise.all([
        api.credentialGroups(),
        api.credentialGroupMembers(),
        api.modelGroups(),
        api.modelGroupMembers(),
        api.accessKeys(),
        api.credentials(),
        api.modelHealth().catch(() => undefined),
      ]);
      return {
        credentialGroups: cg.channel_groups ?? [],
        credentialMembers: cgm.channel_group_details ?? [],
        modelGroups: mg.model_groups ?? [],
        modelMembers: mgm.model_group_details ?? [],
        keys: keys.items ?? [],
        creds: (creds.files ?? []) as Credential[],
        modelIds: (health?.models ?? []).filter((m) => m.status !== 'removed').map((m) => m.id),
      };
    },
    [],
    30000,
  );
}
export type GroupData = NonNullable<ReturnType<typeof useGroupData>['data']>;

export function GroupsPage() {
  const { tr } = useI18n();
  const [tab, setTab] = useState<Tab>('credentials');
  const data = useGroupData();
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{tr('分组与权限', 'Groups & access')}</h1>
          <p>
            {tr(
              '凭据分组把上游账号分组隔离（例如不同下游服务使用不同账号额度）；模型分组限定访问密钥可调用的模型。两者都在“访问密钥”的编辑对话框中绑定。',
              'Credential groups isolate upstream accounts (e.g. per downstream service quota); model groups limit which models an access key may call. Bind both in the access key dialog.',
            )}
          </p>
        </div>
        <button className="btn" onClick={data.reload}>
          <IconRefresh />
          {tr('刷新', 'Refresh')}
        </button>
      </div>
      <div className="tabs">
        <button className={tab === 'credentials' ? 'active' : ''} onClick={() => setTab('credentials')} data-testid="tab-credential-groups">
          {tr('凭据分组', 'Credential groups')}
        </button>
        <button className={tab === 'models' ? 'active' : ''} onClick={() => setTab('models')} data-testid="tab-model-groups">
          {tr('模型分组', 'Model groups')}
        </button>
      </div>
      {data.error ? (
        <ErrorBox error={data.error} onRetry={data.reload} />
      ) : !data.data ? (
        <Loading />
      ) : tab === 'credentials' ? (
        <CredentialGroupsTab d={data.data} reload={data.reload} />
      ) : (
        <ModelGroupsTab d={data.data} reload={data.reload} />
      )}
    </div>
  );
}

function boundKeys(keys: AccessKey[], field: 'channels' | 'model_groups', id: number) {
  return keys.filter((k) => (k[field] ?? []).includes(id));
}

function keyLabel(k: AccessKey) {
  return k.display_name || `ID ${k.id}`;
}

function credLabel(c?: Credential, fallback?: string) {
  return c ? c.email || c.label || c.name || c.id : fallback ?? '?';
}

// ---------------------------------------------------------------- credential groups

function CredentialGroupsTab({ d, reload }: { d: GroupData; reload: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [naming, setNaming] = useState<CredentialGroup | 'new' | null>(null);

  const remove = async (g: CredentialGroup) => {
    const keys = boundKeys(d.keys, 'channels', g.id);
    const ok = await confirm({
      title: tr(`删除凭据分组「${g.channel_name}」`, `Delete credential group "${g.channel_name}"`),
      body: keys.length
        ? tr(
            `以下密钥绑定了该分组，删除后将自动解绑：${keys.map(keyLabel).join('、')}。若这些密钥没有其他凭据分组，将恢复为可使用全部凭据。`,
            `These keys are bound to it and will be unbound: ${keys.map(keyLabel).join(', ')}. Keys left without groups can use all credentials again.`,
          )
        : tr('分组成员关系会一并删除，凭据本身不受影响。', 'Memberships are removed; credentials are not affected.'),
      danger: true,
      confirmText: tr('删除', 'Delete'),
    });
    if (!ok) return;
    await run(`del-cg-${g.id}`, async () => {
      for (const k of keys) await api.setAccessKeyScope(k.id, { channels: (k.channels ?? []).filter((x) => x !== g.id) });
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
          '绑定了凭据分组的访问密钥只会使用分组内的凭据。分组内凭据全部冷却、额度耗尽或被禁用时，该密钥的请求会直接失败（503 / 429），不会借用分组外的凭据。下方“当前可用”为实时状态。',
          'A key bound to credential groups only uses those credentials. When all of them are cooling, out of quota or disabled, its requests fail (503 / 429) instead of borrowing other credentials. "Usable now" below is live.',
        )}
      </div>
      <Card
        title={tr(`凭据分组（${d.credentialGroups.length}）`, `Credential groups (${d.credentialGroups.length})`)}
        extra={
          <button className="btn btn-sm btn-primary" onClick={() => setNaming('new')} data-testid="create-credential-group">
            <IconPlus />
            {tr('新建分组', 'New group')}
          </button>
        }
      >
        {d.credentialGroups.length === 0 ? (
          <Empty>{tr('还没有凭据分组。未绑定分组的密钥可使用全部凭据。', 'No credential groups. Keys without groups use all credentials.')}</Empty>
        ) : (
          <div className="group-list">
            {d.credentialGroups.map((g) => (
              <CredentialGroupRow key={g.id} g={g} d={d} reload={reload} onRename={() => setNaming(g)} onDelete={() => void remove(g)} deleting={busy === `del-cg-${g.id}`} />
            ))}
          </div>
        )}
      </Card>
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

function CredentialGroupRow({ g, d, reload, onRename, onDelete, deleting }: { g: CredentialGroup; d: GroupData; reload: () => Promise<void>; onRename: () => void; onDelete: () => void; deleting: boolean }) {
  const api = useApi();
  const { tr } = useI18n();
  const { busy, run } = useAction();
  const [adding, setAdding] = useState('');
  const members = d.credentialMembers.filter((m) => m.channel_group_id === g.id);
  const credById = new Map(d.creds.map((c) => [c.id, c]));
  const avail = credentialGroupAvailability(g.id, d.credentialMembers, d.creds);
  const keys = boundKeys(d.keys, 'channels', g.id);
  const candidates = d.creds.filter((c) => !members.some((m) => m.auth_id === c.id));
  const kind = !g.enabled ? undefined : avail.usable === 0 ? 'err' : avail.usable < avail.total ? 'warn' : 'ok';

  return (
    <div className={`group-row ${g.enabled ? '' : 'row-muted'}`} data-testid={`cg-${g.id}`}>
      <div className="group-head">
        <strong className="group-name">{g.channel_name}</strong>
        <Badge kind={kind} dot>
          {!g.enabled
            ? tr('已停用', 'Disabled')
            : avail.total === 0
              ? tr('无成员', 'No members')
              : tr(`当前可用 ${avail.usable}/${avail.total}`, `Usable now ${avail.usable}/${avail.total}`)}
        </Badge>
        {g.enabled && avail.total > 0 && avail.usable === 0 && keys.length > 0 && (
          <span className="err-text small">{tr('绑定此分组的密钥当前请求会失败', 'Keys bound here are failing now')}</span>
        )}
        <span className="spacer" />
        <span title={g.enabled ? tr('启用中（停用后绑定此分组的密钥将无凭据可用）', 'Enabled (disabling leaves bound keys without credentials)') : tr('已停用', 'Disabled')}>
          <Switch checked={g.enabled} disabled={!!busy} onChange={(v) => void run('toggle', async () => { await api.updateCredentialGroup(g.id, { enabled: v }); await reload(); })} />
        </span>
        <button className="btn btn-sm btn-ghost" onClick={onRename} title={tr('重命名', 'Rename')}>
          <IconEdit />
        </button>
        <button className="btn btn-sm btn-ghost btn-danger" onClick={onDelete} disabled={deleting} title={tr('删除', 'Delete')} data-testid={`cg-delete-${g.id}`}>
          {deleting ? <Spinner /> : <IconTrash />}
        </button>
      </div>
      <div className="group-body">
        <div className="tag-list">
          {members.length === 0 && <span className="faint small">{tr('还没有成员', 'No members yet')}</span>}
          {members.map((m) => {
            const c = credById.get(m.auth_id);
            const usable = c ? credentialUsable(c) : false;
            const cooling = c ? coolingModels(c).length : 0;
            const title = !c
              ? tr('凭据已不存在', 'Credential no longer exists')
              : usable
                ? cooling
                  ? tr(`可用（${cooling} 个模型冷却中）`, `Usable (${cooling} models cooling)`)
                  : tr('可用', 'Usable')
                : c.disabled
                  ? tr('已禁用', 'Disabled')
                  : tr(`不可用：${c.status_message || c.status || ''}`, `Unavailable: ${c.status_message || c.status || ''}`);
            return (
              <span key={m.id} className={`tag ${!c || !usable ? 'tag-err' : cooling ? 'tag-warn' : 'tag-ok'}`} title={title}>
                <span className="dot" />
                {credLabel(c, m.auth_id)}
                <button className="tag-x" disabled={!!busy} onClick={() => void run('rm', async () => { await api.removeCredentialGroupMember(m.id); await reload(); })} aria-label="remove">
                  <IconX />
                </button>
              </span>
            );
          })}
          {candidates.length > 0 && (
            <span className="row" style={{ gap: 4, flexWrap: 'nowrap' }}>
              <select className="input input-sm" style={{ width: 240 }} value={adding} onChange={(e) => setAdding(e.target.value)} data-testid={`cg-add-select-${g.id}`}>
                <option value="">{tr('添加凭据…', 'Add credential…')}</option>
                {candidates.map((c) => (
                  <option key={c.id} value={c.id}>
                    {credLabel(c)} · {c.provider}
                  </option>
                ))}
              </select>
              <button className="btn btn-sm" disabled={!adding || !!busy} onClick={() => void run('add', async () => { await api.addCredentialGroupMember(g.id, adding); setAdding(''); await reload(); })} data-testid={`cg-add-${g.id}`}>
                <IconPlus />
              </button>
            </span>
          )}
        </div>
        <div className="faint small">
          {keys.length
            ? tr(`绑定的访问密钥：${keys.map(keyLabel).join('、')}`, `Bound keys: ${keys.map(keyLabel).join(', ')}`)
            : tr('尚无访问密钥绑定此分组', 'No access key is bound to this group')}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- model groups

function ModelGroupsTab({ d, reload }: { d: GroupData; reload: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [naming, setNaming] = useState<ModelGroup | 'new' | null>(null);

  const remove = async (g: ModelGroup) => {
    const keys = boundKeys(d.keys, 'model_groups', g.id);
    const ok = await confirm({
      title: tr(`删除模型分组「${g.group_name}」`, `Delete model group "${g.group_name}"`),
      body: keys.length
        ? tr(
            `以下密钥绑定了该分组，删除后将自动解绑：${keys.map(keyLabel).join('、')}。若这些密钥没有其他模型分组，将恢复为可调用全部模型。`,
            `These keys are bound to it and will be unbound: ${keys.map(keyLabel).join(', ')}. Keys left without groups may call every model again.`,
          )
        : tr('分组内的模型条目会一并删除。', 'Its model entries are removed too.'),
      danger: true,
      confirmText: tr('删除', 'Delete'),
    });
    if (!ok) return;
    await run(`del-mg-${g.id}`, async () => {
      for (const k of keys) await api.setAccessKeyScope(k.id, { model_groups: (k.model_groups ?? []).filter((x) => x !== g.id) });
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
        title={tr(`模型分组（${d.modelGroups.length}）`, `Model groups (${d.modelGroups.length})`)}
        extra={
          <button className="btn btn-sm btn-primary" onClick={() => setNaming('new')} data-testid="create-model-group">
            <IconPlus />
            {tr('新建分组', 'New group')}
          </button>
        }
      >
        {d.modelGroups.length === 0 ? (
          <Empty>{tr('还没有模型分组。', 'No model groups yet.')}</Empty>
        ) : (
          <div className="group-list">
            {d.modelGroups.map((g) => (
              <ModelGroupRow key={g.id} g={g} d={d} reload={reload} onRename={() => setNaming(g)} onDelete={() => void remove(g)} deleting={busy === `del-mg-${g.id}`} />
            ))}
          </div>
        )}
      </Card>
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

function ModelGroupRow({ g, d, reload, onRename, onDelete, deleting }: { g: ModelGroup; d: GroupData; reload: () => Promise<void>; onRename: () => void; onDelete: () => void; deleting: boolean }) {
  const api = useApi();
  const { tr } = useI18n();
  const { busy, run } = useAction();
  const [model, setModel] = useState('');
  const [editing, setEditing] = useState<ModelGroupMember | null>(null);
  const members = d.modelMembers.filter((m) => m.model_group_id === g.id);
  const keys = boundKeys(d.keys, 'model_groups', g.id);
  const cgName = new Map(d.credentialGroups.map((c) => [c.id, c.channel_name]));
  const known = new Set(d.modelIds.map((x) => x.toLowerCase()));
  const listId = `mg-models-${g.id}`;
  const add = () =>
    run('add', async () => {
      const id = model.trim();
      if (!id || members.some((m) => m.model_id.toLowerCase() === id.toLowerCase())) return;
      await api.addModelGroupMember(g.id, id);
      setModel('');
      await reload();
    });

  return (
    <div className={`group-row ${g.enabled ? '' : 'row-muted'}`} data-testid={`mg-${g.id}`}>
      <div className="group-head">
        <strong className="group-name">{g.group_name}</strong>
        <Badge kind={g.enabled ? 'primary' : undefined}>{g.enabled ? tr(`${members.length} 个模型`, `${members.length} models`) : tr('已停用', 'Disabled')}</Badge>
        <span className="spacer" />
        <span title={g.enabled ? tr('启用中（停用后此分组不再授予任何模型）', 'Enabled (disabled groups grant nothing)') : tr('已停用', 'Disabled')}>
          <Switch checked={g.enabled} disabled={!!busy} onChange={(v) => void run('toggle', async () => { await api.updateModelGroup(g.id, { enabled: v }); await reload(); })} />
        </span>
        <button className="btn btn-sm btn-ghost" onClick={onRename} title={tr('重命名', 'Rename')}>
          <IconEdit />
        </button>
        <button className="btn btn-sm btn-ghost btn-danger" onClick={onDelete} disabled={deleting} title={tr('删除', 'Delete')} data-testid={`mg-delete-${g.id}`}>
          {deleting ? <Spinner /> : <IconTrash />}
        </button>
      </div>
      <div className="group-body">
        <div className="tag-list">
          {members.length === 0 && <span className="faint small">{tr('还没有模型（绑定此分组的密钥将无模型可用）', 'No models (bound keys can call nothing)')}</span>}
          {members.map((m) => {
            const ch = m.channels ?? [];
            const unknown = !known.has(m.model_id.toLowerCase());
            return (
              <span key={m.id} className={`tag ${unknown ? 'tag-warn' : ''}`} title={unknown ? tr('当前凭据目录中没有此模型', 'Not in the current catalogs') : undefined}>
                <span className="mono">{m.model_id}</span>
                <button className="tag-sub" onClick={() => setEditing(m)} title={tr('限定凭据分组', 'Limit to credential groups')}>
                  {ch.length ? `↳ ${ch.map((id) => cgName.get(id) ?? `#${id}`).join('、')}` : tr('全部凭据', 'all creds')}
                </button>
                <button className="tag-x" disabled={!!busy} onClick={() => void run('rm', async () => { await api.removeModelGroupMember(m.id); await reload(); })} aria-label="remove">
                  <IconX />
                </button>
              </span>
            );
          })}
          <span className="row" style={{ gap: 4, flexWrap: 'nowrap' }}>
            <input
              className="input input-sm mono"
              style={{ width: 240 }}
              list={listId}
              placeholder={tr('添加模型 ID…', 'Add model id…')}
              value={model}
              onChange={(e) => setModel(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void add()}
              data-testid={`mg-add-input-${g.id}`}
            />
            <datalist id={listId}>
              {d.modelIds.filter((id) => !members.some((m) => m.model_id === id)).map((id) => (
                <option key={id} value={id} />
              ))}
            </datalist>
            <button className="btn btn-sm" disabled={!model.trim() || !!busy} onClick={() => void add()} data-testid={`mg-add-${g.id}`}>
              <IconPlus />
            </button>
          </span>
        </div>
        <div className="faint small">
          {keys.length
            ? tr(`绑定的访问密钥：${keys.map(keyLabel).join('、')}`, `Bound keys: ${keys.map(keyLabel).join(', ')}`)
            : tr('尚无访问密钥绑定此分组', 'No access key is bound to this group')}
        </div>
      </div>
      {editing && (
        <ModelChannelsModal
          m={editing}
          d={d}
          onClose={() => setEditing(null)}
          onSave={async (channels) => {
            await api.updateModelGroupMember(editing.id, channels);
            setEditing(null);
            await reload();
          }}
        />
      )}
    </div>
  );
}

function ModelChannelsModal({ m, d, onClose, onSave }: { m: ModelGroupMember; d: GroupData; onClose: () => void; onSave: (channels: number[]) => Promise<void> }) {
  const { t, tr } = useI18n();
  const { busy, run } = useAction();
  const [sel, setSel] = useState<number[]>(m.channels ?? []);
  return (
    <Modal
      title={tr(`限定 ${m.model_id} 的凭据分组`, `Credential groups for ${m.model_id}`)}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {t('cancel')}
          </button>
          <button className="btn btn-primary" disabled={!!busy} onClick={() => void run('save', () => onSave(sel))}>
            {busy && <Spinner />}
            {t('save')}
          </button>
        </>
      }
    >
      <p className="hint">
        {tr(
          '不选 = 由密钥可用的全部凭据服务。选择后，此分组授予的该模型只会路由到所选凭据分组（并与密钥自身绑定的凭据分组取交集，交集为空时请求失败）。',
          'None = served by every credential the key may use. When set, this model is routed only to the chosen credential groups (intersected with the key’s own groups; an empty intersection fails).',
        )}
      </p>
      <GroupChecklist groups={d.credentialGroups.map((g) => ({ id: g.id, name: g.channel_name, enabled: g.enabled, extra: availabilityText(g.id, d, tr) }))} value={sel} onChange={setSel} />
    </Modal>
  );
}

// ---------------------------------------------------------------- shared bits

export function availabilityText(groupId: number, d: Pick<GroupData, 'credentialMembers' | 'creds'>, tr: (zh: string, en: string) => string) {
  const a = credentialGroupAvailability(groupId, d.credentialMembers as CredentialGroupMember[], d.creds);
  if (a.total === 0) return tr('无成员', 'no members');
  return tr(`当前可用 ${a.usable}/${a.total}`, `usable ${a.usable}/${a.total}`);
}

export function GroupChecklist({ groups, value, onChange }: { groups: { id: number; name: string; enabled: boolean; extra?: string }[]; value: number[]; onChange: (v: number[]) => void }) {
  const { tr } = useI18n();
  if (groups.length === 0) return <div className="faint small">{tr('还没有分组，请先在“分组与权限”页面创建。', 'No groups yet – create them on the Groups page.')}</div>;
  return (
    <div className="checklist">
      {groups.map((g) => (
        <label key={g.id} className={`check ${g.enabled ? '' : 'faint'}`}>
          <input type="checkbox" checked={value.includes(g.id)} onChange={(e) => onChange(e.target.checked ? [...value, g.id] : value.filter((x) => x !== g.id))} />
          <span>{g.name}</span>
          {!g.enabled && <span className="small"> · {tr('已停用', 'disabled')}</span>}
          {g.extra && <span className="faint small"> · {g.extra}</span>}
        </label>
      ))}
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

/** Loads only what the access-key and credentials pages need for group pickers. */
export function useGroupLookup() {
  const api = useApi();
  const res = useAsync(
    async () => {
      const [cg, cgm, mg] = await Promise.all([api.credentialGroups(), api.credentialGroupMembers(), api.modelGroups()]);
      return { credentialGroups: cg.channel_groups ?? [], credentialMembers: cgm.channel_group_details ?? [], modelGroups: mg.model_groups ?? [] };
    },
    [],
  );
  const maps = useMemo(
    () => ({
      cgNames: new Map((res.data?.credentialGroups ?? []).map((g) => [g.id, g.channel_name])),
      mgNames: new Map((res.data?.modelGroups ?? []).map((g) => [g.id, g.group_name])),
    }),
    [res.data],
  );
  return { ...res, ...maps };
}
