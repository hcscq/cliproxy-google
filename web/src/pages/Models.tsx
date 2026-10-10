import { useEffect, useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { isSupportedProvider, type ModelInfo } from '../api/client';
import { fmtCompact } from '../format';
import { useI18n } from '../i18n';
import { Badge, Card, CopyButton, Empty, ErrorBox, Loading, Spinner, Switch } from '../components/ui';
import { IconPlus, IconRefresh, IconTrash } from '../components/icons';

type Tab = 'available' | 'excluded' | 'alias' | 'catalog';
const CHANNELS = ['antigravity', 'vertex', 'aistudio'];

export function ModelsPage() {
  const { t, tr } = useI18n();
  const [tab, setTab] = useState<Tab>('available');
  const tabs: [Tab, string][] = [
    ['available', tr('可用模型', 'Available')],
    ['excluded', tr('排除模型', 'Excluded')],
    ['alias', tr('别名 / 扩展模型', 'Aliases / extra models')],
    ['catalog', tr('内置目录', 'Built-in catalog')],
  ];
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('models')}</h1>
          <p>{tr('下游通过代理 /v1/models 看到的就是“可用模型”。Antigravity 模型列表会按账号从上游动态获取，并与内置目录、别名合并。', 'Downstream /v1/models shows the "available" list. Antigravity models are fetched from upstream per account and merged with the built-in catalog and aliases.')}</p>
        </div>
      </div>
      <div className="tabs" style={{ marginBottom: 0 }}>
        {tabs.map(([k, l]) => (
          <button key={k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)} data-testid={`models-tab-${k}`}>
            {l}
          </button>
        ))}
      </div>
      {tab === 'available' && <AvailableModels />}
      {tab === 'excluded' && <ExcludedModels />}
      {tab === 'alias' && <Aliases />}
      {tab === 'catalog' && <Catalog />}
    </div>
  );
}

