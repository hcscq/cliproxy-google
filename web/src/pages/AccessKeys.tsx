import { useEffect, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { generateApiKey, keyScopeSummary, maskSecret, type AccessKey, type KeyQuery } from '../api/client';
import { PageNav, SelectionBar, useDebounced, useSelection } from '../components/picker';
import { availabilityLabel } from './GroupMembers';
import { GroupChecklist, useGroupLookup } from './Groups';
import { fmtCompact, fmtRelative } from '../format';
import { useI18n } from '../i18n';
import { Card, CopyButton, Empty, ErrorBox, Loading, Modal, Spinner } from '../components/ui';
import { IconEdit, IconPlus, IconRefresh, IconTrash } from '../components/icons';

const PROXY_URL = 'http://cliproxy.cliproxy-google.svc.cluster.local:8317/v1';

export function AccessKeysPage() {
  const api = useApi();
  const { t, tr, lang } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const [q, setQ] = useState('');
  const dq = useDebounced(q);
  const [scope, setScope] = useState<KeyQuery['scope']>('');
  const [cg, setCg] = useState(0);
  const [mg, setMg] = useState(0);
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);
  const query: KeyQuery = { q: dq || undefined, scope: scope || undefined, channel: cg || undefined, model_group: mg || undefined };
  useEffect(() => setPage(1), [dq, scope, cg, mg]);
  const keys = useAsync(() => api.searchAccessKeys({ ...query, page, page_size: size }), [dq, scope, cg, mg, page, size]);
  const groups = useGroupLookup();
  const sel = useSelection<number>();
  const [bulk, setBulk] = useState(false);
  const usage = useAsync(() => api.usageOverview({ from: new Date(Date.now() - 30 * 86400e3).toISOString() }).catch(() => undefined), []);
  const [creating, setCreating] = useState(false);
  const [renaming, setRenaming] = useState<AccessKey | null>(null);
  const [revealed, setRevealed] = useState<Record<number, boolean>>({});

  const usageByKey = new Map((usage.data?.top?.client_keys ?? []).map((u) => [String(u.id), u]));
  const items = keys.data?.items ?? [];

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('accessKeys')}</h1>
          <p>{tr('下游客户端调用代理 API 时使用的密钥（Authorization: Bearer <key>）。代理 API 只在集群内部可访问。', 'Keys downstream clients use for the proxy API (Authorization: Bearer <key>). The proxy API is reachable only inside the cluster.')}</p>
        </div>
        <button className="btn btn-primary" onClick={() => setCreating(true)} data-testid="create-access-key">
          <IconPlus />
          {tr('创建密钥', 'Create key')}
        </button>
      </div>

      <Card>
        <div className="row">
          <span className="label">{tr('集群内代理地址', 'In-cluster proxy URL')}</span>
          <code className="spacer break">{PROXY_URL}</code>
          <CopyButton small text={PROXY_URL} />
        </div>
        <p className="hint" style={{ marginBottom: 0 }}>
          {tr('兼容 OpenAI（/v1/chat/completions、/v1/models）、Gemini（/v1beta/models/…:generateContent）与 Claude（/v1/messages）格式。', 'Speaks OpenAI (/v1/chat/completions, /v1/models), Gemini (/v1beta/models/…:generateContent) and Claude (/v1/messages) formats.')}
        </p>
      </Card>

      <Card
        bodyClass="table-wrap"
        title={
          <div className="filter-bar">
            <input className="input input-sm" style={{ width: 200 }} placeholder={tr('搜索名称 / 密钥', 'Search name / key')} value={q} onChange={(e) => setQ(e.target.value)} data-testid="key-q" />
            <select className="select input-sm" value={scope} onChange={(e) => setScope(e.target.value as KeyQuery['scope'])} data-testid="key-scope">
              <option value="">{tr('全部范围', 'Any scope')}</option>
              <option value="unrestricted">{tr('不受限', 'Unrestricted')}</option>
              <option value="restricted">{tr('已绑定分组', 'Restricted')}</option>
            </select>
            <select className="select input-sm" value={cg} onChange={(e) => setCg(Number(e.target.value))}>
              <option value={0}>{tr('任意凭据分组', 'Any credential group')}</option>
              {(groups.data?.credential_groups ?? []).map((g) => (
                <option key={g.id} value={g.id}>
                  🔑 {g.channel_name}
                </option>
              ))}
            </select>
            <select className="select input-sm" value={mg} onChange={(e) => setMg(Number(e.target.value))}>
              <option value={0}>{tr('任意模型分组', 'Any model group')}</option>
              {(groups.data?.model_groups ?? []).map((g) => (
                <option key={g.id} value={g.id}>
                  ▦ {g.group_name}
                </option>
              ))}
            </select>
          </div>
        }
        extra={
          <>
            <span className="faint small nowrap">{tr(`${keys.data?.total ?? 0} / ${keys.data?.all ?? 0} 个`, `${keys.data?.total ?? 0} / ${keys.data?.all ?? 0}`)}</span>
            <button className="btn btn-sm" onClick={keys.reload}>
              <IconRefresh />
              {t('refresh')}
            </button>
          </>
        }
      >
        <SelectionBar selected={sel.size} total={keys.data?.total ?? 0} pageIds={items.map((k) => k.id)} sel={sel} loadAllIds={async () => (await api.accessKeyIds(query)).ids}>
          {sel.size > 0 && (
            <button className="btn btn-sm btn-primary" onClick={() => setBulk(true)} data-testid="bulk-bind-keys">
              {tr(`批量绑定 / 解绑分组（${sel.size}）`, `Bulk bind / unbind (${sel.size})`)}
            </button>
          )}
        </SelectionBar>
        {keys.error ? (
          <div className="card-body">
            <ErrorBox error={keys.error} onRetry={keys.reload} />
          </div>
        ) : !keys.data ? (
          <Loading />
        ) : items.length === 0 ? (
          <Empty>{tr('还没有访问密钥。没有密钥时代理会拒绝所有请求。', 'No access keys. The proxy rejects all requests without one.')}</Empty>
        ) : (
          <table className="table" data-testid="access-keys-table">
            <thead>
              <tr>
                <th style={{ width: 28 }} />
                <th>{t('name')}</th>
                <th>{tr('密钥', 'Key')}</th>
                <th>{tr('可用范围', 'Scope')}</th>
                <th className="hide-sm">{tr('30 天请求', 'Requests (30d)')}</th>
                <th className="hide-sm">{tr('最后使用', 'Last used')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {items.map((k) => {
                const u = usageByKey.get(String(k.id));
                return (
                  <tr key={k.id} data-testid={`access-key-${k.id}`} className={sel.has(k.id) ? 'row-selected' : ''}>
                    <td>
                      <input type="checkbox" checked={sel.has(k.id)} onChange={() => sel.toggle(k.id)} aria-label="select" />
                    </td>
                    <td>
                      <div className="cell-title">{k.display_name || <span className="faint">{tr('未命名', 'Unnamed')}</span>}</div>
                      <div className="cell-sub">
                        <span className="mono" title={tr('密钥 ID', 'Key ID')}>#{k.id}</span>
                      </div>
                    </td>
                    <td>
                      <div className="row" style={{ gap: 6 }}>
                        <code className="break">{revealed[k.id] ? k.api_key : maskSecret(k.api_key, 6)}</code>
                        <button className="btn btn-sm btn-ghost" onClick={() => setRevealed({ ...revealed, [k.id]: !revealed[k.id] })}>
                          {revealed[k.id] ? tr('隐藏', 'Hide') : tr('显示', 'Show')}
                        </button>
                        <CopyButton small text={k.api_key} />
                      </div>
                    </td>
                    <td>
                      <ScopeCell k={k} cgNames={groups.cgNames} mgNames={groups.mgNames} />
                    </td>
                    <td className="hide-sm">{u ? fmtCompact(u.request_count) : <span className="faint">0</span>}</td>
                    <td className="hide-sm faint small">{u?.last_used_at ? fmtRelative(u.last_used_at, lang) : '—'}</td>
                    <td className="actions">
                      <button className="btn btn-sm btn-ghost" onClick={() => setRenaming(k)} title={tr('编辑名称与可用范围', 'Edit name & scope')} data-testid={`edit-access-key-${k.id}`}>
                        <IconEdit />
                      </button>
                      <button
                        className="btn btn-sm btn-ghost btn-danger"
                        data-testid={`delete-access-key-${k.id}`}
                        disabled={busy === `del-${k.id}`}
                        onClick={async () => {
                          const ok = await confirm({
                            title: tr('删除访问密钥', 'Delete access key'),
                            body: tr(`使用 ${maskSecret(k.api_key, 6)}${k.display_name ? `（${k.display_name}）` : ''} 的客户端将立即无法调用代理。`, `Clients using ${maskSecret(k.api_key, 6)}${k.display_name ? ` (${k.display_name})` : ''} will be rejected immediately.`),
                            danger: true,
                            confirmText: t('delete'),
                          });
                          if (!ok) return;
                          await run(`del-${k.id}`, async () => {
                            await api.deleteAccessKey(k.id);
                            toast(tr('已删除', 'Deleted'));
                            await keys.reload();
                          });
                        }}
                      >
                        <IconTrash />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        <PageNav page={page} pageSize={size} total={keys.data?.total ?? 0} onPage={setPage} onPageSize={(n) => (setSize(n), setPage(1))} />
      </Card>

      {bulk && (
        <BulkKeyScopeModal
          ids={sel.list()}
          onClose={() => setBulk(false)}
          onDone={async () => {
            setBulk(false);
            await keys.reload();
            await groups.reload();
          }}
        />
      )}
      {creating && <CreateKeyModal onClose={() => setCreating(false)} onCreated={keys.reload} />}
      {renaming && (
        <EditKeyModal
          k={renaming}
          onClose={() => setRenaming(null)}
          onSaved={async () => {
            setRenaming(null);
            await keys.reload();
          }}
        />
      )}
    </div>
  );
}

function CreateKeyModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => Promise<void> }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toastError } = useUi();
  const [name, setName] = useState('');
  const [key, setKey] = useState(() => generateApiKey());
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<AccessKey | null>(null);

  const submit = async () => {
    setBusy(true);
    try {
      const res = await api.createAccessKey(key.trim(), name.trim());
      setCreated(res.api_key);
      await onCreated();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={created ? tr('密钥已创建', 'Key created') : tr('创建访问密钥', 'Create access key')}
      onClose={onClose}
      footer={
        created ? (
          <button className="btn btn-primary" onClick={onClose}>
            {t('close')}
          </button>
        ) : (
          <>
            <button className="btn" onClick={onClose}>
              {t('cancel')}
            </button>
            <button className="btn btn-primary" disabled={busy || key.trim().length < 8} onClick={submit} data-testid="create-access-key-submit">
              {busy && <Spinner />}
              {t('create')}
            </button>
          </>
        )
      }
    >
      {created ? (
        <div className="stack">
          <div className="alert small">{tr('请立即复制并妥善保存。之后也可以在列表中查看。', 'Copy it now and store it safely. It stays visible in the list too.')}</div>
          <div className="row">
            <code className="spacer break" data-testid="created-key">
              {created.api_key}
            </code>
            <CopyButton text={created.api_key} />
          </div>
        </div>
      ) : (
        <div className="stack">
          <div className="field">
            <label>{tr('名称（可选）', 'Name (optional)')}</label>
            <input className="input" value={name} maxLength={128} onChange={(e) => setName(e.target.value)} placeholder={tr('例如：内部服务 A', 'e.g. internal service A')} autoFocus data-testid="access-key-name" />
          </div>
          <div className="field">
            <label>{tr('密钥', 'Key')}</label>
            <div className="row">
              <input className="input mono spacer" style={{ flex: 1, minWidth: 0 }} value={key} onChange={(e) => setKey(e.target.value)} />
              <button className="btn" onClick={() => setKey(generateApiKey())}>
                <IconRefresh />
                {tr('重新生成', 'Regenerate')}
              </button>
            </div>
            <span className="hint">{tr('默认随机生成 sk- 开头的 48 位十六进制密钥，也可以填自定义值（至少 8 位）。', 'A random sk- key is generated by default; you may enter your own (min 8 chars).')}</span>
          </div>
        </div>
      )}
    </Modal>
  );
}

function ScopeCell({ k, cgNames, mgNames }: { k: AccessKey; cgNames: Map<number, string>; mgNames: Map<number, string> }) {
  const { tr } = useI18n();
  const sc = keyScopeSummary(k, cgNames, mgNames);
  if (!sc.credentials && !sc.models) return <span className="faint small">{tr('全部凭据 · 全部模型', 'All credentials · all models')}</span>;
  return (
    <div className="scope-chips small">
      {sc.credentials ? sc.credentials.map((n) => <span key={`c-${n}`} className="tag" title={tr('凭据分组', 'Credential group')}>🔑 {n}</span>) : <span className="faint">{tr('全部凭据', 'All credentials')}</span>}
      {sc.models ? sc.models.map((n) => <span key={`m-${n}`} className="tag" title={tr('模型分组', 'Model group')}>▦ {n}</span>) : <span className="faint">{tr('全部模型', 'All models')}</span>}
    </div>
  );
}

function EditKeyModal({ k, onClose, onSaved }: { k: AccessKey; onClose: () => void; onSaved: () => Promise<void> }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { run, busy } = useAction();
  const groups = useGroupLookup();
  const [name, setName] = useState(k.display_name ?? '');
  const [channels, setChannels] = useState<number[]>(k.channels ?? []);
  const [modelGroups, setModelGroups] = useState<number[]>(k.model_groups ?? []);
  const d = groups.data;
  const usableInScope = channels.reduce((n, id) => n + (groups.cgById.get(id)?.enabled ? (groups.cgById.get(id)?.usable ?? 0) : 0), 0);
  const save = () =>
    run('save', async () => {
      if ((k.display_name ?? '') !== name.trim()) await api.renameAccessKey(k.id, name.trim());
      await api.setAccessKeyScope(k.id, { channels, model_groups: modelGroups });
      await onSaved();
    });
  return (
    <Modal
      title={tr('编辑访问密钥', 'Edit access key')}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {t('cancel')}
          </button>
          <button className="btn btn-primary" disabled={!!busy || !d} onClick={() => void save()} data-testid="edit-access-key-save">
            {busy && <Spinner />}
            {t('save')}
          </button>
        </>
      }
    >
      <div className="stack">
        <div className="field">
          <label>{t('name')}</label>
          <input className="input" value={name} maxLength={128} onChange={(e) => setName(e.target.value)} autoFocus />
        </div>
        {!d ? (
          <Loading />
        ) : (
          <>
            <div className="field" data-testid="key-credential-groups">
              <label>{tr('凭据分组（不选 = 可使用全部凭据）', 'Credential groups (none = all credentials)')}</label>
              <GroupChecklist
                groups={d.credential_groups.map((g) => ({ id: g.id, name: g.channel_name, enabled: g.enabled, extra: availabilityLabel(g, tr) }))}
                value={channels}
                onChange={setChannels}
              />
              {channels.length > 0 && (
                <div className={`alert small ${usableInScope === 0 ? 'err' : 'warn'}`} data-testid="key-no-fallback-warning">
                  {usableInScope === 0
                    ? tr('所选分组当前没有可用凭据：保存后此密钥的请求会立即失败（503）。', 'The selected groups have no usable credential right now: requests with this key will fail (503).')
                    : tr('此密钥将只使用所选分组内的凭据；分组内凭据全部冷却或耗尽时请求直接失败（503 / 429），不会回退到其他凭据。', 'This key will only use credentials in the selected groups; when all are cooling or exhausted requests fail (503 / 429) with no fallback.')}
                </div>
              )}
            </div>
            <div className="field" data-testid="key-model-groups">
              <label>{tr('模型分组（不选 = 可调用全部模型）', 'Model groups (none = all models)')}</label>
              <GroupChecklist groups={d.model_groups.map((g) => ({ id: g.id, name: g.group_name, enabled: g.enabled, extra: tr(`${g.models} 个模型`, `${g.models} models`) }))} value={modelGroups} onChange={setModelGroups} />
              {modelGroups.length > 0 && (
                <span className="hint">
                  {tr('只能调用所选分组中模型的并集；模型列表只显示这些模型，其他模型返回 403（model_not_allowed）。', 'Only the union of the selected groups’ models; model lists show only those, others get 403 (model_not_allowed).')}
                </span>
              )}
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function BulkKeyScopeModal({ ids, onClose, onDone }: { ids: number[]; onClose: () => void; onDone: () => Promise<void> }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const groups = useGroupLookup();
  const d = groups.data;
  const [addC, setAddC] = useState<number[]>([]);
  const [rmC, setRmC] = useState<number[]>([]);
  const [addM, setAddM] = useState<number[]>([]);
  const [rmM, setRmM] = useState<number[]>([]);
  const [clearC, setClearC] = useState(false);
  const [clearM, setClearM] = useState(false);
  const nothing = !addC.length && !rmC.length && !addM.length && !rmM.length && !clearC && !clearM;
  const usableAdded = addC.reduce((n, id) => n + (groups.cgById.get(id)?.usable ?? 0), 0);
  const apply = () =>
    run('apply', async () => {
      const r = await api.bulkAccessKeys({ ids, add_channels: addC, remove_channels: rmC, add_model_groups: addM, remove_model_groups: rmM, clear_channels: clearC, clear_model_groups: clearM });
      toast(tr(`已更新 ${r.updated} 个密钥${r.unchanged ? `（${r.unchanged} 个无需变更）` : ''}`, `Updated ${r.updated} keys${r.unchanged ? ` (${r.unchanged} unchanged)` : ''}`));
      await onDone();
    });
  const cgList = (d?.credential_groups ?? []).map((g) => ({ id: g.id, name: g.channel_name, enabled: g.enabled, extra: availabilityLabel(g, tr) }));
  const mgList = (d?.model_groups ?? []).map((g) => ({ id: g.id, name: g.group_name, enabled: g.enabled, extra: tr(`${g.models} 个模型`, `${g.models} models`) }));
  return (
    <Modal
      title={tr(`批量修改 ${ids.length} 个密钥的可用范围`, `Change scope of ${ids.length} keys`)}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {t('cancel')}
          </button>
          <button className="btn btn-primary" disabled={nothing || !!busy} onClick={() => void apply()} data-testid="bulk-keys-apply">
            {busy && <Spinner />}
            {tr('应用', 'Apply')}
          </button>
        </>
      }
    >
      {!d ? (
        <Loading />
      ) : (
        <div className="stack">
          <p className="hint">{tr('增量修改：只增加/移除所选分组，其他绑定保持不变；勾选“先清空”则先移除全部现有绑定。整个操作在一个事务中完成。', 'Incremental: only the chosen groups are added/removed, other bindings stay; "clear first" removes all existing bindings. Runs in one transaction.')}</p>
          <div className="grid grid-2">
            <div className="field">
              <label>{tr('加入凭据分组', 'Add credential groups')}</label>
              <GroupChecklist groups={cgList} value={addC} onChange={setAddC} />
            </div>
            <div className="field">
              <label>{tr('移除凭据分组', 'Remove credential groups')}</label>
              <GroupChecklist groups={cgList} value={rmC} onChange={setRmC} />
              <label className="check small">
                <input type="checkbox" checked={clearC} onChange={(e) => setClearC(e.target.checked)} />
                <span>{tr('先清空全部凭据分组绑定', 'Clear all credential groups first')}</span>
              </label>
            </div>
            <div className="field">
              <label>{tr('加入模型分组', 'Add model groups')}</label>
              <GroupChecklist groups={mgList} value={addM} onChange={setAddM} />
            </div>
            <div className="field">
              <label>{tr('移除模型分组', 'Remove model groups')}</label>
              <GroupChecklist groups={mgList} value={rmM} onChange={setRmM} />
              <label className="check small">
                <input type="checkbox" checked={clearM} onChange={(e) => setClearM(e.target.checked)} />
                <span>{tr('先清空全部模型分组绑定', 'Clear all model groups first')}</span>
              </label>
            </div>
          </div>
          {addC.length > 0 && (
            <div className={`alert small ${usableAdded === 0 ? 'err' : 'warn'}`}>
              {usableAdded === 0
                ? tr('所选凭据分组当前没有可用凭据：仅绑定这些分组的密钥请求会立即失败（503）。', 'The chosen groups have no usable credential now: keys bound only to them will fail (503).')
                : tr('绑定凭据分组后，这些密钥只使用分组内的凭据，分组内全部冷却时请求直接失败，不回退。', 'Bound keys only use those credentials; when all are cooling requests fail with no fallback.')}
            </div>
          )}
        </div>
      )}
    </Modal>
  );
}
