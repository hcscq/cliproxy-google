import { useEffect, useMemo, useState } from 'react';
import { useAction, useApi, useAsync, useUi } from '../app-context';
import { useI18n } from '../i18n';
import { Card, ErrorBox, Loading, Spinner, Switch } from '../components/ui';

type FieldType = 'bool' | 'number' | 'text' | 'select';
interface FieldDef {
  path: string;
  type: FieldType;
  zh: string;
  en: string;
  hintZh?: string;
  hintEn?: string;
  options?: string[];
  danger?: boolean;
  /** Value shown when the key is absent from the config (server-side default). */
  def?: unknown;
}

const SECTIONS: { zh: string; en: string; fields: FieldDef[] }[] = [
  {
    zh: '路由',
    en: 'Routing',
    fields: [
      { path: 'routing.strategy', type: 'select', options: ['round-robin', 'weighted-round-robin', 'fill-first'], zh: '凭据选择策略', en: 'Credential strategy', hintZh: '加权轮询使用每个 Key 的权重；fill-first 优先用满一个凭据。', hintEn: 'Weighted uses per-key weights; fill-first drains one credential first.' },
      { path: 'routing.session-affinity', type: 'bool', zh: '会话粘性', en: 'Session affinity', hintZh: '同一会话尽量固定到同一凭据（状态保存在 PostgreSQL）。', hintEn: 'Pin a session to one credential (state in PostgreSQL).' },
      { path: 'routing.session-affinity-ttl', type: 'text', zh: '会话粘性时长', en: 'Session affinity TTL', hintZh: '例如 1h、30m', hintEn: 'e.g. 1h, 30m' },
      { path: 'routing.force-model-prefix', type: 'bool', zh: '强制模型前缀', en: 'Force model prefix', hintZh: '带前缀的凭据只响应 “前缀/模型”。', hintEn: 'Prefixed credentials only serve "prefix/model".' },
    ],
  },
  {
    zh: '重试与冷却',
    en: 'Retry & cooldown',
    fields: [
      { path: 'routing.retry.request-retry', type: 'number', zh: '额外重试轮数', en: 'Request retry rounds', hintZh: '403/408/429/5xx 时换凭据重试的额外轮数。', hintEn: 'Extra rounds on 403/408/429/5xx.' },
      { path: 'routing.retry.max-retry-credentials', type: 'number', zh: '每轮最多尝试凭据数', en: 'Max credentials per round', hintZh: '≤0 表示全部', hintEn: '≤0 means all' },
      { path: 'routing.retry.max-retry-interval', type: 'number', zh: '最长等待冷却（秒）', en: 'Max cooldown wait (s)' },
      { path: 'routing.honor-max-tokens', type: 'bool', def: true, zh: '遵守客户端输出上限（max_tokens）', en: 'Honor client max_tokens', hintZh: '把 max_tokens / maxOutputTokens 传给 Antigravity/AI Studio 的 Gemini 模型。思考 token 也计入上限；固定思考预算 ≥ 上限时自动降为上限的一半。关闭则恢复旧行为（忽略上限）。', hintEn: 'Pass max_tokens / maxOutputTokens to Gemini models on Antigravity/AI Studio. Thinking tokens count toward the limit; a fixed thinking budget >= limit is lowered to half. Off = legacy (limit ignored).' },
      { path: 'routing.stream-thinking-emulation', type: 'bool', def: false, zh: '流式思考模拟（Gemini 3.x）', en: 'Stream thinking emulation (Gemini 3.x)', hintZh: '上游 Gemini 3.x 流式只在思考较长时才返回思考摘要，签名在正文之后到达。开启后，请求思考内容（includeThoughts / reasoning_effort / Claude thinking）的流式请求改走上游非流式接口，拿到完整结果后一次性按流式格式返回：思考文本和签名完整，但首字延迟等于整段生成时间。默认关闭。', hintEn: 'Upstream Gemini 3.x streams only include thought summaries for longer reasoning and send the signature after the text. When on, streaming requests that ask for thoughts use the non-stream upstream endpoint and are replayed as one SSE event: complete thinking and signature, but time-to-first-token equals full generation time. Default off.' },
      { path: 'routing.retry.capacity-retry.enabled', type: 'bool', def: true, zh: '上游容量不足时重试', en: 'Retry on upstream capacity errors', hintZh: '503 UNAVAILABLE / MODEL_CAPACITY_EXHAUSTED 等容量错误：先换其他凭据，再退避后用同一模型重试。不会更换模型。', hintEn: '503 UNAVAILABLE / MODEL_CAPACITY_EXHAUSTED: try other credentials first, then back off and retry the same model. The model is never changed.' },
      { path: 'routing.retry.capacity-retry.max-attempts', type: 'number', def: 3, zh: '容量重试次数', en: 'Capacity retry attempts', hintZh: '首次失败后的最多重试轮数（默认 3，最大 10）', hintEn: 'Extra rounds after the first failure (default 3, max 10)' },
      { path: 'routing.retry.capacity-retry.base-delay', type: 'text', def: '1s', zh: '容量重试初始退避', en: 'Capacity retry base delay', hintZh: '每次翻倍并加 ±20% 抖动，例如 1s', hintEn: 'Doubles per attempt with ±20% jitter, e.g. 1s' },
      { path: 'routing.retry.capacity-retry.max-delay', type: 'text', def: '8s', zh: '容量重试最长退避', en: 'Capacity retry max delay', hintZh: '上游 RetryInfo 的 retryDelay 会被采纳，但不超过此值', hintEn: 'Upstream RetryInfo retryDelay is honoured up to this cap' },
      { path: 'routing.cooldown.disable-cooling', type: 'bool', zh: '全局禁用冷却', en: 'Disable cooldown globally' },
      { path: 'quota-exceeded.switch-project', type: 'bool', zh: '额度耗尽时切换项目', en: 'Switch project on quota exceeded' },
      { path: 'quota-exceeded.switch-preview-model', type: 'bool', zh: '额度耗尽时切换预览模型', en: 'Switch to preview model on quota exceeded' },
    ],
  },
  {
    zh: 'Antigravity',
    en: 'Antigravity',
    fields: [
      { path: 'oauth.providers.antigravity.antigravity-credits', type: 'bool', zh: '使用 Antigravity credits 作为 Claude 兜底', en: 'Use Antigravity credits as Claude fallback' },
      { path: 'oauth.providers.antigravity.signature-cache-enabled', type: 'bool', zh: '思考签名缓存校验', en: 'Thinking signature cache' },
      { path: 'oauth.providers.antigravity.connection-pool.enabled', type: 'bool', zh: '上游连接池', en: 'Upstream connection pool' },
    ],
  },
  {
    zh: '请求',
    en: 'Requests',
    fields: [
      { path: 'requests.proxy-url', type: 'text', zh: '全局出站代理', en: 'Global outbound proxy', hintZh: 'socks5:// 或 http://，留空为直连', hintEn: 'socks5:// or http://; empty = direct' },
      { path: 'requests.streaming.keepalive-seconds', type: 'number', zh: 'SSE 心跳间隔（秒）', en: 'SSE keepalive (s)' },
      { path: 'requests.streaming.bootstrap-retries', type: 'number', zh: '流式首字节前重试次数', en: 'Streaming bootstrap retries' },
      { path: 'requests.passthrough-headers', type: 'bool', zh: '透传上游响应头', en: 'Pass through upstream headers' },
    ],
  },
  {
    zh: '日志与管理',
    en: 'Logging & management',
    fields: [
      { path: 'observability.logs.request-log', type: 'bool', zh: '详细请求日志', en: 'Detailed request logs', hintZh: '会记录请求/响应内容，注意隐私与容量。', hintEn: 'Records request/response bodies; mind privacy and size.' },
      { path: 'observability.logs.error-body-max-bytes', type: 'number', def: 65536, zh: '错误响应体保存上限（字节）', en: 'Error body cap (bytes)', hintZh: '请求记录里保存的上游错误响应体最大字节数，超出部分截断并标注原始大小。默认 65536（64KB）。', hintEn: 'Max bytes of an upstream error body stored in the request log; larger bodies are truncated and marked. Default 65536 (64KB).' },
      { path: 'observability.logs.debug', type: 'bool', zh: '调试日志', en: 'Debug logging' },
      { path: 'management.allow-remote', type: 'bool', danger: true, zh: '允许远程管理', en: 'Allow remote management', hintZh: '关闭后通过 Ingress 访问的本面板将立即失效（只能 kubectl port-forward 恢复）。', hintEn: 'Turning this off immediately locks this panel out via Ingress (recover with kubectl port-forward).' },
    ],
  },
];