function ModelTable({ models }: { models: ModelInfo[] }) {
  const { tr } = useI18n();
  return (
    <table className="table" data-testid="models-table">
      <thead>
        <tr>
          <th>{tr('模型 ID', 'Model ID')}</th>
          <th className="hide-sm">{tr('显示名称', 'Display name')}</th>
          <th>{tr('提供方', 'Providers')}</th>
          <th className="hide-sm">{tr('上下文', 'Context')}</th>
          <th className="hide-sm">{tr('最大输出', 'Max output')}</th>
          <th className="hide-sm">{tr('思考', 'Thinking')}</th>
        </tr>
      </thead>
      <tbody>
        {models.map((m) => (
          <tr key={`${m.id}-${(m.providers ?? []).join()}`}>
            <td>
              <span className="mono">{m.id}</span>
              {m.user_defined && (
                <>
                  {' '}
                  <Badge kind="warn">{tr('自定义', 'custom')}</Badge>
                </>
              )}
            </td>
            <td className="hide-sm muted">{m.display_name || '—'}</td>
            <td>
              {(m.providers ?? [m.owned_by ?? m.type ?? '']).filter(Boolean).map((p) => (
                <Badge key={p} kind="primary">
                  {p}
                </Badge>
              ))}
            </td>
            <td className="hide-sm">{fmtCompact(m.context_length ?? m.inputTokenLimit)}</td>
            <td className="hide-sm">{fmtCompact(m.max_completion_tokens ?? m.outputTokenLimit)}</td>
            <td className="hide-sm small">{m.thinking ? (m.thinking.levels?.join(' / ') || `${fmtCompact(m.thinking.min)}–${fmtCompact(m.thinking.max)}`) : <span className="faint">—</span>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function AvailableModels() {
  const api = useApi();
  const { t, tr } = useI18n();
  const res = useAsync(() => api.models('available'), []);
  const [q, setQ] = useState('');
  const [provider, setProvider] = useState('');
  const models = useMemo(() => {
    const list = Array.isArray(res.data?.models) ? (res.data!.models as ModelInfo[]) : [];
    return list.filter((m) => (m.providers ?? [m.type]).some((p) => isSupportedProvider(p)));
  }, [res.data]);
  const providers = Array.from(new Set(models.flatMap((m) => m.providers ?? []))).sort();
  const shown = models
    .filter((m) => (!provider || m.providers?.includes(provider)) && (!q || `${m.id} ${m.display_name ?? ''}`.toLowerCase().includes(q.toLowerCase())))
    .sort((a, b) => a.id.localeCompare(b.id));
  return (
    <Card
      bodyClass="table-wrap"
      title={
        <div className="row">
          <input className="input input-sm" style={{ width: 220 }} placeholder={t('search')} value={q} onChange={(e) => setQ(e.target.value)} />
          <select className="select input-sm" style={{ width: 150 }} value={provider} onChange={(e) => setProvider(e.target.value)}>
            <option value="">{tr('全部提供方', 'All providers')}</option>
            {providers.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </div>
      }
      extra={
        <>
          <span className="faint small">{tr(`${shown.length} / ${models.length} 个`, `${shown.length} / ${models.length}`)}</span>
          <CopyButton small text={shown.map((m) => m.id).join('\n')} />
          <button className="btn btn-sm" onClick={res.reload}>
            <IconRefresh />
            {t('refresh')}
          </button>
        </>
      }
    >
      {res.error ? (
        <div className="card-body">
          <ErrorBox error={res.error} onRetry={res.reload} />
        </div>
      ) : !res.data ? (
        <Loading />
      ) : shown.length ? (
        <ModelTable models={shown} />
      ) : (
        <Empty>{models.length ? tr('没有匹配的模型', 'No matching models') : tr('没有可用模型：请先添加并启用凭据。', 'No models: add and enable a credential first.')}</Empty>
      )}
    </Card>
  );
}

function wildcardToRegExp(p: string): RegExp {
  return new RegExp(`^${p.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*')}$`, 'i');
}

function ExcludedModels() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const remote = useAsync(() => api.configGet<Record<string, string[]>>('oauth/excluded-models', {}), []);
  const staticModels = useAsync(() => api.modelDefinitions('antigravity').then((r) => r.models ?? []).catch(() => [] as ModelInfo[]), []);
  const [map, setMap] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (remote.data) {
      const m: Record<string, string> = {};
      for (const ch of CHANNELS) m[ch] = (remote.data[ch] ?? []).join('\n');
      for (const [k, v] of Object.entries(remote.data)) if (!(k in m) && isSupportedProvider(k)) m[k] = (v ?? []).join('\n');
      setMap(m);
      setDirty(false);
    }
  }, [remote.data]);

  const save = () =>
    run('save', async () => {
      const value: Record<string, string[]> = { ...(remote.data ?? {}) };
      for (const [k, v] of Object.entries(map)) {
        const list = v
          .split(/[\n,]/)
          .map((s) => s.trim())
          .filter(Boolean);
        if (list.length) value[k] = list;
        else delete value[k];
      }
      await api.configPut('oauth/excluded-models', value);
      toast(t('saved'));
      await remote.reload();
    });

  if (remote.error) return <ErrorBox error={remote.error} onRetry={remote.reload} />;
  if (!remote.data) return <Loading />;

  const agPatterns = (map.antigravity ?? '').split(/[\n,]/).map((s) => s.trim()).filter(Boolean);
  const agHidden = (staticModels.data ?? []).filter((m) => agPatterns.some((p) => wildcardToRegExp(p).test(m.id)));

  return (
    <div className="stack">
      <div className="alert small">
        {tr('对 OAuth/文件类凭据生效（Antigravity、Vertex 服务账号、AI Studio）。每行一个模型名，支持 * 通配符，例如 chat_* 或 *-preview。API Key 分组的排除模型在“Gemini / Vertex 密钥”页配置。', 'Applies to OAuth/file credentials (Antigravity, Vertex service accounts, AI Studio). One model per line; * wildcards allowed, e.g. chat_* or *-preview. API-key groups have their own excluded models on the keys page.')}
      </div>
      <div className="grid grid-3">
        {Object.keys(map).map((ch) => (
          <Card key={ch} title={<h2 className="mono">{ch}</h2>}>
            <textarea
              className="textarea"
              value={map[ch]}
              placeholder={ch === 'antigravity' ? 'chat_*\ntab_*' : ''}
              onChange={(e) => {
                setMap({ ...map, [ch]: e.target.value });
                setDirty(true);
              }}
              data-testid={`excluded-${ch}`}
            />
            {ch === 'antigravity' && agHidden.length > 0 && (
              <div className="small faint" style={{ marginTop: 8 }}>
                {tr('内置目录中被隐藏的模型：', 'Hidden from the built-in catalog: ')}
                {agHidden.map((m) => (
                  <span key={m.id} className="tag">
                    {m.id}
                  </span>
                ))}
              </div>
            )}
          </Card>
        ))}
      </div>
      <div className="row">
        <button className="btn btn-primary" disabled={!dirty || busy === 'save'} onClick={save}>
          {busy === 'save' && <Spinner />}
          {t('save')}
        </button>
        {dirty && <span className="small" style={{ color: 'var(--warn)' }}>{tr('有未保存的修改', 'Unsaved changes')}</span>}
      </div>
    </div>
  );
}

interface Alias {
  name: string;
  alias: string;
  'display-name'?: string;
  fork?: boolean;
  'force-mapping'?: boolean;
}

function Aliases() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast } = useUi();
  const { busy, run } = useAction();
  const remote = useAsync(() => api.configGet<Record<string, Alias[]>>('oauth/model-alias', {}), []);
  const available = useAsync(() => api.models('available').then((r) => (Array.isArray(r.models) ? r.models : [])), []);
  const [channel, setChannel] = useState('antigravity');
  const [rows, setRows] = useState<Alias[]>([]);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (remote.data) {
      setRows((remote.data[channel] ?? []).map((a) => ({ ...a })));
      setDirty(false);
    }
  }, [remote.data, channel]);

  const setRow = (i: number, patch: Partial<Alias>) => {
    setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    setDirty(true);
  };

  const save = () =>
    run('save', async () => {
      const list = rows
        .map((r) => ({ ...r, name: r.name.trim(), alias: r.alias.trim(), 'display-name': r['display-name']?.trim() || undefined }))
        .filter((r) => r.name && r.alias);
      for (const r of list) {
        if (!r['display-name']) delete r['display-name'];
        if (!r.fork) delete r.fork;
        if (!r['force-mapping']) delete r['force-mapping'];
      }
      if (list.length) await api.configPut(`oauth/model-alias/${channel}`, list);
      else if ((remote.data ?? {})[channel]) await api.configDelete(`oauth/model-alias/${channel}`);
      toast(t('saved'));
      await remote.reload();
    });

  if (remote.error) return <ErrorBox error={remote.error} onRetry={remote.reload} />;
  if (!remote.data) return <Loading />;
  const ids = (available.data ?? []).filter((m) => m.providers?.includes(channel)).map((m) => m.id).sort();

  return (
    <div className="stack">
      <div className="alert small">
        {tr(
          '别名让下游用新名字调用上游模型。勾选“保留原名”（fork）时原模型仍可用，相当于“扩展”出一个新模型名；否则原名被替换。需要上游未在目录中列出的模型时，也可直接把上游真实模型名写在“上游模型”里。部署层面还可以用环境变量 HOME_ANTIGRAVITY_EXTRA_MODELS（逗号分隔）追加模型。',
          'Aliases expose an upstream model under a new name. With "keep original" (fork) the original stays available, effectively adding an extra model name; otherwise it is renamed. You may also type an upstream model id that is not in the catalog. At deploy level, HOME_ANTIGRAVITY_EXTRA_MODELS (comma list) adds models too.',
        )}
      </div>
      <Card
        title={
          <div className="row">
            <span className="label">{tr('通道', 'Channel')}</span>
            <select className="select input-sm" style={{ width: 160 }} value={channel} onChange={(e) => setChannel(e.target.value)}>
              {CHANNELS.map((c) => (
                <option key={c}>{c}</option>
              ))}
            </select>
          </div>
        }
        extra={
          <>
            {dirty && <span className="small" style={{ color: 'var(--warn)' }}>{tr('有未保存的修改', 'Unsaved changes')}</span>}
            <button className="btn btn-sm btn-primary" disabled={!dirty || busy === 'save'} onClick={save}>
              {busy === 'save' && <Spinner />}
              {t('save')}
            </button>
          </>
        }
        bodyClass="table-wrap"
      >
        <datalist id="upstream-models">
          {ids.map((id) => (
            <option key={id} value={id} />
          ))}
        </datalist>
        <table className="table">
          <thead>
            <tr>
              <th>{tr('上游模型', 'Upstream model')}</th>
              <th>{tr('别名（下游使用）', 'Alias (client-facing)')}</th>
              <th className="hide-sm">{tr('显示名称', 'Display name')}</th>
              <th>{tr('保留原名', 'Keep original')}</th>
              <th className="hide-sm">{tr('响应用上游名', 'Force mapping')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr>
                <td colSpan={6}>
                  <Empty>{tr('该通道还没有别名', 'No aliases for this channel')}</Empty>
                </td>
              </tr>
            )}
            {rows.map((r, i) => (
              <tr key={i}>
                <td>
                  <input className="input input-sm mono" list="upstream-models" value={r.name} onChange={(e) => setRow(i, { name: e.target.value })} placeholder="gemini-3-flash" />
                </td>
                <td>
                  <input className="input input-sm mono" value={r.alias} onChange={(e) => setRow(i, { alias: e.target.value })} placeholder="my-flash" />
                </td>
                <td className="hide-sm">
                  <input className="input input-sm" value={r['display-name'] ?? ''} onChange={(e) => setRow(i, { 'display-name': e.target.value })} />
                </td>
                <td>
                  <Switch checked={!!r.fork} onChange={(v) => setRow(i, { fork: v })} />
                </td>
                <td className="hide-sm">
                  <Switch checked={!!r['force-mapping']} onChange={(v) => setRow(i, { 'force-mapping': v })} />
                </td>
                <td className="actions">
                  <button
                    className="btn btn-sm btn-ghost btn-danger"
                    onClick={() => {
                      setRows(rows.filter((_, j) => j !== i));
                      setDirty(true);
                    }}
                    aria-label="remove"
                  >
                    <IconTrash />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="card-body">
          <button
            className="btn btn-sm"
            onClick={() => {
              setRows([...rows, { name: '', alias: '', fork: true }]);
              setDirty(true);
            }}
          >
            <IconPlus />
            {tr('添加别名', 'Add alias')}
          </button>
        </div>
      </Card>
    </div>
  );
}

function Catalog() {
  const api = useApi();
  const { tr } = useI18n();
  const [channel, setChannel] = useState('antigravity');
  const res = useAsync(() => api.modelDefinitions(channel), [channel]);
  return (
    <Card
      bodyClass="table-wrap"
      title={
        <div className="row">
          <span className="label">{tr('通道', 'Channel')}</span>
          <select className="select input-sm" style={{ width: 160 }} value={channel} onChange={(e) => setChannel(e.target.value)}>
            {['antigravity', 'gemini', 'vertex', 'aistudio'].map((c) => (
              <option key={c}>{c}</option>
            ))}
          </select>
        </div>
      }
      extra={<span className="faint small">{tr('内置目录仅供参考；实际可用以“可用模型”为准。', 'Reference only; the Available tab is authoritative.')}</span>}
    >
      {res.error ? (
        <div className="card-body">
          <ErrorBox error={res.error} />
        </div>
      ) : !res.data ? (
        <Loading />
      ) : res.data.models?.length ? (
        <ModelTable models={res.data.models} />
      ) : (
        <Empty />
      )}
    </Card>
  );
}
