import { useEffect, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { maskSecret } from '../api/client';
import { useI18n } from '../i18n';
import { Card, Empty, ErrorBox, Loading, Spinner } from '../components/ui';
import { IconPlus, IconTrash } from '../components/icons';

interface KeyEntry {
  id?: string;
  'api-key': string;
  weight?: number;
  [k: string]: unknown;
}

interface Group {
  name?: string;
  'base-url'?: string;
  priority?: number;
  prefix?: string;
  'proxy-url'?: string;
  'excluded-models'?: string[];
  keys: KeyEntry[];
  [k: string]: unknown;
}

const FAMILIES = [
  { id: 'gemini', zh: 'Gemini API Key', en: 'Gemini API keys', descZh: 'Google AI Studio / Gemini API 的 API Key（generativelanguage.googleapis.com）。', descEn: 'API keys for the Gemini API (generativelanguage.googleapis.com).' },
  { id: 'vertex', zh: 'Vertex API Key', en: 'Vertex API keys', descZh: 'Vertex AI 的 API Key（以 x-goog-api-key 发送）。服务账号请在“凭据”页导入。', descEn: 'Vertex AI API keys (sent as x-goog-api-key). Import service accounts on the Credentials page.' },
] as const;

type FamilyId = (typeof FAMILIES)[number]['id'];

export function ProviderKeysPage() {
  const { t, tr } = useI18n();
  const [family, setFamily] = useState<FamilyId>('gemini');
  const fam = FAMILIES.find((f) => f.id === family)!;
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('providerKeys')}</h1>
          <p>{tr('上游 API Key 分组。每个分组可以设置 Base URL、优先级、模型前缀与排除模型；每个 Key 可设置权重（加权轮询时生效）。', 'Upstream API-key groups. Each group has base URL, priority, prefix and excluded models; each key has a weight (used by weighted round-robin).')}</p>
        </div>
      </div>
      <div className="tabs" style={{ marginBottom: 0 }}>
        {FAMILIES.map((f) => (
          <button key={f.id} className={family === f.id ? 'active' : ''} onClick={() => setFamily(f.id)}>
            {tr(f.zh, f.en)}
          </button>
        ))}
      </div>
      <p className="muted" style={{ margin: 0 }}>
        {tr(fam.descZh, fam.descEn)}
      </p>
      <FamilyEditor key={family} family={family} />
    </div>
  );
}