function getPath(obj: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Record<string, unknown>)[k] : undefined), obj);
}

function setPath(obj: Record<string, unknown>, path: string, value: unknown) {
  const keys = path.split('.');
  let cur = obj;
  keys.slice(0, -1).forEach((k) => {
    if (!cur[k] || typeof cur[k] !== 'object') cur[k] = {};
    cur = cur[k] as Record<string, unknown>;
  });
  cur[keys[keys.length - 1]] = value;
}

export function SettingsPage() {
  const { t, tr } = useI18n();
  const [tab, setTab] = useState<'form' | 'yaml'>('form');
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('settings')}</h1>
          <p>{tr('配置保存在 PostgreSQL，修改后所有 Home 副本和代理节点会自动重新加载。', 'Configuration lives in PostgreSQL; all Home replicas and proxies reload automatically.')}</p>
        </div>
      </div>
      <div className="tabs" style={{ marginBottom: 0 }}>
        <button className={tab === 'form' ? 'active' : ''} onClick={() => setTab('form')}>
          {tr('常用设置', 'Common settings')}
        </button>
        <button className={tab === 'yaml' ? 'active' : ''} onClick={() => setTab('yaml')}>
          {tr('完整配置 (YAML)', 'Full config (YAML)')}
        </button>
      </div>
      {tab === 'form' ? <SettingsForm /> : <YamlEditor />}
    </div>
  );
}

