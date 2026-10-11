import { useEffect, useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { coolingModels, credentialUsable, isSupportedProvider, type Credential, type CredentialGroupSummary, type CredentialQuery, type CredentialSearchItem, type ModelInfo, type QuotaItem } from '../api/client';
import { CredentialFilterBar, PageNav, SelectionBar, useSelection } from '../components/picker';
import { downloadBlob, fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Drawer, Empty, ErrorBox, Loading, Modal, Spinner, Switch } from '../components/ui';
import { QuotaStatusBadge, QuotaWindows } from '../components/quota';
import { IconDownload, IconPlus, IconRefresh, IconTrash } from '../components/icons';
import { AddCredentialModal } from './AddCredential';
import { GroupChecklist, useGroupLookup } from './Groups';

export function credentialStatus(c: Credential, t: ReturnType<typeof useI18n>['t'], tr: ReturnType<typeof useI18n>['tr']) {
  if (c.disabled) return <Badge>{t('disabled')}</Badge>;
  if (c.unavailable || c.credential_scope_error) return <Badge kind="warn" dot>{tr('整体冷却/不可用', 'Cooling down')}</Badge>;
  const cooling = coolingModels(c).length;
  if (credentialUsable(c) && cooling > 0)
    return (
      <Badge kind="warn" dot>
        {tr(`可用 · ${cooling} 个模型冷却`, `Active · ${cooling} model(s) cooling`)}
      </Badge>
    );
  if (credentialUsable(c) && (c.status === 'active' || c.status === 'error')) return <Badge kind="ok" dot>{tr('可用', 'Active')}</Badge>;
  return (
    <Badge kind="err" dot>
      {c.status || 'unknown'}
    </Badge>
  );
}

export function CredentialsPage() {
  const api = useApi();
  const { t, tr, lang } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [filter, setFilter] = useState<CredentialQuery>({});
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);
  useEffect(() => setPage(1), [filter]);
  const creds = useAsync(() => api.searchCredentials({ ...filter, page, page_size: size }), [filter, page, size], 20000);
  const groups = useGroupLookup();
  const quota = useAsync(() => api.quotaCredentials({ limit: 500 }), [], 60000);
  const sel = useSelection<string>();
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<CredentialSearchItem | null>(null);
  const [bulk, setBulk] = useState<null | 'add_to_group' | 'remove_from_group' | 'add_tags' | 'remove_tags'>(null);

  const items = useMemo(() => (creds.data?.items ?? []).filter((c) => isSupportedProvider(c.provider)), [creds.data]);
  const quotaById = useMemo(() => new Map<string, QuotaItem>((quota.data?.items ?? []).map((q) => [q.credential_id, q])), [quota.data]);
  const current = selected ? (items.find((c) => c.id === selected.id) ?? selected) : undefined;
  const reloadAll = async () => {
    await creds.reload();
    await groups.reload();
  };

  const toggle = (c: Credential, enabled: boolean) =>
    run(`toggle-${c.id}`, async () => {
      await api.setCredentialDisabled(c.id, !enabled);
      toast(enabled ? tr('已启用', 'Enabled') : tr('已禁用', 'Disabled'));
      await creds.reload();
    });
  const bulkToggle = (action: 'enable' | 'disable') =>
    run(`bulk-${action}`, async () => {
      const r = await api.bulkCredentials({ ids: sel.list(), action });
      toast(tr(`已${action === 'enable' ? '启用' : '禁用'} ${r.updated ?? 0} 个${r.unchanged ? `（${r.unchanged} 个无需变更）` : ''}`, `${action}d ${r.updated ?? 0}${r.unchanged ? ` (${r.unchanged} unchanged)` : ''}`), r.failed?.length ? 'err' : 'ok');
      await reloadAll();
    });

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('credentials')}</h1>
          <p>{tr('Antigravity OAuth、AI Studio 与 Vertex 服务账号凭据。支持服务端筛选、跨页多选与批量操作；点击一行查看详情。', 'Antigravity OAuth, AI Studio and Vertex credentials. Server-side filters, cross-page multi-select and bulk actions; click a row for details.')}</p>
        </div>
        <div className="toolbar">
          <button
            className="btn"
            disabled={busy === 'refresh-all' || !creds.data?.all}
            onClick={() =>
              run('refresh-all', async () => {
                const res = await api.refreshAllCredentials();
                const failed = res.results?.filter((r) => !r.success) ?? [];
                toast(failed.length ? tr(`${failed.length} 个刷新失败`, `${failed.length} refresh(es) failed`) : tr('全部刷新完成', 'All refreshed'), failed.length ? 'err' : 'ok');
                await creds.reload();
              })
            }
          >
            {busy === 'refresh-all' ? <Spinner /> : <IconRefresh />}
            {tr('全部刷新令牌', 'Refresh all tokens')}
          </button>
          <button className="btn btn-primary" onClick={() => setAdding(true)} data-testid="add-credential">
            <IconPlus />
            {t('addCredential')}
          </button>
        </div>
      </div>

      <Card
        bodyClass="table-wrap"
        title={<CredentialFilterBar value={filter} onChange={setFilter} facets={creds.data?.facets} groups={groups.data?.credential_groups} />}
        extra={<span className="faint small nowrap">{tr(`${creds.data?.total ?? 0} / ${creds.data?.all ?? 0} 个`, `${creds.data?.total ?? 0} / ${creds.data?.all ?? 0}`)}</span>}
      >
        <SelectionBar selected={sel.size} total={creds.data?.total ?? 0} pageIds={items.map((c) => c.id)} sel={sel} loadAllIds={async () => (await api.credentialIds(filter)).ids}>
          {sel.size > 0 && (
            <div className="row bulk-actions" style={{ gap: 6 }} data-testid="bulk-actions">
              <button className="btn btn-sm" disabled={!!busy} onClick={() => setBulk('add_to_group')} data-testid="bulk-add-group">
                {tr('加入分组', 'Add to group')}
              </button>
              <button className="btn btn-sm" disabled={!!busy} onClick={() => setBulk('remove_from_group')}>
                {tr('移出分组', 'Remove from group')}
              </button>
              <button className="btn btn-sm" disabled={!!busy} onClick={() => setBulk('add_tags')} data-testid="bulk-add-tags">
                {tr('加标签', 'Add tags')}
              </button>
              <button className="btn btn-sm" disabled={!!busy} onClick={() => setBulk('remove_tags')}>
                {tr('去标签', 'Remove tags')}
              </button>
              <button className="btn btn-sm" disabled={!!busy} onClick={() => void bulkToggle('enable')}>
                {busy === 'bulk-enable' && <Spinner />}
                {tr('启用', 'Enable')}
              </button>
              <button className="btn btn-sm btn-danger" disabled={!!busy} onClick={() => void bulkToggle('disable')}>
                {busy === 'bulk-disable' && <Spinner />}
                {tr('禁用', 'Disable')}
              </button>
            </div>
          )}
        </SelectionBar>
        {creds.error ? (
          <div className="card-body">
            <ErrorBox error={creds.error} onRetry={creds.reload} />
          </div>
        ) : !creds.data ? (
          <Loading />
        ) : items.length === 0 ? (
          <Empty>{creds.data.all ? tr('没有匹配的凭据', 'No matching credentials') : tr('还没有凭据，点击右上角“添加凭据”。', 'No credentials yet — click "Add credential".')}</Empty>
        ) : (
          <table className="table" data-testid="credentials-table">
            <thead>
              <tr>
                <th style={{ width: 28 }} />
                <th>{tr('账号', 'Account')}</th>
                <th>{t('provider')}</th>
                <th>{t('status')}</th>
                <th className="hide-sm">{t('quota')}</th>
                <th className="hide-sm">{t('priority')}</th>
                <th className="hide-sm">{t('updated')}</th>
                <th style={{ textAlign: 'right' }}>{t('enabled')}</th>
              </tr>
            </thead>
            <tbody>
              {items.map((c) => (
                <tr key={c.id} className={`clickable ${sel.has(c.id) ? 'row-selected' : ''}`} onClick={() => setSelected(c)}>
                  <td onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" checked={sel.has(c.id)} onChange={() => sel.toggle(c.id)} aria-label="select" />
                  </td>
                  <td>
                    <div className="ellipsis" style={{ maxWidth: 320 }} title={c.label || c.email || c.name}>
                      {c.label || c.email || c.name}
                    </div>
                    <div className="faint small mono ellipsis" style={{ maxWidth: 320 }}>
                      {c.note ? `📝 ${c.note}` : c.id}
                    </div>
                    {((c.group_ids?.length ?? 0) > 0 || (c.tags?.length ?? 0) > 0) && (
                      <div className="scope-chips small" style={{ marginTop: 3 }}>
                        {(c.group_ids ?? []).slice(0, 4).map((id) => (
                          <span key={`g${id}`} className="tag" title={tr('所属凭据分组', 'Credential group')}>
                            🔑 {groups.cgNames.get(id) ?? `#${id}`}
                          </span>
                        ))}
                        {(c.group_ids?.length ?? 0) > 4 && <span className="faint">+{(c.group_ids?.length ?? 0) - 4}</span>}
                        {(c.tags ?? []).map((tg) => (
                          <span key={`t${tg}`} className="tag tag-soft" title={tr('标签', 'Tag')}>
                            #{tg}
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                  <td>
                    <Badge kind="primary">{c.provider}</Badge>
                  </td>
                  <td>
                    <div className="stack" style={{ gap: 4 }}>
                      {credentialStatus(c, t, tr)}
                      {coolingModels(c).length > 0 ? (
                        <span className="faint small ellipsis" style={{ maxWidth: 220 }} title={coolingModels(c).map((m) => m.model).join(', ')}>
                          {coolingModels(c).map((m) => m.model).join(', ')}
                        </span>
                      ) : (
                        c.credential_scope_error && c.status_message && <span className="faint small ellipsis" style={{ maxWidth: 200 }} title={c.status_message}>{c.status_message}</span>
                      )}
                    </div>
                  </td>
                  <td className="hide-sm">
                    <QuotaWindows item={quotaById.get(c.id)} compact />
                  </td>
                  <td className="hide-sm">{c.priority ?? <span className="faint">0</span>}</td>
                  <td className="hide-sm nowrap faint small">{fmtRelative(c.updated_at, lang)}</td>
                  <td className="actions" onClick={(e) => e.stopPropagation()}>
                    <Switch checked={!c.disabled} disabled={busy === `toggle-${c.id}`} onChange={(v) => toggle(c, v)} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <PageNav page={page} pageSize={size} total={creds.data?.total ?? 0} onPage={setPage} onPageSize={(n) => (setSize(n), setPage(1))} />
      </Card>

      {bulk && (
        <BulkCredentialModal
          action={bulk}
          ids={sel.list()}
          groups={groups.data?.credential_groups ?? []}
          tagFacets={Object.keys(creds.data?.facets.tag ?? {})}
          onClose={() => setBulk(null)}
          onDone={async () => {
            setBulk(null);
            await reloadAll();
          }}
        />
      )}
      {adding && (
        <AddCredentialModal
          onClose={() => setAdding(false)}
          onAdded={() => {
            void creds.reload();
            void quota.reload();
          }}
        />
      )}
      {current && (
        <CredentialDrawer
          cred={current}
          quota={quotaById.get(current.id)}
          onClose={() => setSelected(null)}
          onChanged={async () => {
            await reloadAll();
            await quota.reload();
          }}
          onDeleted={async () => {
            setSelected(null);
            await reloadAll();
          }}
        />
      )}
    </div>
  );
}

function BulkCredentialModal({
  action,
  ids,
  groups,
  tagFacets,
  onClose,
  onDone,
}: {
  action: 'add_to_group' | 'remove_from_group' | 'add_tags' | 'remove_tags';
  ids: string[];
  groups: CredentialGroupSummary[];
  tagFacets: string[];
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [groupId, setGroupId] = useState(0);
  const [tags, setTags] = useState('');
  const isGroup = action.endsWith('group');
  const titles: Record<typeof action, [string, string]> = {
    add_to_group: [`将 ${ids.length} 个凭据加入分组`, `Add ${ids.length} credentials to a group`],
    remove_from_group: [`将 ${ids.length} 个凭据移出分组`, `Remove ${ids.length} credentials from a group`],
    add_tags: [`为 ${ids.length} 个凭据添加标签`, `Tag ${ids.length} credentials`],
    remove_tags: [`从 ${ids.length} 个凭据移除标签`, `Untag ${ids.length} credentials`],
  };
  const tagList = tags.split(/[,，\s]+/).map((x) => x.trim().toLowerCase()).filter(Boolean);
  const valid = isGroup ? groupId > 0 : tagList.length > 0;
  const apply = () =>
    run('apply', async () => {
      const r = await api.bulkCredentials(isGroup ? { ids, action, group_id: groupId } : { ids, action, tags: tagList });
      if (r.result) {
        toast(
          action === 'add_to_group'
            ? tr(`已加入 ${r.result.added} 个${r.result.unchanged ? `（${r.result.unchanged} 个原本就在组内）` : ''}`, `Added ${r.result.added}${r.result.unchanged ? ` (${r.result.unchanged} already in)` : ''}`)
            : tr(`已移出 ${r.result.removed} 个（规则匹配的成员需修改规则）`, `Removed ${r.result.removed} (rule members follow the rule)`),
        );
      } else {
        toast(tr(`已更新 ${r.updated ?? 0} 个`, `Updated ${r.updated ?? 0}`), r.failed?.length ? 'err' : 'ok');
      }
      await onDone();
    });
  const target = groups.find((g) => g.id === groupId);
  return (
    <Modal
      title={tr(...titles[action])}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('取消', 'Cancel')}
          </button>
          <button className="btn btn-primary" disabled={!valid || !!busy} onClick={() => void apply()} data-testid="bulk-apply">
            {busy && <Spinner />}
            {tr('应用', 'Apply')}
          </button>
        </>
      }
    >
      {isGroup ? (
        <div className="stack">
          <select className="select" value={groupId} onChange={(e) => setGroupId(Number(e.target.value))} data-testid="bulk-group">
            <option value={0}>{tr('选择凭据分组…', 'Choose a credential group…')}</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.channel_name} · {tr(`${g.total} 个成员`, `${g.total} members`)}
              </option>
            ))}
          </select>
          {target && target.keys > 0 && (
            <div className="alert warn small">
              {tr(`有 ${target.keys} 个访问密钥绑定此分组，变更会立即影响它们可使用的凭据（无回退）。`, `${target.keys} keys are bound to this group; the change affects them immediately (no fallback).`)}
            </div>
          )}
          <p className="hint">{tr('在一个事务中完成；已在组内的凭据会被跳过。', 'Runs in one transaction; existing members are skipped.')}</p>
        </div>
      ) : (
        <div className="field">
          <label>{tr('标签（逗号分隔，小写）', 'Tags (comma separated)')}</label>
          <input className="input" list="bulk-tags" value={tags} autoFocus onChange={(e) => setTags(e.target.value)} placeholder="vip, eu" data-testid="bulk-tags" />
          <datalist id="bulk-tags">
            {tagFacets.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
          <span className="hint">{tr('标签可用于筛选，以及凭据分组的自动加入规则。', 'Tags drive filters and credential-group auto-join rules.')}</span>
        </div>
      )}
    </Modal>
  );
}

function CredentialDrawer({ cred, quota, onClose, onChanged, onDeleted }: { cred: CredentialSearchItem; quota?: QuotaItem; onClose: () => void; onChanged: () => Promise<void>; onDeleted: () => Promise<void> }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [form, setForm] = useState({
    priority: cred.priority?.toString() ?? '',
    prefix: cred.prefix ?? '',
    proxy_url: cred.proxy_url ?? '',
    note: cred.note ?? '',
    requestRetry: cred['request-retry']?.toString() ?? '',
    cooling: cred['disable-cooling'] === undefined ? 'inherit' : cred['disable-cooling'] ? 'off' : 'on',
  });
  const models = useAsync<ModelInfo[]>(() => api.credentialModels(cred.id).then((r) => r.models ?? []), [cred.id]);

  const save = () =>
    run('save', async () => {
      const fields: Record<string, unknown> = {
        prefix: form.prefix.trim(),
        proxy_url: form.proxy_url.trim(),
        note: form.note.trim(),
        'request-retry': form.requestRetry.trim() === '' ? null : Number(form.requestRetry),
        'disable-cooling': form.cooling === 'inherit' ? null : form.cooling === 'off',
      };
      if (form.priority.trim() !== '') fields.priority = Number(form.priority);
      await api.updateCredentialFields(cred.id, fields);
      toast(t('saved'));
      await onChanged();
    });

  const actions: { id: string; label: string; fn: () => Promise<unknown>; done: string }[] = [
    { id: 'refresh', label: t('refreshToken'), fn: () => api.refreshCredential(cred.id), done: tr('令牌已刷新', 'Token refreshed') },
    { id: 'quota', label: t('fetchQuota'), fn: () => api.fetchQuota(cred.id), done: tr('已开始查询额度，稍后自动更新', 'Quota collection queued') },
    { id: 'cooldown', label: t('resetCooldown'), fn: () => api.resetCooldown(cred.id), done: tr('冷却已清除', 'Cooldown cleared') },
  ];

  return (
    <Drawer title={cred.label || cred.email || cred.name} onClose={onClose}>
      <div className="stack">
        <div className="row">
          <Badge kind="primary">{cred.provider}</Badge>
          {credentialStatus(cred, t, tr)}
          <QuotaStatusBadge item={quota} />
        </div>
        {cred.credential_scope_error && cred.status_message && <div className="alert warn small">{cred.status_message}</div>}
        <ModelCooldownList cred={cred} />

        <dl className="kv small">
          <dt>ID</dt>
          <dd className="mono">{cred.id}</dd>
          <dt>{tr('文件名', 'File')}</dt>
          <dd className="mono">{cred.name}</dd>
          <dt>{tr('来源', 'Source')}</dt>
          <dd>{cred.source ?? '—'}</dd>
          <dt>{t('created')}</dt>
          <dd>{fmtTime(cred.created_at)}</dd>
          <dt>{t('updated')}</dt>
          <dd>{fmtTime(cred.updated_at)}</dd>
          {quota?.observed_at && (
            <>
              <dt>{tr('额度采集', 'Quota observed')}</dt>
              <dd>
                {fmtTime(quota.observed_at)} {quota.freshness === 'stale' && <Badge kind="warn">{tr('已过期', 'stale')}</Badge>}
              </dd>
            </>
          )}
        </dl>

        <div className="row">
          {actions.map((a) => (
            <button
              key={a.id}
              className="btn btn-sm"
              disabled={!!busy}
              onClick={() =>
                run(a.id, async () => {
                  await a.fn();
                  toast(a.done);
                  await onChanged();
                })
              }
            >
              {busy === a.id && <Spinner />}
              {a.label}
            </button>
          ))}
          <button
            className="btn btn-sm"
            onClick={() =>
              run('download', async () => {
                const blob = await api.downloadCredential(cred.id);
                downloadBlob(blob, cred.name.endsWith('.json') ? cred.name : `${cred.name}.json`);
              })
            }
          >
            <IconDownload />
            {t('download')}
          </button>
        </div>

        <div>
          <h3 style={{ marginBottom: 8 }}>{t('quota')}</h3>
          <QuotaWindows item={quota} />
        </div>

        <CredentialGroupsEditor cred={cred} onChanged={onChanged} />

        <div>
          <h3 style={{ marginBottom: 8 }}>{tr('路由设置', 'Routing settings')}</h3>
          <div className="form-grid">
            <div className="field">
              <label>{t('priority')}</label>
              <input className="input" type="number" value={form.priority} onChange={(e) => setForm({ ...form, priority: e.target.value })} placeholder="0" />
              <span className="hint">{tr('数值越大越优先被选择', 'Higher values are picked first')}</span>
            </div>
            <div className="field">
              <label>{t('prefix')}</label>
              <input className="input" value={form.prefix} onChange={(e) => setForm({ ...form, prefix: e.target.value })} placeholder="team-a" />
              <span className="hint">{tr('设置后通过 “前缀/模型” 访问', 'Models become "prefix/model"')}</span>
            </div>
            <div className="field">
              <label>{tr('额外重试轮数', 'Request retry')}</label>
              <input className="input" type="number" min={0} value={form.requestRetry} onChange={(e) => setForm({ ...form, requestRetry: e.target.value })} placeholder={tr('继承全局', 'inherit')} />
            </div>
            <div className="field">
              <label>{tr('冷却', 'Cooldown')}</label>
              <select className="select" value={form.cooling} onChange={(e) => setForm({ ...form, cooling: e.target.value })}>
                <option value="inherit">{tr('继承全局设置', 'Inherit global')}</option>
                <option value="on">{tr('出错时冷却', 'Cool down on errors')}</option>
                <option value="off">{tr('不冷却', 'Never cool down')}</option>
              </select>
            </div>
            <div className="field full">
              <label>{t('proxyUrl')}</label>
              <input className="input" value={form.proxy_url} onChange={(e) => setForm({ ...form, proxy_url: e.target.value })} placeholder="socks5://user:pass@host:1080" />
            </div>
            <div className="field full">
              <label>{t('note')}</label>
              <input className="input" value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />
            </div>
          </div>
          <div className="row" style={{ marginTop: 12 }}>
            <button className="btn btn-primary" disabled={busy === 'save'} onClick={save}>
              {busy === 'save' && <Spinner />}
              {t('save')}
            </button>
          </div>
        </div>

        <div>
          <h3 style={{ marginBottom: 8 }}>
            {tr('可用模型', 'Models')} {models.data && <span className="faint">({models.data.length})</span>}
          </h3>
          {models.error ? <ErrorBox error={models.error} /> : !models.data ? <Loading /> : models.data.length ? models.data.map((m) => <span key={m.id} className="tag" title={m.display_name}>{m.id}</span>) : <span className="faint">—</span>}
        </div>

        <div className="card" style={{ borderColor: 'var(--err)' }}>
          <div className="card-body row">
            <div className="spacer">
              <b>{t('dangerZone')}</b>
              <div className="faint small">{tr('删除后无法恢复（可先下载备份）。', 'Deletion is permanent (download a backup first).')}</div>
            </div>
            <button
              className="btn btn-danger"
              data-testid="delete-credential"
              onClick={async () => {
                const ok = await confirm({
                  title: tr('删除凭据', 'Delete credential'),
                  body: tr(`将永久删除 ${cred.label || cred.name}。`, `This permanently deletes ${cred.label || cred.name}.`),
                  danger: true,
                  confirmText: t('delete'),
                  typeToConfirm: 'DELETE',
                });
                if (!ok) return;
                await run('delete', async () => {
                  await api.deleteCredential(cred.id);
                  toast(tr('已删除', 'Deleted'));
                  await onDeleted();
                });
              }}
            >
              <IconTrash />
              {t('delete')}
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}

function ModelCooldownList({ cred }: { cred: Credential }) {
  const { tr, lang } = useI18n();
  const items = cred.model_cooldowns ?? [];
  if (!items.length) return null;
  return (
    <div className="stack" style={{ gap: 6 }} data-testid="model-cooldowns">
      <div className="small strong">{tr('按模型的冷却 / 最近错误（仅影响对应模型）', 'Per-model cooldown / recent errors (only that model is affected)')}</div>
      <table className="table small">
        <thead>
          <tr>
            <th>{tr('模型', 'Model')}</th>
            <th>{tr('状态', 'State')}</th>
            <th>{tr('恢复时间', 'Retry at')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((m) => (
            <tr key={m.model}>
              <td className="mono">{m.model}</td>
              <td>
                <Badge kind={m.cooling ? 'warn' : undefined} dot>
                  {m.cooling ? (m.quota_exceeded ? tr('额度耗尽冷却', 'Quota cooldown') : tr('冷却中', 'Cooling')) : tr('最近失败', 'Recent error')}
                  {m.http_status ? ` · ${m.http_status}` : ''}
                </Badge>
                {m.status_message && (
                  <div className="faint small ellipsis" style={{ maxWidth: 280 }} title={m.status_message}>
                    {m.status_message}
                  </div>
                )}
              </td>
              <td className="small">{m.next_retry_after ? `${fmtTime(m.next_retry_after)} (${fmtRelative(m.next_retry_after, lang)})` : '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CredentialGroupsEditor({ cred, onChanged }: { cred: CredentialSearchItem; onChanged: () => Promise<void> }) {
  const api = useApi();
  const { tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const groups = useGroupLookup();
  const d = groups.data;
  const [value, setValue] = useState<number[]>(cred.group_ids ?? []);
  useEffect(() => setValue(cred.group_ids ?? []), [cred.group_ids]);
  if (!d) return null;
  const change = (next: number[]) =>
    run('groups', async () => {
      let ruleKept = 0;
      for (const id of next.filter((x) => !value.includes(x))) await api.bulkCredentialGroupMembers(id, { add: [cred.id] });
      for (const id of value.filter((x) => !next.includes(x))) {
        const r = await api.bulkCredentialGroupMembers(id, { remove: [cred.id] });
        if (r.result.removed === 0) ruleKept++;
      }
      if (ruleKept) toast(tr('该凭据由分组规则自动加入，需修改规则才能移出。', 'This credential joined via a group rule; change the rule to remove it.'), 'err');
      setValue(next);
      await groups.reload();
      await onChanged();
    });
  return (
    <div data-testid="cred-groups-editor">
      <h3 style={{ marginBottom: 8 }}>
        {tr('凭据分组', 'Credential groups')} {busy === 'groups' && <Spinner />}
      </h3>
      <GroupChecklist
        groups={d.credential_groups.map((g) => ({ id: g.id, name: g.channel_name, enabled: g.enabled, extra: g.has_rule ? tr('含规则', 'has rule') : undefined }))}
        value={value}
        onChange={(v) => void change(v)}
      />
      <p className="hint" style={{ marginTop: 6 }}>
        {tr('绑定了凭据分组的访问密钥只会使用分组内的凭据。凭据可同时属于多个分组；不属于任何分组的凭据只服务未绑定分组的密钥。', 'Keys bound to credential groups only use members. A credential may be in several groups; ungrouped credentials only serve keys without groups.')}
      </p>
      <TagsEditor cred={cred} onChanged={onChanged} />
    </div>
  );
}

function TagsEditor({ cred, onChanged }: { cred: CredentialSearchItem; onChanged: () => Promise<void> }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const [text, setText] = useState((cred.tags ?? []).join(', '));
  useEffect(() => setText((cred.tags ?? []).join(', ')), [cred.tags]);
  const tags = Array.from(new Set(text.split(/[,，\s]+/).map((x) => x.trim().toLowerCase()).filter(Boolean))).sort();
  const dirty = tags.join(',') !== (cred.tags ?? []).join(',');
  return (
    <div className="field" style={{ marginTop: 12 }} data-testid="cred-tags-editor">
      <label>{tr('标签（逗号分隔）', 'Tags (comma separated)')}</label>
      <div className="row" style={{ gap: 6, flexWrap: 'nowrap' }}>
        <input className="input" value={text} onChange={(e) => setText(e.target.value)} placeholder="vip, eu" data-testid="cred-tags-input" />
        <button
          className="btn"
          disabled={!dirty || !!busy}
          data-testid="cred-tags-save"
          onClick={() =>
            void run('tags', async () => {
              await api.bulkCredentials({ ids: [cred.id], action: 'set_tags', tags });
              toast(t('saved'));
              await onChanged();
            })
          }
        >
          {busy === 'tags' && <Spinner />}
          {t('save')}
        </button>
      </div>
      <span className="hint">{tr('用于筛选与分组自动加入规则。', 'Used by filters and group auto-join rules.')}</span>
    </div>
  );
}