function FamilyEditor({ family }: { family: FamilyId }) {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const remote = useAsync(() => api.configGet<Group[]>(`api-keys/${family}`, []), [family]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [dirty, setDirty] = useState(false);
  const [mode, setMode] = useState<'form' | 'json'>('form');
  const [json, setJson] = useState('');
  const [jsonError, setJsonError] = useState<string>();
  const [reveal, setReveal] = useState<Record<string, boolean>>({});

  useEffect(() => {
    if (remote.data) {
      const g = (Array.isArray(remote.data) ? remote.data : []).map((x) => ({ ...x, keys: Array.isArray(x.keys) ? x.keys : [] }));
      setGroups(g);
      setJson(JSON.stringify(g, null, 2));
      setDirty(false);
    }
  }, [remote.data]);

  const update = (next: Group[]) => {
    setGroups(next);
    setJson(JSON.stringify(next, null, 2));
    setDirty(true);
  };
  const setGroup = (i: number, patch: Partial<Group>) => update(groups.map((g, j) => (j === i ? clean({ ...g, ...patch }) : g)));
  const setKey = (gi: number, ki: number, patch: Partial<KeyEntry>) =>
    setGroup(gi, { keys: groups[gi].keys.map((k, j) => (j === ki ? clean({ ...k, ...patch }) : k)) });

  const save = () =>
    run('save', async () => {
      let payload = groups;
      if (mode === 'json') {
        try {
          payload = JSON.parse(json);
          if (!Array.isArray(payload)) throw new Error(tr('必须是数组', 'Must be an array'));
        } catch (e) {
          setJsonError((e as Error).message);
          return;
        }
      }
      for (const g of payload) {
        if (!Array.isArray(g.keys)) throw new Error(tr('每个分组都需要 keys 数组', 'Every group needs a keys array'));
        for (const k of g.keys) if (!String(k['api-key'] ?? '').trim() && !g['base-url']) throw new Error(tr('API Key 不能为空', 'API key must not be empty'));
      }
      await api.configPut(`api-keys/${family}`, payload);
      toast(t('saved'));
      setJsonError(undefined);
      await remote.reload();
    });

  if (remote.error) return <ErrorBox error={remote.error} onRetry={remote.reload} />;
  if (!remote.data) return <Loading />;

  const keyCount = groups.reduce((n, g) => n + g.keys.length, 0);

  return (
    <div className="stack">
      <div className="row">
        <div className="seg">
          <button className={mode === 'form' ? 'active' : ''} onClick={() => setMode('form')}>
            {tr('表单', 'Form')}
          </button>
          <button
            className={mode === 'json' ? 'active' : ''}
            onClick={() => {
              setJson(JSON.stringify(groups, null, 2));
              setMode('json');
            }}
          >
            JSON
          </button>
        </div>
        <span className="faint small">{tr(`${groups.length} 个分组 · ${keyCount} 个 Key`, `${groups.length} group(s) · ${keyCount} key(s)`)}</span>
        <span className="spacer" />
        {dirty && <span className="small" style={{ color: 'var(--warn)' }}>{tr('有未保存的修改', 'Unsaved changes')}</span>}
        <button className="btn" disabled={!dirty} onClick={() => remote.reload()}>
          {tr('放弃修改', 'Discard')}
        </button>
        <button className="btn btn-primary" disabled={(!dirty && mode === 'form') || busy === 'save'} onClick={save}>
          {busy === 'save' && <Spinner />}
          {t('save')}
        </button>
      </div>

      {mode === 'json' ? (
        <Card>
          <textarea
            className="textarea"
            style={{ minHeight: 420 }}
            spellCheck={false}
            value={json}
            onChange={(e) => {
              setJson(e.target.value);
              setDirty(true);
              try {
                JSON.parse(e.target.value);
                setJsonError(undefined);
              } catch (err) {
                setJsonError((err as Error).message);
              }
            }}
          />
          {jsonError && <div className="alert err small" style={{ marginTop: 8 }}>{jsonError}</div>}
          <p className="hint">{tr('JSON 模式可以编辑 models（别名）、headers、request-retry 等高级字段。保留每个 Key 的 id 以维持凭据身份。', 'JSON mode exposes advanced fields (models/aliases, headers, request-retry…). Keep each key id to preserve credential identity.')}</p>
        </Card>
      ) : (
        <>
          {groups.length === 0 && (
            <Card>
              <Empty>{tr('还没有配置，点击下方“添加分组”。', 'Nothing configured yet — add a group below.')}</Empty>
            </Card>
          )}
          {groups.map((g, gi) => (
            <Card
              key={gi}
              title={
                <div className="row">
                  <h2>{g.name || tr(`分组 ${gi + 1}`, `Group ${gi + 1}`)}</h2>
                  <span className="faint small">{tr(`${g.keys.length} 个 Key`, `${g.keys.length} key(s)`)}</span>
                </div>
              }
              extra={
                <button
                  className="btn btn-sm btn-danger"
                  onClick={async () => {
                    if (await confirm({ title: tr('删除分组', 'Delete group'), body: tr('该分组下的所有 Key 都会被移除（保存后生效）。', 'All keys in this group will be removed (after saving).'), danger: true }))
                      update(groups.filter((_, j) => j !== gi));
                  }}
                >
                  <IconTrash />
                  {tr('删除分组', 'Delete group')}
                </button>
              }
            >
              <div className="form-grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))' }}>
                <div className="field">
                  <label>{t('name')}</label>
                  <input className="input" value={g.name ?? ''} onChange={(e) => setGroup(gi, { name: e.target.value })} />
                </div>
                <div className="field">
                  <label>Base URL</label>
                  <input className="input" value={g['base-url'] ?? ''} placeholder={tr('默认官方地址', 'official default')} onChange={(e) => setGroup(gi, { 'base-url': e.target.value })} />
                </div>
                <div className="field">
                  <label>{t('priority')}</label>
                  <input className="input" type="number" value={g.priority ?? ''} onChange={(e) => setGroup(gi, { priority: e.target.value === '' ? undefined : Number(e.target.value) })} />
                </div>
                <div className="field">
                  <label>{t('prefix')}</label>
                  <input className="input" value={g.prefix ?? ''} onChange={(e) => setGroup(gi, { prefix: e.target.value })} />
                </div>
                <div className="field">
                  <label>{t('proxyUrl')}</label>
                  <input className="input" value={g['proxy-url'] ?? ''} placeholder="direct / socks5://…" onChange={(e) => setGroup(gi, { 'proxy-url': e.target.value })} />
                </div>
                <div className="field">
                  <label>{tr('排除模型（逗号分隔，支持 *）', 'Excluded models (comma, * ok)')}</label>
                  <input
                    className="input"
                    value={(g['excluded-models'] ?? []).join(', ')}
                    onChange={(e) =>
                      setGroup(gi, {
                        'excluded-models': e.target.value
                          .split(',')
                          .map((s) => s.trim())
                          .filter(Boolean),
                      })
                    }
                  />
                </div>
              </div>
              <div className="table-wrap" style={{ marginTop: 14 }}>
                <table className="table">
                  <thead>
                    <tr>
                      <th>API Key</th>
                      <th style={{ width: 120 }}>{t('weight')}</th>
                      <th style={{ width: 90 }} />
                    </tr>
                  </thead>
                  <tbody>
                    {g.keys.map((k, ki) => {
                      const rid = `${gi}-${ki}`;
                      const isNew = !k.id;
                      return (
                        <tr key={k.id ?? `new-${ki}`}>
                          <td>
                            {isNew || reveal[rid] ? (
                              <input className="input input-sm mono" value={k['api-key']} placeholder="AIza…" onChange={(e) => setKey(gi, ki, { 'api-key': e.target.value })} />
                            ) : (
                              <span className="mono">{maskSecret(k['api-key'], 6)}</span>
                            )}
                            {k.id && <div className="faint small mono">{k.id}</div>}
                          </td>
                          <td>
                            <input className="input input-sm" type="number" min={0} value={k.weight ?? ''} placeholder="1" onChange={(e) => setKey(gi, ki, { weight: e.target.value === '' ? undefined : Number(e.target.value) })} />
                          </td>
                          <td className="actions">
                            {!isNew && (
                              <button className="btn btn-sm btn-ghost" onClick={() => setReveal({ ...reveal, [rid]: !reveal[rid] })}>
                                {reveal[rid] ? tr('隐藏', 'Hide') : t('edit')}
                              </button>
                            )}
                            <button className="btn btn-sm btn-ghost btn-danger" onClick={() => setGroup(gi, { keys: g.keys.filter((_, j) => j !== ki) })} aria-label="remove key">
                              <IconTrash />
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              <button className="btn btn-sm" style={{ marginTop: 10 }} onClick={() => setGroup(gi, { keys: [...g.keys, { 'api-key': '' }] })}>
                <IconPlus />
                {tr('添加 Key', 'Add key')}
              </button>
            </Card>
          ))}
          <div>
            <button className="btn" onClick={() => update([...groups, { name: '', keys: [{ 'api-key': '' }] }])}>
              <IconPlus />
              {tr('添加分组', 'Add group')}
            </button>
          </div>
        </>
      )}
    </div>
  );
}

/** Drops empty optional fields so the server applies its defaults. */
function clean<T extends Record<string, unknown>>(o: T): T {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined) continue;
    if (v === '' && k !== 'api-key') continue;
    if (Array.isArray(v) && v.length === 0 && k !== 'keys') continue;
    out[k] = v;
  }
  return out as T;
}