function SettingsForm() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const cfg = useAsync(() => api.config(), []);
  const [values, setValues] = useState<Record<string, unknown>>({});
  const initial = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const s of SECTIONS) for (const f of s.fields) out[f.path] = getPath(cfg.data, f.path) ?? f.def;
    return out;
  }, [cfg.data]);
  useEffect(() => setValues(initial), [initial]);

  const changed = Object.keys(values).filter((k) => JSON.stringify(values[k] ?? null) !== JSON.stringify(initial[k] ?? null));

  const save = async () => {
    const patch: Record<string, unknown> = {};
    for (const k of changed) setPath(patch, k, values[k]);
    if (changed.includes('management.allow-remote') && values['management.allow-remote'] === false) {
      const ok = await confirm({ title: tr('关闭远程管理？', 'Disable remote management?'), body: tr('保存后本面板将无法通过 Ingress 访问。', 'This panel will be unreachable via Ingress after saving.'), danger: true, typeToConfirm: 'LOCK' });
      if (!ok) return;
    }
    await run('save', async () => {
      await api.configPatch(patch);
      toast(t('saved'));
      await cfg.reload();
    });
  };

  if (cfg.error) return <ErrorBox error={cfg.error} onRetry={cfg.reload} />;
  if (!cfg.data) return <Loading />;

  return (
    <div className="stack">
      {SECTIONS.map((s) => (
        <Card key={s.en} title={tr(s.zh, s.en)}>
          <div className="form-grid">
            {s.fields.map((f) => {
              const v = values[f.path];
              const label = tr(f.zh, f.en);
              const hint = f.hintZh ? tr(f.hintZh, f.hintEn ?? '') : undefined;
              return (
                <div className="field" key={f.path}>
                  {f.type === 'bool' ? (
                    <Switch checked={!!v} onChange={(nv) => setValues({ ...values, [f.path]: nv })} label={<span style={f.danger ? { color: 'var(--err)' } : undefined}>{label}</span>} />
                  ) : (
                    <>
                      <label>{label}</label>
                      {f.type === 'select' ? (
                        <select className="select" value={(v as string) ?? f.options![0]} onChange={(e) => setValues({ ...values, [f.path]: e.target.value })}>
                          {f.options!.map((o) => (
                            <option key={o}>{o}</option>
                          ))}
                        </select>
                      ) : (
                        <input
                          className="input"
                          type={f.type === 'number' ? 'number' : 'text'}
                          value={v === undefined || v === null ? '' : String(v)}
                          onChange={(e) => setValues({ ...values, [f.path]: f.type === 'number' ? (e.target.value === '' ? undefined : Number(e.target.value)) : e.target.value })}
                        />
                      )}
                    </>
                  )}
                  {hint && <span className="hint">{hint}</span>}
                  <span className="hint mono" style={{ opacity: 0.7 }}>
                    {f.path}
                  </span>
                </div>
              );
            })}
          </div>
        </Card>
      ))}
      <div className="row" style={{ position: 'sticky', bottom: 0, padding: '12px 0', background: 'var(--bg)' }}>
        <button className="btn btn-primary" disabled={!changed.length || busy === 'save'} onClick={save}>
          {busy === 'save' && <Spinner />}
          {t('save')} {changed.length > 0 && `(${changed.length})`}
        </button>
        <button className="btn" disabled={!changed.length} onClick={() => setValues(initial)}>
          {tr('放弃修改', 'Discard')}
        </button>
      </div>
    </div>
  );
}

function YamlEditor() {
  const api = useApi();
  const { t, tr } = useI18n();
  const { toast, confirm } = useUi();
  const { busy, run } = useAction();
  const yaml = useAsync(() => api.configYAML(), []);
  const [text, setText] = useState('');
  useEffect(() => {
    if (yaml.data !== undefined) setText(yaml.data);
  }, [yaml.data]);
  if (yaml.error) return <ErrorBox error={yaml.error} onRetry={yaml.reload} />;
  if (yaml.data === undefined) return <Loading />;
  const dirty = text !== yaml.data;
  return (
    <Card
      title={<span className="faint small">{tr('整份替换：未出现的提供方分组会被清空。修改前建议先复制备份。', 'Full replacement: provider groups missing from the document are cleared. Copy a backup first.')}</span>}
      extra={
        <>
          <button className="btn btn-sm" disabled={!dirty} onClick={() => setText(yaml.data ?? '')}>
            {tr('还原', 'Revert')}
          </button>
          <button
            className="btn btn-sm btn-primary"
            disabled={!dirty || busy === 'save'}
            onClick={async () => {
              if (!(await confirm({ title: tr('替换完整配置？', 'Replace the full configuration?'), danger: true }))) return;
              await run('save', async () => {
                await api.putConfigYAML(text);
                toast(t('saved'));
                await yaml.reload();
              });
            }}
          >
            {busy === 'save' && <Spinner />}
            {t('save')}
          </button>
        </>
      }
    >
      <textarea className="textarea" style={{ minHeight: 'calc(100vh - 300px)' }} spellCheck={false} value={text} onChange={(e) => setText(e.target.value)} />
    </Card>
  );
}
