import { useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { isSupportedProvider, type Credential, type ModelInfo, type QuotaItem } from '../api/client';
import { downloadBlob, fmtRelative, fmtTime } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, Drawer, Empty, ErrorBox, Loading, Spinner, Switch } from '../components/ui';
import { QuotaStatusBadge, QuotaWindows } from '../components/quota';
import { IconDownload, IconPlus, IconRefresh, IconTrash } from '../components/icons';
import { AddCredentialModal } from './AddCredential';

export function credentialStatus(c: Credential, t: ReturnType<typeof useI18n>['t'], tr: ReturnType<typeof useI18n>['tr']) {
  if (c.disabled) return <Badge>{t('disabled')}</Badge>;
  if (c.unavailable) return <Badge kind="warn" dot>{tr('冷却/不可用', 'Cooling down')}</Badge>;
  if (c.status === 'active') return <Badge kind="ok" dot>{tr('可用', 'Active')}</Badge>;
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
  const creds = useAsync(() => api.credentials(), [], 20000);
  const quota = useAsync(() => api.quotaCredentials({ limit: 200 }), [], 60000);
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [provider, setProvider] = useState('');

  const all = useMemo(() => (creds.data?.files ?? []).filter((c) => isSupportedProvider(c.provider)), [creds.data]);
  const hidden = (creds.data?.files.length ?? 0) - all.length;
  const providers = useMemo(() => Array.from(new Set(all.map((c) => c.provider))).sort(), [all]);
  const quotaById = useMemo(() => new Map<string, QuotaItem>((quota.data?.items ?? []).map((q) => [q.credential_id, q])), [quota.data]);
  const list = all.filter(
    (c) =>
      (!provider || c.provider === provider) &&
      (!filter || [c.label, c.email, c.name, c.note, c.prefix].some((v) => v?.toLowerCase().includes(filter.toLowerCase()))),
  );
  const current = all.find((c) => c.id === selected);

  const toggle = (c: Credential, enabled: boolean) =>
    run(`toggle-${c.id}`, async () => {
      await api.setCredentialDisabled(c.id, !enabled);
      toast(enabled ? tr('已启用', 'Enabled') : tr('已禁用', 'Disabled'));
      await creds.reload();
    });

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('credentials')}</h1>
          <p>{tr('Antigravity OAuth、AI Studio 与 Vertex 服务账号凭据。点击一行查看详情、额度和模型。', 'Antigravity OAuth, AI Studio and Vertex service-account credentials. Click a row for details, quota and models.')}</p>
        </div>
        <div className="toolbar">
          <button
            className="btn"
            disabled={busy === 'refresh-all' || !all.length}
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
        title={
          <div className="row">
            <input className="input input-sm" style={{ width: 220 }} placeholder={t('search')} value={filter} onChange={(e) => setFilter(e.target.value)} />
            <select className="select input-sm" style={{ width: 150 }} value={provider} onChange={(e) => setProvider(e.target.value)}>
              <option value="">{tr('全部提供方', 'All providers')}</option>
              {providers.map((p) => (
                <option key={p}>{p}</option>
              ))}
            </select>
          </div>
        }
        extra={
          <span className="faint small">
            {tr(`${list.length} 个`, `${list.length} shown`)}
            {hidden > 0 && tr(`（隐藏 ${hidden} 个已移除提供方的凭据）`, ` (${hidden} of removed providers hidden)`)}
          </span>
        }
      >
        {creds.error ? (
          <div className="card-body">
            <ErrorBox error={creds.error} onRetry={creds.reload} />
          </div>
        ) : !creds.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <Empty>{all.length ? tr('没有匹配的凭据', 'No matching credentials') : tr('还没有凭据，点击右上角“添加凭据”。', 'No credentials yet — click "Add credential".')}</Empty>
        ) : (
          <table className="table" data-testid="credentials-table">
            <thead>
              <tr>
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
              {list.map((c) => (
                <tr key={c.id} className="clickable" onClick={() => setSelected(c.id)}>
                  <td>
                    <div className="ellipsis" style={{ maxWidth: 320 }} title={c.label || c.email || c.name}>
                      {c.label || c.email || c.name}
                    </div>
                    <div className="faint small mono ellipsis" style={{ maxWidth: 320 }}>
                      {c.note ? `📝 ${c.note}` : c.id}
                    </div>
                  </td>
                  <td>
                    <Badge kind="primary">{c.provider}</Badge>
                  </td>
                  <td>
                    <div className="stack" style={{ gap: 4 }}>
                      {credentialStatus(c, t, tr)}
                      {c.status_message && <span className="faint small ellipsis" style={{ maxWidth: 200 }} title={c.status_message}>{c.status_message}</span>}
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
      </Card>

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
            await creds.reload();
            await quota.reload();
          }}
          onDeleted={async () => {
            setSelected(null);
            await creds.reload();
          }}
        />
      )}
    </div>
  );
}

function CredentialDrawer({ cred, quota, onClose, onChanged, onDeleted }: { cred: Credential; quota?: QuotaItem; onClose: () => void; onChanged: () => Promise<void>; onDeleted: () => Promise<void> }) {
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
        {cred.status_message && <div className="alert warn small">{cred.status_message}</div>}

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
